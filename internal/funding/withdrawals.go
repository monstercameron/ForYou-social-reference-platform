package funding

import (
	"errors"
	"fmt"
	"time"

	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/wire"
)

// Withdrawal statuses.
const (
	WithdrawalPending = "pending"
	WithdrawalSent    = "sent"
	WithdrawalFailed  = "failed"
)

// Withdrawal is a retained withdrawal request and its status.
type Withdrawal struct {
	Request wire.WithdrawalRequest
	// Status is WithdrawalPending, WithdrawalSent or WithdrawalFailed.
	Status string
	// Transfer is the payout transfer; it is nil until the status is sent.
	Transfer  *wire.DepositTransfer
	UpdatedAt time.Time
}

// Withdrawal returns the retained withdrawal of an author's request ID.
func (l *Ledger) Withdrawal(authorID, requestID string) (Withdrawal, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	w := l.withdrawals[recordKey(authorID, requestID)]
	if w == nil {
		return Withdrawal{}, false
	}
	return cloneWithdrawal(*w), true
}

// RequestWithdrawal returns the retained withdrawal for an identical request, or
// moves the amount from available to reserved and records it as pending.
func (l *Ledger) RequestWithdrawal(req wire.WithdrawalRequest) (Withdrawal, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if w := l.withdrawals[recordKey(req.AuthorID, req.RequestID)]; w != nil {
		if w.Request != req {
			return Withdrawal{}, false, ErrRequestConflict
		}
		return cloneWithdrawal(*w), false, nil
	}
	now, err := l.mutationTime()
	if err != nil {
		return Withdrawal{}, false, err
	}
	if req.ProviderID != l.cfg.ProviderID || req.AuthorID == "" || req.RequestID == "" {
		return Withdrawal{}, false, ErrInvalidRequest
	}
	if err := faux.ValidateRecipient(req.Network, req.Recipient); err != nil {
		return Withdrawal{}, false, fmt.Errorf("%w: recipient", ErrInvalidRequest)
	}
	amount, err := parseAmount(req.AmountBaseUnits)
	if err != nil {
		return Withdrawal{}, false, err
	}
	if l.available(req.AuthorID, req.Network).Cmp(amount) < 0 {
		return Withdrawal{}, false, ErrInsufficientBalance
	}
	if err := l.commit(record{Kind: kindWithdrawal, At: wire.FormatTime(now), Withdrawal: &req}); err != nil {
		return Withdrawal{}, false, err
	}
	return cloneWithdrawal(*l.withdrawals[recordKey(req.AuthorID, req.RequestID)]), true, nil
}

func (l *Ledger) stageWithdrawal(r record, at time.Time) (func(), error) {
	if r.Withdrawal == nil {
		return nil, errors.New("funding: withdrawal record incomplete")
	}
	req := *r.Withdrawal
	key := recordKey(req.AuthorID, req.RequestID)
	if l.withdrawals[key] != nil || req.AuthorID == "" || req.RequestID == "" || req.ProviderID != l.cfg.ProviderID {
		return nil, errors.New("funding: duplicate or invalid withdrawal")
	}
	if _, err := fee.Denomination(req.Network); err != nil {
		return nil, err
	}
	amount, err := parseAmount(req.AmountBaseUnits)
	if err != nil {
		return nil, err
	}
	if l.available(req.AuthorID, req.Network).Cmp(amount) < 0 {
		return nil, errors.New("funding: withdrawal exceeds available balance")
	}
	return func() {
		l.withdrawals[key] = &Withdrawal{Request: req, Status: WithdrawalPending, UpdatedAt: at}
		l.withdrawalOrder = append(l.withdrawalOrder, key)
		a := l.touch(req.AuthorID, req.Network, at)
		a.available.Sub(a.available, amount)
		a.reserved.Add(a.reserved, amount)
	}, nil
}

// MarkWithdrawalSent moves a pending withdrawal to sent with its payout transfer
// and removes the reserved amount. Repeating it with the same transfer returns the
// record; any other transition is ErrConflict.
func (l *Ledger) MarkWithdrawalSent(authorID, requestID string, t wire.DepositTransfer) (Withdrawal, error) {
	t = cloneTransfer(t)
	return l.finishWithdrawal(authorID, requestID, WithdrawalSent, &t)
}

// MarkWithdrawalFailed moves a pending withdrawal to failed and restores the
// reserved amount to available exactly once. Repeating it returns the record.
func (l *Ledger) MarkWithdrawalFailed(authorID, requestID string) (Withdrawal, error) {
	return l.finishWithdrawal(authorID, requestID, WithdrawalFailed, nil)
}

func (l *Ledger) finishWithdrawal(authorID, requestID, status string, t *wire.DepositTransfer) (Withdrawal, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	w := l.withdrawals[recordKey(authorID, requestID)]
	if w == nil {
		return Withdrawal{}, ErrNotFound
	}
	if w.Status == status && sameOptionalTransfer(w.Transfer, t) {
		return cloneWithdrawal(*w), nil
	}
	if w.Status != WithdrawalPending {
		return Withdrawal{}, ErrConflict
	}
	if t != nil && (t.Network != w.Request.Network || transferKey(*t) == "") {
		return Withdrawal{}, ErrInvalidRequest
	}
	now, err := l.mutationTime()
	if err != nil {
		return Withdrawal{}, err
	}
	if err := l.commit(record{Kind: kindWithdrawalStatus, At: wire.FormatTime(now), AuthorID: authorID, RequestID: requestID, Status: status, Transfer: t}); err != nil {
		return Withdrawal{}, err
	}
	return cloneWithdrawal(*w), nil
}

func (l *Ledger) stageWithdrawalStatus(r record, at time.Time) (func(), error) {
	w := l.withdrawals[recordKey(r.AuthorID, r.RequestID)]
	if w == nil || w.Status != WithdrawalPending {
		return nil, errors.New("funding: withdrawal missing or not pending")
	}
	switch {
	case r.Status == WithdrawalSent && r.Transfer != nil && r.Transfer.Network == w.Request.Network && transferKey(*r.Transfer) != "":
	case r.Status == WithdrawalFailed && r.Transfer == nil:
	default:
		return nil, errors.New("funding: invalid withdrawal transition")
	}
	amount, err := parseAmount(w.Request.AmountBaseUnits)
	if err != nil {
		return nil, err
	}
	a := l.accounts[accountKey(w.Request.AuthorID, w.Request.Network)]
	if a == nil || a.reserved.Cmp(amount) < 0 {
		return nil, errors.New("funding: withdrawal exceeds reserved balance")
	}
	return func() {
		w.Status, w.UpdatedAt = r.Status, at
		if r.Transfer != nil {
			t := cloneTransfer(*r.Transfer)
			w.Transfer = &t
		}
		a := l.touch(w.Request.AuthorID, w.Request.Network, at)
		a.reserved.Sub(a.reserved, amount)
		if r.Status == WithdrawalFailed {
			a.available.Add(a.available, amount)
		}
	}, nil
}

// PendingWithdrawals lists the withdrawals still pending, in request order.
func (l *Ledger) PendingWithdrawals() []Withdrawal {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var out []Withdrawal
	for _, key := range l.withdrawalOrder {
		if w := l.withdrawals[key]; w.Status == WithdrawalPending {
			out = append(out, cloneWithdrawal(*w))
		}
	}
	return out
}

func sameOptionalTransfer(a, b *wire.DepositTransfer) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return sameTransfer(*a, *b)
}

func cloneWithdrawal(w Withdrawal) Withdrawal {
	if w.Transfer != nil {
		t := cloneTransfer(*w.Transfer)
		w.Transfer = &t
	}
	return w
}
