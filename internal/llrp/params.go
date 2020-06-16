//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

//go:generate stringer -type=ParamType,ConnectionStatus,StatusCode

package llrp

import (
	"fmt"
	"github.com/pkg/errors"
	"strconv"
	"time"
)

// ParamType is an 8 or 10 bit value identifying
// both the encoding and content of an LLRP Parameter.
//
//
// An LLRP message consists of a header identifying its type and size,
// followed by 0 or more data fields.
// Some of these fields have a fixed size and offset (relative the header),
// but others are optional and/or variable length,
// so they start with a parameter header to identify their type and (usually) size.
// In some cases, a parameter's header only identifies its type,
// in which case its size must be determined by a lookup table.
//
// Most LLRP Parameters are TLVs.
// Their headers are 4 bytes:
// 6 reserved bits (zero), a 10 bit type, and 16 bit of length (incl. header).
// The type field of a TLV is specified as 128-2047,
// though it's not clear how types 1024-2047 fit in 10 bits.
// TV parameters can (in theory) be as short as 1 byte.
// The MSBit is always 1, then the next 7 are the type: 1-127
// (so in a certain sense, they range 128-255).
type ParamType uint16

const (
	ParamLLRPStatus                  = ParamType(287)
	ParamFieldError                  = ParamType(288)
	ParamParameterError              = ParamType(289)
	ParamReaderEventNotificationData = ParamType(246)
	ParamUTCTimestamp                = ParamType(128)
	ParamUptime                      = ParamType(129)
	ParamHoppingEvent                = ParamType(247)
	ParamGPIEvent                    = ParamType(248)
	ParamROSpecEvent                 = ParamType(249)
	ParamBufferLevelWarnEvent        = ParamType(250)
	ParamBufferOverflowEvent         = ParamType(251)
	ParamReaderExceptionEvent        = ParamType(252)
	ParamRFSurveyEvent               = ParamType(253)
	ParamAISpecEvent                 = ParamType(254)
	ParamAntennaEvent                = ParamType(255)
	ParamConnectionAttemptEvent      = ParamType(256)
	ParamConnectionCloseEvent        = ParamType(257)
	ParamSpecLoopEvent               = ParamType(365)
	ParamCustomParameter             = ParamType(1023)

	// TV-encoded params
	ParamEPC96                  = ParamType(13)
	ParamROSpectID              = ParamType(9)
	ParamSpecIndex              = ParamType(14)
	ParamInventoryParamSpecID   = ParamType(10)
	ParamAntennaID              = ParamType(1)
	ParamPeakRSSI               = ParamType(6)
	ParamChannelIndex           = ParamType(7)
	ParamFirstSeenUTC           = ParamType(2)
	ParamFirstSeenUptime        = ParamType(3)
	ParamLastSeenUTC            = ParamType(4)
	ParamLastSeenUptime         = ParamType(5)
	ParamTagSeenCount           = ParamType(8)
	ParamClientReqOpSpecResult  = ParamType(15)
	ParamAccessSpecID           = ParamType(16)
	ParamOpSpecID               = ParamType(17)
	ParamC1G2PC                 = ParamType(12)
	ParamC1G2XPCW1              = ParamType(19)
	ParamC1G2XPCW2              = ParamType(20)
	ParamC1G2CRC                = ParamType(11)
	ParamC1G2SingulationDetails = ParamType(18)

	paramInvalid   = ParamType(0)
	paramResvStart = ParamType(900)
	paramResvEnd   = ParamType(999)

	tlvHeaderSz = 4
	maxParamSz  = uint16(1<<16 - 1)
)

// tvLengths to byte lengths (not including the 1-byte type itself)
var tvLengths = map[ParamType]int{
	ParamEPC96:                  12,
	ParamROSpectID:              4,
	ParamSpecIndex:              2,
	ParamInventoryParamSpecID:   2,
	ParamAntennaID:              2,
	ParamPeakRSSI:               1,
	ParamChannelIndex:           2,
	ParamFirstSeenUTC:           8,
	ParamFirstSeenUptime:        8,
	ParamLastSeenUTC:            8,
	ParamLastSeenUptime:         8,
	ParamTagSeenCount:           2,
	ParamClientReqOpSpecResult:  2,
	ParamAccessSpecID:           4,
	ParamOpSpecID:               4,
	ParamC1G2PC:                 2,
	ParamC1G2XPCW1:              2,
	ParamC1G2XPCW2:              2,
	ParamC1G2CRC:                2,
	ParamC1G2SingulationDetails: 4,
}

// isTV returns true if the ParamType is TV-encoded.
// TV-encoded parameters have specific lengths which must be looked up.
func (pt ParamType) isTV() bool {
	return pt != 0 && pt <= 127
}

// isTLV returns true if the ParamType is TLV-encoded.
// TLV-encoded parameters include their length in their headers.
func (pt ParamType) isTLV() bool {
	return pt >= 128 && pt <= 2047
}

// isValid returns true if the ParamType is within the valid LLRP Parameter range
// and not one of the reserved parameter types (900-999)
func (pt ParamType) isValid() bool {
	return 0 < pt && pt <= 2047 && !(paramResvStart <= pt && pt <= paramResvEnd)
}

// parameter keeps track of basic parameter data within this package.
// This does not necessarily map directly to an LLRP header.
//
// Parameters are fields which "declare" themselves within an LLRP message,
// in the sense that they start with some type information
// rather than just existing at a certain offset with a certain size.
// They typically include their length, but sometimes length must be known apriori.
//
// This struct is used when reading or building a parameter,
// usually to track its length and allow for better error messages.
type parameter struct {
	typ    ParamType // TVs are 1-127; TLVs are 128-2047
	length uint16
}

func (ph parameter) String() string {
	if ph.typ.isTLV() {
		return fmt.Sprintf("%v (TLV %[1]d/%#02x, %d bytes)",
			ph.typ, uint16(ph.typ), ph.length)
	} else {
		return fmt.Sprintf("%v (TV %[1]d/%#02x, %d bytes)",
			ph.typ, uint16(ph.typ), ph.length)
	}
}

// ConnectionStatus reports the status field of a ConnectionAttemptEvent (TLV 256).
type ConnectionStatus uint16

const (
	ConnectedSuccessfully     = ConnectionStatus(0)
	ConnExistsReaderInitiated = ConnectionStatus(1)
	ConnExistsClientInitiated = ConnectionStatus(2)
	ConnFailedReasonUnknown   = ConnectionStatus(3)
	ConnAttemptedAgain        = ConnectionStatus(4)
)

func (cs ConnectionStatus) encode(mb *MsgBuilder) error {
	return mb.writeUint(uint64(cs), 2)
}

// StatusCode matches LLRP's Status Codes.
//
// These are described in Section 14 of the Low Level Reader Protocol v1.0.1
// and in Section 15 of Low Level Reader Protocol v1.1.
type StatusCode uint16

const (
	StatusSuccess = StatusCode(0) // message was received and processed successfully

	statusMsgStart            = StatusMsgParamError
	StatusMsgParamError       = StatusCode(100)
	StatusMsgFieldError       = StatusCode(101)
	StatusMsgParamUnexpected  = StatusCode(102)
	StatusMsgParamMissing     = StatusCode(103)
	StatusMsgParamDuplicate   = StatusCode(104)
	StatusMsgParamOverflow    = StatusCode(105)
	StatusMsgFieldOverflow    = StatusCode(106)
	StatusMsgParamUnknown     = StatusCode(107)
	StatusMsgFieldUnknown     = StatusCode(108)
	StatusMsgMsgUnsupported   = StatusCode(109)
	StatusMsgVerUnsupported   = StatusCode(110)
	StatusMsgParamUnsupported = StatusCode(111)
	StatusMsgMsgUnexpected    = StatusCode(112) // version 1.1 only
	statusMsgEnd              = StatusMsgMsgUnexpected

	statusParamStart            = StatusParamParamError
	StatusParamParamError       = StatusCode(200) // some sub-parameter of the parameter is in error
	StatusParamFieldError       = StatusCode(201) // some field in the parameter is in error
	StatusParamParamUnexpected  = StatusCode(202) // sub-parameter not expected
	StatusParamParamMissing     = StatusCode(203) // sub-parameter not expected
	StatusParamParamDuplicate   = StatusCode(204) // sub-parameter not expected
	StatusParamParamOverflow    = StatusCode(205) // sub-parameter not expected
	StatusParamFieldOverflow    = StatusCode(206) // sub-parameter not expected
	StatusParamParamUnknown     = StatusCode(207) // sub-parameter not expected
	StatusParamFieldUnknown     = StatusCode(208) // sub-parameter not expected
	StatusParamParamUnsupported = StatusCode(209) // sub-parameter not expected
	statusParamEnd              = StatusParamParamUnsupported

	statusFieldStart      = StatusFieldInvalid
	StatusFieldInvalid    = StatusCode(300)
	StatusFieldOutOfRange = StatusCode(301)
	statusFieldEnd        = StatusFieldOutOfRange

	statusDeviceStart = StatusDeviceError
	StatusDeviceError = StatusCode(401)
	statusDeviceEnd   = StatusDeviceError
)

func (sc StatusCode) isMsgStatus() bool {
	return statusMsgStart <= sc && sc <= statusMsgEnd
}

func (sc StatusCode) isParamStatus() bool {
	return statusParamStart <= sc && sc <= statusParamEnd
}

func (sc StatusCode) isFieldStatus() bool {
	return statusFieldStart <= sc && sc <= statusFieldEnd
}

func (sc StatusCode) isDeviceStatus() bool {
	return statusDeviceStart <= sc && sc <= statusDeviceEnd
}

func (sc StatusCode) defaultText() string {
	switch {
	case sc == StatusSuccess:
		return "success"
	case sc.isMsgStatus():
		return statusMsgErrs[sc-statusMsgStart]
	case sc.isParamStatus():
		return statusParamErrs[sc-statusParamStart]
	case sc.isFieldStatus():
		return statusFieldErrs[sc-statusFieldStart]
	case sc.isDeviceStatus():
		return statusDeviceErrs[sc-statusDeviceStart]
	}

	return "unknown LLRP status code " + strconv.Itoa(int(sc))
}

var statusMsgErrs = [...]string{
	StatusMsgParamError - statusMsgStart:       "message parameter error",
	StatusMsgFieldError - statusMsgStart:       "message field error",
	StatusMsgParamUnexpected - statusMsgStart:  "message parameter unexpected",
	StatusMsgParamMissing - statusMsgStart:     "message parameter missing",
	StatusMsgParamDuplicate - statusMsgStart:   "message has duplicate parameter",
	StatusMsgParamOverflow - statusMsgStart:    "message parameter instances exceed Reader max",
	StatusMsgFieldOverflow - statusMsgStart:    "message field instances exceed Reader max",
	StatusMsgParamUnknown - statusMsgStart:     "message parameter unknown",
	StatusMsgFieldUnknown - statusMsgStart:     "message field unknown",
	StatusMsgMsgUnsupported - statusMsgStart:   "message type unsupported",
	StatusMsgVerUnsupported - statusMsgStart:   "LLRP version not supported",
	StatusMsgParamUnsupported - statusMsgStart: "message parameter unsupported",
	StatusMsgMsgUnexpected - statusMsgStart:    "unexpected message type",
}
var _ = statusMsgErrs[statusMsgEnd-statusMsgStart] // compile error == missing message

var statusParamErrs = [...]string{
	StatusParamParamError - statusParamStart:       "error in sub-parameter",
	StatusParamFieldError - statusParamStart:       "error in parameter field",
	StatusParamParamUnexpected - statusParamStart:  "unexpected sub-parameter",
	StatusParamParamMissing - statusParamStart:     "missing required sub-parameter",
	StatusParamParamDuplicate - statusParamStart:   "duplicate sub-parameter",
	StatusParamParamOverflow - statusParamStart:    "sub-parameter instances exceed Reader max",
	StatusParamFieldOverflow - statusParamStart:    "parameter field instances exceeds Reader max",
	StatusParamParamUnknown - statusParamStart:     "unknown sub-parameter",
	StatusParamFieldUnknown - statusParamStart:     "unknown parameter field",
	StatusParamParamUnsupported - statusParamStart: "unsupported sub-parameter",
}
var _ = statusParamErrs[statusParamEnd-statusParamStart] // compile error == missing message

var statusFieldErrs = [...]string{
	StatusFieldInvalid - statusFieldStart:    "invalid value",
	StatusFieldOutOfRange - statusFieldStart: "value out of range",
}
var _ = statusFieldErrs[statusFieldEnd-statusFieldStart] // compile error == missing message

var statusDeviceErrs = [...]string{
	StatusDeviceError - statusDeviceEnd: "the Reader encountered a problem unrelated to the message",
}
var _ = statusDeviceErrs[statusDeviceEnd-statusDeviceStart] // compile error == missing message

func (sc StatusCode) encode(mb *MsgBuilder) error {
	return mb.writeUint(uint64(sc), 2)
}

type FieldError struct {
	FieldNum  uint16
	ErrorCode StatusCode
}

func (fe FieldError) Error() string {
	return fe.ErrorCode.defaultText() + " at index " + strconv.Itoa(int(fe.FieldNum))
}

type ParamError struct {
	ParamType ParamType
	ErrorCode StatusCode
	*FieldError
	*ParamError
}

// Error constructs a string from the parameter's error.
func (pe *ParamError) Error() string {
	msg := pe.ErrorCode.defaultText() + " " + pe.ParamType.String()
	if pe.ParamType.isValid() {
		msg += " (type " + strconv.Itoa(int(pe.ParamType)) + ")"
	}

	if pe.FieldError != nil {
		msg += ": " + pe.FieldError.Error()
	}

	if pe.ParamError != nil {
		msg += ": " + pe.ParamError.Error()
	}

	return msg
}

// LLRPStatus holds information about the result of a command.
type LLRPStatus struct {
	ErrDescription string
	FieldErr       *FieldError
	ParamErr       *ParamError
	Code           StatusCode
}

// Err returns an error represented by this LLRPStatus, if any.
// If the Status is Success, this returns nil.
func (ls *LLRPStatus) Err() error {
	if ls.Code == StatusSuccess {
		return nil
	}

	msg := ls.Code.defaultText()
	if ls.ErrDescription != "" {
		msg += ": " + ls.ErrDescription
	}

	if ls.FieldErr != nil {
		msg += ": " + ls.FieldErr.Error()
	}

	if ls.ParamErr != nil {
		msg += ": " + ls.ParamErr.Error()
	}

	return errors.New(msg)
}

func (*LLRPStatus) getType() ParamType {
	return ParamLLRPStatus
}

func (ls *LLRPStatus) encode(mb *MsgBuilder) error {
	if err := mb.WriteFields(uint16(ls.Code), ls.ErrDescription); err != nil {
		return err
	}

	if ls.FieldErr != nil {
		if err := mb.writeParam(ls.FieldErr); err != nil {
			return err
		}
	}

	if ls.ParamErr != nil {
		if err := mb.writeParam(ls.ParamErr); err != nil {
			return err
		}
	}

	return nil
}

func (*FieldError) getType() ParamType {
	return ParamFieldError
}

func (fe *FieldError) encode(mb *MsgBuilder) error {
	return mb.WriteFields(fe.FieldNum, fe.ErrorCode)
}

func (fe *FieldError) decode(mr *MsgReader) error {
	return mr.ReadFields(&(fe.FieldNum), (*uint16)(&(fe.ErrorCode)))
}

func (*ParamError) getType() ParamType {
	return ParamParameterError
}

func (pe *ParamError) encode(mb *MsgBuilder) error {
	if err := mb.WriteFields(uint16(pe.ParamType), uint16(pe.ErrorCode)); err != nil {
		return err
	}

	if pe.FieldError != nil {
		if err := mb.writeParam(pe.FieldError); err != nil {
			return err
		}
	}

	if pe.ParamError != nil {
		if err := mb.writeParam(pe.ParamError); err != nil {
			return err
		}
	}

	return nil
}

func (pe *ParamError) decode(mr *MsgReader) error {
	if err := mr.ReadFields((*uint16)(&(pe.ParamType)), (*uint16)(&(pe.ErrorCode))); err != nil {
		return err
	}

	for mr.hasData() {
		prev, err := mr.readParam()
		if err != nil {
			return err
		}

		switch mr.cur.typ {
		case ParamFieldError:
			pe.FieldError = new(FieldError)
			if err := mr.read(pe.FieldError); err != nil {
				return err
			}
		case ParamParameterError:
			pe.ParamError = new(ParamError)
			if err := mr.read(pe.ParamError); err != nil {
				return err
			}
		default:
			return errors.Errorf("expected either %v or %v, but found %v",
				ParamFieldError, ParamParameterError, mr.cur)
		}

		if err := mr.endParam(prev); err != nil {
			return err
		}
	}

	return nil
}

func (s *LLRPStatus) decode(mr *MsgReader) error {
	if err := mr.ReadFields((*uint16)(&(s.Code)), &(s.ErrDescription)); err != nil {
		return err
	}

	for mr.hasData() {
		prev, err := mr.readParam()
		if err != nil {
			return err
		}

		switch mr.cur.typ {
		case ParamFieldError:
			f := &FieldError{}
			if err := mr.read(f); err != nil {
				return err
			}
			s.FieldErr = f
		case ParamParameterError:
			p := &ParamError{}
			if err := mr.read(p); err != nil {
				return err
			}
			s.ParamErr = p
		default:
			return errors.Errorf("expected either %v or %v, but found %v",
				ParamFieldError, ParamParameterError, mr.cur)
		}

		if err := mr.endParam(prev); err != nil {
			return err
		}
	}

	return nil
}

// SupportedVersion holds a GetSupportedVersionResponse.
type SupportedVersion struct {
	LLRPStatus
	Current   VersionNum
	Supported VersionNum
}

func (sv *SupportedVersion) decode(mr *MsgReader) error {
	if err := mr.ReadFields((*uint8)(&(sv.Current)), (*uint8)(&(sv.Supported))); err != nil {
		return err
	}
	return mr.readParameter(&sv.LLRPStatus)
}

type readerEventNotification struct {
	NotificationData readerEventNotificationData
}

func (ren *readerEventNotification) decode(mr *MsgReader) error {
	return mr.ReadParameters(&ren.NotificationData)
}

func (ren *readerEventNotification) encode(mb *MsgBuilder) error {
	return mb.writeParam(&ren.NotificationData)
}

// TLV type 246
type readerEventNotificationData struct {
	TS                timestamp
	ConnectionAttempt ConnectionStatus
}

func (rend *readerEventNotificationData) encode(mb *MsgBuilder) error {
	return mb.WriteParams(&rend.TS, &rend.ConnectionAttempt)
}

func (*readerEventNotificationData) getType() ParamType {
	return ParamReaderEventNotificationData
}

func (*ConnectionStatus) getType() ParamType {
	return ParamConnectionAttemptEvent
}

func (ren *readerEventNotificationData) decode(mr *MsgReader) error {
	prev, err := mr.readParam()
	if err != nil {
		return err
	}
	if err := mr.read(&ren.TS); err != nil {
		return err
	}
	if err := mr.endParam(prev); err != nil {
		return err
	}

	for mr.hasData() {
		prev, err := mr.readParam()
		if err != nil {
			return err
		}

		switch mr.cur.typ {
		case ParamConnectionAttemptEvent:
			if err := mr.read((*uint16)(&ren.ConnectionAttempt)); err != nil {
				return err
			}
		}

		if err := mr.endParam(prev); err != nil {
			return err
		}
	}

	return nil
}

// TLV type 128 == UTC Timestamp, microseconds since 00:00:00 UTC, Jan 1, 1970.
// TLV type 129 == Uptime Timestamp, microseconds since reader started.
type timestamp struct {
	microseconds uint64
	isUptime     bool // true if microseconds is uptime instead of offset since unix epoch
}

// toGoTime returns the timestamp as a Go time.Time type,
// assuming that it's a UTC timestamp.
// If it isn't, then the caller should add the reader's start time.
func (ts timestamp) toGoTime() time.Time {
	// todo: extends this sometime in the next few hundred years
	return time.Unix(0, int64(ts.microseconds*1000))
}

func (*timestamp) fromGoTime(t time.Time) timestamp {
	return timestamp{
		microseconds: uint64(time.Duration(t.UnixNano()).Microseconds()),
	}
}

func (ts timestamp) String() string {
	if ts.isUptime {
		return time.Duration(ts.microseconds).String() + " since reader start"
	}
	return ts.toGoTime().String()
}

func utcCurrent() timestamp {
	return timestamp{
		microseconds: uint64(time.Duration(time.Now().UnixNano()).Microseconds()),
	}
}

func (ts *timestamp) getType() ParamType {
	if ts.isUptime {
		return ParamUptime
	}
	return ParamUTCTimestamp
}

func (ts *timestamp) decode(mr *MsgReader) error {
	switch mr.cur.typ {
	case ParamUTCTimestamp:
		ts.isUptime = false
		return mr.read(&ts.microseconds)
	case ParamUptime:
		ts.isUptime = true
		return mr.read(&ts.microseconds)
	default:
		return errors.Errorf("expected %v or %v, but got %v",
			ParamUptime, ParamUTCTimestamp, mr.cur.typ)
	}
}

func (ts *timestamp) encode(mb *MsgBuilder) error {
	return mb.writeUint(ts.microseconds, 8)
}
