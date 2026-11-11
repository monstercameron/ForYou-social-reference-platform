package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/discovery"
	"foryou.local/foryou/internal/retention"
	"foryou.local/foryou/internal/wire"
)

func TestRetentionRoutes(t *testing.T) {
	t.Run("get returns the signed initial declaration as canonical no-store JSON", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		s := retentionService(t, func() time.Time { return now }, true)
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/retention", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("GET /v1/retention status = %d; want 200", response.Code)
		}
		declarations := s.Declarations()
		if len(declarations) != 1 {
			t.Fatalf("declaration count = %d; want 1", len(declarations))
		}
		declaration := declarations[0]
		if err := retention.Verify(declaration); err != nil {
			t.Fatalf("initial declaration verification failed: %v", err)
		}
		if declaration.RetentionSeconds != 31536000 {
			t.Errorf("retention_seconds = %d; want 31536000", declaration.RetentionSeconds)
		}
		want, err := canon.Encode(wire.RetentionDeclarationsPage{Declarations: []wire.RetentionDeclaration{declaration}})
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(response.Body.Bytes(), want) {
			t.Errorf("body = %s; want JCS %s", response.Body.Bytes(), want)
		}
		if got := response.Header().Get("Content-Type"); got != "application/json" {
			t.Errorf("Content-Type = %q; want application/json", got)
		}
		if got := response.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q; want no-store", got)
		}
	})

	t.Run("dev declaration is signed and becomes newest", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		s := retentionService(t, func() time.Time { return now }, true)
		request := httptest.NewRequest(http.MethodPost, "/dev/retention", strings.NewReader(`{"retention_seconds":315360000,"effective_at":"2026-11-12T00:00:00Z"}`))
		request.RemoteAddr = "127.0.0.1:12345"
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusCreated {
			t.Fatalf("POST /dev/retention status = %d; want 201: %s", response.Code, response.Body)
		}
		var declaration wire.RetentionDeclaration
		if err := json.Unmarshal(response.Body.Bytes(), &declaration); err != nil {
			t.Fatalf("decode declaration: %v", err)
		}
		if err := retention.Verify(declaration); err != nil {
			t.Fatalf("returned declaration verification failed: %v", err)
		}
		response = httptest.NewRecorder()
		s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/retention", nil))
		if response.Code != http.StatusOK {
			t.Fatalf("GET /v1/retention after declaration status = %d; want 200", response.Code)
		}
		var page wire.RetentionDeclarationsPage
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatalf("decode retention page: %v", err)
		}
		if len(page.Declarations) != 2 || page.Declarations[0].DeclarationID != declaration.DeclarationID {
			t.Fatalf("GET declarations = %#v; want new declaration first", page.Declarations)
		}
	})

	t.Run("backdated declaration is invalid", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		s := retentionService(t, func() time.Time { return now }, true)
		request := httptest.NewRequest(http.MethodPost, "/dev/retention", strings.NewReader(`{"retention_seconds":315360000,"effective_at":"2026-11-10T00:00:00Z"}`))
		request.RemoteAddr = "127.0.0.1:12345"
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"status":"invalid_query"`) {
			t.Fatalf("backdated response = %d %s; want 400 invalid_query", response.Code, response.Body)
		}
	})

	t.Run("retention below one year is invalid", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		s := retentionService(t, func() time.Time { return now }, true)
		request := httptest.NewRequest(http.MethodPost, "/dev/retention", strings.NewReader(`{"retention_seconds":31535999}`))
		request.RemoteAddr = "127.0.0.1:12345"
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"status":"invalid_query"`) {
			t.Fatalf("short declaration response = %d %s; want 400 invalid_query", response.Code, response.Body)
		}
	})

	t.Run("unknown fields are rejected", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		s := retentionService(t, func() time.Time { return now }, true)
		request := httptest.NewRequest(http.MethodPost, "/dev/retention", strings.NewReader(`{"retention_seconds":31536000,"extra":true}`))
		request.RemoteAddr = "127.0.0.1:12345"
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest {
			t.Fatalf("unknown field response status = %d; want 400", response.Code)
		}
	})

	t.Run("non-loopback peer is hidden", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		s := retentionService(t, func() time.Time { return now }, true)
		request := httptest.NewRequest(http.MethodPost, "/dev/retention", strings.NewReader(`{"retention_seconds":31536000}`))
		request.RemoteAddr = "192.0.2.10:12345"
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusNotFound || !strings.Contains(response.Body.String(), `"status":"not_found"`) {
			t.Fatalf("non-loopback response = %d %s; want 404 not_found", response.Code, response.Body)
		}
	})

	t.Run("route is absent outside dev mode", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		s := retentionService(t, func() time.Time { return now }, false)
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodPost, "/dev/retention", nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("non-dev route status = %d; want 404", response.Code)
		}
	})
}

func TestRetentionExpiry(t *testing.T) {
	t.Run("record expires at retain_until and unclaimed records survive", func(t *testing.T) {
		now := time.Date(2027, 11, 10, 23, 59, 59, 0, time.UTC)
		s := retentionService(t, func() time.Time { return now }, true)
		retainedID := "fy1:4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f"
		unclaimedID := "fy1:49111e1567a90f12f8c56575cb7f4f42a49edd083ddfcaeb0f97af24bc198e72"
		retentionAcceptExample(t, s, retainedID, 0, now)
		retentionAcceptExample(t, s, unclaimedID, 1, now)
		retainUntil := time.Date(2027, 11, 11, 0, 0, 0, 0, time.UTC)
		if err := s.Records().SetRetainUntil(retainedID, retainUntil); err != nil {
			t.Fatal(err)
		}
		if err := s.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		data, state, err := s.Records().Lookup(retainedID)
		if err != nil || state != RecordRetained || len(data) == 0 {
			t.Fatalf("lookup before deadline = state %q, bytes %d, err %v", state, len(data), err)
		}

		now = retainUntil
		maxSequence := s.Records().MaxSequence()
		if err := s.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		data, state, err = s.Records().Lookup(retainedID)
		if err != nil || state != RecordExpired || len(data) != 0 {
			t.Fatalf("expired lookup = state %q, bytes %d, err %v", state, len(data), err)
		}
		if records := s.Records().Select(discovery.Query{Route: discovery.Messages}); len(records) != 1 || records[0].MessageID != unclaimedID {
			t.Fatalf("message listing after expiry = %#v; want only unclaimed record", records)
		}
		if records := s.Records().After(0, 100); len(records) != 1 || records[0].MessageID != unclaimedID {
			t.Fatalf("changes after expiry = %#v; want only unclaimed record", records)
		}
		if got := s.Records().MaxSequence(); got != maxSequence {
			t.Errorf("MaxSequence() after expiry = %d; want %d", got, maxSequence)
		}
		visible, _, err := s.Visibility().Visible(retainedID)
		if err != nil || visible {
			t.Errorf("expired record visible = %t, err = %v; want false", visible, err)
		}
		data, state, err = s.Records().Lookup(unclaimedID)
		if err != nil || state != RecordRetained || len(data) == 0 {
			t.Errorf("unclaimed lookup = state %q, bytes %d, err %v; want retained bytes", state, len(data), err)
		}
	})
}

func TestRetentionExpiryRejectsUncertainClock(t *testing.T) {
	now := time.Date(2027, 11, 11, 0, 10, 0, 0, time.UTC)
	s := retentionService(t, func() time.Time { return now }, true)
	messageID := "fy1:4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f"
	retentionAcceptExample(t, s, messageID, 0, now)
	if err := s.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	now = time.Date(2027, 11, 11, 0, 5, 0, 0, time.UTC)
	if err := s.Records().SetRetainUntil(messageID, time.Date(2027, 11, 11, 0, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	if err := s.RunOnce(context.Background()); !errors.Is(err, ErrClockUncertain) {
		t.Fatalf("RunOnce with rolled-back clock error = %v; want ErrClockUncertain", err)
	}
	data, state, err := s.Records().Lookup(messageID)
	if err != nil || state != RecordRetained || len(data) == 0 {
		t.Fatalf("lookup after uncertain run = state %q, bytes %d, err %v; want retained bytes", state, len(data), err)
	}

	now = time.Date(2027, 11, 11, 0, 10, 0, 0, time.UTC)
	if err := s.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, state, err = s.Records().Lookup(messageID)
	if err != nil || state != RecordExpired || len(data) != 0 {
		t.Fatalf("lookup after clock recovery = state %q, bytes %d, err %v; want expired without bytes", state, len(data), err)
	}
}

func TestRetentionExpirySurvivesRestart(t *testing.T) {
	now := time.Date(2027, 11, 10, 23, 59, 59, 0, time.UTC)
	cfg, err := TestConfig(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = t.TempDir()
	messageID := "fy1:4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f"
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	retentionAcceptExample(t, s, messageID, 0, now)
	retainUntil := time.Date(2027, 11, 11, 0, 0, 0, 0, time.UTC)
	if err := s.Records().SetRetainUntil(messageID, retainUntil); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	data, state, err := s.Records().Lookup(messageID)
	if err != nil || state != RecordRetained || len(data) == 0 {
		t.Fatalf("first reopen lookup = state %q, bytes %d, err %v; want retained bytes", state, len(data), err)
	}
	if err := s.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	data, state, err = s.Records().Lookup(messageID)
	if err != nil || state != RecordRetained || len(data) == 0 {
		t.Fatalf("first reopen expiry run = state %q, bytes %d, err %v; want retained bytes", state, len(data), err)
	}
	if err := s.Records().SetRetainUntil(messageID, time.Date(2027, 11, 10, 0, 0, 0, 0, time.UTC)); !errors.Is(err, ErrRetentionShortened) {
		t.Fatalf("shortened retain_until error = %v; want ErrRetentionShortened", err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	now = retainUntil
	s, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	data, state, err = s.Records().Lookup(messageID)
	if err != nil || state != RecordExpired || len(data) != 0 {
		t.Fatalf("expired reopen lookup = state %q, bytes %d, err %v; want expired without bytes", state, len(data), err)
	}
	if records := s.Records().Select(discovery.Query{Route: discovery.Messages}); len(records) != 0 {
		t.Fatalf("message listing after expired reopen = %#v; want empty", records)
	}
}

func retentionService(t *testing.T, now func() time.Time, dev bool) *Service {
	t.Helper()
	cfg, err := TestConfig(now)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Dev = dev
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func retentionAcceptExample(t *testing.T, s *Service, id string, example int, now time.Time) {
	t.Helper()
	if _, err := s.Records().Accept(Acceptance{
		MessageID:    id,
		Bytes:        loadExample(t, example),
		IntentDigest: sha256.Sum256([]byte(id)),
		ReceiptID:    "retention-" + id,
		IssuedAt:     now,
	}); err != nil {
		t.Fatal(err)
	}
}
