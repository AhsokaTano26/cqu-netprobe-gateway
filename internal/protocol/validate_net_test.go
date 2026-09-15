package protocol

import (
	"math"
	"testing"
)

func TestValidateDNSAccepts(t *testing.T) {
	cases := map[string]*DNSResult{
		"success":                    {Success: true, DurationMS: f(8.4)},
		"success zero duration":      {Success: true, DurationMS: f(0)},
		"failure with null duration": {Success: false, DurationMS: nil},
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			if err := validateDNS(m); err != nil {
				t.Fatalf("validateDNS() error = %v, want nil", err)
			}
		})
	}
}

func TestValidateDNSRejects(t *testing.T) {
	cases := map[string]*DNSResult{
		"success without duration":  {Success: true, DurationMS: nil},
		"success negative duration": {Success: true, DurationMS: f(-1)},
		"failure with duration":     {Success: false, DurationMS: f(8.4)},
		"nan duration":              {Success: true, DurationMS: f(math.NaN())},
		"inf duration":              {Success: true, DurationMS: f(math.Inf(1))},
		"negative inf duration":     {Success: true, DurationMS: f(math.Inf(-1))},
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			assertCode(t, validateDNS(m), CodeInvalidPayload)
		})
	}
}

func TestValidateHTTPAccepts(t *testing.T) {
	cases := map[string]*HTTPResult{
		"200 ok":       {Success: true, StatusCode: i(200), DurationMS: f(51.2)},
		"204 ok":       {Success: true, StatusCode: i(204), DurationMS: f(1)},
		"301 redirect": {Success: true, StatusCode: i(301), DurationMS: f(1)},
		"399 boundary": {Success: true, StatusCode: i(399), DurationMS: f(1)},
		"404 false":    {Success: false, StatusCode: i(404), DurationMS: f(1)},
		"500 false":    {Success: false, StatusCode: i(500), DurationMS: f(63.2)},
		"no response":  {Success: false, StatusCode: nil, DurationMS: nil},
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			if err := validateHTTP(m); err != nil {
				t.Fatalf("validateHTTP() error = %v, want nil", err)
			}
		})
	}
}

func TestValidateHTTPRejects(t *testing.T) {
	cases := map[string]*HTTPResult{
		"success without status":              {Success: true, StatusCode: nil, DurationMS: f(1)},
		"success without duration":            {Success: true, StatusCode: i(200), DurationMS: nil},
		"failure with no status but duration": {Success: false, StatusCode: nil, DurationMS: f(1)},
		"failure with status but no duration": {Success: false, StatusCode: i(500), DurationMS: nil},
		"status below 100":                    {Success: false, StatusCode: i(99), DurationMS: f(1)},
		"status above 599":                    {Success: false, StatusCode: i(600), DurationMS: f(1)},
		"200 marked unsuccessful":             {Success: false, StatusCode: i(200), DurationMS: f(1)},
		"301 marked unsuccessful":             {Success: false, StatusCode: i(301), DurationMS: f(1)},
		"500 marked successful":               {Success: true, StatusCode: i(500), DurationMS: f(1)},
		"404 marked successful":               {Success: true, StatusCode: i(404), DurationMS: f(1)},
		"negative duration":                   {Success: true, StatusCode: i(200), DurationMS: f(-1)},
		"nan duration":                        {Success: true, StatusCode: i(200), DurationMS: f(math.NaN())},
		"inf duration":                        {Success: true, StatusCode: i(200), DurationMS: f(math.Inf(1))},
	}
	for name, m := range cases {
		t.Run(name, func(t *testing.T) {
			assertCode(t, validateHTTP(m), CodeInvalidPayload)
		})
	}
}

func TestValidateMeasurementDispatch(t *testing.T) {
	ok := Measurement{
		ICMP: validICMP(),
		DNS:  &DNSResult{Success: true, DurationMS: f(1)},
		HTTP: &HTTPResult{Success: true, StatusCode: i(200), DurationMS: f(1)},
	}
	if err := validateMeasurement(ProbeICMP, ok); err != nil {
		t.Errorf("icmp: %v", err)
	}
	if err := validateMeasurement(ProbeDNS, ok); err != nil {
		t.Errorf("dns: %v", err)
	}
	if err := validateMeasurement(ProbeHTTP, ok); err != nil {
		t.Errorf("http: %v", err)
	}
	if err := validateMeasurement(ProbeType("tcp"), ok); err == nil {
		t.Error("unknown probe type should be rejected")
	}
}
