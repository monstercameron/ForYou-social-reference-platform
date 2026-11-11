package replica

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/message"
	"foryou.local/foryou/internal/wire"
)

type syResponse struct {
	status int
	body   []byte
	header http.Header
}

type syProvider struct {
	server   *httptest.Server
	mu       sync.Mutex
	requests []string
	respond  func(int, *http.Request) syResponse
}

func syStartProvider(t *testing.T, respond func(int, *http.Request) syResponse) *syProvider {
	t.Helper()
	provider := &syProvider{respond: respond}
	provider.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		provider.mu.Lock()
		index := len(provider.requests)
		provider.requests = append(provider.requests, r.URL.RequestURI())
		provider.mu.Unlock()
		response := provider.respond(index, r)
		w.Header().Set("Content-Type", "application/json")
		if location := response.header.Get("Location"); location != "" {
			w.Header().Set("Location", location)
		}
		w.WriteHeader(response.status)
		_, _ = w.Write(response.body)
	}))
	t.Cleanup(provider.server.Close)
	return provider
}

func (p *syProvider) requestPaths() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.requests...)
}

func syLoadMessages(t *testing.T) []wire.PostMessage {
	t.Helper()
	raw, err := os.ReadFile("testdata/post-version-chain.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Messages []wire.PostMessage `json:"messages"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil || len(fixture.Messages) != 5 {
		t.Fatalf("decode five-message fixture: %v", err)
	}
	return fixture.Messages
}

func syPage(t *testing.T, providerID string, next int64, more bool, items ...wire.ChangesPageItem) []byte {
	t.Helper()
	pageItems := append([]wire.ChangesPageItem{}, items...)
	body, err := canon.Encode(wire.ChangesPage{ProviderID: providerID, Items: pageItems, NextAfter: next, More: more})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func syNewSyncService(t *testing.T, origin, providerID string, now *time.Time) *Service {
	t.Helper()
	s, err := New(Config{Key: devnet.ReplicaKey(), Sources: []Source{{Origin: origin, ProviderID: providerID}}, Dev: true, Now: func() time.Time { return *now }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func syRunSync(t *testing.T, s *Service) {
	t.Helper()
	if err := s.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func sySeed(seed []byte) keys.PrivateKey {
	key, err := keys.NewKeyFromSeed(seed)
	if err != nil {
		panic(err)
	}
	return key
}

func syProviderKey() keys.PrivateKey {
	return sySeed([]byte{32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63})
}

func syResign(t *testing.T, original wire.PostMessage, mediaURL string, foreignReceipt bool) wire.PostMessage {
	t.Helper()
	intent := message.IntentOf(original)
	if mediaURL != "" {
		slots := []wire.MediaReference{{ID: "media", URL: mediaURL, Kind: "image", SHA256: strings.Repeat("a", 64)}}
		intent.Payload.MediaSlots = &slots
	}
	receipt := original.SubmissionReceipt
	messageID, err := message.ID(intent)
	if err != nil {
		t.Fatal(err)
	}
	receipt.MessageID = messageID
	receipt.ProviderSignature = ""
	if foreignReceipt {
		foreign := sySeed([]byte{31, 30, 29, 28, 27, 26, 25, 24, 23, 22, 21, 20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1, 0})
		receipt.ProviderID = keys.DIDOf(foreign)
		receipt.ProviderSignature = ""
		receiptBytes, err := canon.EncodeWithout(receipt, "provider_signature")
		if err != nil {
			t.Fatal(err)
		}
		receipt.ProviderSignature, err = keys.Sign(foreign, "receipt", receiptBytes)
		if err != nil {
			t.Fatal(err)
		}
	} else {
		provider := syProviderKey()
		receiptBytes, err := canon.EncodeWithout(receipt, "provider_signature")
		if err != nil {
			t.Fatal(err)
		}
		receipt.ProviderSignature, err = keys.Sign(provider, "receipt", receiptBytes)
		if err != nil {
			t.Fatal(err)
		}
	}
	post, err := message.Assemble(intent, receipt)
	if err != nil {
		t.Fatal(err)
	}
	author := sySeed([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31})
	if err := message.Sign(&post, author); err != nil {
		t.Fatal(err)
	}
	return post
}

func TestSyncImportsPages(t *testing.T) {
	t.Run("two pages import five verified posts", func(t *testing.T) {
		messages := syLoadMessages(t)
		providerID := keys.DIDOf(syProviderKey())
		first := syPage(t, providerID, 3, true,
			wire.ChangesPageItem{Sequence: 1, Message: messages[0]},
			wire.ChangesPageItem{Sequence: 2, Message: messages[1]},
			wire.ChangesPageItem{Sequence: 3, Message: messages[2]},
		)
		second := syPage(t, providerID, 5, false,
			wire.ChangesPageItem{Sequence: 4, Message: messages[3]},
			wire.ChangesPageItem{Sequence: 5, Message: messages[4]},
		)
		provider := syStartProvider(t, func(index int, _ *http.Request) syResponse {
			if index == 0 {
				return syResponse{status: 200, body: first}
			}
			return syResponse{status: 200, body: second}
		})
		now := time.Date(2026, 11, 11, 0, 1, 0, 0, time.UTC)
		s := syNewSyncService(t, provider.server.URL, providerID, &now)
		syRunSync(t, s)
		if got := s.Records().MaxSequence(); got != 5 {
			t.Fatalf("local sequence = %d; want 5", got)
		}
		if got := s.Records().Cursor(providerID); got != 5 {
			t.Fatalf("cursor = %d; want 5", got)
		}
		if _, ok := s.Records().Lookup(messages[0].MessageID); !ok {
			t.Fatal("first message was not imported")
		}
		if _, ok := s.Records().Lookup(messages[1].MessageID); !ok {
			t.Fatal("second message was not imported")
		}
		if _, ok := s.Records().Lookup(messages[2].MessageID); !ok {
			t.Fatal("third message was not imported")
		}
		if _, ok := s.Records().Lookup(messages[3].MessageID); !ok {
			t.Fatal("fourth message was not imported")
		}
		if _, ok := s.Records().Lookup(messages[4].MessageID); !ok {
			t.Fatal("fifth message was not imported")
		}
		if got := provider.requestPaths(); len(got) != 2 || got[0] != "/v1/changes?after=0&limit=100" || got[1] != "/v1/changes?after=3&limit=100" {
			t.Fatalf("requests = %v; want after 0 and after 3", got)
		}
	})
	t.Run("empty gap page advances the next request cursor", func(t *testing.T) {
		providerID := keys.DIDOf(syProviderKey())
		moreBody := syPage(t, providerID, 40, true)
		doneBody := syPage(t, providerID, 40, false)
		provider := syStartProvider(t, func(index int, _ *http.Request) syResponse {
			if index == 0 {
				return syResponse{status: 200, body: moreBody}
			}
			return syResponse{status: 200, body: doneBody}
		})
		now := time.Date(2026, 11, 11, 0, 1, 0, 0, time.UTC)
		s := syNewSyncService(t, provider.server.URL, providerID, &now)
		if _, err := s.Records().Import(providerID, nil, 12); err != nil {
			t.Fatal(err)
		}
		syRunSync(t, s)
		if got := s.Records().Cursor(providerID); got != 40 {
			t.Fatalf("cursor = %d; want 40", got)
		}
		if got := provider.requestPaths(); len(got) != 2 || got[0] != "/v1/changes?after=12&limit=100" || got[1] != "/v1/changes?after=40&limit=100" {
			t.Fatalf("requests = %v; want requests after 12 and 40", got)
		}
	})
}

func syRejectedPage(t *testing.T, body []byte, initialCursor uint64) {
	t.Helper()
	providerID := keys.DIDOf(syProviderKey())
	provider := syStartProvider(t, func(_ int, _ *http.Request) syResponse { return syResponse{status: 200, body: body} })
	now := time.Date(2026, 11, 11, 0, 1, 0, 0, time.UTC)
	s := syNewSyncService(t, provider.server.URL, providerID, &now)
	if initialCursor > 0 {
		if _, err := s.Records().Import(providerID, nil, initialCursor); err != nil {
			t.Fatal(err)
		}
	}
	syRunSync(t, s)
	if got := s.Records().Cursor(providerID); got != initialCursor {
		t.Fatalf("cursor = %d; want %d", got, initialCursor)
	}
	if got := s.Records().MaxSequence(); got != 0 {
		t.Fatalf("imported local sequence = %d; want 0", got)
	}
}

func TestSyncRejectsInvalidPages(t *testing.T) {
	providerID := keys.DIDOf(syProviderKey())
	otherID := keys.DIDOf(sySeed([]byte{31, 30, 29, 28, 27, 26, 25, 24, 23, 22, 21, 20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1, 0}))
	messages := syLoadMessages(t)
	t.Run("items out of order", func(t *testing.T) {
		syRejectedPage(t, syPage(t, providerID, 2, false, wire.ChangesPageItem{Sequence: 2, Message: messages[0]}, wire.ChangesPageItem{Sequence: 1, Message: messages[1]}), 0)
	})
	t.Run("repeated sequence", func(t *testing.T) {
		syRejectedPage(t, syPage(t, providerID, 1, false, wire.ChangesPageItem{Sequence: 1, Message: messages[0]}, wire.ChangesPageItem{Sequence: 1, Message: messages[1]}), 0)
	})
	t.Run("sequence not greater than after", func(t *testing.T) {
		syRejectedPage(t, syPage(t, providerID, 4, false, wire.ChangesPageItem{Sequence: 4, Message: messages[0]}), 4)
	})
	t.Run("next_after behind last item", func(t *testing.T) {
		syRejectedPage(t, syPage(t, providerID, 4, false, wire.ChangesPageItem{Sequence: 5, Message: messages[0]}), 0)
	})
	t.Run("next_after below after", func(t *testing.T) { syRejectedPage(t, syPage(t, providerID, 3, false), 4) })
	t.Run("provider ID differs", func(t *testing.T) {
		body := syPage(t, otherID, 1, false, wire.ChangesPageItem{Sequence: 1, Message: messages[0]})
		syRejectedPage(t, body, 0)
	})
	t.Run("extra top-level field", func(t *testing.T) {
		body := strings.TrimSuffix(string(syPage(t, providerID, 0, false)), "}") + `,"cursor":"x"}`
		syRejectedPage(t, []byte(body), 0)
	})
}

func TestSyncSkipsInvalidRecords(t *testing.T) {
	t.Run("altered signed text is skipped while valid page records import", func(t *testing.T) {
		messages := syLoadMessages(t)
		messages[0].Payload.Text += " altered"
		providerID := keys.DIDOf(syProviderKey())
		body := syPage(t, providerID, 2, false,
			wire.ChangesPageItem{Sequence: 1, Message: messages[0]},
			wire.ChangesPageItem{Sequence: 2, Message: messages[1]},
		)
		provider := syStartProvider(t, func(_ int, _ *http.Request) syResponse { return syResponse{status: 200, body: body} })
		now := time.Date(2026, 11, 11, 0, 1, 0, 0, time.UTC)
		s := syNewSyncService(t, provider.server.URL, providerID, &now)
		syRunSync(t, s)
		if got := s.Records().MaxSequence(); got != 1 {
			t.Fatalf("local sequence = %d; want 1", got)
		}
		if got := s.Records().Cursor(providerID); got != 2 {
			t.Fatalf("cursor = %d; want 2", got)
		}
		if _, ok := s.Records().Lookup(messages[1].MessageID); !ok {
			t.Fatal("valid message was not imported")
		}
	})
	t.Run("foreign but valid receipt signer is rejected", func(t *testing.T) {
		messages := syLoadMessages(t)
		foreign := syResign(t, messages[0], "", true)
		providerID := keys.DIDOf(syProviderKey())
		body := syPage(t, providerID, 1, false, wire.ChangesPageItem{Sequence: 1, Message: foreign})
		provider := syStartProvider(t, func(_ int, _ *http.Request) syResponse { return syResponse{status: 200, body: body} })
		now := time.Date(2026, 11, 11, 0, 1, 0, 0, time.UTC)
		s := syNewSyncService(t, provider.server.URL, providerID, &now)
		syRunSync(t, s)
		if got := s.Records().MaxSequence(); got != 0 {
			t.Fatalf("local sequence = %d; want 0", got)
		}
		if got := s.Records().Cursor(providerID); got != 1 {
			t.Fatalf("cursor = %d; want 1", got)
		}
	})
	t.Run("media URL with userinfo rejects the page", func(t *testing.T) {
		messages := syLoadMessages(t)
		userinfo := syResign(t, messages[0], "https://user:pass@example.com/image.png", false)
		providerID := keys.DIDOf(syProviderKey())
		body := syPage(t, providerID, 2, false,
			wire.ChangesPageItem{Sequence: 1, Message: messages[0]},
			wire.ChangesPageItem{Sequence: 2, Message: userinfo},
		)
		provider := syStartProvider(t, func(_ int, _ *http.Request) syResponse { return syResponse{status: 200, body: body} })
		now := time.Date(2026, 11, 11, 0, 1, 0, 0, time.UTC)
		s := syNewSyncService(t, provider.server.URL, providerID, &now)
		syRunSync(t, s)
		if got := s.Records().Cursor(providerID); got != 0 {
			t.Fatalf("cursor = %d; want 0", got)
		}
		if got := s.Records().MaxSequence(); got != 0 {
			t.Fatalf("local sequence = %d; want 0", got)
		}
	})
	t.Run("media URL failing semantic rules is not imported", func(t *testing.T) {
		messages := syLoadMessages(t)
		invalid := syResign(t, messages[0], "https://Example.com/a.png", false)
		providerID := keys.DIDOf(syProviderKey())
		body := syPage(t, providerID, 2, false,
			wire.ChangesPageItem{Sequence: 1, Message: invalid},
			wire.ChangesPageItem{Sequence: 2, Message: messages[1]},
		)
		provider := syStartProvider(t, func(_ int, _ *http.Request) syResponse { return syResponse{status: 200, body: body} })
		now := time.Date(2026, 11, 11, 0, 1, 0, 0, time.UTC)
		s := syNewSyncService(t, provider.server.URL, providerID, &now)
		syRunSync(t, s)
		if _, ok := s.Records().Lookup(invalid.MessageID); ok {
			t.Fatal("post with uppercase media host was imported")
		}
		if _, ok := s.Records().Lookup(messages[1].MessageID); !ok {
			t.Fatal("valid message was not imported")
		}
		if got := s.Records().Cursor(providerID); got != 2 {
			t.Fatalf("cursor = %d; want 2", got)
		}
	})
}

func TestSyncRateLimitBackoff(t *testing.T) {
	t.Run("429 delays the next request for retry_after_seconds", func(t *testing.T) {
		providerID := keys.DIDOf(syProviderKey())
		provider := syStartProvider(t, func(_ int, _ *http.Request) syResponse {
			return syResponse{status: http.StatusTooManyRequests, body: []byte(`{"retry_after_seconds":120,"status":"rate_limited"}`)}
		})
		now := time.Date(2026, 11, 11, 0, 1, 0, 0, time.UTC)
		s := syNewSyncService(t, provider.server.URL, providerID, &now)
		syRunSync(t, s)
		now = now.Add(60 * time.Second)
		syRunSync(t, s)
		if got := len(provider.requestPaths()); got != 1 {
			t.Fatalf("requests after 60 seconds = %d; want 1", got)
		}
		now = now.Add(61 * time.Second)
		syRunSync(t, s)
		if got := len(provider.requestPaths()); got != 2 {
			t.Fatalf("requests after 121 seconds = %d; want 2", got)
		}
	})
}

func TestSyncDoesNotFollowRedirect(t *testing.T) {
	t.Run("302 target is never contacted", func(t *testing.T) {
		targetCalls := 0
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { targetCalls++; w.WriteHeader(http.StatusOK) }))
		defer target.Close()
		providerID := keys.DIDOf(syProviderKey())
		provider := syStartProvider(t, func(_ int, _ *http.Request) syResponse {
			return syResponse{status: http.StatusFound, header: http.Header{"Location": []string{target.URL}}}
		})
		now := time.Date(2026, 11, 11, 0, 1, 0, 0, time.UTC)
		s := syNewSyncService(t, provider.server.URL, providerID, &now)
		syRunSync(t, s)
		if targetCalls != 0 {
			t.Fatalf("redirect target calls = %d; want 0", targetCalls)
		}
		if got := len(provider.requestPaths()); got != 1 {
			t.Fatalf("provider requests = %d; want 1", got)
		}
	})
}

func TestSyncPageBudget(t *testing.T) {
	t.Run("more pages stop at twenty per run", func(t *testing.T) {
		providerID := keys.DIDOf(syProviderKey())
		body := syPage(t, providerID, 0, true)
		provider := syStartProvider(t, func(_ int, _ *http.Request) syResponse { return syResponse{status: 200, body: body} })
		now := time.Date(2026, 11, 11, 0, 1, 0, 0, time.UTC)
		s := syNewSyncService(t, provider.server.URL, providerID, &now)
		syRunSync(t, s)
		if got := len(provider.requestPaths()); got != syMaxPages {
			t.Fatalf("requests = %d; want %d", got, syMaxPages)
		}
	})
}

func TestSyncErrorBodyDefaultRetry(t *testing.T) {
	t.Run("invalid retry duration defaults to sixty seconds", func(t *testing.T) {
		providerID := keys.DIDOf(syProviderKey())
		provider := syStartProvider(t, func(_ int, _ *http.Request) syResponse {
			return syResponse{status: http.StatusServiceUnavailable, body: []byte(`{"retry_after_seconds":0,"status":"unavailable"}`)}
		})
		now := time.Date(2026, 11, 11, 0, 1, 0, 0, time.UTC)
		s := syNewSyncService(t, provider.server.URL, providerID, &now)
		syRunSync(t, s)
		now = now.Add(59 * time.Second)
		syRunSync(t, s)
		if got := len(provider.requestPaths()); got != 1 {
			t.Fatalf("requests before default delay = %d; want 1", got)
		}
		now = now.Add(2 * time.Second)
		syRunSync(t, s)
		if got := len(provider.requestPaths()); got != 2 {
			t.Fatalf("requests after default delay = %d; want 2", got)
		}
	})
}
