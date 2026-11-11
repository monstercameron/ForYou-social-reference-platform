package provider

import (
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/store"
)

const controlTestMessageID = "fy1:4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f"

func TestControlRestrictionAndLift(t *testing.T) {
	t.Run("restrict then lift", func(t *testing.T) {
		s := controlNewMemoryService(t, true)
		controlAcceptExample(t, s)
		if _, err := controlApply(s, s.controlsState.(*controlState), "restrict", controlTestMessageID, LegalRestricted); err != nil {
			t.Fatal(err)
		}
		if got, err := s.Restriction(controlTestMessageID); err != nil || got != LegalRestricted {
			t.Fatalf("restriction = %q, %v", got, err)
		}
		if visible, _, err := s.Visibility().Visible(controlTestMessageID); err != nil || visible {
			t.Fatalf("visible after restriction = %t, %v", visible, err)
		}
		if _, err := controlApply(s, s.controlsState.(*controlState), "lift", controlTestMessageID, Unrestricted); err != nil {
			t.Fatal(err)
		}
		if got, err := s.Restriction(controlTestMessageID); err != nil || got != Unrestricted {
			t.Fatalf("restriction after lift = %q, %v", got, err)
		}
	})
}

func TestControlRemovalIsPermanent(t *testing.T) {
	t.Run("lift cannot undo removal", func(t *testing.T) {
		s := controlNewMemoryService(t, true)
		controlAcceptExample(t, s)
		controls := s.controlsState.(*controlState)
		if _, err := controlApply(s, controls, "remove", controlTestMessageID, Concealed); err != nil {
			t.Fatal(err)
		}
		if _, err := controlApply(s, controls, "lift", controlTestMessageID, Unrestricted); err != nil {
			t.Fatal(err)
		}
		if got, state, err := s.Records().Lookup(controlTestMessageID); err != nil || state != RecordRemoved || len(got) != 0 {
			t.Fatalf("removed lookup = %q, %d bytes, %v", state, len(got), err)
		}
		if got, err := s.Restriction(controlTestMessageID); err != nil || got != Concealed {
			t.Fatalf("restriction after removal and lift = %q, %v", got, err)
		}
	})
}

func TestControlRestartKeepsStateAndReconcilesBackup(t *testing.T) {
	t.Run("intact checkpoint preserves restriction", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "provider")
		s := controlNewFileService(t, dir)
		controlAcceptExample(t, s)
		if _, err := controlApply(s, s.controlsState.(*controlState), "restrict", controlTestMessageID, LegalRestricted); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		restarted := controlNewFileService(t, dir)
		defer restarted.Close()
		if got, err := restarted.Restriction(controlTestMessageID); err != nil || got != LegalRestricted {
			t.Fatalf("restarted restriction = %q, %v", got, err)
		}
	})
	t.Run("old content backup cannot restore a removed record", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "provider")
		s := controlNewFileService(t, dir)
		controlAcceptExample(t, s)
		backup := filepath.Join(t.TempDir(), "old-content")
		if err := os.CopyFS(backup, os.DirFS(filepath.Join(dir, "content"))); err != nil {
			t.Fatal(err)
		}
		if _, err := controlApply(s, s.controlsState.(*controlState), "remove", controlTestMessageID, Concealed); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(dir, "content")); err != nil {
			t.Fatal(err)
		}
		if err := os.CopyFS(filepath.Join(dir, "content"), os.DirFS(backup)); err != nil {
			t.Fatal(err)
		}
		restarted := controlNewFileService(t, dir)
		defer restarted.Close()
		if _, state, err := restarted.Records().Lookup(controlTestMessageID); err != nil || state != RecordRemoved {
			t.Fatalf("restored record state = %q, %v", state, err)
		}
		if got, err := restarted.Restriction(controlTestMessageID); err != nil || got != Concealed {
			t.Fatalf("restriction after backup restore = %q, %v", got, err)
		}
		if visible, _, err := restarted.Visibility().Visible(controlTestMessageID); err != nil || visible {
			t.Fatalf("restored removed record visible = %t, %v", visible, err)
		}
	})
}

func TestControlRecoveryFailsClosed(t *testing.T) {
	t.Run("deleted checkpoint with nonempty log", func(t *testing.T) {
		controlCheckRecoveryFailure(t, func(t *testing.T, dir string, _ *Service) {
			if err := os.Remove(filepath.Join(dir, "control-checkpoint.json")); err != nil {
				t.Fatal(err)
			}
		})
	})
	t.Run("altered digest", func(t *testing.T) {
		controlCheckRecoveryFailure(t, func(t *testing.T, dir string, s *Service) {
			controlAlterCheckpoint(t, dir, s.Key(), func(checkpoint *controlCheckpoint) { checkpoint.Digest = strings.Repeat("0", 64) })
		})
	})
	t.Run("checkpoint names another operator", func(t *testing.T) {
		controlCheckRecoveryFailure(t, func(t *testing.T, dir string, _ *Service) {
			otherKey := controlOtherProviderKey()
			controlAlterCheckpoint(t, dir, otherKey, func(checkpoint *controlCheckpoint) { checkpoint.Operator = keys.DIDOf(otherKey) })
		})
	})
	t.Run("checkpoint has another generation", func(t *testing.T) {
		controlCheckRecoveryFailure(t, func(t *testing.T, dir string, s *Service) {
			controlAlterCheckpoint(t, dir, s.Key(), func(checkpoint *controlCheckpoint) { checkpoint.Generation = 2 })
		})
	})
	t.Run("control journal is truncated", func(t *testing.T) {
		controlCheckRecoveryFailure(t, func(t *testing.T, dir string, _ *Service) {
			path := filepath.Join(dir, "controls", "journal.jsonl")
			journal, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			second := strings.Index(string(journal), `"k":"00000000000000000002"`)
			if second < 0 {
				t.Fatal("control journal lacks the second control event")
			}
			lineStart := strings.LastIndexByte(string(journal[:second]), '\n') + 1
			if err = os.WriteFile(path, journal[:lineStart], 0600); err != nil {
				t.Fatal(err)
			}
		})
	})
}

func TestControlAcceptsCommittedTail(t *testing.T) {
	t.Run("tail after checkpoint is checkpointed on restart", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "provider")
		s := controlNewFileService(t, dir)
		controlAcceptExample(t, s)
		controls := s.controlsState.(*controlState)
		if _, err := controlApply(s, controls, "restrict", controlTestMessageID, LegalRestricted); err != nil {
			t.Fatal(err)
		}
		if err := s.ControlStore().Update(func(tx store.Tx) error {
			sequence, err := tx.NextSequence("provider.controls")
			if err != nil {
				return err
			}
			event := controlEvent{Action: "restrict", At: s.Now().Format("2006-01-02T15:04:05Z"), MessageID: controlTestMessageID, Sequence: sequence, Status: Restricted}
			encoded, err := json.Marshal(event)
			if err != nil {
				return err
			}
			return tx.Put("provider.controls.log", "00000000000000000002", encoded)
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		restarted := controlNewFileService(t, dir)
		defer restarted.Close()
		if got, err := restarted.Restriction(controlTestMessageID); err != nil || got != Restricted {
			t.Fatalf("recovered tail restriction = %q, %v", got, err)
		}
		encoded, err := os.ReadFile(filepath.Join(dir, "control-checkpoint.json"))
		if err != nil {
			t.Fatal(err)
		}
		var checkpoint controlCheckpoint
		if err = json.Unmarshal(encoded, &checkpoint); err != nil || checkpoint.Sequence != 2 {
			t.Fatalf("checkpoint sequence = %d, %v", checkpoint.Sequence, err)
		}
	})
}

func TestControlDevRoute(t *testing.T) {
	t.Run("loopback restriction succeeds after durable checkpoint", func(t *testing.T) {
		s := controlNewMemoryService(t, true)
		request := httptest.NewRequest(http.MethodPost, "/dev/controls", strings.NewReader(`{"action":"restrict","message_id":"`+controlTestMessageID+`","status":"legal_restricted"}`))
		request.RemoteAddr = "127.0.0.1:1234"
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusOK || response.Body.String() != `{"sequence":1}` {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
	})
	t.Run("nonloopback peer is concealed", func(t *testing.T) {
		s := controlNewMemoryService(t, true)
		request := httptest.NewRequest(http.MethodPost, "/dev/controls", strings.NewReader(`{}`))
		request.RemoteAddr = "192.0.2.1:1234"
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusNotFound || response.Body.String() != `{"status":"not_found"}` {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
	})
	t.Run("route is absent outside dev mode", func(t *testing.T) {
		s := controlNewMemoryService(t, false)
		request := httptest.NewRequest(http.MethodPost, "/dev/controls", strings.NewReader(`{}`))
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusNotFound {
			t.Fatalf("response = %d", response.Code)
		}
	})
	t.Run("unknown JSON field is invalid", func(t *testing.T) {
		s := controlNewMemoryService(t, true)
		request := httptest.NewRequest(http.MethodPost, "/dev/controls", strings.NewReader(`{"action":"restrict","message_id":"`+controlTestMessageID+`","status":"legal_restricted","extra":true}`))
		request.RemoteAddr = "127.0.0.1:1234"
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || response.Body.String() != `{"status":"invalid_query"}` {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
	})
	t.Run("malformed message ID is invalid", func(t *testing.T) {
		s := controlNewMemoryService(t, true)
		request := httptest.NewRequest(http.MethodPost, "/dev/controls", strings.NewReader(`{"action":"restrict","message_id":"fy1:zz","status":"legal_restricted"}`))
		request.RemoteAddr = "127.0.0.1:1234"
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, request)
		if response.Code != http.StatusBadRequest || response.Body.String() != `{"status":"invalid_query"}` {
			t.Fatalf("response = %d %s", response.Code, response.Body.String())
		}
	})
}

func controlCheckRecoveryFailure(t *testing.T, alter func(*testing.T, string, *Service)) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "provider")
	s := controlNewFileService(t, dir)
	controlAcceptExample(t, s)
	controls := s.controlsState.(*controlState)
	if _, err := controlApply(s, controls, "restrict", controlTestMessageID, LegalRestricted); err != nil {
		t.Fatal(err)
	}
	if _, err := controlApply(s, controls, "lift", controlTestMessageID, Unrestricted); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	alter(t, dir, s)
	restarted := controlNewFileService(t, dir)
	defer restarted.Close()
	if _, err := restarted.Restriction(controlTestMessageID); err == nil {
		t.Fatal("restriction succeeded with unverified control state")
	}
	if err := restarted.controlsState.Ready(); err == nil {
		t.Fatal("Ready succeeded with unverified control state")
	}
	response := httptest.NewRecorder()
	restarted.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz status = %d", response.Code)
	}
}

func controlAlterCheckpoint(t *testing.T, dir string, key keys.PrivateKey, change func(*controlCheckpoint)) {
	t.Helper()
	path := filepath.Join(dir, "control-checkpoint.json")
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint controlCheckpoint
	if err = json.Unmarshal(encoded, &checkpoint); err != nil {
		t.Fatal(err)
	}
	change(&checkpoint)
	unsigned := controlCheckpointUnsigned{Operator: checkpoint.Operator, Generation: checkpoint.Generation, Sequence: checkpoint.Sequence, Digest: checkpoint.Digest}
	canonical, err := canon.Encode(unsigned)
	if err != nil {
		t.Fatal(err)
	}
	checkpoint.Signature, err = keys.Sign(key, "control-checkpoint", canonical)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(checkpoint)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, encoded, 0600); err != nil {
		t.Fatal(err)
	}
}

func controlNewMemoryService(t *testing.T, dev bool) *Service {
	t.Helper()
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	cfg, err := TestConfig(func() time.Time { return now })
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

func controlNewFileService(t *testing.T, dir string) *Service {
	t.Helper()
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	cfg, err := TestConfig(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = dir
	cfg.ContentStore = nil
	cfg.ControlStore = nil
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func controlAcceptExample(t *testing.T, s *Service) {
	t.Helper()
	if _, err := s.Records().Accept(Acceptance{MessageID: controlTestMessageID, Bytes: loadExample(t, 0), IntentDigest: sha256.Sum256([]byte("controls")), ReceiptID: "control-test", IssuedAt: s.Now()}); err != nil {
		t.Fatal(err)
	}
}

func controlOtherProviderKey() keys.PrivateKey {
	return devnet.ProviderKey(1)
}
