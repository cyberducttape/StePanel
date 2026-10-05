package stepanel

import (
	"errors"
	"log"
	"net/http"
)

// PublicError separates a client-safe explanation from the internal cause.
// Handlers should expose Code/Message and retain Cause only in server logs.
type PublicError struct {
	Code    string
	Message string
	Cause   error
}

func (e *PublicError) Error() string {
	if e == nil {
		return ""
	}
	return e.Message
}

func (e *PublicError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func publicError(code, message string, cause error) *PublicError {
	return &PublicError{Code: code, Message: message, Cause: cause}
}

// writePublicError emits only the deliberately safe error fields. Internal
// causes are logged with the request ID and never selected by HTTP status.
func writePublicError(w http.ResponseWriter, r *http.Request, status int, err error) {
	requestID, _ := r.Context().Value(requestIDContextKey{}).(string)
	var public *PublicError
	if errors.As(err, &public) && public != nil {
		if public.Cause != nil {
			log.Printf("api error request_id=%q code=%q cause=%v", requestID, public.Code, public.Cause)
		}
		_, nextAction, retryable := apiErrorMetadata(status, r.URL.Path)
		writeJSON(w, status, struct {
			Error      string `json:"error"`
			ErrorCode  string `json:"error_code"`
			Code       int    `json:"code"`
			RequestID  string `json:"request_id,omitempty"`
			Resource   string `json:"resource,omitempty"`
			Retryable  bool   `json:"retryable"`
			NextAction string `json:"next_action"`
		}{public.Message, public.Code, status, requestID, r.URL.Path, retryable, nextAction})
		return
	}
	writeAPIError(w, r, status, safeServerErrorMessage(status))
}
