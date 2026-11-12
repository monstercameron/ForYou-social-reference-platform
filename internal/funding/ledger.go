package funding

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"time"

	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/wire"
)

var (
	// ErrUnavailable means the ledger cannot safely accept new financial mutations.
	ErrUnavailable = errors.New("funding: unavailable")
	// ErrSaturated means the provider's outstanding invoice limit is reached.
	ErrSaturated = errors.New("funding: saturated")
	// ErrInvoiceLimit means the account already has four outstanding invoices.
	ErrInvoiceLimit = errors.New("funding: invoice limit")
	// ErrCustodyCap means the invoice would raise confirmed plus pending funds above the custody cap.
	ErrCustodyCap = errors.New("funding: custody cap")
	// ErrRequestConflict means a retained request ID names different content.
	ErrRequestConflict = errors.New("funding: request conflict")
	// ErrNotFound means no record is available to the requesting author.
	ErrNotFound = errors.New("funding: not found")
	// ErrInvalidHint means the chain transfer contradicts the supplied hint or invoice.
	ErrInvalidHint = errors.New("funding: invalid hint")
	// ErrClaim means the transfer belongs to the claims process and is never credited automatically.
	ErrClaim = errors.New("funding: claim")
	// ErrInsufficientBalance means available funds do not cover the amount.
	ErrInsufficientBalance = errors.New("funding: insufficient balance")
	// ErrQuoteConflict means the quote is bound to another intent or was used by another admission.
	ErrQuoteConflict = errors.New("funding: quote conflict")
	// ErrConflict means the requested transition conflicts with retained state.
	ErrConflict = errors.New("funding: conflict")
)

const (
	maxInvoiceLifetime     = 24 * time.Hour
	defaultMaxOpenInvoices = 10_000
	// maxClockSkew is the protocol's clock uncertainty bound; beyond it issuance stops.
	maxClockSkew = 60 * time.Second
)

// Journal is the durable, ordered record of committed ledger mutations.
type Journal interface {
	// Append durably stores one record and returns only after it is synced.
	Append(record []byte) error
	// Replay calls apply for every committed record in order and stops at the first error.
	Replay(apply func(record []byte) error) error
}

// Sealer completes provider-signed funding objects. It may not change their financial fields.
type Sealer interface {
	// SealInvoice sets deposit_id and provider_signature.
	SealInvoice(wire.DepositInvoice) (wire.DepositInvoice, error)
	// SealCredit sets credit_id and provider_signature.
	SealCredit(wire.DepositCredit) (wire.DepositCredit, error)
}

// Config configures a provider ledger.
type Config struct {
	// ProviderID is this provider's identity, bound into every invoice, credit and receipt.
	ProviderID string
	// Journal stores committed mutations; nil keeps the ledger in memory only.
	Journal Journal
	// Now is the provider clock; nil uses time.Now.
	Now func() time.Time
	// Rand supplies receipt IDs; nil uses crypto/rand.
	Rand io.Reader
	// RecipientLabel names this provider's faux deposit recipients.
	RecipientLabel string
	// InvoiceLifetime is expires_at minus issued_at; zero means 24 hours, which is also the maximum.
	InvoiceLifetime time.Duration
	// MaxOpenInvoices bounds outstanding invoices across all accounts; zero means 10,000.
	MaxOpenInvoices int
	// ReorgWatch is how long after a credit its transfer is re-checked against the
	// chain; zero means 24 hours.
	ReorgWatch time.Duration
	// ReversalDelay is how long a frozen credit waits for its transfer to be final
	// again before it is reversed; zero means 24 hours.
	ReversalDelay time.Duration
}

// Balance is the provider-local balance of one author on one network, in accounting units.
type Balance struct {
	AuthorID, Network           string
	Available, Frozen, Reserved *big.Int
	Revision                    uint64
	UpdatedAt                   time.Time
}

// Ledger is the provider's single source of financial truth: invoices, credits,
// claims, admission debits and withdrawals, each journaled before memory changes.
type Ledger struct {
	cfg     Config
	journal Journal

	mu        sync.RWMutex
	failed    bool
	highWater time.Time

	accounts map[string]*account

	invoices       map[string]*invoiceState
	invoiceByReq   map[string]string
	outstanding    map[string]bool
	recipients     map[string]bool
	invoiceCounter uint64

	credits        map[string]wire.DepositCredit
	creditIDs      map[string]bool
	creditSequence int64
	holds          map[string]*creditHold
	watched        []string
	claims         []Claim
	claimIndex     map[string]int

	admissions         map[string]*admissionState
	quotes             map[string]bool
	receiptIDs         map[string]bool
	receipts           map[string][]ReceiptEntry
	admissionSequences map[string]uint64

	withdrawals     map[string]*Withdrawal
	withdrawalOrder []string
}

type account struct {
	available, frozen, reserved *big.Int
	revision                    uint64
	updatedAt                   time.Time
	history                     []HistoryEntry
}

// record is one journaled mutation with only the fields of its kind set. Balances
// are never journaled, so replay recomputes them from the mutations themselves.
type record struct {
	Kind             string                        `json:"kind"`
	At               string                        `json:"at"`
	Counter          uint64                        `json:"counter,omitempty"`
	Request          *wire.DepositRequest          `json:"request,omitempty"`
	Invoice          *wire.DepositInvoice          `json:"invoice,omitempty"`
	DepositID        string                        `json:"deposit_id,omitempty"`
	Transfer         *wire.DepositTransfer         `json:"transfer,omitempty"`
	AmountChainUnits string                        `json:"amount_chain_units,omitempty"`
	Reason           string                        `json:"reason,omitempty"`
	Credit           *wire.DepositCredit           `json:"credit,omitempty"`
	TransferID       string                        `json:"transfer_id,omitempty"`
	Disposition      string                        `json:"disposition,omitempty"`
	Admission        *wire.PreparedAdmissionRecord `json:"admission,omitempty"`
	AuthorID         string                        `json:"author_id,omitempty"`
	MessageID        string                        `json:"message_id,omitempty"`
	Withdrawal       *wire.WithdrawalRequest       `json:"withdrawal,omitempty"`
	RequestID        string                        `json:"request_id,omitempty"`
	Status           string                        `json:"status,omitempty"`
}

const (
	kindInvoice          = "invoice"
	kindPending          = "pending"
	kindClaim            = "claim"
	kindCredit           = "credit"
	kindDisposition      = "disposition"
	kindAdmission        = "admission"
	kindRestore          = "restore"
	kindProviderFailure  = "provider_failure"
	kindWithdrawal       = "withdrawal"
	kindWithdrawalStatus = "withdrawal_status"
	kindPendingDropped   = "pending_dropped"
	kindCreditFreeze     = "credit_freeze"
	kindCreditRelease    = "credit_release"
	kindCreditReversal   = "credit_reversal"
	kindCreditRestore    = "credit_restore"
)

const defaultReorgDelay = 24 * time.Hour

// Open creates a ledger and replays its journal. A journal that cannot be read or
// replayed leaves the ledger failed: reads keep working and mutations return ErrUnavailable.
func Open(cfg Config) (*Ledger, error) {
	if cfg.ProviderID == "" {
		return nil, fmt.Errorf("%w: provider ID required", ErrInvalidRequest)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.InvoiceLifetime == 0 {
		cfg.InvoiceLifetime = maxInvoiceLifetime
	}
	if cfg.InvoiceLifetime < time.Second || cfg.InvoiceLifetime > maxInvoiceLifetime {
		return nil, fmt.Errorf("%w: invoice lifetime must be between 1 second and 24 hours", ErrInvalidRequest)
	}
	if cfg.MaxOpenInvoices == 0 {
		cfg.MaxOpenInvoices = defaultMaxOpenInvoices
	}
	if cfg.MaxOpenInvoices < 0 {
		return nil, fmt.Errorf("%w: negative invoice limit", ErrInvalidRequest)
	}
	if cfg.ReorgWatch == 0 {
		cfg.ReorgWatch = defaultReorgDelay
	}
	if cfg.ReversalDelay == 0 {
		cfg.ReversalDelay = defaultReorgDelay
	}
	if cfg.ReorgWatch < 0 || cfg.ReversalDelay < 0 {
		return nil, fmt.Errorf("%w: negative reorganization delay", ErrInvalidRequest)
	}
	l := &Ledger{
		cfg:                cfg,
		journal:            cfg.Journal,
		accounts:           make(map[string]*account),
		invoices:           make(map[string]*invoiceState),
		invoiceByReq:       make(map[string]string),
		outstanding:        make(map[string]bool),
		recipients:         make(map[string]bool),
		credits:            make(map[string]wire.DepositCredit),
		creditIDs:          make(map[string]bool),
		holds:              make(map[string]*creditHold),
		claimIndex:         make(map[string]int),
		admissions:         make(map[string]*admissionState),
		quotes:             make(map[string]bool),
		receiptIDs:         make(map[string]bool),
		receipts:           make(map[string][]ReceiptEntry),
		admissionSequences: make(map[string]uint64),
		withdrawals:        make(map[string]*Withdrawal),
	}
	if l.journal != nil {
		if err := l.journal.Replay(l.replay); err != nil {
			l.failed = true
		}
	}
	return l, nil
}

func (l *Ledger) replay(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	var r record
	if err := dec.Decode(&r); err != nil {
		return err
	}
	if dec.More() {
		return errors.New("funding: trailing data in journal record")
	}
	apply, err := l.stage(r)
	if err != nil {
		return err
	}
	apply()
	return nil
}

// commit journals r and only then applies it, so memory never runs ahead of the journal.
func (l *Ledger) commit(r record) error {
	apply, err := l.stage(r)
	if err != nil {
		return err
	}
	if l.journal != nil {
		b, err := json.Marshal(r)
		if err != nil {
			return err
		}
		if err := l.journal.Append(b); err != nil {
			l.failed = true
			return fmt.Errorf("%w: journal append: %v", ErrUnavailable, err)
		}
	}
	apply()
	return nil
}

// stage validates r against the current state and returns the change it makes.
// Staging changes nothing, so a rejected record leaves the ledger untouched.
func (l *Ledger) stage(r record) (func(), error) {
	at, err := wire.ParseTime(r.At)
	if err != nil {
		return nil, fmt.Errorf("funding: record time: %w", err)
	}
	var apply func()
	switch r.Kind {
	case kindInvoice:
		apply, err = l.stageInvoice(r)
	case kindPending:
		apply, err = l.stagePending(r)
	case kindClaim:
		apply, err = l.stageClaim(r, at)
	case kindCredit:
		apply, err = l.stageCredit(r, at)
	case kindDisposition:
		apply, err = l.stageDisposition(r, at)
	case kindAdmission:
		apply, err = l.stageAdmission(r, at)
	case kindRestore:
		apply, err = l.stageRestore(r, at)
	case kindProviderFailure:
		apply, err = l.stageProviderFailure(r)
	case kindWithdrawal:
		apply, err = l.stageWithdrawal(r, at)
	case kindWithdrawalStatus:
		apply, err = l.stageWithdrawalStatus(r, at)
	case kindPendingDropped:
		apply, err = l.stagePendingDropped(r)
	case kindCreditFreeze:
		apply, err = l.stageCreditFreeze(r, at)
	case kindCreditRelease:
		apply, err = l.stageCreditRelease(r, at)
	case kindCreditReversal:
		apply, err = l.stageCreditReversal(r, at)
	case kindCreditRestore:
		apply, err = l.stageCreditRestore(r, at)
	default:
		err = fmt.Errorf("funding: unknown record kind %q", r.Kind)
	}
	if err != nil {
		return nil, err
	}
	return func() {
		apply()
		if at.After(l.highWater) {
			l.highWater = at
		}
	}, nil
}

// mutationTime applies the fail-closed checks shared by every new mutation and
// returns the commit time: the later of the clock and the greatest committed time,
// so a clock rollback never revives an expired invoice or request.
func (l *Ledger) mutationTime() (time.Time, error) {
	if l.failed {
		return time.Time{}, ErrUnavailable
	}
	now := l.clock()
	if now.Before(l.highWater.Add(-maxClockSkew)) {
		return time.Time{}, fmt.Errorf("%w: clock is more than 60 seconds behind committed time", ErrUnavailable)
	}
	if now.Before(l.highWater) {
		now = l.highWater
	}
	return now, nil
}

func (l *Ledger) clock() time.Time {
	return l.cfg.Now().UTC().Truncate(time.Second)
}

func (l *Ledger) available(authorID, network string) *big.Int {
	if a := l.accounts[accountKey(authorID, network)]; a != nil {
		return a.available
	}
	return new(big.Int)
}

// touch returns the account for an applied change, creating it if needed, and
// records the change in its revision and update time.
func (l *Ledger) touch(authorID, network string, at time.Time) *account {
	key := accountKey(authorID, network)
	a := l.accounts[key]
	if a == nil {
		a = &account{available: new(big.Int), frozen: new(big.Int), reserved: new(big.Int)}
		l.accounts[key] = a
	}
	a.revision++
	a.updatedAt = at
	return a
}

func accountKey(authorID, network string) string { return authorID + "\n" + network }

func recordKey(authorID, id string) string { return authorID + "\n" + id }

func parseAmount(s string) (*big.Int, error) {
	n, err := fee.ParsePositiveAmount(s)
	if err != nil {
		return nil, fmt.Errorf("%w: amount", ErrInvalidRequest)
	}
	return n, nil
}

// Balance returns the account's disjoint categories. An untouched account reports
// zero amounts, revision 0 and the request time.
func (l *Ledger) Balance(authorID, network string) (Balance, error) {
	if _, err := fee.Denomination(network); err != nil {
		return Balance{}, fmt.Errorf("%w: network", ErrInvalidRequest)
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	b := Balance{AuthorID: authorID, Network: network, Available: new(big.Int), Frozen: new(big.Int), Reserved: new(big.Int), UpdatedAt: l.clock()}
	if a := l.accounts[accountKey(authorID, network)]; a != nil {
		b.Available.Set(a.available)
		b.Frozen.Set(a.frozen)
		b.Reserved.Set(a.reserved)
		b.Revision = a.revision
		b.UpdatedAt = a.updatedAt
	}
	return b, nil
}

// NewFileJournal returns a JSON-lines journal at path, creating its directory.
func NewFileJournal(path string) (Journal, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	return fileJournal{path: path}, nil
}

type fileJournal struct{ path string }

func (j fileJournal) Append(b []byte) error {
	f, err := os.OpenFile(j.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(append(append(make([]byte, 0, len(b)+1), b...), '\n'))
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// Replay applies every record in order. A final record that is not complete JSON
// was torn by a crash during Append and never committed, so it is truncated away.
// A damaged record before the end is an error and the file is left unchanged.
func (j fileJournal) Replay(apply func([]byte) error) error {
	data, err := os.ReadFile(j.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for start := 0; start < len(data); {
		end := bytes.IndexByte(data[start:], '\n')
		if end < 0 {
			line := data[start:]
			if !json.Valid(line) {
				return rewriteJournal(j.path, int64(start), nil)
			}
			if err := apply(line); err != nil {
				return err
			}
			return rewriteJournal(j.path, int64(len(data)), []byte{'\n'})
		}
		if err := apply(data[start : start+end]); err != nil {
			return err
		}
		start += end + 1
	}
	return nil
}

// rewriteJournal truncates the journal to size and appends tail, then syncs it.
func rewriteJournal(path string, size int64, tail []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	err = f.Truncate(size)
	if err == nil && len(tail) > 0 {
		_, err = f.WriteAt(tail, size)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}
