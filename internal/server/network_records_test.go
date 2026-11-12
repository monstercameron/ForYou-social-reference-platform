package server

import (
	"crypto/ed25519"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/message"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/wire"
)

func TestRecordSourceSelection(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	serveEmpty := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(wire.DiscoveryPage{GeneratedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(30 * time.Minute).Format(time.RFC3339), Messages: []wire.PublicTransportMessage{}, NextCursor: nil})
	}
	t.Run("unconfigured client uses demo source", func(t *testing.T) {
		client, err := NewClient(Config{Dev: true, DataDir: t.TempDir(), Now: func() time.Time { return now }})
		if err != nil {
			t.Fatal(err)
		}
		posts, err := client.app.records().Posts(t.Context(), httptest.NewRequest(http.MethodGet, "/", nil))
		if err != nil || len(posts) == 0 || posts[0].DemoData {
			t.Fatalf("demo posts = %d, error %v; want existing demo data without fallback marker", len(posts), err)
		}
	})
	t.Run("empty provider in dev falls back with a marker", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(serveEmpty))
		defer server.Close()
		client, err := NewClient(Config{Dev: true, DataDir: t.TempDir(), Now: func() time.Time { return now }, HTTPClient: server.Client(), Providers: []Endpoint{{URL: server.URL, ServiceID: keys.DIDOf(devnet.ProviderKey(0)), Label: "Provider"}}})
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		client.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Demo data") || !strings.Contains(response.Body.String(), "Provider has no posts yet; showing demo data") {
			t.Fatalf("dev empty-provider page = %d %q; want visible demo fallback", response.Code, response.Body.String())
		}
	})
	t.Run("empty provider outside dev has no demo posts", func(t *testing.T) {
		server := httptest.NewTLSServer(http.HandlerFunc(serveEmpty))
		defer server.Close()
		client, err := NewClient(Config{DataDir: t.TempDir(), Now: func() time.Time { return now }, HTTPClient: server.Client(), Providers: []Endpoint{{URL: server.URL, ServiceID: keys.DIDOf(devnet.ProviderKey(0)), Label: "Provider"}}})
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		client.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "Demo data") || !strings.Contains(response.Body.String(), "No posts from your sources yet") || !strings.Contains(response.Body.String(), "Your sources have not returned any posts") {
			t.Fatalf("non-dev empty-provider page = %d %q; want honest empty state", response.Code, response.Body.String())
		}
	})
}

func TestNetworkSourceRendersVerifiedRecord(t *testing.T) {
	now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	issuedAt := now.Add(-24 * time.Hour)
	recordRaw, providerID := signedNetworkRecord(t, issuedAt)
	var served atomic.Pointer[[]byte]
	served.Store(&recordRaw)
	var message wire.TransportMessage
	if err := wire.Decode(recordRaw, "TransportMessage", &message); err != nil {
		t.Fatal(err)
	}
	genesis := devnet.Genesis()
	root, err := sdk.BuildTrustRoot(sdk.Pin{ReleaseKeyIDs: genesis.ReleaseKeyIDs, Threshold: int(genesis.ReleaseThreshold)}, sdk.Publications{Genesis: genesis, FetchedAt: now, Endpoint: "https://foundation.example"})
	if err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/messages":
			_ = json.NewEncoder(w).Encode(struct {
				GeneratedAt string            `json:"generated_at"`
				ExpiresAt   string            `json:"expires_at"`
				Messages    []json.RawMessage `json:"messages"`
				NextCursor  *string           `json:"next_cursor"`
			}{GeneratedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(30 * time.Minute).Format(time.RFC3339), Messages: []json.RawMessage{*served.Load()}})
		case strings.HasPrefix(r.URL.Path, "/v1/messages/"):
			_, _ = w.Write(*served.Load())
		case strings.HasPrefix(r.URL.Path, "/v1/replies/"):
			_ = json.NewEncoder(w).Encode(wire.DiscoveryPage{GeneratedAt: now.Format(time.RFC3339), ExpiresAt: now.Add(30 * time.Minute).Format(time.RFC3339), Messages: []wire.PublicTransportMessage{}, NextCursor: nil})
		default:
			http.NotFound(w, r)
		}
	}))
	defer provider.Close()
	client, err := NewClient(Config{DataDir: t.TempDir(), Now: func() time.Time { return now }, HTTPClient: provider.Client(), Providers: []Endpoint{{URL: provider.URL, ServiceID: providerID, Label: "Fixture provider"}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("unavailable trust root is visible on the record", func(t *testing.T) {
		response := httptest.NewRecorder()
		client.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "Not verifiable now") || !strings.Contains(response.Body.String(), "trust root is required") || !strings.Contains(response.Body.String(), message.Payload.Text) {
			t.Fatalf("feed without trust root = %d %q; want visible unverified record and reason", response.Code, response.Body.String())
		}
	})
	client.app.trustMu.Lock()
	client.app.root = root
	client.app.trustStatus = TrustStatus{State: TrustReady, FetchedAt: now, Endpoint: "https://foundation.example"}
	client.app.trustMu.Unlock()
	t.Run("verified post appears with settlement badge and trust summary", func(t *testing.T) {
		response := httptest.NewRecorder()
		client.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), message.Payload.Text) || !strings.Contains(response.Body.String(), "Pending") || !strings.Contains(response.Body.String(), "4 of 5 release keys") || !strings.Contains(response.Body.String(), "reachable") {
			t.Fatalf("network feed = %d %q; want verified post, badge and trust summary", response.Code, response.Body.String())
		}
	})
	t.Run("post and thread resolve through provider reads", func(t *testing.T) {
		postResponse := httptest.NewRecorder()
		client.Handler().ServeHTTP(postResponse, httptest.NewRequest(http.MethodGet, "/posts/"+message.MessageID, nil))
		if postResponse.Code != http.StatusOK || !strings.Contains(postResponse.Body.String(), message.Payload.Text) {
			t.Fatalf("post page = %d %q; want verified fixture post", postResponse.Code, postResponse.Body.String())
		}
		threadResponse := httptest.NewRecorder()
		client.Handler().ServeHTTP(threadResponse, httptest.NewRequest(http.MethodGet, "/thread?id="+message.MessageID, nil))
		if threadResponse.Code != http.StatusOK || !strings.Contains(threadResponse.Body.String(), message.Payload.Text) {
			t.Fatalf("thread page = %d %q; want verified parent post", threadResponse.Code, threadResponse.Body.String())
		}
	})
	t.Run("tampered signed payload is not displayed", func(t *testing.T) {
		var tampered wire.TransportMessage
		if err := wire.Decode(recordRaw, "TransportMessage", &tampered); err != nil {
			t.Fatal(err)
		}
		original := tampered.Payload.Text
		tampered.Payload.Text = "tampered payload"
		encoded, err := json.Marshal(tampered)
		if err != nil {
			t.Fatal(err)
		}
		served.Store(&encoded)
		defer served.Store(&recordRaw)
		response := httptest.NewRecorder()
		client.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "tampered payload") || !strings.Contains(response.Body.String(), original) {
			t.Fatalf("tampered feed = %d %q; want the held verified %q and not the tampered text", response.Code, response.Body.String(), original)
		}
	})
}

func signedNetworkRecord(t *testing.T, issuedAt time.Time) ([]byte, string) {
	t.Helper()
	authorSeed := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31}
	author := ed25519.NewKeyFromSeed(authorSeed)
	genesis := devnet.Genesis()
	schedule := genesis.FeeSchedules[0]
	intent := wire.Intent{AuthorID: keys.DIDOf(author), Timestamp: wire.FormatTime(issuedAt), MessageType: "post", Version: wire.Version{Major: 2, Minor: 0}, Payload: wire.PostPayload{ContentFormat: "text/plain", Text: "A verified network post."}}
	id, err := message.ID(intent)
	if err != nil {
		t.Fatal(err)
	}
	provider := devnet.ProviderKey(0)
	receipt := wire.SubmissionReceipt{ProviderID: keys.DIDOf(provider), AuthorID: intent.AuthorID, MessageID: id, ReceiptID: keys.EncodeB64URL(make([]byte, 32)), QuoteID: "fyfq2:0000000000000000000000000000000000000000000000000000000000000000", ScheduleID: schedule.ScheduleID, Network: schedule.Network, AmountBaseUnits: "136", AuthorizationAt: wire.FormatTime(issuedAt), IssuedAt: wire.FormatTime(issuedAt)}
	receipt, err = fee.SignReceipt(receipt, provider)
	if err != nil {
		t.Fatal(err)
	}
	postMessage, err := message.Assemble(intent, receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := message.Sign(&postMessage, author); err != nil {
		t.Fatal(err)
	}
	raw, err := canon.Encode(postMessage)
	if err != nil {
		t.Fatal(err)
	}
	return raw, receipt.ProviderID
}
