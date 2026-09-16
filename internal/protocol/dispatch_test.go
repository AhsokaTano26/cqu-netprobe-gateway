package protocol

import "testing"

// The defaults have to pass the same gate an administrator's values do, so a
// constant edited into an impossible combination (a round longer than the
// cycle, say) fails here rather than on a fleet of probes.
func TestDefaultMeasurementConfigIsValid(t *testing.T) {
	c := DefaultMeasurementConfig()
	if err := c.Validate(); err != nil {
		t.Fatalf("the shipped defaults are not valid: %v", err)
	}
}

func TestValidateRejectsUnusableConfigs(t *testing.T) {
	valid := DefaultMeasurementConfig

	cases := []struct {
		name   string
		mutate func(*MeasurementConfig)
	}{
		{"zero cycle", func(c *MeasurementConfig) { c.IntervalMS = 0 }},
		{"negative cycle", func(c *MeasurementConfig) { c.IntervalMS = -10000 }},
		{"cycle below the floor", func(c *MeasurementConfig) { c.IntervalMS = 999 }},
		{"zero icmp count", func(c *MeasurementConfig) { c.ICMP.Count = 0 }},
		{"zero icmp interval", func(c *MeasurementConfig) { c.ICMP.IntervalMS = 0 }},
		{"zero icmp timeout", func(c *MeasurementConfig) { c.ICMP.TimeoutMS = 0 }},
		// 60 packets 200ms apart plus a 1s timeout is 12.8s of work in a 10s
		// cycle: the next round would start before this one finished.
		{"icmp round outruns the cycle", func(c *MeasurementConfig) { c.ICMP.Count = 60 }},
		{"icmp round equal to the cycle", func(c *MeasurementConfig) {
			c.IntervalMS = 2000
			c.ICMP.Count = 5 // 4*200 + 1000 = 1800
			c.ICMP.TimeoutMS = 1200
		}},
		{"http method not GET", func(c *MeasurementConfig) { c.HTTP.Method = "POST" }},
		{"dns transport not udp", func(c *MeasurementConfig) { c.DNS.Transport = "tcp" }},
		{"zero http timeout", func(c *MeasurementConfig) { c.HTTP.TimeoutMS = 0 }},
		{"http timeout longer than the cycle", func(c *MeasurementConfig) { c.HTTP.TimeoutMS = 10000 }},
		{"zero dns timeout", func(c *MeasurementConfig) { c.DNS.TimeoutMS = 0 }},
		{"dns timeout longer than the cycle", func(c *MeasurementConfig) { c.DNS.TimeoutMS = 10000 }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := valid()
			tc.mutate(&c)
			if err := c.Validate(); err == nil {
				t.Fatal("Validate() accepted a config a probe cannot honour")
			}
		})
	}

	// And the boundary the other way: one millisecond of slack must still be
	// accepted, or the rule would reject configurations that are merely tight.
	// The other timeouts come down with the cycle, since they have to fit too.
	edge := valid()
	edge.IntervalMS = 1801 // the ICMP round is 4*200 + 1000 = 1800
	edge.HTTP.TimeoutMS = 1000
	edge.DNS.TimeoutMS = 1000
	if err := edge.Validate(); err != nil {
		t.Errorf("a round that fits in the cycle was rejected: %v", err)
	}
}
