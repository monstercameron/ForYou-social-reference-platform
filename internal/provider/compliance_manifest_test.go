package provider

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/wire"
)

func TestOperatorManifestPublication(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	s := manifestTestService(t, func() time.Time { return now }, "abuse@example.invalid")
	first := manifestGet(t, s.Handler(), "/.well-known/foryou-operator.json")
	second := manifestGet(t, s.Handler(), "/.well-known/foryou-operator.json")
	if !bytes.Equal(first, second) {
		t.Fatal("repeated manifest GET changed the signed bytes")
	}
	manifestResponse := httptest.NewRecorder()
	s.Handler().ServeHTTP(manifestResponse, httptest.NewRequest(http.MethodGet, "/.well-known/foryou-operator.json", nil))
	if manifestResponse.Code != http.StatusOK || manifestResponse.Header().Get("Content-Type") != "application/json" || manifestResponse.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("manifest response headers/status = %d %#v", manifestResponse.Code, manifestResponse.Header())
	}
	var manifest wire.OperatorManifest
	if err := wire.Decode(first, "OperatorManifest", &manifest); err != nil {
		t.Fatalf("manifest schema: %v", err)
	}
	pins := sdk.NewManifestPins()
	if got := pins.Check(manifest.OperatorID, first, manifest, now); got != sdk.ManifestCurrent {
		t.Fatalf("manifest pin status = %s; want current", got)
	}
	issued, err := wire.ParseTime(manifest.IssuedAt)
	if err != nil {
		t.Fatal(err)
	}
	expires, err := wire.ParseTime(manifest.ExpiresAt)
	if err != nil {
		t.Fatal(err)
	}
	if got := expires.Sub(issued); got != 30*24*time.Hour {
		t.Fatalf("manifest validity = %s; want 30 days", got)
	}
	policy := httptest.NewRecorder()
	s.Handler().ServeHTTP(policy, httptest.NewRequest(http.MethodGet, "/policy", nil))
	if policy.Code != http.StatusOK || policy.Header().Get("Content-Type") != "application/json" || policy.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("policy response headers/status = %d %#v", policy.Code, policy.Header())
	}
	digest := sha256.Sum256(policy.Body.Bytes())
	if manifest.PolicyDigest != hex.EncodeToString(digest[:]) {
		t.Fatal("manifest policy_digest does not match the exact /policy bytes")
	}
}

func TestOperatorManifestRenewalWindow(t *testing.T) {
	base := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	now := base
	s := manifestTestService(t, func() time.Time { return now }, "abuse@example.invalid")
	initial := manifestReadValue(t, manifestGet(t, s.Handler(), "/.well-known/foryou-operator.json"))
	pins := sdk.NewManifestPins()
	t.Run("23 days keeps the stored version", func(t *testing.T) {
		now = base.Add(23 * 24 * time.Hour)
		body := manifestGet(t, s.Handler(), "/.well-known/foryou-operator.json")
		manifest := manifestReadValue(t, body)
		if manifest.ManifestVersion != 1 || manifest.IssuedAt != initial.IssuedAt {
			t.Fatalf("manifest version %d issued %s; want version 1 issued %s", manifest.ManifestVersion, manifest.IssuedAt, initial.IssuedAt)
		}
		if got := pins.Check(manifest.OperatorID, body, manifest, now); got != sdk.ManifestCurrent {
			t.Fatalf("version 1 pin status = %s; want current", got)
		}
	})
	t.Run("24 days issues version two", func(t *testing.T) {
		now = base.Add(24 * 24 * time.Hour)
		body := manifestGet(t, s.Handler(), "/.well-known/foryou-operator.json")
		got := manifestReadValue(t, body)
		if got.ManifestVersion != 2 {
			t.Fatalf("manifest version = %d; want 2", got.ManifestVersion)
		}
		if got.IssuedAt != "2026-12-05T00:00:00Z" {
			t.Fatalf("renewed issued_at = %q; want 2026-12-05T00:00:00Z", got.IssuedAt)
		}
		if status := pins.Check(got.OperatorID, body, got, now); status != sdk.ManifestCurrent {
			t.Fatalf("version 2 pin status = %s; want current", status)
		}
	})
}

func TestOperatorManifestRestartWithChangedConfiguration(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	dir := t.TempDir()
	cfg := manifestTestConfig(t, clock, "abuse@example.invalid")
	cfg.DataDir = filepath.Join(dir, "provider")
	first, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	firstBody := manifestGet(t, first.Handler(), "/.well-known/foryou-operator.json")
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	cfg.Operator.AbuseContact = "new-abuse@example.invalid"
	second, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	secondBody := manifestGet(t, second.Handler(), "/.well-known/foryou-operator.json")
	got := manifestReadValue(t, secondBody)
	if got.ManifestVersion != 2 || bytes.Equal(firstBody, secondBody) {
		t.Fatalf("changed configuration yielded version %d and identical bytes=%t; want version 2 and changed bytes", got.ManifestVersion, bytes.Equal(firstBody, secondBody))
	}
}

func TestOperatorManifestRestartWithUnchangedConfiguration(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	dir := t.TempDir()
	cfg := manifestTestConfig(t, clock, "abuse@example.invalid")
	cfg.DataDir = filepath.Join(dir, "provider")
	first, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	firstBody := manifestGet(t, first.Handler(), "/.well-known/foryou-operator.json")
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	secondBody := manifestGet(t, second.Handler(), "/.well-known/foryou-operator.json")
	if !bytes.Equal(firstBody, secondBody) {
		t.Fatal("restart with unchanged configuration changed the stored manifest bytes")
	}
	if got := manifestReadValue(t, secondBody).ManifestVersion; got != 1 {
		t.Fatalf("manifest version after unchanged restart = %d; want 1", got)
	}
}

func TestOperatorManifestPolicyLimits(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	s := manifestTestService(t, func() time.Time { return now }, "abuse@example.invalid")
	body := manifestGet(t, s.Handler(), "/policy")
	value, err := canon.Parse(body, canon.Limits{MaxDepth: 32})
	if err != nil {
		t.Fatal(err)
	}
	document := value.(map[string]any)
	reads := document["read_limits"].(map[string]any)
	if reads["listing_per_source_per_minute"] != json.Number("6") {
		t.Fatalf("listing per-source limit = %v; want 6", reads["listing_per_source_per_minute"])
	}
	snapshots := document["snapshot_limits"].(map[string]any)
	if snapshots["lifetime_seconds"] != json.Number("900") {
		t.Fatalf("snapshot lifetime = %v; want 900 seconds", snapshots["lifetime_seconds"])
	}
}

func manifestTestService(t *testing.T, now func() time.Time, abuseContact string) *Service {
	t.Helper()
	cfg := manifestTestConfig(t, now, abuseContact)
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func manifestTestConfig(t *testing.T, now func() time.Time, abuseContact string) Config {
	t.Helper()
	cfg, err := TestConfig(now)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Operator = OperatorConfig{
		LegalContact: "legal@example.invalid", AbuseContact: abuseContact,
		PrivacyContact: "privacy@example.invalid",
	}
	cfg.Reads.ListingPerSourcePerMinute = 6
	cfg.Snapshots.Lifetime = 15 * time.Minute
	return cfg
}

func manifestGet(t *testing.T, handler http.Handler, path string) []byte {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, body=%s", path, response.Code, response.Body.String())
	}
	return append([]byte(nil), response.Body.Bytes()...)
}

func manifestReadValue(t *testing.T, body []byte) wire.OperatorManifest {
	t.Helper()
	var manifest wire.OperatorManifest
	if err := wire.Decode(body, "OperatorManifest", &manifest); err != nil {
		t.Fatal(err)
	}
	return manifest
}
