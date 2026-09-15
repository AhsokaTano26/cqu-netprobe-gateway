package protocol

import "math"

// lossRatioTolerance absorbs the rounding a probe performs when it serialises
// (sent-received)/sent as a short decimal. 1/3 becomes 0.333, which is 3.3e-4
// away; 1e-6 would be too tight, 1e-2 too loose to catch real mismatches.
const lossRatioTolerance = 1e-3

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

// NOTE (Task 4/Task 5 boundary): the two functions below are placeholders. Task
// 5 replaces them with the real Protocol v1 §10 and §11 implementations, which
// its brief appends to this file. They exist here only so validateMeasurement
// compiles and the package builds; they fail closed, so a DNS or HTTP
// measurement can never pass validation by omission in the meantime.
//
// Task 5: delete both stubs, then paste the real implementations in their place.

func validateDNS(m *DNSResult) error {
	return newError(CodeInvalidPayload, "dns measurement validation is not implemented yet")
}

func validateHTTP(m *HTTPResult) error {
	return newError(CodeInvalidPayload, "http measurement validation is not implemented yet")
}
