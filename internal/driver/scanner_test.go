package driver

import (
	"testing"
)

func TestScanner(t *testing.T) {
	mon, wg, err := RunScanner()
	if err != nil {
		t.Fatal(err)
	}
	mon.Stop()
	wg.Wait()
}
