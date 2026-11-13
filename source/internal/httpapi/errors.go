package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"p2pfs/internal/state"
)

// ErrorBody is the one error shape every endpoint uses, so clients write
// one error parser instead of one per endpoint.
type ErrorBody struct {
	Error ErrorDetail `json:"error"`
}

// ErrorDetail carries a stable machine code plus a human message.
type ErrorDetail struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"requestId,omitempty"`
}

// apiError is an error that already knows its HTTP status and code.
type apiError struct {
	status int
	code   string
	msg    string
}

func (e *apiError) Error() string { return e.msg }

func newAPIError(status int, code, format string, args ...any) *apiError {
	return &apiError{status: status, code: code, msg: fmt.Sprintf(format, args...)}
}

// writeError maps any error to the consistent JSON error body.
//   - apiError: used as-is (decode problems, 404s, 415s, ...)
//   - state.ErrInvalid  -> 422 Unprocessable Content (well-formed but wrong)
//   - state.ErrNotFound -> 404
//   - state.ErrConflict -> 409
//   - anything else     -> 500 with a generic message (never leak internals)
func writeError(w http.ResponseWriter, r *http.Request, err error) {
	var ae *apiError
	switch {
	case errors.As(err, &ae):
	case errors.Is(err, state.ErrInvalid):
		ae = newAPIError(http.StatusUnprocessableEntity, "invalid_value", "%s", err.Error())
	case errors.Is(err, state.ErrNotFound):
		ae = newAPIError(http.StatusNotFound, "not_found", "%s", err.Error())
	case errors.Is(err, state.ErrConflict):
		ae = newAPIError(http.StatusConflict, "conflict", "%s", err.Error())
	default:
		loggerFrom(r).Error("internal error", "err", err)
		ae = newAPIError(http.StatusInternalServerError, "internal", "internal server error")
	}
	writeJSON(w, ae.status, ErrorBody{Error: ErrorDetail{Code: ae.code, Message: ae.msg, RequestID: requestIDFrom(r)}})
}

// decodeJSON enforces the request-body contract:
// correct media type, bounded size, no unknown fields, exactly one JSON value.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64) error {
	ct := r.Header.Get("Content-Type")
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil || mt != "application/json" {
		return newAPIError(http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json")
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		var syn *json.SyntaxError
		var typ *json.UnmarshalTypeError
		var tooBig *http.MaxBytesError
		switch {
		case errors.As(err, &tooBig):
			return newAPIError(http.StatusRequestEntityTooLarge, "body_too_large", "request body exceeds %d bytes", maxBytes)
		case errors.Is(err, io.EOF):
			return newAPIError(http.StatusBadRequest, "empty_body", "request body is empty")
		case errors.As(err, &syn), errors.Is(err, io.ErrUnexpectedEOF):
			return newAPIError(http.StatusBadRequest, "invalid_json", "malformed JSON")
		case errors.As(err, &typ):
			return newAPIError(http.StatusBadRequest, "invalid_type", "field %q has the wrong type", typ.Field)
		case strings.HasPrefix(err.Error(), "json: unknown field "):
			// encoding/json has no typed error for this; the prefix is stable.
			return newAPIError(http.StatusBadRequest, "unknown_field", "%s", strings.TrimPrefix(err.Error(), "json: "))
		default:
			return newAPIError(http.StatusBadRequest, "invalid_json", "could not decode body")
		}
	}
	// Reject `{"a":1}{"b":2}` and trailing garbage.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return newAPIError(http.StatusBadRequest, "invalid_json", "body must contain a single JSON object")
	}
	return nil
}
