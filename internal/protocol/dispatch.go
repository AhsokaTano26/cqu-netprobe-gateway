package protocol

// Measurement parameters dispatched with the target list (Protocol v1 §32.4).
//
// These are constants, not settings: every probe measures every target the same
// way, so they are neither per-target nor per-probe, and there is no page that
// edits them. They are dispatched anyway rather than compiled into each probe,
// for the reason §32 exists at all — the alternative is two repositories that
// must be changed together and two binaries that can drift apart.
//
// Changing a value here changes what every probe does on its next refresh. The
// same numbers appear in docs/openapi.json and in the protocol document, and
// internal/api's spec test fails if they disagree.
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
