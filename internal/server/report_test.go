package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

var reportTestNow = time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)

const reportTestFakeOperator = "did:key:z6MkgFMdkUdT5dYyzMgBHmCBnPvwEFS7pZkjGnLCVG4coNEM"

// reportTestProvider answers the intake and manifest routes of one operator.
type reportTestProvider struct {
	mu         sync.Mutex
	key        keys.PrivateKey
	operator   string
	messageID  string
	message    []byte
	manifest   []byte
	forms      map[string][]byte
	busy       int
	submits    []reportTestSubmit
	recoveries int
	stored     map[string]reportTestStored
	mux        *http.ServeMux
}

type reportTestSubmit struct {
	path string
	key  string
	body []byte
}

type reportTestStored struct {
	request []byte
	receipt wire.IntakeReceipt
}

func newReportTestProvider(t *testing.T, message []byte, messageID string, forms map[string][]byte) *reportTestProvider {
	t.Helper()
	key := devnet.ProviderKey(0)
	p := &reportTestProvider{key: key, operator: keys.DIDOf(key), messageID: messageID, message: message, forms: forms, stored: map[string]reportTestStored{}}
	p.manifest = reportTestManifest(t, key)
	p.mux = http.NewServeMux()
	p.mux.HandleFunc("GET /v1/messages/{id}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("id") != p.messageID {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(p.message)
	})
	p.mux.HandleFunc("GET /.well-known/foryou-operator.json", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(p.manifest)
	})
	p.mux.HandleFunc("GET /v1/intake-forms/{form_id}/{form_version}", func(w http.ResponseWriter, r *http.Request) {
		form, ok := p.forms[r.PathValue("form_id")]
		if !ok || r.PathValue("form_version") != "1" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(form)
	})
	p.mux.HandleFunc("POST /v1/notices", p.submit)
	p.mux.HandleFunc("POST /v1/rights-requests", p.submit)
	p.mux.HandleFunc("POST /v1/appeals", p.submit)
	p.mux.HandleFunc("GET /v1/intake/{request_id}", p.recover)
	return p
}

func (p *reportTestProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mux.ServeHTTP(w, r)
}

// reportTestRequestID derives the request ID the way the provider checks it.
func reportTestRequestID(operator string, secret []byte) string {
	canonical, _ := canon.Encode(map[string]string{"operator_id": operator, "recovery_key": keys.EncodeB64URL(secret)})
	input, _ := keys.SigningInput("intake-id", canonical)
	digest := sha256.Sum256(input)
	return keys.EncodeB64URL(digest[:])
}

func (p *reportTestProvider) receipt(requestID string, requestBytes []byte) wire.IntakeReceipt {
	digest := sha256.Sum256(requestBytes)
	receipt := wire.IntakeReceipt{OperatorID: p.operator, RequestID: requestID, RequestDigest: hex.EncodeToString(digest[:]), ReceivedAt: wire.FormatTime(reportTestNow), Status: "received"}
	signed, _ := canon.EncodeWithout(receipt, "signature")
	receipt.Signature, _ = keys.Sign(p.key, "intake-receipt", signed)
	return receipt
}

func (p *reportTestProvider) submit(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)
	p.mu.Lock()
	defer p.mu.Unlock()
	p.submits = append(p.submits, reportTestSubmit{path: r.URL.Path, key: r.Header.Get("ForYou-Intake-Key"), body: body})
	if p.busy > 0 {
		p.busy--
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	var request wire.IntakeRequest
	if err := wire.Decode(body, "IntakeRequest", &request); err != nil {
		http.Error(w, "invalid", http.StatusBadRequest)
		return
	}
	secret, err := keys.DecodeB64URL(r.Header.Get("ForYou-Intake-Key"), 32)
	if err != nil || reportTestRequestID(p.operator, secret) != request.RequestID {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	if existing, ok := p.stored[request.RequestID]; ok {
		if string(existing.request) != string(body) {
			http.Error(w, "conflict", http.StatusConflict)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		receipt, _ := wire.Encode("IntakeReceipt", existing.receipt)
		_, _ = w.Write(receipt)
		return
	}
	receipt := p.receipt(request.RequestID, body)
	p.stored[request.RequestID] = reportTestStored{request: append([]byte(nil), body...), receipt: receipt}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	encoded, _ := wire.Encode("IntakeReceipt", receipt)
	_, _ = w.Write(encoded)
}

func (p *reportTestProvider) recover(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.recoveries++
	secret, err := keys.DecodeB64URL(r.Header.Get("ForYou-Intake-Key"), 32)
	if err != nil || reportTestRequestID(p.operator, secret) != r.PathValue("request_id") {
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	}
	stored, ok := p.stored[r.PathValue("request_id")]
	if !ok {
		http.NotFound(w, r)
		return
	}
	var request wire.IntakeRequest
	if err := wire.Decode(stored.request, "IntakeRequest", &request); err != nil {
		http.Error(w, "invalid", http.StatusInternalServerError)
		return
	}
	encoded, _ := wire.Encode("IntakeRecovery", wire.IntakeRecovery{Request: request, Receipt: stored.receipt})
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(encoded)
}

func reportTestManifest(t *testing.T, key keys.PrivateKey) []byte {
	t.Helper()
	m := wire.OperatorManifest{OperatorID: keys.DIDOf(key), ManifestVersion: 1, IssuedAt: "2026-11-11T00:00:00Z", ExpiresAt: "2026-12-01T00:00:00Z", ServiceRoles: []string{"ingress"}, ServiceEndpoints: []string{"https://provider.example"}, SupportedProfiles: []string{"ForYouExportSet-1"}, PolicyURL: "https://provider.example/policy", PolicyDigest: strings.Repeat("a", 64), LegalContact: "legal@provider.example", AbuseContact: "abuse@provider.example", PrivacyContact: "privacy@provider.example", AppealURL: "https://provider.example/appeal", MarketsAssessed: []string{}}
	body, err := canon.EncodeWithout(m, "signature")
	if err != nil {
		t.Fatal(err)
	}
	m.Signature, err = keys.Sign(key, "operator-manifest", body)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := canon.Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// reportTestForms returns the three intake forms the fake provider publishes.
func reportTestForms(t *testing.T, operator string) map[string][]byte {
	t.Helper()
	country := []wire.IntakeFormField{{Name: "country", Required: true, MaxUTF8Bytes: 8, Description: "Two-letter country code."}}
	notice, err := wire.Encode("IntakeForm", wire.IntakeForm{OperatorID: operator, FormID: "notice", FormVersion: "1", Kind: "notice", Fields: country, Instructions: "Submit a bounded notice."})
	if err != nil {
		t.Fatal(err)
	}
	rights, err := wire.Encode("IntakeForm", wire.IntakeForm{OperatorID: operator, FormID: "rights", FormVersion: "1", Kind: "rights_request", Fields: []wire.IntakeFormField{}, Instructions: "Submit a bounded data-rights request."})
	if err != nil {
		t.Fatal(err)
	}
	appeal, err := wire.Encode("IntakeForm", wire.IntakeForm{OperatorID: operator, FormID: "appeal", FormVersion: "1", Kind: "appeal", Fields: []wire.IntakeFormField{}, Instructions: "Submit a bounded appeal."})
	if err != nil {
		t.Fatal(err)
	}
	return map[string][]byte{"notice": notice, "rights": rights, "appeal": appeal}
}

// newReportTest starts a fake provider holding one signed post and a client that selects it.
func newReportTest(t *testing.T, forms map[string][]byte) (*Client, *reportTestProvider, *httptest.Server, string) {
	t.Helper()
	raw, providerID := signedNetworkRecord(t, reportTestNow.Add(-24*time.Hour))
	var message wire.TransportMessage
	if err := wire.Decode(raw, "TransportMessage", &message); err != nil {
		t.Fatal(err)
	}
	provider := newReportTestProvider(t, raw, message.MessageID, forms)
	server := httptest.NewTLSServer(provider)
	t.Cleanup(server.Close)
	client, err := NewClient(Config{DataDir: t.TempDir(), Now: func() time.Time { return reportTestNow }, HTTPClient: server.Client(), Providers: []Endpoint{{URL: server.URL, ServiceID: providerID, Label: "Fixture provider"}}})
	if err != nil {
		t.Fatal(err)
	}
	return client, provider, server, message.MessageID
}

func reportTestCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == profileCookie {
			return c
		}
	}
	t.Fatal("no profile cookie was set")
	return nil
}

// reportTestValue returns the text after marker up to the next double quote.
func reportTestValue(body, marker string) string {
	_, after, found := strings.Cut(body, marker)
	if !found {
		return ""
	}
	value, _, _ := strings.Cut(after, `"`)
	return value
}

func reportTestNotice(id string, formBody []byte) url.Values {
	sum := sha256.Sum256(formBody)
	return url.Values{"id": {id}, "kind": {"notice"}, "form_digest": {hex.EncodeToString(sum[:])}, "locators": {""}, "statement": {"This post is spam."}, "understood": {"1"}, "field_country": {"NL"}}
}

func reportTestSend(t *testing.T, client *Client, form url.Values) (string, *httptest.ResponseRecorder) {
	t.Helper()
	w := postForm(client.Handler(), "/report", form)
	return reportTestValue(w.Body.String(), `id="report-secret-value" readonly value="`), w
}

func TestReportDeriveRequestID(t *testing.T) {
	t.Run("matches the provider derivation", func(t *testing.T) {
		secret := make([]byte, 32)
		for i := range secret {
			secret[i] = byte(i)
		}
		got, err := reportDeriveRequestID(reportTestFakeOperator, secret)
		if err != nil || got != "0J0SM2nXDeVHesosHx3bqZkLFGB55eutJM5UxgZRktY" {
			t.Fatalf("request ID = %q, error %v; want the provider's derivation", got, err)
		}
	})
}

func TestReportVerifyReceipt(t *testing.T) {
	key := devnet.ProviderKey(0)
	operator := keys.DIDOf(key)
	requestBytes := []byte(`{"kind":"notice"}`)
	p := &reportTestProvider{key: key, operator: operator}
	requestID := "0J0SM2nXDeVHesosHx3bqZkLFGB55eutJM5UxgZRktY"
	receipt := p.receipt(requestID, requestBytes)
	t.Run("accepts a signed receipt for the exact bytes", func(t *testing.T) {
		if err := reportVerifyReceipt(receipt, operator, requestID, requestBytes); err != nil {
			t.Fatalf("verified receipt rejected: %v", err)
		}
	})
	t.Run("rejects a receipt for other bytes", func(t *testing.T) {
		changed := []byte(`{"kind":"appeal"}`)
		if err := reportVerifyReceipt(receipt, operator, requestID, changed); err == nil {
			t.Fatal("receipt for other request bytes was accepted")
		}
	})
	t.Run("rejects another operator's signature", func(t *testing.T) {
		other := &reportTestProvider{key: devnet.ProviderKey(1), operator: keys.DIDOf(devnet.ProviderKey(1))}
		forged := other.receipt(requestID, requestBytes)
		forged.OperatorID = operator
		if err := reportVerifyReceipt(forged, operator, requestID, requestBytes); err == nil {
			t.Fatal("receipt signed by another operator was accepted")
		}
	})
}

func TestReportPage(t *testing.T) {
	t.Run("demo post explains there is no provider", func(t *testing.T) {
		client, err := NewClient(Config{DataDir: t.TempDir(), Now: func() time.Time { return reportTestNow }})
		if err != nil {
			t.Fatal(err)
		}
		body := getWith(client.Handler(), "/report?id=fy1:"+strings.Repeat("a", 64))
		if !strings.Contains(body, "It is not on the network") {
			t.Fatal("demo post page does not explain that there is no provider")
		}
	})
	t.Run("no intake shows contacts", func(t *testing.T) {
		client, _, _, id := newReportTest(t, nil)
		body := getWith(client.Handler(), "/report?id="+id)
		if !strings.Contains(body, "Contact the provider directly") || !strings.Contains(body, "abuse@provider.example") {
			t.Fatal("provider without intake does not show its verified contacts")
		}
	})
	t.Run("offered kinds are selectable", func(t *testing.T) {
		client, _, _, id := newReportTest(t, reportTestForms(t, reportTestFakeOperator))
		body := getWith(client.Handler(), "/report?id="+id)
		if !strings.Contains(body, `<input type="radio" name="kind" value="notice">`) || !strings.Contains(body, `<input type="radio" name="kind" value="rights_request">`) || !strings.Contains(body, `<input type="radio" name="kind" value="appeal">`) {
			t.Fatal("offered kinds are not selectable radio buttons")
		}
	})
}

func TestReportSubmit(t *testing.T) {
	forms := reportTestForms(t, reportTestFakeOperator)
	t.Run("notice gets a verified receipt and the secret once", func(t *testing.T) {
		client, p, _, id := newReportTest(t, forms)
		key, w := reportTestSend(t, client, reportTestNotice(id, forms["notice"]))
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "Receipt verified") || !strings.Contains(body, "does not remove the post from the network") {
			t.Fatalf("receipt page = %d; want a verified receipt", w.Code)
		}
		secret, err := keys.DecodeB64URL(key, 32)
		if err != nil || len(key) != 43 {
			t.Fatalf("secret %q is not a 43-character recovery key", key)
		}
		requestID := reportTestRequestID(p.operator, secret)
		if len(p.submits) != 1 || p.submits[0].path != "/v1/notices" || p.submits[0].key != key || !strings.Contains(body, requestID) {
			t.Fatal("the provider did not receive exactly one notice for this secret")
		}
		profile := reportTestCookie(t, w).Value
		listed, err := os.ReadFile(filepath.Join(client.app.profiles.root, profile, "reports.json"))
		if err != nil || !strings.Contains(string(listed), requestID) || strings.Contains(string(listed), key) || strings.Contains(string(listed), "This post is spam.") {
			t.Fatalf("reports.json holds the wrong data: %v %s", err, listed)
		}
	})
	t.Run("fragment answer is the flow only", func(t *testing.T) {
		client, _, _, id := newReportTest(t, forms)
		w := postForm(client.Handler(), "/report/fragment", reportTestNotice(id, forms["notice"]))
		if strings.Contains(w.Body.String(), "<html") || !strings.Contains(w.Body.String(), "Receipt verified") {
			t.Fatal("fragment response is not the flow content")
		}
	})
	t.Run("changed form is not sent", func(t *testing.T) {
		client, p, _, id := newReportTest(t, forms)
		values := reportTestNotice(id, forms["notice"])
		values.Set("form_digest", strings.Repeat("0", 64))
		w := postForm(client.Handler(), "/report", values)
		if len(p.submits) != 0 || !strings.Contains(w.Body.String(), "changed this form") {
			t.Fatal("a report was sent against a form the reader did not see")
		}
	})
	t.Run("unclear outcome offers resend with the same bytes", func(t *testing.T) {
		client, p, _, id := newReportTest(t, forms)
		p.busy = 1
		key, w := reportTestSend(t, client, reportTestNotice(id, forms["notice"]))
		if key == "" || !strings.Contains(w.Body.String(), "Send the same report again") {
			t.Fatal("unclear outcome does not offer a resend with the secret")
		}
		resend := url.Values{"request": {reportTestValue(w.Body.String(), `name="request" value="`)}, "key": {reportTestValue(w.Body.String(), `name="key" value="`)}}
		again := postForm(client.Handler(), "/report/resend", resend)
		if !strings.Contains(again.Body.String(), "Receipt verified") || len(p.submits) != 2 || string(p.submits[0].body) != string(p.submits[1].body) {
			t.Fatal("resend did not deliver byte-identical request bytes and a receipt")
		}
	})
	t.Run("cross-origin post is refused", func(t *testing.T) {
		client, _, _, id := newReportTest(t, forms)
		r := httptest.NewRequest(http.MethodPost, "/report", strings.NewReader(reportTestNotice(id, forms["notice"]).Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://evil.example")
		w := httptest.NewRecorder()
		client.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Fatalf("cross-origin report status = %d; want 403", w.Code)
		}
	})
}

func TestReportCheck(t *testing.T) {
	forms := reportTestForms(t, reportTestFakeOperator)
	t.Run("recovers and verifies a request", func(t *testing.T) {
		client, _, _, id := newReportTest(t, forms)
		key, _ := reportTestSend(t, client, reportTestNotice(id, forms["notice"]))
		w := postForm(client.Handler(), "/report/check", url.Values{"provider": {reportTestFakeOperator}, "key": {key}})
		if !strings.Contains(w.Body.String(), "still holds this request") {
			t.Fatal("the stored request was not recovered and verified")
		}
	})
	t.Run("unknown secret says nothing", func(t *testing.T) {
		client, _, _, _ := newReportTest(t, forms)
		unknown := keys.EncodeB64URL(make([]byte, 32))
		w := postForm(client.Handler(), "/report/check", url.Values{"provider": {reportTestFakeOperator}, "key": {unknown}})
		if !strings.Contains(w.Body.String(), "No request to show") {
			t.Fatal("an unknown secret did not get the neutral answer")
		}
	})
	t.Run("malformed secret is a field error", func(t *testing.T) {
		client, p, _, _ := newReportTest(t, forms)
		w := postForm(client.Handler(), "/report/check", url.Values{"provider": {reportTestFakeOperator}, "key": {"too-short"}})
		if !strings.Contains(w.Body.String(), "43 characters") || p.recoveries != 0 {
			t.Fatal("a malformed secret reached the provider or was not refused in the form")
		}
	})
}

func TestReportStore(t *testing.T) {
	t.Run("damaged file reads as empty with a notice", func(t *testing.T) {
		client, err := NewClient(Config{DataDir: t.TempDir(), Now: func() time.Time { return reportTestNow }})
		if err != nil {
			t.Fatal(err)
		}
		profile := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
		dir := filepath.Join(client.app.profiles.root, profile)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "reports.json"), []byte("{"), 0600); err != nil {
			t.Fatal(err)
		}
		body := getWith(client.Handler(), "/report/check", &http.Cookie{Name: profileCookie, Value: profile})
		if !strings.Contains(body, "could not be read") {
			t.Fatal("damaged report list was not reported")
		}
	})
}
