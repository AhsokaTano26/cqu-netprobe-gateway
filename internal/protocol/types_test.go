package protocol

import (
	"errors"
	"testing"
)

const validICMPBody = `{
  "version": 1,
  "timestamp": 1789490000,
  "probe_version": "0.1.0",
  "results": {
    "aliyun_dns": {
      "icmp": {
        "success": true, "sent": 5, "received": 5, "loss_ratio": 0.0,
        "min_rtt_ms": 10.2, "avg_rtt_ms": 12.3, "max_rtt_ms": 15.8, "jitter_ms": 1.4
      }
    }
  }
}`

func TestDecodeValid(t *testing.T) {
	req, err := Decode([]byte(validICMPBody))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	if req.Version != 1 {
		t.Errorf("Version = %d, want 1", req.Version)
	}
	if req.Timestamp != 1789490000 {
		t.Errorf("Timestamp = %d, want 1789490000", req.Timestamp)
	}
	if req.ProbeVersion != "0.1.0" {
		t.Errorf("ProbeVersion = %q, want 0.1.0", req.ProbeVersion)
	}
	m, ok := req.Results["aliyun_dns"][ProbeICMP]
	if !ok {
		t.Fatalf("missing aliyun_dns/icmp in %#v", req.Results)
	}
	if m.ICMP == nil {
		t.Fatal("ICMP payload is nil")
	}
	if m.ICMP.Sent != 5 || m.ICMP.Received != 5 {
		t.Errorf("sent/received = %d/%d, want 5/5", m.ICMP.Sent, m.ICMP.Received)
	}
	if m.ICMP.MinRTTMS == nil || *m.ICMP.MinRTTMS != 10.2 {
		t.Errorf("MinRTTMS = %v, want 10.2", m.ICMP.MinRTTMS)
	}
	if m.ICMP.JitterMS == nil || *m.ICMP.JitterMS != 1.4 {
		t.Errorf("JitterMS = %v, want 1.4", m.ICMP.JitterMS)
	}
}

func TestDecodeRejectsMalformedJSON(t *testing.T) {
	_, err := Decode([]byte(`{"version": 1,`))
	assertCode(t, err, CodeInvalidJSON)
}

func TestDecodeRejectsWrongTopLevelType(t *testing.T) {
	_, err := Decode([]byte(`{"version": "one"}`))
	assertCode(t, err, CodeInvalidJSON)
}

func TestDecodeIgnoresUnknownTopLevelFields(t *testing.T) {
	body := `{"version":1,"timestamp":1,"probe_version":"1","future_field":{"a":1},"results":{"campus_dns":{"dns":{"success":true,"duration_ms":1.0}}}}`
	if _, err := Decode([]byte(body)); err != nil {
		t.Fatalf("Decode() with unknown top-level field should succeed, got %v", err)
	}
}

func TestDecodeIgnoresUnknownFieldsInsideMeasurement(t *testing.T) {
	body := `{"version":1,"timestamp":1,"probe_version":"1","results":{"campus_dns":{"dns":{"success":true,"duration_ms":1.0,"resolved_ip":"1.2.3.4","ttl":300}}}}`
	if _, err := Decode([]byte(body)); err != nil {
		t.Fatalf("Decode() with unknown measurement field should succeed, got %v", err)
	}
}

func TestDecodeDistinguishesNullFromAbsent(t *testing.T) {
	// Both forms must decode without error; both leave the pointer nil.
	withNull := `{"version":1,"timestamp":1,"probe_version":"1","results":{"a":{"icmp":{"success":false,"sent":3,"received":0,"loss_ratio":1.0,"min_rtt_ms":null,"avg_rtt_ms":null,"max_rtt_ms":null,"jitter_ms":null}}}}`
	absent := `{"version":1,"timestamp":1,"probe_version":"1","results":{"a":{"icmp":{"success":false,"sent":3,"received":0,"loss_ratio":1.0}}}}`

	for name, body := range map[string]string{"explicit null": withNull, "absent": absent} {
		t.Run(name, func(t *testing.T) {
			req, err := Decode([]byte(body))
			if err != nil {
				t.Fatalf("Decode() error = %v", err)
			}
			icmp := req.Results["a"][ProbeICMP].ICMP
			if icmp == nil {
				t.Fatal("ICMP payload is nil")
			}
			if icmp.MinRTTMS != nil {
				t.Errorf("MinRTTMS = %v, want nil", *icmp.MinRTTMS)
			}
		})
	}
}

func TestDecodeRejectsTrailingContent(t *testing.T) {
	valid := `{"version":1,"timestamp":1,"probe_version":"1","results":{"campus_dns":{"dns":{"success":true,"duration_ms":1.0}}}}`

	rejected := map[string]string{
		"second JSON value":   valid + `{"version":2}`,
		"garbage":             valid + ` garbage`,
		"second array":        valid + `[]`,
		"stray close bracket": valid + `]`,
		"stray close brace":   valid + `}`,
	}
	for name, body := range rejected {
		t.Run(name, func(t *testing.T) {
			_, err := Decode([]byte(body))
			assertCode(t, err, CodeInvalidJSON)
		})
	}

	accepted := map[string]string{
		"trailing whitespace": valid + "  \n\t",
		"trailing newline":    valid + "\n",
		"no trailing content": valid,
	}
	for name, body := range accepted {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode([]byte(body)); err != nil {
				t.Fatalf("Decode() with %s error = %v, want nil", name, err)
			}
		})
	}
}

func TestDecodeRejectsUnknownProbeTypeShape(t *testing.T) {
	// An unrecognised probe type key must still be captured so validation can
	// reject the whole push with invalid_probe_type.
	body := `{"version":1,"timestamp":1,"probe_version":"1","results":{"a":{"tcp":{"success":true}}}}`
	req, err := Decode([]byte(body))
	if err != nil {
		t.Fatalf("Decode() should not reject at decode time, got %v", err)
	}
	if _, ok := req.Results["a"][ProbeType("tcp")]; !ok {
		t.Fatalf("unknown probe type was dropped during decode: %#v", req.Results)
	}
}

func assertCode(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil {
		t.Fatalf("want error with code %q, got nil", want)
	}
	var pe *Error
	if !errors.As(err, &pe) {
		t.Fatalf("want *protocol.Error, got %T: %v", err, err)
	}
	if pe.Code != want {
		t.Fatalf("error code = %q, want %q", pe.Code, want)
	}
}
