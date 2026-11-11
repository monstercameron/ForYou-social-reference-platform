package apierr

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

const maxErrorBodyBytes = 4096

// Write writes a validated public error response, replacing invalid bodies with unavailable.
func Write(w http.ResponseWriter, body PublicReadError) {
	code, ok := body.Status.HTTPStatus()
	if err := body.Validate(); err != nil || !ok {
		body = PublicReadError{Status: Unavailable, RetryAfterSeconds: DefaultRetryAfterSeconds}
		code, _ = body.Status.HTTPStatus()
	}
	encoded, _ := json.Marshal(body)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Del("Retry-After")
	if code == http.StatusTooManyRequests || code == http.StatusServiceUnavailable {
		w.Header().Set("Retry-After", strconv.Itoa(body.RetryAfterSeconds))
	}
	w.WriteHeader(code)
	_, _ = w.Write(encoded)
}

// WriteError writes the public Error in err, or unavailable when no public Error exists.
func WriteError(w http.ResponseWriter, err error) {
	var publicError *Error
	if !errors.As(err, &publicError) || publicError == nil {
		Write(w, PublicReadError{Status: Unavailable, RetryAfterSeconds: DefaultRetryAfterSeconds})
		return
	}
	Write(w, PublicReadError{Status: publicError.Status, RetryAfterSeconds: publicError.RetryAfterSeconds})
}

// Parse validates a bounded PublicReadError JSON body.
func Parse(body []byte) (PublicReadError, error) {
	if len(body) > maxErrorBodyBytes {
		return PublicReadError{}, errors.New("public error body exceeds 4096 bytes")
	}
	var encoded struct {
		RetryAfterSeconds json.RawMessage `json:"retry_after_seconds"`
		Status            *Status         `json:"status"`
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	decoder.UseNumber()
	if err := decoder.Decode(&encoded); err != nil {
		return PublicReadError{}, fmt.Errorf("decode public error: %w", err)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		if err == nil {
			return PublicReadError{}, errors.New("trailing data after public error")
		}
		return PublicReadError{}, fmt.Errorf("read public error trailer: %w", err)
	}
	if encoded.Status == nil {
		return PublicReadError{}, errors.New("public error status is required")
	}
	result := PublicReadError{Status: *encoded.Status}
	if encoded.RetryAfterSeconds != nil {
		var value any
		numberDecoder := json.NewDecoder(bytes.NewReader(encoded.RetryAfterSeconds))
		numberDecoder.UseNumber()
		if err := numberDecoder.Decode(&value); err != nil {
			return PublicReadError{}, fmt.Errorf("decode retry_after_seconds: %w", err)
		}
		number, ok := value.(json.Number)
		if !ok {
			return PublicReadError{}, errors.New("retry_after_seconds must be an integer")
		}
		seconds, err := strconv.Atoi(number.String())
		if err != nil {
			return PublicReadError{}, fmt.Errorf("retry_after_seconds must be an integer: %w", err)
		}
		result.RetryAfterSeconds = seconds
	}
	if err := result.Validate(); err != nil {
		return PublicReadError{}, err
	}
	return result, nil
}
