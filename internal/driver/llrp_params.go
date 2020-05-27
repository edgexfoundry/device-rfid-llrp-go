package driver

import (
	"bufio"
	"encoding/binary"
	"github.com/pkg/errors"
	"io"
)

type paramType uint16 // 8 (TL) or 10 bits (TLV)

const (
	LLRPStatusParam     = paramType(287)
	FieldErrorParam     = paramType(288)
	ParameterErrorParam = paramType(289)
)

func (pt paramType) isTL() bool {
	return pt <= 127
}
func (pt paramType) isTLV() bool {
	return pt >= 128 && pt <= 2047
}
func (pt paramType) isValid() bool {
	return pt <= 2047
}

type paramHeader struct {
	typ    paramType // TVs are 1-127; TLVs are 128-2047
	length uint16    // only present for TLVs
}

type paramReader struct {
	r       *bufio.Reader
	n       uint32
	cur     paramHeader
	unknown bool
}

func newParamReader(m msgOut) *paramReader {
	pr := paramReader{r: bufio.NewReader(m.data), n: m.length}
	return &pr
}

func (pr *paramReader) next() (paramType, error) {
	if pr.n == 0 {
		return 0, nil
	}

	t, err := pr.r.ReadByte()
	if err != nil {
		return 0, errors.Wrap(err, "failed to parse param header")
	}

	if t == 0 {
		return 0, errors.New("invalid parameter type 0")
	}

	if t&0x80 == 0 {
		// if the 1st bit is 0, it's a TLV
		t2, err := pr.r.ReadByte()
		if err != nil {
			return 0, errors.Wrap(err, "failed to parse param header")
		}
		pr.cur.typ = paramType((uint16(t&3) << 8) | uint16(t2))

		b := []byte{0, 0}
		if _, err := io.ReadFull(pr.r, b); err != nil {
			return 0, errors.Wrap(err, "failed to parse param header")
		}
		pr.cur.length = binary.BigEndian.Uint16(b) - 4 // subtract header size
		pr.n -= 4
	} else {
		// if the 1st bit is 1, it's a TV and has no length info
		pr.cur.typ = paramType(t & 0x7F)
		pr.cur.length = 0 // todo
		pr.n -= 1
	}

	return pr.cur.typ, nil
}

// TLV, type 287
type llrpStatus struct {
	statusCode  uint16
	errDescLen  uint16 // number of UTF-8 bytes
	err         string // UTF-8
	*fieldError        // optional
	*paramError        // optional
}

// TLV, type 288
type fieldError struct {
	fieldNum uint16
	errCode  uint16
}

// TLV, type 289
type paramError struct {
	typ     paramType
	errCode uint16
	*fieldError
	*paramError
}

func paramParseErr(pt paramType, msg string, args ...interface{}) error {
	return errors.Errorf("failed to parse parameter %v: "+msg,
		append([]interface{}{pt}, args...))
}

func paramParseWrap(err error, pt paramType, msg string, args ...interface{}) error {
	return errors.Wrapf(err, "failed to parse parameter %v: "+msg,
		append([]interface{}{pt}, args...))
}

func (pr *paramReader) llrpStatus(n uint16) (llrpStatus, error) {
	var ls llrpStatus
	if n < 4 {
		return ls, paramParseErr(LLRPStatusParam, "not enough data")
	}

	err := binary.Read(pr.r, binary.BigEndian, &ls.statusCode)
	err = binary.Read(pr.r, binary.BigEndian, &ls.errDescLen)
	if err != nil {
		return ls, paramParseWrap(err, LLRPStatusParam, "couldn't read initial fields")
	}
	n -= 4

	if ls.errDescLen > 0 {
		if n < ls.errDescLen {
			return ls, paramParseErr(LLRPStatusParam,
				"not enough bytes to read err description: %d < %d", n, ls.errDescLen)
		}

		b := make([]byte, ls.errDescLen)
		if _, err := io.ReadFull(pr.r, b); err != nil {
			return ls, errors.Wrap(err, "failed to parse error message")
		}
		ls.err = string(b)

		n -= ls.errDescLen
	}

	if n == 0 {
		return ls, nil
	}

	// more parameters, yay...
	b := []byte{0, 0}
	if _, err := io.ReadFull(pr.r, b); err != nil {
		return ls, errors.Wrap(err, "failed to parse error message")
	}
	n -= 2

	typ := binary.BigEndian.Uint16(b)
	switch paramType(typ) {
	case FieldErrorParam:
		fe, err := pr.fieldError()
		if err != nil {
			return ls, errors.WithMessage(err, "failed parsing LLRPStatus")
		}
		n -= 4
		ls.fieldError = &fe
	case ParameterErrorParam:
	default:
		return ls, paramParseErr(LLRPStatusParam, "invalid sub-parameter type: %d", typ)
	}

	return ls, nil
}

func (pr *paramReader) parameterError() (paramError, error) {
	pe := paramError{}
	err := binary.Read(pr.r, binary.BigEndian, []interface{}{
		&pe.typ, &pe.errCode})
	if err != nil {
		return pe, paramParseWrap(err, FieldErrorParam, "failed filling fields")
	}
	// todo: read other params
	return pe, nil
}

func (pr *paramReader) fieldError() (fieldError, error) {
	fe := fieldError{}
	err := binary.Read(pr.r, binary.BigEndian, []interface{}{
		&fe.fieldNum, &fe.errCode})
	if err != nil {
		return fe, paramParseWrap(err, FieldErrorParam, "failed filling fields")
	}
	return fe, nil
}
