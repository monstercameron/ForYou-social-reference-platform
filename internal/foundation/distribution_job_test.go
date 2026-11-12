package foundation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/distribution"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/quality"
	"foryou.local/foryou/internal/retention"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

func TestDistSettledUsesOneCommitteeQuorumAndInclusiveCutoff(t *testing.T) {
	t.Run("three monitors do not settle", func(t *testing.T) {
		_, data, policies, _, cutoff := distTestWitnessFixture(t, false)
		if got := distHighestSettledSequence(&data, policies, cutoff); got != 0 {
			t.Fatal("three witnesses settled a five-member committee claim")
		}
	})
	t.Run("fourth witness at cutoff settles", func(t *testing.T) {
		claim, data, policies, _, cutoff := distTestWitnessFixture(t, true)
		if got := distHighestSettledSequence(&data, policies, cutoff); got != claim.Sequence {
			t.Fatal("four witnesses at the cutoff did not settle the claim")
		}
	})
}

func TestDistQuarterPublishesVerifiedStatementsAndReports(t *testing.T) {
	fixture := distTestQuarterFixture(t, -1, false)
	service, quarterEnd, providerID := fixture.service, fixture.quarterEnd, fixture.providerID
	reports0, reports1, reports2, reports3, reports4 := fixture.reports[0], fixture.reports[1], fixture.reports[2], fixture.reports[3], fixture.reports[4]
	*fixture.now = fixture.cutoff
	if err := service.distRunDue(context.Background(), fixture.cutoff); err != nil {
		t.Fatal(err)
	}
	btcRaw, exists := service.DistributionStatement(wire.BitcoinMainnet, wire.FormatTime(quarterEnd))
	if !exists {
		t.Fatal("Bitcoin distribution statement was not published")
	}
	var btc wire.DistributionStatement
	if err := json.Unmarshal(btcRaw, &btc); err != nil {
		t.Fatal(err)
	}
	if len(btc.Entries) != 1 || btc.Entries[0].ProviderID != providerID || btc.Entries[0].QualityBP != 9000 || btc.Entries[0].RetentionYears != 1 || btc.Entries[0].ProvenQuarters != 1 || btc.Entries[0].AmountBaseUnits != "2475000" || btc.Entries[0].AccruedBaseUnits != "2475000" || btc.Entries[0].Payout != nil {
		t.Fatalf("Bitcoin provider entry = %+v; want the stated 2,475,000 accrued allocation", btc.Entries)
	}
	if len(btc.MonitorPayments) != 5 || btc.MonitorPayments[0].AmountBaseUnits != "100000" || btc.MonitorPayments[3].AmountBaseUnits != "100000" || btc.MonitorPayments[4].AmountBaseUnits != "0" || btc.MonitorPayments[0].Payout == nil || btc.MonitorPayments[3].Payout == nil || btc.MonitorPayments[4].Payout != nil {
		t.Fatalf("monitor payments = %+v; want four paid 100,000 base-unit seats and no payout to the monitor without a witness", btc.MonitorPayments)
	}
	if err := distribution.CheckStatementID(btc); err != nil {
		t.Fatal(err)
	}
	if err := service.Trust().VerifyFoundation(btc, quarterEnd, "distribution-statement", nil); err != nil {
		t.Fatalf("Trust.VerifyFoundation() error = %v", err)
	}
	ethRaw, exists := service.DistributionStatement(wire.EthereumMainnet, wire.FormatTime(quarterEnd))
	if !exists {
		t.Fatal("Ethereum distribution statement was not published")
	}
	var eth wire.DistributionStatement
	if err := json.Unmarshal(ethRaw, &eth); err != nil || len(eth.Entries) != 0 {
		t.Fatalf("Ethereum statement entries = %+v, error = %v; want none", eth.Entries, err)
	}
	served := append(append(append(append(append([]wire.QualityReport{}, reports0...), reports1...), reports2...), reports3...), reports4...)
	servedIDs := []string{
		reports0[0].ReportID, reports0[1].ReportID, reports0[2].ReportID,
		reports1[0].ReportID, reports1[1].ReportID, reports1[2].ReportID,
		reports2[0].ReportID, reports2[1].ReportID, reports2[2].ReportID,
		reports3[0].ReportID, reports3[1].ReportID, reports3[2].ReportID,
		reports4[0].ReportID, reports4[1].ReportID, reports4[2].ReportID,
	}
	sort.Strings(servedIDs)
	if !reflect.DeepEqual(btc.Entries[0].ReportIDs, servedIDs) {
		t.Fatalf("statement report IDs = %v; want the 15 served report IDs %v", btc.Entries[0].ReportIDs, servedIDs)
	}
	stored := service.QualityReports(providerID, wire.FormatTime(quarterEnd))
	sort.Slice(served, func(i, j int) bool { return served[i].ReportID < served[j].ReportID })
	sort.Slice(stored, func(i, j int) bool { return stored[i].ReportID < stored[j].ReportID })
	if !reflect.DeepEqual(stored, served) {
		t.Fatalf("stored quality reports = %d reports; want the 15 served reports used by the statement", len(stored))
	}
}

func TestDistUnreadableMonitorDoesNotStopStatement(t *testing.T) {
	t.Run("monitor whose quality reports answer 404 counts as having no score", func(t *testing.T) {
		fixture := distTestQuarterFixture(t, 4, false)
		*fixture.now = fixture.cutoff
		if err := fixture.service.distRunDue(context.Background(), fixture.cutoff); err != nil {
			t.Fatalf("distRunDue() error = %v; want the statement published", err)
		}
		raw, exists := fixture.service.DistributionStatement(wire.BitcoinMainnet, wire.FormatTime(fixture.quarterEnd))
		if !exists {
			t.Fatal("Bitcoin distribution statement was not published")
		}
		var statement wire.DistributionStatement
		if err := json.Unmarshal(raw, &statement); err != nil {
			t.Fatal(err)
		}
		reportIDs := []string{
			fixture.reports[0][0].ReportID, fixture.reports[0][1].ReportID, fixture.reports[0][2].ReportID,
			fixture.reports[1][0].ReportID, fixture.reports[1][1].ReportID, fixture.reports[1][2].ReportID,
			fixture.reports[2][0].ReportID, fixture.reports[2][1].ReportID, fixture.reports[2][2].ReportID,
			fixture.reports[3][0].ReportID, fixture.reports[3][1].ReportID, fixture.reports[3][2].ReportID,
		}
		sort.Strings(reportIDs)
		if len(statement.Entries) != 1 || statement.Entries[0].QualityBP != 9000 || statement.Entries[0].AmountBaseUnits != "2475000" || !reflect.DeepEqual(statement.Entries[0].ReportIDs, reportIDs) {
			t.Fatalf("statement entries = %+v; want quality 9000, 2,475,000 base units and the 12 reports of the four readable monitors", statement.Entries)
		}
	})
	t.Run("reports whose evidence is not served are not used", func(t *testing.T) {
		distTestWithheldEvidence = keys.DIDOf(devnet.MonitorKey(0))
		t.Cleanup(func() { distTestWithheldEvidence = "" })
		fixture := distTestQuarterFixture(t, -1, false)
		*fixture.now = fixture.cutoff
		if err := fixture.service.distRunDue(context.Background(), fixture.cutoff); err != nil {
			t.Fatalf("distRunDue() error = %v; want the statement published", err)
		}
		raw, exists := fixture.service.DistributionStatement(wire.BitcoinMainnet, wire.FormatTime(fixture.quarterEnd))
		if !exists {
			t.Fatal("Bitcoin distribution statement was not published")
		}
		var statement wire.DistributionStatement
		if err := json.Unmarshal(raw, &statement); err != nil {
			t.Fatal(err)
		}
		reportIDs := []string{
			fixture.reports[1][0].ReportID, fixture.reports[1][1].ReportID, fixture.reports[1][2].ReportID,
			fixture.reports[2][0].ReportID, fixture.reports[2][1].ReportID, fixture.reports[2][2].ReportID,
			fixture.reports[3][0].ReportID, fixture.reports[3][1].ReportID, fixture.reports[3][2].ReportID,
			fixture.reports[4][0].ReportID, fixture.reports[4][1].ReportID, fixture.reports[4][2].ReportID,
		}
		sort.Strings(reportIDs)
		if len(statement.Entries) != 1 || statement.Entries[0].QualityBP != 9000 || !reflect.DeepEqual(statement.Entries[0].ReportIDs, reportIDs) {
			t.Fatalf("statement entries = %+v; want quality 9000 from the 12 reports of the four monitors that serve their evidence", statement.Entries)
		}
	})
	t.Run("unreadable settlement claims publish nothing and fail the run", func(t *testing.T) {
		fixture := distTestQuarterFixture(t, -1, true)
		*fixture.now = fixture.cutoff
		if err := fixture.service.distRunDue(context.Background(), fixture.cutoff); err == nil {
			t.Fatal("distribution succeeded without the provider's settlement claims")
		}
		if _, exists := fixture.service.DistributionStatement(wire.BitcoinMainnet, wire.FormatTime(fixture.quarterEnd)); exists {
			t.Fatal("Bitcoin distribution statement was published without the provider's settlement claims")
		}
		if _, exists := fixture.service.DistributionStatement(wire.EthereumMainnet, wire.FormatTime(fixture.quarterEnd)); exists {
			t.Fatal("Ethereum distribution statement was published without the provider's settlement claims")
		}
	})
}

// distTestQuarterFixture serves the devnet quarter from five committee monitors and one provider. reportsUnreadable names the monitor whose quality-reports route answers 404, or -1 for none; claimsUnreadable fails the provider's settlement claims route.
func distTestQuarterFixture(t *testing.T, reportsUnreadable int, claimsUnreadable bool) distTestFixture {
	t.Helper()
	calendar, err := devnet.Calendar(30 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	quarterStart, _ := calendar.Quarter(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
	quarterEnd := quarterStart.Add(90 * calendar.Day())
	cutoff := calendar.CutOff(quarterEnd)
	now := quarterStart
	providerKey := devnet.ProviderKey(0)
	providerID := keys.DIDOf(providerKey)
	declaration, err := retention.Declare(providerKey, 10*31536000, wire.PayoutRecipients{BitcoinMainnet: devnet.Recipient("provider", wire.BitcoinMainnet)}, quarterStart)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := settlement.BuildClaim(providerKey, settlement.ClaimInput{Network: wire.BitcoinMainnet, PeriodStart: quarterStart.Add(3 * calendar.Day()), PeriodEnd: quarterStart.Add(4 * calendar.Day()), DeclarationID: declaration.DeclarationID, ReceiptCount: 1, Total: big.NewInt(10_000_000), ReceiptsRoot: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	witnessAt := quarterStart.Add(5 * calendar.Day())
	witness0, err := settlement.SignWitness(devnet.MonitorKey(0), claim, big.NewInt(0), witnessAt)
	if err != nil {
		t.Fatal(err)
	}
	witness1, err := settlement.SignWitness(devnet.MonitorKey(1), claim, big.NewInt(0), witnessAt)
	if err != nil {
		t.Fatal(err)
	}
	witness2, err := settlement.SignWitness(devnet.MonitorKey(2), claim, big.NewInt(0), witnessAt)
	if err != nil {
		t.Fatal(err)
	}
	witness3, err := settlement.SignWitness(devnet.MonitorKey(3), claim, big.NewInt(0), witnessAt)
	if err != nil {
		t.Fatal(err)
	}
	monitor0ID := keys.DIDOf(devnet.MonitorKey(0))
	monitor1ID := keys.DIDOf(devnet.MonitorKey(1))
	monitor2ID := keys.DIDOf(devnet.MonitorKey(2))
	monitor3ID := keys.DIDOf(devnet.MonitorKey(3))
	monitor4ID := keys.DIDOf(devnet.MonitorKey(4))
	reports0 := distTestQualityReports(t, monitor0ID, providerID, quarterStart, cutoff, devnet.MonitorKey(0))
	reports1 := distTestQualityReports(t, monitor1ID, providerID, quarterStart, cutoff, devnet.MonitorKey(1))
	reports2 := distTestQualityReports(t, monitor2ID, providerID, quarterStart, cutoff, devnet.MonitorKey(2))
	reports3 := distTestQualityReports(t, monitor3ID, providerID, quarterStart, cutoff, devnet.MonitorKey(3))
	reports4 := distTestQualityReports(t, monitor4ID, providerID, quarterStart, cutoff, devnet.MonitorKey(4))
	monitor0Handler := distTestMonitorHandler(t, witness0, reports0)
	if reportsUnreadable == 0 {
		monitor0Handler = distTestRouteStatus(monitor0Handler, "/v1/quality-reports", http.StatusNotFound)
	}
	monitor1Handler := distTestMonitorHandler(t, witness1, reports1)
	if reportsUnreadable == 1 {
		monitor1Handler = distTestRouteStatus(monitor1Handler, "/v1/quality-reports", http.StatusNotFound)
	}
	monitor2Handler := distTestMonitorHandler(t, witness2, reports2)
	if reportsUnreadable == 2 {
		monitor2Handler = distTestRouteStatus(monitor2Handler, "/v1/quality-reports", http.StatusNotFound)
	}
	monitor3Handler := distTestMonitorHandler(t, witness3, reports3)
	if reportsUnreadable == 3 {
		monitor3Handler = distTestRouteStatus(monitor3Handler, "/v1/quality-reports", http.StatusNotFound)
	}
	monitor4Handler := distTestMutableMonitorHandler(t, func() *wire.SettlementWitness { return nil }, reports4)
	if reportsUnreadable == 4 {
		monitor4Handler = distTestRouteStatus(monitor4Handler, "/v1/quality-reports", http.StatusNotFound)
	}
	monitor0 := httptest.NewServer(monitor0Handler)
	monitor1 := httptest.NewServer(monitor1Handler)
	monitor2 := httptest.NewServer(monitor2Handler)
	monitor3 := httptest.NewServer(monitor3Handler)
	monitor4 := httptest.NewServer(monitor4Handler)
	providerHandler := distTestProviderHandler(t, claim, declaration)
	if claimsUnreadable {
		providerHandler = distTestRouteStatus(providerHandler, "/v1/settlement-claims", http.StatusInternalServerError)
	}
	provider := httptest.NewServer(providerHandler)
	stopServers := func() {
		monitor0.Close()
		monitor1.Close()
		monitor2.Close()
		monitor3.Close()
		monitor4.Close()
		provider.Close()
	}
	t.Cleanup(stopServers)
	fauxLedger, err := faux.Open(faux.Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, &now)
	cfg.Calendar = calendar
	cfg.Providers = []string{provider.URL}
	cfg.Monitors = map[string]string{monitor0ID: monitor0.URL, monitor1ID: monitor1.URL, monitor2ID: monitor2.URL, monitor3ID: monitor3.URL, monitor4ID: monitor4.URL}
	cfg.MonitorPayouts = map[string]map[string]string{
		monitor0ID: {wire.BitcoinMainnet: devnet.Recipient("monitor/0", wire.BitcoinMainnet)},
		monitor1ID: {wire.BitcoinMainnet: devnet.Recipient("monitor/1", wire.BitcoinMainnet)},
		monitor2ID: {wire.BitcoinMainnet: devnet.Recipient("monitor/2", wire.BitcoinMainnet)},
		monitor3ID: {wire.BitcoinMainnet: devnet.Recipient("monitor/3", wire.BitcoinMainnet)},
		monitor4ID: {wire.BitcoinMainnet: devnet.Recipient("monitor/4", wire.BitcoinMainnet)},
	}
	cfg.Faux = fauxLedger
	service, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { service.Close() })
	return distTestFixture{service: service, chain: fauxLedger, stopServers: stopServers, now: &now, quarterEnd: quarterEnd, cutoff: cutoff, providerID: providerID, reports: [][]wire.QualityReport{reports0, reports1, reports2, reports3, reports4}}
}

type distTestFixture struct {
	service *Service
	chain   *faux.Ledger
	// stopServers takes the provider and every monitor offline.
	stopServers func()
	now         *time.Time
	quarterEnd  time.Time
	cutoff      time.Time
	providerID  string
	reports     [][]wire.QualityReport
}

func distTestRouteStatus(handler http.Handler, path string, status int) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == path {
			w.WriteHeader(status)
			return
		}
		handler.ServeHTTP(w, r)
	})
}

func TestDistLateQuorumCountsClaimNextQuarter(t *testing.T) {
	calendar, err := devnet.Calendar(30 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	quarterStart, _ := calendar.Quarter(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
	firstEnd := quarterStart.Add(90 * calendar.Day())
	firstCutoff := calendar.CutOff(firstEnd)
	secondEnd := firstEnd.Add(90 * calendar.Day())
	secondCutoff := calendar.CutOff(secondEnd)
	now := quarterStart
	providerKey := devnet.ProviderKey(0)
	providerID := keys.DIDOf(providerKey)
	declaration, err := retention.Declare(providerKey, 10*31536000, wire.PayoutRecipients{BitcoinMainnet: devnet.Recipient("provider", wire.BitcoinMainnet)}, quarterStart)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := settlement.BuildClaim(providerKey, settlement.ClaimInput{Network: wire.BitcoinMainnet, PeriodStart: quarterStart.Add(3 * calendar.Day()), PeriodEnd: quarterStart.Add(4 * calendar.Day()), DeclarationID: declaration.DeclarationID, ReceiptCount: 1, Total: big.NewInt(10_000_000), ReceiptsRoot: strings.Repeat("a", 64)})
	if err != nil {
		t.Fatal(err)
	}
	witnessAt := quarterStart.Add(5 * calendar.Day())
	witness0, err := settlement.SignWitness(devnet.MonitorKey(0), claim, big.NewInt(0), witnessAt)
	if err != nil {
		t.Fatal(err)
	}
	witness1, err := settlement.SignWitness(devnet.MonitorKey(1), claim, big.NewInt(0), witnessAt)
	if err != nil {
		t.Fatal(err)
	}
	witness2, err := settlement.SignWitness(devnet.MonitorKey(2), claim, big.NewInt(0), witnessAt)
	if err != nil {
		t.Fatal(err)
	}
	witness3, err := settlement.SignWitness(devnet.MonitorKey(3), claim, big.NewInt(0), firstCutoff.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	monitor0ID := keys.DIDOf(devnet.MonitorKey(0))
	monitor1ID := keys.DIDOf(devnet.MonitorKey(1))
	monitor2ID := keys.DIDOf(devnet.MonitorKey(2))
	monitor3ID := keys.DIDOf(devnet.MonitorKey(3))
	monitor4ID := keys.DIDOf(devnet.MonitorKey(4))
	monitor0 := httptest.NewServer(distTestMutableMonitorHandler(t, func() *wire.SettlementWitness { return &witness0 }, []wire.QualityReport{}))
	defer monitor0.Close()
	monitor1 := httptest.NewServer(distTestMutableMonitorHandler(t, func() *wire.SettlementWitness { return &witness1 }, []wire.QualityReport{}))
	defer monitor1.Close()
	monitor2 := httptest.NewServer(distTestMutableMonitorHandler(t, func() *wire.SettlementWitness { return &witness2 }, []wire.QualityReport{}))
	defer monitor2.Close()
	var lateWitness *wire.SettlementWitness
	monitor3 := httptest.NewServer(distTestMutableMonitorHandler(t, func() *wire.SettlementWitness { return lateWitness }, []wire.QualityReport{}))
	defer monitor3.Close()
	monitor4 := httptest.NewServer(distTestMutableMonitorHandler(t, func() *wire.SettlementWitness { return nil }, []wire.QualityReport{}))
	defer monitor4.Close()
	provider := httptest.NewServer(distTestProviderHandler(t, claim, declaration))
	defer provider.Close()
	fauxLedger, err := faux.Open(faux.Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, &now)
	cfg.Calendar = calendar
	cfg.Providers = []string{provider.URL}
	cfg.Monitors = map[string]string{monitor0ID: monitor0.URL, monitor1ID: monitor1.URL, monitor2ID: monitor2.URL, monitor3ID: monitor3.URL, monitor4ID: monitor4.URL}
	cfg.MonitorPayouts = map[string]map[string]string{
		monitor0ID: {wire.BitcoinMainnet: devnet.Recipient("monitor/0", wire.BitcoinMainnet)},
		monitor1ID: {wire.BitcoinMainnet: devnet.Recipient("monitor/1", wire.BitcoinMainnet)},
		monitor2ID: {wire.BitcoinMainnet: devnet.Recipient("monitor/2", wire.BitcoinMainnet)},
		monitor3ID: {wire.BitcoinMainnet: devnet.Recipient("monitor/3", wire.BitcoinMainnet)},
		monitor4ID: {wire.BitcoinMainnet: devnet.Recipient("monitor/4", wire.BitcoinMainnet)},
	}
	cfg.Faux = fauxLedger
	service, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	now = firstCutoff
	if err := service.distRunDue(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	firstRaw, exists := service.DistributionStatement(wire.BitcoinMainnet, wire.FormatTime(firstEnd))
	if !exists {
		t.Fatal("first quarter statement was not published")
	}
	var first wire.DistributionStatement
	if err := json.Unmarshal(firstRaw, &first); err != nil || len(first.Entries) != 0 {
		t.Fatalf("first quarter entries = %+v, error = %v; want no unsettled claim", first.Entries, err)
	}
	lateWitness = &witness3
	now = secondCutoff
	if err := service.distRunDue(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	secondRaw, exists := service.DistributionStatement(wire.BitcoinMainnet, wire.FormatTime(secondEnd))
	if !exists {
		t.Fatal("second quarter statement was not published")
	}
	var second wire.DistributionStatement
	if err := json.Unmarshal(secondRaw, &second); err != nil {
		t.Fatal(err)
	}
	if len(second.Entries) != 1 || second.Entries[0].ProviderID != providerID || second.Entries[0].SettledFeesBaseUnits != "10000000" {
		t.Fatalf("second quarter entries = %+v; want the late-settled claim counted once", second.Entries)
	}
}

func distTestProviderHandler(t *testing.T, claim wire.SettlementClaim, declaration wire.RetentionDeclaration) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/v1/settlement-claims":
			after, _ := strconv.ParseUint(r.URL.Query().Get("after_sequence"), 10, 64)
			claims := []wire.SettlementClaim{}
			if after == 0 && claim.Network == r.URL.Query().Get("network") {
				claims = []wire.SettlementClaim{claim}
			}
			_ = json.NewEncoder(w).Encode(wire.SettlementClaimsPage{Claims: claims})
		case r.URL.Path == "/v1/retention":
			_ = json.NewEncoder(w).Encode(wire.RetentionDeclarationsPage{Declarations: []wire.RetentionDeclaration{declaration}})
		case strings.HasPrefix(r.URL.Path, "/v1/successions/"):
			_, _ = w.Write([]byte(`{"successions":[]}`))
		default:
			http.NotFound(w, r)
		}
	})
}

func distTestMonitorHandler(t *testing.T, witness wire.SettlementWitness, reports []wire.QualityReport) http.Handler {
	t.Helper()
	return distTestMutableMonitorHandler(t, func() *wire.SettlementWitness { return &witness }, reports)
}

func distTestMutableMonitorHandler(t *testing.T, getWitness func() *wire.SettlementWitness, reports []wire.QualityReport) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/v1/witnesses":
			witnesses := []wire.SettlementWitness{}
			after, _ := strconv.ParseUint(r.URL.Query().Get("after_sequence"), 10, 64)
			if witness := getWitness(); witness != nil && uint64(witness.Sequence) > after {
				witnesses = append(witnesses, *witness)
			}
			_ = json.NewEncoder(w).Encode(wire.MonitorWitnessesPage{Witnesses: witnesses, Refusals: []wire.SettlementRefusal{}, Holds: []wire.SettlementHold{}, Claims: []wire.SettlementClaim{}})
		case "/v1/equivocations":
			_, _ = w.Write([]byte(`{"proofs":[]}`))
		case "/v1/quality-reports":
			_ = json.NewEncoder(w).Encode(wire.QualityReportsPage{Reports: reports})
		default:
			distTestServeEvidence(w, r, reports)
		}
	})
}

// distTestEvidence is the methodology and observation set behind a fixture report:
// 20 availability probes and 20 retrievals in its period, 18 of each passing in 100 ms.
func distTestEvidence(monitorID, providerID string, start, end time.Time) (string, wire.QualityObservationSet) {
	ok, failed, latency := int64(200), int64(500), int64(100)
	set := wire.QualityObservationSet{ProviderID: providerID, MonitorID: monitorID, PeriodStart: wire.FormatTime(start), PeriodEnd: wire.FormatTime(end)}
	for i := range 20 {
		status, outcome := &ok, "pass"
		if i >= 18 {
			status, outcome = &failed, "fail"
		}
		messageID := "fy1:" + strings.Repeat("0", 62) + fmt.Sprintf("%02d", i)
		at := wire.FormatTime(start.Add(time.Duration(i) * time.Second))
		set.Observations = append(set.Observations,
			wire.QualityObservation{Kind: "availability", StartedAt: at, DurationMs: latency, Outcome: outcome, HTTPStatus: status, WithinLimits: true},
			wire.QualityObservation{Kind: "retrieval", MessageID: &messageID, StartedAt: at, DurationMs: latency, Outcome: outcome, HTTPStatus: status, WithinLimits: true})
	}
	return "Fixture methodology: 20 availability probes and 20 retrievals, one pair a second from the period start.", set
}

func distTestReport(t *testing.T, monitorID, providerID string, start, end, created time.Time, key keys.PrivateKey) wire.QualityReport {
	t.Helper()
	seed := keys.EncodeB64URL(make([]byte, 32))
	commitment, err := quality.SeedCommitment(seed)
	if err != nil {
		t.Fatal(err)
	}
	methodology, set := distTestEvidence(monitorID, providerID, start, end)
	observationsDigest, err := quality.ObservationsDigest(set)
	if err != nil {
		t.Fatal(err)
	}
	methodologyDigest := sha256.Sum256([]byte(methodology))
	latency := int64(100)
	report, err := quality.Sign(wire.QualityReport{MonitorID: monitorID, ProviderID: providerID, PeriodStart: wire.FormatTime(start), PeriodEnd: wire.FormatTime(end), CreatedAt: wire.FormatTime(created), MethodologyDigest: hex.EncodeToString(methodologyDigest[:]), ObservationsDigest: observationsDigest, SampleSeed: seed, SampleSeedCommitment: commitment, SampleCoverage: "complete", AvailabilityAttempts: 20, AvailabilitySuccesses: 18, RetrievalAttempts: 20, RetrievalPasses: 18, RestoreAttempts: 0, RestorePasses: 0, LatencyP50Ms: &latency, LatencyP95Ms: &latency, LatencyP99Ms: &latency, Sponsors: []string{keys.DIDOf(devnet.ReplicaKey())}, Conflicts: "none", SampleFrameDigest: strings.Repeat("c", 64)}, key)
	if err != nil {
		t.Fatal(err)
	}
	if err := quality.Verify(report, set, []byte(methodology)); err != nil {
		t.Fatalf("fixture report does not verify against its own evidence: %v", err)
	}
	return report
}

// distTestWithheldEvidence names the monitor whose fake does not serve the evidence of its reports.
var distTestWithheldEvidence string

// distTestServeEvidence answers the two evidence routes for the reports a fake monitor lists.
func distTestServeEvidence(w http.ResponseWriter, r *http.Request, reports []wire.QualityReport) {
	for _, report := range reports {
		if report.MonitorID == distTestWithheldEvidence {
			break
		}
		base := "/v1/quality-reports/" + report.ReportID
		start, _ := wire.ParseTime(report.PeriodStart)
		end, _ := wire.ParseTime(report.PeriodEnd)
		methodology, set := distTestEvidence(report.MonitorID, report.ProviderID, start, end)
		switch r.URL.Path {
		case base + "/methodology":
			w.Header().Set("Content-Type", "text/plain; charset=utf-8")
			_, _ = w.Write([]byte(methodology))
			return
		case base + "/observations":
			body, _ := canon.Encode(set)
			_, _ = w.Write(body)
			return
		}
	}
	http.NotFound(w, r)
}

func distTestQualityReports(t *testing.T, monitorID, providerID string, start, created time.Time, key keys.PrivateKey) []wire.QualityReport {
	t.Helper()
	firstStart := start.Add(30 * time.Second)
	firstEnd := firstStart.Add(20 * 30 * time.Second)
	secondEnd := firstEnd.Add(20 * 30 * time.Second)
	thirdEnd := secondEnd.Add(20 * 30 * time.Second)
	return []wire.QualityReport{
		distTestReport(t, monitorID, providerID, firstStart, firstEnd, created, key),
		distTestReport(t, monitorID, providerID, firstEnd, secondEnd, created, key),
		distTestReport(t, monitorID, providerID, secondEnd, thirdEnd, created, key),
	}
}

func TestDistPayoutIsReusedAfterServiceRestart(t *testing.T) {
	now := time.Date(2027, 4, 15, 0, 0, 0, 0, time.UTC)
	ledger, err := faux.Open(faux.Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, &now)
	cfg.DataDir = t.TempDir()
	cfg.Faux = ledger
	service, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	quarterEnd := wire.FormatTime(time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC))
	recipient := devnet.Recipient("distribution-test", wire.BitcoinMainnet)
	first, err := service.distMakeTransfer(context.Background(), wire.BitcoinMainnet, quarterEnd, "provider", keys.DIDOf(devnet.ProviderKey(0)), recipient, big.NewInt(2_475_000))
	if err != nil {
		t.Fatal(err)
	}
	if err := service.Close(); err != nil {
		t.Fatal(err)
	}
	service, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	second, err := service.distMakeTransfer(context.Background(), wire.BitcoinMainnet, quarterEnd, "provider", keys.DIDOf(devnet.ProviderKey(0)), recipient, big.NewInt(2_475_000))
	if err != nil {
		t.Fatal(err)
	}
	if first.TxID != second.TxID {
		t.Fatalf("restarted payout txid = %q, want recorded txid %q", second.TxID, first.TxID)
	}
	transfers, err := ledger.Transfers(context.Background(), faux.Query{Network: wire.BitcoinMainnet, Limit: 10})
	if err != nil || len(transfers) != 1 {
		t.Fatalf("faux transfer count = %d, error = %v; want one transfer", len(transfers), err)
	}
}

type distFailAfterSubmitChain struct {
	ledger *faux.Ledger
	failed bool
}

func (c *distFailAfterSubmitChain) Submit(ctx context.Context, request faux.SubmitRequest) (faux.Transfer, error) {
	transfer, err := c.ledger.Submit(ctx, request)
	if err == nil && !c.failed {
		c.failed = true
		return faux.Transfer{}, errors.New("simulated crash after faux submission")
	}
	return transfer, err
}

func (c *distFailAfterSubmitChain) Transfer(ctx context.Context, network, id string) (faux.Transfer, error) {
	return c.ledger.Transfer(ctx, network, id)
}

func (c *distFailAfterSubmitChain) Transfers(ctx context.Context, query faux.Query) ([]faux.Transfer, error) {
	return c.ledger.Transfers(ctx, query)
}

func TestDistSubmitCrashRetriesWithoutDuplicateTransfer(t *testing.T) {
	now := time.Date(2027, 4, 15, 0, 0, 0, 0, time.UTC)
	ledger, err := faux.Open(faux.Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, &now)
	cfg.Faux = &distFailAfterSubmitChain{ledger: ledger}
	service, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	policy := service.Policies()[0]
	start := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC)
	cutoff := time.Date(2027, 4, 15, 0, 0, 0, 0, time.UTC)
	providerID := keys.DIDOf(devnet.ProviderKey(0))
	score := 9000
	reportSpan := func(id, hexDigit string) distribution.ReportSpan {
		return distribution.ReportSpan{ReportID: "fyqr2:" + strings.Repeat(hexDigit, 64), MonitorID: id, Start: start, End: start.Add(90 * 24 * time.Hour), CreatedAt: cutoff, Score: &score}
	}
	reports := []distribution.ReportSpan{
		reportSpan(policy.Monitors[0], "a"),
		reportSpan(policy.Monitors[1], "b"),
		reportSpan(policy.Monitors[2], "c"),
		reportSpan(policy.Monitors[3], "d"),
		reportSpan(policy.Monitors[4], "e"),
	}
	provider := distribution.ProviderInput{ChainID: providerID, SettledFees: big.NewInt(10_000_000), DeclaredSeconds: []int64{315_360_000}, Reports: reports, PayoutRecipient: devnet.Recipient("provider", wire.BitcoinMainnet)}
	prior := wire.DistributionStatement{Entries: []wire.DistributionEntry{{ProviderID: providerID, AccruedBaseUnits: "7525000"}}}
	input := distribution.Input{Policy: policy, Network: wire.BitcoinMainnet, Start: start, End: end, Day: 24 * time.Hour, CutOff: cutoff, Members: []distribution.MemberSpan{
		{MonitorID: policy.Monitors[0], Start: start, End: end},
		{MonitorID: policy.Monitors[1], Start: start, End: end},
		{MonitorID: policy.Monitors[2], Start: start, End: end},
		{MonitorID: policy.Monitors[3], Start: start, End: end},
		{MonitorID: policy.Monitors[4], Start: start, End: end},
	}, Providers: []distribution.ProviderInput{provider}, Seats: map[string]distribution.SeatEvidence{
		policy.Monitors[0]: {Counted: 1},
		policy.Monitors[1]: {Counted: 1},
		policy.Monitors[2]: {Counted: 1},
		policy.Monitors[3]: {Counted: 1},
		policy.Monitors[4]: {Counted: 1},
	}, Earlier: []wire.DistributionStatement{prior}}
	if err := service.distPublish(context.Background(), input, nil, nil); err == nil {
		t.Fatal("first distribution attempt succeeded despite simulated post-submit crash")
	}
	if err := service.distPublish(context.Background(), input, nil, nil); err != nil {
		t.Fatal(err)
	}
	raw, exists := service.DistributionStatement(wire.BitcoinMainnet, wire.FormatTime(end))
	if !exists {
		t.Fatal("distribution statement was not published after retry")
	}
	var statement wire.DistributionStatement
	if err := json.Unmarshal(raw, &statement); err != nil {
		t.Fatal(err)
	}
	transfers, err := ledger.Transfers(context.Background(), faux.Query{Network: wire.BitcoinMainnet, Limit: 10})
	if err != nil || len(transfers) != 1 || statement.Entries[0].Payout == nil || statement.Entries[0].Payout.TxID != transfers[0].TxID {
		t.Fatalf("transfers = %+v, statement entry = %+v, error = %v; want one transfer named by payout", transfers, statement.Entries[0], err)
	}
}

func TestDistProviderPayoutUsesPriorAccrualAndWholeSatoshis(t *testing.T) {
	now := time.Date(2027, 4, 15, 0, 0, 0, 0, time.UTC)
	ledger, err := faux.Open(faux.Config{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	cfg := testConfig(t, &now)
	cfg.Faux = ledger
	service, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	policy := service.Policies()[0]
	start := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC)
	cutoff := time.Date(2027, 4, 15, 0, 0, 0, 0, time.UTC)
	providerID := keys.DIDOf(devnet.ProviderKey(0))
	score := 9000
	reportStart := start
	reportEnd := start.Add(60 * 24 * time.Hour)
	provider := distribution.ProviderInput{ChainID: providerID, SettledFees: big.NewInt(10_000_000), DeclaredSeconds: []int64{315_360_000}, Reports: []distribution.ReportSpan{
		{ReportID: "fyqr2:" + strings.Repeat("a", 64), MonitorID: policy.Monitors[0], Start: reportStart, End: reportEnd, CreatedAt: cutoff, Score: &score},
		{ReportID: "fyqr2:" + strings.Repeat("b", 64), MonitorID: policy.Monitors[1], Start: reportStart, End: reportEnd, CreatedAt: cutoff, Score: &score},
		{ReportID: "fyqr2:" + strings.Repeat("c", 64), MonitorID: policy.Monitors[2], Start: reportStart, End: reportEnd, CreatedAt: cutoff, Score: &score},
		{ReportID: "fyqr2:" + strings.Repeat("d", 64), MonitorID: policy.Monitors[3], Start: reportStart, End: reportEnd, CreatedAt: cutoff, Score: &score},
		{ReportID: "fyqr2:" + strings.Repeat("e", 64), MonitorID: policy.Monitors[4], Start: reportStart, End: reportEnd, CreatedAt: cutoff, Score: &score},
	}, PayoutRecipient: devnet.Recipient("provider", wire.BitcoinMainnet)}
	seats := map[string]distribution.SeatEvidence{
		policy.Monitors[0]: {Counted: 1},
		policy.Monitors[1]: {Counted: 1},
		policy.Monitors[2]: {Counted: 1},
		policy.Monitors[3]: {Counted: 1},
		policy.Monitors[4]: {Counted: 1},
	}
	prior := wire.DistributionStatement{Entries: []wire.DistributionEntry{{ProviderID: providerID, ProvenQuarters: 0, AccruedBaseUnits: "7525700"}}}
	input := distribution.Input{Policy: policy, Network: wire.BitcoinMainnet, Start: start, End: end, Day: 24 * time.Hour, CutOff: cutoff, Members: []distribution.MemberSpan{
		{MonitorID: policy.Monitors[0], Start: start, End: end},
		{MonitorID: policy.Monitors[1], Start: start, End: end},
		{MonitorID: policy.Monitors[2], Start: start, End: end},
		{MonitorID: policy.Monitors[3], Start: start, End: end},
		{MonitorID: policy.Monitors[4], Start: start, End: end},
	}, Providers: []distribution.ProviderInput{provider}, Seats: seats, Earlier: []wire.DistributionStatement{prior}}
	if err := service.distPublish(context.Background(), input, nil, nil); err != nil {
		t.Fatal(err)
	}
	raw, exists := service.DistributionStatement(wire.BitcoinMainnet, wire.FormatTime(end))
	if !exists {
		t.Fatal("distribution statement was not published")
	}
	var statement wire.DistributionStatement
	if err := json.Unmarshal(raw, &statement); err != nil {
		t.Fatal(err)
	}
	entry := statement.Entries[0]
	if entry.AmountBaseUnits != "2475000" || entry.AccruedBaseUnits != "700" || entry.Payout == nil || entry.Payout.Network != wire.BitcoinMainnet || entry.Payout.OutputIndex == nil {
		t.Fatalf("provider entry = %+v; want 7,525,700 + 2,475,000 msat paid as 10,000 whole satoshis with 700 msat still accrued", entry)
	}
	transfers, err := ledger.Transfers(context.Background(), faux.Query{Network: wire.BitcoinMainnet, Limit: 10})
	if err != nil || len(transfers) != 1 || transfers[0].AmountChainUnits != "10000" || transfers[0].TxID != entry.Payout.TxID {
		t.Fatalf("provider faux transfers = %+v, error = %v; want one 10,000-satoshi transfer matching the entry", transfers, err)
	}
}

func distTestWitnessFixture(t *testing.T, includeFourth bool) (wire.SettlementClaim, distProviderData, []wire.DistributionPolicy, map[string][]distribution.Interval, time.Time) {
	t.Helper()
	start := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC)
	cutoff := time.Date(2027, 4, 15, 0, 0, 0, 0, time.UTC)
	claim, err := settlement.BuildClaim(devnet.ProviderKey(0), settlement.ClaimInput{Network: wire.BitcoinMainnet, PeriodStart: start.AddDate(0, 0, 1), PeriodEnd: start.AddDate(0, 0, 2), DeclarationID: "fyrd2:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ReceiptCount: 1, Total: big.NewInt(100), ReceiptsRoot: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"})
	if err != nil {
		t.Fatal(err)
	}
	policies := devnet.Policies()
	membership := distribution.Membership(policies, start, end)
	witnessAt := cutoff.Add(-time.Second)
	w1, err := settlement.SignWitness(devnet.MonitorKey(0), claim, big.NewInt(0), witnessAt)
	if err != nil {
		t.Fatal(err)
	}
	w2, err := settlement.SignWitness(devnet.MonitorKey(1), claim, big.NewInt(0), witnessAt)
	if err != nil {
		t.Fatal(err)
	}
	w3, err := settlement.SignWitness(devnet.MonitorKey(2), claim, big.NewInt(0), witnessAt)
	if err != nil {
		t.Fatal(err)
	}
	w4, err := settlement.SignWitness(devnet.MonitorKey(3), claim, big.NewInt(0), cutoff)
	if err != nil {
		t.Fatal(err)
	}
	monitorIDs := policies[0].Monitors
	data := distProviderData{chain: []wire.SettlementClaim{claim}, witnesses: map[string][]wire.SettlementWitness{
		monitorIDs[0]: {w1},
		monitorIDs[1]: {w2},
		monitorIDs[2]: {w3},
	}}
	if includeFourth {
		data.witnesses[monitorIDs[3]] = []wire.SettlementWitness{w4}
	}
	return claim, data, policies, membership, cutoff
}

func TestDistMissingPolicyMonitorURLAbortsQuarter(t *testing.T) {
	calendar, err := devnet.Calendar(30 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	quarterStart, _ := calendar.Quarter(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC))
	quarterEnd := quarterStart.Add(90 * calendar.Day())
	now := quarterStart
	cfg := testConfig(t, &now)
	cfg.Calendar = calendar
	cfg.Monitors = map[string]string{}
	service, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	now = calendar.CutOff(quarterEnd)
	if err := service.distRunDue(context.Background(), now); err == nil {
		t.Fatal("distribution succeeded without configured policy monitor URLs")
	}
	if _, exists := service.DistributionStatement(wire.BitcoinMainnet, wire.FormatTime(quarterEnd)); exists {
		t.Fatal("distribution statement exists after missing monitor data")
	}
}
