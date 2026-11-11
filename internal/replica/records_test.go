package replica

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/keys"
)

func versionCopies(t *testing.T) []Copy {
	t.Helper()
	raw, err := os.ReadFile("testdata/post-version-chain.json")
	if err != nil {
		t.Fatalf("read fixture copied from examples/post-version-chain.json: %v", err)
	}
	document, err := canon.Parse(raw, canon.MessageLimits)
	if err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	envelope, ok := document.(map[string]any)
	if !ok {
		t.Fatal("fixture must be a JSON object")
	}
	messages, ok := envelope["messages"].([]any)
	if !ok || len(messages) != 5 {
		t.Fatalf("fixture has %d messages; want exactly five", len(messages))
	}
	makeCopy := func(index int, sequence uint64) Copy {
		message, ok := messages[index].(map[string]any)
		if !ok {
			t.Fatalf("fixture message %d must be an object", index)
		}
		messageID, ok := message["message_id"].(string)
		if !ok {
			t.Fatalf("fixture message %d has no string message_id", index)
		}
		messageBytes, err := canon.Encode(message)
		if err != nil {
			t.Fatalf("encode fixture message %d: %v", index, err)
		}
		return Copy{MessageID: messageID, Bytes: messageBytes, SourceSequence: sequence}
	}
	return []Copy{makeCopy(0, 1), makeCopy(1, 2), makeCopy(2, 3), makeCopy(3, 4), makeCopy(4, 5)}
}

func replicaConfig(t *testing.T, dir string, now time.Time) Config {
	t.Helper()
	return Config{DataDir: dir, Key: devnet.ReplicaKey(), Now: func() time.Time { return now }}
}

func TestRecordsImport(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 13, 14, 987, time.FixedZone("test", -5*60*60))
	copies := versionCopies(t)
	cfg := replicaConfig(t, t.TempDir(), now)
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	providerID := keys.DIDOf(devnet.ProviderKey(0))
	t.Run("five copied posts receive local sequence and cursor", func(t *testing.T) {
		added, err := s.Records().Import(providerID, copies, 5)
		if err != nil || added != 5 {
			t.Fatalf("Import() = (%d, %v); want (5, nil)", added, err)
		}
		if got := s.Records().MaxSequence(); got != 5 {
			t.Fatalf("MaxSequence() = %d; want 5", got)
		}
		if got := s.Records().Cursor(providerID); got != 5 {
			t.Fatalf("Cursor() = %d; want 5", got)
		}
	})
	t.Run("duplicate import is idempotent", func(t *testing.T) {
		added, err := s.Records().Import(providerID, copies, 5)
		if err != nil || added != 0 {
			t.Fatalf("Import() = (%d, %v); want (0, nil)", added, err)
		}
		if got := s.Records().Cursor(providerID); got != 5 {
			t.Fatalf("Cursor() = %d; want 5", got)
		}
	})
	t.Run("cursor regression commits nothing", func(t *testing.T) {
		added, err := s.Records().Import(providerID, copies, 3)
		if added != 0 || !errors.Is(err, ErrCursorRegression) {
			t.Fatalf("Import() = (%d, %v); want (0, ErrCursorRegression)", added, err)
		}
		if got := s.Records().Cursor(providerID); got != 5 {
			t.Fatalf("Cursor() = %d; want 5", got)
		}
		if got := s.Records().MaxSequence(); got != 5 {
			t.Fatalf("MaxSequence() = %d; want 5", got)
		}
	})
	t.Run("invalid copy rejects the whole batch", func(t *testing.T) {
		fresh, err := New(replicaConfig(t, t.TempDir(), now))
		if err != nil {
			t.Fatal(err)
		}
		defer fresh.Close()
		if added, err := fresh.Records().Import(providerID, copies[:4], 4); err != nil || added != 4 {
			t.Fatalf("initial Import() = (%d, %v); want (4, nil)", added, err)
		}
		bad := Copy{MessageID: copies[0].MessageID, Bytes: []byte(`{}`), SourceSequence: 6}
		added, err := fresh.Records().Import(providerID, []Copy{copies[4], bad}, 6)
		if err == nil || added != 0 {
			t.Fatalf("Import() = (%d, %v); want parse failure and zero additions", added, err)
		}
		if _, ok := fresh.Records().Lookup(copies[4].MessageID); ok {
			t.Fatal("valid member of rejected batch was stored")
		}
		if got := fresh.Records().Cursor(providerID); got != 4 {
			t.Fatalf("Cursor() = %d; want 4", got)
		}
		if got := fresh.Records().MaxSequence(); got != 4 {
			t.Fatalf("MaxSequence() = %d; want 4", got)
		}
	})
	t.Run("copy ID must match message bytes", func(t *testing.T) {
		fresh, err := New(replicaConfig(t, t.TempDir(), now))
		if err != nil {
			t.Fatal(err)
		}
		defer fresh.Close()
		mismatched := copies[0]
		mismatched.MessageID = copies[1].MessageID
		added, err := fresh.Records().Import(providerID, []Copy{mismatched}, 1)
		if err == nil || added != 0 {
			t.Fatalf("Import() = (%d, %v); want ID mismatch and zero additions", added, err)
		}
		if got := fresh.Records().MaxSequence(); got != 0 {
			t.Fatalf("MaxSequence() = %d; want 0", got)
		}
		if got := fresh.Records().Cursor(providerID); got != 0 {
			t.Fatalf("Cursor() = %d; want 0", got)
		}
	})
}

func TestRecordsDeduplicateAcrossSources(t *testing.T) {
	copies := versionCopies(t)
	s, err := New(replicaConfig(t, t.TempDir(), time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	first := keys.DIDOf(devnet.ProviderKey(0))
	second := keys.DIDOf(devnet.ProviderKey(1))
	if added, err := s.Records().Import(first, []Copy{copies[0]}, 4); err != nil || added != 1 {
		t.Fatalf("first Import() = (%d, %v); want (1, nil)", added, err)
	}
	if added, err := s.Records().Import(second, []Copy{copies[0]}, 9); err != nil || added != 0 {
		t.Fatalf("second Import() = (%d, %v); want (0, nil)", added, err)
	}
	if s.Records().Cursor(first) != 4 || s.Records().Cursor(second) != 9 {
		t.Fatalf("source cursors = (%d, %d); want (4, 9)", s.Records().Cursor(first), s.Records().Cursor(second))
	}
	if s.Records().MaxSequence() != 1 {
		t.Fatalf("MaxSequence() = %d; want 1", s.Records().MaxSequence())
	}
}

func TestRecordsRestart(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	cfg := replicaConfig(t, dir, now)
	copies := versionCopies(t)
	providerID := keys.DIDOf(devnet.ProviderKey(0))
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Records().Import(providerID, copies, 5); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	if restarted.Records().Cursor(providerID) != 5 || restarted.Records().MaxSequence() != 5 {
		t.Fatalf("restored cursor/sequence = %d/%d; want 5/5", restarted.Records().Cursor(providerID), restarted.Records().MaxSequence())
	}
	got, ok := restarted.Records().Lookup(copies[0].MessageID)
	if !ok || !bytes.Equal(got, copies[0].Bytes) {
		t.Fatal("Lookup() did not restore byte-identical message")
	}
	newID := "fy1:" + strings.Repeat("0", 64)
	newBytes := bytes.Replace(copies[0].Bytes, []byte(copies[0].MessageID), []byte(newID), 1)
	added, err := restarted.Records().Import(keys.DIDOf(devnet.ProviderKey(1)), []Copy{{MessageID: newID, Bytes: newBytes, SourceSequence: 1}}, 1)
	if err != nil || added != 1 {
		t.Fatalf("next Import() = (%d, %v); want (1, nil)", added, err)
	}
	if got := restarted.Records().MaxSequence(); got != 6 {
		t.Fatalf("MaxSequence() = %d; want 6", got)
	}
}
