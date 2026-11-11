package monitor

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"foryou.local/foryou/internal/committee"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/store"
)

func TestMemberDoPersistsAcrossRestart(t *testing.T) {
	root := t.TempDir()
	first := testService(t, root, &testChain{})
	member := first.Members()[0]
	if member.Index() != 0 || member.DID() != keys.DIDOf(devnet.MonitorKey(0)) {
		t.Fatalf("member accessors = (%d, %q)", member.Index(), member.DID())
	}
	stamp := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := member.Do(func(state *committee.Monitor) error { state.Observe("claim", stamp); return nil }); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second := testService(t, root, &testChain{})
	defer second.Close()
	if err := second.Members()[0].Do(func(state *committee.Monitor) error {
		if got := state.Observe("claim", stamp.Add(24*time.Hour)); !got.Equal(stamp) {
			t.Errorf("restored first-seen = %v; want %v", got, stamp)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestMemberDoDiscardsChangeWhenJournalFails(t *testing.T) {
	svc := testService(t, t.TempDir(), &testChain{})
	defer svc.Close()
	member := svc.Members()[0]
	stamp := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := member.db.Close(); err != nil {
		t.Fatal(err)
	}
	err := member.Do(func(state *committee.Monitor) error { state.Observe("claim", stamp); return nil })
	if !errors.Is(err, store.ErrClosed) {
		t.Fatalf("Do error = %v; want closed journal", err)
	}
	if err := member.Do(func(state *committee.Monitor) error {
		if got := state.Observe("claim", stamp.Add(time.Hour)); !got.Equal(stamp.Add(time.Hour)) {
			t.Errorf("failed change persisted: first-seen = %v", got)
		}
		return nil
	}); !errors.Is(err, store.ErrClosed) {
		t.Errorf("second Do error = %v; want closed journal", err)
	}
}

func TestMemberViewDoesNotJournalOrPersistChanges(t *testing.T) {
	root := t.TempDir()
	svc := testService(t, root, &testChain{})
	defer svc.Close()
	member := svc.Members()[0]
	journal := filepath.Join(root, "0", "journal.jsonl")
	stat, err := os.Stat(journal)
	if err != nil {
		t.Fatal(err)
	}
	before := stat.Size()
	viewStamp := time.Date(2027, 1, 2, 3, 4, 5, 0, time.UTC)
	if err := member.View(func(state *committee.Monitor) error {
		state.Observe("view-only", viewStamp)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stat, err = os.Stat(journal)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Size() != before {
		t.Errorf("journal size after View = %d; want unchanged size %d", stat.Size(), before)
	}
	writeStamp := viewStamp.Add(time.Hour)
	if err := member.Do(func(state *committee.Monitor) error {
		if got := state.Observe("view-only", writeStamp); !got.Equal(writeStamp) {
			t.Errorf("Do observed view-only state at %v; want %v", got, writeStamp)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	stat, err = os.Stat(journal)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Size() <= before {
		t.Errorf("journal size after state-changing Do = %d; want greater than %d", stat.Size(), before)
	}
}

func TestMemberViewReturnsCallbackErrorUnchanged(t *testing.T) {
	svc := testService(t, t.TempDir(), &testChain{})
	defer svc.Close()
	want := errors.New("view failed")
	err := svc.Members()[0].View(func(*committee.Monitor) error { return want })
	if err != want {
		t.Errorf("View error = %v; want identical error %v", err, want)
	}
}
