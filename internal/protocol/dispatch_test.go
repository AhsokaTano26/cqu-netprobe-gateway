package protocol

import "testing"

// The cycle has to be long enough for every measurement to finish before the
// next round starts. A config where it is not makes probes overlap rounds, and
// the symptom is not an error but duplicated loss — a probe that reports two
// rounds' worth of timeouts as one.
func TestMeasurementConfigFitsInsideTheCycle(t *testing.T) {
	c := DefaultMeasurementConfig()

	// The worst-case ICMP round: the gaps between requests, plus the last
	// request's timeout. Note it is not Count*IntervalMS: the last request is
	// sent at (Count-1)*IntervalMS and then waits TimeoutMS for its reply.
	icmpRound := (c.ICMP.Count-1)*c.ICMP.IntervalMS + c.ICMP.TimeoutMS

	for name, took := range map[string]int{
		"icmp": icmpRound,
		"http": c.HTTP.TimeoutMS,
		"dns":  c.DNS.TimeoutMS,
	} {
		if took >= c.IntervalMS {
			t.Errorf("%s can take up to %dms, which does not fit in the %dms cycle",
				name, took, c.IntervalMS)
		}
	}
}

// A zero here would reach a probe as "no timeout" or "send nothing", which no
// validation downstream would catch: the probe simply measures nothing and
// reports nothing, and the gateway sees a probe that stopped.
func TestMeasurementConfigHasNoZeroOrEmptyValues(t *testing.T) {
	c := DefaultMeasurementConfig()

	if c.IntervalMS <= 0 {
		t.Errorf("interval_ms = %d, want positive", c.IntervalMS)
	}
	if c.ICMP.Count <= 0 {
		t.Errorf("icmp.count = %d, want positive; sent must be > 0 (§7)", c.ICMP.Count)
	}
	if c.ICMP.IntervalMS <= 0 {
		t.Errorf("icmp.interval_ms = %d, want positive", c.ICMP.IntervalMS)
	}
	if c.ICMP.TimeoutMS <= 0 {
		t.Errorf("icmp.timeout_ms = %d, want positive", c.ICMP.TimeoutMS)
	}
	if c.HTTP.TimeoutMS <= 0 {
		t.Errorf("http.timeout_ms = %d, want positive", c.HTTP.TimeoutMS)
	}
	if c.DNS.TimeoutMS <= 0 {
		t.Errorf("dns.timeout_ms = %d, want positive", c.DNS.TimeoutMS)
	}
	if c.HTTP.Method == "" {
		t.Error("http.method is empty")
	}
	if c.DNS.Transport == "" {
		t.Error("dns.transport is empty")
	}
}
