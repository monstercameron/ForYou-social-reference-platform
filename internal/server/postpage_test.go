package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

// ppTestProvider serves one signed record and, once settled is set, its settlement proof.
func ppTestProvider(t *testing.T, raw []byte, m wire.TransportMessage, settled *atomic.Bool) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/v1/messages/"+m.MessageID:
			nrTestJSON(w, http.StatusOK, string(raw))
		case r.URL.Path == "/v1/settlements/"+m.SubmissionReceipt.ReceiptID && settled.Load():
			nrTestJSON(w, http.StatusOK, string(nrTestProof(t, m.SubmissionReceipt)))
		default:
			nrTestJSON(w, http.StatusNotFound, nrNotFound)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

// ppSignedVault serves a Foundation receipt that stores the message at nrTestNow.
func ppSignedVault(t *testing.T, messageID string) *httptest.Server {
	t.Helper()
	receipt := wire.VaultReceipt{FoundationID: keys.DIDOf(devnet.FoundationKey()), MessageID: messageID, StoredAt: wire.FormatTime(nrTestNow)}
	canonical, err := canon.EncodeWithout(receipt, "foundation_signature")
	if err != nil {
		t.Fatal(err)
	}
	if receipt.FoundationSignature, err = keys.Sign(devnet.FoundationKey(), "vault-receipt", canonical); err != nil {
		t.Fatal(err)
	}
	body, err := canon.Encode(receipt)
	if err != nil {
		t.Fatal(err)
	}
	vault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/vault/receipts/"+messageID {
			nrTestJSON(w, http.StatusOK, string(body))
			return
		}
		nrTestJSON(w, http.StatusNotFound, nrNotFound)
	}))
	t.Cleanup(vault.Close)
	return vault
}

func TestPostPageNetworkRecord(t *testing.T) {
	raw, m := nrTestRecord(t, "A post whose trust the page explains.", nrTestNow.Add(-time.Hour), "")
	providerID := keys.DIDOf(devnet.ProviderKey(0))
	var settled atomic.Bool
	provider := ppTestProvider(t, raw, m, &settled)
	clock := nrTestNow
	providers := []Endpoint{{URL: provider.URL, ServiceID: providerID, Label: "Provider"}}
	path := "/posts/" + m.MessageID

	t.Run("settled record lists witnesses, quorum and passed checks", func(t *testing.T) {
		settled.Store(true)
		defer settled.Store(false)
		client := nrTestClient(t, t.TempDir(), &clock, providers, Endpoint{})
		w := httptest.NewRecorder()
		client.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, `pill-ok">Settled`) || !strings.Contains(body, "Step 6: Settlement state of the receipt") || !strings.Contains(body, "4 of 5 monitors witnessed; quorum is more than two-thirds (4)") || !strings.Contains(body, "in current committee") || strings.Count(body, `pill-ok">Passed`) != 5 {
			t.Fatalf("status %d; settled record page is missing its proof, witnesses, settlement step or passed checks", w.Code)
		}
	})

	t.Run("pending record shows the recheck date", func(t *testing.T) {
		client := nrTestClient(t, t.TempDir(), &clock, providers, Endpoint{})
		w := httptest.NewRecorder()
		client.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Not proof of payment. Your trust in this provider lets you treat it as admitted until 2026-11-18.") {
			t.Fatalf("status %d; pending record does not show its recheck date", w.Code)
		}
	})

	t.Run("raw endpoint returns the exact signed bytes with the download policy", func(t *testing.T) {
		client := nrTestClient(t, t.TempDir(), &clock, providers, Endpoint{})
		w := httptest.NewRecorder()
		client.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path+"/raw", nil))
		header := w.Header()
		if w.Code != http.StatusOK || w.Body.String() != string(raw) || header.Get("Content-Type") != "application/json" || header.Get("Content-Security-Policy") != "default-src 'none'" || header.Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(header.Get("Content-Disposition"), "attachment;") {
			t.Fatalf("status %d; raw record is not the exact signed bytes under the download headers", w.Code)
		}
	})

	t.Run("absent from every source is reported as not returned, not deleted", func(t *testing.T) {
		absent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { nrTestJSON(w, http.StatusNotFound, nrNotFound) }))
		defer absent.Close()
		client := nrTestClient(t, t.TempDir(), &clock, []Endpoint{{URL: absent.URL, ServiceID: providerID, Label: "Provider"}}, Endpoint{})
		w := httptest.NewRecorder()
		client.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "This post was not returned by your sources.") || !strings.Contains(w.Body.String(), "This does not mean it was deleted or never existed.") {
			t.Fatalf("status %d; absent record is not described as not returned", w.Code)
		}
	})

	t.Run("retention ended at a provider points to the Lifetime Vault", func(t *testing.T) {
		expired := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			nrTestJSON(w, http.StatusGone, `{"status":"retention_expired"}`)
		}))
		defer expired.Close()
		client := nrTestClient(t, t.TempDir(), &clock, []Endpoint{{URL: expired.URL, ServiceID: providerID, Label: "Provider"}}, Endpoint{})
		w := httptest.NewRecorder()
		client.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "This provider's declared retention ended; the Lifetime Vault may still have it.") {
			t.Fatalf("status %d; retention end is not shown with the vault sentence", w.Code)
		}
	})

	t.Run("a non-canonical ID is answered without contacting any source", func(t *testing.T) {
		var asked atomic.Int32
		counting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			asked.Add(1)
			nrTestJSON(w, http.StatusNotFound, nrNotFound)
		}))
		defer counting.Close()
		client := nrTestClient(t, t.TempDir(), &clock, []Endpoint{{URL: counting.URL, ServiceID: providerID, Label: "Provider"}}, Endpoint{})
		w := httptest.NewRecorder()
		client.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/posts/unknown", nil))
		if w.Code != http.StatusNotFound || asked.Load() != 0 {
			t.Fatalf("status %d after %d source requests; want 404 with none", w.Code, asked.Load())
		}
	})

	t.Run("unsettled copy from the vault says where it came from", func(t *testing.T) {
		absent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { nrTestJSON(w, http.StatusNotFound, nrNotFound) }))
		defer absent.Close()
		vault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/v1/messages/"+m.MessageID {
				nrTestJSON(w, http.StatusOK, string(raw))
				return
			}
			nrTestJSON(w, http.StatusNotFound, nrNotFound)
		}))
		defer vault.Close()
		client := nrTestClient(t, t.TempDir(), &clock, []Endpoint{{URL: absent.URL, ServiceID: providerID, Label: "Provider"}}, Endpoint{URL: vault.URL, ServiceID: keys.DIDOf(devnet.FoundationKey()), Label: "Vault"})
		w := httptest.NewRecorder()
		client.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "Unsettled") || !strings.Contains(body, "this copy came from a mirror or the Lifetime Vault") {
			t.Fatalf("status %d; vault copy without a settlement proof is not shown as unsettled from the vault", w.Code)
		}
	})

	t.Run("settlement check fragment replaces only the settlement section", func(t *testing.T) {
		client := nrTestClient(t, t.TempDir(), &clock, providers, Endpoint{})
		w := httptest.NewRecorder()
		client.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Pending settlement") {
			t.Fatalf("status %d; the first read does not hold a pending record", w.Code)
		}
		settled.Store(true)
		defer settled.Store(false)
		w = postForm(client.Handler(), path+"/recheck/fragment", url.Values{})
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, `<section id="settlement"`) || !strings.Contains(body, `pill-ok">Settled`) || !strings.Contains(body, "Checked against your sources just now.") || strings.Contains(body, "<html") {
			t.Fatalf("status %d; settlement fragment is not the section alone with the recheck result", w.Code)
		}
	})

	t.Run("settlement check without scripts returns the full page", func(t *testing.T) {
		client := nrTestClient(t, t.TempDir(), &clock, providers, Endpoint{})
		w := httptest.NewRecorder()
		client.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Pending settlement") {
			t.Fatalf("status %d; the first read does not hold a pending record", w.Code)
		}
		settled.Store(true)
		defer settled.Store(false)
		w = postForm(client.Handler(), path+"/recheck", url.Values{})
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "<html") || !strings.Contains(body, `pill-ok">Settled`) || !strings.Contains(body, "Checked against your sources just now.") {
			t.Fatalf("status %d; full-page settlement check did not render the recheck result", w.Code)
		}
	})

	t.Run("vault receipt signed by the example Foundation verifies", func(t *testing.T) {
		vault := ppSignedVault(t, m.MessageID)
		client := nrTestClient(t, t.TempDir(), &clock, providers, Endpoint{URL: vault.URL, ServiceID: keys.DIDOf(devnet.FoundationKey()), Label: "Vault"})
		w := httptest.NewRecorder()
		client.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path+"/vault/fragment", nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Stored in the Lifetime Vault on 2026-11-12") {
			t.Fatalf("status %d; signed vault receipt was not shown as verified", w.Code)
		}
	})

	t.Run("vault check without scripts returns the full page", func(t *testing.T) {
		vault := ppSignedVault(t, m.MessageID)
		client := nrTestClient(t, t.TempDir(), &clock, providers, Endpoint{URL: vault.URL, ServiceID: keys.DIDOf(devnet.FoundationKey()), Label: "Vault"})
		w := httptest.NewRecorder()
		client.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path+"/vault", nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<html") || !strings.Contains(w.Body.String(), "Stored in the Lifetime Vault on 2026-11-12") {
			t.Fatalf("status %d; full-page vault check did not show the verified receipt", w.Code)
		}
	})

	t.Run("tampered vault receipt is not shown as verified", func(t *testing.T) {
		receipt := wire.VaultReceipt{FoundationID: keys.DIDOf(devnet.FoundationKey()), MessageID: m.MessageID, StoredAt: wire.FormatTime(nrTestNow)}
		canonical, err := canon.EncodeWithout(receipt, "foundation_signature")
		if err != nil {
			t.Fatal(err)
		}
		if receipt.FoundationSignature, err = keys.Sign(devnet.FoundationKey(), "vault-receipt", canonical); err != nil {
			t.Fatal(err)
		}
		receipt.StoredAt = wire.FormatTime(nrTestNow.AddDate(1, 0, 0))
		body, err := canon.Encode(receipt)
		if err != nil {
			t.Fatal(err)
		}
		vault := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			nrTestJSON(w, http.StatusOK, string(body))
		}))
		defer vault.Close()
		client := nrTestClient(t, t.TempDir(), &clock, providers, Endpoint{URL: vault.URL, ServiceID: keys.DIDOf(devnet.FoundationKey()), Label: "Vault"})
		w := httptest.NewRecorder()
		client.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path+"/vault/fragment", nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "could not be verified") || strings.Contains(w.Body.String(), "Stored in the Lifetime Vault") {
			t.Fatalf("status %d; tampered vault receipt was shown as stored", w.Code)
		}
	})

	t.Run("record without a trust root is not checked and offers no download", func(t *testing.T) {
		client := nrTestClient(t, t.TempDir(), &clock, providers, Endpoint{})
		client.app.trustMu.Lock()
		client.app.root = nil
		client.app.trustStatus = TrustStatus{State: TrustUnavailable}
		client.app.trustMu.Unlock()
		w := httptest.NewRecorder()
		client.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		body := w.Body.String()
		if w.Code != http.StatusOK || strings.Count(body, "Not checked: the trust root is unavailable.") != 4 || strings.Contains(body, "/posts//raw") || strings.Contains(body, path+"/raw") || strings.Contains(body, "does not match the receipt") || strings.Contains(body, "Check settlement now") {
			t.Fatalf("status %d; record without a trust root is not marked as not checked in four sections or still offers rows, a recheck or a download", w.Code)
		}
	})
}
