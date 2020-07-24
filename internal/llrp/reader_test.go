//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package llrp

import (
	"context"
	"encoding/binary"
	"encoding/hex"
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

func TestClient_readHeader(t *testing.T) {
	type testCase struct {
		desc     string
		data     []byte
		expected Header
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
			expected: Header{payloadLen: 0, id: 1, typ: 1, version: 2},
		},
		{
			desc:     "reserved bits are set",
			data:     []byte{0b010_001_00, 1, 0, 0, 0, 0xA, 0, 0, 0, 1},
			expected: Header{payloadLen: 0, id: 1, typ: 1, version: 1},
		},
		{
			desc:     "huge payload length",
			data:     []byte{8, 1, 0xff, 0xff, 0xff, 0xff, 0, 0, 0, 1},
			expected: Header{payloadLen: 4294967285, id: 1, typ: 1, version: 2},
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

			lr := NewClient()
			lr.conn = p2

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

func TestClient_newMessage(t *testing.T) {
	ack := NewHdrOnlyMsg(MsgKeepAliveAck)
	expMsg := Message{Header: Header{version: versionMin, typ: MsgKeepAliveAck}}
	if expMsg != ack {
		t.Errorf("expected %+v; got %+v", expMsg, ack)
	}

	client, server := net.Pipe()

	errs := make(chan error, 1)
	v := Version1_0_1
	go func() {
		defer close(errs)
		lr := NewClient(WithVersion(v))
		lr.conn = client
		if err := lr.writeHeader(ack.Header); err != nil {
			errs <- err
		}
	}()

	r := NewClient()
	r.conn = server

	hdr, err := r.readHeader()
	if err != nil {
		t.Fatal(err)
	}

	expHdr := Header{
		typ:     MsgKeepAliveAck,
		version: v,
		id:      messageID(0),
	}
	if expHdr != hdr {
		t.Errorf("expected %+v; got %+v", expHdr, hdr)
	}

	if err, ok := <-errs; ok && err != nil {
		t.Errorf("error during writing: %+v", err)
	}
}

// dummyRead reads a header into h and discards the payload, if present.
func dummyRead(h *Header, rfid net.Conn) error {
	buf := make([]byte, HeaderSz)
	if n, err := io.ReadFull(rfid, buf); err != nil {
		return errors.Wrapf(err, "header read failed after %d bytes", n)
	}
	if err := h.UnmarshalBinary(buf); err != nil {
		return err
	}
	n, err := io.CopyN(ioutil.Discard, rfid, int64(h.payloadLen))
	if err != nil {
		return errors.Wrapf(err, "payload read failed after %d bytes", n)
	}
	return nil
}

// connectSuccess writes a ReaderEventNotification indicating successful connection.
func connectSuccess(h *Header, rfid net.Conn) error {
	d, _ := hex.DecodeString("043f000000200000000000f600160080000c0005a738133c2c9e010000060000")
	_, err := rfid.Write(d)
	return err
}

func closeSuccess(h *Header, rfid net.Conn) error {
	d, _ := hex.DecodeString("04040000000000000000011f000800000000")
	binary.BigEndian.PutUint32(d[2:], uint32(len(d)))
	binary.BigEndian.PutUint32(d[6:], uint32(h.id))
	_, err := rfid.Write(d)
	return err
}

// dummyReply sends empty replies with the header's response type.
func dummyReply(h *Header, rfid net.Conn) error {
	reply := Header{id: h.id, version: h.version, typ: responseType[h.typ]}
	hdr, err := reply.MarshalBinary()
	if err != nil {
		return err
	}
	if n, err := rfid.Write(hdr); err != nil {
		return errors.Wrapf(err, "write failed after %d bytes", n)
	}
	return nil
}

// randomReply returns a function that sends replies with a random payload
// such that minSz <= len(payload) <= maxSz.
func randomReply(minSz, maxSz uint32) func(h *Header, rfid net.Conn) error {
	rnd := rand.New(rand.NewSource(1))
	if minSz > maxSz {
		panic(errors.Errorf("bad sizes: %d > %d", minSz, maxSz))
	}
	dist := int64(maxSz - minSz)
	min64 := int64(minSz)

	return func(h *Header, rfid net.Conn) error {
		// Use int64 to get full uint32; CopyN needs an int64 anyway.
		sz := rnd.Int63n(dist) + min64
		reply := Header{
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

		if sz == 0 {
			return nil
		}

		if n, err := io.CopyN(rfid, rnd, sz); err != nil {
			return errors.Wrapf(err, "write data failed after %d bytes", n)
		}
		return nil
	}
}

func TestClient_SendNotConnected(t *testing.T) {
	client, rfid := net.Pipe()
	if err := client.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
		return
	}
	if err := rfid.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}

	r := NewClient()
	r.conn = client

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, _, err := r.SendMessage(ctx, MessageType(0), nil); context.DeadlineExceeded != err {
		t.Errorf("expected DeadlineExceeded; got: %v", err)
	}
}

func TestClient_Connection(t *testing.T) {
	const tout = 3 * time.Second

	// Connect to a Reader, send some messages, close the Client.
	client, rfid := net.Pipe()

	opts := []ClientOpt{
		WithVersion(Version1_0_1),
		WithTimeout(tout),
	}

	if !testing.Verbose() {
		opts = append(opts, WithLogger(nil))
	}

	c := NewClient(opts...)

	// start a connection attempt
	connErrs := make(chan error, 1)
	go func() {
		defer close(connErrs)
		connErrs <- errors.Wrap(c.Connect(client), "connect failed")
	}()

	// lazy RFID device: discard incoming messages & send empty replies
	rfidErrs := make(chan error, 1)
	go func() {
		defer close(rfidErrs)
		defer rfid.Close()
		var err error

		h := Header{version: Version1_0_1}
		for _, op := range []func(*Header, net.Conn) error{
			connectSuccess,
			dummyRead,  // CustomMessage
			dummyReply, // response
			dummyRead,  // CloseConnection
			closeSuccess,
		} {
			err = op(&h, rfid)
			if err != nil {
				break
			}
		}
		rfidErrs <- errors.Wrap(err, "mock failed")
	}()

	data := []byte{1, 2, 3, 4, 5, 6, 7, 8}
	_, resp, err := c.SendMessage(context.Background(), MsgCustomMessage, data)
	if err != nil {
		t.Error(err)
	} else if resp == nil {
		t.Error("expected non-nil response")
	}

	if err := c.Shutdown(context.Background()); err != nil {
		t.Errorf("%+v", err)
		_ = c.Close()
	}
	client.Close()

	for err := range connErrs {
		if !errors.Is(err, ErrClientClosed) {
			t.Errorf("connection error: %+v", err)
		}
	}

	if _, err := rfid.Write([]byte{1}); err == nil {
		t.Error("expected connection to be closed, but write succeeded")
	}

	if err := c.Close(); !errors.Is(err, ErrClientClosed) {
		t.Errorf("expected %q; got %+v", ErrClientClosed, err)
	}

	_, _, err = c.SendMessage(context.Background(), MsgCustomMessage, data)
	if err := c.Close(); !errors.Is(err, ErrClientClosed) {
		t.Errorf("expected %q; got %+v", ErrClientClosed, err)
	}

	for err := range rfidErrs {
		if err != nil && !errors.Is(err, io.EOF) {
			t.Errorf("RFID reader error: %+v", err)
		}
	}
}

func TestClient_ManySenders(t *testing.T) {
	client, rfid := net.Pipe()
	if err := client.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
		return
	}
	if err := rfid.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}

	var c *Client
	if testing.Verbose() {
		c = NewClient(WithVersion(Version1_0_1))
	} else {
		c = NewClient(WithVersion(Version1_0_1), WithLogger(nil))
	}

	// start a connection
	connErrs := make(chan error, 1)
	go func() {
		defer close(connErrs)
		connErrs <- c.Connect(client)
	}()

	// RFID device: discard incoming messages; send random replies
	rfidErrs := make(chan error, 1)
	go func() {
		defer close(rfidErrs)
		var h Header
		err := connectSuccess(&h, rfid)
		op, nextOp := dummyRead, randomReply(0, 1024)
		s1, s2 := "read", "reply"
		for err == nil {
			err = op(&h, rfid)
			op, nextOp = nextOp, op
			s1, s2 = s2, s1
		}
		rfidErrs <- errors.Wrap(err, "mock failed")
	}()

	// send a bunch of messages all at once
	senders := 300
	msgGrp := sync.WaitGroup{}
	msgGrp.Add(senders)
	sendErrs := make(chan error, senders)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*60)
	defer cancel()
	for i := 0; i < senders; i++ {
		go func() {
			defer msgGrp.Done()

			sz := rand.Int31n(1024)
			data := make([]byte, sz)
			rand.Read(data)

			_, _, err := c.SendMessage(ctx, MsgCustomMessage, data)
			if err != nil {
				sendErrs <- err
			}
		}()
	}

	msgGrp.Wait()
	t.Log("closing")
	_ = c.Close()
	client.Close()

	close(sendErrs)
	for err := range sendErrs {
		t.Errorf("send error: %+v", err)
	}

	for err := range connErrs {
		if !(errors.Is(err, ErrClientClosed) ||
			errors.Is(err, io.ErrClosedPipe)) {
			t.Errorf("connect error: %+v", err)
		}
	}

	for err := range rfidErrs {
		if !errors.Is(err, io.EOF) {
			t.Errorf("mock error: %+v", err)
		}
	}

}

func BenchmarkReader_ManySenders(b *testing.B) {
	client, rfid := net.Pipe()
	if err := client.SetDeadline(time.Now().Add(300 * time.Second)); err != nil {
		b.Fatal(err)
		return
	}
	if err := rfid.SetDeadline(time.Now().Add(300 * time.Second)); err != nil {
		b.Fatal(err)
	}

	r := NewClient(WithLogger(nil), WithVersion(Version1_0_1))

	// start a connection
	connErrs := make(chan error, 1)
	go func() {
		defer close(connErrs)
		connErrs <- r.Connect(client)
	}()

	// RFID device: discard incoming messages; send random replies
	rfidErrs := make(chan error, 1)
	go func() {
		defer close(rfidErrs)
		var h Header
		err := connectSuccess(&h, rfid)
		op, nextOp := dummyRead, randomReply(0, 1024)
		s1, s2 := "read", "reply"
		for err == nil {
			err = op(&h, rfid)
			op, nextOp = nextOp, op
			s1, s2 = s2, s1
		}
		rfidErrs <- errors.Wrap(err, "mock failed")
	}()

	// send a bunch of messages all at once
	senders := b.N
	msgGrp := sync.WaitGroup{}
	msgGrp.Add(senders)
	sendErrs := make(chan error, senders)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*300)
	defer cancel()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		go func() {
			defer msgGrp.Done()

			sz := rand.Int31n(1024)
			data := make([]byte, sz)
			rand.Read(data)

			_, _, err := r.SendMessage(ctx, MsgCustomMessage, data)
			if err != nil {
				sendErrs <- err
			}
		}()
	}
	msgGrp.Wait()
	b.Log("closing")

	if err := r.Shutdown(context.Background()); err != nil {
		b.Error(err)
	}
	b.StopTimer()

	close(sendErrs)
	for err := range sendErrs {
		b.Errorf("send error: %+v", err)
	}

	for err := range connErrs {
		if !errors.Is(err, ErrClientClosed) {
			b.Errorf("connect error: %+v", err)
		}
	}

	for err := range rfidErrs {
		if !errors.Is(err, io.EOF) {
			b.Errorf("mock error: %+v", err)
		}
	}

}
