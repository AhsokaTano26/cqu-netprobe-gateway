package protocol

import (
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
)

// Measurement parameters dispatched with the target list (Protocol v1 §32.4).
//
// These are the defaults. Every probe measures every target the same way, so
// the parameters are neither per-target nor per-probe, but they are not fixed
// either: an administrator edits them at /admin/settings and the stored set
// replaces these on every probe's next refresh. They are dispatched rather than
// compiled into each probe for the reason §32 exists at all — the alternative is
// two repositories that must be changed together and two binaries that drift.
//
// Changing a constant here changes what a probe does only until an
// administrator saves their own set; it also changes what a fresh deployment
// starts with. The same numbers appear in docs/openapi.json and in the protocol
// document, and internal/api's spec test fails if the three disagree.
const (
	// IntervalMS is the measurement cycle: the time between the starts of two
	// consecutive rounds (§18), which is also the push cadence. It must stay
	// compatible with the per-token push rate limit of §21.
	IntervalMS = 10_000

	// ICMP round parameters (§7): how many echo requests, how far apart, and how
	// long each one waits before it counts as lost.
	ICMPCount      = 5
	ICMPIntervalMS = 200
	ICMPTimeoutMS  = 1000

	// HTTP parameters (§11). VerifyTLS is part of the measurement, not a
	// convenience: a probe that skips verification measures a different thing
	// than one that does, and the results would be silently incomparable.
	HTTPMethod          = "GET"
	HTTPFollowRedirects = true
	HTTPVerifyTLS       = true
	HTTPTimeoutMS       = 5000

	// DNS parameters (§10).
	DNSTransport = "udp"
	DNSTimeoutMS = 3000
)

// MeasurementConfig is the `config` object of the target list response.
//
// Every duration is in milliseconds and says so in its name, following §24: the
// probe-facing wire format uses milliseconds throughout. A structure that mixed
// seconds and milliseconds is how a probe ends up sleeping ten milliseconds
// between rounds instead of ten seconds.
type MeasurementConfig struct {
	// IntervalMS is the measurement cycle.
	IntervalMS int `json:"interval_ms"`
	// ICMP, HTTP and DNS are the parameters for each measurement type. All three
	// are always present: a probe that only measures ICMP targets ignores the
	// other two rather than having to handle their absence.
	ICMP ICMPConfig `json:"icmp"`
	HTTP HTTPConfig `json:"http"`
	DNS  DNSConfig  `json:"dns"`
}

// ICMPConfig is the ICMP round shape defined by Protocol v1 §7.
type ICMPConfig struct {
	// Count is the number of echo requests per round, which is the `sent` value
	// the probe reports back.
	Count int `json:"count"`
	// IntervalMS is the delay between two echo requests.
	IntervalMS int `json:"interval_ms"`
	// TimeoutMS is how long a single echo request waits for its reply.
	TimeoutMS int `json:"timeout_ms"`
}

// HTTPConfig is the HTTP measurement shape defined by Protocol v1 §11.
type HTTPConfig struct {
	// Method is the request method. v1 fixes it to GET.
	Method string `json:"method"`
	// FollowRedirects controls whether a 3xx response is followed. The success
	// rule of §11 (200 <= status < 400) applies to whichever response ends the
	// chain.
	FollowRedirects bool `json:"follow_redirects"`
	// VerifyTLS controls certificate verification.
	VerifyTLS bool `json:"verify_tls"`
	// TimeoutMS is the overall timeout for the whole request, redirects
	// included.
	TimeoutMS int `json:"timeout_ms"`
}

// DNSConfig is the DNS measurement shape defined by Protocol v1 §10.
type DNSConfig struct {
	// Transport is the transport used to query the server. v1 fixes it to udp.
	Transport string `json:"transport"`
	// TimeoutMS is the overall timeout for the query.
	TimeoutMS int `json:"timeout_ms"`
}

// MinIntervalMS is the shortest measurement cycle the protocol accepts. It is a
// sanity floor, not the real constraint: whether a cycle is fast enough to be
// throttled depends on the deployment's push rate limit, which this package
// cannot see. The admin form enforces that one.
const MinIntervalMS = 1000

// ICMPRoundMS is the worst-case duration of one ICMP round: the gaps between
// the echo requests, plus the last request's timeout. Note it is not
// Count*IntervalMS — the final request is sent at (Count-1)*IntervalMS and then
// waits TimeoutMS for its reply.
func (c MeasurementConfig) ICMPRoundMS() int {
	if c.ICMP.Count <= 0 {
		return 0
	}
	return (c.ICMP.Count-1)*c.ICMP.IntervalMS + c.ICMP.TimeoutMS
}

// Validate rejects parameters that would make probes misbehave. It is the single
// gate for administrator-supplied values: these numbers reach every probe on its
// next refresh, so a bad set takes the whole fleet with it at once.
//
// The messages are written in Chinese because the admin form renders them
// verbatim, next to the field that was rejected. Elsewhere in this package the
// errors are English and never shown to a person.
func (c MeasurementConfig) Validate() error {
	if c.IntervalMS < MinIntervalMS {
		return fmt.Errorf("测量周期必须不小于 %d 毫秒", MinIntervalMS)
	}

	switch {
	case c.ICMP.Count <= 0:
		return errors.New("ICMP 每轮次数必须大于 0")
	case c.ICMP.IntervalMS <= 0:
		return errors.New("ICMP 请求间隔必须大于 0")
	case c.ICMP.TimeoutMS <= 0:
		return errors.New("ICMP 单次超时必须大于 0")
	}
	// A round that outlives the cycle makes the probe start the next one before
	// the previous finished, and two rounds' worth of timeouts get reported as
	// one. The symptom is under-reported loss, not an error.
	if round := c.ICMPRoundMS(); round >= c.IntervalMS {
		return fmt.Errorf("ICMP 整轮最坏耗时 %d 毫秒（%d 次 × %d 毫秒间隔 + %d 毫秒超时）必须小于测量周期 %d 毫秒",
			round, c.ICMP.Count, c.ICMP.IntervalMS, c.ICMP.TimeoutMS, c.IntervalMS)
	}

	// v1 fixes these two. They are part of the measurement's meaning, not
	// knobs: a probe that followed redirects, or queried over TCP, would
	// produce numbers that cannot be compared with the rest of the fleet.
	if c.HTTP.Method != HTTPMethod {
		return fmt.Errorf("HTTP 方法在协议 v1 中固定为 %s", HTTPMethod)
	}
	if c.DNS.Transport != DNSTransport {
		return fmt.Errorf("DNS 传输层在协议 v1 中固定为 %s", DNSTransport)
	}

	switch {
	case c.HTTP.TimeoutMS <= 0:
		return errors.New("HTTP 超时必须大于 0")
	case c.HTTP.TimeoutMS >= c.IntervalMS:
		return fmt.Errorf("HTTP 超时 %d 毫秒必须小于测量周期 %d 毫秒", c.HTTP.TimeoutMS, c.IntervalMS)
	case c.DNS.TimeoutMS <= 0:
		return errors.New("DNS 超时必须大于 0")
	case c.DNS.TimeoutMS >= c.IntervalMS:
		return fmt.Errorf("DNS 超时 %d 毫秒必须小于测量周期 %d 毫秒", c.DNS.TimeoutMS, c.IntervalMS)
	}
	return nil
}

// configIDNamespace is the UUIDv5 namespace for measurement configs. The value
// is arbitrary and used nowhere else; it only has to be fixed, so that the same
// parameter set always maps to the same UUID.
var configIDNamespace = [16]byte{
	0x6f, 0x1d, 0x3d, 0x2e, 0x8a, 0x14, 0x4b, 0x77,
	0x9c, 0x31, 0x2f, 0x0b, 0x5e, 0x88, 0x41, 0xd2,
}

// ID identifies this parameter set, as a UUID derived from its values.
//
// It is derived rather than generated, which is the property the staleness
// check needs: saving the form again with the same numbers produces the same
// ID, so a no-op edit does not tell every probe in the fleet to re-fetch. It
// also means nothing has to be stored — the ID of a deployment that has never
// customised anything is simply the ID of the defaults.
//
// The marshalling order is the struct's field order, which is fixed, so the
// bytes hashed are the same on every run.
func (c MeasurementConfig) ID() string {
	canonical, err := json.Marshal(c)
	if err != nil {
		// Unreachable: every field is an int, a bool or a string.
		return ""
	}
	h := sha1.New() //nolint:gosec // UUIDv5 is defined over SHA-1; this is identity, not integrity.
	_, _ = h.Write(configIDNamespace[:])
	_, _ = h.Write(canonical)
	sum := h.Sum(nil)

	var id [16]byte
	copy(id[:], sum)
	id[6] = (id[6] & 0x0f) | 0x50 // version 5: name-based
	id[8] = (id[8] & 0x3f) | 0x80 // RFC 4122 variant
	return formatUUID(id)
}

func formatUUID(id [16]byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, 36)
	for i, b := range id {
		if i == 4 || i == 6 || i == 8 || i == 10 {
			out = append(out, '-')
		}
		out = append(out, hexdigits[b>>4], hexdigits[b&0x0f])
	}
	return string(out)
}

// DefaultMeasurementConfig returns the v1 measurement parameters.
func DefaultMeasurementConfig() MeasurementConfig {
	return MeasurementConfig{
		IntervalMS: IntervalMS,
		ICMP: ICMPConfig{
			Count:      ICMPCount,
			IntervalMS: ICMPIntervalMS,
			TimeoutMS:  ICMPTimeoutMS,
		},
		HTTP: HTTPConfig{
			Method:          HTTPMethod,
			FollowRedirects: HTTPFollowRedirects,
			VerifyTLS:       HTTPVerifyTLS,
			TimeoutMS:       HTTPTimeoutMS,
		},
		DNS: DNSConfig{
			Transport: DNSTransport,
			TimeoutMS: DNSTimeoutMS,
		},
	}
}
