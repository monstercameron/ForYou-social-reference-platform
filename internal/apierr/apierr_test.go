package apierr

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPStatus(t *testing.T) {
	check := func(t *testing.T, status Status, want int, wantOK bool) {
		t.Helper()
		got, ok := status.HTTPStatus()
		if got != want || ok != wantOK {
			t.Errorf("HTTPStatus(%q) = (%d, %t); want (%d, %t)", status, got, ok, want, wantOK)
		}
	}
	t.Run("400 invalid cursor", func(t *testing.T) { check(t, InvalidCursor, 400, true) })
	t.Run("401 unauthenticated", func(t *testing.T) { check(t, Unauthenticated, 401, true) })
	t.Run("402 insufficient balance", func(t *testing.T) { check(t, InsufficientBalance, 402, true) })
	t.Run("403 delegation revoked", func(t *testing.T) { check(t, DelegationRevoked, 403, true) })
	t.Run("404 not found", func(t *testing.T) { check(t, NotFound, 404, true) })
	t.Run("409 quote conflict", func(t *testing.T) { check(t, QuoteConflict, 409, true) })
	t.Run("410 retention expired", func(t *testing.T) { check(t, RetentionExpired, 410, true) })
	t.Run("416 range not satisfiable", func(t *testing.T) { check(t, RangeNotSatisfiable, 416, true) })
	t.Run("422 message too large", func(t *testing.T) { check(t, MessageTooLarge, 422, true) })
	t.Run("429 rate limited", func(t *testing.T) { check(t, RateLimited, 429, true) })
	t.Run("503 unavailable", func(t *testing.T) { check(t, Unavailable, 503, true) })
	t.Run("too large has no catalog code", func(t *testing.T) { check(t, TooLarge, 0, false) })
}

func TestWrite(t *testing.T) {
	t.Run("not found", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		Write(recorder, PublicReadError{Status: NotFound})
		if recorder.Code != http.StatusNotFound {
			t.Errorf("status = %d; want 404", recorder.Code)
		}
		if got := recorder.Header().Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q; want application/json", got)
		}
		if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q; want no-store", got)
		}
		if got := recorder.Body.String(); got != `{"status":"not_found"}` {
			t.Errorf("body = %q; want %q", got, `{"status":"not_found"}`)
		}
	})
	t.Run("rate limited with retry delay", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		Write(recorder, PublicReadError{Status: RateLimited, RetryAfterSeconds: 30})
		if recorder.Code != http.StatusTooManyRequests {
			t.Errorf("status = %d; want 429", recorder.Code)
		}
		if got := recorder.Header().Get("Retry-After"); got != "30" {
			t.Errorf("Retry-After = %q; want 30", got)
		}
		if got := recorder.Body.String(); got != `{"retry_after_seconds":30,"status":"rate_limited"}` {
			t.Errorf("body = %q; want %q", got, `{"retry_after_seconds":30,"status":"rate_limited"}`)
		}
	})
	checkFallback := func(t *testing.T, body PublicReadError) {
		t.Helper()
		recorder := httptest.NewRecorder()
		Write(recorder, body)
		if recorder.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d; want 503", recorder.Code)
		}
		if got := recorder.Header().Get("Retry-After"); got != "30" {
			t.Errorf("Retry-After = %q; want 30", got)
		}
		if got := recorder.Body.String(); got != `{"retry_after_seconds":30,"status":"unavailable"}` {
			t.Errorf("body = %q; want fallback body", got)
		}
	}
	t.Run("rate limited without retry delay", func(t *testing.T) {
		checkFallback(t, PublicReadError{Status: RateLimited})
	})
	t.Run("status without HTTP mapping", func(t *testing.T) {
		checkFallback(t, PublicReadError{Status: TooLarge})
	})
}

func TestWriteError(t *testing.T) {
	t.Run("wrapped public error omits cause", func(t *testing.T) {
		cause := errors.New("private message contents")
		err := fmt.Errorf("context: %w", New(IDMismatch, cause))
		recorder := httptest.NewRecorder()
		WriteError(recorder, err)
		if recorder.Code != http.StatusUnprocessableEntity {
			t.Errorf("status = %d; want 422", recorder.Code)
		}
		if got := recorder.Body.String(); got != `{"status":"id_mismatch"}` {
			t.Errorf("body = %q; want %q", got, `{"status":"id_mismatch"}`)
		}
		if strings.Contains(recorder.Body.String(), cause.Error()) {
			t.Errorf("response body exposed private cause: %q", recorder.Body.String())
		}
	})
	t.Run("plain error becomes unavailable", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		WriteError(recorder, errors.New("internal failure"))
		if recorder.Code != http.StatusServiceUnavailable {
			t.Errorf("status = %d; want 503", recorder.Code)
		}
		if got := recorder.Body.String(); got != `{"retry_after_seconds":30,"status":"unavailable"}` {
			t.Errorf("body = %q; want fallback body", got)
		}
	})
}

func TestParse(t *testing.T) {
	checkAccepted := func(t *testing.T, body string, want PublicReadError) {
		t.Helper()
		got, err := Parse([]byte(body))
		if err != nil {
			t.Fatalf("Parse(%s) error = %v", body, err)
		}
		if got != want {
			t.Errorf("Parse(%s) = %#v; want %#v", body, got, want)
		}
	}
	checkRejected := func(t *testing.T, body []byte) {
		t.Helper()
		if _, err := Parse(body); err == nil {
			t.Errorf("Parse(%s) succeeded; want error", body)
		}
	}
	t.Run("accepts not found", func(t *testing.T) {
		checkAccepted(t, `{"status":"not_found"}`, PublicReadError{Status: NotFound})
	})
	t.Run("rejects unknown member", func(t *testing.T) {
		checkRejected(t, []byte(`{"status":"not_found","detail":"x"}`))
	})
	t.Run("rejects unknown status", func(t *testing.T) {
		checkRejected(t, []byte(`{"status":"teapot"}`))
	})
	t.Run("rejects zero retry delay", func(t *testing.T) {
		checkRejected(t, []byte(`{"retry_after_seconds":0,"status":"unavailable"}`))
	})
	t.Run("rejects explicit zero retry delay on not found", func(t *testing.T) {
		checkRejected(t, []byte(`{"retry_after_seconds":0,"status":"not_found"}`))
	})
	t.Run("rejects explicit zero retry delay on retention expired", func(t *testing.T) {
		checkRejected(t, []byte(`{"retry_after_seconds":0,"status":"retention_expired"}`))
	})
	t.Run("rejects retry delay above maximum", func(t *testing.T) {
		checkRejected(t, []byte(`{"retry_after_seconds":86401,"status":"unavailable"}`))
	})
	t.Run("rejects fractional retry delay", func(t *testing.T) {
		checkRejected(t, []byte(`{"retry_after_seconds":1.5,"status":"not_found"}`))
	})
	t.Run("rejects trailing JSON value", func(t *testing.T) {
		checkRejected(t, []byte(`{"status":"not_found"} {}`))
	})
	t.Run("rejects oversized body", func(t *testing.T) {
		checkRejected(t, []byte(`{"status":"not_found"}`+strings.Repeat(" ", maxErrorBodyBytes)))
	})
	t.Run("accepts body at size limit", func(t *testing.T) {
		body := `{"status":"not_found"}` + strings.Repeat(" ", maxErrorBodyBytes-len(`{"status":"not_found"}`))
		checkAccepted(t, body, PublicReadError{Status: NotFound})
	})
}

func TestStatusOf(t *testing.T) {
	t.Run("finds wrapped error", func(t *testing.T) {
		got, ok := StatusOf(fmt.Errorf("context: %w", New(IDMismatch, nil)))
		if got != IDMismatch || !ok {
			t.Errorf("StatusOf() = (%q, %t); want (%q, true)", got, ok, IDMismatch)
		}
	})
	t.Run("reports no public error", func(t *testing.T) {
		if got, ok := StatusOf(errors.New("plain")); got != "" || ok {
			t.Errorf("StatusOf() = (%q, %t); want empty status and false", got, ok)
		}
	})
}
