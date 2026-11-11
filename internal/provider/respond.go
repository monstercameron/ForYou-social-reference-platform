package provider

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/canon"
)

var (
	errBodyTooLarge = errors.New("provider: request body too large")
	errBadJSON      = errors.New("provider: invalid JSON")
)

func decodeJSON(r *http.Request, max int64, v any) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > max {
		return nil, errBodyTooLarge
	}
	if _, err = canon.Parse(b, canon.Limits{MaxBytes: int(max), MaxDepth: 32}); err != nil {
		return nil, errBadJSON
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err = d.Decode(v); err != nil {
		return nil, errBadJSON
	}
	if err = d.Decode(new(any)); err != io.EOF {
		return nil, errBadJSON
	}
	return b, nil
}
func writeError(w http.ResponseWriter, status int, name string) {
	publicStatus := apierr.Status(name)
	want, ok := publicStatus.HTTPStatus()
	if !ok || want != status || status == http.StatusTooManyRequests || status == http.StatusServiceUnavailable {
		publicStatus = apierr.Unavailable
		status = http.StatusServiceUnavailable
	}
	if status == http.StatusServiceUnavailable {
		writeRetry(w, status, string(publicStatus), apierr.DefaultRetryAfterSeconds)
		return
	}
	body, err := encodePublicError(apierr.PublicReadError{Status: publicStatus})
	if err != nil {
		status = http.StatusServiceUnavailable
		body, _ = encodePublicError(apierr.PublicReadError{Status: apierr.Unavailable, RetryAfterSeconds: apierr.DefaultRetryAfterSeconds})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
func writeRetry(w http.ResponseWriter, status int, name string, seconds int) {
	publicStatus := apierr.Status(name)
	want, ok := publicStatus.HTTPStatus()
	if !ok || want != status || status != http.StatusTooManyRequests && status != http.StatusServiceUnavailable {
		status = http.StatusServiceUnavailable
		publicStatus = apierr.Unavailable
	}
	if seconds < 1 {
		seconds = 1
	}
	if seconds > 86400 {
		seconds = 86400
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	body, err := encodePublicError(apierr.PublicReadError{RetryAfterSeconds: seconds, Status: publicStatus})
	if err != nil {
		status = http.StatusServiceUnavailable
		seconds = apierr.DefaultRetryAfterSeconds
		publicStatus = apierr.Unavailable
		body, _ = encodePublicError(apierr.PublicReadError{RetryAfterSeconds: seconds, Status: publicStatus})
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func encodePublicError(body apierr.PublicReadError) ([]byte, error) {
	if err := body.Validate(); err != nil {
		return nil, err
	}
	return canon.Encode(body)
}
func writeCanonical(w http.ResponseWriter, status int, body []byte, cacheControl string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	if cacheControl != "" {
		w.Header().Set("Cache-Control", cacheControl)
	}
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
func isLoopback(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
