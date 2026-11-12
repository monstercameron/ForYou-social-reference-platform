package monitor

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"foryou.local/foryou/internal/committee"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

type monitorExample struct {
	Genesis      wire.GenesisManifest        `json:"genesis_manifest"`
	Declarations []wire.RetentionDeclaration `json:"retention_declarations"`
	Claim        wire.SettlementClaim        `json:"settlement_claim"`
	Leaves       []wire.SettlementLeaf       `json:"settlement_leaves"`
}

func TestMonitorWitnessAndEquivocationRoutes(t *testing.T) {
	fixture := monitorReadExample(t)
	providerKey := monitorExampleProviderKey(t)
	if keys.DIDOf(providerKey) != fixture.Claim.ProviderID {
		t.Fatal("example provider key does not match signed fixture")
	}
	chain := monitorFundedTestChain(t, fixture.Claim.ProviderID, fixture.Genesis.FoundationKeyCertificates[0].Recipients.BitcoinMainnet)
	svc, err := New(Config{
		Genesis: fixture.Genesis, DataDir: t.TempDir(),
		Now:   func() time.Time { return monitorTime(t, "2027-01-20T00:00:00Z") },
		Peers: map[string]string{"monitor": "http://127.0.0.1:8447", "foundation": "http://127.0.0.1:8444"}, Faux: chain,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	member := svc.Members()[0]
	scheduleID := fixture.Genesis.FeeSchedules[0].ScheduleID
	second, secondLeaves := monitorSecondExampleClaim(t, providerKey, fixture.Claim, fixture.Declarations[0].DeclarationID, scheduleID, "second-example")
	fork, forkLeaves := monitorSecondExampleClaim(t, providerKey, fixture.Claim, fixture.Declarations[0].DeclarationID, scheduleID, "fork-example")
	if err := member.Do(func(m *committee.Monitor) error {
		if err := m.RegisterDeclaration(fixture.Declarations[0]); err != nil {
			return err
		}
		if _, err := m.Audit(fixture.Claim, fixture.Leaves, monitorTime(t, "2027-01-06T12:00:00Z"), monitorTime(t, "2027-01-06T12:00:00Z")); err != nil {
			return err
		}
		if _, err := m.Audit(second, secondLeaves, monitorTime(t, "2027-01-13T12:00:00Z"), monitorTime(t, "2027-01-13T12:00:00Z")); err != nil {
			return err
		}
		_, err := m.Audit(fork, forkLeaves, monitorTime(t, "2027-01-14T12:00:00Z"), monitorTime(t, "2027-01-14T12:00:00Z"))
		return err
	}); err != nil {
		t.Fatal(err)
	}

	did := fixture.Claim.ProviderID
	request := func(method, path string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		svc.Handler().ServeHTTP(response, httptest.NewRequest(method, path, nil))
		return response
	}
	t.Run("witness page contains signed example decisions", func(t *testing.T) {
		response := request(http.MethodGet, "/monitors/0/v1/witnesses?provider_id="+monitorEncodeValue(did)+"&network=bitcoin-mainnet")
		var page wire.MonitorWitnessesPage
		if err := wire.Decode(response.Body.Bytes(), "MonitorWitnessesPage", &page); err != nil {
			t.Fatalf("decode page: %v; body %s", err, response.Body.String())
		}
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("response = %d headers %v", response.Code, response.Header())
		}
		if len(page.Witnesses) != 2 || len(page.Refusals) != 1 || len(page.Claims) != 2 {
			t.Fatalf("page counts = witnesses %d refusals %d claims %d", len(page.Witnesses), len(page.Refusals), len(page.Claims))
		}
		if page.Witnesses[0].Sequence != 1 || page.Witnesses[1].Sequence != 2 {
			t.Fatalf("witness sequences = %d, %d", page.Witnesses[0].Sequence, page.Witnesses[1].Sequence)
		}
		if page.Claims[0].ClaimID != fixture.Claim.ClaimID || page.Claims[1].ClaimID != second.ClaimID {
			t.Fatalf("claims = %q, %q", page.Claims[0].ClaimID, page.Claims[1].ClaimID)
		}
	})
	t.Run("after sequence returns only later records", func(t *testing.T) {
		response := request(http.MethodGet, "/monitors/0/v1/witnesses?provider_id="+monitorEncodeValue(did)+"&network=bitcoin-mainnet&after_sequence=1")
		var page wire.MonitorWitnessesPage
		if err := wire.Decode(response.Body.Bytes(), "MonitorWitnessesPage", &page); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || len(page.Witnesses) != 1 || page.Witnesses[0].Sequence != 2 || len(page.Claims) != 1 || page.Claims[0].ClaimID != second.ClaimID {
			t.Fatalf("after_sequence page = %+v", page)
		}
	})
	t.Run("unseen network returns empty arrays", func(t *testing.T) {
		response := request(http.MethodGet, "/monitors/0/v1/witnesses?provider_id="+monitorEncodeValue(did)+"&network=ethereum-mainnet")
		var page wire.MonitorWitnessesPage
		if err := wire.Decode(response.Body.Bytes(), "MonitorWitnessesPage", &page); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || len(page.Witnesses)+len(page.Refusals)+len(page.Holds)+len(page.Claims) != 0 {
			t.Fatalf("empty network response = %+v", page)
		}
	})
	t.Run("head returns the page headers without a body", func(t *testing.T) {
		response := request(http.MethodHead, "/monitors/0/v1/witnesses?provider_id="+monitorEncodeValue(did)+"&network=bitcoin-mainnet")
		if response.Code != http.StatusOK || response.Body.Len() != 0 || response.Header().Get("Content-Type") != "application/json" || response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Content-Length") == "" {
			t.Fatalf("HEAD response = %d headers %v body %q", response.Code, response.Header(), response.Body.String())
		}
	})
	t.Run("equivocation proof is independently verifiable", func(t *testing.T) {
		response := request(http.MethodGet, "/monitors/0/v1/equivocations?provider_id="+monitorEncodeValue(did))
		var page wire.EquivocationsPage
		if err := wire.Decode(response.Body.Bytes(), "EquivocationsPage", &page); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || len(page.Proofs) != 1 || !settlement.VerifyEquivocation(page.Proofs[0]) {
			t.Fatalf("equivocations = %d, response %d", len(page.Proofs), response.Code)
		}
	})
	t.Run("provider without proofs returns empty array", func(t *testing.T) {
		unknown := keys.DIDOf(devnet.ProviderKey(1))
		response := request(http.MethodGet, "/monitors/0/v1/equivocations?provider_id="+monitorEncodeValue(unknown))
		if response.Code != http.StatusOK || response.Body.String() != `{"proofs":[]}` {
			t.Fatalf("empty proofs response = %d %q", response.Code, response.Body.String())
		}
	})
	t.Run("unknown equivocation parameter is invalid", func(t *testing.T) {
		response := request(http.MethodGet, "/monitors/0/v1/equivocations?provider_id="+monitorEncodeValue(did)+"&network=bitcoin-mainnet")
		monitorWantQueryError(t, response)
	})
	t.Run("post is not allowed", func(t *testing.T) {
		response := request(http.MethodPost, "/monitors/0/v1/witnesses")
		if response.Code != http.StatusMethodNotAllowed || response.Header().Get("Allow") != "GET, HEAD" {
			t.Fatalf("POST response = %d, Allow %q", response.Code, response.Header().Get("Allow"))
		}
	})
	checkInvalid := func(t *testing.T, rawQuery string) {
		t.Helper()
		monitorWantQueryError(t, request(http.MethodGet, "/monitors/0/v1/witnesses?"+rawQuery))
	}
	t.Run("missing provider ID", func(t *testing.T) { checkInvalid(t, "network=bitcoin-mainnet") })
	t.Run("unsupported network", func(t *testing.T) { checkInvalid(t, "provider_id="+monitorEncodeValue(did)+"&network=litecoin") })
	t.Run("leading zero sequence", func(t *testing.T) {
		checkInvalid(t, "provider_id="+monitorEncodeValue(did)+"&network=bitcoin-mainnet&after_sequence=01")
	})
	t.Run("negative sequence", func(t *testing.T) {
		checkInvalid(t, "provider_id="+monitorEncodeValue(did)+"&network=bitcoin-mainnet&after_sequence=-1")
	})
	t.Run("unknown parameter", func(t *testing.T) {
		checkInvalid(t, "provider_id="+monitorEncodeValue(did)+"&network=bitcoin-mainnet&x=1")
	})
	t.Run("repeated provider ID", func(t *testing.T) {
		checkInvalid(t, "provider_id="+monitorEncodeValue(did)+"&provider_id="+monitorEncodeValue(did)+"&network=bitcoin-mainnet")
	})
	t.Run("malformed percent escape", func(t *testing.T) { checkInvalid(t, "provider_id=%2g&network=bitcoin-mainnet") })
}

func TestMonitorPublicReadsLeaveJournalUnchanged(t *testing.T) {
	root := t.TempDir()
	svc := testService(t, root, &testChain{})
	defer svc.Close()
	journal := filepath.Join(root, "0", "journal.jsonl")
	stat, err := os.Stat(journal)
	if err != nil {
		t.Fatal(err)
	}
	before := stat.Size()
	did := keys.DIDOf(devnet.ProviderKey(1))
	get := func(path string) {
		t.Helper()
		response := httptest.NewRecorder()
		svc.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %q", path, response.Code, response.Body.String())
		}
	}
	witnesses := "/monitors/0/v1/witnesses?provider_id=" + monitorEncodeValue(did) + "&network=bitcoin-mainnet"
	equivocations := "/monitors/0/v1/equivocations?provider_id=" + monitorEncodeValue(did)
	get(witnesses)
	get(witnesses)
	get(equivocations)
	get(equivocations)
	stat, err = os.Stat(journal)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Size() != before {
		t.Errorf("journal size after public reads = %d; want unchanged %d", stat.Size(), before)
	}
}

func monitorWantQueryError(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	var body struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || response.Code != http.StatusBadRequest || body.Status != "invalid_query" {
		t.Fatalf("query error = %d %q; decode error %v", response.Code, response.Body.String(), err)
	}
}

func monitorReadExample(t *testing.T) monitorExample {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "committee", "testdata", "settlement.example.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture monitorExample
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func monitorExampleProviderKey(t *testing.T) keys.PrivateKey {
	t.Helper()
	seed := []byte{32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63}
	key, err := keys.NewKeyFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func monitorFundedTestChain(t *testing.T, providerID, recipient string) *testChain {
	t.Helper()
	commitment, err := settlement.DepositCommitment(providerID, wire.BitcoinMainnet)
	if err != nil {
		t.Fatal(err)
	}
	return &testChain{transfers: []faux.Transfer{{
		Network: wire.BitcoinMainnet, Recipient: recipient, AmountChainUnits: "100000", Data: hex.EncodeToString(commitment[:]),
		Sequence: 1, BlockTime: "2027-01-01T00:00:00Z", Finalized: true, Success: true,
	}}}
}

func monitorSecondExampleClaim(t *testing.T, providerKey keys.PrivateKey, previous wire.SettlementClaim, declarationID, scheduleID, label string) (wire.SettlementClaim, []wire.SettlementLeaf) {
	t.Helper()
	hash := sha256.Sum256([]byte(label))
	leaves := []wire.SettlementLeaf{{
		ReceiptHash: hex.EncodeToString(hash[:]), ScheduleID: scheduleID,
		AmountBaseUnits: "136", IssuedAt: "2027-01-12T06:00:00Z",
	}}
	root, err := settlement.TreeRoot(leaves)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := settlement.BuildClaim(providerKey, settlement.ClaimInput{
		Network: wire.BitcoinMainnet, Previous: &previous, PeriodStart: monitorTime(t, "2027-01-12T00:00:00Z"),
		PeriodEnd: monitorTime(t, "2027-01-13T00:00:00Z"), DeclarationID: declarationID,
		ReceiptCount: len(leaves), Total: big.NewInt(136), ReceiptsRoot: hex.EncodeToString(root.Hash[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	return claim, leaves
}

func monitorTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := wire.ParseTime(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
