package metrics

import (
	"slices"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestSelfMetricsRegister(t *testing.T) {
	reg := prometheus.NewRegistry()
	self := NewSelf(reg)
	if self == nil {
		t.Fatal("NewSelf() = nil")
	}

	self.PushTotal.Inc()
	self.PushRejected.WithLabelValues("invalid_payload").Inc()
	self.AuthFailed.WithLabelValues("invalid_token").Inc()
	self.HTTPRequests.WithLabelValues("POST", "/api/v1/push", "204").Inc()

	want := `
# HELP cqu_netprobe_gateway_push_total Accepted push requests.
# TYPE cqu_netprobe_gateway_push_total counter
cqu_netprobe_gateway_push_total 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "cqu_netprobe_gateway_push_total"); err != nil {
		t.Fatal(err)
	}
}

func TestSelfMetricsReasonLabelsAreEnumerated(t *testing.T) {
	reg := prometheus.NewRegistry()
	self := NewSelf(reg)

	for _, reason := range []string{"invalid_payload", "rate_limit", "invalid_token"} {
		self.PushRejected.WithLabelValues(reason).Inc()
	}

	want := `
# HELP cqu_netprobe_gateway_push_rejected_total Rejected push requests by reason.
# TYPE cqu_netprobe_gateway_push_rejected_total counter
cqu_netprobe_gateway_push_rejected_total{reason="invalid_payload"} 1
cqu_netprobe_gateway_push_rejected_total{reason="invalid_token"} 1
cqu_netprobe_gateway_push_rejected_total{reason="rate_limit"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), "cqu_netprobe_gateway_push_rejected_total"); err != nil {
		t.Fatal(err)
	}
}

// TestIdentityLabelNames pins the frozen label sets element by element.
// Comparing lengths (and, for the measurement set, only the last element) would
// accept a permuted first five, which would silently relabel every series: the
// label names and the values identityLabels returns are zipped positionally.
func TestIdentityLabelNames(t *testing.T) {
	want := []string{"probe_id", "campus", "building_group", "building", "network_type"}
	if !slices.Equal(IdentityLabelNames, want) {
		t.Fatalf("IdentityLabelNames = %v, want %v", IdentityLabelNames, want)
	}
	wantMeasurement := append(append([]string{}, want...), "target")
	if !slices.Equal(MeasurementLabelNames, wantMeasurement) {
		t.Fatalf("MeasurementLabelNames = %v, want %v", MeasurementLabelNames, wantMeasurement)
	}

	// A Desc declares one label name per value MustNewConstMetric is handed, so
	// a short identityLabels() panics at *scrape* time and takes /metrics down
	// with it. The registry's consistency check compares name sets, not arity,
	// so nothing else catches a dropped value.
	if got := identityLabels(probe("hx-sy01-aaaaaa")); len(got) != len(IdentityLabelNames) {
		t.Fatalf("identityLabels() returned %d values, want %d (one per IdentityLabelNames entry)",
			len(got), len(IdentityLabelNames))
	}
}
