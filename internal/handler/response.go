package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
)

// Stable error codes clients can switch on.
const (
	CodeInvalidRequest     = "invalid_request"
	CodeInvalidCredentials = "invalid_credentials"
	CodeUnauthorized       = "unauthorized"
	CodeRateLimited        = "rate_limited"
	CodeUnavailable        = "service_unavailable"
	CodeInternal           = "internal_error"
)

const maxJSONBody = 64 << 10 // 64 KiB is far more than any auth request needs

type envelope struct {
	Success bool       `json:"success"`
	Data    any        `json:"data,omitempty"`
	Error   *errorBody `json:"error,omitempty"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Field   string `json:"field,omitempty"`
}

func writeData(w http.ResponseWriter, code int, data any) {
	writeJSON(w, code, envelope{Success: true, Data: data})
}

func writeError(w http.ResponseWriter, status int, code, message, field string) {
	writeJSON(w, status, envelope{Error: &errorBody{Code: code, Message: message, Field: field}})
}

// decodeJSON reads exactly one JSON object with no unknown fields.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(ct), "application/json") {
		return errors.New("Content-Type must be application/json")
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxJSONBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var tooBig *http.MaxBytesError
		var unknown *json.UnmarshalTypeError
		switch {
		case errors.As(err, &tooBig):
			return errors.New("request body is too large")
		case errors.As(err, &unknown):
			return fmt.Errorf("field %q has the wrong type", unknown.Field)
		case strings.HasPrefix(err.Error(), "json: unknown field"):
			return errors.New(strings.TrimPrefix(err.Error(), "json: "))
		default:
			return errors.New("request body must be a valid JSON object")
		}
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return errors.New("request body must contain a single JSON object")
	}
	return nil
}

// clientIP returns the TCP peer address. X-Forwarded-For is deliberately
// ignored: it is client-controlled and would let anyone dodge per-IP rate
// limits. Trusting it must wait until the real proxy setup is known.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
