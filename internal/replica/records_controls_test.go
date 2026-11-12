package replica

import (
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/keys"
)

func controlledReplica(t *testing.T, dir string, now time.Time) *Service {
	t.Helper()
	s, err := New(replicaConfig(t, dir, now))
	if err != nil {
		t.Fatal(err)
	}
	if added, err := s.Records().Import(keys.DIDOf(devnet.ProviderKey(0)), versionCopies(t), 5); err != nil || added != 5 {
		t.Fatalf("Import() = (%d, %v); want (5, nil)", added, err)
	}
	return s
}

func TestRecordsControls(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	copies := versionCopies(t)
	providerID := keys.DIDOf(devnet.ProviderKey(0))
	unknownID := "fy1:" + strings.Repeat("0", 64)

	t.Run("legal restriction answers 403 with only the status", func(t *testing.T) {
		s := controlledReplica(t, t.TempDir(), now)
		defer s.Close()
		id := copies[0].MessageID
		if generation, err := s.Restrict(id, StatusLegalRestricted); err != nil || generation != 1 {
			t.Fatalf("Restrict() = (%d, %v); want (1, nil)", generation, err)
		}
		w := readTestRequest(t, s, http.MethodGet, "/v1/messages/"+id)
		if w.Code != http.StatusForbidden || w.Body.String() != `{"status":"legal_restricted"}` {
			t.Fatalf("GET = %d %q; want 403 {\"status\":\"legal_restricted\"}", w.Code, w.Body.String())
		}
	})

	t.Run("restricted record is omitted from listings without a marker", func(t *testing.T) {
		s := controlledReplica(t, t.TempDir(), now)
		defer s.Close()
		if _, err := s.Restrict(copies[0].MessageID, StatusRestricted); err != nil {
			t.Fatalf("Restrict() = %v; want nil", err)
		}
		w := readTestRequest(t, s, http.MethodGet, "/v1/messages?limit=100")
		if w.Code != http.StatusOK {
			t.Fatalf("listing = %d; want 200", w.Code)
		}
		if strings.Contains(w.Body.String(), string(copies[0].Bytes)) {
			t.Fatal("listing contains the restricted record")
		}
		if !strings.Contains(w.Body.String(), string(copies[1].Bytes)) {
			t.Fatal("listing omits an unrestricted record")
		}
	})

	t.Run("nonrevealing restriction answers the same 404 as an unknown ID", func(t *testing.T) {
		s := controlledReplica(t, t.TempDir(), now)
		defer s.Close()
		if _, err := s.Restrict(copies[1].MessageID, StatusNotFound); err != nil {
			t.Fatalf("Restrict() = %v; want nil", err)
		}
		hidden := readTestRequest(t, s, http.MethodGet, "/v1/messages/"+copies[1].MessageID)
		unknown := readTestRequest(t, s, http.MethodGet, "/v1/messages/"+unknownID)
		if hidden.Code != http.StatusNotFound || hidden.Body.String() != `{"status":"not_found"}` {
			t.Fatalf("hidden GET = %d %q; want 404 {\"status\":\"not_found\"}", hidden.Code, hidden.Body.String())
		}
		if unknown.Code != hidden.Code || unknown.Body.String() != hidden.Body.String() {
			t.Fatalf("unknown GET = %d %q; want the hidden response", unknown.Code, unknown.Body.String())
		}
	})

	t.Run("removal survives a later sync from the source", func(t *testing.T) {
		s := controlledReplica(t, t.TempDir(), now)
		defer s.Close()
		id := copies[2].MessageID
		if generation, err := s.Remove(id, StatusLegalRestricted); err != nil || generation != 1 {
			t.Fatalf("Remove() = (%d, %v); want (1, nil)", generation, err)
		}
		if added, err := s.Records().Import(providerID, copies, 7); err != nil || added != 0 {
			t.Fatalf("sync Import() = (%d, %v); want (0, nil)", added, err)
		}
		if _, ok := s.Records().Lookup(id); ok {
			t.Fatal("removed record was stored again by sync")
		}
		w := readTestRequest(t, s, http.MethodGet, "/v1/messages/"+id)
		if w.Code != http.StatusForbidden || w.Body.String() != `{"status":"legal_restricted"}` {
			t.Fatalf("GET after sync = %d %q; want 403 {\"status\":\"legal_restricted\"}", w.Code, w.Body.String())
		}
	})

	t.Run("generation advances on every committed control", func(t *testing.T) {
		s := controlledReplica(t, t.TempDir(), now)
		defer s.Close()
		if generation, err := s.Restrict(copies[0].MessageID, StatusRestricted); err != nil || generation != 1 {
			t.Fatalf("first Restrict() = (%d, %v); want (1, nil)", generation, err)
		}
		if generation, err := s.Remove(copies[1].MessageID, StatusLegalRestricted); err != nil || generation != 2 {
			t.Fatalf("Remove() = (%d, %v); want (2, nil)", generation, err)
		}
		if generation, err := s.Restrict(copies[0].MessageID, StatusPolicyRestricted); err != nil || generation != 3 {
			t.Fatalf("second Restrict() = (%d, %v); want (3, nil)", generation, err)
		}
		if got := s.Visibility().Generation(); got != 3 {
			t.Fatalf("Generation() = %d; want 3", got)
		}
	})

	t.Run("controls survive a restart", func(t *testing.T) {
		dir := t.TempDir()
		s := controlledReplica(t, dir, now)
		if _, err := s.Restrict(copies[0].MessageID, StatusRestricted); err != nil {
			t.Fatalf("Restrict() = %v; want nil", err)
		}
		if _, err := s.Remove(copies[1].MessageID, StatusLegalRestricted); err != nil {
			t.Fatalf("Remove() = %v; want nil", err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		restarted, err := New(replicaConfig(t, dir, now))
		if err != nil {
			t.Fatal(err)
		}
		defer restarted.Close()
		if got := restarted.Visibility().Generation(); got != 2 {
			t.Fatalf("restored Generation() = %d; want 2", got)
		}
		if w := readTestRequest(t, restarted, http.MethodGet, "/v1/messages/"+copies[0].MessageID); w.Code != http.StatusForbidden || w.Body.String() != `{"status":"restricted"}` {
			t.Fatalf("restored restriction GET = %d %q; want 403 {\"status\":\"restricted\"}", w.Code, w.Body.String())
		}
		if w := readTestRequest(t, restarted, http.MethodGet, "/v1/messages/"+copies[1].MessageID); w.Code != http.StatusForbidden || w.Body.String() != `{"status":"legal_restricted"}` {
			t.Fatalf("restored removal GET = %d %q; want 403 {\"status\":\"legal_restricted\"}", w.Code, w.Body.String())
		}
		if _, ok := restarted.Records().Lookup(copies[1].MessageID); ok {
			t.Fatal("restart restored a removed record")
		}
	})

	t.Run("uncertain control state fails closed with 503", func(t *testing.T) {
		s := controlledReplica(t, t.TempDir(), now)
		defer s.Close()
		s.records.controls.mu.Lock()
		s.records.controls.err = errors.New("test control state uncertain")
		s.records.controls.mu.Unlock()
		if w := readTestRequest(t, s, http.MethodGet, "/v1/messages/"+copies[0].MessageID); w.Code != http.StatusServiceUnavailable {
			t.Fatalf("GET = %d; want 503", w.Code)
		}
		if _, err := s.Restrict(copies[0].MessageID, StatusRestricted); err == nil {
			t.Fatal("Restrict() succeeded with uncertain control state")
		}
	})

	t.Run("invalid control is refused and commits nothing", func(t *testing.T) {
		s := controlledReplica(t, t.TempDir(), now)
		defer s.Close()
		if _, err := s.Restrict("fy1:short", StatusRestricted); err == nil {
			t.Fatal("Restrict() accepted a malformed message ID")
		}
		if _, err := s.Restrict(copies[0].MessageID, ControlStatus("allowed")); err == nil {
			t.Fatal("Restrict() accepted a status outside the closed set")
		}
		if got := s.Visibility().Generation(); got != 0 {
			t.Fatalf("Generation() = %d; want 0", got)
		}
	})
}
