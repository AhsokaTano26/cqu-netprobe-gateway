package protocol

import (
	"errors"
	"strings"
	"testing"
)

func testAllowlist() Allowlist {
	return Allowlist{
		"campus_dns":     {ProbeICMP: true, ProbeDNS: true},
		"aliyun_dns":     {ProbeICMP: true},
		"cqu_mirror":     {ProbeHTTP: true},
		"cloudflare_dns": {ProbeICMP: true},
	}
}

func decodeOrFail(t *testing.T, body string) *PushRequest {
	t.Helper()
	req, err := Decode([]byte(body))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	return req
}

func TestValidateAcceptsFullRequest(t *testing.T) {
	body := `{
	  "version": 1,
	  "timestamp": 1789490000,
	  "probe_version": "0.1.0",
	  "results": {
	    "aliyun_dns": {"icmp": {"success": true, "sent": 5, "received": 5, "loss_ratio": 0.0,
	      "min_rtt_ms": 10.2, "avg_rtt_ms": 12.3, "max_rtt_ms": 15.8, "jitter_ms": 1.4}},
	    "campus_dns": {"dns": {"success": true, "duration_ms": 8.4},
	                   "icmp": {"success": true, "sent": 3, "received": 3, "loss_ratio": 0.0,
	                            "min_rtt_ms": 1.0, "avg_rtt_ms": 2.0, "max_rtt_ms": 3.0, "jitter_ms": 1.0}},
	    "cqu_mirror": {"http": {"success": true, "status_code": 200, "duration_ms": 51.2}}
	  }
	}`
	if err := decodeOrFail(t, body).Validate(testAllowlist()); err != nil {
		t.Fatalf("Validate() error = %v, want nil", err)
	}
}

func TestValidateRejectsUnsupportedVersion(t *testing.T) {
	body := `{"version":2,"timestamp":1,"probe_version":"1","results":{"aliyun_dns":{"icmp":{"success":true,"sent":1,"received":1,"loss_ratio":0,"min_rtt_ms":1,"avg_rtt_ms":1,"max_rtt_ms":1,"jitter_ms":null}}}}`
	assertCode(t, decodeOrFail(t, body).Validate(testAllowlist()), CodeUnsupportedVersion)
}

func TestValidateRejectsVersionZero(t *testing.T) {
	body := `{"version":0,"timestamp":1,"probe_version":"1","results":{"aliyun_dns":{"icmp":{"success":true,"sent":1,"received":1,"loss_ratio":0,"min_rtt_ms":1,"avg_rtt_ms":1,"max_rtt_ms":1,"jitter_ms":null}}}}`
	assertCode(t, decodeOrFail(t, body).Validate(testAllowlist()), CodeUnsupportedVersion)
}

func TestValidateRejectsUnknownTarget(t *testing.T) {
	body := `{"version":1,"timestamp":1,"probe_version":"1","results":{"evil_target":{"icmp":{"success":true,"sent":1,"received":1,"loss_ratio":0,"min_rtt_ms":1,"avg_rtt_ms":1,"max_rtt_ms":1,"jitter_ms":null}}}}`
	assertCode(t, decodeOrFail(t, body).Validate(testAllowlist()), CodeInvalidTarget)
}

func TestValidateRejectsProbeTypeNotAllowedForTarget(t *testing.T) {
	// cqu_mirror permits only http.
	body := `{"version":1,"timestamp":1,"probe_version":"1","results":{"cqu_mirror":{"icmp":{"success":true,"sent":1,"received":1,"loss_ratio":0,"min_rtt_ms":1,"avg_rtt_ms":1,"max_rtt_ms":1,"jitter_ms":null}}}}`
	assertCode(t, decodeOrFail(t, body).Validate(testAllowlist()), CodeInvalidProbeType)
}

func TestValidateRejectsUnknownProbeType(t *testing.T) {
	body := `{"version":1,"timestamp":1,"probe_version":"1","results":{"aliyun_dns":{"tcp":{"success":true}}}}`
	assertCode(t, decodeOrFail(t, body).Validate(testAllowlist()), CodeInvalidProbeType)
}

func TestValidateRejectsEmptyResults(t *testing.T) {
	body := `{"version":1,"timestamp":1,"probe_version":"1","results":{}}`
	assertCode(t, decodeOrFail(t, body).Validate(testAllowlist()), CodeInvalidPayload)
}

func TestValidateRejectsTargetWithNoMeasurements(t *testing.T) {
	body := `{"version":1,"timestamp":1,"probe_version":"1","results":{"aliyun_dns":{}}}`
	assertCode(t, decodeOrFail(t, body).Validate(testAllowlist()), CodeInvalidPayload)
}

func TestValidateRejectsMissingResults(t *testing.T) {
	body := `{"version":1,"timestamp":1,"probe_version":"1"}`
	assertCode(t, decodeOrFail(t, body).Validate(testAllowlist()), CodeInvalidPayload)
}

func TestValidateRejectsNegativeTimestamp(t *testing.T) {
	body := `{"version":1,"timestamp":-5,"probe_version":"1","results":{"aliyun_dns":{"icmp":{"success":true,"sent":1,"received":1,"loss_ratio":0,"min_rtt_ms":1,"avg_rtt_ms":1,"max_rtt_ms":1,"jitter_ms":null}}}}`
	assertCode(t, decodeOrFail(t, body).Validate(testAllowlist()), CodeInvalidPayload)
}

func TestValidateAcceptsZeroTimestamp(t *testing.T) {
	// Timestamp is auxiliary metadata; it is never range-checked against the
	// server clock because clock skew must not reject an otherwise valid push.
	body := `{"version":1,"timestamp":0,"probe_version":"1","results":{"aliyun_dns":{"icmp":{"success":true,"sent":1,"received":1,"loss_ratio":0,"min_rtt_ms":1,"avg_rtt_ms":1,"max_rtt_ms":1,"jitter_ms":null}}}}`
	if err := decodeOrFail(t, body).Validate(testAllowlist()); err != nil {
		t.Fatalf("timestamp 0 should be accepted, got %v", err)
	}
}

func TestValidateRejectsOversizedProbeVersion(t *testing.T) {
	long := strings.Repeat("x", MaxProbeVersionLen+1)
	body := `{"version":1,"timestamp":1,"probe_version":"` + long + `","results":{"aliyun_dns":{"icmp":{"success":true,"sent":1,"received":1,"loss_ratio":0,"min_rtt_ms":1,"avg_rtt_ms":1,"max_rtt_ms":1,"jitter_ms":null}}}}`
	assertCode(t, decodeOrFail(t, body).Validate(testAllowlist()), CodeInvalidPayload)
}

func TestValidateAcceptsMaxLengthProbeVersion(t *testing.T) {
	exact := strings.Repeat("x", MaxProbeVersionLen)
	body := `{"version":1,"timestamp":1,"probe_version":"` + exact + `","results":{"aliyun_dns":{"icmp":{"success":true,"sent":1,"received":1,"loss_ratio":0,"min_rtt_ms":1,"avg_rtt_ms":1,"max_rtt_ms":1,"jitter_ms":null}}}}`
	if err := decodeOrFail(t, body).Validate(testAllowlist()); err != nil {
		t.Fatalf("32-byte probe_version should be accepted, got %v", err)
	}
}

func TestValidateRejectsMultibyteProbeVersionOverByteLimit(t *testing.T) {
	// Protocol v1 §6 caps probe_version at 32 *bytes*, not 32 runes. 11 copies
	// of "测" are 33 bytes but only 11 runes, so a rune-counting implementation
	// would wrongly accept this. The ASCII fixtures above cannot tell the two
	// rules apart; this one pins the byte semantics.
	long := strings.Repeat("测", 11)
	if len(long) != 33 {
		t.Fatalf("fixture is %d bytes, want 33", len(long))
	}
	body := `{"version":1,"timestamp":1,"probe_version":"` + long + `","results":{"aliyun_dns":{"icmp":{"success":true,"sent":1,"received":1,"loss_ratio":0,"min_rtt_ms":1,"avg_rtt_ms":1,"max_rtt_ms":1,"jitter_ms":null}}}}`
	assertCode(t, decodeOrFail(t, body).Validate(testAllowlist()), CodeInvalidPayload)
}

func TestValidateAcceptsMultibyteProbeVersionUnderByteLimit(t *testing.T) {
	// 10 copies of "测" are 30 bytes / 10 runes: under the 32-byte cap.
	exact := strings.Repeat("测", 10)
	if len(exact) != 30 {
		t.Fatalf("fixture is %d bytes, want 30", len(exact))
	}
	body := `{"version":1,"timestamp":1,"probe_version":"` + exact + `","results":{"aliyun_dns":{"icmp":{"success":true,"sent":1,"received":1,"loss_ratio":0,"min_rtt_ms":1,"avg_rtt_ms":1,"max_rtt_ms":1,"jitter_ms":null}}}}`
	if err := decodeOrFail(t, body).Validate(testAllowlist()); err != nil {
		t.Fatalf("30-byte multibyte probe_version should be accepted, got %v", err)
	}
}

func TestValidateRejectsMeasurementFailure(t *testing.T) {
	body := `{"version":1,"timestamp":1,"probe_version":"1","results":{"aliyun_dns":{"icmp":{"success":true,"sent":5,"received":0,"loss_ratio":1.0,"min_rtt_ms":null,"avg_rtt_ms":null,"max_rtt_ms":null,"jitter_ms":null}}}}`
	assertCode(t, decodeOrFail(t, body).Validate(testAllowlist()), CodeInvalidPayload)
}

func TestValidateReportsFirstErrorDeterministically(t *testing.T) {
	// Two bad targets; the error must be stable across runs so operators can
	// correlate logs. Validate walks targets in sorted order.
	//
	// The two failures MUST stay different in kind, and this is the whole point
	// of the fixture: aliyun_dns is allowlisted and fails measurement validation
	// (received == 0 with success == true violates Protocol v1 §7) giving
	// CodeInvalidPayload, while zzz_unknown is not allowlisted at all giving
	// CodeInvalidTarget. Two identically-failing targets would produce
	// byte-identical errors, so every visitation order would yield the same
	// value and this test would pass even with the sort deleted. Do not
	// "simplify" the fixture back to two unknowns.
	//
	// Sorted order visits aliyun_dns first, so the first error is always
	// CodeInvalidPayload. Without the sort, Go's randomised map iteration
	// returns CodeInvalidTarget about half the time, which the 20 iterations
	// below catch with probability 1 - 2^-20.
	body := `{"version":1,"timestamp":1,"probe_version":"1","results":{` +
		`"aliyun_dns":{"icmp":{"success":true,"sent":5,"received":0,"loss_ratio":1.0,"min_rtt_ms":null,"avg_rtt_ms":null,"max_rtt_ms":null,"jitter_ms":null}},` +
		`"zzz_unknown":{"icmp":{"success":true,"sent":1,"received":1,"loss_ratio":0,"min_rtt_ms":1,"avg_rtt_ms":1,"max_rtt_ms":1,"jitter_ms":null}}}}`
	for i := 0; i < 20; i++ {
		err := decodeOrFail(t, body).Validate(testAllowlist())
		var pe *Error
		if !errors.As(err, &pe) {
			t.Fatalf("iteration %d: want *Error, got %T", i, err)
		}
		if pe.Code != CodeInvalidPayload {
			t.Fatalf("iteration %d: code = %q, want %q (targets must be visited in sorted order)",
				i, pe.Code, CodeInvalidPayload)
		}
	}
}

func TestValidateEmptyAllowlistRejectsEverything(t *testing.T) {
	body := `{"version":1,"timestamp":1,"probe_version":"1","results":{"aliyun_dns":{"icmp":{"success":true,"sent":1,"received":1,"loss_ratio":0,"min_rtt_ms":1,"avg_rtt_ms":1,"max_rtt_ms":1,"jitter_ms":null}}}}`
	assertCode(t, decodeOrFail(t, body).Validate(Allowlist{}), CodeInvalidTarget)
}
