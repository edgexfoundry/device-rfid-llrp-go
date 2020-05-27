//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package driver

import (
	"context"
	"github.com/pkg/errors"
	"io"
	"io/ioutil"
	"net"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestGetTCPAddr(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		t.Parallel()
		for _, m := range []protocolMap{{
			"tcp": {"host": "127.0.0.1", "port": "1234"},
		}} {
			addr, err := getAddr(m)
			if err != nil {
				t.Error(err)
			}

			expected := "127.0.0.1:1234"
			if expected != addr.String() {
				t.Errorf("expected %s; got %s", expected, addr.String())
			}
		}
	})

	t.Run("invalid", func(t *testing.T) {
		t.Parallel()
		for _, m := range []protocolMap{{
			"tcp": {"host": "127.0.0.1", "port": "86492"},
		}} {
			if _, err := getAddr(m); err == nil {
				t.Error("expected an error, but didn't get one")
			}
		}
	})
}

func TestReader_readHeader(t *testing.T) {
	type testCase struct {
		desc     string
		data     []byte
		expected header
		invalid  bool
	}

	for i, tc := range []testCase{
		{
			desc: "good header",
			data: []byte{
				0b000_010_00, 0b0000_0001, // rsvd, ver, msg type
				0x0, 0x0, 0x0, 0xA, // msg length
				0x0, 0x0, 0x0, 0x1, // msg id
				// no payload
			},
			expected: header{payloadLen: 0, id: 1, typ: 1, version: 2},
		},
		{
			desc:     "reserved bits are set",
			data:     []byte{0b010_001_00, 1, 0, 0, 0, 0xA, 0, 0, 0, 1},
			expected: header{payloadLen: 0, id: 1, typ: 1, version: 1},
		},
		{
			desc:     "huge payload length",
			data:     []byte{8, 1, 0xff, 0xff, 0xff, 0xff, 0, 0, 0, 1},
			expected: header{payloadLen: 4294967285, id: 1, typ: 1, version: 2},
		},
		{
			desc:    "impossible message size",
			data:    []byte{8, 1, 0, 0, 0, 1, 0, 0, 0, 1},
			invalid: true,
		},
		{desc: "no data", data: []byte{}, invalid: true},
		{desc: "short data", data: []byte{0x1}, invalid: true},
	} {
		tc := tc
		if tc.desc == "" {
			tc.desc = "case " + strconv.Itoa(i)
		}
		t.Run(tc.desc, func(t *testing.T) {
			t.Parallel()

			errs := make(chan error, 1)
			p1, p2 := net.Pipe()
			go func() {
				if _, err := p1.Write(tc.data); err != nil {
					errs <- err
				}
				close(errs)
			}()

			if err := p2.SetDeadline(time.Now().Add(time.Millisecond * 100)); err != nil {
				t.Fatal(err)
			}

			lr, err := NewReader(WithConn(p2))
			if err != nil {
				t.Fatal(err)
			}

			if tc.invalid {
				if _, err := lr.readHeader(); err == nil {
					t.Error("expected error, but didn't get one")
				} else {
					// to make it a little easier to see what we get
					t.Logf("as expected, got an error: %s", err.Error())
				}
				return
			}

			msg, err := lr.readHeader()
			if err != nil {
				t.Error(err)
			} else if !reflect.DeepEqual(tc.expected, msg) {
				t.Errorf("expected %+v; got %+v", tc.expected, msg)
			}

			if err, ok := <-errs; ok && err != nil {
				t.Errorf("error during writing: %+v", err)
			}
		})
	}
}

func TestReader_newMessage(t *testing.T) {
	ack := newHdrOnlyMsg(KeepAliveAck)
	expMsg := msgOut{typ: KeepAliveAck}
	if expMsg != ack {
		t.Errorf("expected %+v; got %+v", expMsg, ack)
	}

	client, server := net.Pipe()

	errs := make(chan error, 1)
	v := uint8(1)
	mid := uint32(1)
	go func() {
		defer close(errs)
		lr, err := NewReader(WithConn(client), WithVersion(v))
		if err != nil {
			errs <- err
			return
		}
		if err := lr.writeHeader(mid, ack.length, ack.typ); err != nil {
			errs <- err
		}
	}()

	r, err := NewReader(WithConn(server))
	if err != nil {
		t.Fatal(err)
	}

	hdr, err := r.readHeader()
	if err != nil {
		t.Fatal(err)
	}

	expHdr := header{
		typ:     KeepAliveAck,
		version: v,
		id:      mid,
	}
	if expHdr != hdr {
		t.Errorf("expected %+v; got %+v", expHdr, hdr)
	}

	if err, ok := <-errs; ok && err != nil {
		t.Errorf("error during writing: %+v", err)
	}
}

func dummyRead(h *header, rfid net.Conn) error {
	buf := make([]byte, headerSz)
	if n, err := io.ReadFull(rfid, buf); err != nil {
		return errors.Wrapf(err, "read failed after %d bytes", n)
	}
	if err := h.UnmarshalBinary(buf); err != nil {
		return err
	}
	n, err := io.CopyN(ioutil.Discard, rfid, int64(h.payloadLen))
	if err != nil {
		return errors.Wrapf(err, "read failed after %d bytes", n)
	}
	return nil
}

func init() {
	// this is only useful during testing
	responseType[0] = ReaderEventNotification
}

func dummyReply(h *header, rfid net.Conn) error {
	reply := header{id: h.id, version: 1, typ: responseType[h.typ]}
	data, err := reply.MarshalBinary()
	if err != nil {
		return err
	}
	if n, err := rfid.Write(data); err != nil {
		return errors.Wrapf(err, "write failed after %d bytes", n)
	}
	return nil
}

func TestReader_Close(t *testing.T) {
	// a simple message: mid == 1, type == 1, 1 byte payload (0xF)
	data := []byte{4, 1, 0, 0, 0, 0xB, 0, 0, 0, 1, 0xF}

	client, rfid := net.Pipe()
	if err := client.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
		return
	}
	if err := rfid.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}

	r, err := NewReader(WithConn(client))
	if err != nil {
		t.Fatal(err)
	}

	wg := sync.WaitGroup{}
	wg.Add(2)
	errs := make(chan error, 2)
	go func() {
		defer wg.Done()
		errs <- r.Connect()
	}()

	// lazy RFID device: discard incoming messages & send empty replies
	go func() {
		defer wg.Done()
		var h header
		for _, proc := range []func(*header, net.Conn) error{
			dummyReply, // connection attempt
			dummyRead,  // incoming message
			dummyReply, // response
		} {
			if err := proc(&h, rfid); err != nil {
				errs <- err
				return
			}
		}
	}()

	resp, err := r.SendMessage(context.Background(), data, uint16(0))
	if err != nil {
		t.Error(err)
	} else if resp == nil {
		t.Error("expected non-nil response")
	}

	if err := r.Close(); err != nil {
		t.Error(err)
	}

	if err := r.Close(); !errors.Is(err, ErrReaderClosed) {
		t.Errorf("expected %q; got %+v", ErrReaderClosed, err)
	}

	wg.Wait()
	close(errs)
	for err := range errs {
		if !errors.Is(err, ErrReaderClosed) {
			t.Error(err)
		}
	}
}
