package protocol

import (
	"math"
	"testing"
)

func f(v float64) *float64 { return &v }
func i(v int) *int         { return &v }

// validICMP returns a measurement satisfying every §7 constraint for the
// given counts. Callers mutate one field to isolate a failure.
func validICMP() *ICMPResult {
	return &ICMPResult{
		Success: true, Sent: 5, Received: 5, LossRatio: 0,
		MinRTTMS: f(10.2), AvgRTTMS: f(12.3), MaxRTTMS: f(15.8), JitterMS: f(1.4),
	}
}

func TestValidateICMPAccepts(t *testing.T) {
	cases := map[string]*ICMPResult{
		"all received": validICMP(),
		"partial loss": {Success: true, Sent: 5, Received: 3, LossRatio: 0.4,
			MinRTTMS: f(1), AvgRTTMS: f(2), MaxRTTMS: f(3), JitterMS: f(0.5)},
		"single packet, no jitter": {Success: true, Sent: 1, Received: 1, LossRatio: 0,
			MinRTTMS: f(1), AvgRTTMS: f(1), MaxRTTMS: f(1), JitterMS: nil},
		"two packets, jitter present": {Success: true, Sent: 5, Received: 2, LossRatio: 0.6,
			MinRTTMS: f(1), AvgRTTMS: f(2), MaxRTTMS: f(3), JitterMS: f(1)},
		"zero loss with sent=1": {Success: true, Sent: 1, Received: 1, LossRatio: 0,
			MinRTTMS: f(0), AvgRTTMS: f(0), MaxRTTMS: f(0), JitterMS: nil},
		"total loss": {Success: false, Sent: 5, Received: 0, LossRatio: 1,
			MinRTTMS: nil, AvgRTTMS: nil, MaxRTTMS: nil, JitterMS: nil},
		"equal rtts": {Success: true, Sent: 4, Received: 4, LossRatio: 0,
			MinRTTMS: f(5), AvgRTTMS: f(5), MaxRTTMS: f(5), JitterMS: f(0)},
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			if err := validateICMP(m); err != nil {
				t.Fatalf("validateICMP() error = %v, want nil", err)
			}
		})
	}
}

func TestValidateICMPRejects(t *testing.T) {
	cases := map[string]func(*ICMPResult){
		"sent zero":     func(m *ICMPResult) { m.Sent = 0 },
		"sent negative": func(m *ICMPResult) { m.Sent = -1 },
		"received negative": func(m *ICMPResult) {
			m.Received, m.LossRatio = -1, 1
		},
		"received exceeds sent": func(m *ICMPResult) { m.Received = 6 },
		"loss ratio above 1":    func(m *ICMPResult) { m.LossRatio = 1.5 },
		"loss ratio below 0":    func(m *ICMPResult) { m.LossRatio = -0.1 },
		"loss ratio mismatch": func(m *ICMPResult) {
			m.Received, m.LossRatio = 3, 0.1
		},
		"loss not one on total loss": func(m *ICMPResult) {
			m.Success, m.Received, m.LossRatio = false, 0, 0.5
			m.MinRTTMS, m.AvgRTTMS, m.MaxRTTMS, m.JitterMS = nil, nil, nil, nil
		},
		"total loss with rtt set": func(m *ICMPResult) {
			m.Success, m.Received, m.LossRatio = false, 0, 1
			m.MinRTTMS, m.AvgRTTMS, m.MaxRTTMS, m.JitterMS = nil, nil, nil, f(1)
		},
		"total loss with success true": func(m *ICMPResult) {
			m.Success, m.Received, m.LossRatio = true, 0, 1
			m.MinRTTMS, m.AvgRTTMS, m.MaxRTTMS, m.JitterMS = nil, nil, nil, nil
		},
		"received zero but success false with min rtt": func(m *ICMPResult) {
			m.Success, m.Received, m.LossRatio = false, 0, 1
			m.MinRTTMS, m.AvgRTTMS, m.MaxRTTMS, m.JitterMS = f(1), nil, nil, nil
		},
		"partial loss with success false":                   func(m *ICMPResult) { m.Success = false },
		"partial loss with success true ok but min missing": func(m *ICMPResult) { m.MinRTTMS = nil },
		"avg missing":      func(m *ICMPResult) { m.AvgRTTMS = nil },
		"max missing":      func(m *ICMPResult) { m.MaxRTTMS = nil },
		"min above avg":    func(m *ICMPResult) { m.MinRTTMS, m.AvgRTTMS = f(20), f(10) },
		"avg above max":    func(m *ICMPResult) { m.AvgRTTMS, m.MaxRTTMS = f(30), f(20) },
		"negative min rtt": func(m *ICMPResult) { m.MinRTTMS = f(-1) },
		"negative jitter":  func(m *ICMPResult) { m.JitterMS = f(-0.1) },
		"nan loss ratio": func(m *ICMPResult) {
			m.LossRatio = math.NaN()
		},
		"inf loss ratio": func(m *ICMPResult) {
			m.LossRatio = math.Inf(1)
		},
		"negative inf rtt": func(m *ICMPResult) {
			m.MinRTTMS = f(math.Inf(-1))
		},
		"nan rtt": func(m *ICMPResult) {
			m.AvgRTTMS = f(math.NaN())
		},
		"inf sent as rtt": func(m *ICMPResult) {
			// 1e400 in JSON overflows to +Inf rather than failing to parse.
			m.MaxRTTMS = f(math.Inf(1))
		},
		"jitter present with received under 2 is allowed, but negative is not": func(m *ICMPResult) {
			m.Sent, m.Received, m.LossRatio = 5, 1, 0.8
			m.MinRTTMS, m.AvgRTTMS, m.MaxRTTMS, m.JitterMS = f(1), f(1), f(1), f(-1)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := validICMP()
			mutate(m)
			err := validateICMP(m)
			if err == nil {
				t.Fatal("validateICMP() = nil, want error")
			}
			assertCode(t, err, CodeInvalidPayload)
		})
	}
}

func TestValidateICMPAllowsJitterNullWhenReceivedUnderTwo(t *testing.T) {
	m := validICMP()
	m.Sent, m.Received, m.LossRatio = 5, 1, 0.8
	m.MinRTTMS, m.AvgRTTMS, m.MaxRTTMS, m.JitterMS = f(1), f(1), f(1), nil
	if err := validateICMP(m); err != nil {
		t.Fatalf("validateICMP() error = %v, want nil", err)
	}
}

func TestValidateICMPLossRatioTolerance(t *testing.T) {
	// 1/3 = 0.3333333333333333; a probe sending 0.333 must still be accepted.
	m := validICMP()
	m.Sent, m.Received, m.LossRatio = 3, 2, 0.333
	m.MinRTTMS, m.AvgRTTMS, m.MaxRTTMS = f(1), f(2), f(3)
	if err := validateICMP(m); err != nil {
		t.Fatalf("loss 0.333 for 2/3 should be within tolerance, got %v", err)
	}

	// But a materially different value must be rejected.
	m.LossRatio = 0.2
	if err := validateICMP(m); err == nil {
		t.Fatal("loss 0.2 for 2/3 should be rejected")
	}
}

func TestValidateICMPMeasurementTypeMismatch(t *testing.T) {
	// A measurement decoded under "icmp" but carrying no ICMP payload.
	if err := validateMeasurement(ProbeICMP, Measurement{}); err == nil {
		t.Fatal("validateMeasurement(icmp, empty) = nil, want error")
	}
	_ = i(1)
}
