package server

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/wire"
)

// walCreditedDeposit pays the seeded invoice on the faux chain and records its credit in the funding log.
func walCreditedDeposit(t *testing.T, client *Client, ledger *faux.Ledger, profile, operator string) (string, faux.Transfer) {
	t.Helper()
	depositID := walSeedInvoice(t, client, profile, operator, "100000")
	paid, err := ledger.Submit(context.Background(), faux.SubmitRequest{Network: wire.BitcoinMainnet, Recipient: faux.DevAddress(wire.BitcoinMainnet, "recipient"), AmountChainUnits: "100000"})
	if err != nil {
		t.Fatal(err)
	}
	log, err := walLog(client.app, profile)
	if err != nil {
		t.Fatal(err)
	}
	transfer := walTransferFrom(paid)
	now := wire.FormatTime(time.Now())
	if err := log.Append(sdk.FundingEvent{Kind: sdk.FundingTransferSent, RequestID: "req-1", DepositID: depositID, Network: wire.BitcoinMainnet, Transfer: &transfer, At: now}); err != nil {
		t.Fatal(err)
	}
	if err := log.Append(sdk.FundingEvent{Kind: sdk.FundingCredited, RequestID: "req-1", DepositID: depositID, Network: wire.BitcoinMainnet, CreditID: "credit-1", At: now}); err != nil {
		t.Fatal(err)
	}
	return depositID, paid
}

func TestWalletReorganization(t *testing.T) {
	setup := func(t *testing.T, frozen string) (*Client, *walFake, *faux.Ledger, string, string, faux.Transfer) {
		t.Helper()
		balances := walBalancesFor("0")
		balances[wire.BitcoinMainnet] = wire.BalanceStatus{Network: wire.BitcoinMainnet, Available: "0", Reserved: "0", Frozen: frozen, UpdatedAt: "2026-11-11T00:00:00Z"}
		fake := &walFake{balances: balances}
		client, profile, operator := walSetup(t, fake, true, true)
		ledger := walFauxLedger(t, client)
		walAcknowledge(t, client, profile)
		depositID, paid := walCreditedDeposit(t, client, ledger, profile, operator)
		return client, fake, ledger, profile, depositID, paid
	}
	t.Run("the page offers to drop a credited transfer", func(t *testing.T) {
		client, _, _, profile, _, _ := setup(t, "0")
		code, body := walGet(t, client, "/wallet", profile)
		if code != http.StatusOK || !strings.Contains(body, "Simulate a chain reorganization") || !strings.Contains(body, `action="/wallet/deposits/dep-1/drop"`) || !strings.Contains(body, "On the chain") {
			t.Errorf("status %d; want the panel with a drop control for dep-1", code)
		}
	})
	t.Run("dropping removes the transfer and asks the provider to look", func(t *testing.T) {
		client, fake, ledger, profile, depositID, paid := setup(t, "100000000")
		w := postForm(client.Handler(), "/wallet/deposits/"+depositID+"/drop/fragment", url.Values{}, walCookie(profile))
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, `<fragment target="#wallet-reorg(outerHTML)">`) || !strings.Contains(body, "Not on the chain") || !strings.Contains(body, `action="/wallet/deposits/dep-1/mine"`) {
			t.Fatalf("drop status %d; want the panel showing the transfer gone and a mine control", w.Code)
		}
		if !strings.Contains(body, `<fragment target="#wallet-balances(outerHTML)">`) || !strings.Contains(body, "Frozen funds come from a deposit") {
			t.Errorf("drop response lacks the refreshed balances with the frozen explanation")
		}
		if _, err := ledger.Transfer(context.Background(), wire.BitcoinMainnet, paid.TxID); !errors.Is(err, faux.ErrNotFound) {
			t.Errorf("faux transfer = %v; want ErrNotFound", err)
		}
		if fake.observeCalls != 1 {
			t.Errorf("provider observations = %d; want 1", fake.observeCalls)
		}
	})
	t.Run("mining again puts the transfer back", func(t *testing.T) {
		client, _, ledger, profile, depositID, paid := setup(t, "0")
		if err := ledger.Orphan(context.Background(), wire.BitcoinMainnet, paid.TxID); err != nil {
			t.Fatal(err)
		}
		w := postForm(client.Handler(), "/wallet/deposits/"+depositID+"/mine/fragment", url.Values{}, walCookie(profile))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "On the chain") || !strings.Contains(w.Body.String(), "Mined again.") {
			t.Fatalf("mine status %d; want the panel showing the transfer back", w.Code)
		}
		back, err := ledger.Transfer(context.Background(), wire.BitcoinMainnet, paid.TxID)
		if err != nil || !back.Finalized {
			t.Errorf("faux transfer = %+v, %v; want it final again", back, err)
		}
	})
	t.Run("the funding log gains nothing", func(t *testing.T) {
		client, _, _, profile, depositID, _ := setup(t, "0")
		postForm(client.Handler(), "/wallet/deposits/"+depositID+"/drop/fragment", url.Values{}, walCookie(profile))
		log, err := walLog(client.app, profile)
		if err != nil {
			t.Fatal(err)
		}
		attempts, err := log.Attempts()
		if err != nil || len(attempts) != 1 || !attempts[0].Credited || attempts[0].Failed {
			t.Errorf("attempts = %+v, %v; want the one credited attempt unchanged", attempts, err)
		}
	})
	t.Run("a deposit that is not credited is refused", func(t *testing.T) {
		client, profile, operator := walSetup(t, &walFake{balances: walBalancesFor("0")}, true, true)
		walFauxLedger(t, client)
		walAcknowledge(t, client, profile)
		depositID := walSeedInvoice(t, client, profile, operator, "100000")
		w := postForm(client.Handler(), "/wallet/deposits/"+depositID+"/drop/fragment", url.Values{}, walCookie(profile))
		if !strings.Contains(w.Body.String(), "This is not a credited deposit in your funding log.") {
			t.Errorf("drop of an unpaid invoice = %d %s; want the refusal", w.Code, w.Body.String())
		}
	})
	t.Run("without script the action returns to the panel", func(t *testing.T) {
		client, _, _, profile, depositID, _ := setup(t, "0")
		w := postForm(client.Handler(), "/wallet/deposits/"+depositID+"/drop", url.Values{}, walCookie(profile))
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/wallet#wallet-reorg" {
			t.Errorf("status %d location %q; want 303 to the panel", w.Code, w.Header().Get("Location"))
		}
	})
}
