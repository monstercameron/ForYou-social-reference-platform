package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJournalTransactions(t *testing.T) {
	t.Run("failed callback writes no journal line", func(t *testing.T) {
		dir := t.TempDir()
		db, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		failure := errors.New("rollback")
		if err := db.Update(func(tx Tx) error {
			if err := tx.Put("test.items", "a", []byte("one")); err != nil {
				return err
			}
			if err := tx.Put("test.items", "b", []byte("two")); err != nil {
				return err
			}
			return failure
		}); !errors.Is(err, failure) {
			t.Fatalf("Update error = %v", err)
		}
		data, err := os.ReadFile(filepath.Join(dir, "journal.jsonl"))
		if err != nil || len(data) != 0 {
			t.Errorf("journal = %q, %v; want empty", data, err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("reopen replays committed transactions and exact bytes", func(t *testing.T) {
		dir := t.TempDir()
		db, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		value := []byte("{\"text\":\"<b>&é</b>\"}")
		if err := db.Update(func(tx Tx) error { return tx.Put("test.bytes", "message", value) }); err != nil {
			t.Fatal(err)
		}
		if err := db.Update(func(tx Tx) error { return tx.Put("test.bytes", "second", []byte("two")) }); err != nil {
			t.Fatal(err)
		}
		if err := db.Update(func(tx Tx) error { return tx.Delete("test.bytes", "second") }); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		if err := reopened.View(func(tx Tx) error {
			got, ok, err := tx.Get("test.bytes", "message")
			if err != nil || !ok || string(got) != string(value) {
				t.Errorf("Get = %q, %t, %v", got, ok, err)
			}
			if _, ok, err := tx.Get("test.bytes", "second"); err != nil || ok {
				t.Errorf("deleted key ok=%t err=%v", ok, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("committed sequences continue after reopen", func(t *testing.T) {
		dir := t.TempDir()
		db, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Update(func(tx Tx) error {
			first, err := tx.NextSequence("test.counter")
			if first != 1 {
				t.Errorf("first sequence = %d; want 1", first)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.Update(func(tx Tx) error {
			second, err := tx.NextSequence("test.counter")
			if second != 2 {
				t.Errorf("second sequence = %d; want 2", second)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.Update(func(tx Tx) error {
			third, err := tx.NextSequence("test.counter")
			if third != 3 {
				t.Errorf("third sequence = %d; want 3", third)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		if err := reopened.Update(func(tx Tx) error {
			fourth, err := tx.NextSequence("test.counter")
			if fourth != 4 {
				t.Errorf("reopened sequence = %d; want 4", fourth)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("torn final line is discarded", func(t *testing.T) {
		dir := t.TempDir()
		db, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Update(func(tx Tx) error { return tx.Put("test.items", "a", []byte("kept")) }); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "journal.jsonl")
		original, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteString(`{"n":2,"ops":[{"op":"put"`); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		got, err := os.ReadFile(path)
		if err != nil || string(got) != string(original) {
			t.Errorf("journal = %q, %v; want prior complete data", got, err)
		}
		if err := reopened.View(func(tx Tx) error {
			value, ok, err := tx.Get("test.items", "a")
			if err != nil || !ok || string(value) != "kept" {
				t.Errorf("replayed value = %q, %t, %v", value, ok, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("middle corruption fails open", func(t *testing.T) {
		dir := t.TempDir()
		db, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Update(func(tx Tx) error { return tx.Put("test.items", "a", []byte("one")) }); err != nil {
			t.Fatal(err)
		}
		if err := db.Update(func(tx Tx) error { return tx.Put("test.items", "b", []byte("two")) }); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(dir, "journal.jsonl")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		changed := strings.Replace(string(data), `"k":"a"`, `"k":"z"`, 1)
		if changed == string(data) {
			t.Fatal("could not locate journal key to corrupt")
		}
		// This keeps the JSON valid so the checksum must detect the changed key.
		data = []byte(changed)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir); !errors.Is(err, ErrCorrupt) {
			t.Errorf("Open error = %v; want ErrCorrupt", err)
		}
	})
	t.Run("append failure rolls back and fails closed", func(t *testing.T) {
		db, err := Open(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Update(func(tx Tx) error { return tx.Put("test.items", "first", []byte("kept")) }); err != nil {
			t.Fatal(err)
		}
		if err := db.journal.file.Close(); err != nil {
			t.Fatal(err)
		}
		if err := db.Update(func(tx Tx) error { return tx.Put("test.items", "second", []byte("rejected")) }); err == nil {
			t.Fatal("Update after journal close returned nil")
		}
		if err := db.View(func(tx Tx) error {
			first, ok, err := tx.Get("test.items", "first")
			if err != nil || !ok || string(first) != "kept" {
				t.Errorf("first value = %q, %t, %v", first, ok, err)
			}
			if _, ok, err := tx.Get("test.items", "second"); err != nil || ok {
				t.Errorf("failed write visible: ok=%t err=%v", ok, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := db.Update(func(Tx) error { return nil }); !errors.Is(err, ErrFailed) {
			t.Errorf("later Update error = %v; want ErrFailed", err)
		}
	})
}

func TestCompact(t *testing.T) {
	t.Run("snapshot preserves values and sequences", func(t *testing.T) {
		dir := t.TempDir()
		db, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		var seq uint64
		if err := db.Update(func(tx Tx) error {
			if err := tx.Put("test.items", "value", []byte("persisted")); err != nil {
				return err
			}
			var err error
			seq, err = tx.NextSequence("test.counter")
			return err
		}); err != nil {
			t.Fatal(err)
		}
		if seq != 1 {
			t.Fatalf("first sequence = %d", seq)
		}
		if err := db.Compact(); err != nil {
			t.Fatal(err)
		}
		journalData, err := os.ReadFile(filepath.Join(dir, "journal.jsonl"))
		if err != nil || len(journalData) != 0 {
			t.Errorf("journal after compact = %q, %v", journalData, err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		reopened, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		if err := reopened.View(func(tx Tx) error {
			value, ok, err := tx.Get("test.items", "value")
			if err != nil || !ok || string(value) != "persisted" {
				t.Errorf("snapshot value = %q, %t, %v", value, ok, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := reopened.Update(func(tx Tx) error {
			got, err := tx.NextSequence("test.counter")
			if got != 2 {
				t.Errorf("next sequence = %d; want 2", got)
			}
			return err
		}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("pre-truncation journal records are skipped", func(t *testing.T) {
		dir := t.TempDir()
		db, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Update(func(tx Tx) error { return tx.Put("test.items", "value", []byte("same")) }); err != nil {
			t.Fatal(err)
		}
		originalJournal, err := os.ReadFile(filepath.Join(dir, "journal.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Compact(); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "journal.jsonl"), originalJournal, 0600); err != nil {
			t.Fatal(err)
		}
		reopened, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer reopened.Close()
		if err := reopened.View(func(tx Tx) error {
			value, ok, err := tx.Get("test.items", "value")
			if err != nil || !ok || string(value) != "same" {
				t.Errorf("replayed value = %q, %t, %v", value, ok, err)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	})
}

func TestJournalLocationAndClose(t *testing.T) {
	t.Run("data directory and close state", func(t *testing.T) {
		if got := DataDir(".local", "provider"); got != filepath.Join(".local", "data", "provider") {
			t.Errorf("DataDir = %q", got)
		}
		dir := t.TempDir()
		db, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if err := db.View(func(Tx) error { return nil }); !errors.Is(err, ErrClosed) {
			t.Errorf("View after close = %v", err)
		}
		if err := db.Update(func(Tx) error { return nil }); !errors.Is(err, ErrClosed) {
			t.Errorf("Update after close = %v", err)
		}
		if err := db.Close(); !errors.Is(err, ErrClosed) {
			t.Errorf("second Close = %v", err)
		}
	})
	t.Run("journal records are JSON lines", func(t *testing.T) {
		dir := t.TempDir()
		db, err := Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Update(func(tx Tx) error { return tx.Put("test.items", "empty", []byte{}) }); err != nil {
			t.Fatal(err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(filepath.Join(dir, "journal.jsonl"))
		if err != nil || !strings.HasSuffix(string(data), "\n") || !strings.Contains(string(data), `"v":""`) {
			t.Errorf("journal line = %q, %v", data, err)
		}
	})
}
