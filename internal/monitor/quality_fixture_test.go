package monitor

import (
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/committee"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/discovery"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/message"
	"foryou.local/foryou/internal/retention"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

// qualitySettledAt is when the fixture's claim was audited; 2027-01-01T00:00:00Z starts a 45-minute dev quarter.
var qualitySettledAt = time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)

type qualityVaultMode int

const (
	qualityVaultServes qualityVaultMode = iota
	qualityVaultNotFound
	qualityVaultDown
)

// qualityWorldOptions describes the provider's misbehavior and the shape of the fixture.
type qualityWorldOptions struct {
	settled            int
	unsettled          int
	members            int
	vault              qualityVaultMode
	unavailableListing int
	hangListings       bool
	wrongIDRetrievals  int
	goneRetrievals     int
}

// qualityWorld is one provider and one vault answering as the API catalog says, plus a monitor service whose
// members audited one claim holding the settled messages.
type qualityWorld struct {
	t        *testing.T
	service  *Service
	now      time.Time
	provider Provider
	ids      []string
	options  qualityWorldOptions
	vaultID  string

	mu             sync.Mutex
	raw            map[string][]byte
	changes        [][]byte
	listings       int
	retrievals     int
	providerHits   int
	firstProbeSeen func()
}

func qualityNewWorld(t *testing.T, options qualityWorldOptions) *qualityWorld {
	t.Helper()
	qualitySetTunables(t, 600, 600, 0)
	providerKey := devnet.ProviderKey(0)
	w := &qualityWorld{t: t, now: qualitySettledAt, options: options, raw: make(map[string][]byte)}
	var receipts []wire.SubmissionReceipt
	for i := 0; i < options.settled+options.unsettled; i++ {
		raw, receipt := qualityTestMessage(t, providerKey, i)
		w.raw[receipt.MessageID] = raw
		w.changes = append(w.changes, raw)
		if i < options.settled {
			w.ids = append(w.ids, receipt.MessageID)
			receipts = append(receipts, receipt)
		}
	}
	sort.Strings(w.ids)
	provider := httptest.NewServer(http.HandlerFunc(w.serveProvider))
	t.Cleanup(provider.Close)
	w.provider = Provider{ID: keys.DIDOf(providerKey), URL: provider.URL}
	vaultURL := "http://127.0.0.1:1"
	if options.vault != qualityVaultDown {
		vault := httptest.NewServer(http.HandlerFunc(w.serveVault))
		t.Cleanup(vault.Close)
		vaultURL = vault.URL
	}
	calendar, err := devnet.Calendar(30 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	declaration, err := retention.Declare(providerKey, retention.MinSeconds, wire.PayoutRecipients{BitcoinMainnet: devnet.Recipient(devnet.ProviderLabelPrefix+"0", faux.BitcoinMainnet)}, qualitySettledAt.Add(-48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	var leaves []wire.SettlementLeaf
	for _, receipt := range receipts {
		leaf, err := settlement.LeafOf(receipt)
		if err != nil {
			t.Fatal(err)
		}
		leaves = append(leaves, leaf)
	}
	leaves = settlement.SortLeaves(leaves)
	root, err := settlement.TreeRoot(leaves)
	if err != nil {
		t.Fatal(err)
	}
	claim, err := settlement.BuildClaim(providerKey, settlement.ClaimInput{
		Network: faux.BitcoinMainnet, PeriodStart: qualitySettledAt.Add(-24 * time.Hour), PeriodEnd: qualitySettledAt.Add(-time.Hour),
		DeclarationID: declaration.DeclarationID, ReceiptCount: len(leaves), Total: root.Sum, ReceiptsRoot: hex.EncodeToString(root.Hash[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	commitment, err := settlement.DepositCommitment(w.provider.ID, faux.BitcoinMainnet)
	if err != nil {
		t.Fatal(err)
	}
	chain := &testChain{transfers: []faux.Transfer{{Network: faux.BitcoinMainnet, Recipient: devnet.Genesis().FoundationKeyCertificates[0].Recipients.BitcoinMainnet, AmountChainUnits: "100000000", Data: hex.EncodeToString(commitment[:]), Sequence: 1, BlockTime: "2026-12-31T00:00:00Z", Finalized: true, Success: true}}}
	members := []keys.PrivateKey{devnet.MonitorKey(0), devnet.MonitorKey(1)}[:options.members]
	w.service, err = New(Config{
		Genesis: devnet.Genesis(), Members: members, DataDir: t.TempDir(), Now: func() time.Time { return w.now }, Calendar: calendar,
		Providers: map[string]string{w.provider.ID: provider.URL}, Peers: map[string]string{"foundation": vaultURL}, Faux: chain,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = w.service.Close() })
	w.vaultID, _ = w.service.View().Trust().CurrentFoundationID(qualitySettledAt)
	for _, member := range w.service.Members() {
		err := member.Do(func(m *committee.Monitor) error {
			if err := m.RegisterDeclaration(declaration); err != nil {
				return err
			}
			outcome, err := m.Audit(claim, leaves, qualitySettledAt, qualitySettledAt)
			if err == nil && outcome.Witness == nil {
				return fmt.Errorf("audit outcome %+v has no witness", outcome)
			}
			return err
		})
		if err != nil {
			t.Fatalf("member %d audit: %v", member.Index(), err)
		}
	}
	return w
}

// qualitySetTunables raises the per-source limits for fixtures that run a whole period in one pass and restores every tunable after the test.
func qualitySetTunables(t *testing.T, listing, direct, reportDays int) {
	t.Helper()
	previousListing, previousDirect, previousDays, previousTimeout, previousSeed := qualityListingPerSourcePerMinute, qualityDirectPerSourcePerMinute, qualityReportDays, qualityProbeTimeout, qualitySeedSource
	t.Cleanup(func() {
		qualityListingPerSourcePerMinute, qualityDirectPerSourcePerMinute, qualityReportDays, qualityProbeTimeout, qualitySeedSource = previousListing, previousDirect, previousDays, previousTimeout, previousSeed
	})
	qualityListingPerSourcePerMinute, qualityDirectPerSourcePerMinute, qualityReportDays = listing, direct, reportDays
}

// qualityTestMessage builds a correctly signed post whose receipt ID and text depend on index.
func qualityTestMessage(t *testing.T, providerKey keys.PrivateKey, index int) ([]byte, wire.SubmissionReceipt) {
	t.Helper()
	author := devnet.ProviderKey(90)
	issuedAt := qualitySettledAt.Add(-2*time.Hour + time.Duration(index)*time.Second)
	intent := wire.Intent{
		AuthorID: keys.DIDOf(author), Timestamp: wire.FormatTime(issuedAt.Add(-30 * time.Second)), MessageType: "post",
		Version: wire.Version{Major: 2, Minor: 0}, Payload: wire.PostPayload{ContentFormat: "text/plain", Text: "Quality probe record " + strconv.Itoa(index)},
	}
	id, err := message.ID(intent)
	if err != nil {
		t.Fatal(err)
	}
	schedule := devnet.Genesis().FeeSchedules[0]
	receipt := wire.SubmissionReceipt{
		ProviderID: keys.DIDOf(providerKey), AuthorID: intent.AuthorID, MessageID: id,
		ReceiptID: keys.EncodeB64URL([]byte(fmt.Sprintf("%032d", index))), QuoteID: "fyfq2:" + strings.Repeat("a", 64),
		ScheduleID: schedule.ScheduleID, Network: schedule.Network, AmountBaseUnits: "1",
		AuthorizationAt: wire.FormatTime(issuedAt), IssuedAt: wire.FormatTime(issuedAt),
	}
	size, err := message.FinalSize(intent, receipt)
	if err != nil {
		t.Fatal(err)
	}
	amount, err := fee.Amount(schedule, size)
	if err != nil {
		t.Fatal(err)
	}
	receipt.AmountBaseUnits = amount.String()
	if receipt, err = fee.SignReceipt(receipt, providerKey); err != nil {
		t.Fatal(err)
	}
	post, err := message.Assemble(intent, receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := message.Sign(&post, author); err != nil {
		t.Fatal(err)
	}
	raw, err := canon.Encode(post)
	if err != nil {
		t.Fatal(err)
	}
	return raw, receipt
}

func (w *qualityWorld) serveProvider(rw http.ResponseWriter, r *http.Request) {
	w.mu.Lock()
	w.providerHits++
	hook := w.firstProbeSeen
	if strings.HasPrefix(r.URL.Path, "/v1/messages") {
		w.firstProbeSeen = nil
	}
	w.mu.Unlock()
	if hook != nil && strings.HasPrefix(r.URL.Path, "/v1/messages") {
		hook()
	}
	rw.Header().Set("Content-Type", "application/json")
	switch {
	case r.URL.Path == "/v1/messages":
		w.mu.Lock()
		w.listings++
		listing := w.listings
		w.mu.Unlock()
		if w.options.hangListings {
			<-r.Context().Done()
			return
		}
		if listing <= w.options.unavailableListing {
			apierr.Write(rw, apierr.PublicReadError{Status: apierr.Unavailable, RetryAfterSeconds: 30})
			return
		}
		_, _ = rw.Write(discovery.EncodeDiscoveryPage(w.now, w.now.Add(30*time.Minute), [][]byte{w.raw[w.ids[0]]}, ""))
	case strings.HasPrefix(r.URL.Path, "/v1/messages/"):
		w.mu.Lock()
		w.retrievals++
		retrieval := w.retrievals
		w.mu.Unlock()
		id := strings.TrimPrefix(r.URL.Path, "/v1/messages/")
		switch {
		case retrieval <= w.options.wrongIDRetrievals:
			other := w.ids[0]
			if other == id {
				other = w.ids[1]
			}
			_, _ = rw.Write(w.raw[other])
		case retrieval <= w.options.wrongIDRetrievals+w.options.goneRetrievals:
			apierr.Write(rw, apierr.PublicReadError{Status: apierr.RetentionExpired})
		default:
			_, _ = rw.Write(w.raw[id])
		}
	case r.URL.Path == "/v1/changes":
		_, _ = io.WriteString(rw, w.changesPage(w.provider.ID, r.URL.Query().Get("after")))
	default:
		apierr.Write(rw, apierr.PublicReadError{Status: apierr.NotFound})
	}
}

func (w *qualityWorld) serveVault(rw http.ResponseWriter, r *http.Request) {
	rw.Header().Set("Content-Type", "application/json")
	if r.URL.Path != "/v1/changes" || w.options.vault == qualityVaultNotFound {
		apierr.Write(rw, apierr.PublicReadError{Status: apierr.NotFound})
		return
	}
	_, _ = io.WriteString(rw, w.changesPage(w.vaultID, r.URL.Query().Get("after")))
}

// changesPage answers GET /v1/changes with every record after the requested sequence.
func (w *qualityWorld) changesPage(providerID, afterValue string) string {
	after, _ := strconv.Atoi(afterValue)
	items := make([]string, 0, len(w.changes))
	for i, raw := range w.changes[min(after, len(w.changes)):] {
		items = append(items, `{"message":`+string(raw)+`,"sequence":`+strconv.Itoa(after+i+1)+`}`)
	}
	return `{"items":[` + strings.Join(items, ",") + `],"more":false,"next_after":` + strconv.Itoa(len(w.changes)) + `,"provider_id":"` + providerID + `"}`
}

// runAt sets the monitor clock and runs the quality step once.
func (w *qualityWorld) runAt(at time.Time) {
	w.t.Helper()
	w.now = at
	if err := qualityRun(w.service, context.Background()); err != nil {
		w.t.Fatalf("quality step at %s: %v", wire.FormatTime(at), err)
	}
}

// runPeriod plans at the period start, sends every probe in the last second and signs the report at the period end.
func (w *qualityWorld) runPeriod(start, end time.Time) {
	w.t.Helper()
	w.runAt(start)
	w.runAt(end.Add(-time.Second))
	w.runAt(end)
}

func (w *qualityWorld) report(member int, periodStart time.Time) (wire.QualityReport, qualityEvidenceRecord) {
	w.t.Helper()
	var report wire.QualityReport
	var evidence qualityEvidenceRecord
	var found bool
	err := w.service.Members()[member].DB().View(func(tx store.Tx) error {
		var err error
		if report, found, err = qualityReports.Get(tx, qualityPlanKey(w.provider.ID, periodStart)); err != nil || !found {
			return err
		}
		evidence, _, err = qualityEvidence.Get(tx, report.ReportID)
		return err
	})
	if err != nil || !found {
		w.t.Fatalf("member %d report for %s: found %v, %v", member, wire.FormatTime(periodStart), found, err)
	}
	return report, evidence
}

func (w *qualityWorld) plan(member int, periodStart time.Time) (qualityPlan, bool) {
	w.t.Helper()
	var plan qualityPlan
	var found bool
	if err := w.service.Members()[member].DB().View(func(tx store.Tx) error {
		var err error
		plan, found, err = qualityPlans.Get(tx, qualityPlanKey(w.provider.ID, periodStart))
		return err
	}); err != nil {
		w.t.Fatal(err)
	}
	return plan, found
}

// qualityTally counts observations by kind, outcome and HTTP status, so a whole set is compared with one literal.
func qualityTally(set wire.QualityObservationSet) map[string]int {
	tally := make(map[string]int)
	for _, observation := range set.Observations {
		status := "null"
		if observation.HTTPStatus != nil {
			status = strconv.FormatInt(*observation.HTTPStatus, 10)
		}
		tally[observation.Kind+" "+observation.Outcome+" "+status]++
	}
	return tally
}

// qualityRetrievalTargets returns the sampled message IDs of a plan in ASCII order.
func qualityRetrievalTargets(plan qualityPlan) []string {
	var targets []string
	for _, probe := range plan.Probes {
		if probe.MessageID != nil {
			targets = append(targets, *probe.MessageID)
		}
	}
	sort.Strings(targets)
	return targets
}

// qualityReportSummaries lists "monitor_id period_start period_end" per report, in page order.
func qualityReportSummaries(reports []wire.QualityReport) []string {
	summaries := make([]string, len(reports))
	for i, report := range reports {
		summaries[i] = report.MonitorID + " " + report.PeriodStart + " " + report.PeriodEnd
	}
	return summaries
}
