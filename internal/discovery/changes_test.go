package discovery

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
)

func TestDiscoveryPageEncoding(t *testing.T) {
	t.Run("exact JCS bytes and canonical cross-check", func(t *testing.T) {
		generated := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		message := []byte(`{"message_id":"fy1:abc"}`)
		got := EncodeDiscoveryPage(generated, generated.Add(15*time.Minute), [][]byte{message}, "")
		want := []byte(`{"expires_at":"2026-11-11T00:15:00Z","generated_at":"2026-11-11T00:00:00Z","messages":[{"message_id":"fy1:abc"}],"next_cursor":null}`)
		if !bytes.Equal(got, want) {
			t.Fatalf("EncodeDiscoveryPage() = %s; want %s", got, want)
		}
		crossCheckCanonical(t, got)
	})
}

func TestChangesPageEncoding(t *testing.T) {
	t.Run("exact JCS bytes and canonical cross-check", func(t *testing.T) {
		message := []byte(`{"message_id":"fy1:abc"}`)
		got := EncodeChangesPage("did:key:z6MkExample", []Candidate{{Sequence: 5, Bytes: message, Visible: true}}, 5, false)
		want := []byte(`{"items":[{"message":{"message_id":"fy1:abc"},"sequence":5}],"more":false,"next_after":5,"provider_id":"did:key:z6MkExample"}`)
		if !bytes.Equal(got, want) {
			t.Fatalf("EncodeChangesPage() = %s; want %s", got, want)
		}
		crossCheckCanonical(t, got)
	})
}

func TestBuildChanges(t *testing.T) {
	message := []byte(`{"message_id":"fy1:abc"}`)
	check := func(t *testing.T, got []byte, items int, nextAfter uint64, more bool) {
		t.Helper()
		var page struct {
			Items     []json.RawMessage `json:"items"`
			NextAfter uint64            `json:"next_after"`
			More      bool              `json:"more"`
		}
		if err := json.Unmarshal(got, &page); err != nil {
			t.Fatalf("json.Unmarshal() error = %v", err)
		}
		if len(page.Items) != items || page.NextAfter != nextAfter || page.More != more {
			t.Fatalf("page has %d items, next_after=%d, more=%t; want %d, %d, %t", len(page.Items), page.NextAfter, page.More, items, nextAfter, more)
		}
	}
	t.Run("visible sequences and complete tail", func(t *testing.T) {
		got := BuildChanges("did:key:z6MkExample", 4, 10, 9, []Candidate{{Sequence: 5, Bytes: message, Visible: true}, {Sequence: 9, Bytes: message, Visible: true}}, true)
		check(t, got, 2, 9, false)
	})
	t.Run("empty stream", func(t *testing.T) {
		got := BuildChanges("did:key:z6MkExample", 0, 10, 0, nil, true)
		check(t, got, 0, 0, false)
	})
	t.Run("hidden candidates advance without disclosure", func(t *testing.T) {
		got := BuildChanges("did:key:z6MkExample", 12, 10, 50, []Candidate{{Sequence: 13}, {Sequence: 40}}, false)
		check(t, got, 0, 40, true)
	})
	t.Run("after greater than assignment stays fixed", func(t *testing.T) {
		got := BuildChanges("did:key:z6MkExample", 60, 10, 50, nil, true)
		check(t, got, 0, 60, false)
	})
	t.Run("limit leaves later sequence for next page", func(t *testing.T) {
		got := BuildChanges("did:key:z6MkExample", 4, 1, 9, []Candidate{{Sequence: 5, Bytes: message, Visible: true}, {Sequence: 9, Bytes: message, Visible: true}}, true)
		check(t, got, 1, 5, true)
	})
	t.Run("byte budget reserves the final cursor and more field", func(t *testing.T) {
		providerID := "did:key:z6MkExample"
		maxAssigned := uint64(9007199254740991)
		oldTrialSize := len(EncodeChangesPage(providerID, []Candidate{{Sequence: 5}, {Sequence: 9}}, 9, true))
		messageSize := MaxPageBytes - 1 - oldTrialSize
		firstMessageSize := 1 << 20
		secondMessageSize := messageSize - firstMessageSize
		makeMessage := func(size int) []byte {
			return []byte(`{"data":"` + strings.Repeat("x", size-11) + `"}`)
		}
		firstMessage := makeMessage(firstMessageSize)
		secondMessage := makeMessage(secondMessageSize)
		got := BuildChanges(providerID, 4, 10, maxAssigned, []Candidate{
			{Sequence: 5, Bytes: firstMessage, Visible: true},
			{Sequence: 9, Bytes: secondMessage, Visible: true},
		}, true)
		if len(got) > MaxPageBytes {
			t.Fatalf("BuildChanges() size = %d; want at most %d", len(got), MaxPageBytes)
		}
		check(t, got, 1, 5, true)
	})
}

func crossCheckCanonical(t *testing.T, encoded []byte) {
	t.Helper()
	value, err := canon.Parse(encoded, canon.ListingLimits)
	if err != nil {
		t.Fatalf("canon.Parse() error = %v", err)
	}
	canonical, err := canon.Marshal(value)
	if err != nil {
		t.Fatalf("canon.Marshal() error = %v", err)
	}
	if !bytes.Equal(encoded, canonical) {
		t.Fatalf("encoding differs from canonical JCS: got %s; canonical %s", encoded, canonical)
	}
}
