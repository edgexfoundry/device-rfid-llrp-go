//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

//go:generate python3 generate_param_code.py -i messages.yaml -o generated_unmarshal.go
//go:generate stringer -type=ParamType,ConnectionAttemptEventType,StatusCode

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
	paramInvalid = ParamType(0)

	// TV-encoded params

	ParamAntennaID                 = ParamType(1)
	ParamFirstSeenUTC              = ParamType(2)
	ParamFirstSeenUptime           = ParamType(3)
	ParamLastSeenUTC               = ParamType(4)
	ParamLastSeenUptime            = ParamType(5)
	ParamPeakRSSI                  = ParamType(6)
	ParamChannelIndex              = ParamType(7)
	ParamTagSeenCount              = ParamType(8)
	ParamROSpecID                  = ParamType(9)
	ParamInventoryParameterSpecID  = ParamType(10)
	ParamC1G2CRC                   = ParamType(11)
	ParamC1G2PC                    = ParamType(12)
	ParamEPC96                     = ParamType(13)
	ParamSpecIndex                 = ParamType(14)
	ParamClientRequestOpSpecResult = ParamType(15)
	ParamAccessSpecID              = ParamType(16)
	ParamOpSpecID                  = ParamType(17)
	ParamC1G2SingulationDetails    = ParamType(18)
	ParamC1G2XPCW1                 = ParamType(19)
	ParamC1G2XPCW2                 = ParamType(20)

	// TLV encoded parameters

	ParamUTCTimestamp                      = ParamType(128)
	ParamUptime                            = ParamType(129)
	ParamGeneralDeviceCapabilities         = ParamType(137)
	ParamReceiveSensitivityTableEntry      = ParamType(139)
	ParamPerAntennaAirProtocol             = ParamType(140)
	ParamGPIOCapabilities                  = ParamType(141)
	ParamLLRPCapabilities                  = ParamType(142)
	ParamRegulatoryCapabilities            = ParamType(143)
	ParamUHFBandCapabilities               = ParamType(144)
	ParamTransmitPowerLevelTableEntry      = ParamType(145)
	ParamFrequencyInformation              = ParamType(146)
	ParamFrequencyHopTable                 = ParamType(147)
	ParamFixedFrequencyTable               = ParamType(148)
	ParamPerAntennaReceiveSensitivityRange = ParamType(149)
	ParamROSpec                            = ParamType(177)
	ParamROBoundarySpec                    = ParamType(178)
	ParamROSpecStartTrigger                = ParamType(179)
	ParamPeriodicTriggerValue              = ParamType(180)
	ParamGPITriggerValue                   = ParamType(181)
	ParamROSpecStopTrigger                 = ParamType(182)
	ParamAISpec                            = ParamType(183)
	ParamAISpecStopTrigger                 = ParamType(184)
	ParamTagObservationTrigger             = ParamType(185)
	ParamInventoryParameterSpec            = ParamType(186)
	ParamRFSurveySpec                      = ParamType(187)
	ParamRFSurveySpecStopTrigger           = ParamType(188)
	ParamAccessSpec                        = ParamType(207)
	ParamAccessSpecStopTrigger             = ParamType(208)
	ParamAccessCommand                     = ParamType(209)
	ParamClientRequestOpSpec               = ParamType(210)
	ParamClientRequestResponse             = ParamType(211)
	ParamLLRPConfigurationStateValue       = ParamType(217)
	ParamIdentification                    = ParamType(218)
	ParamGPOWriteData                      = ParamType(219)
	ParamKeepAliveSpec                     = ParamType(220)
	ParamAntennaProperties                 = ParamType(221)
	ParamAntennaConfiguration              = ParamType(222)
	ParamRFReceiver                        = ParamType(223)
	ParamRFTransmitter                     = ParamType(224)
	ParamGPIPortCurrentState               = ParamType(225)
	ParamEventsAndReports                  = ParamType(226)
	ParamROReportSpec                      = ParamType(237)
	ParamTagReportContentSelector          = ParamType(238)
	ParamAccessReportSpec                  = ParamType(239)
	ParamTagReportData                     = ParamType(240)
	ParamEPCData                           = ParamType(241)
	ParamRFSurveyReportData                = ParamType(242)
	ParamFrequencyRSSILevelEntry           = ParamType(243)
	ParamReaderEventNotificationSpec       = ParamType(244)
	ParamEventNotificationState            = ParamType(245)
	ParamReaderEventNotificationData       = ParamType(246)
	ParamHoppingEvent                      = ParamType(247)
	ParamGPIEvent                          = ParamType(248)
	ParamROSpecEvent                       = ParamType(249)
	ParamReportBufferLevelWarningEvent     = ParamType(250)
	ParamReportBufferOverflowErrorEvent    = ParamType(251)
	ParamReaderExceptionEvent              = ParamType(252)
	ParamRFSurveyEvent                     = ParamType(253)
	ParamAISpecEvent                       = ParamType(254)
	ParamAntennaEvent                      = ParamType(255)
	ParamConnectionAttemptEvent            = ParamType(256)
	ParamConnectionCloseEvent              = ParamType(257)
	ParamLLRPStatus                        = ParamType(287)
	ParamFieldError                        = ParamType(288)
	ParamParameterError                    = ParamType(289)
	ParamLoopSpec                          = ParamType(355)
	ParamSpecLoopEvent                     = ParamType(356)
	ParamCustom                            = ParamType(1023)

	ParamC1G2LLRPCapabilities                        = ParamType(327)
	ParamUHFC1G2RFModeTable                          = ParamType(328)
	ParamUHFC1G2RFModeTableEntry                     = ParamType(329)
	ParamC1G2InventoryCommand                        = ParamType(330)
	ParamC1G2Filter                                  = ParamType(331)
	ParamC1G2TagInventoryMask                        = ParamType(332)
	ParamC1G2TagInventoryStateAwareFilterAction      = ParamType(333)
	ParamC1G2TagInventoryStateUnawareFilterAction    = ParamType(334)
	ParamC1G2RFControl                               = ParamType(335)
	ParamC1G2SingulationControl                      = ParamType(336)
	ParamC1G2TagInventoryStateAwareSingulationAction = ParamType(337)
	ParamC1G2TagSpec                                 = ParamType(338)
	ParamC1G2TargetTag                               = ParamType(339)
	ParamC1G2Read                                    = ParamType(341)
	ParamC1G2Write                                   = ParamType(342)
	ParamC1G2Kill                                    = ParamType(343)
	ParamC1G2Lock                                    = ParamType(344)
	ParamC1G2LockPayload                             = ParamType(345)
	ParamC1G2BlockErase                              = ParamType(346)
	ParamC1G2BlockWrite                              = ParamType(347)
	ParamC1G2EPCMemorySelector                       = ParamType(348)
	ParamC1G2ReadOpSpecResult                        = ParamType(349)
	ParamC1G2WriteOpSpecResult                       = ParamType(350)
	ParamC1G2KillOpSpecResult                        = ParamType(351)
	ParamC1G2LockOpSpecResult                        = ParamType(352)
	ParamC1G2BlockEraseOpSpecResult                  = ParamType(353)
	ParamC1G2BlockWriteOpSpecResult                  = ParamType(354)
	ParamC1G2Recommission                            = ParamType(357)
	ParamC1G2BlockPermalock                          = ParamType(358)
	ParamC1G2GetBlockPermalockStatus                 = ParamType(359)
	ParamC1G2RecommissionOpSpecResult                = ParamType(360)
	ParamC1G2BlockPermalockOpSpecResult              = ParamType(361)
	ParamC1G2GetBlockPermalockStatusOpSpecResult     = ParamType(362)

	ParamMaximumReceiveSensitivity     = ParamType(363)
	ParamRFSurveyFrequencyCapabilities = ParamType(365)

	paramResvStart = ParamType(900)
	paramResvEnd   = ParamType(999)

	tlvHeaderSz = 4
	maxParamSz  = uint16(1<<16 - 1)
)

// tvLengths to byte lengths (not including the 1-byte type itself)
var tvLengths = map[ParamType]int{
	ParamEPC96:                     12,
	ParamROSpecID:                  4,
	ParamSpecIndex:                 2,
	ParamInventoryParameterSpecID:  2,
	ParamAntennaID:                 2,
	ParamPeakRSSI:                  1,
	ParamChannelIndex:              2,
	ParamFirstSeenUTC:              8,
	ParamFirstSeenUptime:           8,
	ParamLastSeenUTC:               8,
	ParamLastSeenUptime:            8,
	ParamTagSeenCount:              2,
	ParamClientRequestOpSpecResult: 2,
	ParamAccessSpecID:              4,
	ParamOpSpecID:                  4,
	ParamC1G2PC:                    2,
	ParamC1G2XPCW1:                 2,
	ParamC1G2XPCW2:                 2,
	ParamC1G2CRC:                   2,
	ParamC1G2SingulationDetails:    4,
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

const (
	statusMsgStart    = StatusMsgParamError
	statusMsgEnd      = StatusMsgMsgUnexpected
	statusParamStart  = StatusParamParamError
	statusParamEnd    = StatusParamParamUnsupported
	statusFieldStart  = StatusFieldInvalid
	statusFieldEnd    = StatusFieldOutOfRange
	statusDeviceStart = StatusDeviceError
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

func (fe fieldError) Error() string {
	return fe.ErrorCode.defaultText() + " at index " + strconv.Itoa(int(fe.FieldIndex))
}

// Error constructs a string from the parameter's error.
func (pe *parameterError) Error() string {
	msg := pe.ErrorCode.defaultText() + " " + pe.ParameterType.String()
	if pe.ParameterType.isValid() {
		msg += " (type " + strconv.Itoa(int(pe.ParameterType)) + ")"
	}

	if pe.FieldError != nil {
		msg += ": " + pe.FieldError.Error()
	}

	if pe.ParameterError != nil {
		msg += ": " + pe.ParameterError.Error()
	}

	return msg
}

// Err returns an error represented by this LLRPStatus, if any.
// If the Status is Success, this returns nil.
func (ls *llrpStatus) Err() error {
	if ls.Status == StatusSuccess {
		return nil
	}

	msg := ls.Status.defaultText()
	if ls.ErrorDescription != "" {
		msg += ": " + ls.ErrorDescription
	}

	if ls.FieldError != nil {
		msg += ": " + ls.FieldError.Error()
	}

	if ls.ParameterError != nil {
		msg += ": " + ls.ParameterError.Error()
	}

	return errors.New(msg)
}

func (*llrpStatus) getType() ParamType {
	return ParamLLRPStatus
}

func (fe *parameterError) getType() ParamType {
	return ParamParameterError
}

func (fe *fieldError) getType() ParamType {
	return ParamFieldError
}

func (ls *llrpStatus) encode(mb *MsgBuilder) error {
	if err := mb.WriteFields(uint16(ls.Status), ls.ErrorDescription); err != nil {
		return err
	}

	if ls.FieldError != nil {
		if err := mb.writeParam(ls.FieldError); err != nil {
			return err
		}
	}

	if ls.ParameterError != nil {
		if err := mb.writeParam(ls.ParameterError); err != nil {
			return err
		}
	}

	return nil
}

func (fe *fieldError) encode(mb *MsgBuilder) error {
	return mb.WriteFields(fe.FieldIndex, fe.ErrorCode)
}

func (fe *fieldError) decode(mr *MsgReader) error {
	return mr.ReadFields(&(fe.FieldIndex), (*uint16)(&(fe.ErrorCode)))
}

func (pe *parameterError) encode(mb *MsgBuilder) error {
	if err := mb.WriteFields(uint16(pe.ParameterType), uint16(pe.ErrorCode)); err != nil {
		return err
	}

	if pe.FieldError != nil {
		if err := mb.writeParam(pe.FieldError); err != nil {
			return err
		}
	}

	if pe.ParameterError != nil {
		if err := mb.writeParam(pe.ParameterError); err != nil {
			return err
		}
	}

	return nil
}

func (pe *parameterError) decode(mr *MsgReader) error {
	if err := mr.ReadFields((*uint16)(&(pe.ParameterType)), (*uint16)(&(pe.ErrorCode))); err != nil {
		return err
	}

	for mr.hasData() {
		prev, err := mr.readParam()
		if err != nil {
			return err
		}

		switch mr.cur.typ {
		case ParamFieldError:
			pe.FieldError = new(fieldError)
			if err := mr.read(pe.FieldError); err != nil {
				return err
			}
		case ParamParameterError:
			pe.ParameterError = new(parameterError)
			if err := mr.read(pe.ParameterError); err != nil {
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

func (s *llrpStatus) decode(mr *MsgReader) error {
	if err := mr.ReadFields((*uint16)(&(s.Status)), &(s.ErrorDescription)); err != nil {
		return err
	}

	for mr.hasData() {
		prev, err := mr.readParam()
		if err != nil {
			return err
		}

		switch mr.cur.typ {
		case ParamFieldError:
			f := &fieldError{}
			if err := mr.read(f); err != nil {
				return err
			}
			s.FieldError = f
		case ParamParameterError:
			p := &parameterError{}
			if err := mr.read(p); err != nil {
				return err
			}
			s.ParameterError = p
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

func (sv *getSupportedVersionResponse) decode(mr *MsgReader) error {
	if err := mr.ReadFields((*uint8)(&(sv.CurrentVersion)), (*uint8)(&(sv.MaxSupportedVersion))); err != nil {
		return err
	}
	return mr.readParameter(&sv.LLRPStatus)
}

func (ren *readerEventNotification) decode(mr *MsgReader) error {
	return mr.ReadParameters(&ren.ReaderEventNotificationData)
}

func (ren *readerEventNotification) encode(mb *MsgBuilder) error {
	return mb.writeParam(&ren.ReaderEventNotificationData)
}

func (rend *readerEventNotificationData) encode(mb *MsgBuilder) error {
	return mb.WriteParams(&rend.UTCTimestamp, rend.ConnectionAttemptEvent)
}

func (utcTS *utcTimestamp) encode(mb *MsgBuilder) error {
	return mb.write(uint64(*utcTS))
}

func (cae *connectionAttemptEvent) encode(mb *MsgBuilder) error {
	return mb.write(uint16(*cae))
}

func (*utcTimestamp) getType() ParamType {
	return ParamUTCTimestamp
}

func (*connectionAttemptEvent) getType() ParamType {
	return ParamConnectionAttemptEvent
}

func (*readerEventNotificationData) getType() ParamType {
	return ParamReaderEventNotificationData
}

func (ren *readerEventNotificationData) decode(mr *MsgReader) error {
	prev, err := mr.readParam()
	if err != nil {
		return err
	}
	if err := mr.read(&ren.UTCTimestamp); err != nil {
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
			if err := mr.read(ren.ConnectionAttemptEvent); err != nil {
				return err
			}
		}

		if err := mr.endParam(prev); err != nil {
			return err
		}
	}

	return nil
}

func (ren *readerEventNotification) isConnectSuccess() bool {
	if ren == nil || ren.ReaderEventNotificationData.ConnectionAttemptEvent == nil {
		return false
	}

	return ConnSuccess == ConnectionAttemptEventType(*ren.ReaderEventNotificationData.ConnectionAttemptEvent)
}

func (gdc *generalDeviceCapabilities) decode(mr *MsgReader) error {
	if err := mr.ReadFields(&gdc.MaxSupportedAntennas,
		(*uint8)(&gdc.GeneralCapabilityFlags),
		&gdc.DeviceManufacturerName,
		&gdc.ModelName,
		&gdc.ReaderFirmwareVersion); err != nil {
		return err
	}

	for mr.hasData() {
		prev, err := mr.readParam()
		if err != nil {
			return err
		}

		switch mr.cur.typ {
		case ParamReceiveSensitivityTableEntry:
			rste := receiveSensitivityTableEntry{}
			if err := mr.read(&rste); err != nil {
				return err
			}
			gdc.ReceiveSensitivityTableEntries = append(gdc.ReceiveSensitivityTableEntries, rste)
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
	microseconds microSecs64
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
