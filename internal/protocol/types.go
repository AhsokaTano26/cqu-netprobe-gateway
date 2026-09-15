// Package protocol implements the Protocol v1 wire format and all of its
// validation rules. It performs no I/O and imports no other internal package,
// so every rule below is testable as a pure function.
package protocol

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// Version is the only protocol version this gateway speaks.
const Version = 1

// MaxProbeVersionLen is the probe_version limit from Protocol v1 §6.
const MaxProbeVersionLen = 32

// ProbeType is one of the measurement kinds Protocol v1 defines.
type ProbeType string

const (
	ProbeICMP ProbeType = "icmp"
	ProbeDNS  ProbeType = "dns"
	ProbeHTTP ProbeType = "http"
)

// Allowlist maps target ID to the set of probe types permitted for it,
// implementing Protocol v1 §13's Target × ProbeType allowlist.
type Allowlist map[string]map[ProbeType]bool

// PushRequest is the Protocol v1 request body (Protocol v1 §5).
type PushRequest struct {
	Version      int
	Timestamp    int64
	ProbeVersion string
	Results      Results
}

// Results maps target ID to probe type to measurement.
type Results map[string]map[ProbeType]Measurement

// Measurement carries exactly one populated field, matching the probe type it
// was decoded under. A measurement whose type does not match the payload shape
// leaves every field nil, which validation rejects.
type Measurement struct {
	ICMP *ICMPResult
	DNS  *DNSResult
	HTTP *HTTPResult
}

// ICMPResult is Protocol v1 §7. The RTT pointers are nil when the field was
// null or absent; Protocol v1 §8 treats both as "no measurement".
type ICMPResult struct {
	Success   bool
	Sent      int
	Received  int
	LossRatio float64
	MinRTTMS  *float64
	AvgRTTMS  *float64
	MaxRTTMS  *float64
	JitterMS  *float64

	present map[string]bool
}

// DNSResult is Protocol v1 §10.
type DNSResult struct {
	Success    bool
	DurationMS *float64

	present map[string]bool
}

// HTTPResult is Protocol v1 §11.
type HTTPResult struct {
	Success    bool
	StatusCode *int
	DurationMS *float64

	present map[string]bool
}

// rawRequest mirrors the JSON shape. RawMessage defers measurement decoding so
// unknown probe types survive long enough for validation to reject them with
// the right error code.
type rawRequest struct {
	Version      *int                                  `json:"version"`
	Timestamp    *int64                                `json:"timestamp"`
	ProbeVersion *string                               `json:"probe_version"`
	Results      map[string]map[string]json.RawMessage `json:"results"`
}

// Decode parses a Protocol v1 request body. It performs structural decoding
// only; call Validate for the measurement rules.
//
// Unknown top-level fields and unknown fields inside a measurement are ignored
// (Protocol v1 §29 and design doc §6.3). Unknown *targets* and *probe types* are
// preserved so Validate can reject them.
func Decode(body []byte) (*PushRequest, error) {
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()

	var raw rawRequest
	if err := dec.Decode(&raw); err != nil {
		return nil, newError(CodeInvalidJSON, "request body is not valid JSON")
	}
	// Reject trailing content after the first JSON value.
	if dec.More() {
		return nil, newError(CodeInvalidJSON, "request body contains trailing data")
	}

	req := &PushRequest{Results: make(Results)}
	if raw.Version != nil {
		req.Version = *raw.Version
	}
	if raw.Timestamp != nil {
		req.Timestamp = *raw.Timestamp
	}
	if raw.ProbeVersion != nil {
		req.ProbeVersion = *raw.ProbeVersion
	}

	for target, byType := range raw.Results {
		req.Results[target] = make(map[ProbeType]Measurement, len(byType))
		for typeName, body := range byType {
			pt := ProbeType(typeName)
			m, err := decodeMeasurement(pt, body)
			if err != nil {
				return nil, err
			}
			req.Results[target][pt] = m
		}
	}
	return req, nil
}

func decodeMeasurement(pt ProbeType, body json.RawMessage) (Measurement, error) {
	switch pt {
	case ProbeICMP:
		r, err := decodeICMP(body)
		return Measurement{ICMP: r}, err
	case ProbeDNS:
		r, err := decodeDNS(body)
		return Measurement{DNS: r}, err
	case ProbeHTTP:
		r, err := decodeHTTP(body)
		return Measurement{HTTP: r}, err
	default:
		// Unknown probe type: structural validation is not our job here.
		// Validate rejects it with invalid_probe_type.
		return Measurement{}, nil
	}
}

// jsonFields decodes into a generic map so we can record which keys were
// literally present. The wide-format API reports "field absent"; the probe
// sending an explicit null is indistinguishable to encoding/json. Protocol v1
// §8 shows explicit nulls, so presence is tracked only to keep the option of
// stricter handling open, not to reject anything.
func jsonFields(body json.RawMessage) (map[string]json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		return nil, newError(CodeInvalidJSON, "measurement is not a JSON object")
	}
	return fields, nil
}

func decodeICMP(body json.RawMessage) (*ICMPResult, error) {
	fields, err := jsonFields(body)
	if err != nil {
		return nil, err
	}
	r := &ICMPResult{present: make(map[string]bool, len(fields))}
	for k := range fields {
		r.present[k] = true
	}
	var wire struct {
		Success   *bool    `json:"success"`
		Sent      *int     `json:"sent"`
		Received  *int     `json:"received"`
		LossRatio *float64 `json:"loss_ratio"`
		MinRTTMS  *float64 `json:"min_rtt_ms"`
		AvgRTTMS  *float64 `json:"avg_rtt_ms"`
		MaxRTTMS  *float64 `json:"max_rtt_ms"`
		JitterMS  *float64 `json:"jitter_ms"`
	}
	if err := unmarshalStrict(body, &wire); err != nil {
		return nil, err
	}
	if wire.Success != nil {
		r.Success = *wire.Success
	}
	if wire.Sent != nil {
		r.Sent = *wire.Sent
	}
	if wire.Received != nil {
		r.Received = *wire.Received
	}
	if wire.LossRatio != nil {
		r.LossRatio = *wire.LossRatio
	}
	r.MinRTTMS, r.AvgRTTMS, r.MaxRTTMS, r.JitterMS = wire.MinRTTMS, wire.AvgRTTMS, wire.MaxRTTMS, wire.JitterMS
	return r, nil
}

func decodeDNS(body json.RawMessage) (*DNSResult, error) {
	fields, err := jsonFields(body)
	if err != nil {
		return nil, err
	}
	r := &DNSResult{present: make(map[string]bool, len(fields))}
	for k := range fields {
		r.present[k] = true
	}
	var wire struct {
		Success    *bool    `json:"success"`
		DurationMS *float64 `json:"duration_ms"`
	}
	if err := unmarshalStrict(body, &wire); err != nil {
		return nil, err
	}
	if wire.Success != nil {
		r.Success = *wire.Success
	}
	r.DurationMS = wire.DurationMS
	return r, nil
}

func decodeHTTP(body json.RawMessage) (*HTTPResult, error) {
	fields, err := jsonFields(body)
	if err != nil {
		return nil, err
	}
	r := &HTTPResult{present: make(map[string]bool, len(fields))}
	for k := range fields {
		r.present[k] = true
	}
	var wire struct {
		Success    *bool    `json:"success"`
		StatusCode *int     `json:"status_code"`
		DurationMS *float64 `json:"duration_ms"`
	}
	if err := unmarshalStrict(body, &wire); err != nil {
		return nil, err
	}
	if wire.Success != nil {
		r.Success = *wire.Success
	}
	r.StatusCode = wire.StatusCode
	r.DurationMS = wire.DurationMS
	return r, nil
}

// unmarshalStrict decodes JSON into v, converting any decode failure into a
// protocol invalid_json error. Unknown fields are ignored, per design doc §6.3.
func unmarshalStrict(body json.RawMessage, v any) error {
	if err := json.Unmarshal(body, v); err != nil {
		return newError(CodeInvalidJSON, "measurement contains a value of the wrong type")
	}
	return nil
}

// String renders results for debugging. Never logged for untrusted input.
func (r Results) String() string {
	return fmt.Sprintf("Results(%d targets)", len(r))
}
