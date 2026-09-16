package protocol

import (
	"regexp"
	"testing"
)

func TestConfigIDIsAUUIDv5(t *testing.T) {
	id := DefaultMeasurementConfig().ID()
	// 8-4-4-4-12 hex, version nibble 5, variant nibble 8/9/a/b.
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !pattern.MatchString(id) {
		t.Errorf("ID() = %q, which is not a version-5 UUID", id)
	}
}

// The ID is the config's identity, so equal values must never produce different
// IDs — that is what keeps a no-op save from telling the whole fleet to
// re-fetch.
func TestConfigIDIsDerivedFromTheValues(t *testing.T) {
	a := DefaultMeasurementConfig()
	b := DefaultMeasurementConfig()
	if a.ID() != b.ID() {
		t.Fatalf("two identical configs have different IDs: %s vs %s", a.ID(), b.ID())
	}

	// Every field must be part of the hash: a change to any one of them has to
	// move the ID, or a real edit would go unnoticed by probes.
	mutations := map[string]func(*MeasurementConfig){
		"interval":       func(c *MeasurementConfig) { c.IntervalMS = 20000 },
		"icmp count":     func(c *MeasurementConfig) { c.ICMP.Count = 10 },
		"icmp interval":  func(c *MeasurementConfig) { c.ICMP.IntervalMS = 500 },
		"icmp timeout":   func(c *MeasurementConfig) { c.ICMP.TimeoutMS = 900 },
		"http redirects": func(c *MeasurementConfig) { c.HTTP.FollowRedirects = false },
		"http verify":    func(c *MeasurementConfig) { c.HTTP.VerifyTLS = false },
		"http timeout":   func(c *MeasurementConfig) { c.HTTP.TimeoutMS = 4000 },
		"dns timeout":    func(c *MeasurementConfig) { c.DNS.TimeoutMS = 2500 },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := DefaultMeasurementConfig()
			mutate(&changed)
			if changed.ID() == a.ID() {
				t.Errorf("changing %s left the config ID at %s", name, a.ID())
			}
		})
	}
}
