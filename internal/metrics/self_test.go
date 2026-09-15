package metrics

import (
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

func TestIdentityLabelNames(t *testing.T) {
	want := []string{"probe_id", "campus", "building_group", "building", "network_type"}
	if len(IdentityLabelNames) != len(want) {
		t.Fatalf("IdentityLabelNames = %v, want %v", IdentityLabelNames, want)
	}
	for i := range want {
		if IdentityLabelNames[i] != want[i] {
			t.Fatalf("IdentityLabelNames[%d] = %q, want %q", i, IdentityLabelNames[i], want[i])
		}
	}
	if len(MeasurementLabelNames) != len(want)+1 || MeasurementLabelNames[len(want)] != "target" {
		t.Fatalf("MeasurementLabelNames = %v, want identity labels plus target", MeasurementLabelNames)
	}
}
