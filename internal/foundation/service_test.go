package foundation

import (
	"crypto/ed25519"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
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

func TestNewKeepsEmptyArraysInGovernanceUpdates(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	t.Run("empty voided closure is accepted and published", func(t *testing.T) {
		cfg := testConfig(t, &now)
		cfg.Closures = []wire.FoundationKeyClosure{emptyVoidedClosure(t, now)}
		service, err := New(cfg)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		defer service.Close()
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/foundation/certificates", nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"voided":[]`) {
			t.Fatalf("certificates response = %d, want closure with \"voided\":[]", response.Code)
		}
	})

	t.Run("empty prior objects key-set update is accepted and published", func(t *testing.T) {
		cfg := testConfig(t, &now)
		cfg.KeySetUpdates = []wire.ReleaseKeySetUpdate{emptyPriorObjectsUpdate(t)}
		service, err := New(cfg)
		if err != nil {
			t.Fatalf("New() error = %v", err)
		}
		defer service.Close()
		response := httptest.NewRecorder()
		service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/v1/foundation/certificates", nil))
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"prior_objects":[]`) {
			t.Fatalf("certificates response = %d, want key-set update with \"prior_objects\":[]", response.Code)
		}
	})
}

func emptyVoidedClosure(t *testing.T, now time.Time) wire.FoundationKeyClosure {
	t.Helper()
	foundationKey := devnet.FoundationKey()
	foundationID := keys.DIDOf(foundationKey)
	head, err := governance.SignHead(foundationKey, foundationID, strings.Repeat("0", 64), 0, now)
	if err != nil {
		t.Fatal(err)
	}
	closure := wire.FoundationKeyClosure{FoundationID: foundationID, ClosedAt: wire.FormatTime(now), Head: head, Voided: []string{}}
	unsigned, err := canon.EncodeWithout(closure, "release_signatures")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := canon.Parse(unsigned, canon.Limits{MaxDepth: 32})
	if err != nil {
		t.Fatal(err)
	}
	closure.ReleaseSignatures, err = governance.SignRelease("foundation-key-closure", parsed, devnet.ReleaseKeys()[:4])
	if err != nil {
		t.Fatal(err)
	}
	return closure
}

func emptyPriorObjectsUpdate(t *testing.T) wire.ReleaseKeySetUpdate {
	t.Helper()
	genesis := devnet.Genesis()
	update := wire.ReleaseKeySetUpdate{Sequence: 1, ReleaseKeyIDs: genesis.ReleaseKeyIDs, ReleaseThreshold: genesis.ReleaseThreshold, PriorObjects: []string{}, EffectiveAt: "2026-06-01T00:00:00Z"}
	var err error
	update.UpdateID, err = governance.UpdateID(update)
	if err != nil {
		t.Fatal(err)
	}
	unsigned, err := canon.EncodeWithout(update, "release_signatures")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := canon.Parse(unsigned, canon.Limits{MaxDepth: 32})
	if err != nil {
		t.Fatal(err)
	}
	update.ReleaseSignatures, err = governance.SignRelease("release-key-set-update", parsed, devnet.ReleaseKeys()[:4])
	if err != nil {
		t.Fatal(err)
	}
	return update
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

func TestClockIsWholeSecondsForTrust(t *testing.T) {
	fractional := time.Date(2026, 11, 11, 23, 4, 21, 123456789, time.FixedZone("local", -5*3600))
	service, err := New(testConfig(t, &fractional))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	if got, want := service.Clock(), time.Date(2026, 11, 12, 4, 4, 21, 0, time.UTC); !got.Equal(want) || got.Location() != time.UTC {
		t.Errorf("Clock() = %v; want %v in UTC", got, want)
	}
	if _, ok := service.Trust().CurrentFoundationID(service.Clock()); !ok {
		t.Error("with a wall clock that has fractions of a second, the Foundation key must still be in force")
	}
}
