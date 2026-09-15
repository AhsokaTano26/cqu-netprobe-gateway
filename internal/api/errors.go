// Package api implements the Protocol v1 push endpoint.
package api

import (
	"encoding/json"
	"net/http"

	"github.com/tano/cqu-netprobe-gateway/internal/protocol"
)

// MaxBodyBytes is the Protocol v1 §20 request body limit: 64 KiB exactly.
const MaxBodyBytes = 65536

// Fixed messages. Nothing derived from the request, the database, or an error
// value is ever interpolated into a response body, so a response cannot leak a
// token, SQL, a stack trace, or a filesystem path.
const (
	msgBadRequest   = "request is not valid"
	msgInvalidJSON  = "request body is not valid JSON"
	msgPayload      = "invalid measurement payload"
	msgVersion      = "unsupported protocol version"
	msgTarget       = "unknown target"
	msgProbeType    = "probe type is not allowed for this target"
	msgUnauthorized = "missing or invalid credentials"
	msgDisabled     = "probe is disabled"
	msgRateLimited  = "too many requests"
	msgTooLarge     = "request body too large"
	msgMediaType    = "unsupported content type"
	msgMethod       = "method not allowed"
	errInternal     = "internal error"
	msgUnavailable  = "service temporarily unavailable"
)

// errorBody is the Protocol v1 §15 error envelope.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeError emits an error envelope. The message must always be one of the
// constants above.
func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(errorBody{Error: errorDetail{Code: code, Message: message}})
}

// statusForCode maps a protocol error code to its Protocol v1 §16 HTTP status.
func statusForCode(code string) int {
	switch code {
	case protocol.CodeUnsupportedVersion,
		protocol.CodeInvalidJSON,
		protocol.CodeInvalidPayload,
		protocol.CodeInvalidTarget,
		protocol.CodeInvalidProbeType,
		protocol.CodeInvalidRequest:
		return http.StatusBadRequest
	case protocol.CodeUnauthorized:
		return http.StatusUnauthorized
	case protocol.CodeProbeDisabled:
		return http.StatusForbidden
	case protocol.CodeRateLimited:
		return http.StatusTooManyRequests
	case protocol.CodeServiceUnavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// messageForCode returns the fixed message for a code.
func messageForCode(code string) string {
	switch code {
	case protocol.CodeInvalidJSON:
		return msgInvalidJSON
	case protocol.CodeUnsupportedVersion:
		return msgVersion
	case protocol.CodeInvalidTarget:
		return msgTarget
	case protocol.CodeInvalidProbeType:
		return msgProbeType
	case protocol.CodeUnauthorized:
		return msgUnauthorized
	case protocol.CodeProbeDisabled:
		return msgDisabled
	case protocol.CodeRateLimited:
		return msgRateLimited
	case protocol.CodeInternalError:
		return errInternal
	case protocol.CodeServiceUnavailable:
		return msgUnavailable
	case protocol.CodeInvalidPayload:
		return msgPayload
	default:
		return msgBadRequest
	}
}

// writeProtocolError renders a *protocol.Error using its status and message.
func writeProtocolError(w http.ResponseWriter, err *protocol.Error) {
	writeError(w, statusForCode(err.Code), err.Code, messageForCode(err.Code))
}
