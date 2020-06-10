//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package llrp

import (
	"bufio"
	"bytes"
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
	return pt != 0 && pt <= 127
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

func (caep connAttemptEventParam) encode(mb *msgBuilder) error {
	return mb.writeUint(uint64(caep), 2)
}

type LLRPStatus struct {
	StatusCode     uint16
	ErrDescription string
	FieldErrors    []FieldError
	ParamErrors    []ParamError
}

func (*LLRPStatus) getType() paramType {
	return ParamLLRPStatus
}

func (ls *LLRPStatus) encode(mb *msgBuilder) error {
	if err := mb.writeFields(ls.StatusCode, ls.ErrDescription); err != nil {
		return err
	}
	// todo: field errors, param errors
	return nil
}

type FieldError struct {
	FieldNum  uint16
	ErrorCode uint16
}

func (fe *FieldError) decode(mr *msgReader) error {
	if err := mr.readFields(&(fe.FieldNum), &(fe.ErrorCode)); err != nil {
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
	if err := mr.readFields(&(pe.ParamType), &(pe.ErrorCode)); err != nil {
		return err
	}

	for mr.hasData() {
		prev, err := mr.readParam()
		if err != nil {
			return err
		}

		switch mr.cur.typ {
		case ParamFieldError:
			if err := mr.read(&pe.FieldError); err != nil {
				return err
			}
		case ParamParameterError:
			if err := mr.read(&pe.ParamError); err != nil {
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

func (s *LLRPStatus) decode(mr *msgReader) error {
	if err := mr.readFields(&(s.StatusCode), &(s.ErrDescription)); err != nil {
		return err
	}

	for mr.hasData() {
		prev, err := mr.readParam()
		if err != nil {
			return err
		}

		switch mr.cur.typ {
		case ParamFieldError:
			f := FieldError{}
			if err := mr.read(&f); err != nil {
				return err
			}
			s.FieldErrors = append(s.FieldErrors, f)
		case ParamParameterError:
			p := ParamError{}
			if err := mr.read(&p); err != nil {
				return err
			}
			s.ParamErrors = append(s.ParamErrors, p)
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

// Message type
type getSupportedVersionResponse struct {
	Current   VersionNum
	Supported VersionNum
	Status    LLRPStatus
}

func (sv *getSupportedVersionResponse) decode(mr *msgReader) error {
	if err := mr.readFields((*uint8)(&(sv.Current)), (*uint8)(&(sv.Supported))); err != nil {
		return err
	}
	return mr.readParameter(&sv.Status)
}

// message type 63
type readerEventNotification struct {
	NotificationData readerEventNotificationData
}

func (ren *readerEventNotification) decode(mr *msgReader) error {
	return mr.readParameters(&ren.NotificationData)
}

func (ren *readerEventNotification) encode(mb *msgBuilder) error {
	return mb.writeParam(&ren.NotificationData)
}

// TLV type 246
type readerEventNotificationData struct {
	TS                timestamp
	ConnectionAttempt connAttemptEventParam
}

func (rend *readerEventNotificationData) encode(mb *msgBuilder) error {
	return mb.writeParams(&rend.TS, &rend.ConnectionAttempt)
}

func (*readerEventNotificationData) getType() paramType {
	return ParamReaderEventNotificationData
}

func (*connAttemptEventParam) getType() paramType {
	return ParamConnectionAttemptEvent
}

func (ren *readerEventNotificationData) decode(mr *msgReader) error {
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

func (ts *timestamp) getType() paramType {
	if ts.isUptime {
		return ParamUptime
	}
	return ParamUTCTimestamp
}

func (ts *timestamp) decode(mr *msgReader) error {
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

func (ts *timestamp) encode(mb *msgBuilder) error {
	return mb.writeUint(ts.microseconds, 8)
}

type msgBuilder struct {
	w *bytes.Buffer
}

func newMsgBuilder() *msgBuilder {
	w := bytes.Buffer{}
	return &msgBuilder{w: &w}
}

func (mb *msgBuilder) reset() {
	mb.w.Reset()
}

func (mb *msgBuilder) finish(mt messageType) (message, error) {
	if mb.w.Len() > int(maxPayloadSz) {
		return message{}, errors.Errorf("payload size exceeds max: %d", mb.w.Len())
	}

	return message{
		payload: mb.w,
		header: header{
			payloadLen: uint32(mb.w.Len()),
			typ:        mt,
		},
	}, nil
}

func (vn VersionNum) encode(mb *msgBuilder) error {
	return mb.write(uint8(vn))
}

func (mb *msgBuilder) writeFields(fields ...interface{}) error {
	for i := range fields {
		if err := mb.write(fields[i]); err != nil {
			return err
		}
	}
	return nil
}

func (mb *msgBuilder) writeParams(params ...pT) error {
	for i := range params {
		if err := mb.writeParam(params[i]); err != nil {
			return err
		}
	}
	return nil
}

func (mb *msgBuilder) writeParam(p pT) error {
	pc, err := mb.startParam(p.getType())
	if err != nil {
		return err
	}
	if err := mb.write(p); err != nil {
		return err
	}
	return mb.finishParam(pc)
}

type paramEncoder interface {
	encode(mb *msgBuilder) error
}

// write a single field f.
//
// If f implements paramEncoder, it calls that.
// Otherwise, f must be one of the following types:
// -- string: written as a 2 byte length, followed its bytes (presumed to be UTF-8).
// -- uint64: written as 8 bytes
// -- uint32: written as 4 bytes
// -- uint16: written as 2 bytes
// --  uint8: written as 1 byte
func (mb *msgBuilder) write(f interface{}) error {
	switch v := f.(type) {
	case paramEncoder:
		return v.encode(mb)
	case uint64:
		return mb.writeUint(v, 8)
	case uint32:
		return mb.writeUint(uint64(v), 4)
	case uint16:
		return mb.writeUint(uint64(v), 2)
	case uint8:
		return mb.writeUint(uint64(v), 1)
	case string:
		return mb.writeString(v)
	default:
		if p, ok := v.(pT); ok {
			return errors.Errorf("unhandled type: %T for %v -- should it implement paramEncoder?",
				v, p.getType())
		}
		return errors.Errorf("unhandled type: %T -- should it implement paramEncoder?", v)
	}
}

// writeUint u using nBytes, which should be 1, 2, 4, or 8,
// but that isn't validated by this method.
func (mb *msgBuilder) writeUint(v uint64, nBytes int) error {
	switch nBytes {
	case 8:
		mb.w.WriteByte(uint8(v >> 56))
		mb.w.WriteByte(uint8(v >> 48))
		mb.w.WriteByte(uint8(v >> 40))
		mb.w.WriteByte(uint8(v >> 32))
		fallthrough
	case 4:
		mb.w.WriteByte(uint8(v >> 24))
		mb.w.WriteByte(uint8(v >> 16))
		fallthrough
	case 2:
		mb.w.WriteByte(uint8(v >> 8))
		fallthrough
	case 1:
		mb.w.WriteByte(uint8(v))
	default:
		return errors.Errorf("not a valid uint size: %d", nBytes)
	}

	return nil
}

// writeString s to the stream by prepending its length as a uint16,
// followed by its data.
// LLRP string should be UTF-8 encoded, but this doesn't validate that.
func (mb *msgBuilder) writeString(s string) error {
	ls := len(s)

	// make sure we're not dealing with a string with more than 2^16-2 bytes
	if ls > int(maxParamSz)-2 {
		return errors.Errorf("string has %d bytes and is larger than an LLRP parameter", len(s))
	}

	mb.w.WriteByte(byte(ls >> 8))
	mb.w.WriteByte(byte(ls & 0xFF))
	mb.w.WriteString(s)
	return nil
}

type paramCtx struct {
	start int
	parameter
}

// startParam starts a parameter context which must be finished with finishParam.
//
// The writes the parameter type to the stream (1 byte for TVs, 2 for TLVs),
// and for TLVs, reserves 2 bytes for the length,
// which must be set later via finishParam.
func (mb *msgBuilder) startParam(pt paramType) (paramCtx, error) {
	// log.Printf("starting %v", pt)
	pc := paramCtx{
		parameter: parameter{typ: pt},
		start:     mb.w.Len(),
	}

	if pt.isTV() {
		if _, ok := tvLengths[pt]; !ok {
			return pc, errors.Errorf("unknown TV %v", pt)
		}

		mb.w.WriteByte(byte(pt) | 0x80)
	} else {
		mb.w.WriteByte(uint8(pt >> 8 & 0b11))
		mb.w.WriteByte(uint8(pt))
		// length, to be set later
		mb.w.WriteByte(0x0)
		mb.w.WriteByte(0x0)
	}

	return pc, nil
}

// finishParam validates a parameter's length and pops it off the stack.
//
// For TLVs, it writes the the parameter's length into the header.
// For TVs, it validates that the written data matches the expected length.
// If this is a sub-parameter, it adds its length to its parent
// and validates that it fits within that parameter.
func (mb *msgBuilder) finishParam(pc paramCtx) error {
	end := mb.w.Len()
	length := end - pc.start
	// log.Printf("finishing %v [%d to %d] (%d bytes)", pc.typ, pc.start, end, length)

	b := mb.w.Bytes()
	b[pc.start+3] = uint8(length & 0xFF)
	b[pc.start+2] = uint8(length >> 8)

	return nil
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
	r   *bufio.Reader
	cur parameter
}

func newMsgReader(m message) *msgReader {
	mr := &msgReader{
		r: bufio.NewReader(m.payload),
	}
	return mr
}

func (mr *msgReader) reset(m message) {
	mr.r.Reset(m.payload)
	mr.cur = parameter{}
}

type pT interface {
	getType() paramType
}

type paramDecoder interface {
	decode(*msgReader) error
}

// hasData returns true if the reader current parameter has data.
func (mr msgReader) hasData() bool {
	return mr.cur.typ != paramInvalid && mr.cur.length > 0
}

// readParam reads the next parameter header, returning was the current one.
// Callers should use finishParam when done to replace the parameter context.
func (mr *msgReader) readParam() (prev parameter, err error) {
	t, err := mr.readUint8()
	if err != nil {
		if !errors.Is(err, io.EOF) {
			err = errors.Wrap(err, "failed to read parameter")
		}
		return prev, errors.Wrap(err, "no more data")
	}

	var p parameter
	if t&0x80 != 0 { // TV parameter
		p.typ = paramType(t & 0x7F)
		l, ok := tvLengths[p.typ]
		if !ok {
			return prev, errors.Errorf("unknown TV type %v", p)
		}
		p.length = uint16(l)
	} else {
		p.typ = paramType(uint16(t) << 8)
		t, err = mr.readUint8()
		if err != nil {
			return prev, errors.Wrap(err, "failed to read parameter")
		}
		p.typ |= paramType(t)

		p.length, err = mr.readUint16()
		if err != nil {
			return prev, errors.Wrap(err, "failed to read parameter")
		}

		if p.length < tlvHeaderSz {
			return prev, errors.Errorf("invalid TLV header length: %v", p)
		}
		p.length -= tlvHeaderSz
	}

	if mr.cur.typ != paramInvalid {
		if p.length > mr.cur.length {
			return prev, errors.Errorf(
				"sub-parameter %v needs more bytes than those remaining in %v",
				p, mr.cur)
		}

		// "Remove" the sub-param from the current parameter.
		mr.cur.length -= p.length
	}

	prev = mr.cur
	mr.cur = p
	return prev, nil
}

// discard the current parameter by popping it off the stack
// and advancing the reader past any of its unread data.
func (mr *msgReader) discard() error {
	if mr.cur.typ == 0 {
		return errors.New("no current parameter")
	}

	if mr.hasData() {
		_, err := mr.r.Discard(int(mr.cur.length))
		if err != nil {
			return errors.Wrapf(err, "failed to discard parameter %v", mr.cur)
		}
	}

	return nil
}

// readFields reads a series of required fields in a known order.
func (mr *msgReader) readFields(fields ...interface{}) error {
	for i := range fields {
		if err := mr.read(fields[i]); err != nil {
			return err
		}
	}
	return nil
}

// readParameters reads a series of required parameters in a known order.
func (mr *msgReader) readParameters(params ...pT) error {
	for i := range params {
		if err := mr.readParameter(params[i]); err != nil {
			return err
		}
	}
	return nil
}

// readParameter reads a single, required parameter,
// reading its header, decoding its data,
// and popping it off the stack once done.
func (mr *msgReader) readParameter(p pT) error {
	prev, err := mr.readParam()
	if err != nil {
		return err
	}

	if mr.cur.typ != p.getType() {
		return errors.Errorf("unexpected parameter %v when looking for %v",
			mr.cur.typ, p.getType())
	}

	if err := mr.read(p); err != nil {
		return err
	}

	return mr.endParam(prev)
}

func (mr *msgReader) endParam(prev parameter) error {
	if mr.hasData() {
		return errors.Errorf("parameter has unexpected data: %v", mr.cur)
	}

	mr.cur = prev
	return nil
}

// read a single field f.
//
// If f implements paramDecoder, it calls that.
// Otherwise, f must one of the following types:
// -- *string: data is 2 byte length, followed by UTF-8 string data.
// -- *uint64: data is 8 byte unsigned int.
// -- *uint32: data is 4 byte unsigned int.
// -- *uint16: data is 2 byte unsigned int.
// --  *uint8: data is 1 byte unsigned int.
func (mr *msgReader) read(f interface{}) error {
	if f == nil {
		return nil
	}

	var err error
	switch v := f.(type) {
	case paramDecoder:
		return v.decode(mr)
	case *string:
		*v, err = mr.readString()
	case *uint64:
		*v, err = mr.readUint64()
	case *uint32:
		*v, err = mr.readUint32()
	case *uint16:
		*v, err = mr.readUint16()
	case *uint8:
		*v, err = mr.readUint8()
	default:
		if p, ok := v.(pT); ok {
			err = errors.Errorf("unhandled type: %T for %v -- should it implement paramDecoder?",
				v, p.getType())
		} else {
			err = errors.Errorf("unhandled type: %T -- should it implement paramDecoder?", v)
		}
	}
	return err
}

// readString reads a UTF-8 encoded string from the current parameter,
// assuming its prepended by a uint16 indicating its length in bytes.
func (mr *msgReader) readString() (string, error) {
	sLen, err := mr.readUint16()
	if err != nil {
		return "", errors.WithMessagef(err, "failed to read string length")
	}

	if sLen == 0 {
		return "", nil
	}

	if mr.cur.length < sLen {
		return "", errors.Errorf("not enough data to read string of length %d from %v",
			sLen, mr.cur)
	}
	mr.cur.length -= sLen

	sBuild := strings.Builder{}
	if _, err = io.CopyN(&sBuild, mr.r, int64(sLen)); err != nil {
		return "", errors.Wrapf(err, "failed to read string of length %d from %v",
			sLen, mr.cur)
	}

	return sBuild.String(), nil
}

// readUint64 reads an 8-bit uint from the payload.
func (mr *msgReader) readUint8() (uint8, error) {
	u, err := mr.readUint(1)
	return uint8(u), err
}

// readUint64 reads a 16-bit uint from the payload.
func (mr *msgReader) readUint16() (uint16, error) {
	u, err := mr.readUint(2)
	return uint16(u), err
}

// readUint64 reads a 32-bit uint from the payload.
func (mr *msgReader) readUint32() (uint32, error) {
	u, err := mr.readUint(4)
	return uint32(u), err
}

// readUint64 reads a 64-bit uint from the payload.
func (mr *msgReader) readUint64() (uint64, error) {
	return mr.readUint(8)
}

// readUint of size 0 < nBytes <= 8, (presumably 1, 2, 4, 8), assumed BigEndian.
// If nBytes is outside of that range, this will panic.
// This advances the reader by nBytes
// and subtracts them from the current parameter's length.
func (mr *msgReader) readUint(nBytes int) (uint64, error) {
	if mr.cur.typ != paramInvalid && int(mr.cur.length) < nBytes {
		return 0, errors.Errorf("not enough data to read uint%d from %v", nBytes*8, mr.cur)
	}
	mr.cur.length -= uint16(nBytes)

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
