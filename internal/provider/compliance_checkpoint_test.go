package provider

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/keys"
)

var checkpointTestNow = time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)

func checkpointTestConfig(t *testing.T, dir string) Config {
	t.Helper()
	cfg, err := TestConfig(func() time.Time { return checkpointTestNow })
	if err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = dir
	return cfg
}

func checkpointTestOpen(t *testing.T, dir string) *Service {
	t.Helper()
	cfg := checkpointTestConfig(t, dir)
	cfg.ControlCheckpointDir = dir + "-checkpoint"
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestCheckpointLocation(t *testing.T) {
	t.Run("dev mode defaults the checkpoint directory to a sibling of DataDir", func(t *testing.T) {
		root := t.TempDir()
		s, err := New(checkpointTestConfig(t, filepath.Join(root, "provider")))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		want := filepath.Join(root, "provider-checkpoint")
		if got := s.Config().ControlCheckpointDir; got != want {
			t.Fatalf("ControlCheckpointDir = %q; want %q", got, want)
		}
		if _, err := os.Stat(filepath.Join(want, controlInitializedName)); err != nil {
			t.Fatalf("first-start marker not in the default directory: %v", err)
		}
	})
	t.Run("production mode requires an explicit checkpoint directory", func(t *testing.T) {
		cfg := checkpointTestConfig(t, filepath.Join(t.TempDir(), "provider"))
		cfg.Dev = false
		_, err := New(cfg)
		if err == nil || !strings.Contains(err.Error(), "ControlCheckpointDir") {
			t.Fatalf("New without checkpoint directory error = %v", err)
		}
	})
	t.Run("checkpoint directory inside DataDir is rejected", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "provider")
		cfg := checkpointTestConfig(t, dir)
		cfg.ControlCheckpointDir = filepath.Join(dir, "checkpoint")
		_, err := New(cfg)
		if err == nil || !strings.Contains(err.Error(), "must be outside DataDir") {
			t.Fatalf("New with checkpoint inside DataDir error = %v", err)
		}
	})
}

func TestControlCheckpointIndependence(t *testing.T) {
	t.Run("older whole DataDir fails recovery with 503", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "provider")
		s := controlNewFileService(t, dir)
		controlAcceptExample(t, s)
		controls := s.controlsState.(*controlState)
		if _, err := controlApply(s, controls, "restrict", controlTestMessageID, LegalRestricted); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		older := filepath.Join(t.TempDir(), "older")
		if err := os.CopyFS(older, os.DirFS(dir)); err != nil {
			t.Fatal(err)
		}
		s = controlNewFileService(t, dir)
		controls = s.controlsState.(*controlState)
		if _, err := controlApply(s, controls, "lift", controlTestMessageID, Unrestricted); err != nil {
			t.Fatal(err)
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(dir); err != nil {
			t.Fatal(err)
		}
		if err := os.CopyFS(dir, os.DirFS(older)); err != nil {
			t.Fatal(err)
		}
		restarted := controlNewFileService(t, dir)
		if _, err := restarted.Restriction(controlTestMessageID); err == nil {
			t.Fatal("restriction succeeded after rollback below the independent checkpoint")
		}
		read := httptest.NewRecorder()
		restarted.Handler().ServeHTTP(read, httptest.NewRequest(http.MethodGet, "/v1/messages/"+controlTestMessageID, nil))
		if read.Code != http.StatusServiceUnavailable {
			t.Fatalf("message read status = %d", read.Code)
		}
		response := httptest.NewRecorder()
		restarted.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if response.Code != http.StatusServiceUnavailable {
			t.Fatalf("/readyz status = %d", response.Code)
		}
	})
	t.Run("deleted controls and checkpoint after initialization fail closed", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "provider")
		s := controlNewFileService(t, dir)
		controlAcceptExample(t, s)
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(dir, "controls")); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Join(s.cfg.ControlCheckpointDir, controlCheckpointName)); err != nil {
			t.Fatal(err)
		}
		restarted := controlNewFileService(t, dir)
		if _, err := restarted.Restriction(controlTestMessageID); err == nil {
			t.Fatal("restriction succeeded with deleted control state after initialization")
		}
	})
}

func TestIntakeCheckpointRecovery(t *testing.T) {
	t.Run("restoring an older content directory keeps acknowledged intake recoverable", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "provider")
		s := checkpointTestOpen(t, dir)
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		older := filepath.Join(t.TempDir(), "older-content")
		if err := os.CopyFS(older, os.DirFS(filepath.Join(dir, "content"))); err != nil {
			t.Fatal(err)
		}
		s = checkpointTestOpen(t, dir)
		request, body, recoveryBytes := intakeTestSubmission(t, s, checkpointTestNow, "Example notice")
		if created := intakeTestPost(t, s, body, recoveryBytes); created.Code != http.StatusCreated {
			t.Fatalf("submission status = %d: %s", created.Code, created.Body.Bytes())
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(dir, "content")); err != nil {
			t.Fatal(err)
		}
		if err := os.CopyFS(filepath.Join(dir, "content"), os.DirFS(older)); err != nil {
			t.Fatal(err)
		}
		restarted := checkpointTestOpen(t, dir)
		recovery := intakeTestRequest(t, restarted, http.MethodGet, "/v1/intake/"+request.RequestID, nil, keys.EncodeB64URL(recoveryBytes))
		if recovery.Code != http.StatusOK {
			t.Fatalf("recovery status = %d: %s", recovery.Code, recovery.Body.Bytes())
		}
	})
	t.Run("rolling back the intake store below the checkpoint yields 503", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "provider")
		s := checkpointTestOpen(t, dir)
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		older := filepath.Join(t.TempDir(), "older-controls")
		if err := os.CopyFS(older, os.DirFS(filepath.Join(dir, "controls"))); err != nil {
			t.Fatal(err)
		}
		s = checkpointTestOpen(t, dir)
		request, body, recoveryBytes := intakeTestSubmission(t, s, checkpointTestNow, "Example notice")
		if created := intakeTestPost(t, s, body, recoveryBytes); created.Code != http.StatusCreated {
			t.Fatalf("submission status = %d: %s", created.Code, created.Body.Bytes())
		}
		if err := s.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(filepath.Join(dir, "controls")); err != nil {
			t.Fatal(err)
		}
		if err := os.CopyFS(filepath.Join(dir, "controls"), os.DirFS(older)); err != nil {
			t.Fatal(err)
		}
		restarted := checkpointTestOpen(t, dir)
		if recovery := intakeTestRequest(t, restarted, http.MethodGet, "/v1/intake/"+request.RequestID, nil, keys.EncodeB64URL(recoveryBytes)); recovery.Code != http.StatusServiceUnavailable {
			t.Fatalf("recovery status = %d: %s", recovery.Code, recovery.Body.Bytes())
		}
		if retry := intakeTestPost(t, restarted, body, recoveryBytes); retry.Code != http.StatusServiceUnavailable {
			t.Fatalf("submission status after rollback = %d: %s", retry.Code, retry.Body.Bytes())
		}
	})
}
