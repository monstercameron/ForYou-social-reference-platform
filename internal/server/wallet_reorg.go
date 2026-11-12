package server

import (
	"context"
	"errors"
	"net/http"

	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/sdk"
)

// walReorgLimit bounds the credited deposits the dev reorganization panel lists.
const walReorgLimit = 3

type walReorgDeposit struct {
	DepositID, Amount string
	Dropped           bool
}

// walReorg is the dev-only panel that drops a credited transfer from the faux chain
// and mines it again.
type walReorg struct {
	Show     bool
	Deposits []walReorgDeposit
	Notice   string
}

// walReadOnlyLog lets a credited deposit be observed again without adding to its history.
type walReadOnlyLog struct{ sdk.FundingLog }

func (walReadOnlyLog) Append(sdk.FundingEvent) error { return nil }

func init() {
	routeGroups = append(routeGroups, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("POST /wallet/deposits/{id}/drop", a.walReorgHandler(true))
		mux.HandleFunc("POST /wallet/deposits/{id}/drop"+walFragmentPath, a.walReorgHandler(true))
		mux.HandleFunc("POST /wallet/deposits/{id}/mine", a.walReorgHandler(false))
		mux.HandleFunc("POST /wallet/deposits/{id}/mine"+walFragmentPath, a.walReorgHandler(false))
	})
}

// walReorgView lists the latest credited deposits and whether the faux chain still shows each transfer.
func (a *App) walReorgView(ctx context.Context, attempts []sdk.FundingAttempt) walReorg {
	view := walReorg{}
	if !a.config.Dev || a.faux == nil {
		return view
	}
	for i := len(attempts) - 1; i >= 0 && len(view.Deposits) < walReorgLimit; i-- {
		at := attempts[i]
		if !at.Credited || at.Transfer == nil || at.Invoice == nil {
			continue
		}
		callCtx, cancel := context.WithTimeout(ctx, walBalanceWait)
		_, err := a.faux.Transfer(callCtx, at.Network, at.Transfer.TxID+at.Transfer.TxHash)
		cancel()
		if err != nil && !errors.Is(err, faux.ErrNotFound) {
			continue
		}
		view.Deposits = append(view.Deposits, walReorgDeposit{DepositID: at.DepositID, Amount: walChainText(at.Network, walMustChain(at.Invoice.AmountChainUnits)), Dropped: err != nil})
	}
	view.Show = len(view.Deposits) > 0
	return view
}

func (a *App) walReorgHandler(drop bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if _, ok := readActionForm(w, r); !ok {
			return
		}
		profile, err := a.profile(w, r)
		if err != nil {
			a.fail(w, r, http.StatusInternalServerError, "The wallet could not start a session.")
			return
		}
		fail := func(status int, message string) {
			a.walFail(w, r, profile, status, "wallet-reorg-alert", walOptions{Balances: true, Reorg: true, Errors: walErrors{Reorg: message}}, message)
		}
		if !a.config.Dev || a.faux == nil {
			fail(http.StatusConflict, "A reorganization can be simulated only in dev mode.")
			return
		}
		key, hasKey, err := a.walKey(profile)
		if err != nil || !hasKey {
			fail(http.StatusConflict, "Create an identity first.")
			return
		}
		log, err := walLog(a, profile)
		if err != nil {
			fail(http.StatusInternalServerError, "The funding log could not be opened.")
			return
		}
		attempts, err := log.Attempts()
		if err != nil {
			fail(http.StatusInternalServerError, "The funding log could not be read.")
			return
		}
		at, ok := walAttemptFor(attempts, r.PathValue("id"))
		if !ok || !at.Credited || at.Transfer == nil || at.Invoice == nil {
			fail(http.StatusNotFound, "This is not a credited deposit in your funding log.")
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), walFundingWait)
		defer cancel()
		transaction := at.Transfer.TxID + at.Transfer.TxHash
		notice := "Mined again. The provider releases the frozen funds, or restores the credit if it had already reversed it."
		if drop {
			notice = "Dropped. The provider freezes the unspent part of this credit, and reverses it after its reversal delay unless the transfer is mined again."
			err = a.faux.Orphan(ctx, at.Network, transaction)
		} else {
			_, err = a.faux.Reinclude(ctx, at.Network, transaction)
		}
		switch {
		case errors.Is(err, faux.ErrNotFound):
			fail(http.StatusConflict, "The faux chain does not show this transfer.")
			return
		case errors.Is(err, faux.ErrConflict):
			fail(http.StatusConflict, "This transfer is already on the faux chain.")
			return
		case err != nil:
			fail(http.StatusBadGateway, "The faux chain could not be reached. Try again.")
			return
		}
		// The author's own retry makes the provider look at the chain now, not on its next check.
		if choice, ok := a.publishingProvider(profile); ok {
			if funding, err := walFundingFor(a, walEndpoint(choice)); err == nil {
				_, _ = funding.ObserveDeposit(ctx, key, *at.Invoice, *at.Transfer, walReadOnlyLog{log})
			}
		}
		if !walFragment(r) {
			http.Redirect(w, r, "/wallet#wallet-reorg", http.StatusSeeOther)
			return
		}
		view := a.walView(r, profile, walOptions{Balances: true, Reorg: true})
		view.Reorg.Notice = notice
		a.fragment(w, r, "wallet-reorg-response", view)
	}
}
