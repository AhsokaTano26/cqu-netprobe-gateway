package store

import (
	"path/filepath"
	"testing"

	"github.com/tano/cqu-netprobe-gateway/internal/protocol"
)

func measurementStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "measurement.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// An untouched deployment must dispatch exactly what the protocol documents,
// with no migration and no seed row.
func TestMeasurementConfigDefaultsWhenUnset(t *testing.T) {
	s := measurementStore(t)

	got, custom, err := s.MeasurementConfig()
	if err != nil {
		t.Fatalf("MeasurementConfig() error = %v", err)
	}
	if custom {
		t.Error("a fresh database reported a custom config")
	}
	if got != protocol.DefaultMeasurementConfig() {
		t.Errorf("MeasurementConfig() = %+v, want the protocol defaults", got)
	}
}

func TestMeasurementConfigRoundTrips(t *testing.T) {
	s := measurementStore(t)

	want := protocol.DefaultMeasurementConfig()
	want.IntervalMS = 30000
	want.ICMP.Count = 10
	want.HTTP.FollowRedirects = false
	want.HTTP.VerifyTLS = false
	want.DNS.TimeoutMS = 2000

	if err := s.SetMeasurementConfig(want); err != nil {
		t.Fatalf("SetMeasurementConfig() error = %v", err)
	}
	got, custom, err := s.MeasurementConfig()
	if err != nil {
		t.Fatalf("MeasurementConfig() error = %v", err)
	}
	if !custom {
		t.Error("a saved config was not reported as custom")
	}
	if got != want {
		t.Errorf("MeasurementConfig() = %+v, want %+v", got, want)
	}

	// And resetting really does bring the defaults back rather than leaving a
	// stale row behind.
	if err := s.ResetMeasurementConfig(); err != nil {
		t.Fatalf("ResetMeasurementConfig() error = %v", err)
	}
	got, custom, err = s.MeasurementConfig()
	if err != nil {
		t.Fatalf("MeasurementConfig() error = %v", err)
	}
	if custom || got != protocol.DefaultMeasurementConfig() {
		t.Errorf("after reset: %+v custom=%v, want the defaults", got, custom)
	}
}

func TestSetMeasurementConfigRejectsInvalid(t *testing.T) {
	s := measurementStore(t)

	bad := protocol.DefaultMeasurementConfig()
	bad.IntervalMS = 500 // below the floor

	if err := s.SetMeasurementConfig(bad); err == nil {
		t.Fatal("SetMeasurementConfig() stored a config the protocol rejects")
	}
	// The rejected write must not have landed.
	if _, custom, _ := s.MeasurementConfig(); custom {
		t.Error("a rejected config was stored anyway")
	}
}

// A row that a hand-edit broke must not take the target list down with it: the
// gateway keeps dispatching the defaults, which is the only thing a probe can
// act on.
func TestMeasurementConfigFallsBackOnUnreadableValue(t *testing.T) {
	for name, raw := range map[string]string{
		"not json":     "{{{",
		"wrong shape":  `{"interval_ms": "soon"}`,
		"invalid body": `{"interval_ms":10,"icmp":{"count":0},"http":{},"dns":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			s := measurementStore(t)
			if err := s.SetSetting(measurementConfigKey, raw); err != nil {
				t.Fatalf("SetSetting() error = %v", err)
			}

			got, custom, err := s.MeasurementConfig()
			if err != nil {
				t.Fatalf("MeasurementConfig() error = %v; a bad row must not be fatal", err)
			}
			if custom {
				t.Error("an unreadable value was reported as a custom config")
			}
			if got != protocol.DefaultMeasurementConfig() {
				t.Errorf("MeasurementConfig() = %+v, want the protocol defaults", got)
			}
		})
	}
}
