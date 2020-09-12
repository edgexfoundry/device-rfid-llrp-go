//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"github.impcloud.net/RSP-Inventory-Suite/device-llrp-go/internal/llrp"
	"io/ioutil"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
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

func check(err error) {
	if err == nil {
		return
	}

	if errors.Is(err, llrp.ErrClientClosed) {
		log.Info(err)
		return
	}

	log.Errorf("%v", err)
	os.Exit(1)
}

func checkf(f func() error) {
	check(f())
}

func logErr(err error) {
	if err == nil {
		return
	}

	log.Errorf("%v", err)
}

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

func main() {
	checkf(checkFlags)

	spec, err := getSpec()
	check(err)

	rconn, err := net.Dial("tcp", rfidAddr)
	check(err)
	defer rconn.Close()

	sig := newSignaler()

	c := getClient()
	go func() { logErr(c.Connect(rconn)) }()

	ctx := sig.watch(context.Background())

	if customPath != "" {
		check(sendCustom(ctx, c, customPath))
	}

	if confPath != "" {
		// check(sendConf(ctx, c, confPath))
	}

	if accessSpecPath != "" {
		// check(sendAccess(ctx, c, accessSpecPath))
	} else {
		// check(deleteAccess(ctx, c, 0))
	}

	startTime := time.Now()
	check(start(ctx, c, spec))
	watchROs(ctx)

	log.Info("attempting clean up... (send signal again to force stop)")
	ctx = sig.watch(context.Background())

	check(stop(ctx, c, spec))
	logErr(closeConn(ctx, c))

	elapsed := time.Since(startTime)

	for epc, td := range unique {
		if len(td.readTimes) > 1 {
			prev := td.readTimes[0]
			for i := range td.readTimes {
				prev, td.readTimes[i] = td.readTimes[i], (td.readTimes[i]-prev)/1000
			}
		}
		log.Infof("%s | cnt %d | MDID %#03x | Mdl %#03x | SN %#02x | XTID %t | times %d",
			epc,
			td.count,
			td.tid.MaskDesigner,
			td.tid.TagModelNumber,
			td.tid.Serial,
			td.tid.HasXTID,
			td.readTimes)
	}

	log.Infof("%d tags | %d reads in %v | rate: %f/s",
		len(unique), nReads, elapsed, float64(nReads)/elapsed.Seconds())
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

func getClient() *llrp.Client {
	opts := []llrp.ClientOpt{
		llrp.WithTimeout(3600 * time.Second),
		llrp.WithMessageHandler(llrp.MsgROAccessReport, llrp.MessageHandlerFunc(handleROAR)),
		llrp.WithMessageHandler(llrp.MsgReaderEventNotification, llrp.MessageHandlerFunc(func(c *llrp.Client, msg llrp.Message) {
			log.Info("Reader Event")
			ren := llrp.ReaderEventNotification{}
			if err := msg.UnmarshalTo(&ren); err != nil {
				logErr(err)
				return
			}
			prettyPrint(ren.ReaderEventNotificationData)
		})),
		llrp.WithLogger(nil),
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

type tagData struct {
	readTimes []uint64
	count     uint
	tid       TIDClassE2
}

var (
	nReads    uint
	unique    = map[string]tagData{}
	atomicTID = uint32(0)
	mask      = []byte{
		0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
		0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
		0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
		0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF,
	}

	bufTID = TIDClassE2{}
)

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

func handleROAR(c *llrp.Client, msg llrp.Message) {
	report := llrp.ROAccessReport{}
	if err := msg.UnmarshalTo(&report); err != nil {
		logErr(err)
		return
	}

	rpt := strings.Builder{}
	for _, tagReport := range report.TagReportData {

		if tagReport.EPC96.EPC != nil {
			fmt.Fprintf(&rpt, "% 02x", tagReport.EPC96.EPC)
		} else {
			fmt.Fprintf(&rpt, "% 02x", tagReport.EPCData.EPC)
		}

		epc := rpt.String()
		td, readBefore := unique[epc]
		readBefore = true

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
		}

		if tagReport.FirstSeenUTC != nil {
			fmt.Fprintf(&rpt, " | first %s",
				time.Unix(0, int64(*tagReport.FirstSeenUTC*1000)).Format(time.StampMicro))
		}

		if tagReport.LastSeenUTC != nil {
			fmt.Fprintf(&rpt, " | last %s",
				time.Unix(0, int64(*tagReport.LastSeenUTC*1000)).Format(time.StampMicro))
			td.readTimes = append(td.readTimes, uint64(*tagReport.LastSeenUTC))
		}

		if tagReport.TagSeenCount != nil {
			nReads += uint(*tagReport.TagSeenCount)
			fmt.Fprintf(&rpt, " | cnt %02d", *tagReport.TagSeenCount)
			td.count += uint(*tagReport.TagSeenCount)
		} else {
			nReads++
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
			fmt.Fprintf(&rpt, " | read %d: %#x", r.OpSpecID, r.Data)
			td.tid, _ = decodeTIDClassE2(r.Data)
		} else if !readBefore {
			var epcMatch []byte
			var nBits uint16
			if tagReport.EPC96.EPC != nil {
				epcMatch = make([]byte, len(tagReport.EPC96.EPC))
				copy(epcMatch, tagReport.EPC96.EPC)
				nBits = 96
			} else {
				epcMatch = make([]byte, len(tagReport.EPCData.EPC))
				copy(epcMatch, tagReport.EPC96.EPC)
				nBits = tagReport.EPCData.EPCNumBits
			}

			go func(epcMatch []byte, nBits uint16) {
				id := atomic.AddUint32(&atomicTID, 1)
				as := llrp.AddAccessSpec{
					AccessSpec: llrp.AccessSpec{
						AccessSpecID:  id,
						AirProtocolID: 1,
						Trigger: llrp.AccessSpecStopTrigger{
							Trigger:             llrp.AccessSpecStopTriggerOperationCount,
							OperationCountValue: 1,
						},
						AccessCommand: llrp.AccessCommand{
							C1G2TagSpec: llrp.C1G2TagSpec{
								TagPattern1: llrp.C1G2TargetTag{
									C1G2MemoryBank:     1,
									MatchFlag:          true,
									MostSignificantBit: 0x20,
									TagMaskNumBits:     nBits,
									TagMask:            mask[:1+((nBits-1)>>3)],
									TagDataNumBits:     nBits,
									TagData:            epcMatch,
								},
							},
							C1G2Read: &llrp.C1G2Read{
								OpSpecID:       uint16(id),
								C1G2MemoryBank: 2,
								WordAddress:    0,
								WordCount:      6,
							},
						},
					},
				}

				if err := send(context.Background(), c, &as, &llrp.AddAccessSpecResponse{}); err != nil {
					logErr(err)
					return
				}

				logErr(send(context.Background(), c,
					&llrp.EnableAccessSpec{AccessSpecID: id},
					&llrp.EnableAccessSpecResponse{}))
			}(epcMatch, nBits)
		}

		unique[epc] = td
		log.Info(rpt.String())
		rpt.Reset()
	}
}

func prettyPrint(v interface{}) {
	if pretty, err := json.MarshalIndent(v, "", "\t"); err != nil {
		log.Errorf("can't pretty print %+v: %+v", v, err)
	} else {
		log.Infof("%s", pretty)
	}
}
