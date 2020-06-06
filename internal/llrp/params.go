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
	"reflect"
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

// paramHeader represents the type and possibly length of a parameter.
type paramHeader struct {
	typ    paramType // TVs are 1-127; TLVs are 128-2047
	length uint16    // only present for TLVs; does not include header size
}

func (ph paramHeader) String() string {
	if ph.typ.isTLV() {
		return fmt.Sprintf("TLV %v (%[1]d, %#02x), %d bytes",
			ph.typ, uint16(ph.typ), ph.length)
	} else {
		return fmt.Sprintf("TV %v (%[1]d, %#02x), %d bytes",
			ph.typ, uint16(ph.typ), ph.length)
	}
}

func (ph paramHeader) MarshalBinary() ([]byte, error) {
	if ph.typ.isTV() {
		return []byte{ph.typ.low()}, nil
	}

	b := []byte{0, 0, 0, 0}
	binary.BigEndian.PutUint16(b[0:2], uint16(ph.typ))
	binary.BigEndian.PutUint16(b[2:4], ph.length)
	return b, nil
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

func (caep connAttemptEventParam) writeData(w io.Writer) error {
	_, err := w.Write([]byte{
		ParamConnectionAttemptEvent.high(),
		ParamConnectionAttemptEvent.low(),
		0, uint8(tlvHeaderSz) + 2,
		uint8(caep >> 8), uint8(caep & 0xFF),
	})
	return err
}

type msgReader struct {
	r *bufio.Reader
}

func newMsgReader(m message) msgReader {
	return msgReader{
		r: bufio.NewReader(m.payload),
	}
}

// discard the current parameter, advancing the reader beyond its payload.
// Note that if the parameter contains sub-parameters, they are also discarded.
//
// This only returns an error if it is unable to discard a parameter's bytes;
// if the reader is exhausted, it returns nil.
func (mr msgReader) discard() error {
	ph, err := mr.currentParam()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}

	_, err = mr.r.Discard(int(ph.length))
	return errors.Wrap(err, "failed to discard parameter body")
}

// stepInto the current parameter by discarding the header,
// aligning the reader at the first field.
func (mr msgReader) stepInto() error {
	ph, err := mr.currentParam()
	if err != nil {
		return err
	}

	if ph.typ.isTLV() {
		_, err = mr.r.Discard(tlvHeaderSz)
	} else {
		_, err = mr.r.Discard(tvHeaderSz)
	}
	return errors.Wrap(err, "failed to discard parameter header")
}

// currentParam returns the header of the parameter currently pointed to
// without advancing the reader position.
//
// Multiple calls to currentParam should return the same header.
// If the reader is exhausted, this returns an io.EOF.
func (mr msgReader) currentParam() (paramHeader, error) {
	t, err := mr.r.ReadByte()
	if err != nil {
		if !errors.Is(err, io.EOF) {
			err = errors.Wrap(err, "failed to read parameter")
		}
		return paramHeader{}, err
	}

	_ = mr.r.UnreadByte()

	if t&0x80 == 1 { // TV parameter
		p := paramHeader{typ: paramType(t & 0x7F)}

		l, ok := tvLengths[p.typ]
		if !ok {
			return p, errors.New("unknown TV type")
		}

		p.length = uint16(l)
		return p, nil
	}

	b, err := mr.r.Peek(4)
	if err != nil {
		return paramHeader{}, errors.Wrap(err, "failed to read parameter")
	}

	return paramHeader{
		typ:    paramType(binary.BigEndian.Uint16(b[0:2])),
		length: binary.BigEndian.Uint16(b[2:4]),
	}, nil
}

// TLV type 246
type readerEventNotificationData struct {
	TS                timestamp
	ConnectionAttempt *connAttemptEventParam
}

type parameter interface {
	getType() paramType
}

func (readerEventNotificationData) getType() paramType {
	return ParamReaderEventNotificationData
}

func (connAttemptEventParam) getType() paramType {
	return ParamConnectionAttemptEvent
}

type utcTimestamp uint64
type uptime uint64

func (utcTimestamp) getType() paramType {
	return ParamUTCTimestamp
}

func (uptime) getType() paramType {
	return ParamUptime
}

// TLV type 128 == UTC Timestamp, microseconds are from 00:00:00 UTC, Jan 1, 1970.
// TLV type 129 == Uptime Timestamp, microseconds since reader started.
type timestamp struct {
	UTC    *utcTimestamp
	Uptime *uptime
}

// toGoTime returns the timestamp as a Go time.Time type.
func (ut utcTimestamp) toGoTime() time.Time {
	return time.Unix(0, (time.Duration(ut) * time.Second).Nanoseconds())
}

func (ut utcTimestamp) String() string {
	return ut.toGoTime().String()
}

func utcCurrent() utcTimestamp {
	return utcTimestamp(time.Now().UnixNano() / 1000)
}

func (ts *timestamp) decode(mr msgReader) error {
	ph, err := mr.currentParam()
	if err != nil {
		return err
	}
	if err := mr.stepInto(); err != nil {
		return err
	}

	switch ph.typ {
	case ParamUTCTimestamp:
		return mr.setUint64((*uint64)(ts.UTC))
	case ParamUptime:
		return mr.setUint64((*uint64)(ts.Uptime))
	default:
		return errors.Errorf("expected %v or %v, but got %v",
			ParamUptime, ParamUTCTimestamp, ph.typ)
	}
}

func (mr msgReader) decode(v ...parameter) error {
	for i := range v {
		if err := mr.decodeExactly(v[i]); err != nil {
			return err
		}
	}
	return nil
}

func (mr msgReader) decodeExactly(v parameter) error {
	rv := reflect.ValueOf(v)
	if !rv.IsValid() || rv.Kind() != reflect.Ptr {
		return errors.New("can only decode into non-nil parameter pointers")
	}
	if rv.IsNil() {
		rv.Set(reflect.New(rv.Type().Elem()))
	}
	return errors.WithMessagef(mr.set(rv.Elem()), "failed to read %T from %v", v, v.getType())
}

func (mr msgReader) set(rv reflect.Value) error {
	if asParam, ok := rv.Interface().(parameter); ok {
		var err error
		var ph paramHeader
		if ph, err = mr.currentParam(); err != nil {
			return err
		}

		if rv.Kind() == reflect.Ptr && rv.IsNil() {
			return nil
		}

		t := asParam.getType()
		if ph.typ != t {
			return errors.Errorf("unexpected %v; expected %v", ph, t)
		}

		if err = mr.stepInto(); err != nil {
			return err
		}
	}

	if rv.Kind() == reflect.Ptr {
		if rv.IsNil() {
			rv.Set(reflect.New(rv.Type().Elem()))
		}
		rv = rv.Elem()
	}

	switch rv.Kind() {
	case reflect.String:
		sLen, err := mr.getUint(2)
		if err != nil {
			return errors.WithMessagef(err, "failed to read string length")
		}

		sBuild := strings.Builder{}
		if _, err = io.CopyN(&sBuild, mr.r, int64(sLen)); err != nil {
			return errors.Wrapf(err, "expected length %d", sLen)
		}

		rv.SetString(sBuild.String())

	case reflect.Struct:
		rt := rv.Type()
		for i := 0; i < rt.NumField(); i++ {
			ft := rt.Field(i)
			if ft.PkgPath != "" || ft.Name == "_" {
				continue // skip unexported field
			}

			if err := mr.set(rv.Field(i)); err != nil {
				return errors.Wrapf(err, "unable to set %s", ft.Name)
			}
		}

	case reflect.Uint8:
		return mr.setUintV(rv, 1)
	case reflect.Uint16:
		return mr.setUintV(rv, 2)
	case reflect.Uint32:
		return mr.setUintV(rv, 4)
	case reflect.Uint64:
		return mr.setUintV(rv, 8)
	default:
		return errors.Errorf("unhandled type: %v", rv.Type().String())
	}
	return nil
}

var refParameterType = reflect.TypeOf((*parameter)(nil)).Elem()

// setUint reads nBytes and assigns them to reflect uint pointer v.
// This method panics if v is not settable or if nBytes is outside of [1,8].
func (mr msgReader) setUintV(v reflect.Value, nBytes int) error {
	if u, err := mr.getUint(nBytes); err != nil {
		return err
	} else {
		v.SetUint(u)
	}
	return nil
}

func (mr msgReader) setUint64(v *uint64) error {
	if u, err := mr.getUint(8); err != nil {
		return err
	} else {
		*v = u
	}
	return nil
}

// getUint of size 0 < nBytes <= 8, but presumably 1, 2, 4, 8.
// Values outside that range will panic.
// This advances the reader by nBytes.
// As always, it assumes BigEndian.
func (mr msgReader) getUint(nBytes int) (uint64, error) {
	b := make([]byte, 8)
	if _, err := io.ReadFull(mr.r, b[8-nBytes:]); err != nil {
		return 0, errors.Wrapf(err, "failed to read uint%d", nBytes*8)
	}
	return binary.BigEndian.Uint64(b), nil
}
