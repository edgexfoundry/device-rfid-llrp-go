//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/pkg/errors"
	"github.impcloud.net/RSP-Inventory-Suite/device-llrp-go/internal/llrp"
	"io/ioutil"
	stdlog "log"
	"net"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

var rfidAddr, roSpecPath, accessSpecPath, confPath, customPath string
var doAdd, doEnable, doDisable, doDelete bool
var report llrp.TagReportContentSelector
var roSpecID uint
var watchTimeout time.Duration

func init() {
	flag.StringVar(&rfidAddr, "rfid", "", "ip:port of RFID reader")
	flag.StringVar(&confPath, "conf", "", "path to config JSON file to send before starting")
	flag.StringVar(&roSpecPath, "ro", "", "path to ROSpec JSON file; must be set if -add is true")
	flag.StringVar(&accessSpecPath, "access", "", "path to AccessSpec JSON file")
	flag.StringVar(&customPath, "custom", "", "path to JSON file with a list of Custom messages to send before ROSpec")
	flag.BoolVar(&doAdd, "add", true, "add ROSpec")
	flag.BoolVar(&doEnable, "enable", true, "enable ROSpec")
	flag.BoolVar(&doDisable, "disable", true, "disable ROSpec")
	flag.BoolVar(&doDelete, "delete", true, "delete ROSpec")

	flag.UintVar(&roSpecID, "id", 1, "ROSpecID to use; overrides ID in file, if -ro is given")
	flag.BoolVar(&report.EnableROSpecID, "ro-id", false, "report RO spec ID ")
	flag.BoolVar(&report.EnableSpecIndex, "spec-index", false, "report spec index ")
	flag.BoolVar(&report.EnableInventoryParamSpecID, "inventory-id", false, "report inventory param spec ID ")
	flag.BoolVar(&report.EnableAntennaID, "antenna-id", false, "report antenna ID ")
	flag.BoolVar(&report.EnableChannelIndex, "channel-index", false, "report channel index ")
	flag.BoolVar(&report.EnablePeakRSSI, "rssi", true, "report peak RSSI ")
	flag.BoolVar(&report.EnableFirstSeenTimestamp, "first-seen", true, "report first seen timestamp ")
	flag.BoolVar(&report.EnableLastSeenTimestamp, "last-seen", false, "report last seen timestamp ")
	flag.BoolVar(&report.EnableTagSeenCount, "tag-count", true, "report tag seen count ")
	flag.BoolVar(&report.EnableAccessSpecID, "access-spec-id", false, "report access spec id ")

	report.C1G2EPCMemorySelector = &llrp.C1G2EPCMemorySelector{}
	flag.BoolVar(&report.C1G2EPCMemorySelector.CRCEnabled, "crc", true, "report crc ")
	flag.BoolVar(&report.C1G2EPCMemorySelector.PCBitsEnabled, "pc", true, "report pc ")
	flag.BoolVar(&report.C1G2EPCMemorySelector.XPCBitsEnabled, "xpc", false, "report xpc")

	flag.DurationVar(&watchTimeout, "watch-for", 0,
		"watches reports until timeout or interrupt; forever if =0, never if <0")
}

// logger wraps the standard logger and provides some other common methods.
type logger struct {
	infolg *stdlog.Logger
	errlg  *stdlog.Logger
}

var log = logger{
	infolg: stdlog.New(os.Stdout, " [INFO] ", stdlog.Lmicroseconds),
	errlg:  stdlog.New(os.Stderr, "[ERROR] ", stdlog.Lmicroseconds),
}

func (l logger) Info(msg string) {
	l.infolg.Println(msg)
}

func (l logger) Infof(msg string, args ...interface{}) {
	l.infolg.Printf(msg, args...)
}

func (l logger) Errorf(msg string, args ...interface{}) {
	l.errlg.Printf(msg, args...)
}

// Implement the Client logger interface, but only log Handler panics

func (l logger) ReceivedMsg(_ llrp.Header, _ llrp.VersionNum) {
}

func (l logger) SendingMsg(_ llrp.Header) {
}

func (l logger) MsgHandled(_ llrp.Header) {
}

func (l logger) MsgUnhandled(_ llrp.Header) {
}

func (l logger) HandlerPanic(header llrp.Header, err error) {
	l.errlg.Printf("handler panic on %+v: %+v", header, err)
}

// logErr logs the input and returns true if it's a non-nil error
// other than one indicating a normal client shutdown;
// otherwise, it does nothing and returns false.
func logErr(err error) (ok bool) {
	if err == nil {
		return
	}

	if errors.Is(err, llrp.ErrClientClosed) {
		return
	}

	log.Errorf("%v", err)
	return true
}

// check logs an error and exits if the input is a non-nil error
// other than one indicating a normal client shutdown.
func check(err error) {
	if logErr(err) {
		os.Exit(1)
	}
}

// checkf checks the result of a function that returns an error.
func checkf(f func() error) {
	check(f())
}

// checkFlags validates the input flags.
func checkFlags() error {
	flag.Parse()
	if rfidAddr == "" {
		return errors.New("rfid address not set")
	}

	if doAdd && roSpecPath == "" {
		return errors.New("missing path to ROSpec JSON file")
	}

	return nil
}

// main loads LLRP messages from JSON files and sends them to a Reader,
// then watches the connection for ROAccessReports, which it prints to stdout.
func main() {
	checkf(checkFlags)

	spec, err := getSpec()
	check(err)

	rconn, err := net.Dial("tcp", rfidAddr)
	check(err)
	defer rconn.Close()

	sig := newSignaler()
	ri := newReadInfo()

	c := getClient(ri)
	go func() { logErr(c.Connect(rconn)) }()

	ctx := sig.watch(context.Background())

	if customPath != "" {
		check(sendCustom(ctx, c, customPath))
	}

	if confPath != "" {
		check(sendConf(ctx, c, confPath))
	}

	if accessSpecPath != "" {
		check(sendAccess(ctx, c, accessSpecPath))
	} else {
		check(deleteAccess(ctx, c, 0))
	}

	startTime := time.Now()
	check(start(ctx, c, spec))
	watchROs(ctx)

	log.Info("attempting clean up... (send signal again to force stop)")
	ctx = sig.watch(context.Background())

	check(stop(ctx, c, spec))
	logErr(closeConn(ctx, c))

	elapsed := time.Since(startTime)

	for epc, td := range ri.unique {
		log.Infof("%s | cnt %02d | avg rssi %02.2f dBm | avg time btw reads %02.2f ms",
			epc, td.count, td.rssis.Mean(), td.times.Mean())

		if td.tid.MaskDesigner != 0 {
			log.Infof("    MDID %#03x | Mdl %#03x | SN %#02x | XTID %t",
				td.tid.MaskDesigner,
				td.tid.TagModelNumber,
				td.tid.Serial,
				td.tid.HasXTID)
		}
	}

	log.Infof("%d total tags | %d total reads in %v | rate: %.2f tags/s | avg rssi over all tags %02.2f dBm",
		len(ri.unique), ri.nReads, elapsed, float64(ri.nReads)/elapsed.Seconds(), ri.allRssis.Mean())
}

type signaler struct {
	interrupt chan os.Signal
}

func newSignaler() *signaler {
	interrupt := make(chan os.Signal)
	signal.Notify(interrupt, os.Interrupt, syscall.SIGTERM)
	return &signaler{interrupt: interrupt}
}

func (s *signaler) watch(ctx context.Context) context.Context {
	child, cancel := context.WithCancel(ctx)
	go func() {
		select {
		case <-s.interrupt:
			cancel()
		case <-child.Done():
		}
	}()
	return child
}

type firstErr struct {
	err error
}

func (fe *firstErr) save(err error) {
	logErr(err)

	if fe.err == nil {
		fe.err = err
	}
}

func sendConf(ctx context.Context, c *llrp.Client, confPath string) error {
	data, err := ioutil.ReadFile(confPath)
	if err != nil {
		return err
	}

	conf := &llrp.SetReaderConfig{}
	if err := json.Unmarshal(data, conf); err != nil {
		return err
	}

	log.Info("sending config")
	return send(ctx, c, conf, &llrp.SetReaderConfigResponse{})
}

func sendCustom(ctx context.Context, c *llrp.Client, custom string) error {
	data, err := ioutil.ReadFile(custom)
	if err != nil {
		return err
	}

	msgs := &[]llrp.CustomMessage{}
	if err := json.Unmarshal(data, msgs); err != nil {
		return err
	}

	for i, cst := range *msgs {
		log.Infof("sending custom %d", i)
		logErr(send(ctx, c, &cst, &llrp.CustomMessage{}))
	}

	return nil
}

func deleteAccess(ctx context.Context, c *llrp.Client, id uint32) error {
	log.Info("deleting access spec")
	return send(ctx, c,
		&llrp.DeleteAccessSpec{AccessSpecID: id},
		&llrp.DeleteAccessSpecResponse{})
}

func sendAccess(ctx context.Context, c *llrp.Client, accessPath string) error {
	data, err := ioutil.ReadFile(accessPath)
	if err != nil {
		return err
	}

	as := llrp.AccessSpec{}
	if err := json.Unmarshal(data, &as); err != nil {
		return err
	}

	log.Info("sending access spec")
	e := firstErr{}
	_ = deleteAccess(ctx, c, as.AccessSpecID)
	e.save(send(ctx, c, &llrp.AddAccessSpec{AccessSpec: as}, &llrp.AddAccessSpecResponse{}))
	e.save(send(ctx, c,
		&llrp.EnableAccessSpec{AccessSpecID: as.AccessSpecID}, &llrp.EnableAccessSpecResponse{}))
	return e.err
}

// start sends the messages needed to add and enable an ROSpec.
//
// If either op returns an error, it logs the error, but continues on.
// The returned error is nil if both were successful,
// but otherwise it's the first error received.
//
// The user can disable one or both by setting the `add` or `enable` flags to false.
func start(ctx context.Context, c *llrp.Client, spec *llrp.ROSpec) error {
	fe := firstErr{}
	if doAdd {
		log.Info("adding ROSpec")
		fe.save(send(ctx, c, spec.Add(), &llrp.AddROSpecResponse{}))
	}

	if doEnable {
		log.Info("enabling ROSpec")
		fe.save(send(ctx, c, spec.Enable(), &llrp.EnableROSpecResponse{}))
	}

	return fe.err
}

// watchROs waits until the context is canceled or the timeout duration expires.
// If the timeout flag is negative, it logs a message and returns.
func watchROs(ctx context.Context) {
	if watchTimeout < 0 {
		log.Info("not waiting for ROAccessReports because timeout is negative")
		return
	}

	watchMsg := "watching for ROAccessReports until interrupted"
	watchCtx := ctx
	if watchTimeout > 0 {
		var cancel context.CancelFunc
		watchCtx, cancel = context.WithTimeout(ctx, watchTimeout)
		defer cancel()
		watchMsg += " or until " + watchTimeout.String() + " elapses"
	}

	log.Info(watchMsg) // let the user know how to stop
	<-watchCtx.Done()
	log.Info("done watching ROAccessReports")
}

// stop sends the messages to disable and delete an ROSpec.
// The user can disable one or both by setting the `disable` or `delete` flags to false.
func stop(ctx context.Context, c *llrp.Client, spec *llrp.ROSpec) error {
	fe := firstErr{}

	if doDisable {
		log.Info("disabling ROSpec")
		fe.save(send(ctx, c, spec.Disable(), &llrp.DisableROSpecResponse{}))
	}

	if doDelete {
		log.Info("deleting ROSpec")
		fe.save(send(ctx, c, spec.Delete(), &llrp.DeleteROSpecResponse{}))
	}

	return fe.err
}

func getClient(ri *readInfo) *llrp.Client {
	opts := []llrp.ClientOpt{
		llrp.WithTimeout(3600 * time.Second),
		llrp.WithMessageHandler(llrp.MsgROAccessReport, llrp.MessageHandlerFunc(ri.handleROAR)),
		llrp.WithMessageHandler(llrp.MsgReaderEventNotification, llrp.MessageHandlerFunc(func(c *llrp.Client, msg llrp.Message) {
			log.Info("Reader Event")
			ren := llrp.ReaderEventNotification{}
			if err := msg.UnmarshalTo(&ren); err != nil {
				logErr(err)
				return
			}
			prettyPrint(ren.ReaderEventNotificationData)
		})),
		llrp.WithLogger(log),
	}

	return llrp.NewClient(opts...)
}

func getSpec() (*llrp.ROSpec, error) {
	if doAdd && roSpecPath == "" {
		return nil, errors.New("missing ROSpec file path; required when add is true")
	} else if roSpecPath == "" {
		return &llrp.ROSpec{}, nil
	}

	data, err := ioutil.ReadFile(roSpecPath)
	if err != nil {
		return nil, err
	}

	spec := &llrp.ROSpec{}
	if err := json.Unmarshal(data, spec); err != nil {
		return nil, err
	}

	spec.ROSpecID = uint32(roSpecID)

	if spec.ROReportSpec == nil {
		spec.ROReportSpec = &llrp.ROReportSpec{
			Trigger: llrp.NSecondsOrROEnd,
			N:       5,
		}
	}

	if spec.ROReportSpec.TagReportContentSelector.C1G2EPCMemorySelector == nil {
		spec.ROReportSpec.TagReportContentSelector.C1G2EPCMemorySelector = new(llrp.C1G2EPCMemorySelector)
	}

	for _, b := range []struct {
		sel *bool
		arg bool
	}{
		{&spec.ROReportSpec.TagReportContentSelector.EnableROSpecID, report.EnableROSpecID},
		{&spec.ROReportSpec.TagReportContentSelector.EnableSpecIndex, report.EnableSpecIndex},
		{&spec.ROReportSpec.TagReportContentSelector.EnableInventoryParamSpecID, report.EnableInventoryParamSpecID},
		{&spec.ROReportSpec.TagReportContentSelector.EnableAntennaID, report.EnableAntennaID},
		{&spec.ROReportSpec.TagReportContentSelector.EnableChannelIndex, report.EnableChannelIndex},
		{&spec.ROReportSpec.TagReportContentSelector.EnablePeakRSSI, report.EnablePeakRSSI},
		{&spec.ROReportSpec.TagReportContentSelector.EnableFirstSeenTimestamp, report.EnableFirstSeenTimestamp},
		{&spec.ROReportSpec.TagReportContentSelector.EnableLastSeenTimestamp, report.EnableLastSeenTimestamp},
		{&spec.ROReportSpec.TagReportContentSelector.EnableTagSeenCount, report.EnableTagSeenCount},
		{&spec.ROReportSpec.TagReportContentSelector.EnableAccessSpecID, report.EnableAccessSpecID},
		{&spec.ROReportSpec.TagReportContentSelector.C1G2EPCMemorySelector.CRCEnabled, report.C1G2EPCMemorySelector.CRCEnabled},
		{&spec.ROReportSpec.TagReportContentSelector.C1G2EPCMemorySelector.PCBitsEnabled, report.C1G2EPCMemorySelector.PCBitsEnabled},
		{&spec.ROReportSpec.TagReportContentSelector.C1G2EPCMemorySelector.XPCBitsEnabled, report.C1G2EPCMemorySelector.XPCBitsEnabled},
	} {
		if b.arg {
			*b.sel = true
		}
	}

	return spec, nil
}

func send(ctx context.Context, c *llrp.Client, out llrp.Outgoing, in llrp.Incoming) error {
	prettyPrint(out)

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	err := c.SendFor(ctx, out, in)
	switch in.Type() {
	// These are the only Responses that have non-Status information
	case llrp.MsgGetReaderCapabilitiesResponse, llrp.MsgGetReaderConfigResponse,
		llrp.MsgGetROSpecsResponse, llrp.MsgGetAccessSpecsResponse,
		llrp.MsgGetSupportedVersionResponse, llrp.MsgErrorMessage:
		prettyPrint(in)
	}

	return err
}

func closeConn(ctx context.Context, c *llrp.Client) error {
	ctx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	if err := c.Shutdown(ctx); err != nil && !errors.Is(err, llrp.ErrClientClosed) {
		if err := c.Close(); err != nil && !errors.Is(err, llrp.ErrClientClosed) {
			return err
		}
		return err
	}
	return nil
}

type movingMean struct {
	values []int64
	total  int64
	index  int
}

const defMaxValues = 50

func (mm *movingMean) add(i int64) {
	if cap(mm.values) == 0 {
		mm.values = make([]int64, 0, defMaxValues)
	}

	if (i < 0 && (-1<<63)-i > mm.total) || (mm.total > (1<<63-1)-i && i > 0) {
		log.Errorf("overflow: %d + %d: %d, %d, %d",
			mm.total, i, mm.total+i, (-1<<63)-i, (1<<63-1)-i)
	}

	mm.total += i
	if len(mm.values) < cap(mm.values) {
		mm.values = append(mm.values, i)
		return
	}

	mm.total -= mm.values[mm.index]
	mm.values[mm.index] = i
	mm.index++
	if mm.index >= cap(mm.values) {
		mm.index = 0
	}
}

func (mm *movingMean) Mean() float64 {
	if len(mm.values) == 0 {
		return 0
	}

	return float64(mm.total) / float64(len(mm.values))
}

type intervalMean struct {
	last int64
	mm   movingMean
}

func (iv *intervalMean) addInterval(i int64) {
	if cap(iv.mm.values) == 0 {
		iv.mm.values = make([]int64, 0, defMaxValues)
	} else if i < iv.last {
		return
	} else {
		iv.mm.add(i - iv.last)
	}
	iv.last = i
}

func (iv *intervalMean) Mean() float64 {
	if len(iv.mm.values) == 0 {
		return float64(iv.last)
	}

	return float64(iv.mm.total) / float64(len(iv.mm.values))
}

type tagData struct {
	times intervalMean
	rssis movingMean
	tid   TIDClassE2
	count uint
}

// readInfo tracks tags that were singulated.
//
// These fields aren't protected with sync primitives because
// they're only used in a single message handler of a single llrp.Client
// (which blocks new reads until it completes)
// or after the connection is closed.
type readInfo struct {
	nReads   uint
	unique   map[string]tagData
	allRssis movingMean
}

func newReadInfo() *readInfo {
	return &readInfo{
		unique: map[string]tagData{},
	}
}

// TIDClassE2 represents the EPCglobal Class ID information
// a tag might store in its TID bank.
// If it does, then the TID memory begins with 0xE2.
type TIDClassE2 struct {
	Serial           uint64
	TagModelNumber   uint16 // only 12 bits
	MaskDesigner     uint16 // only 9 bits
	SupportsFileOpen bool
	SupportsAuth     bool
	HasXTID          bool
}

func decodeTIDClassE2(words []uint16) (tid TIDClassE2, ok bool) {
	ok = len(words) >= 6 && (words[0]&^0xE200) != 0
	if !ok {
		return
	}

	tid.HasXTID = words[0]&0x0080 != 0
	tid.SupportsAuth = words[0]&0x0040 != 0
	tid.SupportsFileOpen = words[0]&0x0020 != 0
	tid.MaskDesigner = (words[0] & 0x001f) | (words[1] >> 12)
	tid.TagModelNumber = words[1] & 0x0fff
	tid.Serial = uint64(words[5])<<48 | uint64(words[4])<<32 |
		uint64(words[3])<<16 | uint64(words[2])
	return
}

func (ri *readInfo) handleROAR(_ *llrp.Client, msg llrp.Message) {
	report := llrp.ROAccessReport{}
	if err := msg.UnmarshalTo(&report); err != nil {
		logErr(err)
		return
	}

	rpt := strings.Builder{}
	for _, tagReport := range report.TagReportData {

		var epc string
		if tagReport.EPC96.EPC != nil {
			epc = hex.EncodeToString(tagReport.EPC96.EPC)
			fmt.Fprintf(&rpt, "% 02x", tagReport.EPC96.EPC)
		} else {
			epc = hex.EncodeToString(tagReport.EPCData.EPC)
			fmt.Fprintf(&rpt, "% 02x", tagReport.EPCData.EPC)
		}

		td := ri.unique[epc]

		if tagReport.ROSpecID != nil {
			fmt.Fprintf(&rpt, " | id %02d", *tagReport.ROSpecID)
		}

		if tagReport.SpecIndex != nil {
			fmt.Fprintf(&rpt, " | idx %02d", *tagReport.SpecIndex)
		}

		if tagReport.InventoryParameterSpecID != nil {
			fmt.Fprintf(&rpt, " | inv %02d", *tagReport.InventoryParameterSpecID)
		}

		if tagReport.AntennaID != nil {
			fmt.Fprintf(&rpt, " | ant %02d", *tagReport.AntennaID)
		}

		if tagReport.ChannelIndex != nil {
			fmt.Fprintf(&rpt, " | ch %02d", *tagReport.ChannelIndex)
		}

		if tagReport.PeakRSSI != nil {
			fmt.Fprintf(&rpt, " | rssi %02d", *tagReport.PeakRSSI)
			td.rssis.add(int64(*tagReport.PeakRSSI))
			ri.allRssis.add(int64(*tagReport.PeakRSSI))
		}

		if tagReport.FirstSeenUTC != nil {
			fmt.Fprintf(&rpt, " | first %s",
				time.Unix(0, int64(*tagReport.FirstSeenUTC*1000)).Format(time.StampMicro))
		}

		if tagReport.LastSeenUTC != nil {
			fmt.Fprintf(&rpt, " | last %s",
				time.Unix(0, int64(*tagReport.LastSeenUTC*1000)).Format(time.StampMicro))
			td.times.addInterval(int64(*tagReport.LastSeenUTC) / 1000)
		}

		if tagReport.TagSeenCount != nil {
			ri.nReads += uint(*tagReport.TagSeenCount)
			fmt.Fprintf(&rpt, " | cnt %02d", *tagReport.TagSeenCount)
			td.count += uint(*tagReport.TagSeenCount)
		} else {
			ri.nReads++
			td.count++
		}

		if tagReport.AccessSpecID != nil {
			fmt.Fprintf(&rpt, " | acc %02d", *tagReport.AccessSpecID)
		}

		if tagReport.C1G2CRC != nil {
			fmt.Fprintf(&rpt, " | crc %05d", *tagReport.C1G2CRC)
		}

		if tagReport.C1G2PC != nil {
			fmt.Fprintf(&rpt, " | epc len %02d", tagReport.C1G2PC.EPCMemoryLength)
			fmt.Fprintf(&rpt, " | umem %t", tagReport.C1G2PC.HasUserMemory)
			fmt.Fprintf(&rpt, " | xpc %t", tagReport.C1G2PC.HasXPC)
			if tagReport.C1G2PC.IsISO15961 {
				fmt.Fprintf(&rpt, " | afi  %08b", tagReport.C1G2PC.AttributesOrAFI)
			}
		}

		if tagReport.C1G2XPCW1 != nil {
			fmt.Fprintf(&rpt, " | xpcw1 %#x", *tagReport.C1G2XPCW1)
		}

		if tagReport.C1G2XPCW2 != nil {
			fmt.Fprintf(&rpt, " | xpcw2 %#x", *tagReport.C1G2XPCW2)
		}

		for i, c := range tagReport.Custom {
			fmt.Fprintf(&rpt, " | cstm[%d]<%d,%d> %#x", i, c.VendorID, c.Subtype, c.Data)
		}

		if tagReport.C1G2ReadOpSpecResult != nil {
			r := *tagReport.C1G2ReadOpSpecResult
			fmt.Fprintf(&rpt, " | read %d, result %d: %s",
				r.OpSpecID, r.C1G2ReadOpSpecResultType, wordsToHex(r.Data))
			td.tid, _ = decodeTIDClassE2(r.Data)
		}

		ri.unique[epc] = td
		log.Info(rpt.String())
		rpt.Reset()
	}
}

const hexChars = "0123456789abcdef"

// wordsToHex converts a word slice to a hex string.
func wordsToHex(src []uint16) string {
	dst := make([]byte, len(src)*4)

	i := 0
	for _, word := range src {
		dst[i+0] = hexChars[(word>>0xC)&0xF]
		dst[i+1] = hexChars[(word>>0x8)&0xF]
		dst[i+2] = hexChars[(word>>0x4)&0xF]
		dst[i+3] = hexChars[(word>>0x0)&0xF]
		i += 4
	}

	return string(dst)
}

// prettyPrint marshals the interface to JSON with indentation and logs it.
func prettyPrint(v interface{}) {
	if pretty, err := json.MarshalIndent(v, "  ", "  "); err != nil {
		log.Errorf("can't pretty print %+v: %+v", v, err)
	} else {
		log.Infof("%T:\n  %s", v, pretty)
	}
}
