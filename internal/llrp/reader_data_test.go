//
// Copyright (C) 2020 Intel Corporation
//
// SPDX-License-Identifier: Apache-2.0

package llrp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestClient_withRecordedData(t *testing.T) {
	testRecordedData(t, "testdata")
	if *roDirectory != "" {
		testRecordedData(t, filepath.Join("testdata", *roDirectory))
	}
}

func BenchmarkUnmarshalRO(b *testing.B) {
	for _, nTags := range []int{
		100, 200, 300, 400,
	} {
		nTags := nTags
		b.Run(strconv.Itoa(nTags)+"Tags", func(b *testing.B) {
			ro := &ROAccessReport{}

			for i := 0; i < nTags; i++ {
				ro.TagReportData = append(ro.TagReportData, TagReportData{
					EPC96: EPC96{
						EPC: []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12},
					},
					C1G2PC: &C1G2PC{
						EPCMemoryLength: 12,
						HasUserMemory:   false,
						HasXPC:          false,
						IsISO15961:      false,
						AttributesOrAFI: 0x21,
					},
				})
			}

			data, err := ro.MarshalBinary()
			if err != nil {
				b.Fatal(err)
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				target := &ROAccessReport{}
				if err := target.UnmarshalBinary(data); err != nil {
					b.Error(err)
				}
			}
		})
	}
}

func testRecordedData(t *testing.T, dir string) {
	files, err := ioutil.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, f := range files {
		if !strings.HasSuffix(f.Name(), ".json") {
			continue
		}

		prefix := f.Name()[:len(f.Name())-len(".json")]
		msg := strings.SplitN(f.Name(), "-", 2)[0]
		t.Run(prefix, compareMessages(msg, filepath.Join(dir, prefix)))
	}
}

func compareMessages(msgName, prefix string) func(t *testing.T) {
	type binRoundTrip interface {
		UnmarshalBinary(data []byte) error
		MarshalBinary() ([]byte, error)
	}
	var v binRoundTrip
	switch "Msg" + msgName {
	case MsgGetReaderCapabilitiesResponse.String():
		v = &GetReaderCapabilitiesResponse{}
	case MsgGetReaderConfigResponse.String():
		v = &GetReaderConfigResponse{}
	case MsgGetAccessSpecsResponse.String():
		v = &GetAccessSpecsResponse{}
	case MsgGetROSpecsResponse.String():
		v = &GetROSpecsResponse{}
	case MsgCloseConnectionResponse.String():
		v = &CloseConnectionResponse{}
	case MsgROAccessReport.String():
		v = &ROAccessReport{}
	}

	// This tests the following two conversions using data captured from a reader:
	//   JSON -> Go -> binary & check it matches original binary
	// binary -> Go -> JSON   & check it matches original JSON
	return func(t *testing.T) {
		if v == nil {
			t.Fatalf("unknown message type: %s", msgName)
		}

		var originalJSON, originalBin, marshaledBin, marshaledJSON []byte
		var err error

		// load data files
		originalJSON, err = ioutil.ReadFile(prefix + ".json")
		if err != nil {
			t.Fatalf("can't read .json file: %v", err)
		}

		originalBin, err = ioutil.ReadFile(prefix + ".bytes")
		if err != nil {
			t.Fatalf("can't read .bytes file: %v", err)
		}

		// unmarshal original JSON form
		if err = json.Unmarshal(originalJSON, v); err != nil {
			t.Error(err)
		}

		// marshal resulting struct to binary
		if marshaledBin, err = v.MarshalBinary(); err != nil {
			t.Error(err)
		}

		// get a new v (so we're not duplicating list items)
		v = reflect.New(reflect.TypeOf(v).Elem()).Interface().(binRoundTrip)

		// unmarshal original binary form to struct
		if err = v.UnmarshalBinary(originalBin); err != nil {
			t.Error(err)
		}

		// marshal struct back to JSON
		marshaledJSON, err = json.MarshalIndent(v, "", "\t")
		if err != nil {
			t.Error(err)
		}

		// Check the JSON first, since it'll be easier to eyeball differences.
		// If names change, unmarshaling is likely to leave many zero values,
		// which will make the binary mismatched.
		// The name differences should be pretty obvious when viewing the JSON.
		checkJSONEq(t, originalJSON, marshaledJSON)

		// Finally, confirm binary the matches.
		checkBytesEq(t, originalBin, marshaledBin)
	}
}

// checkJSONEq checks that two json data byte arrays are equal line-by-line,
// ignoring leading and trailing whitespace within each line,
// as well as any remaining whitespace at the end of the data.
//
// If not, it prints a side-by-side diff around the first line difference.
//
// Returns true if the two arrays are equal; false otherwise.
func checkJSONEq(t *testing.T, original, marshaled []byte) (matched bool) {
	t.Helper()

	lines1, lines2 := bytes.Split(original, []byte("\n")), bytes.Split(marshaled, []byte("\n"))

	larger, smaller := lines1, lines2
	if len(lines1) < len(lines2) {
		larger, smaller = lines2, lines1
	}

	matched = true
	firstDiff := 0
	for ; matched && firstDiff < len(smaller); firstDiff++ {
		matched = bytes.Equal(
			bytes.TrimSpace(lines1[firstDiff]),
			bytes.TrimSpace(lines2[firstDiff]))
	}
	firstDiff--

	// the larger of the two may only have blank lines
	for i := firstDiff + 1; matched && i < len(larger); i++ {
		matched = len(bytes.TrimSpace(larger[i])) == 0
	}

	if matched {
		return
	}

	const contextAbove = 4
	start := firstDiff - contextAbove
	if start < 0 {
		start = 0
	}

	const contextTotal = 16
	end := start + contextTotal
	if end > len(lines1)-1 {
		end = len(lines1) - 1
	}

	if end > len(lines2)-1 {
		end = len(lines2) - 1
	}

	longest := 1
	for i := start; i < end; i++ {
		lines1[i] = bytes.ReplaceAll(lines1[i], []byte("\t"), []byte("  "))
		lines2[i] = bytes.ReplaceAll(lines2[i], []byte("\t"), []byte("  "))
		if len(lines1[i]) > longest {
			longest = len(lines1[i])
		}
		if len(lines2[i]) > longest {
			longest = len(lines2[i])
		}
	}

	diff := bytes.Buffer{}
	fmt.Fprintf(&diff, "%-[1]*s  |  %s\n", longest,
		"    --Original JSON Data--", "    --Marshaled Result--")
	for i := start; i < end; i++ {
		if i == firstDiff {
			msg := "--first diff below this line--"
			fmt.Fprintf(&diff, "%[1]*s\n", longest+3+len(msg)/2, msg)
		}
		fmt.Fprintf(&diff, "%-[1]*s  |  %s\n", longest, lines1[i], lines2[i])
	}

	t.Errorf("JSON data mismatched; first difference around line %d:\n%s",
		firstDiff, diff.String())
	return matched
}

func checkBytesEq(t *testing.T, original, marshaled []byte) (matched bool) {
	t.Helper()

	matched = len(original) == len(marshaled)
	smaller := len(marshaled)
	if len(original) < len(marshaled) {
		smaller = len(original)
		matched = false
	}

	firstDiff := 0
	for ; firstDiff < smaller; firstDiff++ {
		if original[firstDiff] != marshaled[firstDiff] {
			matched = false
			break
		}
	}

	if matched {
		return
	}

	if len(original) == 0 || len(marshaled) == 0 {
		t.Errorf("byte data mismatched: one is empty: \n"+
			" original: %# 02x\n"+
			"marshaled: %# 02x", original, marshaled)
		return
	}

	// these constants control how much context to show
	const bpl = 8    // bytes per line (per byte array, so really, 2x this)
	const before = 2 // lines before first diff
	const after = 4  // lines after first diff

	start := ((firstDiff / bpl) - before) * bpl
	if start < 0 {
		start = 0
	}
	end := start + bpl*(before+after)

	ob := original[start:]
	mb := marshaled[start:]

	format := fmt.Sprintf("%%04d-%%04d > %%#- %d.%dx |  %%# .%dx\n", bpl*5, bpl, bpl)

	buff := bytes.Buffer{}
	for i := start; i < end && (len(ob) > 0 || len(mb) > 0); i += bpl {
		if i <= firstDiff && firstDiff < i+bpl {
			buff.Write(bytes.Repeat([]byte(" "), 5*(firstDiff-i)+12))
			buff.WriteString(expand((bpl+1)*5+4, "-", "v", "first diff", "v\n"))
		}

		fmt.Fprintf(&buff, format, i, i+bpl, ob, mb)

		if len(ob) > bpl {
			ob = ob[bpl:]
		} else {
			ob = nil
		}

		if len(mb) > bpl {
			mb = mb[bpl:]
		} else {
			mb = nil
		}
	}

	t.Errorf("byte data mismatched starting at byte %d\n"+
		"  index   > %[2]*s |  marshaled binary\n%s",
		firstDiff, bpl*5, "original binary", buff.String())

	return matched
}

// expand returns a string that centers s between l and r
// by inserting fill as many times as need
// so that the final string has at least the given width.
//
// If width <= len(l+s+r), this return l+s+r,
// even if that would be larger than width.
// Otherwise, it inserts the minimum number of fill values
// so that the len the returned value >= width.
// If len(fill) == 1, it'll insert just to width.
// If it's larger, the return might exceed width by as much.
// If it's an empty string, this'll panic.
func expand(width int, fill, l, s, r string) string {
	width -= len(s) + len(l) + len(r)
	nFills := atLeastZero(width / len(fill))
	rFill := atLeastZero(nFills / 2)
	lFill := atLeastZero(nFills - rFill)
	return l + strings.Repeat(fill, rFill) + s + strings.Repeat(fill, lFill) + r
}

func atLeastZero(i int) int {
	if i < 0 {
		return 0
	}
	return i
}
