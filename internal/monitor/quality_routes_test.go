package monitor

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

func TestQualityReportsAcrossAQuarterEnd(t *testing.T) {
	w := qualityNewWorld(t, qualityWorldOptions{settled: 30, members: 2})
	qualitySetTunables(t, 600, 600, 7)
	at := func(clock string) time.Time {
		parsed, err := wire.ParseTime("2027-01-01T" + clock + "Z")
		if err != nil {
			t.Fatal(err)
		}
		return parsed
	}
	w.runPeriod(at("00:38:30"), at("00:42:00"))
	w.runPeriod(at("00:42:00"), at("00:45:00"))
	w.runPeriod(at("00:45:00"), at("00:48:30"))
	member0, member1 := keys.DIDOf(devnet.MonitorKey(0)), keys.DIDOf(devnet.MonitorKey(1))
	get := func(t *testing.T, member int, query string) *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		w.service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/monitors/"+strconv.Itoa(member)+"/v1/quality-reports?"+query, nil))
		return response
	}
	quarter := func(end string) string {
		return "provider_id=" + url.QueryEscape(w.provider.ID) + "&quarter_end=" + url.QueryEscape(end)
	}
	periods := func(t *testing.T, response *httptest.ResponseRecorder) []string {
		t.Helper()
		if response.Code != http.StatusOK || response.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("status %d, Cache-Control %q; want 200 and no-store", response.Code, response.Header().Get("Cache-Control"))
		}
		var page wire.QualityReportsPage
		if err := wire.Decode(response.Body.Bytes(), "QualityReportsPage", &page); err != nil {
			t.Fatal(err)
		}
		return qualityReportSummaries(page.Reports)
	}
	t.Run("a member's reports for the quarter come in period order", func(t *testing.T) {
		want := []string{member0 + " 2027-01-01T00:38:30Z 2027-01-01T00:42:00Z", member0 + " 2027-01-01T00:42:00Z 2027-01-01T00:45:00Z"}
		if got := periods(t, get(t, 0, quarter("2027-01-01T00:45:00Z"))); !reflect.DeepEqual(got, want) {
			t.Errorf("member 0 reports = %v; want %v", got, want)
		}
	})
	t.Run("each member returns only its own reports", func(t *testing.T) {
		want := []string{member1 + " 2027-01-01T00:38:30Z 2027-01-01T00:42:00Z", member1 + " 2027-01-01T00:42:00Z 2027-01-01T00:45:00Z"}
		if got := periods(t, get(t, 1, quarter("2027-01-01T00:45:00Z"))); !reflect.DeepEqual(got, want) {
			t.Errorf("member 1 reports = %v; want %v", got, want)
		}
	})
	t.Run("the next quarter holds the period after the quarter end", func(t *testing.T) {
		want := []string{member0 + " 2027-01-01T00:45:00Z 2027-01-01T00:48:30Z"}
		if got := periods(t, get(t, 0, quarter("2027-01-01T01:30:00Z"))); !reflect.DeepEqual(got, want) {
			t.Errorf("member 0 next-quarter reports = %v; want %v", got, want)
		}
	})
	t.Run("stored reports neither overlap nor cross the quarter end", func(t *testing.T) {
		var stored []string
		if err := w.service.Members()[0].DB().View(func(tx store.Tx) error {
			return qualityReports.Scan(tx, store.Range{}, func(_ string, report wire.QualityReport) (bool, error) {
				stored = append(stored, report.PeriodStart+" "+report.PeriodEnd+" "+report.SampleCoverage)
				return true, nil
			})
		}); err != nil {
			t.Fatal(err)
		}
		want := []string{"2027-01-01T00:38:30Z 2027-01-01T00:42:00Z complete", "2027-01-01T00:42:00Z 2027-01-01T00:45:00Z complete", "2027-01-01T00:45:00Z 2027-01-01T00:48:30Z complete"}
		if !reflect.DeepEqual(stored, want) {
			t.Errorf("stored reports = %v; want %v", stored, want)
		}
	})
	t.Run("an unknown provider has an empty page", func(t *testing.T) {
		response := get(t, 0, "provider_id="+url.QueryEscape(keys.DIDOf(devnet.ProviderKey(4)))+"&quarter_end=2027-01-01T00%3A45%3A00Z")
		if body := strings.TrimSpace(response.Body.String()); response.Code != http.StatusOK || body != `{"reports":[]}` {
			t.Errorf("response = %d %s; want 200 {\"reports\":[]}", response.Code, body)
		}
	})
	invalid := func(t *testing.T, query string) {
		t.Helper()
		response := get(t, 0, query)
		var body struct {
			Status string `json:"status"`
		}
		_ = json.Unmarshal(response.Body.Bytes(), &body)
		if response.Code != http.StatusBadRequest || body.Status != "invalid_query" {
			t.Errorf("query %q answered %d %s; want 400 invalid_query", query, response.Code, response.Body.String())
		}
	}
	t.Run("quarter_end that is not a quarter end is invalid", func(t *testing.T) {
		invalid(t, quarter("2027-01-01T00:44:00Z"))
	})
	t.Run("quarter_end with fractional seconds is invalid", func(t *testing.T) {
		invalid(t, quarter("2027-01-01T00:45:00.000Z"))
	})
	t.Run("a missing provider_id is invalid", func(t *testing.T) {
		invalid(t, "quarter_end=2027-01-01T00%3A45%3A00Z")
	})
	t.Run("a non-canonical provider_id is invalid", func(t *testing.T) {
		invalid(t, "provider_id=did%3Akey%3Az6Mk&quarter_end=2027-01-01T00%3A45%3A00Z")
	})
	t.Run("an unknown parameter is invalid", func(t *testing.T) {
		invalid(t, quarter("2027-01-01T00:45:00Z")+"&limit=1")
	})
}
