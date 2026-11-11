package foundation

import (
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/distribution"
	"foryou.local/foryou/internal/governance"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

func init() {
	routeGroups = append(routeGroups, func(_ *Service, mux *http.ServeMux) {
		mux.HandleFunc("GET /test-route-group", func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		})
	})
}

func TestNewValidatesFoundationConfig(t *testing.T) {
	t.Run("valid devnet configuration", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		cfg := testConfig(t, &now)
		service, err := New(cfg)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		_ = service.Close()
	})

	t.Run("uncertified signing key", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		cfg := testConfig(t, &now)
		cfg.Key = devnet.ProviderKey(0)
		if _, err := New(cfg); !errors.Is(err, ErrUncertifiedFoundationKey) {
			t.Fatalf("New() error = %v, want ErrUncertifiedFoundationKey", err)
		}
	})

	t.Run("invalid second policy", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		cfg := testConfig(t, &now)
		second := cfg.Policies[0]
		second.Revision = 2
		second.ApprovedAt = "2027-03-12T00:00:00Z"
		second.EffectiveAt = "2027-04-01T00:00:00Z"
		releaseKeys := devnet.ReleaseKeys()
		second.Monitors = []string{second.Monitors[0], second.Monitors[1], keys.DIDOf(devnet.ProviderKey(0)), keys.DIDOf(devnet.ReplicaKey()), keys.DIDOf(releaseKeys[0])}
		sort.Strings(second.Monitors)
		policyID, err := distribution.PolicyID(second)
		if err != nil {
			t.Fatal(err)
		}
		second.PolicyID = policyID
		unsigned, err := canon.EncodeWithout(second, "release_signatures")
		if err != nil {
			t.Fatal(err)
		}
		value, err := canon.Parse(unsigned, canon.Limits{MaxDepth: 32})
		if err != nil {
			t.Fatal(err)
		}
		second.ReleaseSignatures, err = governance.SignRelease("distribution-policy", value, devnet.ReleaseKeys())
		if err != nil {
			t.Fatal(err)
		}
		cfg.Policies = append(cfg.Policies, second)
		if _, err := New(cfg); !errors.Is(err, distribution.ErrCommitteeChange) {
			t.Fatalf("New() error = %v, want ErrCommitteeChange", err)
		}
	})
}

func TestHandlerHealthAndRouteGroup(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	service, err := New(testConfig(t, &now))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	t.Run("health has no-store", func(t *testing.T) {
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/healthz", nil))
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("health response = %d, Cache-Control %q", response.Code, response.Header().Get("Cache-Control"))
		}
	})

	t.Run("registered test route group", func(t *testing.T) {
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/test-route-group", nil))
		if response.Code != http.StatusNoContent {
			t.Fatalf("route-group response status = %d, want %d", response.Code, http.StatusNoContent)
		}
	})
}

func testConfig(t *testing.T, now *time.Time) Config {
	t.Helper()
	calendar, err := devnet.Calendar(0)
	if err != nil {
		t.Fatal(err)
	}
	return Config{
		Dev: true, Key: ed25519.PrivateKey(devnet.FoundationKey()), Genesis: devnet.Genesis(),
		Certificates: devnet.Certificates, FeeSchedules: devnet.FeeSchedules(), Policies: devnet.Policies(),
		Calendar: calendar, DataDir: t.TempDir(), Clock: func() time.Time { return *now }, HeadInterval: time.Hour,
		Providers: []string{"http://127.0.0.1:8445"}, Monitors: map[string]string{}, MonitorPayouts: map[string]map[string]string{},
	}
}

func signedReceipt(t *testing.T, service *Service, messageID, storedAt string) (wire.FoundationLogEntry, []byte) {
	t.Helper()
	receipt := wire.VaultReceipt{FoundationID: keys.DIDOf(service.cfg.Key), MessageID: messageID, StoredAt: storedAt}
	signed, entry, err := service.SignAndLog(governance.LogKindVaultReceipt, "vault-receipt", receipt)
	if err != nil {
		t.Fatal(err)
	}
	return entry, signed
}
