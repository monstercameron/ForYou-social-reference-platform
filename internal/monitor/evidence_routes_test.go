package monitor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

func evidenceGet(handler http.Handler, member int, path string) *httptest.ResponseRecorder {
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/monitors/"+strconv.Itoa(member)+path, nil))
	return response
}

func evidenceDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

func TestQualityReportEvidenceIsServedUnderItsDigests(t *testing.T) {
	w := qualityNewWorld(t, qualityWorldOptions{settled: 30, members: 2})
	qualitySetTunables(t, 600, 600, 7)
	start, end := time.Date(2027, 1, 1, 0, 38, 30, 0, time.UTC), time.Date(2027, 1, 1, 0, 42, 0, 0, time.UTC)
	w.runPeriod(start, end)
	listed := evidenceGet(w.service.Handler(), 0, "/v1/quality-reports?provider_id="+url.QueryEscape(w.provider.ID)+"&quarter_end="+url.QueryEscape("2027-01-01T00:45:00Z"))
	var page wire.QualityReportsPage
	if err := wire.Decode(listed.Body.Bytes(), "QualityReportsPage", &page); err != nil || len(page.Reports) != 1 {
		t.Fatalf("reports = %d, %v; want member 0's one report", len(page.Reports), err)
	}
	report := page.Reports[0]
	base := "/v1/quality-reports/" + url.PathEscape(report.ReportID)

	t.Run("the methodology text hashes to methodology_digest", func(t *testing.T) {
		got := evidenceGet(w.service.Handler(), 0, base+"/methodology")
		if got.Code != http.StatusOK || got.Header().Get("Content-Type") != "text/plain; charset=utf-8" || got.Header().Get("Cache-Control") != "no-store" || evidenceDigest(got.Body.Bytes()) != report.MethodologyDigest {
			t.Errorf("status %d, type %q, digest %s; want 200 text whose digest is %s", got.Code, got.Header().Get("Content-Type"), evidenceDigest(got.Body.Bytes()), report.MethodologyDigest)
		}
	})
	t.Run("the observation set hashes to observations_digest and is the report's", func(t *testing.T) {
		got := evidenceGet(w.service.Handler(), 0, base+"/observations")
		var set wire.QualityObservationSet
		if got.Code != http.StatusOK || evidenceDigest(got.Body.Bytes()) != report.ObservationsDigest || wire.Decode(got.Body.Bytes(), "QualityObservationSet", &set) != nil {
			t.Fatalf("status %d, digest %s; want 200 and digest %s", got.Code, evidenceDigest(got.Body.Bytes()), report.ObservationsDigest)
		}
		if set.MonitorID != report.MonitorID || set.ProviderID != report.ProviderID || set.PeriodStart != report.PeriodStart || set.PeriodEnd != report.PeriodEnd || len(set.Observations) == 0 {
			t.Errorf("observation set = %s %s %s..%s with %d observations; want the report's monitor, provider and period", set.MonitorID, set.ProviderID, set.PeriodStart, set.PeriodEnd, len(set.Observations))
		}
	})
	t.Run("another member does not serve this member's evidence", func(t *testing.T) {
		got := evidenceGet(w.service.Handler(), 1, base+"/observations")
		if got.Code != http.StatusNotFound || got.Body.String() != `{"status":"not_found"}` {
			t.Errorf("status %d, body %s; want 404 not_found", got.Code, got.Body)
		}
	})
	t.Run("an unknown report is not found", func(t *testing.T) {
		got := evidenceGet(w.service.Handler(), 0, "/v1/quality-reports/fyqr2:"+hex.EncodeToString(make([]byte, 32))+"/methodology")
		if got.Code != http.StatusNotFound || got.Body.String() != `{"status":"not_found"}` {
			t.Errorf("status %d, body %s; want 404 not_found", got.Code, got.Body)
		}
	})
}

func TestFoundationEvidenceIsPublishedSignedByEachMember(t *testing.T) {
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
	published := func(t *testing.T, member int) wire.FoundationEvidence {
		t.Helper()
		got := evidenceGet(svc.Handler(), member, "/v1/foundation-evidence")
		var page wire.FoundationEvidencePage
		if got.Code != http.StatusOK || got.Header().Get("Cache-Control") != "no-store" || wire.Decode(got.Body.Bytes(), "FoundationEvidencePage", &page) != nil || len(page.Evidence) != 1 {
			t.Fatalf("status %d, body %s; want one schema-valid evidence object", got.Code, got.Body)
		}
		return page.Evidence[0]
	}
	verified := func(t *testing.T, evidence wire.FoundationEvidence) error {
		t.Helper()
		unsigned, err := canon.EncodeWithout(evidence, "monitor_signature")
		if err != nil {
			t.Fatal(err)
		}
		return keys.Verify(evidence.MonitorID, "foundation-evidence", unsigned, evidence.MonitorSignature)
	}
	t.Run("a rewritten log is published as its two conflicting heads", func(t *testing.T) {
		evidence := published(t, 0)
		if evidence.Kind != "log_rewrite" || len(evidence.Heads) != 2 || evidence.Heads[0].Size != 3 || evidence.Heads[1].Size != 3 || evidence.Heads[0].Root == evidence.Heads[1].Root || evidence.Closure != nil || evidence.Transfer != nil || evidence.ObservedAt != "2027-01-01T00:00:00Z" {
			t.Errorf("evidence = %+v; want log_rewrite with two size-3 heads of different roots, observed at the watch time", evidence)
		}
	})
	t.Run("each member signs what it serves with its own key", func(t *testing.T) {
		first, second := published(t, 0), published(t, 1)
		if first.MonitorID != keys.DIDOf(devnet.MonitorKey(0)) || second.MonitorID != keys.DIDOf(devnet.MonitorKey(1)) || verified(t, first) != nil || verified(t, second) != nil {
			t.Errorf("monitor IDs %s and %s; want members 0 and 1 with valid signatures", first.MonitorID, second.MonitorID)
		}
	})
	t.Run("a changed head breaks the member's signature", func(t *testing.T) {
		evidence := published(t, 0)
		evidence.Heads[0].Size = 4
		if verified(t, evidence) == nil {
			t.Error("evidence with an altered head still verified")
		}
	})
}

func TestEvidencePublishedMapsRecordedKinds(t *testing.T) {
	index := uint32(1)
	raw := func(t *testing.T, value any) json.RawMessage {
		t.Helper()
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return encoded
	}
	t.Run("a Bitcoin deposit funded from a recipient names its output and the funding address", func(t *testing.T) {
		transfer := faux.Transfer{Network: wire.BitcoinMainnet, TxID: "ab12", OutputIndex: &index, From: "bc1qfoundation", Recipient: "bc1qfoundation"}
		got, ok := evidencePublished(Evidence{Kind: "recipient_funded_deposit", Objects: []json.RawMessage{raw(t, transfer), raw(t, map[string]string{"from": "bc1qfoundation"})}})
		if !ok || got.Kind != "recipient_funded_deposit" || got.Transfer == nil || *got.Transfer != (wire.FoundationEvidenceTransfer{Network: wire.BitcoinMainnet, Transaction: "ab12:1", Recipient: "bc1qfoundation"}) || len(got.Heads) != 0 || got.Closure != nil {
			t.Errorf("published = %+v, %v; want the transfer ab12:1 funded from bc1qfoundation", got, ok)
		}
	})
	t.Run("an Ethereum deposit is named by its transaction hash", func(t *testing.T) {
		transfer := faux.Transfer{Network: wire.EthereumMainnet, TxHash: "0xfeed", From: "0xf00d"}
		got, ok := evidencePublished(Evidence{Kind: "recipient_funded_deposit", Objects: []json.RawMessage{raw(t, transfer)}})
		if !ok || got.Transfer == nil || got.Transfer.Transaction != "0xfeed" || got.Transfer.Recipient != "0xf00d" {
			t.Errorf("published = %+v, %v; want transaction 0xfeed funded from 0xf00d", got, ok)
		}
	})
	t.Run("an entry that could not be checked is not published", func(t *testing.T) {
		if got, ok := evidencePublished(Evidence{Kind: "missing_object", Objects: []json.RawMessage{raw(t, wire.FoundationLogEntry{Index: 2})}}); ok {
			t.Errorf("published %+v for a missing object; absence is not publishable evidence", got)
		}
	})
	t.Run("a single unpaired head is not published as a rewrite", func(t *testing.T) {
		if got, ok := evidencePublished(Evidence{Kind: "rewritten_log", Objects: []json.RawMessage{raw(t, wire.FoundationLogHead{Size: 3})}}); ok {
			t.Errorf("published %+v from one head; a rewrite needs two", got)
		}
	})
}
