//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package llrp

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"github.com/pkg/errors"
	"io"
	"strings"
	"time"
)

// paramType is an 8 or 10 bit value identifying
// both the encoding and content of an LLRP Parameter.
type paramType uint16

// A TLV-encoded parameter must be at least 4 bytes:
// 6 reserved bits (zero), 10 bits to indicate the type, and 16 bits of length.
// The type field of a TLV is specified as 128-2047,
// though it's not clear how types 1024-2047 fit in 10 bits.
// The initial 6 bits of the 2-byte field containing the paramType must be zeros.
// EPCglobal indicates that TLV-encoded parameters are 128-2047,
// though it's not clear how they fit types 1024-2047 in 10 bits.
//
// TV parameters can (in theory) be as short as 1 byte.
// The MSBit is always 1, then the next 7 are the type: 1-127
// (so in a certain sense, they range 128-255).
//
// The TV vs TLV encoding is designed for more efficient message packing,
// though parameter fields always start with TLV,
// wherein the L indicates the size of the full parameter.
// Sometimes, this is needed to infer there are optional parameters.
// If it happens that we don't know a TV-param's type,
// we can skip the encapsulating TLV to continue parsing.
// Clients are required to accept messages with custom parameters.

const (
	ParamLLRPStatus                  = paramType(287)
	ParamFieldError                  = paramType(288)
	ParamParameterError              = paramType(289)
	ParamReaderEventNotificationData = paramType(246)
	ParamUTCTimestamp                = paramType(128)
	ParamUptime                      = paramType(129)
	ParamHoppingEvent                = paramType(247)
	ParamGPIEvent                    = paramType(248)
	ParamROSpecEvent                 = paramType(249)
	ParamBufferLevelWarnEvent        = paramType(250)
	ParamBufferOverflowEvent         = paramType(251)
	ParamReaderExceptionEvent        = paramType(252)
	ParamRFSurveyEvent               = paramType(253)
	ParamAISpecEvent                 = paramType(254)
	ParamAntennaEvent                = paramType(255)
	ParamConnectionAttemptEvent      = paramType(256)
	ParamConnectionCloseEvent        = paramType(257)
	ParamSpecLoopEvent               = paramType(365)
	ParamCustomParameter             = paramType(1023)

	// TV-encoded params
	ParamEPC96                  = paramType(13)
	ParamROSpectID              = paramType(9)
	ParamSpecIndex              = paramType(14)
	ParamInventoryParamSpecID   = paramType(10)
	ParamAntennaID              = paramType(1)
	ParamPeakRSSI               = paramType(6)
	ParamChannelIndex           = paramType(7)
	ParamFirstSeenUTC           = paramType(2)
	ParamFirstSeenUptime        = paramType(3)
	ParamLastSeenUTC            = paramType(4)
	ParamLastSeenUptime         = paramType(5)
	ParamTagSeenCount           = paramType(8)
	ParamClientReqOpSpecResult  = paramType(15)
	ParamAccessSpecID           = paramType(16)
	ParamOpSpecID               = paramType(17)
	ParamC1G2PC                 = paramType(12)
	ParamC1G2XPCW1              = paramType(19)
	ParamC1G2XPCW2              = paramType(20)
	ParamC1G2CRC                = paramType(11)
	ParamC1G2SingulationDetails = paramType(18)

	paramInvalid   = paramType(0)
	paramResvStart = 900
	paramResvEnd   = 999

	tlvHeaderSz = 4
	tvHeaderSz  = 1
	maxParamSz  = uint16(1<<16 - 1)
)

// tvLengths to byte lengths (not including the 1-byte type itself)
var tvLengths = map[paramType]int{
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

// isTV returns true if the paramType is TV-encoded.
// TV-encoded parameters have specific lengths which must be looked up.
func (pt paramType) isTV() bool {
	return pt <= 127
}

// isTLV returns true if the paramType is TLV-encoded.
// TLV-encoded parameters include their length in their headers.
func (pt paramType) isTLV() bool {
	return pt >= 128 && pt <= 2047
}

// isValid returns true if the paramType is within the valid LLRP Parameter range.
func (pt paramType) isValid() bool {
	return pt > 0 && pt <= 2047
}

// high returns the high byte of a paramType.
func (pt paramType) high() uint8 {
	return uint8(pt >> 8 & 0b11)
}

// low returns the low byte of a paramType.
func (pt paramType) low() uint8 {
	return uint8(pt)
}

// parameters are fields which "declare" themselves within an LLRP message,
// in the sense that they start with some type information
// rather than just existing at a certain offset with a certain size.
// They typically include their length, but sometimes length must be known apriori.
type parameter struct {
	typ    paramType // TVs are 1-127; TLVs are 128-2047
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

// TLV type 256
type connAttemptEventParam uint16

const (
	connectionSuccess         = connAttemptEventParam(0)
	readerInitiatedConnExists = connAttemptEventParam(1)
	clientInitiatedConnExists = connAttemptEventParam(2)
	failedReasonUnknown       = connAttemptEventParam(3)
	anotherConnAttempted      = connAttemptEventParam(4)
)

func (caep connAttemptEventParam) encode(w io.Writer) error {
	_, err := w.Write([]byte{
		uint8(caep >> 8), uint8(caep & 0xFF),
	})
	return err
}

func (caep *connAttemptEventParam) decode(r io.Reader) error {
	b := []byte{0, 0}
	if _, err := io.ReadFull(r, b); err != nil {
		return errors.Wrap(err, "read failed")
	}
	*caep = connAttemptEventParam(binary.BigEndian.Uint16(b))
	return nil
}

type LLRPStatus struct {
	StatusCode     uint16
	ErrDescription string
	FieldErrors    []FieldError
	ParamErrors    []ParamError
}

type FieldError struct {
	FieldNum  uint16
	ErrorCode uint16
}

func (fe *FieldError) decode(mr *msgReader) error {
	if fe == nil {
		*fe = FieldError{}
	}

	if err := mr.setFields(&(fe.FieldNum), &(fe.ErrorCode)); err != nil {
		return err
	}

	return nil
}

type ParamError struct {
	ParamType paramType
	ErrorCode uint16
	*FieldError
	*ParamError
}

func (pe *ParamError) decode(mr *msgReader) error {
	if pe == nil {
		*pe = ParamError{}
	}

	if err := mr.setFields(&(pe.ParamType), &(pe.ErrorCode)); err != nil {
		return err
	}

	for mr.currentParam().length != 0 {
		if err := mr.readParam(); err != nil {
			return err
		}

		switch mr.currentParam().typ {
		case ParamFieldError:
			if err := mr.set(&pe.FieldError); err != nil {
				return err
			}
		case ParamParameterError:
			if err := mr.set(&pe.ParamError); err != nil {
				return err
			}
		default:
			return errors.Errorf("expected either %v or %v, but found %v",
				ParamFieldError, ParamParameterError, mr.currentParam())
		}

		if err := mr.doneWithParam(); err != nil {
			return err
		}
	}

	return nil
}

func (s *LLRPStatus) decode(mr *msgReader) error {
	if s == nil {
		*s = LLRPStatus{}
	}

	if err := mr.setFields(&(s.StatusCode), &(s.ErrDescription)); err != nil {
		return err
	}

	for mr.currentParam().length != 0 {
		if err := mr.readParam(); err != nil {
			return err
		}

		switch mr.currentParam().typ {
		case ParamFieldError:
			f := FieldError{}
			if err := mr.set(&f); err != nil {
				return err
			}
			s.FieldErrors = append(s.FieldErrors, f)
		case ParamParameterError:
			p := ParamError{}
			if err := mr.set(&p); err != nil {
				return err
			}
			s.ParamErrors = append(s.ParamErrors, p)
		default:
			return errors.Errorf("expected either %v or %v, but found %v",
				ParamFieldError, ParamParameterError, mr.currentParam())
		}

		if err := mr.doneWithParam(); err != nil {
			return err
		}
	}

	return nil
}

// Message type
type getSupportedVersionResponse struct {
	Current   VersionNum
	Supported VersionNum
	Status    LLRPStatus
}

func (sv *getSupportedVersionResponse) decode(mr *msgReader) error {
	if sv == nil {
		*sv = getSupportedVersionResponse{}
	}

	return mr.setFields(&(sv.Current), &(sv.Supported), &(sv.Status))
}

// message type 63
type readerEventNotification struct {
	NotificationData readerEventNotificationData
}

func (ren *readerEventNotification) decode(mr *msgReader) error {
	*ren = readerEventNotification{}

	if err := mr.readParam(); err != nil {
		return err
	}

	// todo: confirm message type?
	return mr.setFields(&ren.NotificationData)
}

// TLV type 246
type readerEventNotificationData struct {
	TS                timestamp
	ConnectionAttempt *connAttemptEventParam
}

func (mr msgReader) verify(pt ...paramType) error {
	if len(pt) == 0 {
		return nil
	}

	ph := mr.currentParam()
	for _, t := range pt {
		if ph.typ == t {
			return nil
		}
	}

	if len(pt) == 1 {
		return errors.Errorf("unexpected param %v when looking for %v", ph.typ, pt[0])
	}
	return errors.Errorf("unexpected param %v when looking for one of %v", ph.typ, pt)
}

func (*readerEventNotificationData) getType() paramType {
	return ParamReaderEventNotificationData
}

func (ren *readerEventNotificationData) decode(mr *msgReader) error {
	if ren == nil {
		*ren = readerEventNotificationData{}
	}

	if err := mr.readParam(); err != nil {
		return err
	}

	if err := mr.set(&ren.TS); err != nil {
		return err
	}

	if err := mr.doneWithParam(); err != nil {
		return err
	}

	if mr.currentParam().length == 0 {
		return nil
	}

	ren.ConnectionAttempt = new(connAttemptEventParam)
	if err := mr.set((*uint16)(ren.ConnectionAttempt)); err != nil {
		return err
	}

	return nil
}

// TLV type 128 == UTC Timestamp, microseconds since 00:00:00 UTC, Jan 1, 1970.
// TLV type 129 == Uptime Timestamp, microseconds since reader started.
type timestamp struct {
	microseconds uint64
	isUTC        bool // true if microseconds offset since unix epoch; otherwise, it's uptime
}

// toGoTime returns the timestamp as a Go time.Time type,
// assuming that it's a UTC timestamp.
// If it isn't, then the caller should add the reader's start time.
func (ts timestamp) toGoTime() time.Time {
	// todo: extends this sometime in the next few hundred years
	return time.Unix(
		0,
		int64(ts.microseconds*1000))
}

func (ts timestamp) String() string {
	if ts.isUTC {
		return ts.toGoTime().String()
	}
	return time.Duration(ts.microseconds).String() + " since start"
}

func utcCurrent() timestamp {
	return timestamp{
		microseconds: uint64(time.Duration(time.Now().UnixNano()).Microseconds()),
		isUTC:        true,
	}
}

func (ts *timestamp) decode(mr *msgReader) error {
	if ts == nil {
		*ts = timestamp{}
	}
	switch mr.currentParam().typ {
	case ParamUTCTimestamp:
		ts.isUTC = true
		return mr.set(&ts.microseconds)
	case ParamUptime:
		ts.isUTC = false
		return mr.set(&ts.microseconds)
	default:
		return errors.Errorf("expected %v or %v, but got %v",
			ParamUptime, ParamUTCTimestamp, mr.currentParam().typ)
	}
}

// msgReader provides a structured way of streaming an LLRP message.
//
// This reader provides a simplified model to access message data fields and parameters,
// but relies on exclusive control of the underlying data source,
// so if you use it, you must not use the underlying reader at the same time;
// only access fields and parameters via the msgReader's methods.
//
// An LLRP message consists of a header identifying its type and size,
// followed by 0 or more data fields and parameters.
// Parameters are similar to messages:
// they begin with a header identifying their type and, in most cases, size,
// and they are followed by 0 or more data fields and sub-parameters.
// In some cases, a parameter's header only identifies its type,
// in which case its size is determine by a lookup table.
// Fields are always a fixed length and offset relative their container,
// and thus never include a header.
//
// Use newMsgReader to wrap a message, then use set to extract payload data
// into fields and parameters.
type msgReader struct {
	r      *bufio.Reader
	pStack field
}

type field struct {
	parameter
	prev parameter
}

func newMsgReader(m message) msgReader {
	mr := msgReader{
		r: bufio.NewReader(m.payload),
	}
	return mr
}

type pT interface {
	getType() paramType
}

type paramDecoder interface {
	decode(*msgReader) error
}

// currentParam returns the header of the current parameter.
func (mr msgReader) currentParam() parameter {
	return mr.pStack.parameter
}

// readParam reads the next parameter header onto the stack.
// During decoding, it should be called after reading a fixed fields.
func (mr *msgReader) readParam() error {
	t, err := mr.r.ReadByte()
	if err != nil {
		if !errors.Is(err, io.EOF) {
			err = errors.Wrap(err, "failed to read parameter")
		}
		return errors.Wrap(err, "no more data")
	}

	var p parameter
	if t&0x80 == 1 { // TV parameter
		p.typ = paramType(t & 0x7F)
		l, ok := tvLengths[p.typ]
		if !ok {
			return errors.Errorf("unknown TV type %v", p)
		}
		p.length = uint16(l)
	} else {
		t2, err := mr.r.ReadByte()
		if err != nil {
			return errors.Wrap(err, "failed to read parameter")
		}
		t3, err := mr.r.ReadByte()
		if err != nil {
			return errors.Wrap(err, "failed to read parameter")
		}
		t4, err := mr.r.ReadByte()
		if err != nil {
			return errors.Wrap(err, "failed to read parameter")
		}
		/*
			b := []byte{t, 0, 0, 0}
			if _, err := io.ReadFull(mr.r, b[1:]); err != nil {
				return errors.Wrap(err, "failed to read parameter")
			}

			p = parameter{
				typ:    paramType(binary.BigEndian.Uint16(b[0:2])),
				length: binary.BigEndian.Uint16(b[2:4]),
			}
		*/

		p = parameter{
			typ:    paramType(uint16(t)<<8 | uint16(t2)),
			length: uint16(t3)<<8 | uint16(t4),
		}
	}

	return mr.add(p)
}

// add a parameter to the current parameter stack.
// this typically should only be called by readParam.
func (mr *msgReader) add(p parameter) error {
	if mr.pStack.length > 0 {
		cp := mr.currentParam()
		if p.length > cp.length {
			return errors.Errorf("sub-parameter %v is larger than its container %v", p, cp)
		}

		// Remove the sub-param from the current top-of-stack parameter.
		mr.pStack.length -= p.length
	}

	// Exclude the parameter's header size from its payload;
	// this doesn't apply to TV params, as their length is known apriori.
	if p.typ.isTLV() {
		if p.length < tlvHeaderSz {
			return errors.Errorf("invalid TLV header length: %v", p)
		}
		p.length -= tlvHeaderSz
	}

	mr.pStack = field{parameter: p, prev: mr.pStack.parameter}
	return nil
}

// discard the current parameter by popping it off the stack
// and advancing the reader past any of its unread data.
func (mr *msgReader) discard() error {
	if mr.pStack.typ == 0 {
		return errors.New("no current parameter")
	}

	ph := mr.currentParam()
	mr.pStack = field{parameter: mr.pStack.prev}

	if ph.length != 0 {
		_, err := mr.r.Discard(int(ph.length))
		if err != nil {
			return errors.Wrap(err, "failed to discard parameter body")
		}
	}

	return nil
}

// doneWithParam checks that the current parameter's data is fully read,
// then discards it.
func (mr *msgReader) doneWithParam() error {
	if mr.currentParam().length != 0 {
		return errors.Errorf("parameter has unexpected data: %v", mr.currentParam())
	}

	if err := mr.discard(); err != nil {
		return err
	}
	return mr.readParam()
}

// decode the current parameter by matching its type against the permitted inputs.
// If none match, return an error; otherwise, set the matching parameter.
func (mr *msgReader) decodeOne(first pT, more ...pT) error {
	ph := mr.currentParam()

	if ph.typ == first.getType() {
		if err := mr.set(first); err != nil {
			return err
		}

		return mr.doneWithParam()
	}

	for i := range more {
		if ph.typ != more[i].getType() {
			continue
		}

		if err := mr.set(more[i]); err != nil {
			return err
		}

		return mr.doneWithParam()
	}

	if len(more) == 0 {
		return errors.Errorf("unexpected parameter %v when looking for %v", ph, first.getType())
	}

	pts := make([]string, len(more))
	pts[0] = first.getType().String()

	for i := range more[:len(more)-1] {
		pts[i+1] = more[i].getType().String()
	}

	return errors.Errorf("unexpected parameter %v when looking for %v or %v",
		ph, strings.Join(pts, ", "), more[len(more)-1].getType())
}

// setFields sets a series of required fields in a known order.
func (mr *msgReader) setFields(v ...interface{}) error {
	for i := range v {
		if err := mr.set(v[i]); err != nil {
			return err
		}
	}
	return nil
}

// set a single field v.
//
// If v implements paramDecoder, it calls that.
// Otherwise, v must one of the following types:
// -- *string: data is 2 byte length, followed by UTF-8 string data.
// -- *uint64: data is 8 byte unsigned int.
// -- *uint32: data is 4 byte unsigned int.
// -- *uint16: data is 2 byte unsigned int.
// --  *uint8: data is 1 byte unsigned int.
func (mr *msgReader) set(v interface{}) error {
	if v == nil {
		return nil
	}

	var err error
	switch v := v.(type) {
	case paramDecoder:
		return v.decode(mr)
	case *string:
		*v, err = mr.getString()
	case *uint64:
		*v, err = mr.getUint(8)
	case *uint32:
		var u uint64
		u, err = mr.getUint(4)
		*v = uint32(u)
	case *uint16:
		var u uint64
		u, err = mr.getUint(2)
		*v = uint16(u)
	case *uint8:
		var u uint64
		u, err = mr.getUint(1)
		*v = uint8(u)
	default:
		if p, ok := v.(pT); ok {
			err = errors.Errorf("unhandled type: %T for %v", v, p.getType())
		} else {
			err = errors.Errorf("unhandled type: %T", v)
		}
	}
	return err
}

// getString reads a UTF-8 encoded string from the current parameter,
// assuming its prepended by a uint16 indicating its length in bytes.
func (mr *msgReader) getString() (string, error) {
	sLen, err := mr.getUint(2)
	if err != nil {
		return "", errors.WithMessagef(err, "failed to read string length")
	}

	if sLen == 0 {
		return "", nil
	}

	cp := mr.currentParam()
	if cp.length < uint16(sLen) {
		return "", errors.Errorf("not enough data to read string of length %d from %v", sLen, cp)
	}

	mr.pStack.length -= uint16(sLen)
	sBuild := strings.Builder{}
	if _, err = io.CopyN(&sBuild, mr.r, int64(sLen)); err != nil {
		return "", errors.Wrapf(err, "failed to read string of length %d from %v", sLen, cp)
	}

	return sBuild.String(), nil
}

// getUint of size 0 < nBytes <= 8, (presumably 1, 2, 4, 8), assumed BigEndian.
// If nBytes is outside of that range, this will panic.
// This advances the reader by nBytes.
func (mr *msgReader) getUint(nBytes int) (uint64, error) {
	cp := mr.currentParam()
	if int(cp.length) < nBytes {
		return 0, errors.Errorf("not enough data to read uint%d from %v", nBytes*8, cp)
	}
	mr.pStack.length -= uint16(nBytes)

	var u uint64
	for i := 0; i < nBytes; i++ {
		b, err := mr.r.ReadByte()
		if err != nil {
			return 0, errors.Wrapf(err, "failed to read uint%d", nBytes*8)
		}
		u = (u << 8) | uint64(b)
	}

	return u, nil
}
