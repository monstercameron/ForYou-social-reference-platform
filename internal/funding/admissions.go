package funding

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"time"

	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/wire"
)

// preparationWindow is how long the admitting provider holds an unpublished prepared
// intent after the receipt issued_at; the deadline is exclusive.
const preparationWindow = 30 * 24 * time.Hour

// AdmissionDraft is a verified admission request awaiting its atomic debit.
type AdmissionDraft struct {
	Request   wire.AdmissionRequest
	AuthorID  string
	MessageID string
	Quote     wire.FeeQuote
	// At is the authorization time chosen by the delegation gate; zero means the ledger's now.
	At time.Time
}

// ReceiptEntry is a committed receipt with its per-network commit sequence.
type ReceiptEntry struct {
	Sequence uint64
	Receipt  wire.SubmissionReceipt
}

// ProviderFailure is a provider-caused finalization failure whose fee is not yet restored.
type ProviderFailure struct {
	AuthorID, MessageID, ReceiptID string
	Issued                         time.Time
}

type admissionState struct {
	encoded        []byte
	network        string
	amount         *big.Int
	issued         time.Time
	authorID       string
	messageID      string
	receiptID      string
	restored       bool
	providerFailed bool
}

func (s *admissionState) record() (wire.PreparedAdmissionRecord, error) {
	var r wire.PreparedAdmissionRecord
	err := json.Unmarshal(s.encoded, &r)
	return r, err
}

// Admission returns the committed prepared admission of an author's message.
func (l *Ledger) Admission(authorID, messageID string) (wire.PreparedAdmissionRecord, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	st := l.admissions[recordKey(authorID, messageID)]
	if st == nil {
		return wire.PreparedAdmissionRecord{}, false
	}
	r, err := st.record()
	return r, err == nil
}

// Admit commits quote use, the caller's checks, the balance check, the debit and
// the sealed receipt as one journaled record. A committed admission for the same
// author and message is returned first, with created false, without calling check
// or seal and without any quote, time, balance or capacity rule. check runs inside
// the ledger lock at the authorization time; an error from check or seal is
// returned unchanged and nothing is debited.
func (l *Ledger) Admit(ctx context.Context, d AdmissionDraft, check func(at time.Time) error, seal func(wire.SubmissionReceipt) (wire.SubmissionReceipt, error)) (wire.PreparedAdmissionRecord, bool, error) {
	if err := ctx.Err(); err != nil {
		return wire.PreparedAdmissionRecord{}, false, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if st := l.admissions[recordKey(d.AuthorID, d.MessageID)]; st != nil {
		r, err := st.record()
		return r, false, err
	}
	now, err := l.mutationTime()
	if err != nil {
		return wire.PreparedAdmissionRecord{}, false, err
	}
	q := d.Quote
	if q.ProviderID != l.cfg.ProviderID || q.AuthorID != d.AuthorID || q.MessageID != d.MessageID || l.quotes[q.QuoteID] {
		return wire.PreparedAdmissionRecord{}, false, ErrQuoteConflict
	}
	if d.AuthorID == "" || d.MessageID == "" || q.QuoteID == "" || check == nil || seal == nil {
		return wire.PreparedAdmissionRecord{}, false, ErrInvalidRequest
	}
	if _, err := fee.Denomination(q.Network); err != nil {
		return wire.PreparedAdmissionRecord{}, false, fmt.Errorf("%w: network", ErrInvalidRequest)
	}
	amount, err := parseAmount(q.AmountBaseUnits)
	if err != nil {
		return wire.PreparedAdmissionRecord{}, false, err
	}
	at := now
	if !d.At.IsZero() {
		at = d.At.UTC().Truncate(time.Second)
		if at.Before(now.Add(-maxClockSkew)) || at.After(now.Add(maxClockSkew)) {
			return wire.PreparedAdmissionRecord{}, false, fmt.Errorf("%w: authorization time more than 60 seconds from now", ErrUnavailable)
		}
	}
	if err := check(at); err != nil {
		return wire.PreparedAdmissionRecord{}, false, err
	}
	if l.available(d.AuthorID, q.Network).Cmp(amount) < 0 {
		return wire.PreparedAdmissionRecord{}, false, ErrInsufficientBalance
	}
	receiptID, err := fee.NewReceiptID(l.cfg.Rand)
	if err != nil {
		return wire.PreparedAdmissionRecord{}, false, err
	}
	draft := wire.SubmissionReceipt{
		ProviderID:      l.cfg.ProviderID,
		AuthorID:        d.AuthorID,
		MessageID:       d.MessageID,
		ReceiptID:       receiptID,
		QuoteID:         q.QuoteID,
		ScheduleID:      q.ScheduleID,
		Network:         q.Network,
		AmountBaseUnits: q.AmountBaseUnits,
		AuthorizationAt: wire.FormatTime(at),
		IssuedAt:        wire.FormatTime(at),
	}
	sealed, err := seal(draft)
	if err != nil {
		return wire.PreparedAdmissionRecord{}, false, err
	}
	unsigned := sealed
	unsigned.ProviderSignature = ""
	if unsigned != draft || sealed.ProviderSignature == "" {
		return wire.PreparedAdmissionRecord{}, false, fmt.Errorf("%w: seal changed the receipt", ErrInvalidRequest)
	}
	committed := wire.PreparedAdmissionRecord{Request: d.Request, Receipt: sealed}
	if err := l.commit(record{Kind: kindAdmission, At: draft.IssuedAt, Admission: &committed}); err != nil {
		return wire.PreparedAdmissionRecord{}, false, err
	}
	r, err := l.admissions[recordKey(d.AuthorID, d.MessageID)].record()
	return r, true, err
}

func (l *Ledger) stageAdmission(r record, at time.Time) (func(), error) {
	if r.Admission == nil {
		return nil, errors.New("funding: admission record incomplete")
	}
	rc := r.Admission.Receipt
	key := recordKey(rc.AuthorID, rc.MessageID)
	switch {
	case l.admissions[key] != nil:
		return nil, errors.New("funding: duplicate admission")
	case rc.ProviderID != l.cfg.ProviderID || rc.AuthorID == "" || rc.MessageID == "":
		return nil, errors.New("funding: admission names another provider or no message")
	case rc.QuoteID == "" || l.quotes[rc.QuoteID]:
		return nil, errors.New("funding: quote missing or already used")
	case rc.ReceiptID == "" || rc.ProviderSignature == "" || l.receiptIDs[rc.ReceiptID]:
		return nil, errors.New("funding: receipt ID missing or reused")
	case rc.IssuedAt != r.At || rc.AuthorizationAt != r.At:
		return nil, errors.New("funding: receipt time does not match its record")
	}
	if _, err := fee.Denomination(rc.Network); err != nil {
		return nil, err
	}
	amount, err := parseAmount(rc.AmountBaseUnits)
	if err != nil {
		return nil, err
	}
	if l.available(rc.AuthorID, rc.Network).Cmp(amount) < 0 {
		return nil, errors.New("funding: admission debit exceeds available balance")
	}
	encoded, err := json.Marshal(r.Admission)
	if err != nil {
		return nil, err
	}
	return func() {
		sequence := l.admissionSequences[rc.Network] + 1
		l.admissionSequences[rc.Network] = sequence
		l.admissions[key] = &admissionState{encoded: encoded, network: rc.Network, amount: amount, issued: at, authorID: rc.AuthorID, messageID: rc.MessageID, receiptID: rc.ReceiptID}
		l.quotes[rc.QuoteID] = true
		l.receiptIDs[rc.ReceiptID] = true
		l.receipts[rc.Network] = append(l.receipts[rc.Network], ReceiptEntry{Sequence: sequence, Receipt: rc})
		a := l.touch(rc.AuthorID, rc.Network, at)
		a.available.Sub(a.available, amount)
	}, nil
}

// Receipts returns up to limit committed receipts of a network with a sequence
// greater than after, in commit order.
func (l *Ledger) Receipts(network string, after uint64, limit int) []ReceiptEntry {
	l.mu.RLock()
	defer l.mu.RUnlock()
	list := l.receipts[network]
	if limit < 1 || after >= uint64(len(list)) {
		return nil
	}
	end := uint64(len(list))
	if after+uint64(limit) < end {
		end = after + uint64(limit)
	}
	return append([]ReceiptEntry(nil), list[after:end]...)
}

// Restored reports whether an admission's fee was restored.
func (l *Ledger) Restored(authorID, messageID string) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	st := l.admissions[recordKey(authorID, messageID)]
	return st != nil && st.restored
}

// ProviderFailed reports whether the provider itself caused an admission's finalization
// to fail. Its receipt is left out of settlement claims from that moment on.
func (l *Ledger) ProviderFailed(authorID, messageID string) bool {
	l.mu.RLock()
	defer l.mu.RUnlock()
	st := l.admissions[recordKey(authorID, messageID)]
	return st != nil && st.providerFailed
}

// MarkProviderFailed durably records a provider-caused finalization failure of an
// admission before the provider replies. A repeat is a no-op; an admission whose fee
// is already restored cannot be marked.
func (l *Ledger) MarkProviderFailed(authorID, messageID string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.admissions[recordKey(authorID, messageID)]
	switch {
	case st == nil:
		return ErrNotFound
	case st.restored:
		return fmt.Errorf("%w: fee already restored", ErrConflict)
	case st.providerFailed:
		return nil
	}
	now, err := l.mutationTime()
	if err != nil {
		return err
	}
	return l.commit(record{Kind: kindProviderFailure, At: wire.FormatTime(now), AuthorID: authorID, MessageID: messageID})
}

func (l *Ledger) stageProviderFailure(r record) (func(), error) {
	st := l.admissions[recordKey(r.AuthorID, r.MessageID)]
	if st == nil || st.restored || st.providerFailed {
		return nil, errors.New("funding: provider failure of a missing, restored or already failed admission")
	}
	return func() { st.providerFailed = true }, nil
}

// ProviderFailures lists provider-caused failures whose fee is not yet restored, oldest first.
func (l *Ledger) ProviderFailures() []ProviderFailure {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var out []ProviderFailure
	for _, st := range l.admissions {
		if st.providerFailed && !st.restored {
			out = append(out, ProviderFailure{AuthorID: st.authorID, MessageID: st.messageID, ReceiptID: st.receiptID, Issued: st.issued})
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].Issued.Equal(out[j].Issued) {
			return out[i].Issued.Before(out[j].Issued)
		}
		return out[i].MessageID < out[j].MessageID
	})
	return out
}

// RestoreFee restores the fee of a provider-caused failed preparation to the same
// balance exactly once. It is allowed only at or after the receipt issued_at plus
// 30 days and only when accepted, called inside the ledger lock so acceptance and
// remedy are serialized, reports that the message was never accepted. A repeat
// returns false with no error and no second credit.
func (l *Ledger) RestoreFee(authorID, messageID string, accepted func() (bool, error)) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := l.admissions[recordKey(authorID, messageID)]
	if st == nil {
		return false, ErrNotFound
	}
	if st.restored {
		return false, nil
	}
	if accepted == nil {
		return false, ErrInvalidRequest
	}
	now, err := l.mutationTime()
	if err != nil {
		return false, err
	}
	if now.Before(st.issued.Add(preparationWindow)) {
		return false, fmt.Errorf("%w: preparation window still open", ErrConflict)
	}
	ok, err := accepted()
	if err != nil {
		return false, err
	}
	if ok {
		return false, fmt.Errorf("%w: message was accepted", ErrConflict)
	}
	if err := l.commit(record{Kind: kindRestore, At: wire.FormatTime(now), AuthorID: authorID, MessageID: messageID}); err != nil {
		return false, err
	}
	return true, nil
}

func (l *Ledger) stageRestore(r record, at time.Time) (func(), error) {
	st := l.admissions[recordKey(r.AuthorID, r.MessageID)]
	if st == nil || st.restored {
		return nil, errors.New("funding: restore of a missing or restored admission")
	}
	if at.Before(st.issued.Add(preparationWindow)) {
		return nil, errors.New("funding: restore before the preparation deadline")
	}
	return func() {
		st.restored = true
		a := l.touch(r.AuthorID, st.network, at)
		a.available.Add(a.available, st.amount)
	}, nil
}
