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

func TestValidateRejectsMeasurementFailure(t *testing.T) {
	body := `{"version":1,"timestamp":1,"probe_version":"1","results":{"aliyun_dns":{"icmp":{"success":true,"sent":5,"received":0,"loss_ratio":1.0,"min_rtt_ms":null,"avg_rtt_ms":null,"max_rtt_ms":null,"jitter_ms":null}}}}`
	assertCode(t, decodeOrFail(t, body).Validate(testAllowlist()), CodeInvalidPayload)
}

func TestValidateReportsFirstErrorDeterministically(t *testing.T) {
	// Two bad targets; the error must be stable across runs so operators can
	// correlate logs. Validate walks targets in sorted order.
	body := `{"version":1,"timestamp":1,"probe_version":"1","results":{"zzz_bad":{"icmp":{"success":true,"sent":1,"received":1,"loss_ratio":0,"min_rtt_ms":1,"avg_rtt_ms":1,"max_rtt_ms":1,"jitter_ms":null}},"aaa_bad":{"icmp":{"success":true,"sent":1,"received":1,"loss_ratio":0,"min_rtt_ms":1,"avg_rtt_ms":1,"max_rtt_ms":1,"jitter_ms":null}}}}`
	for i := 0; i < 20; i++ {
		err := decodeOrFail(t, body).Validate(testAllowlist())
		var pe *Error
		if !errors.As(err, &pe) {
			t.Fatalf("want *Error, got %T", err)
		}
		if pe.Code != CodeInvalidTarget {
			t.Fatalf("code = %q, want %q", pe.Code, CodeInvalidTarget)
		}
	}
}

func TestValidateEmptyAllowlistRejectsEverything(t *testing.T) {
	body := `{"version":1,"timestamp":1,"probe_version":"1","results":{"aliyun_dns":{"icmp":{"success":true,"sent":1,"received":1,"loss_ratio":0,"min_rtt_ms":1,"avg_rtt_ms":1,"max_rtt_ms":1,"jitter_ms":null}}}}`
	assertCode(t, decodeOrFail(t, body).Validate(Allowlist{}), CodeInvalidTarget)
}
