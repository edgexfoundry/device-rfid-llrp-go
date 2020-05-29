//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package llrp

import (
	"context"
	"github.com/pkg/errors"
	"io"
	"io/ioutil"
	"math/rand"
	"net"
	"reflect"
	"strconv"
	"sync"
	"testing"
	"time"
)

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
	v := Version1_0_1
	mid := messageID(1)
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
	// this is only useful during testing; lets dummyReply send initial message on connect
	responseType[0] = ReaderEventNotification
}

func dummyReply(h *header, rfid net.Conn) error {
	reply := header{id: h.id, version: h.version, typ: responseType[h.typ]}
	data, err := reply.MarshalBinary()
	if err != nil {
		return err
	}
	if n, err := rfid.Write(data); err != nil {
		return errors.Wrapf(err, "write failed after %d bytes", n)
	}
	return nil
}

// randomReply returns a function that sends replies with random data.
func randomReply(minSz, maxSz uint32) func(h *header, rfid net.Conn) error {
	rnd := rand.New(rand.NewSource(1))
	if minSz > maxSz {
		panic(errors.Errorf("bad sizes: %d > %d", minSz, maxSz))
	}
	dist := int64(maxSz - minSz)
	min64 := int64(minSz)

	return func(h *header, rfid net.Conn) error {
		// Use int64 to get full uint32; CopyN needs an int64 anyway.
		sz := rnd.Int63n(dist) + min64
		reply := header{
			payloadLen: uint32(sz),
			id:         h.id,
			typ:        responseType[h.typ],
			version:    h.version,
		}

		data, err := reply.MarshalBinary()
		if err != nil {
			return err
		}
		if n, err := rfid.Write(data); err != nil {
			return errors.Wrapf(err, "write header failed after %d bytes", n)
		}
		if n, err := io.CopyN(rfid, rnd, sz); err != nil {
			return errors.Wrapf(err, "write data failed after %d bytes", n)
		}
		return nil
	}
}

func TestReader_Connection(t *testing.T) {
	// Connect to a Reader, send some messages, close the Reader.
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

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := r.SendMessage(ctx, nil, messageType(0)); context.DeadlineExceeded != err {
		t.Errorf("expected DeadlineExceeded; got: %v", err)
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

	data := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	resp, err := r.SendMessage(context.Background(), data, messageType(0))
	if err != nil {
		t.Error(err)
	} else if resp == nil {
		t.Error("expected non-nil response")
	}

	// The first time we Close should be fine.
	if err := r.Close(); err != nil {
		t.Error(err)
	}
	wg.Wait()

	if _, err := rfid.Write([]byte{1}); err == nil {
		t.Error("expected connection to be closed, but write succeeded")
	}

	if err := r.Close(); !errors.Is(err, ErrReaderClosed) {
		t.Errorf("expected %q; got %+v", ErrReaderClosed, err)
	}

	_, err = r.SendMessage(context.Background(), data, messageType(0))
	if err := r.Close(); !errors.Is(err, ErrReaderClosed) {
		t.Errorf("expected %q; got %+v", ErrReaderClosed, err)
	}

	close(errs)
	for err := range errs {
		if !errors.Is(err, ErrReaderClosed) {
			t.Error(err)
		}
	}
}

func TestReader_ManySenders(t *testing.T) {
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

	connGrp := sync.WaitGroup{}
	connGrp.Add(2)
	errs := make(chan error, 2)
	go func() {
		defer connGrp.Done()
		errs <- r.Connect()
	}()

	// RFID device: discard incoming messages; send random replies
	done := make(chan struct{})
	go func() {
		defer connGrp.Done()
		var h header
		err := dummyReply(&h, rfid) // initial connection message
		op, nextOp := dummyRead, randomReply(0, 1024)
		for err == nil {
			select {
			case <-done:
				return
			default:
				err = op(&h, rfid)
				op, nextOp = nextOp, op
			}
		}
		errs <- err
	}()

	msgGrp := sync.WaitGroup{}
	senders := 100
	msgGrp.Add(senders)
	sendErrs := make(chan error, senders)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*60)
	defer cancel()
	for i := 0; i < senders; i++ {
		go func() {
			defer msgGrp.Done()
			sz := rand.Int31n(1024)
			data := make([]byte, sz)
			_, err := r.SendMessage(ctx, data, messageType(0))
			if err != nil {
				sendErrs <- err
				return
			}
		}()
	}

	msgGrp.Wait()
	cancel()
	close(sendErrs)
	for err := range sendErrs {
		if err != nil {
			t.Error(err)
		}
	}

	close(done)
	if err := r.Close(); err != nil {
		t.Error(err)
	}
	connGrp.Wait()

	close(errs)
	for err := range errs {
		if !errors.Is(err, ErrReaderClosed) {
			t.Errorf("%+v", err)
		}
	}
}

func TestParamReader(t *testing.T) {
	errDesc := "error description"
	errLen := uint16(len(errDesc))

	payload := append([]byte{
		0x01, 0x1F, // type 287, LLRPStatus Parameter
		0x00, 0x00, // total length (including 4 byte header)
		0x00, 0x00, // StatusCode
		uint8(errLen >> 8), uint8(errLen & 0xFF), // Error Description ByteCount
	}, []byte(errDesc)...)
	payload[2] = uint8(len(payload) >> 8)
	payload[3] = uint8(len(payload) & 0xFF)
	msg, err := newByteMessage(payload, LLRPErrorMessage)
	if err != nil {
		t.Fatal(err)
	}

	pr := newParamReader(msg)

	pTyp, err := pr.next()
	if err != nil {
		t.Fatal(err)
	}
	if LLRPStatusParam != pTyp {
		t.Fatalf("expected %v; got %v", LLRPStatusParam, pTyp)
	}

	t.Log(pr.cur)
	ls, err := pr.llrpStatus(pr.cur.length)
	if err != nil {
		t.Fatalf("%+v", err)
	}
	t.Logf("%+v", ls)
}
