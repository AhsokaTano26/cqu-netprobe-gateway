package protocol

// Error codes defined by Protocol v1 §17.
const (
	CodeInvalidRequest     = "invalid_request"
	CodeInvalidJSON        = "invalid_json"
	CodeInvalidPayload     = "invalid_payload"
	CodeUnsupportedVersion = "unsupported_version"
	CodeInvalidTarget      = "invalid_target"
	CodeInvalidProbeType   = "invalid_probe_type"
	CodeUnauthorized       = "unauthorized"
	CodeProbeDisabled      = "probe_disabled"
	CodeRateLimited        = "rate_limited"
	CodeConfigStale        = "config_stale"
	CodeInternalError      = "internal_error"
	CodeServiceUnavailable = "service_unavailable"
)

// Error is a protocol-level rejection. Code is one of the constants above;
// Message is a fixed human-readable string that never contains probe-supplied
// data, tokens, or internal detail.
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func newError(code, message string) *Error { return &Error{Code: code, Message: message} }
