package funding

import (
	"fmt"
	"math/big"
	"time"

	"foryou.local/foryou/internal/fee"
)

// HistoryKind names what one account history entry did to the balance.
type HistoryKind string

const (
	// HistoryDepositCredit adds a confirmed deposit, a funding claim settled in the author's
	// favor, or a reversed credit whose transfer came back.
	HistoryDepositCredit HistoryKind = "deposit_credit"
	// HistoryDepositReversal subtracts the unspent part of a credit that a chain reorganization removed.
	HistoryDepositReversal HistoryKind = "deposit_reversal"
	// HistoryDebit subtracts the fee of one admission.
	HistoryDebit HistoryKind = "debit"
	// HistoryFeeRestore adds back the fee of an admission the provider failed to finalize.
	HistoryFeeRestore HistoryKind = "fee_restore"
	// HistoryWithdrawal subtracts a requested withdrawal.
	HistoryWithdrawal HistoryKind = "withdrawal"
	// HistoryWithdrawalReturn adds back a withdrawal that failed.
	HistoryWithdrawalReturn HistoryKind = "withdrawal_return"
)

// HistoryEntry is one change to an account, in the order it was committed.
type HistoryEntry struct {
	Sequence  uint64
	At        time.Time
	Kind      HistoryKind
	Amount    *big.Int
	Reference string
}

// maxHistoryPage bounds one page of account history.
const maxHistoryPage = 100

// note appends one history entry. It is called only from applied journal records,
// so replay rebuilds the same gapless sequence that readers were served.
func (a *account) note(kind HistoryKind, amount *big.Int, reference string, at time.Time) {
	a.history = append(a.history, HistoryEntry{Sequence: uint64(len(a.history)) + 1, At: at, Kind: kind, Amount: new(big.Int).Set(amount), Reference: reference})
}

// History returns up to 100 entries of one account with a sequence greater than
// after, in order, and whether later entries exist.
func (l *Ledger) History(authorID, network string, after uint64) ([]HistoryEntry, bool, error) {
	if _, err := fee.Denomination(network); err != nil {
		return nil, false, fmt.Errorf("%w: network", ErrInvalidRequest)
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	a := l.accounts[accountKey(authorID, network)]
	if a == nil || after >= uint64(len(a.history)) {
		return nil, false, nil
	}
	rest := a.history[after:]
	more := len(rest) > maxHistoryPage
	if more {
		rest = rest[:maxHistoryPage]
	}
	page := make([]HistoryEntry, len(rest))
	for i, entry := range rest {
		entry.Amount = new(big.Int).Set(entry.Amount)
		page[i] = entry
	}
	return page, more, nil
}
