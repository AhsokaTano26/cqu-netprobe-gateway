package metrics

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/tano/cqu-netprobe-gateway/internal/latest"
	"github.com/tano/cqu-netprobe-gateway/internal/protocol"
	"github.com/tano/cqu-netprobe-gateway/internal/store"
)

type fakeProbes struct {
	probes []store.Probe
	err    error
}

func (f *fakeProbes) ListProbes() ([]store.Probe, error) { return f.probes, f.err }

func probe(id string) store.Probe {
	return store.Probe{
		ProbeID:           id,
		CampusCode:        "hx",
		BuildingGroupCode: "sy",
		BuildingCode:      "sy01",
		NetworkType:       "wired",
		Enabled:           true,
	}
}

func f64(v float64) *float64 { return &v }
func intp(v int) *int        { return &v }

// icmpResults builds a full-success ICMP result for target aliyun_dns.
func icmpResults(avgMS float64) protocol.Results {
	return protocol.Results{
		"aliyun_dns": {
			protocol.ProbeICMP: {ICMP: &protocol.ICMPResult{
				Success: true, Sent: 5, Received: 5, LossRatio: 0,
				MinRTTMS: f64(avgMS - 1), AvgRTTMS: f64(avgMS), MaxRTTMS: f64(avgMS + 1), JitterMS: f64(0.5),
			}},
		},
	}
}

// newTestCollector wires a collector with a frozen clock.
func newTestCollector(t *testing.T, l *latest.Store, src ProbeLister, now time.Time) *prometheus.Registry {
	t.Helper()
	reg := prometheus.NewRegistry()
	self := NewSelf(prometheus.NewRegistry())
	c := NewCollector(l, src, 30*time.Second, self)
	c.Now = func() time.Time { return now }
	reg.MustRegister(c)
	return reg
}

// identitySuffix is spelled in the order the Prometheus exposition encoder
// writes label pairs: MustNewConstMetric sorts them by name, so the emitted
// text is always alphabetical regardless of the Desc's declared order. The
// name/value mapping is still asserted exactly - if identityLabels() returned
// its values in the wrong order, the value would land on the wrong name here
// and the comparison would fail.
const identitySuffix = `building="sy01",building_group="sy",campus="hx",network_type="wired",probe_id="hx-sy01-aaaaaa"`

func TestCollectOnlineProbeExposesMeasurements(t *testing.T) {
	now := time.Unix(1789490000, 0).UTC()
	l := latest.New()
	l.Put("hx-sy01-aaaaaa", icmpResults(12.3), now.Add(-5*time.Second))

	reg := newTestCollector(t, l, &fakeProbes{probes: []store.Probe{probe("hx-sy01-aaaaaa")}}, now)

	want := `
# HELP campus_probe_icmp_rtt_seconds Average ICMP round-trip time in seconds.
# TYPE campus_probe_icmp_rtt_seconds gauge
campus_probe_icmp_rtt_seconds{` + identitySuffix + `,target="aliyun_dns"} 0.0123
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), NameICMPRTT); err != nil {
		t.Fatal(err)
	}
}

func TestCollectConvertsMillisecondsToSeconds(t *testing.T) {
	now := time.Unix(1789490000, 0).UTC()
	l := latest.New()
	l.Put("hx-sy01-aaaaaa", icmpResults(1234.5), now)

	reg := newTestCollector(t, l, &fakeProbes{probes: []store.Probe{probe("hx-sy01-aaaaaa")}}, now)

	want := `
# HELP campus_probe_icmp_rtt_seconds Average ICMP round-trip time in seconds.
# TYPE campus_probe_icmp_rtt_seconds gauge
campus_probe_icmp_rtt_seconds{` + identitySuffix + `,target="aliyun_dns"} 1.2345
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), NameICMPRTT); err != nil {
		t.Fatal(err)
	}
}

func TestCollectOnlineBoundary(t *testing.T) {
	// Protocol v1 §26: age <= 30s is online. Exactly 30s must still be online.
	now := time.Unix(1789490000, 0).UTC()

	cases := []struct {
		name string
		age  time.Duration
		want float64
	}{
		{"fresh", 1 * time.Second, 1},
		{"exactly at threshold", 30 * time.Second, 1},
		{"one millisecond past", 30*time.Second + time.Millisecond, 0},
		{"well past", 5 * time.Minute, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := latest.New()
			l.Put("hx-sy01-aaaaaa", icmpResults(12.3), now.Add(-tc.age))
			reg := newTestCollector(t, l, &fakeProbes{probes: []store.Probe{probe("hx-sy01-aaaaaa")}}, now)

			want := `
# HELP campus_probe_online Whether the probe pushed successfully within the online threshold.
# TYPE campus_probe_online gauge
campus_probe_online{` + identitySuffix + `} ` + formatFloat(tc.want) + `
`
			if err := testutil.GatherAndCompare(reg, strings.NewReader(want), NameOnline); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCollectStaleProbeHidesMeasurements(t *testing.T) {
	now := time.Unix(1789490000, 0).UTC()
	l := latest.New()
	l.Put("hx-sy01-aaaaaa", icmpResults(12.3), now.Add(-31*time.Second))

	reg := newTestCollector(t, l, &fakeProbes{probes: []store.Probe{probe("hx-sy01-aaaaaa")}}, now)

	// online and last_seen must still be present.
	wantKept := `
# HELP campus_probe_online Whether the probe pushed successfully within the online threshold.
# TYPE campus_probe_online gauge
campus_probe_online{` + identitySuffix + `} 0
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(wantKept), NameOnline); err != nil {
		t.Fatal(err)
	}

	// Every measurement family must be absent.
	hidden := []string{
		NameICMPSuccess, NameICMPLossRatio, NameICMPRTT, NameICMPRTTMin,
		NameICMPRTTMax, NameICMPJitter, NameDNSSuccess, NameDNSDuration,
		NameHTTPSuccess, NameHTTPDuration, NameHTTPStatusCode,
	}
	for _, name := range hidden {
		if n := testutil.CollectAndCount(reg, name); n != 0 {
			t.Errorf("%s: %d series exposed for a stale probe, want 0", name, n)
		}
	}
}

func TestCollectStaleProbeKeepsLastSeen(t *testing.T) {
	now := time.Unix(1789490000, 0).UTC()
	receivedAt := now.Add(-2 * time.Minute)
	l := latest.New()
	l.Put("hx-sy01-aaaaaa", icmpResults(12.3), receivedAt)

	reg := newTestCollector(t, l, &fakeProbes{probes: []store.Probe{probe("hx-sy01-aaaaaa")}}, now)

	want := `
# HELP campus_probe_last_seen_timestamp_seconds Unix timestamp of the last accepted push.
# TYPE campus_probe_last_seen_timestamp_seconds gauge
campus_probe_last_seen_timestamp_seconds{` + identitySuffix + `} ` + formatInt(receivedAt.Unix()) + `
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), NameLastSeen); err != nil {
		t.Fatal(err)
	}
}

func TestCollectNeverPushedProbe(t *testing.T) {
	now := time.Unix(1789490000, 0).UTC()
	reg := newTestCollector(t, latest.New(), &fakeProbes{probes: []store.Probe{probe("hx-sy01-aaaaaa")}}, now)

	want := `
# HELP campus_probe_online Whether the probe pushed successfully within the online threshold.
# TYPE campus_probe_online gauge
campus_probe_online{` + identitySuffix + `} 0
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), NameOnline); err != nil {
		t.Fatal(err)
	}

	// A never-seen probe must not emit a bogus epoch timestamp.
	if n := testutil.CollectAndCount(reg, NameLastSeen); n != 0 {
		t.Errorf("%s: %d series for a never-pushed probe, want 0", NameLastSeen, n)
	}
}

func TestCollectDisabledProbeEmitsNothing(t *testing.T) {
	now := time.Unix(1789490000, 0).UTC()
	l := latest.New()
	l.Put("hx-sy01-aaaaaa", icmpResults(12.3), now)

	disabled := probe("hx-sy01-aaaaaa")
	disabled.Enabled = false
	reg := newTestCollector(t, l, &fakeProbes{probes: []store.Probe{disabled}}, now)

	for _, name := range []string{NameOnline, NameLastSeen, NameICMPRTT, NameICMPSuccess} {
		if n := testutil.CollectAndCount(reg, name); n != 0 {
			t.Errorf("%s: %d series for a disabled probe, want 0", name, n)
		}
	}
}

func TestCollectICMPTotalLossOmitsRTT(t *testing.T) {
	now := time.Unix(1789490000, 0).UTC()
	l := latest.New()
	l.Put("hx-sy01-aaaaaa", protocol.Results{
		"aliyun_dns": {
			protocol.ProbeICMP: {ICMP: &protocol.ICMPResult{
				Success: false, Sent: 5, Received: 0, LossRatio: 1,
			}},
		},
	}, now)

	reg := newTestCollector(t, l, &fakeProbes{probes: []store.Probe{probe("hx-sy01-aaaaaa")}}, now)

	want := `
# HELP campus_probe_icmp_success Whether the ICMP probe succeeded.
# TYPE campus_probe_icmp_success gauge
campus_probe_icmp_success{` + identitySuffix + `,target="aliyun_dns"} 0
# HELP campus_probe_icmp_loss_ratio ICMP packet loss ratio.
# TYPE campus_probe_icmp_loss_ratio gauge
campus_probe_icmp_loss_ratio{` + identitySuffix + `,target="aliyun_dns"} 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), NameICMPSuccess, NameICMPLossRatio); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{NameICMPRTT, NameICMPRTTMin, NameICMPRTTMax, NameICMPJitter} {
		if n := testutil.CollectAndCount(reg, name); n != 0 {
			t.Errorf("%s: %d series with null RTT, want 0", name, n)
		}
	}
}

func TestCollectNullJitterOmitsJitterOnly(t *testing.T) {
	now := time.Unix(1789490000, 0).UTC()
	l := latest.New()
	l.Put("hx-sy01-aaaaaa", protocol.Results{
		"aliyun_dns": {
			protocol.ProbeICMP: {ICMP: &protocol.ICMPResult{
				Success: true, Sent: 5, Received: 1, LossRatio: 0.8,
				MinRTTMS: f64(1), AvgRTTMS: f64(1), MaxRTTMS: f64(1), JitterMS: nil,
			}},
		},
	}, now)

	reg := newTestCollector(t, l, &fakeProbes{probes: []store.Probe{probe("hx-sy01-aaaaaa")}}, now)

	if n := testutil.CollectAndCount(reg, NameICMPJitter); n != 0 {
		t.Errorf("%s: %d series with null jitter, want 0", NameICMPJitter, n)
	}
	if n := testutil.CollectAndCount(reg, NameICMPRTT); n != 1 {
		t.Errorf("%s: %d series, want 1", NameICMPRTT, n)
	}
}

func TestCollectHTTPFailureWithResponseKeepsStatusAndDuration(t *testing.T) {
	now := time.Unix(1789490000, 0).UTC()
	l := latest.New()
	l.Put("hx-sy01-aaaaaa", protocol.Results{
		"cqu_mirror": {
			protocol.ProbeHTTP: {HTTP: &protocol.HTTPResult{
				Success: false, StatusCode: intp(500), DurationMS: f64(63.2),
			}},
		},
	}, now)

	reg := newTestCollector(t, l, &fakeProbes{probes: []store.Probe{probe("hx-sy01-aaaaaa")}}, now)

	want := `
# HELP campus_probe_http_success Whether the HTTP probe succeeded.
# TYPE campus_probe_http_success gauge
campus_probe_http_success{` + identitySuffix + `,target="cqu_mirror"} 0
# HELP campus_probe_http_status_code HTTP response status code.
# TYPE campus_probe_http_status_code gauge
campus_probe_http_status_code{` + identitySuffix + `,target="cqu_mirror"} 500
# HELP campus_probe_http_duration_seconds HTTP request duration in seconds.
# TYPE campus_probe_http_duration_seconds gauge
campus_probe_http_duration_seconds{` + identitySuffix + `,target="cqu_mirror"} 0.0632
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want),
		NameHTTPSuccess, NameHTTPStatusCode, NameHTTPDuration); err != nil {
		t.Fatal(err)
	}
}

func TestCollectHTTPNoResponseOmitsStatusAndDuration(t *testing.T) {
	now := time.Unix(1789490000, 0).UTC()
	l := latest.New()
	l.Put("hx-sy01-aaaaaa", protocol.Results{
		"cqu_mirror": {
			protocol.ProbeHTTP: {HTTP: &protocol.HTTPResult{Success: false}},
		},
	}, now)

	reg := newTestCollector(t, l, &fakeProbes{probes: []store.Probe{probe("hx-sy01-aaaaaa")}}, now)

	want := `
# HELP campus_probe_http_success Whether the HTTP probe succeeded.
# TYPE campus_probe_http_success gauge
campus_probe_http_success{` + identitySuffix + `,target="cqu_mirror"} 0
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), NameHTTPSuccess); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{NameHTTPStatusCode, NameHTTPDuration} {
		if n := testutil.CollectAndCount(reg, name); n != 0 {
			t.Errorf("%s: %d series without a response, want 0", name, n)
		}
	}
}

func TestCollectDNSFailureOmitsDuration(t *testing.T) {
	now := time.Unix(1789490000, 0).UTC()
	l := latest.New()
	l.Put("hx-sy01-aaaaaa", protocol.Results{
		"campus_dns": {
			protocol.ProbeDNS: {DNS: &protocol.DNSResult{Success: false}},
		},
	}, now)

	reg := newTestCollector(t, l, &fakeProbes{probes: []store.Probe{probe("hx-sy01-aaaaaa")}}, now)

	want := `
# HELP campus_probe_dns_success Whether the DNS probe succeeded.
# TYPE campus_probe_dns_success gauge
campus_probe_dns_success{` + identitySuffix + `,target="campus_dns"} 0
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want), NameDNSSuccess); err != nil {
		t.Fatal(err)
	}
	if n := testutil.CollectAndCount(reg, NameDNSDuration); n != 0 {
		t.Errorf("%s: %d series on failure, want 0", NameDNSDuration, n)
	}
}

func TestCollectRegisteredAndOnlineProbeCounts(t *testing.T) {
	now := time.Unix(1789490000, 0).UTC()
	l := latest.New()
	l.Put("a", icmpResults(1), now)

	disabled := probe("c")
	disabled.Enabled = false
	reg := newTestCollector(t, l, &fakeProbes{probes: []store.Probe{
		probe("a"), probe("b"), disabled,
	}}, now)

	want := `
# HELP cqu_netprobe_gateway_registered_probes Number of registered probes.
# TYPE cqu_netprobe_gateway_registered_probes gauge
cqu_netprobe_gateway_registered_probes 3
# HELP cqu_netprobe_gateway_online_probes Number of probes currently online.
# TYPE cqu_netprobe_gateway_online_probes gauge
cqu_netprobe_gateway_online_probes 1
`
	if err := testutil.GatherAndCompare(reg, strings.NewReader(want),
		NameRegisteredProbes, NameOnlineProbes); err != nil {
		t.Fatal(err)
	}
}

func TestCollectHandlesListErrorWithoutPanicking(t *testing.T) {
	now := time.Unix(1789490000, 0).UTC()
	reg := prometheus.NewRegistry()
	self := NewSelf(prometheus.NewRegistry())
	c := NewCollector(latest.New(), &fakeProbes{err: errors.New("boom")}, 30*time.Second, self)
	c.Now = func() time.Time { return now }
	reg.MustRegister(c)

	// Scrape must succeed (returning nothing) rather than panic.
	if _, err := reg.Gather(); err != nil {
		t.Fatalf("Gather() error = %v", err)
	}
	if got := testutil.ToFloat64(self.CollectErrors); got != 1 {
		t.Errorf("CollectErrors = %v, want 1", got)
	}
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'g', -1, 64)
}

func formatInt(v int64) string {
	return strconv.FormatInt(v, 10)
}
