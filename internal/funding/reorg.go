package funding

import (
	"context"
	"errors"
	"math/big"
	"sort"
	"time"

	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/wire"
)

// creditHold is the state of a credit whose transfer the chain stopped showing as
// final. Its amount is the part of the credit that was still unspent when it was
// frozen; the provider carries the rest.
type creditHold struct {
	amount   *big.Int
	since    time.Time
	reversed bool
}

// Reconcile re-checks credited transfers against the chain. A credit whose transfer
// is no longer final has its unspent part frozen. A frozen credit is released when
// the transfer is final again and reversed when it is still missing after the
// reversal delay; a reversed credit is restored if the transfer comes back. A chain
// that cannot be read changes nothing.
func (l *Ledger) Reconcile(ctx context.Context, chain faux.Chain) error {
	if chain == nil {
		return ErrInvalidRequest
	}
	var first error
	for _, id := range l.reconcilable() {
		if err := l.recheck(ctx, chain, id); err != nil && first == nil {
			first = err
		}
	}
	for depositID, t := range l.expiredPending() {
		_, err := chain.Transfer(ctx, t.Network, lookupID(t))
		if errors.Is(err, faux.ErrNotFound) {
			err = l.dropPending(depositID, transferKey(t))
		}
		if err != nil && first == nil {
			first = err
		}
	}
	return first
}

// reconcilable lists the credits to re-check: those credited inside the watch
// window, and every frozen credit.
func (l *Ledger) reconcilable() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	horizon := l.clock().Add(-l.cfg.ReorgWatch)
	for len(l.watched) > 0 {
		at, err := wire.ParseTime(l.credits[l.watched[0]].CreditedAt)
		if err == nil && at.After(horizon) {
			break
		}
		l.watched = l.watched[1:]
	}
	ids := append([]string(nil), l.watched...)
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		seen[id] = true
	}
	var frozen []string
	for id, hold := range l.holds {
		if !hold.reversed && !seen[id] {
			frozen = append(frozen, id)
		}
	}
	sort.Strings(frozen)
	return append(ids, frozen...)
}

// expiredPending lists the pending observations of expired invoices by deposit ID.
func (l *Ledger) expiredPending() map[string]wire.DepositTransfer {
	l.mu.RLock()
	defer l.mu.RUnlock()
	now := l.clock()
	out := make(map[string]wire.DepositTransfer)
	for id := range l.outstanding {
		if st := l.invoices[id]; st.pending != nil && !st.open(now) {
			out[id] = cloneTransfer(st.pending.transfer)
		}
	}
	return out
}

// recheck compares one credit with the chain and commits the change that follows.
func (l *Ledger) recheck(ctx context.Context, chain faux.Chain, transferID string) error {
	l.mu.RLock()
	credit, ok := l.credits[transferID]
	recipient := ""
	if ok {
		recipient = l.invoices[credit.DepositID].invoice.Recipient
	}
	l.mu.RUnlock()
	if !ok {
		return nil
	}
	stands, err := creditStands(ctx, chain, credit, recipient)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now, err := l.mutationTime()
	if err != nil {
		return err
	}
	hold := l.holds[transferID]
	var kind string
	switch {
	case hold == nil && !stands:
		kind = kindCreditFreeze
	case hold != nil && stands && hold.reversed:
		kind = kindCreditRestore
	case hold != nil && stands:
		kind = kindCreditRelease
	case hold != nil && !hold.reversed && !now.Before(hold.since.Add(l.cfg.ReversalDelay)):
		kind = kindCreditReversal
	default:
		return nil
	}
	return l.commit(record{Kind: kind, At: wire.FormatTime(now), TransferID: transferID})
}

// creditStands reports whether the chain still shows the credited transfer as the
// final payment the credit was issued for. An unknown transfer does not stand; any
// other lookup failure is an error, because an unreachable chain proves nothing.
func creditStands(ctx context.Context, chain faux.Chain, credit wire.DepositCredit, recipient string) (bool, error) {
	t, err := chain.Transfer(ctx, credit.Transfer.Network, lookupID(credit.Transfer))
	if errors.Is(err, faux.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if credit.Transfer.OutputIndex != nil && (t.OutputIndex == nil || int64(*t.OutputIndex) != *credit.Transfer.OutputIndex) {
		return false, nil
	}
	return t.Success && !t.Coinbase && final(t) && t.AmountChainUnits == credit.AmountChainUnits && paysRecipient(t, recipient), nil
}

// rechecks reports whether a retry for this credit looks at the chain again: the
// credit is frozen or reversed, or still inside the watch window.
func (l *Ledger) rechecks(transferID string) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if l.holds[transferID] != nil {
		return true
	}
	at, err := wire.ParseTime(l.credits[transferID].CreditedAt)
	return err == nil && at.After(l.clock().Add(-l.cfg.ReorgWatch))
}

// dropPending forgets the pending observation of a transfer the chain no longer
// shows. The invoice keeps its recipient, so the transfer is assessed again if it
// is ever mined.
func (l *Ledger) dropPending(depositID, transferID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.invoices[depositID]
	if st == nil || st.pending == nil || st.pending.transferID != transferID {
		return nil
	}
	now, err := l.mutationTime()
	if err != nil {
		return err
	}
	return l.commit(record{Kind: kindPendingDropped, At: wire.FormatTime(now), DepositID: depositID, TransferID: transferID})
}

func (l *Ledger) stagePendingDropped(r record) (func(), error) {
	st := l.invoices[r.DepositID]
	if st == nil || st.pending == nil || st.pending.transferID != r.TransferID {
		return nil, errors.New("funding: no such pending observation")
	}
	return func() { st.pending = nil }, nil
}

// heldCredit finds the credit and hold a record names and its account.
func (l *Ledger) heldCredit(transferID string) (wire.DepositCredit, *creditHold, *account, error) {
	c, ok := l.credits[transferID]
	if !ok {
		return wire.DepositCredit{}, nil, nil, errors.New("funding: record names an unknown credit")
	}
	a := l.accounts[accountKey(c.AuthorID, c.Network)]
	if a == nil {
		return wire.DepositCredit{}, nil, nil, errors.New("funding: credit without an account")
	}
	return c, l.holds[transferID], a, nil
}

func (l *Ledger) stageCreditFreeze(r record, at time.Time) (func(), error) {
	c, hold, a, err := l.heldCredit(r.TransferID)
	if err != nil {
		return nil, err
	}
	if hold != nil {
		return nil, errors.New("funding: credit already frozen or reversed")
	}
	amount, ok := new(big.Int).SetString(c.AmountAccountingUnits, 10)
	if !ok {
		return nil, errors.New("funding: credit amount")
	}
	return func() {
		if a.available.Cmp(amount) < 0 {
			amount.Set(a.available)
		}
		if amount.Sign() > 0 {
			l.touch(c.AuthorID, c.Network, at)
			a.available.Sub(a.available, amount)
			a.frozen.Add(a.frozen, amount)
		}
		l.holds[r.TransferID] = &creditHold{amount: amount, since: at}
	}, nil
}

func (l *Ledger) stageCreditRelease(r record, at time.Time) (func(), error) {
	c, hold, a, err := l.heldCredit(r.TransferID)
	if err != nil {
		return nil, err
	}
	if hold == nil || hold.reversed {
		return nil, errors.New("funding: credit is not frozen")
	}
	return func() {
		if hold.amount.Sign() > 0 {
			l.touch(c.AuthorID, c.Network, at)
			a.frozen.Sub(a.frozen, hold.amount)
			a.available.Add(a.available, hold.amount)
		}
		delete(l.holds, r.TransferID)
	}, nil
}

func (l *Ledger) stageCreditReversal(r record, at time.Time) (func(), error) {
	c, hold, a, err := l.heldCredit(r.TransferID)
	if err != nil {
		return nil, err
	}
	if hold == nil || hold.reversed {
		return nil, errors.New("funding: credit is not frozen")
	}
	return func() {
		if hold.amount.Sign() > 0 {
			l.touch(c.AuthorID, c.Network, at)
			a.frozen.Sub(a.frozen, hold.amount)
			a.note(HistoryDepositReversal, hold.amount, c.CreditID, at)
		}
		hold.reversed, hold.since = true, at
	}, nil
}

func (l *Ledger) stageCreditRestore(r record, at time.Time) (func(), error) {
	c, hold, a, err := l.heldCredit(r.TransferID)
	if err != nil {
		return nil, err
	}
	if hold == nil || !hold.reversed {
		return nil, errors.New("funding: credit is not reversed")
	}
	return func() {
		if hold.amount.Sign() > 0 {
			l.touch(c.AuthorID, c.Network, at)
			a.available.Add(a.available, hold.amount)
			a.note(HistoryDepositCredit, hold.amount, c.CreditID, at)
		}
		delete(l.holds, r.TransferID)
	}, nil
}
