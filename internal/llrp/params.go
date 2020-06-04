//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package llrp

import (
	"encoding/binary"
	"io"
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

func (ph paramHeader) MarshalBinary() ([]byte, error) {
	if ph.typ.isTV() {
		return []byte{ph.typ.low()}, nil
	}

	b := []byte{0, 0, 0, 0}
	binary.BigEndian.PutUint16(b[0:2], uint16(ph.typ))
	binary.BigEndian.PutUint16(b[2:4], ph.length)
	return b, nil
}

// TLV type 246
type readerEventNotificationData struct {
	ts     timestamp
	events []paramHeader
}

func newConnectionSuccessEvent() readerEventNotificationData {
	return readerEventNotificationData{
		ts: currentUTC(),
	}
}

// TLV type 128 == UTC Timestamp
// TLV type 129 == Uptime Timestamp
type timestamp struct {
	microseconds uint64
	// utc is true if microseconds are from 00:00:00 UTC, Jan 1, 1970.
	// otherwise, they represent the reader's uptime
	utc bool
}

// toGoTime returns the timestamp as a Go time.Time type,
// assuming it represents a UTC time.
// If it doesn't, the return value is meaningless.
func (ts timestamp) toGoTime() time.Time {
	return time.Unix(0, (time.Duration(ts.microseconds) * time.Second).Nanoseconds())
}

func currentUTC() timestamp {
	return timestamp{microseconds: uint64(time.Now().UnixNano() / 1000), utc: true}
}

func (ts timestamp) writeData(w io.Writer) error {
	b := [12]byte{}
	if ts.utc {
		binary.BigEndian.PutUint16(b[0:2], uint16(ParamUTCTimestamp))
	} else {
		binary.BigEndian.PutUint16(b[0:2], uint16(ParamUptime))
	}
	binary.BigEndian.PutUint16(b[2:4], uint16(len(b)))
	binary.BigEndian.PutUint64(b[4:12], ts.microseconds)
	_, err := w.Write(b[:])
	return err
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
