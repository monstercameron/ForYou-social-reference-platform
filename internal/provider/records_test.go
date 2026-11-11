package provider

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"foryou.local/foryou/internal/discovery"
	"foryou.local/foryou/internal/store"
)

func TestRecordsAccept(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	cfg, err := TestConfig(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	message := loadExample(t, 0)
	id := "fy1:4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f"
	digest := sha256.Sum256([]byte("intent"))
	a := Acceptance{MessageID: id, Bytes: message, IntentDigest: digest, ReceiptID: "receipt", IssuedAt: now}
	first, err := s.Records().Accept(a)
	if err != nil || !first.Created || first.Sequence != 1 {
		t.Fatalf("first accept = %#v, %v", first, err)
	}
	retry, err := s.Records().Accept(a)
	if err != nil || retry.Created || retry.Sequence != 1 {
		t.Fatalf("retry = %#v, %v", retry, err)
	}
	other := a
	other.IntentDigest = sha256.Sum256([]byte("other"))
	if _, err = s.Records().Accept(other); !errors.Is(err, ErrIDCollision) {
		t.Fatalf("collision error = %v", err)
	}
	bytes, state, err := s.Records().Lookup(id)
	if err != nil || state != RecordRetained || string(bytes) != string(message) {
		t.Fatalf("lookup state=%q err=%v", state, err)
	}
	if err = s.Records().SetRetainUntil(id, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = s.Records().SetRetainUntil(id, now.Add(time.Minute)); !errors.Is(err, ErrRetentionShortened) {
		t.Fatalf("shorten error = %v", err)
	}
	if err = s.Records().Expire(id, now); !errors.Is(err, ErrNotExpired) {
		t.Fatalf("early expiry = %v", err)
	}
	_, beforeHidden, _ := s.Visibility().Visible(id)
	if len(s.Records().Select(discovery.Query{Route: discovery.Messages})) != 1 {
		t.Fatal("retained record missing from selection")
	}
	if err = s.Records().Expire(id, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if got := s.Records().MaxSequence(); got != 1 {
		t.Fatalf("sequence after expiry = %d, want 1", got)
	}
	_, state, err = s.Records().Lookup(id)
	if err != nil || state != RecordExpired {
		t.Fatalf("expired lookup = %q, %v", state, err)
	}
	retry, err = s.Records().Accept(a)
	if err != nil || retry.Created || retry.Sequence != 1 {
		t.Fatalf("expired retry = %#v, %v", retry, err)
	}
	bytes, state, err = s.Records().Lookup(id)
	if err != nil || state != RecordExpired || len(bytes) != 0 {
		t.Fatalf("expired retry restored bytes: state=%q bytes=%d err=%v", state, len(bytes), err)
	}
	visible, hiddenAt, err := s.Visibility().Visible(id)
	if err != nil || visible || hiddenAt <= beforeHidden {
		t.Fatalf("expired visibility = %t, hiddenAt %d, prior %d, %v", visible, hiddenAt, beforeHidden, err)
	}
	if len(s.Records().Select(discovery.Query{Route: discovery.Messages})) != 0 {
		t.Fatal("expired record remains in selection")
	}
	if err = s.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRecordsRemovalIsPermanent(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	cfg, err := TestConfig(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "provider")
	cfg.DataDir = dir
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	message := loadExample(t, 0)
	id := "fy1:4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f"
	a := Acceptance{MessageID: id, Bytes: message, IntentDigest: sha256.Sum256([]byte("intent")), ReceiptID: "r", IssuedAt: now}
	if _, err = s.Records().Accept(a); err != nil {
		t.Fatal(err)
	}
	if err = s.Records().Remove(id); err != nil {
		t.Fatal(err)
	}
	removedBytes, state, err := s.Records().Lookup(id)
	if err != nil || state != RecordRemoved || len(removedBytes) != 0 {
		t.Fatalf("removed state=%q bytes=%d err=%v", state, len(removedBytes), err)
	}
	if _, err = s.Records().Accept(a); !errors.Is(err, ErrRemoved) {
		t.Fatalf("accept removed = %v", err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	cfg, err = TestConfig(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = dir
	s, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	removedBytes, state, err = s.Records().Lookup(id)
	if err != nil || state != RecordRemoved || len(removedBytes) != 0 {
		t.Fatalf("restarted removed state=%q bytes=%d err=%v", state, len(removedBytes), err)
	}
	if _, err = s.Records().Accept(a); !errors.Is(err, ErrRemoved) {
		t.Fatalf("restarted accept removed = %v", err)
	}
}

func TestRecordsRemovalBeforeAcceptance(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	cfg, err := TestConfig(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	id := "fy1:4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f"
	if err = s.Records().Remove(id); err != nil {
		t.Fatal(err)
	}
	a := Acceptance{MessageID: id, Bytes: loadExample(t, 0), IntentDigest: sha256.Sum256([]byte("intent")), ReceiptID: "r", IssuedAt: now}
	if _, err = s.Records().Accept(a); !errors.Is(err, ErrRemoved) {
		t.Fatalf("accept after removal of an unaccepted ID = %v", err)
	}
	if s.Records().MaxSequence() != 0 {
		t.Fatalf("removal assigned sequence %d", s.Records().MaxSequence())
	}
}

func TestRecordsAcceptRepairsMissingRetainedBytes(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	cfg, err := TestConfig(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	message := loadExample(t, 0)
	id := "fy1:4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f"
	a := Acceptance{MessageID: id, Bytes: message, IntentDigest: sha256.Sum256([]byte("intent")), ReceiptID: "receipt", IssuedAt: now}
	if _, err := s.Records().Accept(a); err != nil {
		t.Fatal(err)
	}
	if err := s.Store().Update(func(tx store.Tx) error { return tx.Delete("provider.message_bytes", id) }); err != nil {
		t.Fatal(err)
	}
	accepted, err := s.Records().Accept(a)
	if err != nil || accepted.Created || accepted.Sequence != 1 {
		t.Fatalf("repair retry = %#v, %v", accepted, err)
	}
	got, state, err := s.Records().Lookup(id)
	if err != nil || state != RecordRetained || string(got) != string(message) {
		t.Fatalf("repaired lookup state=%q bytes=%d err=%v", state, len(got), err)
	}
}

func TestRecordsRestartKeepsBytesAndSequence(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	root := t.TempDir()
	cfg, err := TestConfig(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = filepath.Join(root, "data", "provider")
	cfg.ContentStore = nil
	cfg.ControlStore = nil
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	firstBytes := loadExample(t, 0)
	firstID := "fy1:4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f"
	a := Acceptance{MessageID: firstID, Bytes: firstBytes, IntentDigest: sha256.Sum256([]byte("first")), ReceiptID: "r1", IssuedAt: now}
	if _, err = s.Records().Accept(a); err != nil {
		t.Fatal(err)
	}
	secondID := "fy1:49111e1567a90f12f8c56575cb7f4f42a49edd083ddfcaeb0f97af24bc198e72"
	secondBytes := loadExample(t, 1)
	secondAcceptance := Acceptance{MessageID: secondID, Bytes: secondBytes, IntentDigest: sha256.Sum256([]byte("second")), ReceiptID: "r2", IssuedAt: now}
	if accepted, e := s.Records().Accept(secondAcceptance); e != nil || accepted.Sequence != 2 {
		t.Fatalf("second record = %#v, %v", accepted, e)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	cfg, err = TestConfig(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = filepath.Join(root, "data", "provider")
	cfg.ContentStore = nil
	cfg.ControlStore = nil
	s, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if len(s.Declarations()) != 1 {
		t.Fatalf("retention declarations after restart = %d", len(s.Declarations()))
	}
	got, state, err := s.Records().Lookup(firstID)
	if err != nil || state != RecordRetained || string(got) != string(firstBytes) {
		t.Fatalf("restarted record state=%q err=%v", state, err)
	}
	got, state, err = s.Records().Lookup(secondID)
	if err != nil || state != RecordRetained || string(got) != string(secondBytes) {
		t.Fatalf("second restarted record state=%q err=%v", state, err)
	}
	third := loadExample(t, 2)
	thirdID := "fy1:c33c9c7dad2904158a564e8bf1a3af66b7516ebfe58f8da0a40d77b7ec35538b"
	b := Acceptance{MessageID: thirdID, Bytes: third, IntentDigest: sha256.Sum256([]byte("third")), ReceiptID: "r3", IssuedAt: now}
	accepted, err := s.Records().Accept(b)
	if err != nil || accepted.Sequence != 3 || !accepted.Created {
		t.Fatalf("third after restart = %#v, %v", accepted, err)
	}
}

type failingStore struct {
	store.Store
	fail bool
}

func (f *failingStore) Update(fn func(store.Tx) error) error {
	if f.fail {
		return fmt.Errorf("injected commit failure")
	}
	return f.Store.Update(fn)
}

func TestRecordsStorageFailureDoesNotIndex(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	cfg, err := TestConfig(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	content := &failingStore{Store: store.NewMemory()}
	cfg.ContentStore = content
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	content.fail = true
	id := "fy1:4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f"
	a := Acceptance{MessageID: id, Bytes: loadExample(t, 0), IntentDigest: sha256.Sum256([]byte("x")), IssuedAt: now}
	if _, err = s.Records().Accept(a); err == nil {
		t.Fatal("failed commit returned success")
	}
	_, state, err := s.Records().Lookup(id)
	if err != nil || state != RecordUnknown {
		t.Fatalf("failed accept lookup = %q %v", state, err)
	}
	if s.Records().MaxSequence() != 0 {
		t.Fatalf("sequence advanced to %d", s.Records().MaxSequence())
	}
}

func loadExample(t *testing.T, n int) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/post-version-chain.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err = json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	if n < 0 || n >= len(doc.Messages) {
		t.Fatalf("example index %d out of range", n)
	}
	return append([]byte(nil), doc.Messages[n]...)
}
