package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/faux"
)

func buildTestRole(t *testing.T, role string) (roleConfig, service) {
	t.Helper()
	config, _, err := loadRoleConfig(role, mapEnv(map[string]string{
		"FORYOU_DEV": "1", "FORYOU_DATA_DIR": t.TempDir(),
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(config.DataDir, 0700); err != nil {
		t.Fatal(err)
	}
	svc, err := builders[role](config)
	if err != nil {
		t.Fatalf("build %s: %v", role, err)
	}
	t.Cleanup(func() {
		if err := svc.Close(); err != nil {
			t.Errorf("close %s: %v", role, err)
		}
	})
	return config, svc
}

func requireStatus(t *testing.T, handler http.Handler, path string, want int) {
	t.Helper()
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
	if recorder.Code != want {
		t.Errorf("GET %s status = %d; want %d", path, recorder.Code, want)
	}
}

func TestBuildServices(t *testing.T) {
	t.Run("faux", func(t *testing.T) {
		_, svc := buildTestRole(t, "faux")
		requireStatus(t, svc.Handler(), "/healthz", http.StatusOK)
		requireStatus(t, svc.Handler(), "/faux/v1/chains/bitcoin-mainnet", http.StatusOK)
	})
	t.Run("foundation", func(t *testing.T) {
		_, svc := buildTestRole(t, "foundation")
		requireStatus(t, svc.Handler(), "/healthz", http.StatusOK)
		requireStatus(t, svc.Handler(), "/v1/foundation/genesis", http.StatusOK)
	})
	t.Run("monitor", func(t *testing.T) {
		_, svc := buildTestRole(t, "monitor")
		requireStatus(t, svc.Handler(), "/healthz", http.StatusOK)
		recorder := httptest.NewRecorder()
		svc.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/monitors/", nil))
		if recorder.Code != http.StatusNotFound || recorder.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("monitor member route = status %d, content type %q; want JSON 404", recorder.Code, recorder.Header().Get("Content-Type"))
		}
	})
	t.Run("provider", func(t *testing.T) {
		_, svc := buildTestRole(t, "provider")
		requireStatus(t, svc.Handler(), "/healthz", http.StatusOK)
		requireStatus(t, svc.Handler(), "/", http.StatusNotFound)
	})
	t.Run("replica", func(t *testing.T) {
		_, svc := buildTestRole(t, "replica")
		requireStatus(t, svc.Handler(), "/healthz", http.StatusOK)
		requireStatus(t, svc.Handler(), "/.well-known/foryou-operator.json", http.StatusOK)
	})
	t.Run("client", func(t *testing.T) {
		_, svc := buildTestRole(t, "client")
		requireStatus(t, svc.Handler(), "/healthz", http.StatusOK)
		requireStatus(t, svc.Handler(), "/static/app.js", http.StatusOK)
	})
}

func TestProviderRoleServesItsLocalPeersFromOneAddress(t *testing.T) {
	_, svc := buildTestRole(t, "provider")
	listing := func() int {
		recorder := httptest.NewRecorder()
		svc.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/settlement-claims?network=bitcoin-mainnet", nil))
		return recorder.Code
	}
	// A monitor's pass alone makes more listing reads in a minute than the
	// production floor of six for one source.
	statuses := [12]int{listing(), listing(), listing(), listing(), listing(), listing(), listing(), listing(), listing(), listing(), listing(), listing()}
	if statuses != [12]int{200, 200, 200, 200, 200, 200, 200, 200, 200, 200, 200, 200} {
		t.Errorf("twelve listing reads from one address answered %v; want all 200", statuses)
	}
}

func TestBuildProviderRejectsClaimPeriodAboveDay(t *testing.T) {
	config, _, err := loadRoleConfig("provider", mapEnv(map[string]string{"FORYOU_DEV": "1", "FORYOU_DATA_DIR": t.TempDir()}))
	if err != nil {
		t.Fatal(err)
	}
	config.ClaimPeriod = 25 * time.Hour
	if _, err := builders["provider"](config); err == nil {
		t.Fatal("provider builder accepted a claim period above 24 hours")
	}
}

func TestBuildFoundationRequiresDevMode(t *testing.T) {
	config, _, err := loadRoleConfig("foundation", mapEnv(map[string]string{"FORYOU_DATA_DIR": t.TempDir()}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = builders["foundation"](config)
	if !errors.Is(err, errDevOnly) {
		t.Fatalf("foundation builder error = %v; want %v", err, errDevOnly)
	}
}

func TestBuildFauxRestoresJournal(t *testing.T) {
	config, _, err := loadRoleConfig("faux", mapEnv(map[string]string{"FORYOU_DEV": "1", "FORYOU_DATA_DIR": t.TempDir()}))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(config.DataDir, 0700); err != nil {
		t.Fatal(err)
	}
	first, err := builders["faux"](config)
	if err != nil {
		t.Fatal(err)
	}
	firstServer := httptest.NewServer(first.Handler())
	client := faux.NewClient(firstServer.URL, nil)
	transfer, err := client.Submit(context.Background(), faux.SubmitRequest{
		Network: faux.BitcoinMainnet, Recipient: devnet.Recipient("test", faux.BitcoinMainnet), AmountChainUnits: "1000",
	})
	if err != nil {
		firstServer.Close()
		_ = first.Close()
		t.Fatal(err)
	}
	firstServer.Close()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(config.DataDir, "journal.jsonl")); err != nil {
		t.Fatalf("faux journal: %v", err)
	}

	second, err := builders["faux"](config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := second.Close(); err != nil {
			t.Errorf("close restarted faux: %v", err)
		}
	})
	secondServer := httptest.NewServer(second.Handler())
	defer secondServer.Close()
	restored, err := faux.NewClient(secondServer.URL, nil).Transfer(context.Background(), faux.BitcoinMainnet, transfer.TxID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.TxID != transfer.TxID || restored.Recipient != transfer.Recipient || restored.AmountChainUnits != transfer.AmountChainUnits {
		t.Errorf("restored transfer = %+v; want submitted transfer %+v", restored, transfer)
	}
}
