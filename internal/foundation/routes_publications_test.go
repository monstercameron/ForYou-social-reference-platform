package foundation

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/distribution"
	"foryou.local/foryou/internal/governance"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

func TestPublicationRoutesReturnSchemaValidatedPages(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	service, err := New(testConfig(t, &now))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	t.Run("genesis is the configured JCS object", func(t *testing.T) {
		response := pubRouteRequest(t, service, "/v1/foundation/genesis")
		want, err := canon.Encode(devnet.Genesis())
		if err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), want) {
			t.Fatalf("genesis response status/body = %d/%s, want configured JCS %s", response.Code, response.Body.Bytes(), want)
		}
		pubCheckSchema(t, response.Body.Bytes(), "GenesisManifest", &wire.GenesisManifest{})
	})

	t.Run("certificates include genesis once", func(t *testing.T) {
		response := pubRouteRequest(t, service, "/v1/foundation/certificates")
		var page wire.FoundationCertificatesPage
		pubCheckSchema(t, response.Body.Bytes(), "FoundationCertificatesPage", &page)
		if response.Code != http.StatusOK || len(page.Certificates) != 1 || page.Certificates[0].CertificateID != devnet.Genesis().FoundationKeyCertificates[0].CertificateID || len(page.Closures) != 0 || len(page.KeySetUpdates) != 0 {
			t.Fatalf("certificate page = %#v, want genesis certificate and no closures or updates", page)
		}
	})

	t.Run("fee schedules are the two schedule one approvals", func(t *testing.T) {
		response := pubRouteRequest(t, service, "/v1/foundation/fee-schedules")
		var page wire.FeeSchedulesPage
		pubCheckSchema(t, response.Body.Bytes(), "FeeSchedulesPage", &page)
		if response.Code != http.StatusOK || len(page.Schedules) != 2 || page.Schedules[0].Schedule.Revision != 1 || page.Schedules[1].Schedule.Revision != 1 || page.Schedules[0].Schedule.Network != wire.BitcoinMainnet || page.Schedules[1].Schedule.Network != wire.EthereumMainnet {
			t.Fatalf("fee schedule page = %#v, want schedule 1 for Bitcoin then Ethereum", page)
		}
	})

	t.Run("distribution is schema-valid", func(t *testing.T) {
		response := pubRouteRequest(t, service, "/v1/foundation/distribution")
		var page wire.DistributionPage
		pubCheckSchema(t, response.Body.Bytes(), "DistributionPage", &page)
		if response.Code != http.StatusOK || len(page.Policies) != 1 || len(page.Statements) != 0 {
			t.Fatalf("distribution page = %#v, want policy history and no statements", page)
		}
	})

	t.Run("vault status is schema-valid", func(t *testing.T) {
		response := pubRouteRequest(t, service, "/v1/foundation/vault-status")
		var page wire.VaultStatusPage
		pubCheckSchema(t, response.Body.Bytes(), "VaultStatusPage", &page)
		if response.Code != http.StatusOK || len(page.Statuses) != 0 {
			t.Fatalf("vault status page = %#v, want no statuses", page)
		}
	})
}

func TestPublicationQualityReportQuery(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	service, err := New(testConfig(t, &now))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()
	providerID := keys.DIDOf(devnet.ProviderKey(0))
	validQuarter := url.Values{"provider_id": {providerID}, "quarter_end": {"2026-10-01T00:00:00Z"}}.Encode()

	t.Run("valid quarter with no reports", func(t *testing.T) {
		response := pubRouteRequest(t, service, "/v1/foundation/quality-reports?"+validQuarter)
		var page wire.QualityReportsPage
		pubCheckSchema(t, response.Body.Bytes(), "QualityReportsPage", &page)
		if response.Code != http.StatusOK || len(page.Reports) != 0 || response.Body.String() != `{"reports":[]}` {
			t.Fatalf("quality page = %d %s, want empty report page", response.Code, response.Body.String())
		}
	})

	t.Run("quarter end is required", func(t *testing.T) {
		response := pubRouteRequest(t, service, "/v1/foundation/quality-reports?provider_id="+url.QueryEscape(providerID))
		pubExpectInvalidQuery(t, response)
	})

	t.Run("not a quarter end", func(t *testing.T) {
		query := url.Values{"provider_id": {providerID}, "quarter_end": {"2026-11-11T00:00:00Z"}}.Encode()
		response := pubRouteRequest(t, service, "/v1/foundation/quality-reports?"+query)
		pubExpectInvalidQuery(t, response)
	})

	t.Run("literal reserved colon is noncanonical", func(t *testing.T) {
		response := pubRouteRequest(t, service, "/v1/foundation/quality-reports?provider_id="+providerID+"&quarter_end=2026-10-01T00:00:00Z")
		pubExpectInvalidQuery(t, response)
	})

	t.Run("repeated and unknown parameters", func(t *testing.T) {
		pubExpectInvalidQuery(t, pubRouteRequest(t, service, "/v1/foundation/quality-reports?provider_id="+providerID+"&provider_id="+providerID+"&quarter_end=2026-10-01T00%3A00%3A00Z"))
		pubExpectInvalidQuery(t, pubRouteRequest(t, service, "/v1/foundation/quality-reports?provider_id="+providerID+"&quarter_end=2026-10-01T00%3A00%3A00Z&other=x"))
	})

	t.Run("parameters rejected on no-query publication", func(t *testing.T) {
		pubExpectInvalidQuery(t, pubRouteRequest(t, service, "/v1/foundation/genesis?anything=1"))
	})
}

func TestPublicationVaultStatusOrderingAndExactBytes(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	service, err := New(testConfig(t, &now))
	if err != nil {
		t.Fatal(err)
	}
	defer service.Close()

	certificate := devnet.Genesis().FoundationKeyCertificates[0]
	validFrom, err := wire.ParseTime(certificate.ValidFrom)
	if err != nil {
		t.Fatal(err)
	}
	validUntil, err := wire.ParseTime(certificate.ValidUntil)
	if err != nil {
		t.Fatal(err)
	}
	olderPeriodEnd, _ := wire.ParseTime("2026-07-01T00:00:00Z")
	newerPeriodEnd, _ := wire.ParseTime("2026-10-01T00:00:00Z")
	if olderPeriodEnd.Before(validFrom) || olderPeriodEnd.After(validUntil) || newerPeriodEnd.Before(validFrom) || newerPeriodEnd.After(validUntil) {
		t.Fatalf("genesis certificate validity %s to %s does not cover status quarters", certificate.ValidFrom, certificate.ValidUntil)
	}

	newerBytes, _, err := service.SignAndLog(governance.LogKindVaultStatus, "vault-status", wire.VaultStatus{
		FoundationID: keys.DIDOf(devnet.FoundationKey()), PeriodStart: "2026-07-01T00:00:00Z", PeriodEnd: "2026-10-01T00:00:00Z",
		RecordCount: 2, StoredBytes: 200, Funding: []wire.VaultFunding{},
	})
	if err != nil {
		t.Fatal(err)
	}
	olderBytes, _, err := service.SignAndLog(governance.LogKindVaultStatus, "vault-status", wire.VaultStatus{
		FoundationID: keys.DIDOf(devnet.FoundationKey()), PeriodStart: "2026-04-01T00:00:00Z", PeriodEnd: "2026-07-01T00:00:00Z",
		RecordCount: 1, StoredBytes: 100, Funding: []wire.VaultFunding{},
	})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("statuses are oldest first and preserve their signed bytes", func(t *testing.T) {
		response := pubRouteRequest(t, service, "/v1/foundation/vault-status")
		var page wire.VaultStatusPage
		pubCheckSchema(t, response.Body.Bytes(), "VaultStatusPage", &page)
		var rawPage struct {
			Statuses []json.RawMessage `json:"statuses"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &rawPage); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || len(page.Statuses) != 2 || len(rawPage.Statuses) != 2 {
			t.Fatalf("vault status page = %d %#v, want two statuses", response.Code, page)
		}
		if page.Statuses[0].PeriodEnd != "2026-07-01T00:00:00Z" || page.Statuses[1].PeriodEnd != "2026-10-01T00:00:00Z" {
			t.Fatalf("status period ends = %s, %s; want oldest first", page.Statuses[0].PeriodEnd, page.Statuses[1].PeriodEnd)
		}
		if !bytes.Equal(rawPage.Statuses[0], olderBytes) || !bytes.Equal(rawPage.Statuses[1], newerBytes) {
			t.Fatalf("status bytes = %s, %s; want signed bytes %s, %s", rawPage.Statuses[0], rawPage.Statuses[1], olderBytes, newerBytes)
		}
	})

	makeStatement := func(periodStart, periodEnd string) wire.DistributionStatement {
		statement := wire.DistributionStatement{
			FoundationID: keys.DIDOf(devnet.FoundationKey()), Network: wire.BitcoinMainnet,
			PeriodStart: periodStart, PeriodEnd: periodEnd, PolicyID: devnet.Genesis().DistributionPolicy.PolicyID,
			ReceivedBaseUnits: "0", DistributedBaseUnits: "0", MonitorPayments: []wire.MonitorPayment{}, Entries: []wire.DistributionEntry{},
		}
		statement.StatementID, err = distribution.StatementID(statement)
		if err != nil {
			t.Fatal(err)
		}
		return statement
	}
	newerStatementBytes, _, err := service.SignAndLog(governance.LogKindStatement, "distribution-statement", makeStatement("2026-07-01T00:00:00Z", "2026-10-01T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	olderStatementBytes, _, err := service.SignAndLog(governance.LogKindStatement, "distribution-statement", makeStatement("2026-04-01T00:00:00Z", "2026-07-01T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	t.Run("distribution statements are oldest first and preserve their signed bytes", func(t *testing.T) {
		response := pubRouteRequest(t, service, "/v1/foundation/distribution")
		var page pubDistributionPage
		pubCheckSchema(t, response.Body.Bytes(), "DistributionPage", &page)
		var rawPage struct {
			Statements []json.RawMessage `json:"statements"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &rawPage); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || len(page.Statements) != 2 || len(rawPage.Statements) != 2 {
			t.Fatalf("distribution page = %d %#v, want two statements", response.Code, page)
		}
		if !bytes.Equal(rawPage.Statements[0], olderStatementBytes) || !bytes.Equal(rawPage.Statements[1], newerStatementBytes) {
			t.Fatalf("statement bytes = %s, %s; want signed bytes %s, %s", rawPage.Statements[0], rawPage.Statements[1], olderStatementBytes, newerStatementBytes)
		}
	})
}

func pubRouteRequest(t *testing.T, service *Service, target string) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	service.Handler().ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
	if response.Header().Get("Cache-Control") != "no-store" || response.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("response headers = %#v, want JSON and no-store", response.Header())
	}
	return response
}

func pubCheckSchema(t *testing.T, body []byte, schema string, target any) {
	t.Helper()
	if err := wire.DecodeLimits(body, canon.Limits{MaxDepth: 32}, schema, target); err != nil {
		t.Fatalf("response does not decode as %s: %v", schema, err)
	}
}

func pubExpectInvalidQuery(t *testing.T, response *httptest.ResponseRecorder) {
	t.Helper()
	if response.Code != http.StatusBadRequest {
		t.Fatalf("response status = %d, want 400: %s", response.Code, response.Body.String())
	}
	var body apierr.PublicReadError
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Status != apierr.InvalidQuery {
		t.Fatalf("error body = %s, want invalid_query", response.Body.String())
	}
}
