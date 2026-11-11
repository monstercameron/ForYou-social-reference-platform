package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func rdPagerForBodies(t *testing.T, first, second string, now time.Time) *Pager {
	t.Helper()
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		if calls == 1 {
			_, _ = w.Write([]byte(first))
			return
		}
		_, _ = w.Write([]byte(second))
	}))
	t.Cleanup(server.Close)
	client := rdTestClient(t, server.URL, Options{AllowLoopbackHTTP: true, Now: func() time.Time { return now }})
	pager, err := NewPager(client, MessagesRoute(), ListQuery{})
	if err != nil {
		t.Fatal(err)
	}
	return pager
}

func rdCommentMessage(t *testing.T, index int) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/rd/post-comment-thread.json")
	if err != nil {
		t.Fatal(err)
	}
	var example struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(data, &example); err != nil {
		t.Fatal(err)
	}
	return example.Messages[index]
}

func TestPager(t *testing.T) {
	t.Run("empty page with cursor continues", func(t *testing.T) {
		message := rdCommentMessage(t, 2)
		cursor := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		first := `{"expires_at":"2026-11-11T00:15:00Z","generated_at":"2026-11-11T00:00:00Z","messages":[],"next_cursor":"` + cursor + `"}`
		second := `{"expires_at":"2026-11-11T00:15:00Z","generated_at":"2026-11-11T00:00:00Z","messages":[` + string(message) + `],"next_cursor":null}`
		pager := rdPagerForBodies(t, first, second, time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC))
		page1, err := pager.Next(context.Background())
		if err != nil || page1.Done || len(page1.Messages) != 0 {
			t.Fatalf("first page=(%+v,%v); want empty continuing page", page1, err)
		}
		page2, err := pager.Next(context.Background())
		if err != nil || !page2.Done || len(page2.Messages) != 1 {
			t.Fatalf("second page=(%+v,%v); want final message", page2, err)
		}
	})
	t.Run("rejects out of order records across pages", func(t *testing.T) {
		older := rdCommentMessage(t, 0)
		newer := rdCommentMessage(t, 1)
		cursor := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		first := `{"expires_at":"2026-11-11T00:15:00Z","generated_at":"2026-11-11T00:00:00Z","messages":[` + string(older) + `],"next_cursor":"` + cursor + `"}`
		second := `{"expires_at":"2026-11-11T00:15:00Z","generated_at":"2026-11-11T00:00:00Z","messages":[` + string(newer) + `],"next_cursor":null}`
		pager := rdPagerForBodies(t, first, second, time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC))
		if _, err := pager.Next(context.Background()); err != nil {
			t.Fatal(err)
		}
		_, err := pager.Next(context.Background())
		if Classify(err) != SignalProtocol {
			t.Fatalf("Classify(error)=%s; want protocol", Classify(err))
		}
	})
	t.Run("rejects a fourteen minute snapshot", func(t *testing.T) {
		body := `{"expires_at":"2026-11-11T00:14:00Z","generated_at":"2026-11-11T00:00:00Z","messages":[],"next_cursor":null}`
		pager := rdPagerForBodies(t, body, body, time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC))
		_, err := pager.Next(context.Background())
		if Classify(err) != SignalProtocol {
			t.Fatalf("Classify(error)=%s; want protocol", Classify(err))
		}
	})
	t.Run("rejects generated time sixty one seconds ahead", func(t *testing.T) {
		body := `{"expires_at":"2026-11-11T00:16:01Z","generated_at":"2026-11-11T00:01:01Z","messages":[],"next_cursor":null}`
		pager := rdPagerForBodies(t, body, body, time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC))
		_, err := pager.Next(context.Background())
		if Classify(err) != SignalProtocol {
			t.Fatalf("Classify(error)=%s; want protocol", Classify(err))
		}
	})
	t.Run("maps invalid cursor to snapshot lost", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"status":"invalid_cursor"}`))
		}))
		defer server.Close()
		client := rdTestClient(t, server.URL, Options{AllowLoopbackHTTP: true})
		pager, err := NewPager(client, MessagesRoute(), ListQuery{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = pager.Next(context.Background())
		if !errors.Is(err, ErrSnapshotLost) {
			t.Fatalf("Next() error=%v; want ErrSnapshotLost", err)
		}
	})
	t.Run("paginates a comment thread snapshot", func(t *testing.T) {
		first := rdCommentMessage(t, 2)
		second := rdCommentMessage(t, 1)
		cursor := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.Header().Set("Content-Type", "application/json")
			if calls == 1 {
				_, _ = w.Write([]byte(`{"expires_at":"2026-11-11T00:15:00Z","generated_at":"2026-11-11T00:00:00Z","messages":[` + string(first) + `],"next_cursor":"` + cursor + `"}`))
				return
			}
			_, _ = w.Write([]byte(`{"expires_at":"2026-11-11T00:15:00Z","generated_at":"2026-11-11T00:00:00Z","messages":[` + string(second) + `],"next_cursor":null}`))
		}))
		defer server.Close()
		client := rdTestClient(t, server.URL, Options{AllowLoopbackHTTP: true, Now: func() time.Time { return time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) }})
		pager, err := NewPager(client, MessagesRoute(), ListQuery{})
		if err != nil {
			t.Fatal(err)
		}
		page1, err := pager.Next(context.Background())
		if err != nil || page1.Done || len(page1.Messages) != 1 {
			t.Fatalf("first page=(%+v,%v); want one continuing page", page1, err)
		}
		page2, err := pager.Next(context.Background())
		if err != nil || !page2.Done || len(page2.Messages) != 1 {
			t.Fatalf("second page=(%+v,%v); want one final page", page2, err)
		}
	})
	t.Run("accepts a complete one-page snapshot", func(t *testing.T) {
		message := rdVersionMessage(t, 0)
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		body := `{"expires_at":"2026-11-11T00:15:00Z","generated_at":"2026-11-11T00:00:00Z","messages":` + "[" + string(message) + `],"next_cursor":null}`
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(body))
		}))
		defer server.Close()
		client := rdTestClient(t, server.URL, Options{AllowLoopbackHTTP: true, Now: func() time.Time { return now }})
		pager, err := NewPager(client, MessagesRoute(), ListQuery{})
		if err != nil {
			t.Fatal(err)
		}
		page, err := pager.Next(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if !page.Done || len(page.Messages) != 1 {
			t.Fatalf("page Done=%t messages=%d", page.Done, len(page.Messages))
		}
		done, err := pager.Next(context.Background())
		if err != nil || !done.Done {
			t.Fatalf("next after completion=(%+v,%v)", done, err)
		}
	})
	t.Run("rejects a changed snapshot", func(t *testing.T) {
		cursor := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			generated := "2026-11-11T00:00:00Z"
			expires := "2026-11-11T00:15:00Z"
			if calls == 2 {
				generated = "2026-11-11T00:01:00Z"
				expires = "2026-11-11T00:16:00Z"
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"expires_at":"` + expires + `","generated_at":"` + generated + `","messages":[],"next_cursor":"` + cursor + `"}`))
		}))
		defer server.Close()
		client := rdTestClient(t, server.URL, Options{AllowLoopbackHTTP: true, Now: func() time.Time { return time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) }})
		pager, err := NewPager(client, MessagesRoute(), ListQuery{})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pager.Next(context.Background()); err != nil {
			t.Fatal(err)
		}
		_, err = pager.Next(context.Background())
		if Classify(err) != SignalProtocol {
			t.Fatalf("Classify(error)=%s; want protocol", Classify(err))
		}
	})
	t.Run("rejects duplicate message IDs across pages", func(t *testing.T) {
		message := rdCommentMessage(t, 2)
		cursor := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		first := `{"expires_at":"2026-11-11T00:15:00Z","generated_at":"2026-11-11T00:00:00Z","messages":[` + string(message) + `],"next_cursor":"` + cursor + `"}`
		second := `{"expires_at":"2026-11-11T00:15:00Z","generated_at":"2026-11-11T00:00:00Z","messages":[` + string(message) + `],"next_cursor":null}`
		pager := rdPagerForBodies(t, first, second, time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC))
		if _, err := pager.Next(context.Background()); err != nil {
			t.Fatal(err)
		}
		_, err := pager.Next(context.Background())
		if Classify(err) != SignalProtocol {
			t.Fatalf("Classify(error)=%s; want protocol", Classify(err))
		}
	})
	t.Run("rejects repeated cursor", func(t *testing.T) {
		cursor := "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
		body := `{"expires_at":"2026-11-11T00:15:00Z","generated_at":"2026-11-11T00:00:00Z","messages":[],"next_cursor":"` + cursor + `"}`
		pager := rdPagerForBodies(t, body, body, time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC))
		if _, err := pager.Next(context.Background()); err != nil {
			t.Fatal(err)
		}
		_, err := pager.Next(context.Background())
		if Classify(err) != SignalProtocol {
			t.Fatalf("Classify(error)=%s; want protocol", Classify(err))
		}
	})
	t.Run("rejects replies with a different parent", func(t *testing.T) {
		parent := rdCommentMessage(t, 0)
		var envelope struct {
			MessageID string `json:"message_id"`
		}
		if err := json.Unmarshal(parent, &envelope); err != nil {
			t.Fatal(err)
		}
		reply, err := RepliesRoute(envelope.MessageID)
		if err != nil {
			t.Fatal(err)
		}
		message := rdCommentMessage(t, 2)
		body := `{"expires_at":"2026-11-11T00:15:00Z","generated_at":"2026-11-11T00:00:00Z","messages":[` + string(message) + `],"next_cursor":null}`
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}))
		defer server.Close()
		client := rdTestClient(t, server.URL, Options{AllowLoopbackHTTP: true, Now: func() time.Time { return time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) }})
		pager, err := NewPager(client, reply, ListQuery{})
		if err != nil {
			t.Fatal(err)
		}
		_, err = pager.Next(context.Background())
		if Classify(err) != SignalProtocol {
			t.Fatalf("Classify(error)=%s; want protocol", Classify(err))
		}
	})
	t.Run("rejects page above requested limit", func(t *testing.T) {
		first := rdCommentMessage(t, 2)
		second := rdCommentMessage(t, 1)
		body := `{"expires_at":"2026-11-11T00:15:00Z","generated_at":"2026-11-11T00:00:00Z","messages":[` + string(first) + `,` + string(second) + `],"next_cursor":null}`
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(body))
		}))
		defer server.Close()
		client := rdTestClient(t, server.URL, Options{AllowLoopbackHTTP: true, Now: func() time.Time { return time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC) }})
		pager, err := NewPager(client, MessagesRoute(), ListQuery{Limit: 1})
		if err != nil {
			t.Fatal(err)
		}
		_, err = pager.Next(context.Background())
		if Classify(err) != SignalProtocol {
			t.Fatalf("Classify(error)=%s; want protocol", Classify(err))
		}
	})
}
