package server

import (
	"context"
	"crypto/ed25519"
	"errors"
	"strings"
	"testing"

	"foryou.local/foryou/internal/wire"
)

// walHistoryFake is a provider that also serves an account history.
type walHistoryFake struct {
	walFake
	page  wire.AccountHistoryPage
	err   error
	after []uint64
}

func (f *walHistoryFake) AccountHistory(_ context.Context, _ ed25519.PrivateKey, _ string, after uint64) (wire.AccountHistoryPage, error) {
	f.after = append(f.after, after)
	return f.page, f.err
}

func walHistorySetup(t *testing.T, fake *walHistoryFake) (*Client, string) {
	t.Helper()
	client, profile, _ := walSetup(t, &fake.walFake, true, true)
	walFundingFor = func(*App, Endpoint) (walFunding, error) { return fake, nil }
	return client, profile
}

func TestWalletHistory(t *testing.T) {
	t.Run("lists entries with their direction and reference", func(t *testing.T) {
		fake := &walHistoryFake{page: wire.AccountHistoryPage{Entries: []wire.AccountHistoryEntry{
			{Sequence: 1, At: "2026-11-12T08:00:00Z", Kind: "deposit_credit", AmountBaseUnits: "5000000", Reference: "credit-1"},
			{Sequence: 2, At: "2026-11-12T08:05:00Z", Kind: "debit", AmountBaseUnits: "21000", Reference: "receipt-1"},
		}}}
		client, profile := walHistorySetup(t, fake)
		body := getWith(client.Handler(), "/wallet/history?network=bitcoin-mainnet", walCookie(profile))
		if !strings.Contains(body, "Deposit credited") || !strings.Contains(body, "+5,000,000 msat (5,000 sat)") {
			t.Error("history does not show the credited deposit as an addition of 5,000,000 msat")
		}
		if !strings.Contains(body, "Post fee") || !strings.Contains(body, "−21,000 msat (21 sat)") || !strings.Contains(body, "receipt-1") {
			t.Error("history does not show the post fee as a subtraction of 21,000 msat with its receipt")
		}
		if strings.Contains(body, "Later entries") {
			t.Error("history offers later entries although the provider said there are none")
		}
	})
	t.Run("continues from the last entry when the provider has more", func(t *testing.T) {
		fake := &walHistoryFake{page: wire.AccountHistoryPage{More: true, Entries: []wire.AccountHistoryEntry{
			{Sequence: 101, At: "2026-11-12T08:00:00Z", Kind: "debit", AmountBaseUnits: "21000", Reference: "receipt-101"},
		}}}
		client, profile := walHistorySetup(t, fake)
		body := getWith(client.Handler(), "/wallet/history?network=bitcoin-mainnet&after=100", walCookie(profile))
		if len(fake.after) != 1 || fake.after[0] != 100 {
			t.Errorf("provider was asked for entries after %v; want one call after 100", fake.after)
		}
		if !strings.Contains(body, `href="/wallet/history?after=101&amp;network=bitcoin-mainnet"`) {
			t.Error("history does not link to the entries after number 101")
		}
	})
	t.Run("says so when the provider returns no verified page", func(t *testing.T) {
		fake := &walHistoryFake{err: errors.New("bad signature")}
		client, profile := walHistorySetup(t, fake)
		body := getWith(client.Handler(), "/wallet/history", walCookie(profile))
		if !strings.Contains(body, "the provider did not return a verified history") || strings.Contains(body, "bad signature") {
			t.Error("history should state it is unavailable without showing the internal error")
		}
	})
	t.Run("asks for an identity first", func(t *testing.T) {
		client, _, _ := walSetup(t, &walFake{}, false, true)
		body := getWith(client.Handler(), "/wallet/history")
		if !strings.Contains(body, "Create an identity first") {
			t.Error("history without an identity does not ask to create one")
		}
	})
}
