package metrics

import (
	"sort"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/tano/cqu-netprobe-gateway/internal/latest"
	"github.com/tano/cqu-netprobe-gateway/internal/protocol"
	"github.com/tano/cqu-netprobe-gateway/internal/store"
)

// ProbeLister supplies the registered probes. It is satisfied by *store.Store.
type ProbeLister interface {
	ListProbes() ([]store.Probe, error)
}

// Collector renders probe metrics at scrape time.
//
// Computing on scrape rather than maintaining GaugeVecs on push is what makes
// the stale rules trivial: a probe that has gone quiet simply is not emitted,
// instead of requiring DeletePartialMatch calls on a timer racing with writers.
type Collector struct {
	latest    *latest.Store
	probes    ProbeLister
	threshold time.Duration
	self      *Self

	// Now is injectable so tests can freeze the clock.
	Now func() time.Time

	online           *prometheus.Desc
	lastSeen         *prometheus.Desc
	icmpSuccess      *prometheus.Desc
	icmpLossRatio    *prometheus.Desc
	icmpRTT          *prometheus.Desc
	icmpRTTMin       *prometheus.Desc
	icmpRTTMax       *prometheus.Desc
	icmpJitter       *prometheus.Desc
	dnsSuccess       *prometheus.Desc
	dnsDuration      *prometheus.Desc
	httpSuccess      *prometheus.Desc
	httpDuration     *prometheus.Desc
	httpStatusCode   *prometheus.Desc
	registeredProbes *prometheus.Desc
	onlineProbes     *prometheus.Desc
}

// NewCollector builds a collector. threshold is the online window; Protocol v1
// fixes it at 30 seconds.
func NewCollector(l *latest.Store, src ProbeLister, threshold time.Duration, self *Self) *Collector {
	return &Collector{
		latest:    l,
		probes:    src,
		threshold: threshold,
		self:      self,
		Now:       time.Now,

		online: prometheus.NewDesc(NameOnline,
			"Whether the probe pushed successfully within the online threshold.",
			IdentityLabelNames, nil),
		lastSeen: prometheus.NewDesc(NameLastSeen,
			"Unix timestamp of the last accepted push.",
			IdentityLabelNames, nil),
		icmpSuccess: prometheus.NewDesc(NameICMPSuccess,
			"Whether the ICMP probe succeeded.", MeasurementLabelNames, nil),
		icmpLossRatio: prometheus.NewDesc(NameICMPLossRatio,
			"ICMP packet loss ratio.", MeasurementLabelNames, nil),
		icmpRTT: prometheus.NewDesc(NameICMPRTT,
			"Average ICMP round-trip time in seconds.", MeasurementLabelNames, nil),
		icmpRTTMin: prometheus.NewDesc(NameICMPRTTMin,
			"Minimum ICMP round-trip time in seconds.", MeasurementLabelNames, nil),
		icmpRTTMax: prometheus.NewDesc(NameICMPRTTMax,
			"Maximum ICMP round-trip time in seconds.", MeasurementLabelNames, nil),
		icmpJitter: prometheus.NewDesc(NameICMPJitter,
			"ICMP jitter in seconds.", MeasurementLabelNames, nil),
		dnsSuccess: prometheus.NewDesc(NameDNSSuccess,
			"Whether the DNS probe succeeded.", MeasurementLabelNames, nil),
		dnsDuration: prometheus.NewDesc(NameDNSDuration,
			"DNS resolution duration in seconds.", MeasurementLabelNames, nil),
		httpSuccess: prometheus.NewDesc(NameHTTPSuccess,
			"Whether the HTTP probe succeeded.", MeasurementLabelNames, nil),
		httpDuration: prometheus.NewDesc(NameHTTPDuration,
			"HTTP request duration in seconds.", MeasurementLabelNames, nil),
		httpStatusCode: prometheus.NewDesc(NameHTTPStatusCode,
			"HTTP response status code.", MeasurementLabelNames, nil),
		registeredProbes: prometheus.NewDesc(NameRegisteredProbes,
			"Number of registered probes.", nil, nil),
		onlineProbes: prometheus.NewDesc(NameOnlineProbes,
			"Number of probes currently online.", nil, nil),
	}
}

// Describe sends every descriptor. UncheckedCollector is not used because an
// explicit Describe is what makes the metric set auditable.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.online
	ch <- c.lastSeen
	ch <- c.icmpSuccess
	ch <- c.icmpLossRatio
	ch <- c.icmpRTT
	ch <- c.icmpRTTMin
	ch <- c.icmpRTTMax
	ch <- c.icmpJitter
	ch <- c.dnsSuccess
	ch <- c.dnsDuration
	ch <- c.httpSuccess
	ch <- c.httpDuration
	ch <- c.httpStatusCode
	ch <- c.registeredProbes
	ch <- c.onlineProbes
}

// Collect renders every registered probe's current state.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	probes, err := c.probes.ListProbes()
	if err != nil {
		// A scrape that cannot read the probe table must not report stale
		// numbers as if they were current. Emitting nothing is the honest
		// outcome; the error is counted so it is alertable.
		if c.self != nil {
			c.self.CollectErrors.Inc()
		}
		return
	}

	now := c.Now()
	var registered, online float64

	for _, p := range probes {
		registered++
		if !p.Enabled {
			// A disabled probe is a deliberate administrative action, not an
			// outage. Emitting online=0 would conflate the two.
			continue
		}

		ident := identityLabels(p)
		entry, ok := c.latest.Get(p.ProbeID)
		if !ok {
			// Registered but never pushed: report offline, and omit last_seen
			// rather than emitting a bogus 1970 timestamp.
			ch <- prometheus.MustNewConstMetric(c.online, prometheus.GaugeValue, 0, ident...)
			continue
		}

		isOnline := now.Sub(entry.ServerReceivedAt) <= c.threshold
		if isOnline {
			online++
			ch <- prometheus.MustNewConstMetric(c.online, prometheus.GaugeValue, 1, ident...)
			c.emitMeasurements(ch, entry.Results, ident)
		} else {
			ch <- prometheus.MustNewConstMetric(c.online, prometheus.GaugeValue, 0, ident...)
		}
		ch <- prometheus.MustNewConstMetric(c.lastSeen, prometheus.GaugeValue,
			float64(entry.ServerReceivedAt.Unix()), ident...)
	}

	ch <- prometheus.MustNewConstMetric(c.registeredProbes, prometheus.GaugeValue, registered)
	ch <- prometheus.MustNewConstMetric(c.onlineProbes, prometheus.GaugeValue, online)
}

// identityLabels returns the Gateway-owned label values, always in the order
// declared by IdentityLabelNames.
func identityLabels(p store.Probe) []string {
	return []string{
		p.ProbeID,
		p.CampusCode,
		p.BuildingGroupCode,
		p.BuildingCode,
		p.NetworkType,
	}
}

// emitMeasurements walks targets and probe types in sorted order so the
// exposition is deterministic and diffs are readable.
func (c *Collector) emitMeasurements(ch chan<- prometheus.Metric, results protocol.Results, ident []string) {
	targets := make([]string, 0, len(results))
	for target := range results {
		targets = append(targets, target)
	}
	sort.Strings(targets)

	for _, target := range targets {
		byType := results[target]
		types := make([]string, 0, len(byType))
		for pt := range byType {
			types = append(types, string(pt))
		}
		sort.Strings(types)

		for _, typeName := range types {
			pt := protocol.ProbeType(typeName)
			m := byType[pt]
			labels := append(append([]string{}, ident...), target)

			switch pt {
			case protocol.ProbeICMP:
				c.emitICMP(ch, m.ICMP, labels)
			case protocol.ProbeDNS:
				c.emitDNS(ch, m.DNS, labels)
			case protocol.ProbeHTTP:
				c.emitHTTP(ch, m.HTTP, labels)
			}
		}
	}
}

func (c *Collector) emitICMP(ch chan<- prometheus.Metric, m *protocol.ICMPResult, labels []string) {
	if m == nil {
		return
	}
	ch <- prometheus.MustNewConstMetric(c.icmpSuccess, prometheus.GaugeValue, boolValue(m.Success), labels...)
	ch <- prometheus.MustNewConstMetric(c.icmpLossRatio, prometheus.GaugeValue, m.LossRatio, labels...)

	// Protocol v1 §8: null RTT means "no measurement", which is distinct from
	// 0 ms, so no series is emitted at all for a nil field.
	if m.MinRTTMS != nil {
		ch <- prometheus.MustNewConstMetric(c.icmpRTTMin, prometheus.GaugeValue, millisecondsToSeconds(*m.MinRTTMS), labels...)
	}
	if m.AvgRTTMS != nil {
		ch <- prometheus.MustNewConstMetric(c.icmpRTT, prometheus.GaugeValue, millisecondsToSeconds(*m.AvgRTTMS), labels...)
	}
	if m.MaxRTTMS != nil {
		ch <- prometheus.MustNewConstMetric(c.icmpRTTMax, prometheus.GaugeValue, millisecondsToSeconds(*m.MaxRTTMS), labels...)
	}
	if m.JitterMS != nil {
		ch <- prometheus.MustNewConstMetric(c.icmpJitter, prometheus.GaugeValue, millisecondsToSeconds(*m.JitterMS), labels...)
	}
}

func (c *Collector) emitDNS(ch chan<- prometheus.Metric, m *protocol.DNSResult, labels []string) {
	if m == nil {
		return
	}
	ch <- prometheus.MustNewConstMetric(c.dnsSuccess, prometheus.GaugeValue, boolValue(m.Success), labels...)
	if m.DurationMS != nil {
		ch <- prometheus.MustNewConstMetric(c.dnsDuration, prometheus.GaugeValue, millisecondsToSeconds(*m.DurationMS), labels...)
	}
}

func (c *Collector) emitHTTP(ch chan<- prometheus.Metric, m *protocol.HTTPResult, labels []string) {
	if m == nil {
		return
	}
	ch <- prometheus.MustNewConstMetric(c.httpSuccess, prometheus.GaugeValue, boolValue(m.Success), labels...)

	// Protocol v1 §11 has two distinct failure shapes. A probe that reached the
	// server and got 500 reports both status and duration; one that never got a
	// response reports neither.
	if m.StatusCode != nil {
		ch <- prometheus.MustNewConstMetric(c.httpStatusCode, prometheus.GaugeValue, float64(*m.StatusCode), labels...)
	}
	if m.DurationMS != nil {
		ch <- prometheus.MustNewConstMetric(c.httpDuration, prometheus.GaugeValue, millisecondsToSeconds(*m.DurationMS), labels...)
	}
}

func boolValue(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
