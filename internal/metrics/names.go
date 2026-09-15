// Package metrics exposes probe measurements and gateway health to Prometheus.
package metrics

// IdentityLabelNames are the labels the Gateway attaches from its own database.
// A probe has no influence over any of them (Protocol v1 §22, §25).
//
// building_group is a documented addition to the Protocol v1 §25 label set; see
// the design doc §6.1. It lets dashboards aggregate a group of buildings
// (e.g. 松园一栋 + 松园二栋 -> 松园) with a single PromQL by-clause.
var IdentityLabelNames = []string{
	"probe_id",
	"campus",
	"building_group",
	"building",
	"network_type",
}

// MeasurementLabelNames adds the target ID to the identity labels. The target
// comes from the payload but is validated against the allowlist first.
var MeasurementLabelNames = append(append([]string{}, IdentityLabelNames...), "target")

// Probe measurement metric names (Protocol v1 §18 of the gateway requirements).
const (
	NameOnline   = "campus_probe_online"
	NameLastSeen = "campus_probe_last_seen_timestamp_seconds"

	NameICMPSuccess   = "campus_probe_icmp_success"
	NameICMPLossRatio = "campus_probe_icmp_loss_ratio"
	NameICMPRTT       = "campus_probe_icmp_rtt_seconds"
	NameICMPRTTMin    = "campus_probe_icmp_rtt_min_seconds"
	NameICMPRTTMax    = "campus_probe_icmp_rtt_max_seconds"
	NameICMPJitter    = "campus_probe_icmp_jitter_seconds"

	NameDNSSuccess  = "campus_probe_dns_success"
	NameDNSDuration = "campus_probe_dns_duration_seconds"

	NameHTTPSuccess    = "campus_probe_http_success"
	NameHTTPDuration   = "campus_probe_http_duration_seconds"
	NameHTTPStatusCode = "campus_probe_http_status_code"

	NameRegisteredProbes = "cqu_netprobe_gateway_registered_probes"
	NameOnlineProbes     = "cqu_netprobe_gateway_online_probes"
)

// millisecondsToSeconds converts a probe-reported millisecond value into the
// Prometheus base unit. This is the only place the conversion happens.
func millisecondsToSeconds(ms float64) float64 { return ms / 1000 }
