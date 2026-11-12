package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/wire"
)

func TestTrustRefreshWithoutFoundation(t *testing.T) {
	t.Run("unavailable until publication succeeds", func(t *testing.T) {
		client, err := NewClient(Config{DataDir: t.TempDir(), Dev: true})
		if err != nil {
			t.Fatal(err)
		}
		if err := client.RunOnce(context.Background()); err != nil {
			t.Fatalf("RunOnce() error = %v, want nil when Foundation is unconfigured", err)
		}
		root, status := client.app.trustRoot()
		if root != nil || status.State != TrustUnavailable || status.Err != "No Foundation is configured" {
			t.Fatalf("trust state = (%v, %+v), want unavailable without a Foundation", root, status)
		}
	})
}

func TestTrustRefreshPinsAndRetainsRoot(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	var failing atomic.Bool
	genesis := devnet.Genesis()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if failing.Load() {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("{}"))
			return
		}
		var value any
		switch r.URL.Path {
		case "/v1/foundation/genesis":
			value = genesis
		case "/v1/foundation/certificates":
			value = wire.FoundationCertificatesPage{Certificates: devnet.Certificates(now), Closures: []wire.FoundationKeyClosure{}, KeySetUpdates: []wire.ReleaseKeySetUpdate{}}
		case "/v1/foundation/fee-schedules":
			value = wire.FeeSchedulesPage{Schedules: devnet.FeeSchedules()}
		case "/v1/foundation/distribution":
			value = wire.DistributionPage{Policies: devnet.Policies(), Statements: []wire.DistributionStatement{}}
		default:
			http.NotFound(w, r)
			return
		}
		body, err := json.Marshal(value)
		if err != nil {
			t.Errorf("encode fake publication: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}))
	defer server.Close()
	config := func(pin sdk.Pin) Config {
		return Config{Dev: true, DataDir: t.TempDir(), Now: func() time.Time { return now }, HTTPClient: server.Client(), Foundation: Endpoint{URL: server.URL, ServiceID: keys.DIDOf(devnet.FoundationKey()), Label: "Foundation"}, Pin: pin}
	}
	client, err := NewClient(config(sdk.Pin{ReleaseKeyIDs: genesis.ReleaseKeyIDs, Threshold: int(genesis.ReleaseThreshold)}))
	if err != nil {
		t.Fatal(err)
	}
	t.Run("matching release pin becomes ready", func(t *testing.T) {
		if err := client.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		root, status := client.app.trustRoot()
		if root == nil || status.State != TrustReady || status.Err != "" {
			t.Fatalf("trust state = (%v, %+v), want a ready pinned root", root, status)
		}
	})
	t.Run("wrong release pin is unavailable with a reason", func(t *testing.T) {
		wrong := append([]string(nil), genesis.ReleaseKeyIDs...)
		wrong[0] = keys.DIDOf(devnet.MonitorKey(0))
		client, err := NewClient(config(sdk.Pin{ReleaseKeyIDs: wrong, Threshold: int(genesis.ReleaseThreshold)}))
		if err != nil {
			t.Fatal(err)
		}
		if err := client.RunOnce(context.Background()); err == nil {
			t.Fatal("RunOnce() error = nil, want pin failure")
		}
		root, status := client.app.trustRoot()
		if root != nil || status.State != TrustUnavailable || status.Err == "" {
			t.Fatalf("trust state = (%v, %+v), want unavailable with reason", root, status)
		}
	})
	t.Run("failed refresh retains last good root as stale", func(t *testing.T) {
		root, before := client.app.trustRoot()
		failing.Store(true)
		defer failing.Store(false)
		if err := client.RunOnce(context.Background()); err == nil {
			t.Fatal("RunOnce() error = nil, want failed refresh")
		}
		gotRoot, status := client.app.trustRoot()
		if gotRoot != root || status.State != TrustStale || status.Err == "" || !status.FetchedAt.Equal(before.FetchedAt) {
			t.Fatalf("trust state = (%v, %+v), want retained root and stale status", gotRoot, status)
		}
	})
}

func TestRunStopsWhenContextIsCancelled(t *testing.T) {
	t.Run("cancelled context", func(t *testing.T) {
		client, err := NewClient(Config{DataDir: t.TempDir(), Dev: true})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := client.Run(ctx); err != nil {
			t.Fatalf("Run() error = %v, want nil", err)
		}
	})
}
