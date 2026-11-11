package monitor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/governance"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

func TestFoundationWatchMirrorsAndResumes(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	fixture := newWatchTestFixture(t, now)
	server := httptest.NewServer(fixture.handler)
	defer server.Close()
	ledger, err := faux.Open(faux.Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	svc, err := New(Config{Genesis: devnet.Genesis(), DataDir: root, Now: func() time.Time { return now }, Peers: map[string]string{"foundation": server.URL}, Faux: ledger})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := watchTestEntryCount(t, svc); got != 3 {
		t.Fatalf("mirrored entry count = %d; want 3", got)
	}
	if got := watchTestHeadCount(t, svc); got != 2 {
		t.Fatalf("held head count = %d; want 2", got)
	}
	if got := svc.FoundationEvidence(); len(got) != 0 {
		t.Fatalf("evidence = %#v; want none", got)
	}
	objectRequests := fixture.objectRequests
	if err := svc.Close(); err != nil {
		t.Fatal(err)
	}
	svc, err = New(Config{Genesis: devnet.Genesis(), DataDir: root, Now: func() time.Time { return now }, Peers: map[string]string{"foundation": server.URL}, Faux: ledger})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if err := svc.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := fixture.logAfter[len(fixture.logAfter)-1]; got != "2" {
		t.Fatalf("restart log request after = %q; want 2", got)
	}
	if got := fixture.objectRequests; got != objectRequests {
		t.Fatalf("object requests after restart = %d; want unchanged at %d", got, objectRequests)
	}
	if got := len(svc.FoundationEvidence()); got != 0 {
		t.Fatalf("evidence after restart = %d; want 0", got)
	}
}

func TestFoundationWatchRecordsRewrittenHeadOnce(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	fixture := newWatchTestFixture(t, now)
	fixture.conflicting = true
	server := httptest.NewServer(fixture.handler)
	defer server.Close()
	ledger, err := faux.Open(faux.Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(Config{Genesis: devnet.Genesis(), DataDir: t.TempDir(), Now: func() time.Time { return now }, Peers: map[string]string{"foundation": server.URL}, Faux: ledger})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if err := svc.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := svc.FoundationEvidence(); len(got) != 1 || got[0].Kind != "rewritten_log" || len(got[0].Objects) != 2 {
		t.Fatalf("evidence = %#v; want one rewritten_log carrying two heads", got)
	}
	var first, second wire.FoundationLogHead
	if err := json.Unmarshal(svc.FoundationEvidence()[0].Objects[0], &first); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(svc.FoundationEvidence()[0].Objects[1], &second); err != nil {
		t.Fatal(err)
	}
	if first.Size != 3 || second.Size != 3 || first.Root == second.Root || first.Root != fixture.heads[1].Root && second.Root != fixture.heads[1].Root {
		t.Fatalf("rewritten heads = (%d,%s), (%d,%s); want honest and conflicting size-3 roots", first.Size, first.Root, second.Size, second.Root)
	}
	if err := svc.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := len(svc.FoundationEvidence()); got != 1 {
		t.Fatalf("repeated evidence count = %d; want 1", got)
	}
}

func TestFoundationWatchRecordsBadObjectHash(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	fixture := newWatchTestFixture(t, now)
	fixture.badObject = true
	server := httptest.NewServer(fixture.handler)
	defer server.Close()
	ledger, err := faux.Open(faux.Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(Config{Genesis: devnet.Genesis(), DataDir: t.TempDir(), Now: func() time.Time { return now }, Peers: map[string]string{"foundation": server.URL}, Faux: ledger})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if err := svc.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := svc.FoundationEvidence(); len(got) != 1 || got[0].Kind != "missing_object" {
		t.Fatalf("evidence = %#v; want missing_object", got)
	}
}

func TestFoundationWatchUnavailableFoundationContinues(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	ledger, err := faux.Open(faux.Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := New(Config{Genesis: devnet.Genesis(), DataDir: t.TempDir(), Now: func() time.Time { return now }, Peers: map[string]string{"foundation": "http://127.0.0.1:1"}, Faux: ledger})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if err := svc.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce() error = %v; want nil", err)
	}
	if got := len(svc.FoundationEvidence()); got != 0 {
		t.Fatalf("evidence = %d; want 0", got)
	}
}

func TestFoundationWatchHonorsPerPassEntryLimit(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	fixture := newWatchTestFixture(t, now)
	server := httptest.NewServer(fixture.handler)
	defer server.Close()
	ledger, err := faux.Open(faux.Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	limit := watchEntryLimit
	watchEntryLimit = 2
	t.Cleanup(func() { watchEntryLimit = limit })
	svc, err := New(Config{Genesis: devnet.Genesis(), DataDir: t.TempDir(), Now: func() time.Time { return now }, Peers: map[string]string{"foundation": server.URL}, Faux: ledger})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	if err := svc.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := watchTestEntryCount(t, svc); got != 2 {
		t.Fatalf("first pass mirrored %d entries; want 2", got)
	}
	if err := svc.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := watchTestEntryCount(t, svc); got != 3 {
		t.Fatalf("second pass mirrored %d entries; want 3", got)
	}
}

func TestFoundationWatchReportsRecipientFundedDeposit(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	t.Run("provider commitment from recipient address", func(t *testing.T) {
		fixture := newWatchTestFixture(t, now)
		server := httptest.NewServer(fixture.handler)
		defer server.Close()
		ledger, err := faux.Open(faux.Config{Now: func() time.Time { return now }})
		if err != nil {
			t.Fatal(err)
		}
		providerKey := keys.DIDOf(devnet.ProviderKey(0))
		commitment, err := settlement.DepositCommitment(providerKey, faux.BitcoinMainnet)
		if err != nil {
			t.Fatal(err)
		}
		recipient := devnet.Genesis().FoundationKeyCertificates[0].Recipients.BitcoinMainnet
		_, err = ledger.Submit(context.Background(), faux.SubmitRequest{Network: faux.BitcoinMainnet, From: recipient, Recipient: recipient, AmountChainUnits: "10000", Data: hex.EncodeToString(commitment[:])})
		if err != nil {
			t.Fatal(err)
		}
		svc, err := New(Config{Genesis: devnet.Genesis(), DataDir: t.TempDir(), Now: func() time.Time { return now }, Peers: map[string]string{"foundation": server.URL, "provider": "http://127.0.0.1:8445"}, Faux: ledger})
		if err != nil {
			t.Fatal(err)
		}
		defer svc.Close()
		if err := svc.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		evidence := svc.FoundationEvidence()
		if len(evidence) != 1 || evidence[0].Kind != "recipient_funded_deposit" {
			t.Fatalf("evidence count/kind = %d/%q; want one recipient_funded_deposit", len(evidence), watchTestEvidenceKind(evidence))
		}
		var details struct {
			ProviderFirstKey string `json:"provider_first_key"`
		}
		if err := json.Unmarshal(evidence[0].Objects[1], &details); err != nil {
			t.Fatal(err)
		}
		if details.ProviderFirstKey != providerKey {
			t.Fatalf("provider first key = %q; want %q", details.ProviderFirstKey, providerKey)
		}
	})
	t.Run("faucet-funded deposit", func(t *testing.T) {
		fixture := newWatchTestFixture(t, now)
		server := httptest.NewServer(fixture.handler)
		defer server.Close()
		ledger, err := faux.Open(faux.Config{Now: func() time.Time { return now }})
		if err != nil {
			t.Fatal(err)
		}
		commitment, err := settlement.DepositCommitment(keys.DIDOf(devnet.ProviderKey(0)), faux.BitcoinMainnet)
		if err != nil {
			t.Fatal(err)
		}
		recipient := devnet.Genesis().FoundationKeyCertificates[0].Recipients.BitcoinMainnet
		_, err = ledger.Submit(context.Background(), faux.SubmitRequest{Network: faux.BitcoinMainnet, From: faux.DevAddress(faux.BitcoinMainnet, "faucet"), Recipient: recipient, AmountChainUnits: "10000", Data: hex.EncodeToString(commitment[:])})
		if err != nil {
			t.Fatal(err)
		}
		svc, err := New(Config{Genesis: devnet.Genesis(), DataDir: t.TempDir(), Now: func() time.Time { return now }, Peers: map[string]string{"foundation": server.URL, "provider": "http://127.0.0.1:8445"}, Faux: ledger})
		if err != nil {
			t.Fatal(err)
		}
		defer svc.Close()
		if err := svc.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := len(svc.FoundationEvidence()); got != 0 {
			t.Fatalf("evidence count = %d; want 0", got)
		}
	})
}

func TestFoundationWatchClosureTiming(t *testing.T) {
	now := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	t.Run("held larger head predates closure observation", func(t *testing.T) {
		fixture := newWatchTestFixture(t, now)
		server := httptest.NewServer(fixture.handler)
		defer server.Close()
		ledger, err := faux.Open(faux.Config{Now: func() time.Time { return now }})
		if err != nil {
			t.Fatal(err)
		}
		currentTime := now
		svc, err := New(Config{Genesis: devnet.Genesis(), DataDir: t.TempDir(), Now: func() time.Time { return currentTime }, Peers: map[string]string{"foundation": server.URL}, Faux: ledger})
		if err != nil {
			t.Fatal(err)
		}
		defer svc.Close()
		if err := svc.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		fixture.certificates.Closures = []wire.FoundationKeyClosure{watchTestClosure(t, fixture, now)}
		currentTime = now.Add(time.Second)
		if err := svc.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		evidence := svc.FoundationEvidence()
		if len(evidence) != 1 || evidence[0].Kind != "head_after_closure" {
			t.Fatalf("evidence count/kind = %d/%q; want head_after_closure", len(evidence), watchTestEvidenceKind(evidence))
		}
		var closure wire.FoundationKeyClosure
		var head wire.FoundationLogHead
		if err := json.Unmarshal(evidence[0].Objects[0], &closure); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(evidence[0].Objects[1], &head); err != nil {
			t.Fatal(err)
		}
		if closure.Head.Size != 2 || head.Size != 3 {
			t.Fatalf("published closure/head sizes = %d/%d; want 2/3", closure.Head.Size, head.Size)
		}
	})
	t.Run("larger head first arrives after closure observation", func(t *testing.T) {
		fixture := newWatchTestFixture(t, now)
		fixture.hideHeads = true
		fixture.certificates.Closures = []wire.FoundationKeyClosure{watchTestClosure(t, fixture, now)}
		server := httptest.NewServer(fixture.handler)
		defer server.Close()
		ledger, err := faux.Open(faux.Config{Now: func() time.Time { return now }})
		if err != nil {
			t.Fatal(err)
		}
		currentTime := now
		svc, err := New(Config{Genesis: devnet.Genesis(), DataDir: t.TempDir(), Now: func() time.Time { return currentTime }, Peers: map[string]string{"foundation": server.URL}, Faux: ledger})
		if err != nil {
			t.Fatal(err)
		}
		defer svc.Close()
		if err := svc.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		fixture.hideHeads = false
		currentTime = now.Add(time.Second)
		if err := svc.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		if got := len(svc.FoundationEvidence()); got != 0 {
			t.Fatalf("evidence count = %d; want 0", got)
		}
	})
}

type watchTestFixture struct {
	entries        []wire.FoundationLogEntry
	objects        map[string][]byte
	heads          []wire.FoundationLogHead
	certificates   wire.FoundationCertificatesPage
	logRequests    int
	logAfter       []string
	objectRequests int
	conflicting    bool
	badObject      bool
	hideHeads      bool
	handler        http.Handler
}

func newWatchTestFixture(t *testing.T, now time.Time) *watchTestFixture {
	t.Helper()
	fixture := &watchTestFixture{objects: map[string][]byte{}, certificates: wire.FoundationCertificatesPage{Certificates: devnet.Certificates(now), Closures: []wire.FoundationKeyClosure{}, KeySetUpdates: []wire.ReleaseKeySetUpdate{}}}
	first := watchTestReceipt(t, "fy1:"+strings.Repeat("a1", 32), now)
	second := watchTestReceipt(t, "fy1:"+strings.Repeat("b2", 32), now)
	third := watchTestReceipt(t, "fy1:"+strings.Repeat("c3", 32), now)
	fixture.entries = []wire.FoundationLogEntry{watchTestLogEntry(t, first, 0, fixture.objects), watchTestLogEntry(t, second, 1, fixture.objects), watchTestLogEntry(t, third, 2, fixture.objects)}
	firstRoot, err := governance.TreeRoot([]string{fixture.entries[0].ObjectHash})
	if err != nil {
		t.Fatal(err)
	}
	fullRoot, err := governance.TreeRoot([]string{fixture.entries[0].ObjectHash, fixture.entries[1].ObjectHash, fixture.entries[2].ObjectHash})
	if err != nil {
		t.Fatal(err)
	}
	key := devnet.FoundationKey()
	foundationID := keys.DIDOf(key)
	headOne, err := governance.SignHead(key, foundationID, firstRoot, 1, now)
	if err != nil {
		t.Fatal(err)
	}
	headThree, err := governance.SignHead(key, foundationID, fullRoot, 3, now)
	if err != nil {
		t.Fatal(err)
	}
	fixture.heads = []wire.FoundationLogHead{headOne, headThree}
	fixture.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/foundation/log":
			fixture.logRequests++
			afterText := r.URL.Query().Get("after")
			fixture.logAfter = append(fixture.logAfter, afterText)
			after, _ := strconv.Atoi(afterText)
			page := wire.FoundationLogPage{Entries: []wire.FoundationLogEntry{}, Head: &headThree}
			if fixture.hideHeads {
				page.Head = nil
			}
			if afterText == "" {
				page.Entries = fixture.entries
			} else if after < 1 {
				page.Entries = fixture.entries[1:]
			} else if after < 2 {
				page.Entries = fixture.entries[2:]
			}
			_ = json.NewEncoder(w).Encode(page)
		case r.URL.Path == "/v1/foundation/log/heads":
			after, _ := strconv.Atoi(r.URL.Query().Get("after_size"))
			page := wire.FoundationLogHeadsPage{Heads: []wire.FoundationLogHead{}}
			if after < 1 && !fixture.hideHeads {
				page.Heads = fixture.heads
			}
			if fixture.conflicting && after < 3 {
				badRoot := strings.Repeat("f", 64)
				conflict, signErr := governance.SignHead(key, foundationID, badRoot, 3, now)
				if signErr == nil {
					page.Heads = append(page.Heads, conflict)
				}
			}
			_ = json.NewEncoder(w).Encode(page)
		case r.URL.Path == "/v1/foundation/certificates":
			_ = json.NewEncoder(w).Encode(fixture.certificates)
		case strings.HasPrefix(r.URL.Path, "/v1/foundation/log/objects/"):
			fixture.objectRequests++
			hash := strings.TrimPrefix(r.URL.Path, "/v1/foundation/log/objects/")
			object := fixture.objects[hash]
			if fixture.badObject && hash == fixture.entries[0].ObjectHash {
				object = []byte(`{"wrong":true}`)
			}
			_, _ = w.Write(object)
		default:
			http.NotFound(w, r)
		}
	})
	return fixture
}

func watchTestReceipt(t *testing.T, messageID string, now time.Time) wire.VaultReceipt {
	t.Helper()
	receipt := wire.VaultReceipt{FoundationID: keys.DIDOf(devnet.FoundationKey()), MessageID: messageID, StoredAt: wire.FormatTime(now)}
	unsigned, err := canon.EncodeWithout(receipt, "foundation_signature")
	if err != nil {
		t.Fatal(err)
	}
	receipt.FoundationSignature, err = keys.Sign(devnet.FoundationKey(), "vault-receipt", unsigned)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func watchTestClosure(t *testing.T, fixture *watchTestFixture, now time.Time) wire.FoundationKeyClosure {
	t.Helper()
	root, err := governance.TreeRoot([]string{fixture.entries[0].ObjectHash, fixture.entries[1].ObjectHash})
	if err != nil {
		t.Fatal(err)
	}
	foundationKey := devnet.FoundationKey()
	head, err := governance.SignHead(foundationKey, keys.DIDOf(foundationKey), root, 2, now)
	if err != nil {
		t.Fatal(err)
	}
	closure := wire.FoundationKeyClosure{FoundationID: keys.DIDOf(foundationKey), ClosedAt: wire.FormatTime(now.Add(time.Second)), Head: head, Voided: []string{}}
	unsigned, encodeErr := canon.EncodeWithout(closure, "release_signatures")
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	parsed, parseErr := canon.Parse(unsigned, canon.Limits{MaxDepth: 32})
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	closure.ReleaseSignatures, err = governance.SignRelease("foundation-key-closure", parsed, devnet.ReleaseKeys()[:4])
	if err != nil {
		t.Fatal(err)
	}
	return closure
}

func watchTestLogEntry(t *testing.T, receipt wire.VaultReceipt, index int64, objects map[string][]byte) wire.FoundationLogEntry {
	t.Helper()
	canonical, err := canon.Encode(receipt)
	if err != nil {
		t.Fatal(err)
	}
	var decoded wire.VaultReceipt
	if err := wire.Decode(canonical, "VaultReceipt", &decoded); err != nil {
		t.Fatalf("test receipt does not satisfy schema: %v", err)
	}
	digest := sha256.Sum256(canonical)
	hash := hex.EncodeToString(digest[:])
	objects[hash] = canonical
	return wire.FoundationLogEntry{Index: index, Kind: string(governance.LogKindVaultReceipt), ObjectHash: hash}
}

func watchTestEntryCount(t *testing.T, s *Service) int {
	t.Helper()
	count := 0
	if err := s.DB().View(func(tx store.Tx) error {
		return watchEntryValues.Scan(tx, store.Range{}, func(_ string, _ wire.FoundationLogEntry) (bool, error) { count++; return true, nil })
	}); err != nil {
		t.Fatal(err)
	}
	return count
}

func watchTestHeadCount(t *testing.T, s *Service) int {
	t.Helper()
	count := 0
	if err := s.DB().View(func(tx store.Tx) error {
		return watchHeadValues.Scan(tx, store.Range{}, func(_ string, _ watchHeldHead) (bool, error) { count++; return true, nil })
	}); err != nil {
		t.Fatal(err)
	}
	return count
}

func watchTestEvidenceKind(evidence []Evidence) string {
	if len(evidence) == 0 {
		return ""
	}
	return evidence[0].Kind
}
