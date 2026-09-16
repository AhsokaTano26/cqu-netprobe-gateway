package protocol

import (
	"reflect"
	"regexp"
	"testing"
)

func TestConfigIDIsAUUIDv5(t *testing.T) {
	id := ConfigID(DefaultMeasurementConfig(), nil)
	// 8-4-4-4-12 hex, version nibble 5, variant nibble 8/9/a/b.
	pattern := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-5[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	if !pattern.MatchString(id) {
		t.Errorf("ConfigID() = %q, which is not a version-5 UUID", id)
	}
}

// The ID is the configuration's identity, so equal values must never produce
// different IDs — that is what keeps a no-op save from telling the whole fleet
// to re-fetch.
func TestConfigIDIsDerivedFromTheValues(t *testing.T) {
	a := DefaultMeasurementConfig()
	if ConfigID(a, nil) != ConfigID(DefaultMeasurementConfig(), nil) {
		t.Fatalf("two identical configs have different IDs")
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
			if ConfigID(changed, nil) == ConfigID(a, nil) {
				t.Errorf("changing %s left the config ID at %s", name, ConfigID(a, nil))
			}
		})
	}
}

// The target list is part of the identity too. Without it, an administrator who
// deletes a target leaves every probe that is still measuring it holding what
// the gateway calls the current config: their pushes are rejected as
// invalid_target, they are never told to re-fetch, and §23 freezes their
// last_seen until their own periodic refresh happens to fire.
func TestConfigIDCoversTheTargetList(t *testing.T) {
	cfg := DefaultMeasurementConfig()
	base := []DispatchTarget{{TargetID: "a", Address: "223.5.5.5", ProbeTypes: []string{"icmp"}}}
	id := ConfigID(cfg, base)

	cases := map[string][]DispatchTarget{
		"the target list is empty":    nil,
		"another target was added":    append(append([]DispatchTarget{}, base...), DispatchTarget{TargetID: "b", Address: "8.8.8.8", ProbeTypes: []string{"dns"}}),
		"the only target was removed": {},
		"the address changed":         {{TargetID: "a", Address: "9.9.9.9", ProbeTypes: []string{"icmp"}}},
		"a probe type was removed":    {{TargetID: "a", Address: "223.5.5.5", ProbeTypes: []string{}}},
		"a probe type was added":      {{TargetID: "a", Address: "223.5.5.5", ProbeTypes: []string{"icmp", "dns"}}},
		"the target id was renamed":   {{TargetID: "z", Address: "223.5.5.5", ProbeTypes: []string{"icmp"}}},
	}
	for name, targets := range cases {
		t.Run(name, func(t *testing.T) {
			if ConfigID(cfg, targets) == id {
				t.Errorf("%s left the config ID unchanged at %s", name, id)
			}
		})
	}
}

// Identity is a set, not a sequence. The store happens to return targets and
// types in a stable order, but ConfigID must not depend on that: a caller that
// lists the same targets another way would otherwise hand out a different ID
// for the same configuration, and the whole fleet would re-fetch for nothing.
func TestConfigIDIgnoresOrder(t *testing.T) {
	cfg := DefaultMeasurementConfig()
	a := DispatchTarget{TargetID: "a", Address: "223.5.5.5", ProbeTypes: []string{"icmp", "dns"}}
	b := DispatchTarget{TargetID: "b", Address: "8.8.8.8", ProbeTypes: []string{"http"}}

	if ConfigID(cfg, []DispatchTarget{a, b}) != ConfigID(cfg, []DispatchTarget{b, a}) {
		t.Error("reordering the targets changed the ID")
	}

	unsorted := DispatchTarget{TargetID: "a", Address: "223.5.5.5", ProbeTypes: []string{"dns", "icmp"}}
	if ConfigID(cfg, []DispatchTarget{unsorted}) != ConfigID(cfg, []DispatchTarget{a}) {
		t.Error("reordering a target's probe types changed the ID")
	}
}

// ConfigID sorts what it is given, so it sorts a copy. Reordering the caller's
// slice in place would be a visible side effect on a function whose name
// promises only to look at its arguments — and the caller here is the target
// list handler, which hashes the very slice it is about to send.
func TestConfigIDLeavesItsInputAlone(t *testing.T) {
	targets := []DispatchTarget{
		{TargetID: "b", Address: "8.8.8.8", ProbeTypes: []string{"http", "dns"}},
		{TargetID: "a", Address: "223.5.5.5", ProbeTypes: []string{"icmp"}},
	}
	before := make([]DispatchTarget, len(targets))
	for i, t := range targets {
		before[i] = DispatchTarget{t.TargetID, t.Address, append([]string{}, t.ProbeTypes...)}
	}

	ConfigID(DefaultMeasurementConfig(), targets)

	if !reflect.DeepEqual(targets, before) {
		t.Errorf("ConfigID reordered its argument:\n got %+v\nwant %+v", targets, before)
	}
}
