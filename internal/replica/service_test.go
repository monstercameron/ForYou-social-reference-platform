package replica

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/keys"
)

func TestNewSourceValidation(t *testing.T) {
	base := Config{Key: devnet.ReplicaKey()}
	providerID := keys.DIDOf(devnet.ProviderKey(0))
	t.Run("remote plain HTTP is rejected", func(t *testing.T) {
		cfg := base
		cfg.Sources = []Source{{Origin: "http://example.com", ProviderID: providerID}}
		if _, err := New(cfg); err == nil {
			t.Fatal("New() accepted non-loopback HTTP source")
		}
	})
	t.Run("loopback HTTP requires dev mode", func(t *testing.T) {
		cfg := base
		cfg.Sources = []Source{{Origin: "http://127.0.0.1:8445", ProviderID: providerID}}
		if _, err := New(cfg); err == nil {
			t.Fatal("New() accepted HTTP source outside dev mode")
		}
		cfg.Dev = true
		s, err := New(cfg)
		if err != nil {
			t.Fatalf("New() in dev mode: %v", err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("source path is rejected", func(t *testing.T) {
		cfg := base
		cfg.Sources = []Source{{Origin: "https://provider.example/path", ProviderID: providerID}}
		if _, err := New(cfg); err == nil {
			t.Fatal("New() accepted source path")
		}
	})
	t.Run("source userinfo is rejected", func(t *testing.T) {
		cfg := base
		cfg.Sources = []Source{{Origin: "https://user:pass@provider.example", ProviderID: providerID}}
		if _, err := New(cfg); err == nil {
			t.Fatal("New() accepted source credentials")
		}
	})
}

func TestRouteGroupsAndHealth(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	loopFailure := errors.New("test loop failure")
	loopCalls := 0
	loopNow := time.Time{}
	failLoop := false
	original := routeGroups
	routeGroups = append([]func(*Service, *http.ServeMux){}, original...)
	routeGroups = append(routeGroups, func(s *Service, mux *http.ServeMux) {
		s.Every("test", time.Minute, func(_ context.Context, got time.Time) error {
			loopCalls++
			loopNow = got
			if failLoop {
				return loopFailure
			}
			return nil
		})
		mux.HandleFunc("/group", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	})
	defer func() { routeGroups = original }()
	s, err := New(Config{Key: devnet.ReplicaKey(), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	t.Run("health endpoint uses replica role", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		s.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if recorder.Code != http.StatusOK || recorder.Body.String() != `{"role":"replica","status":"ok"}` {
			t.Fatalf("GET /healthz = %d %q", recorder.Code, recorder.Body.String())
		}
		if recorder.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("Cache-Control = %q; want no-store", recorder.Header().Get("Cache-Control"))
		}
	})
	t.Run("registered wave group is in the single mux", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		s.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/group", nil))
		if recorder.Code != http.StatusNoContent {
			t.Fatalf("GET /group status = %d; want 204", recorder.Code)
		}
	})
	t.Run("registered loop runs once with the service clock", func(t *testing.T) {
		if err := s.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		if loopCalls != 1 || !loopNow.Equal(now) {
			t.Fatalf("RunOnce() calls/time = %d/%s; want 1/%s", loopCalls, loopNow, now)
		}
	})
	t.Run("RunOnce returns a registered loop error", func(t *testing.T) {
		failLoop = true
		if err := s.RunOnce(context.Background()); !errors.Is(err, loopFailure) {
			t.Fatalf("RunOnce() error = %v; want wrapped loop failure", err)
		}
		if loopCalls != 2 || !loopNow.Equal(now) {
			t.Fatalf("RunOnce() calls/time = %d/%s; want 2/%s", loopCalls, loopNow, now)
		}
		failLoop = false
	})
	t.Run("ready endpoint reports open store", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		s.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if recorder.Code != http.StatusOK || recorder.Body.String() != `{"role":"replica","status":"ready"}` {
			t.Fatalf("GET /readyz = %d %q", recorder.Code, recorder.Body.String())
		}
	})
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	t.Run("ready endpoint reports closed store", func(t *testing.T) {
		recorder := httptest.NewRecorder()
		s.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if recorder.Code != http.StatusServiceUnavailable || recorder.Body.String() != `{"retry_after_seconds":5,"status":"unavailable"}` {
			t.Fatalf("GET /readyz = %d %q", recorder.Code, recorder.Body.String())
		}
		if got := recorder.Header().Get("Retry-After"); got != "5" {
			t.Fatalf("Retry-After = %q; want 5", got)
		}
	})
}

func TestNowUsesUTCSecondPrecision(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 13, 14, 999, time.FixedZone("test", 2*60*60))
	s, err := New(Config{Key: devnet.ReplicaKey(), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	want := time.Date(2026, 11, 11, 10, 13, 14, 0, time.UTC)
	if got := s.Now(); !got.Equal(want) {
		t.Fatalf("Now() = %s; want %s", got, want)
	}
}
