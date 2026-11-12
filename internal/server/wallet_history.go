package server

import (
	"context"
	"crypto/ed25519"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/wire"
)

func init() { routeGroups = append(routeGroups, registerWalletHistory) }

func registerWalletHistory(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /wallet/history", a.walHistoryHandler)
}

const walHistoryWait = 5 * time.Second

// walHistorian is the provider's signed account history; *sdk.Client satisfies it.
type walHistorian interface {
	AccountHistory(ctx context.Context, key ed25519.PrivateKey, network string, after uint64) (wire.AccountHistoryPage, error)
}

type walHistoryTab struct {
	Label, Href string
	Current     bool
}

type walHistoryRow struct {
	Sequence                      int64
	When, What, Amount, Reference string
	Credit                        bool
}

type walHistoryView struct {
	Layout      Layout
	HasIdentity bool
	HasProvider bool
	Provider    string
	Network     string
	Tabs        []walHistoryTab
	Unavailable bool
	RetryHref   string
	After       uint64
	First       int64
	Rows        []walHistoryRow
	NextHref    string
}

// walHistoryKind is the wording of one entry kind and whether it adds to the balance.
type walHistoryKind struct {
	What   string
	Credit bool
}

var walHistoryKinds = map[string]walHistoryKind{
	"deposit_credit":    {"Deposit credited", true},
	"deposit_reversal":  {"Deposit reversed", false},
	"debit":             {"Post fee", false},
	"fee_restore":       {"Post fee returned", true},
	"withdrawal":        {"Withdrawal requested", false},
	"withdrawal_return": {"Withdrawal returned", true},
}

func walHistoryHref(network string, after uint64) string {
	query := url.Values{"network": {network}}
	if after > 0 {
		query.Set("after", strconv.FormatUint(after, 10))
	}
	return "/wallet/history?" + query.Encode()
}

func walHistoryRows(network string, entries []wire.AccountHistoryEntry) []walHistoryRow {
	rows := make([]walHistoryRow, 0, len(entries))
	for _, entry := range entries {
		kind, known := walHistoryKinds[entry.Kind]
		row := walHistoryRow{Sequence: entry.Sequence, When: walWhen(entry.At), What: kind.What, Credit: kind.Credit, Reference: entry.Reference, Amount: entry.AmountBaseUnits}
		if !known {
			row.What = entry.Kind
		}
		if n, err := fee.ParseAmount(entry.AmountBaseUnits); err == nil {
			row.Amount = walFormat(network, n)
		}
		rows = append(rows, row)
	}
	return rows
}

func (a *App) walHistoryView(r *http.Request, profile string) walHistoryView {
	network := r.URL.Query().Get("network")
	if !walSupported(network) {
		network = walNetworks[0]
	}
	after, err := strconv.ParseUint(r.URL.Query().Get("after"), 10, 63)
	if err != nil {
		after = 0
	}
	view := walHistoryView{Layout: a.layout(r, "wallet", "Account history"), Network: walLabels[network], After: after, RetryHref: walHistoryHref(network, after)}
	for _, n := range walNetworks {
		view.Tabs = append(view.Tabs, walHistoryTab{Label: walLabels[n], Href: walHistoryHref(n, 0), Current: n == network})
	}
	key, hasKey, err := a.walKey(profile)
	if err != nil || !hasKey {
		return view
	}
	view.HasIdentity = true
	choice, ok := a.publishingProvider(profile)
	if !ok {
		return view
	}
	view.HasProvider, view.Provider = true, choice.Label
	funding, err := walFundingFor(a, walEndpoint(choice))
	if err != nil {
		view.Unavailable = true
		return view
	}
	historian, ok := funding.(walHistorian)
	if !ok {
		view.Unavailable = true
		return view
	}
	ctx, cancel := context.WithTimeout(r.Context(), walHistoryWait)
	defer cancel()
	page, err := historian.AccountHistory(ctx, key, network, after)
	if err != nil {
		view.Unavailable = true
		return view
	}
	view.Rows = walHistoryRows(network, page.Entries)
	if len(page.Entries) > 0 {
		view.First = page.Entries[0].Sequence
		if page.More {
			view.NextHref = walHistoryHref(network, uint64(page.Entries[len(page.Entries)-1].Sequence))
		}
	}
	return view
}

func (a *App) walHistoryHandler(w http.ResponseWriter, r *http.Request) {
	a.page(w, r, "wallet-history-page", a.walHistoryView(r, requestProfile(r)))
}
