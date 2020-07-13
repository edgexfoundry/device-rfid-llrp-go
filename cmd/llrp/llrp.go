//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"bytes"
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
	"syscall"
	"time"
)

var rfidAddr, roSpecPath string
var doAdd, doEnable, doDisable, doDelete bool
var report llrp.TagReportContentSelector
var roSpecID uint
var watchTimeout time.Duration

func init() {
	flag.StringVar(&rfidAddr, "rfid", "", "ip:port of RFID reader")
	flag.StringVar(&roSpecPath, "ro", "", "path to ROSpec JSON file; must be set if -add is true")
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

	c, err := getClient(rconn)
	check(err)

	sig := newSignaler()

	go func() { logErr(c.Connect()) }()

	ctx := sig.watch(context.Background())
	check(start(ctx, c, spec))
	watchROs(ctx)

	log.Info("attempting clean up... (send signal again to force stop)")
	ctx = sig.watch(context.Background())

	check(stop(ctx, c, spec))
	logErr(closeConn(ctx, c))
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

func getClient(rconn net.Conn) (*llrp.Client, error) {
	logger := log.New()
	logger.SetLevel(log.PanicLevel)

	opts := []llrp.ClientOpt{
		llrp.WithConn(rconn),
		llrp.WithLogger(logger),
		llrp.WithTimeout(3600 * time.Second),
		llrp.WithMessageHandler(llrp.MsgROAccessReport, llrp.MessageHandlerFunc(handleROAR)),
	}

	return llrp.NewClient(opts...)
}

func getSpec() (*llrp.ROSpec, error) {
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

	spec.ROReportSpec.TagReportContentSelector = report
	return spec, nil
}

func send(ctx context.Context, c *llrp.Client, out llrp.Outgoing, in llrp.Incoming) error {
	prettyPrint(out)

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	err := c.SendFor(ctx, out, in)
	prettyPrint(in) // print this anyway, as it might have useful information
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

func handleROAR(_ *llrp.Client, msg llrp.Message) {
	report := llrp.ROAccessReport{}
	if err := msg.UnmarshalTo(&report); err != nil {
		logErr(err)
		return
	}

	rpt := bytes.Buffer{}
	for _, tagReport := range report.TagReportData {
		if tagReport.EPC96.EPC != nil {
			fmt.Fprintf(&rpt, "% 02x", tagReport.EPC96.EPC)
		} else {
			fmt.Fprintf(&rpt, "% 02x", tagReport.EPCData.EPC)
		}

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
		}
		if tagReport.TagSeenCount != nil {
			fmt.Fprintf(&rpt, " | cnt %02d", *tagReport.TagSeenCount)
		}
		if tagReport.AccessSpecID != nil {
			fmt.Fprintf(&rpt, " | acc %02d", *tagReport.AccessSpecID)
		}
		if tagReport.C1G2CRC != nil {
			fmt.Fprintf(&rpt, " | crc %05d", *tagReport.C1G2CRC)
		}
		if tagReport.C1G2PC != nil {
			fmt.Fprintf(&rpt, " | epc len %04db", tagReport.C1G2PC.EPCMemoryLength)
			fmt.Fprintf(&rpt, " | usr mem %t", tagReport.C1G2PC.HasUserMemory)
			fmt.Fprintf(&rpt, " | xpc %t", tagReport.C1G2PC.HasXPC)
			if tagReport.C1G2PC.IsISO15961 {
				fmt.Fprintf(&rpt, " | afi %08b", tagReport.C1G2PC.AttributesOrAFI)
			} else {
				fmt.Fprintf(&rpt, " | attrib %08b", tagReport.C1G2PC.AttributesOrAFI)
			}
		}
		if tagReport.C1G2XPCW1 != nil {
			fmt.Fprintf(&rpt, " | xpcw1 %#x", *tagReport.C1G2XPCW1)
		}
		if tagReport.C1G2XPCW2 != nil {
			fmt.Fprintf(&rpt, " | xpcw2 %#x", *tagReport.C1G2XPCW2)
		}

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
