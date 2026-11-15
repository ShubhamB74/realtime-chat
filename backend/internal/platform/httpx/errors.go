// Package httpx holds HTTP plumbing shared by all handlers: middleware, errors, server setup.
package httpx

import (
	"encoding/json"
	"net/http"
)

// ErrorBody is the shared error contract (see docs/LLD.md section 6).
// Messages must never include internals or message content.
type ErrorBody struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id,omitempty"`
	Retryable bool   `json:"retryable"`
}

// WriteError writes the standard JSON error response.
func WriteError(w http.ResponseWriter, r *http.Request, status int, code, message string, retryable bool) {
	WriteJSON(w, status, ErrorBody{
		Code:      code,
		Message:   message,
		RequestID: RequestIDFrom(r.Context()),
		Retryable: retryable,
	})
}

// WriteJSON writes v as JSON with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// NotFound is the catch-all handler returning the JSON error contract.
func NotFound(w http.ResponseWriter, r *http.Request) {
	WriteError(w, r, http.StatusNotFound, "not_found", "Resource not found", false)
}
