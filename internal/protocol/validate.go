package protocol

import (
	"math"
	"sort"
)

// lossRatioTolerance absorbs the rounding a probe performs when it serialises
// (sent-received)/sent as a short decimal. 1/3 becomes 0.333, which is 3.3e-4
// away; 1e-6 would be too tight, 1e-2 too loose to catch real mismatches.
const lossRatioTolerance = 1e-3

// Validate applies every request-level rule in Protocol v1. It is the single
// entry point the push handler calls after Decode succeeded.
//
// Targets are visited in sorted order so that a request with several problems
// always reports the same first error, keeping logs correlatable.
func (r *PushRequest) Validate(al Allowlist) error {
	if r.Version != Version {
		return newError(CodeUnsupportedVersion, "unsupported protocol version")
	}
	if r.Timestamp < 0 {
		return newError(CodeInvalidPayload, "timestamp must not be negative")
	}
	if len(r.ProbeVersion) > MaxProbeVersionLen {
		return newError(CodeInvalidPayload, "probe_version is too long")
	}
	if len(r.Results) == 0 {
		// Protocol v1 §23 only refreshes last_seen on a fully valid push. An
		// empty result set is vacuously valid, which would let a broken probe
		// stay "online" forever while measuring nothing.
		return newError(CodeInvalidPayload, "results must contain at least one measurement")
	}

	targets := make([]string, 0, len(r.Results))
	for target := range r.Results {
		targets = append(targets, target)
	}
	sort.Strings(targets)

	total := 0
	for _, target := range targets {
		allowed, known := al[target]
		if !known {
			return newError(CodeInvalidTarget, "unknown target")
		}
		byType := r.Results[target]
		if len(byType) == 0 {
			return newError(CodeInvalidPayload, "target has no measurements")
		}

		types := make([]string, 0, len(byType))
		for pt := range byType {
			types = append(types, string(pt))
		}
		sort.Strings(types)

		for _, typeName := range types {
			pt := ProbeType(typeName)
			switch pt {
			case ProbeICMP, ProbeDNS, ProbeHTTP:
			default:
				return newError(CodeInvalidProbeType, "unsupported probe type")
			}
			if !allowed[pt] {
				return newError(CodeInvalidProbeType, "probe type is not allowed for this target")
			}
			if err := validateMeasurement(pt, byType[pt]); err != nil {
				return err
			}
			total++
		}
	}
	if total == 0 {
		return newError(CodeInvalidPayload, "results must contain at least one measurement")
	}
	return nil
}

// validateMeasurement dispatches to the validator for the measurement's type.
// A measurement that carries no payload for its declared type is rejected: the
// decoder leaves every field nil when the JSON object did not match the shape.
func validateMeasurement(pt ProbeType, m Measurement) error {
	switch pt {
	case ProbeICMP:
		if m.ICMP == nil {
			return newError(CodeInvalidPayload, "icmp measurement is missing or malformed")
		}
		return validateICMP(m.ICMP)
	case ProbeDNS:
		if m.DNS == nil {
			return newError(CodeInvalidPayload, "dns measurement is missing or malformed")
		}
		return validateDNS(m.DNS)
	case ProbeHTTP:
		if m.HTTP == nil {
			return newError(CodeInvalidPayload, "http measurement is missing or malformed")
		}
		return validateHTTP(m.HTTP)
	default:
		return newError(CodeInvalidProbeType, "unsupported probe type")
	}
}

// validateICMP enforces every constraint in Protocol v1 §7, §8 and §9.
func validateICMP(m *ICMPResult) error {
	if m.Sent <= 0 {
		return newError(CodeInvalidPayload, "icmp sent must be greater than zero")
	}
	if m.Received < 0 || m.Received > m.Sent {
		return newError(CodeInvalidPayload, "icmp received must be between zero and sent")
	}
	if !finite(m.LossRatio) || m.LossRatio < 0 || m.LossRatio > 1 {
		return newError(CodeInvalidPayload, "icmp loss_ratio must be between zero and one")
	}

	expected := float64(m.Sent-m.Received) / float64(m.Sent)
	if math.Abs(m.LossRatio-expected) > lossRatioTolerance {
		return newError(CodeInvalidPayload, "icmp loss_ratio does not match sent and received")
	}

	// Protocol v1 §7 defines loss_ratio 1.0 as "all packets lost", so it cannot
	// co-exist with a successful reply. The absolute tolerance above would
	// otherwise admit loss_ratio 1.0 for large sent counts.
	if math.Abs(m.LossRatio-1) <= 1e-6 && m.Received != 0 {
		return newError(CodeInvalidPayload, "icmp loss_ratio of 1 requires no replies received")
	}

	if m.Received == 0 {
		// Protocol v1 §8: total loss carries no RTT at all, and success is false.
		if m.Success {
			return newError(CodeInvalidPayload, "icmp success must be false when no reply was received")
		}
		if m.MinRTTMS != nil || m.AvgRTTMS != nil || m.MaxRTTMS != nil || m.JitterMS != nil {
			return newError(CodeInvalidPayload, "icmp rtt fields must be null when no reply was received")
		}
		return nil
	}

	// Protocol v1 §7: any reply at all means success.
	if !m.Success {
		return newError(CodeInvalidPayload, "icmp success must be true when a reply was received")
	}
	if m.MinRTTMS == nil || m.AvgRTTMS == nil || m.MaxRTTMS == nil {
		return newError(CodeInvalidPayload, "icmp rtt fields are required when a reply was received")
	}

	min, avg, max := *m.MinRTTMS, *m.AvgRTTMS, *m.MaxRTTMS
	if !finite(min) || !finite(avg) || !finite(max) {
		return newError(CodeInvalidPayload, "icmp rtt must be a finite number")
	}
	if min < 0 || avg < 0 || max < 0 {
		return newError(CodeInvalidPayload, "icmp rtt must not be negative")
	}
	if min > avg || avg > max {
		return newError(CodeInvalidPayload, "icmp rtt must satisfy min <= avg <= max")
	}

	if m.JitterMS != nil {
		j := *m.JitterMS
		if !finite(j) {
			return newError(CodeInvalidPayload, "icmp jitter must be a finite number")
		}
		if j < 0 {
			return newError(CodeInvalidPayload, "icmp jitter must not be negative")
		}
	}
	// Protocol v1 §9: jitter may be null when fewer than two replies arrived.
	// It is not *required* to be null, so a probe that computes 0 is accepted.
	return nil
}

// finite reports whether v is a real number, rejecting NaN and both infinities.
// JSON literals cannot express NaN or Infinity, but 1e400 overflows float64 to
// +Inf, so this check is load-bearing rather than defensive.
func finite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

// validateDNS enforces Protocol v1 §10.
func validateDNS(m *DNSResult) error {
	if m.Success {
		if m.DurationMS == nil {
			return newError(CodeInvalidPayload, "dns duration is required on success")
		}
		if !finite(*m.DurationMS) {
			return newError(CodeInvalidPayload, "dns duration must be a finite number")
		}
		if *m.DurationMS < 0 {
			return newError(CodeInvalidPayload, "dns duration must not be negative")
		}
		return nil
	}
	// Protocol v1 §10: failure carries no duration at all, and v1 never
	// transports a DNS error string.
	if m.DurationMS != nil {
		return newError(CodeInvalidPayload, "dns duration must be null on failure")
	}
	return nil
}

// validateHTTP enforces Protocol v1 §11.
func validateHTTP(m *HTTPResult) error {
	// No HTTP response at all: both fields must be null.
	if m.StatusCode == nil && m.DurationMS == nil {
		if m.Success {
			return newError(CodeInvalidPayload, "http success cannot be true without a response")
		}
		return nil
	}

	// A response arrived, so both fields are required.
	if m.StatusCode == nil || m.DurationMS == nil {
		return newError(CodeInvalidPayload, "http status_code and duration_ms must both be set or both be null")
	}
	code := *m.StatusCode
	if code < 100 || code > 599 {
		return newError(CodeInvalidPayload, "http status_code must be between 100 and 599")
	}
	if !finite(*m.DurationMS) {
		return newError(CodeInvalidPayload, "http duration must be a finite number")
	}
	if *m.DurationMS < 0 {
		return newError(CodeInvalidPayload, "http duration must not be negative")
	}

	// Protocol v1 §11 fixes the mapping: 200 <= code < 400 means success.
	wantSuccess := code >= 200 && code < 400
	if m.Success != wantSuccess {
		return newError(CodeInvalidPayload, "http success does not match status_code")
	}
	return nil
}
