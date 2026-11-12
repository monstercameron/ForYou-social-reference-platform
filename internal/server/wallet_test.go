package server

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/wire"
)

type walFake struct {
	balances         map[string]wire.BalanceStatus
	balanceErr       error
	requestErr       error
	observeCredit    *wire.DepositCredit
	observeErr       error
	observeCalls     int
	withdrawStatus   wire.WithdrawalStatus
	withdrawErr      error
	withdrawRequests []string
}

func (f *walFake) OperatorManifest(context.Context) (wire.OperatorManifest, []byte, error) {
	return wire.OperatorManifest{AppealURL: "https://provider.example/policy#rights"}, nil, nil
}

func (f *walFake) Balance(_ context.Context, _ ed25519.PrivateKey, network string) (wire.BalanceStatus, error) {
	if f.balanceErr != nil {
		return wire.BalanceStatus{}, f.balanceErr
	}
	return f.balances[network], nil
}

func (f *walFake) RequestDeposit(context.Context, ed25519.PrivateKey, sdk.DepositPlan, sdk.FundingLog) (wire.DepositInvoice, wire.DepositRequest, error) {
	return wire.DepositInvoice{}, wire.DepositRequest{}, f.requestErr
}

func (f *walFake) ObserveDeposit(context.Context, ed25519.PrivateKey, wire.DepositInvoice, wire.DepositTransfer, sdk.FundingLog) (sdk.ObserveResult, error) {
	f.observeCalls++
	if f.observeErr != nil {
		return sdk.ObserveResult{}, f.observeErr
	}
	if f.observeCredit != nil {
		return sdk.ObserveResult{Credit: f.observeCredit}, nil
	}
	return sdk.ObserveResult{Pending: true}, nil
}

func (f *walFake) Withdraw(_ context.Context, _ ed25519.PrivateKey, _, _, _, requestID string) (wire.WithdrawalStatus, error) {
	f.withdrawRequests = append(f.withdrawRequests, requestID)
	if f.withdrawErr != nil {
		return wire.WithdrawalStatus{}, f.withdrawErr
	}
	return f.withdrawStatus, nil
}

func walProfile() string {
	return base64.RawURLEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32))
}

func walCookie(profile string) *http.Cookie {
	return &http.Cookie{Name: profileCookie, Value: profile}
}

// walSetup starts a dev client with an identity and, optionally, one trusted provider.
func walSetup(t *testing.T, fake *walFake, withIdentity, withProvider bool) (*Client, string, string) {
	t.Helper()
	_, operatorKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	operator := keys.DIDOf(operatorKey)
	config := Config{DataDir: t.TempDir(), Dev: true}
	if withProvider {
		config.Providers = []Endpoint{{URL: "https://provider.example", ServiceID: operator, Label: "Test provider"}}
	}
	client, err := NewClient(config)
	if err != nil {
		t.Fatal(err)
	}
	profile := walProfile()
	if withIdentity {
		if _, err := client.app.keystore().Create(profile); err != nil {
			t.Fatal(err)
		}
	}
	previous := walFundingFor
	walFundingFor = func(*App, Endpoint) (walFunding, error) { return fake, nil }
	t.Cleanup(func() { walFundingFor = previous })
	return client, profile, operator
}

func walFauxLedger(t *testing.T, client *Client) *faux.Ledger {
	t.Helper()
	ledger, err := faux.Open(faux.Config{JournalPath: filepath.Join(t.TempDir(), "faux.jsonl"), Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(faux.NewHandler(ledger))
	t.Cleanup(server.Close)
	client.app.faux = faux.NewClient(server.URL, server.Client())
	return ledger
}

func walSeedInvoice(t *testing.T, client *Client, profile, operator string, amount string) string {
	t.Helper()
	log, err := walLog(client.app, profile)
	if err != nil {
		t.Fatal(err)
	}
	const depositID = "dep-1"
	now := time.Now()
	recipient := faux.DevAddress(wire.BitcoinMainnet, "recipient")
	invoice := wire.DepositInvoice{DepositID: depositID, ProviderID: operator, AuthorID: walAuthor(t, client, profile), Network: wire.BitcoinMainnet, AmountChainUnits: amount, Recipient: recipient, IssuedAt: wire.FormatTime(now), ExpiresAt: wire.FormatTime(now.Add(time.Hour)), ProviderSignature: "sig"}
	if err := log.Append(sdk.FundingEvent{Kind: sdk.FundingRequested, RequestID: "req-1", Network: wire.BitcoinMainnet, AmountChainUnits: amount, At: wire.FormatTime(now)}); err != nil {
		t.Fatal(err)
	}
	if err := log.Append(sdk.FundingEvent{Kind: sdk.FundingInvoiced, RequestID: "req-1", DepositID: depositID, Network: wire.BitcoinMainnet, AmountChainUnits: amount, Invoice: &invoice, At: wire.FormatTime(now)}); err != nil {
		t.Fatal(err)
	}
	return depositID
}

func walAuthor(t *testing.T, client *Client, profile string) string {
	t.Helper()
	key, err := client.app.keystore().PrivateKey(profile)
	if err != nil {
		t.Fatal(err)
	}
	return keys.DIDOf(key)
}

func walSentTransfer(t *testing.T, client *Client, profile, depositID string) {
	t.Helper()
	log, err := walLog(client.app, profile)
	if err != nil {
		t.Fatal(err)
	}
	index := int64(0)
	transfer := wire.DepositTransfer{Network: wire.BitcoinMainnet, TxID: "txid-1", OutputIndex: &index}
	if err := log.Append(sdk.FundingEvent{Kind: sdk.FundingTransferSent, RequestID: "req-1", DepositID: depositID, Network: wire.BitcoinMainnet, Transfer: &transfer, At: wire.FormatTime(time.Now())}); err != nil {
		t.Fatal(err)
	}
}

func walAcknowledge(t *testing.T, client *Client, profile string) {
	t.Helper()
	w := postForm(client.Handler(), "/wallet/acknowledge", url.Values{"custody": {"on"}, "linkability": {"on"}}, walCookie(profile))
	if w.Code != http.StatusSeeOther {
		t.Fatalf("acknowledge status %d; want 303", w.Code)
	}
}

func walGet(t *testing.T, client *Client, path string, profile string) (int, string) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	if profile != "" {
		r.AddCookie(walCookie(profile))
	}
	w := httptest.NewRecorder()
	client.Handler().ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

func walBalancesFor(available string) map[string]wire.BalanceStatus {
	return map[string]wire.BalanceStatus{
		wire.BitcoinMainnet:  {Network: wire.BitcoinMainnet, Available: available, Reserved: "0", Frozen: "0", UpdatedAt: "2026-11-11T00:00:00Z"},
		wire.EthereumMainnet: {Network: wire.EthereumMainnet, Available: "0", Reserved: "0", Frozen: "0", UpdatedAt: "2026-11-11T00:00:00Z"},
	}
}

func TestWalletEmptyStates(t *testing.T) {
	t.Run("no identity links to identity", func(t *testing.T) {
		client, _, _ := walSetup(t, &walFake{}, false, true)
		code, body := walGet(t, client, "/wallet", "")
		if code != http.StatusOK || !strings.Contains(body, "Create an identity first") || !strings.Contains(body, `href="/identity"`) {
			t.Errorf("status %d; identity empty state missing", code)
		}
	})
	t.Run("no trusted provider links to network", func(t *testing.T) {
		client, profile, _ := walSetup(t, &walFake{}, true, false)
		code, body := walGet(t, client, "/wallet", profile)
		if code != http.StatusOK || !strings.Contains(body, "No trusted provider") || !strings.Contains(body, `href="/network"`) {
			t.Errorf("status %d; provider empty state missing", code)
		}
	})
}

func TestWalletPage(t *testing.T) {
	t.Run("wallet page renders with the active navigation link", func(t *testing.T) {
		client, _, _ := walSetup(t, &walFake{}, false, false)
		code, body := walGet(t, client, "/wallet", "")
		if code != http.StatusOK || !strings.Contains(body, "<h1>Wallet</h1>") || !strings.Contains(body, `href="/wallet" aria-current="page"`) || strings.Contains(body, "not available yet") {
			t.Errorf("status %d; want the wallet page with active navigation and no placeholder", code)
		}
	})
}

func TestWalletDisclosureGate(t *testing.T) {
	t.Run("funding refused before acknowledgement", func(t *testing.T) {
		client, profile, _ := walSetup(t, &walFake{}, true, true)
		w := postForm(client.Handler(), "/wallet/deposits", url.Values{"network": {wire.BitcoinMainnet}, "amount": {"100000"}}, walCookie(profile))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Read the funding disclosure and confirm both points first.") || !strings.Contains(w.Body.String(), "Before you fund") {
			t.Errorf("status %d; want 400 with the disclosure panel", w.Code)
		}
	})
	t.Run("one box is not enough", func(t *testing.T) {
		client, profile, _ := walSetup(t, &walFake{}, true, true)
		w := postForm(client.Handler(), "/wallet/acknowledge/fragment", url.Values{"custody": {"on"}}, walCookie(profile))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `<fragment status="400" target="#wallet-ack-alert(innerHTML)">Tick both boxes to continue.</fragment>`) {
			t.Errorf("status %d body %q; want a fragment that asks for both boxes in the acknowledgement alert", w.Code, w.Body.String())
		}
	})
	t.Run("both boxes reveal add funds in the fragment", func(t *testing.T) {
		client, profile, _ := walSetup(t, &walFake{balances: walBalancesFor("0")}, true, true)
		w := postForm(client.Handler(), "/wallet/acknowledge/fragment", url.Values{"custody": {"on"}, "linkability": {"on"}}, walCookie(profile))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `id="wallet-deposit"`) || strings.Contains(w.Body.String(), "Before you fund") {
			t.Errorf("status %d; add funds panel missing from the fragment", w.Code)
		}
	})
	t.Run("full page acknowledgement redirects to the funding panel", func(t *testing.T) {
		client, profile, _ := walSetup(t, &walFake{}, true, true)
		w := postForm(client.Handler(), "/wallet/acknowledge", url.Values{"custody": {"on"}, "linkability": {"on"}}, walCookie(profile))
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/wallet#wallet-funding" {
			t.Errorf("status %d location %q; want 303 to the funding panel", w.Code, w.Header().Get("Location"))
		}
	})
}

func TestWalletBalances(t *testing.T) {
	t.Run("amounts render with units", func(t *testing.T) {
		client, profile, _ := walSetup(t, &walFake{balances: walBalancesFor("136000000")}, true, true)
		code, body := walGet(t, client, "/wallet", profile)
		if code != http.StatusOK || !strings.Contains(body, "136,000,000 msat (136,000 sat)") || !strings.Contains(body, "As stated by the provider, 11 Nov 2026, 00:00 UTC.") {
			t.Errorf("status %d; balance with unit and source missing", code)
		}
	})
	t.Run("unavailable balance offers retry", func(t *testing.T) {
		client, profile, _ := walSetup(t, &walFake{balanceErr: errors.New("down")}, true, true)
		code, body := walGet(t, client, "/wallet", profile)
		if code != http.StatusOK || !strings.Contains(body, "Unavailable: the provider did not return a verified balance right now.") || !strings.Contains(body, "Retry") {
			t.Errorf("status %d; unavailable signal or retry link missing", code)
		}
	})
}

func TestWalletDevPayment(t *testing.T) {
	t.Run("one faux transfer of the exact amount with empty data", func(t *testing.T) {
		fake := &walFake{balances: walBalancesFor("0")}
		client, profile, operator := walSetup(t, fake, true, true)
		ledger := walFauxLedger(t, client)
		walAcknowledge(t, client, profile)
		depositID := walSeedInvoice(t, client, profile, operator, "100000")
		w := postForm(client.Handler(), "/wallet/deposits/"+depositID+"/pay/fragment", url.Values{}, walCookie(profile))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `<fragment target="#wallet-invoice-dep-1(outerHTML)">`) || !strings.Contains(w.Body.String(), "Already paid; checking.") || strings.Contains(w.Body.String(), "Pay with the faux chain") {
			t.Fatalf("pay status %d; want the paid invoice card", w.Code)
		}
		transfers, err := ledger.Transfers(context.Background(), faux.Query{Network: wire.BitcoinMainnet, Recipient: faux.DevAddress(wire.BitcoinMainnet, "recipient")})
		if err != nil {
			t.Fatal(err)
		}
		if len(transfers) != 1 || transfers[0].AmountChainUnits != "100000" || transfers[0].Data != "" {
			t.Errorf("faux transfers %+v; want one transfer of 100000 with empty data", transfers)
		}
	})
	t.Run("a payment credited at once shows the credit and refreshes balances", func(t *testing.T) {
		fake := &walFake{balances: walBalancesFor("100000000"), observeCredit: &wire.DepositCredit{CreditID: "credit-1", AmountAccountingUnits: "100000000"}}
		client, profile, operator := walSetup(t, fake, true, true)
		walFauxLedger(t, client)
		walAcknowledge(t, client, profile)
		depositID := walSeedInvoice(t, client, profile, operator, "100000")
		w := postForm(client.Handler(), "/wallet/deposits/"+depositID+"/pay/fragment", url.Values{}, walCookie(profile))
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "Credited 100,000,000 msat (100,000 sat)") || !strings.Contains(body, `<fragment target="#wallet-balances(outerHTML)">`) {
			t.Errorf("pay status %d; want the credit and a balances refresh", w.Code)
		}
	})
	t.Run("paying again sends nothing new", func(t *testing.T) {
		fake := &walFake{balances: walBalancesFor("0")}
		client, profile, operator := walSetup(t, fake, true, true)
		ledger := walFauxLedger(t, client)
		walAcknowledge(t, client, profile)
		depositID := walSeedInvoice(t, client, profile, operator, "100000")
		postForm(client.Handler(), "/wallet/deposits/"+depositID+"/pay/fragment", url.Values{}, walCookie(profile))
		postForm(client.Handler(), "/wallet/deposits/"+depositID+"/pay/fragment", url.Values{}, walCookie(profile))
		transfers, err := ledger.Transfers(context.Background(), faux.Query{Network: wire.BitcoinMainnet, Recipient: faux.DevAddress(wire.BitcoinMainnet, "recipient")})
		if err != nil || len(transfers) != 1 {
			t.Errorf("faux transfers %d (%v); want exactly one", len(transfers), err)
		}
	})
	t.Run("reload shows already paid and no pay control", func(t *testing.T) {
		fake := &walFake{balances: walBalancesFor("0")}
		client, profile, operator := walSetup(t, fake, true, true)
		walFauxLedger(t, client)
		walAcknowledge(t, client, profile)
		walSeedInvoice(t, client, profile, operator, "100000")
		walSentTransfer(t, client, profile, "dep-1")
		code, body := walGet(t, client, "/wallet", profile)
		if code != http.StatusOK || !strings.Contains(body, "Already paid; checking.") || strings.Contains(body, "Pay with the faux chain") || !strings.Contains(body, `poll="3000"`) {
			t.Errorf("status %d; want checking state with polling and no pay button", code)
		}
	})
	t.Run("full page pay redirects to the invoice", func(t *testing.T) {
		client, profile, operator := walSetup(t, &walFake{balances: walBalancesFor("0")}, true, true)
		walFauxLedger(t, client)
		walAcknowledge(t, client, profile)
		depositID := walSeedInvoice(t, client, profile, operator, "100000")
		w := postForm(client.Handler(), "/wallet/deposits/"+depositID+"/pay", url.Values{}, walCookie(profile))
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/wallet#wallet-invoice-dep-1" {
			t.Errorf("status %d location %q; want 303 to the invoice", w.Code, w.Header().Get("Location"))
		}
	})
}

func TestWalletObserve(t *testing.T) {
	t.Run("pending keeps polling", func(t *testing.T) {
		fake := &walFake{balances: walBalancesFor("0")}
		client, profile, operator := walSetup(t, fake, true, true)
		walAcknowledge(t, client, profile)
		depositID := walSeedInvoice(t, client, profile, operator, "100000")
		walSentTransfer(t, client, profile, depositID)
		w := postForm(client.Handler(), "/wallet/deposits/"+depositID+"/observe/fragment", url.Values{}, walCookie(profile))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `poll="3000"`) || !strings.Contains(w.Body.String(), "Waiting for the provider to confirm") || !strings.Contains(w.Body.String(), "Not spendable yet.") {
			t.Errorf("status %d; want a pending card that keeps polling", w.Code)
		}
	})
	t.Run("credit replaces the card and refreshes balances", func(t *testing.T) {
		fake := &walFake{balances: walBalancesFor("100000000"), observeCredit: &wire.DepositCredit{CreditID: "credit-1", AmountAccountingUnits: "100000000"}}
		client, profile, operator := walSetup(t, fake, true, true)
		walAcknowledge(t, client, profile)
		depositID := walSeedInvoice(t, client, profile, operator, "100000")
		walSentTransfer(t, client, profile, depositID)
		w := postForm(client.Handler(), "/wallet/deposits/"+depositID+"/observe/fragment", url.Values{}, walCookie(profile))
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "Credited 100,000,000 msat (100,000 sat)") || strings.Contains(body, `poll="3000"`) || !strings.Contains(body, `<fragment target="#wallet-observe-dep-1(outerHTML)">`) || !strings.Contains(body, `target="#wallet-balances(outerHTML)"`) {
			t.Errorf("status %d; want a credited card and a balances refresh", w.Code)
		}
	})
	t.Run("full page check redirects to the invoice", func(t *testing.T) {
		fake := &walFake{balances: walBalancesFor("0")}
		client, profile, operator := walSetup(t, fake, true, true)
		walAcknowledge(t, client, profile)
		depositID := walSeedInvoice(t, client, profile, operator, "100000")
		walSentTransfer(t, client, profile, depositID)
		w := postForm(client.Handler(), "/wallet/deposits/"+depositID+"/observe", url.Values{}, walCookie(profile))
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/wallet#wallet-invoice-dep-1" || fake.observeCalls != 1 {
			t.Errorf("status %d location %q calls %d; want one check and 303 to the invoice", w.Code, w.Header().Get("Location"), fake.observeCalls)
		}
	})
	t.Run("a stale poll stops and offers check again", func(t *testing.T) {
		fake := &walFake{balances: walBalancesFor("0")}
		client, profile, operator := walSetup(t, fake, true, true)
		walAcknowledge(t, client, profile)
		depositID := walSeedInvoice(t, client, profile, operator, "100000")
		walSentTransfer(t, client, profile, depositID)
		since := time.Now().Add(-11 * time.Minute).Unix()
		w := postForm(client.Handler(), "/wallet/deposits/"+depositID+"/observe/fragment?since="+strconv.FormatInt(since, 10), url.Values{}, walCookie(profile))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Not confirmed yet") || strings.Contains(w.Body.String(), `poll="3000"`) || fake.observeCalls != 0 {
			t.Errorf("status %d calls %d; want check again without polling or a provider call", w.Code, fake.observeCalls)
		}
	})
}

// walTrust installs the dev network trust root, so the fee schedule in force is known.
func walTrust(t *testing.T, client *Client) {
	t.Helper()
	genesis := devnet.Genesis()
	now := time.Now()
	root, err := sdk.BuildTrustRoot(sdk.Pin{ReleaseKeyIDs: genesis.ReleaseKeyIDs, Threshold: int(genesis.ReleaseThreshold)}, sdk.Publications{Genesis: genesis, FetchedAt: now, Endpoint: "https://foundation.example"})
	if err != nil {
		t.Fatal(err)
	}
	client.app.trustMu.Lock()
	client.app.root = root
	client.app.trustStatus = TrustStatus{State: TrustReady, FetchedAt: now, Endpoint: "https://foundation.example"}
	client.app.trustMu.Unlock()
}

func TestWalletDeposit(t *testing.T) {
	form := url.Values{"network": {wire.BitcoinMainnet}, "amount": {"100000"}}
	t.Run("rejected invoice shows the reason in the deposit alert", func(t *testing.T) {
		fake := &walFake{balances: walBalancesFor("0"), requestErr: fmt.Errorf("%w: amount mismatch", sdk.ErrInvoiceRejected)}
		client, profile, _ := walSetup(t, fake, true, true)
		walTrust(t, client)
		walAcknowledge(t, client, profile)
		w := postForm(client.Handler(), "/wallet/deposits/fragment", form, walCookie(profile))
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, `<fragment status="400" target="#wallet-deposit-alert(innerHTML)">`) || !strings.Contains(body, "invoice was rejected and will not be paid: amount mismatch") || strings.Contains(body, "Pay with the faux chain") {
			t.Errorf("status %d body %q; want the rejection in the deposit alert and no pay button", w.Code, body)
		}
	})
	t.Run("rejected invoice on a plain form shows the page without a pay button", func(t *testing.T) {
		fake := &walFake{balances: walBalancesFor("0"), requestErr: fmt.Errorf("%w: amount mismatch", sdk.ErrInvoiceRejected)}
		client, profile, _ := walSetup(t, fake, true, true)
		walTrust(t, client)
		walAcknowledge(t, client, profile)
		w := postForm(client.Handler(), "/wallet/deposits", form, walCookie(profile))
		body := w.Body.String()
		if w.Code != http.StatusBadRequest || !strings.Contains(body, "invoice was rejected and will not be paid: amount mismatch") || !strings.Contains(body, "Add funds") || strings.Contains(body, "Pay with the faux chain") {
			t.Errorf("status %d; want 400 with the reason, the add funds panel and no pay button", w.Code)
		}
	})
	t.Run("accepted request in a fragment replaces the panel and the invoices", func(t *testing.T) {
		client, profile, _ := walSetup(t, &walFake{balances: walBalancesFor("0")}, true, true)
		walTrust(t, client)
		walAcknowledge(t, client, profile)
		w := postForm(client.Handler(), "/wallet/deposits/fragment", form, walCookie(profile))
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, `<fragment target="#wallet-deposit(outerHTML)">`) || !strings.Contains(body, "Invoice requested.") || !strings.Contains(body, `<fragment target="#wallet-invoices(outerHTML)">`) {
			t.Errorf("status %d body %q; want both the deposit panel and the invoices as fragments", w.Code, body)
		}
	})
	t.Run("accepted request on a plain form redirects to the invoices", func(t *testing.T) {
		client, profile, _ := walSetup(t, &walFake{balances: walBalancesFor("0")}, true, true)
		walTrust(t, client)
		walAcknowledge(t, client, profile)
		w := postForm(client.Handler(), "/wallet/deposits", form, walCookie(profile))
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/wallet#wallet-invoices" {
			t.Errorf("status %d location %q; want 303 to the invoices", w.Code, w.Header().Get("Location"))
		}
	})
}

func TestWalletWithdrawal(t *testing.T) {
	address := faux.DevAddress(wire.BitcoinMainnet, "payout")
	t.Run("pending withdrawal shows and the request id is kept on resubmission", func(t *testing.T) {
		fake := &walFake{balances: walBalancesFor("0"), withdrawStatus: wire.WithdrawalStatus{Status: "pending", Network: wire.BitcoinMainnet, AmountBaseUnits: "136000", Recipient: address, RequestID: "req-w"}}
		client, profile, _ := walSetup(t, fake, true, true)
		form := url.Values{"network": {wire.BitcoinMainnet}, "amount": {"136000"}, "recipient": {address}, "request_id": {"req-w"}}
		first := postForm(client.Handler(), "/wallet/withdrawals/fragment", form, walCookie(profile))
		second := postForm(client.Handler(), "/wallet/withdrawals/fragment", form, walCookie(profile))
		if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), "Pending") || !strings.Contains(first.Body.String(), "136,000 msat (136 sat)") {
			t.Errorf("status %d; want the pending withdrawal shown", first.Code)
		}
		if second.Code != http.StatusOK || len(fake.withdrawRequests) != 2 || fake.withdrawRequests[0] != "req-w" || fake.withdrawRequests[1] != "req-w" {
			t.Errorf("request ids %v; want the same id twice", fake.withdrawRequests)
		}
	})
	t.Run("insufficient balance is explained", func(t *testing.T) {
		fake := &walFake{balances: walBalancesFor("0"), withdrawErr: &sdk.Error{HTTPStatus: http.StatusPaymentRequired, Status: "insufficient_balance"}}
		client, profile, _ := walSetup(t, fake, true, true)
		w := postForm(client.Handler(), "/wallet/withdrawals/fragment", url.Values{"network": {wire.BitcoinMainnet}, "amount": {"136000"}, "recipient": {address}, "request_id": {"req-w"}}, walCookie(profile))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `<fragment status="402" target="#wallet-withdraw-alert(innerHTML)">Not enough available balance.</fragment>`) {
			t.Errorf("status %d body %q; want a 200 fragment that carries 402 into the withdraw alert", w.Code, w.Body.String())
		}
	})
	t.Run("plain form refusal keeps its status and shows the page", func(t *testing.T) {
		fake := &walFake{balances: walBalancesFor("0"), withdrawErr: &sdk.Error{HTTPStatus: http.StatusPaymentRequired, Status: "insufficient_balance"}}
		client, profile, _ := walSetup(t, fake, true, true)
		w := postForm(client.Handler(), "/wallet/withdrawals", url.Values{"network": {wire.BitcoinMainnet}, "amount": {"136000"}, "recipient": {address}, "request_id": {"req-w"}}, walCookie(profile))
		if w.Code != http.StatusPaymentRequired || !strings.Contains(w.Body.String(), "Not enough available balance.") || !strings.Contains(w.Body.String(), "<h1>Wallet</h1>") || !strings.Contains(w.Body.String(), `name="request_id" value="req-w"`) {
			t.Errorf("status %d; want 402 with the wallet page, the balance message and the same request id", w.Code)
		}
	})
	t.Run("address for another network is refused", func(t *testing.T) {
		client, profile, _ := walSetup(t, &walFake{balances: walBalancesFor("0")}, true, true)
		w := postForm(client.Handler(), "/wallet/withdrawals/fragment", url.Values{"network": {wire.EthereumMainnet}, "amount": {"136000"}, "recipient": {address}, "request_id": {"req-w"}}, walCookie(profile))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `<fragment status="400" target="#wallet-withdraw-alert(innerHTML)">The recipient address is not valid for this network.</fragment>`) {
			t.Errorf("status %d body %q; want a 200 fragment that carries 400 into the withdraw alert", w.Code, w.Body.String())
		}
	})
}

func TestWalletAmountText(t *testing.T) {
	t.Run("msat with the satoshi equivalent", func(t *testing.T) {
		if got := walFormat(wire.BitcoinMainnet, walMustChain("136000000")); got != "136,000,000 msat (136,000 sat)" {
			t.Errorf("walFormat = %q", got)
		}
	})
	t.Run("msat without a whole satoshi", func(t *testing.T) {
		if got := walFormat(wire.BitcoinMainnet, walMustChain("1500")); got != "1,500 msat" {
			t.Errorf("walFormat = %q", got)
		}
	})
	t.Run("wei with the ETH equivalent to six decimals", func(t *testing.T) {
		if got := walFormat(wire.EthereumMainnet, walMustChain("1500000000000000000")); got != "1,500,000,000,000,000,000 wei (1.500000 ETH)" {
			t.Errorf("walFormat = %q", got)
		}
	})
	t.Run("deposit ID becomes a selector-safe element ID", func(t *testing.T) {
		if got := domKey("fydi2:76a0"); got != "fydi2-76a0" {
			t.Errorf("domKey = %q", got)
		}
	})
	t.Run("custody cap refusal names the limit", func(t *testing.T) {
		schedule := wire.FeeSchedule{Network: wire.BitcoinMainnet, BaseFeeBaseUnits: "136", PerKiBFeeBaseUnits: "0"}
		if got := walDepositMessage(fmt.Errorf("%w", sdk.ErrCustodyCap), schedule); got != "This deposit would take your balance above the custody cap of 136,000,000 msat (136,000 sat)." {
			t.Errorf("message = %q", got)
		}
	})
	t.Run("rejected invoice shows the reason", func(t *testing.T) {
		err := fmt.Errorf("%w: amount mismatch", sdk.ErrInvoiceRejected)
		if got := walDepositMessage(err, wire.FeeSchedule{}); got != "The provider's invoice was rejected and will not be paid: amount mismatch" {
			t.Errorf("message = %q", got)
		}
	})
}
