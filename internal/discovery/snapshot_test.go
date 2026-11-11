package discovery

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

type testVisibility struct {
	generation uint64
	visible    map[string]bool
	hiddenAt   map[string]uint64
	err        error
}

func (v *testVisibility) Generation() uint64 { return v.generation }

func (v *testVisibility) Visible(id string) (bool, uint64, error) {
	if v.err != nil {
		return false, 0, v.err
	}
	visible, ok := v.visible[id]
	if !ok {
		visible = true
	}
	return visible, v.hiddenAt[id], nil
}

func (v *testVisibility) restrict(id string) {
	v.generation++
	if v.visible == nil {
		v.visible = make(map[string]bool)
	}
	if v.hiddenAt == nil {
		v.hiddenAt = make(map[string]uint64)
	}
	v.visible[id] = false
	v.hiddenAt[id] = v.generation
}

func (v *testVisibility) restore(id string) {
	if v.visible == nil {
		v.visible = make(map[string]bool)
	}
	v.visible[id] = true
}

type pageResult struct {
	GeneratedAt string            `json:"generated_at"`
	ExpiresAt   string            `json:"expires_at"`
	Messages    []json.RawMessage `json:"messages"`
	NextCursor  *string           `json:"next_cursor"`
}

func testEntry(n int, size int) Entry {
	id := fmt.Sprintf("fy1:%064x", n)
	body := []byte(fmt.Sprintf(`{"message_id":%q}`, id))
	if size > len(body) {
		body = []byte(fmt.Sprintf(`{"message_id":%q,"data":"%s"}`, id, strings.Repeat("x", size-len(body)-10)))
	}
	return Entry{MessageID: id, Bytes: body}
}

func decodePage(t *testing.T, raw []byte) pageResult {
	t.Helper()
	var page pageResult
	if err := json.Unmarshal(raw, &page); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	return page
}

func cursorOf(t *testing.T, page pageResult) string {
	t.Helper()
	if page.NextCursor == nil {
		t.Fatal("page has no continuation cursor")
	}
	return *page.NextCursor
}

func newTestSnapshots(t *testing.T, limits SnapshotLimits, clock *time.Time) *Snapshots {
	t.Helper()
	randomBytes := strings.Repeat("a", 32) + strings.Repeat("b", 32) + strings.Repeat("c", 32) + strings.Repeat("d", 32)
	reader := bytes.NewReader([]byte(randomBytes))
	snapshots, err := NewSnapshots(limits, func() time.Time { return *clock }, reader)
	if err != nil {
		t.Fatalf("NewSnapshots() error = %v", err)
	}
	return snapshots
}

func TestSnapshotLimits(t *testing.T) {
	t.Run("zero values use defaults", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		snapshots := newTestSnapshots(t, SnapshotLimits{}, &now)
		if got, want := snapshots.Limits(), (SnapshotLimits{1024, 256 << 20, 15 * time.Minute, 1000}); got != want {
			t.Fatalf("Limits() = %+v; want %+v", got, want)
		}
	})
	t.Run("lifetime bounds", func(t *testing.T) {
		if err := (SnapshotLimits{Lifetime: 14*time.Minute + 59*time.Second}).Validate(); err == nil {
			t.Fatal("Validate() accepted 14m59s")
		}
		if err := (SnapshotLimits{Lifetime: 60*time.Minute + time.Second}).Validate(); err == nil {
			t.Fatal("Validate() accepted 60m1s")
		}
		if err := (SnapshotLimits{Lifetime: 15 * time.Minute}).Validate(); err != nil {
			t.Fatalf("Validate(15m) error = %v", err)
		}
		if err := (SnapshotLimits{Lifetime: time.Hour}).Validate(); err != nil {
			t.Fatalf("Validate(60m) error = %v", err)
		}
	})
	t.Run("fractional seconds rejected", func(t *testing.T) {
		if err := (SnapshotLimits{Lifetime: 15*time.Minute + time.Millisecond}).Validate(); err == nil {
			t.Fatal("Validate() accepted fractional seconds")
		}
	})
	t.Run("negative scan budget rejected", func(t *testing.T) {
		if err := (SnapshotLimits{ScanBudget: -1}).Validate(); err == nil {
			t.Fatal("Validate() accepted negative scan budget")
		}
	})
}

func TestSnapshotPagingAndReplay(t *testing.T) {
	t.Run("ordered pages deduplicate equal timestamps", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 500, time.FixedZone("offset", 3600))
		snapshots := newTestSnapshots(t, SnapshotLimits{}, &now)
		vis := &testVisibility{generation: 3}
		entries := []Entry{testEntry(1, 0), testEntry(2, 0), testEntry(3, 0), testEntry(4, 0), testEntry(4, 0), testEntry(5, 0), testEntry(6, 0), testEntry(7, 0)}
		first, err := snapshots.Start("route", 3, vis, func() ([]Entry, error) { return entries, nil })
		if err != nil {
			t.Fatalf("Start() error = %v", err)
		}
		page1 := decodePage(t, first)
		if got := string(page1.Messages[0]) + string(page1.Messages[1]) + string(page1.Messages[2]); got != string(entries[0].Bytes)+string(entries[1].Bytes)+string(entries[2].Bytes) {
			t.Fatalf("first page messages = %s; want first three entries", got)
		}
		secondRaw, err := snapshots.Continue("route", cursorOf(t, page1), 3, vis)
		if err != nil {
			t.Fatalf("Continue(second) error = %v", err)
		}
		page2 := decodePage(t, secondRaw)
		if got := string(page2.Messages[0]) + string(page2.Messages[1]) + string(page2.Messages[2]); got != string(entries[3].Bytes)+string(entries[5].Bytes)+string(entries[6].Bytes) {
			t.Fatalf("second page messages = %s; want entries 4, 5 and 6", got)
		}
		thirdRaw, err := snapshots.Continue("route", cursorOf(t, page2), 3, vis)
		if err != nil {
			t.Fatalf("Continue(third) error = %v", err)
		}
		page3 := decodePage(t, thirdRaw)
		if len(page3.Messages) != 1 || string(page3.Messages[0]) != string(entries[7].Bytes) || page3.NextCursor != nil {
			t.Fatalf("third page = %+v; want final entry and null cursor", page3)
		}
	})
	t.Run("late entries only enter a new snapshot", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		snapshots := newTestSnapshots(t, SnapshotLimits{}, &now)
		vis := &testVisibility{}
		backing := []Entry{testEntry(1, 0), testEntry(2, 0)}
		first, err := snapshots.Start("route", 1, vis, func() ([]Entry, error) { return backing, nil })
		if err != nil {
			t.Fatalf("Start() error = %v", err)
		}
		backing = append(backing, testEntry(3, 0))
		continuation, err := snapshots.Continue("route", cursorOf(t, decodePage(t, first)), 1, vis)
		if err != nil {
			t.Fatalf("Continue() error = %v", err)
		}
		if len(decodePage(t, continuation).Messages) != 1 {
			t.Fatal("continuation included a late entry")
		}
		fresh, err := snapshots.Start("other", 10, vis, func() ([]Entry, error) { return backing, nil })
		if err != nil {
			t.Fatalf("Start(new snapshot) error = %v", err)
		}
		if len(decodePage(t, fresh).Messages) != 3 {
			t.Fatal("new snapshot did not include the late entry")
		}
	})
	t.Run("continuation replay has fixed expiry", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		snapshots := newTestSnapshots(t, SnapshotLimits{}, &now)
		vis := &testVisibility{}
		first, err := snapshots.Start("route", 1, vis, func() ([]Entry, error) { return []Entry{testEntry(1, 0), testEntry(2, 0)}, nil })
		if err != nil {
			t.Fatalf("Start() error = %v", err)
		}
		cursor := cursorOf(t, decodePage(t, first))
		one, err := snapshots.Continue("route", cursor, 1, vis)
		if err != nil {
			t.Fatalf("Continue() error = %v", err)
		}
		two, err := snapshots.Continue("route", cursor, 1, vis)
		if err != nil || !bytes.Equal(one, two) {
			t.Fatalf("replayed Continue() = %s, %v; want identical bytes", two, err)
		}
		now = now.Add(15 * time.Minute)
		if _, err := snapshots.Continue("route", cursor, 1, vis); !errors.Is(err, ErrUnknownCursor) {
			t.Fatalf("Continue(at expiry) error = %v; want ErrUnknownCursor", err)
		}
	})
}

func TestSnapshotRestrictionsAndErrors(t *testing.T) {
	t.Run("suppression persists through restoration and empty progress", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		snapshots := newTestSnapshots(t, SnapshotLimits{ScanBudget: 1}, &now)
		second := testEntry(2, 0)
		third := testEntry(3, 0)
		vis := &testVisibility{}
		first, err := snapshots.Start("route", 1, vis, func() ([]Entry, error) {
			return []Entry{testEntry(1, 0), second, third, testEntry(4, 0)}, nil
		})
		if err != nil {
			t.Fatalf("Start() error = %v", err)
		}
		page1 := decodePage(t, first)
		vis.restrict(second.MessageID)
		vis.restrict(third.MessageID)
		secondRaw, err := snapshots.Continue("route", cursorOf(t, page1), 1, vis)
		if err != nil {
			t.Fatalf("Continue(second) error = %v", err)
		}
		page2 := decodePage(t, secondRaw)
		if len(page2.Messages) != 0 || page2.NextCursor == nil || *page2.NextCursor == *page1.NextCursor {
			t.Fatalf("empty progress page = %+v; want a different continuation cursor", page2)
		}
		vis.restore(second.MessageID)
		replay, err := snapshots.Continue("route", *page1.NextCursor, 1, vis)
		if err != nil || len(decodePage(t, replay).Messages) != 0 {
			t.Fatalf("replayed suppressed page = %s, %v; want empty", replay, err)
		}
		tailProgress, err := snapshots.Continue("route", *page2.NextCursor, 1, vis)
		if err != nil {
			t.Fatalf("Continue(tail) error = %v", err)
		}
		if len(decodePage(t, tailProgress).Messages) != 0 || decodePage(t, tailProgress).NextCursor == nil {
			t.Fatalf("bounded tail progress = %s; want empty nonterminal page", tailProgress)
		}
		// A scan budget of one adds an empty advancing page before entry 4.
		tail, err := snapshots.Continue("route", *decodePage(t, tailProgress).NextCursor, 1, vis)
		if err != nil {
			t.Fatalf("Continue(final) error = %v", err)
		}
		if got := string(decodePage(t, tail).Messages[0]); got != string(testEntry(4, 0).Bytes) || decodePage(t, tail).NextCursor != nil {
			t.Fatalf("final page = %s; want entry 4 and null cursor", tail)
		}
	})
	t.Run("restriction restored before first scan is suppressed", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		snapshots := newTestSnapshots(t, SnapshotLimits{}, &now)
		second := testEntry(2, 0)
		third := testEntry(3, 0)
		vis := &testVisibility{}
		first, err := snapshots.Start("route", 1, vis, func() ([]Entry, error) { return []Entry{testEntry(1, 0), second, third}, nil })
		if err != nil {
			t.Fatalf("Start() error = %v", err)
		}
		firstPage := decodePage(t, first)
		if len(firstPage.Messages) != 1 || !bytes.Equal(firstPage.Messages[0], testEntry(1, 0).Bytes) {
			t.Fatalf("initial page = %+v; want entry 1", firstPage)
		}
		vis.restrict(second.MessageID)
		vis.restore(second.MessageID)
		continuation, err := snapshots.Continue("route", cursorOf(t, firstPage), 1, vis)
		if err != nil {
			t.Fatalf("Continue() error = %v", err)
		}
		continuationMessages := decodePage(t, continuation).Messages
		if len(continuationMessages) != 1 || !bytes.Equal(continuationMessages[0], third.Bytes) {
			t.Fatalf("continuation messages = %q; want entry 3 only", continuationMessages)
		}
		fresh, err := snapshots.Start("fresh", 10, vis, func() ([]Entry, error) { return []Entry{testEntry(1, 0), second, third}, nil })
		if err != nil {
			t.Fatalf("new snapshot error = %v", err)
		}
		freshMessages := decodePage(t, fresh).Messages
		if len(freshMessages) != 3 || !bytes.Equal(freshMessages[1], second.Bytes) {
			t.Fatalf("new snapshot messages = %q; want entries 1, 2 and 3", freshMessages)
		}
	})
	t.Run("binding and manager isolate cursors", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		snapshots := newTestSnapshots(t, SnapshotLimits{}, &now)
		other := newTestSnapshots(t, SnapshotLimits{}, &now)
		vis := &testVisibility{}
		first, err := snapshots.Start("route", 1, vis, func() ([]Entry, error) { return []Entry{testEntry(1, 0), testEntry(2, 0)}, nil })
		if err != nil {
			t.Fatalf("Start() error = %v", err)
		}
		cursor := cursorOf(t, decodePage(t, first))
		if _, err := snapshots.Continue("different", cursor, 1, vis); !errors.Is(err, ErrUnknownCursor) {
			t.Fatalf("Continue(mismatched binding) error = %v", err)
		}
		if _, err := snapshots.Continue("route", cursor, 2, vis); !errors.Is(err, ErrUnknownCursor) {
			t.Fatalf("Continue(mismatched limit) error = %v; want ErrUnknownCursor", err)
		}
		if _, err := other.Continue("route", cursor, 1, vis); !errors.Is(err, ErrUnknownCursor) {
			t.Fatalf("Continue(other manager) error = %v", err)
		}
	})
	t.Run("visibility error fails closed", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		snapshots := newTestSnapshots(t, SnapshotLimits{}, &now)
		_, err := snapshots.Start("route", 1, &testVisibility{err: errors.New("visibility unavailable")}, func() ([]Entry, error) { return []Entry{testEntry(1, 0)}, nil })
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Start() error = %v; want ErrUnavailable", err)
		}
	})
	t.Run("admission happens before produce", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		snapshots := newTestSnapshots(t, SnapshotLimits{MaxSnapshots: 1}, &now)
		vis := &testVisibility{}
		_, err := snapshots.Start("one", 1, vis, func() ([]Entry, error) { return []Entry{testEntry(1, 0)}, nil })
		if err != nil {
			t.Fatalf("first Start() error = %v", err)
		}
		called := false
		_, err = snapshots.Start("two", 1, vis, func() ([]Entry, error) { called = true; return nil, nil })
		if !errors.Is(err, ErrBusy) || called {
			t.Fatalf("second Start() = %v, produce called %t; want ErrBusy and false", err, called)
		}
	})
	t.Run("byte admission refuses without retaining partial state", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		snapshots := newTestSnapshots(t, SnapshotLimits{MaxBytes: 100}, &now)
		_, err := snapshots.Start("large", 10, &testVisibility{}, func() ([]Entry, error) {
			return []Entry{testEntry(1, 0), testEntry(2, 0)}, nil
		})
		if !errors.Is(err, ErrBusy) {
			t.Fatalf("Start(large) error = %v; want ErrBusy", err)
		}
		small, err := snapshots.Start("small", 10, &testVisibility{}, func() ([]Entry, error) {
			return []Entry{testEntry(1, 0)}, nil
		})
		if err != nil || len(decodePage(t, small).Messages) != 1 {
			t.Fatalf("Start(small) = %s, %v; want successful one-entry snapshot", small, err)
		}
	})
	t.Run("maximum messages defer by byte budget", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		snapshots := newTestSnapshots(t, SnapshotLimits{}, &now)
		entries := []Entry{testEntry(1, 1<<20), testEntry(2, 1<<20), testEntry(3, 1<<20)}
		first, err := snapshots.Start("route", 100, &testVisibility{}, func() ([]Entry, error) { return entries, nil })
		if err != nil {
			t.Fatalf("Start() error = %v", err)
		}
		if len(first) > MaxPageBytes || len(decodePage(t, first).Messages) != 1 {
			t.Fatalf("first page size=%d messages=%d", len(first), len(decodePage(t, first).Messages))
		}
		second, err := snapshots.Continue("route", cursorOf(t, decodePage(t, first)), 100, &testVisibility{})
		if err != nil || len(second) > MaxPageBytes || len(decodePage(t, second).Messages) != 1 {
			t.Fatalf("second page size=%d, error=%v", len(second), err)
		}
		third, err := snapshots.Continue("route", cursorOf(t, decodePage(t, second)), 100, &testVisibility{})
		if err != nil || len(third) > MaxPageBytes || len(decodePage(t, third).Messages) != 1 || decodePage(t, third).NextCursor != nil {
			t.Fatalf("third page size=%d, error=%v", len(third), err)
		}
	})
}
