package driver

import "testing"

// todo: expand tests
func TestLoad(t *testing.T) {
	cfg := map[string]string{
		"foo": "bar",
	}
	var driverCfg driverConfiguration
	if err := load(cfg, &driverCfg); err != nil {
		t.Fatalf("got err: %s", err.Error())
	}
}
