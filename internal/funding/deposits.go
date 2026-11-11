package funding

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"time"

	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/wire"
)

// maxOutstandingPerAccount is the protocol's limit of unexpired, unpaid invoices
// per author and network, counting invoices with a pending observed transfer.
const maxOutstandingPerAccount = 4

// Outcome reports what an observation committed.
type Outcome int

const (
	// Pending means the transfer is unknown or not yet final; nothing is spendable.
	Pending Outcome = iota
	// Created means the observation committed a new credit.
	Created
	// Existing means the transfer already produced this invoice's credit, which is returned unchanged.
	Existing
)

// Claim reasons recorded when a final transfer cannot be credited automatically.
const (
	ClaimUnderpaid              = "underpaid"
	ClaimOverpaid               = "overpaid"
	ClaimLate                   = "late"
	ClaimInvoiceAlreadyCredited = "invoice_already_credited"
	ClaimNotSuccessful          = "not_successful"
)

// Claim dispositions; each claim receives at most one.
const (
	// DispositionCredited adds the claimed amount to the author's available balance.
	DispositionCredited = "credited"
	// DispositionReturned records that the funds were returned to the payer.
	DispositionReturned = "returned"
)

// Claim is a transfer routed to the free claims process instead of a credit. It is
// never forfeited and receives at most one disposition.
type Claim struct {
	TransferID            string
	Transfer              wire.DepositTransfer
	DepositID             string
	AuthorID              string
	Network               string
	AmountChainUnits      string
	AmountAccountingUnits string
	Reason                string
	RecordedAt            time.Time
	Disposition           string
	DisposedAt            time.Time
}

type invoiceState struct {
	request  wire.DepositRequest
	invoice  wire.DepositInvoice
	amount   *big.Int
	expires  time.Time
	credited bool
	pending  *pendingObservation
}

// pendingObservation is the one observed but not yet final transfer retained per invoice.
type pendingObservation struct {
	transferID string
	amount     *big.Int
}

func (s *invoiceState) open(now time.Time) bool {
	return !s.credited && now.Before(s.expires)
}

// counted reports whether the invoice still counts toward the invoice and custody limits.
func (s *invoiceState) counted(now time.Time) bool {
	return s.open(now) || s.pending != nil
}

// exposure is the accounting amount the invoice may still add to the balance.
func (s *invoiceState) exposure(now time.Time) *big.Int {
	n := new(big.Int)
	if s.open(now) {
		n.Set(s.amount)
	}
	if s.pending != nil && s.pending.amount.Cmp(n) > 0 {
		n.Set(s.pending.amount)
	}
	return n
}

// InvoiceForRequest returns the retained request and invoice for an author's request ID.
func (l *Ledger) InvoiceForRequest(authorID, requestID string) (wire.DepositRequest, wire.DepositInvoice, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	id, ok := l.invoiceByReq[recordKey(authorID, requestID)]
	if !ok {
		return wire.DepositRequest{}, wire.DepositInvoice{}, false
	}
	st := l.invoices[id]
	return st.request, st.invoice, true
}

// IssueInvoice returns the retained invoice for an identical request, or issues a
// new one within the outstanding-invoice limits and the custody cap. cap is the
// custody cap in accounting units of the schedule in force.
func (l *Ledger) IssueInvoice(req wire.DepositRequest, cap *big.Int, s Sealer) (inv wire.DepositInvoice, created bool, err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if id, ok := l.invoiceByReq[recordKey(req.AuthorID, req.RequestID)]; ok {
		st := l.invoices[id]
		if st.request != req {
			return wire.DepositInvoice{}, false, ErrRequestConflict
		}
		return st.invoice, false, nil
	}
	now, err := l.mutationTime()
	if err != nil {
		return wire.DepositInvoice{}, false, err
	}
	if req.ProviderID != l.cfg.ProviderID || req.AuthorID == "" || req.RequestID == "" || cap == nil || cap.Sign() < 0 || s == nil {
		return wire.DepositInvoice{}, false, ErrInvalidRequest
	}
	chainAmount, err := parseAmount(req.AmountChainUnits)
	if err != nil {
		return wire.DepositInvoice{}, false, err
	}
	amount, err := fee.AccountingUnits(req.Network, chainAmount)
	if err != nil {
		return wire.DepositInvoice{}, false, fmt.Errorf("%w: network", ErrInvalidRequest)
	}
	global, outstanding, pending := l.outstandingFor(req.AuthorID, req.Network, now)
	if global >= l.cfg.MaxOpenInvoices {
		return wire.DepositInvoice{}, false, ErrSaturated
	}
	if outstanding >= maxOutstandingPerAccount {
		return wire.DepositInvoice{}, false, ErrInvoiceLimit
	}
	total := new(big.Int).Add(pending, amount)
	if a := l.accounts[accountKey(req.AuthorID, req.Network)]; a != nil {
		total.Add(total, a.available).Add(total, a.frozen).Add(total, a.reserved)
	}
	if total.Cmp(cap) > 0 {
		return wire.DepositInvoice{}, false, ErrCustodyCap
	}
	counter := l.invoiceCounter + 1
	draft := wire.DepositInvoice{
		ProviderID:       l.cfg.ProviderID,
		AuthorID:         req.AuthorID,
		Network:          req.Network,
		AmountChainUnits: req.AmountChainUnits,
		Recipient:        faux.DevAddress(req.Network, l.cfg.RecipientLabel+"/deposit/"+strconv.FormatUint(counter, 10)),
		IssuedAt:         wire.FormatTime(now),
		ExpiresAt:        wire.FormatTime(now.Add(l.cfg.InvoiceLifetime)),
	}
	sealed, err := s.SealInvoice(draft)
	if err != nil {
		return wire.DepositInvoice{}, false, err
	}
	unsigned := sealed
	unsigned.DepositID, unsigned.ProviderSignature = "", ""
	if unsigned != draft {
		return wire.DepositInvoice{}, false, fmt.Errorf("%w: sealer changed the invoice", ErrInvalidRequest)
	}
	if err := l.commit(record{Kind: kindInvoice, At: wire.FormatTime(now), Counter: counter, Request: &req, Invoice: &sealed}); err != nil {
		return wire.DepositInvoice{}, false, err
	}
	return sealed, true, nil
}

// outstandingFor counts outstanding invoices globally and for one account, with
// the account's pending exposure, and forgets invoices that no longer count.
func (l *Ledger) outstandingFor(authorID, network string, now time.Time) (global, outstanding int, pending *big.Int) {
	pending = new(big.Int)
	for id := range l.outstanding {
		st := l.invoices[id]
		if !st.counted(now) {
			delete(l.outstanding, id)
			continue
		}
		global++
		if st.invoice.AuthorID == authorID && st.invoice.Network == network {
			outstanding++
			pending.Add(pending, st.exposure(now))
		}
	}
	return global, outstanding, pending
}

func (l *Ledger) stageInvoice(r record) (func(), error) {
	if r.Request == nil || r.Invoice == nil {
		return nil, errors.New("funding: invoice record incomplete")
	}
	req, inv := *r.Request, *r.Invoice
	key := recordKey(req.AuthorID, req.RequestID)
	if _, ok := l.invoiceByReq[key]; ok {
		return nil, errors.New("funding: duplicate deposit request")
	}
	if inv.DepositID == "" || inv.ProviderSignature == "" || l.invoices[inv.DepositID] != nil {
		return nil, errors.New("funding: invalid or duplicate deposit ID")
	}
	if r.Counter != l.invoiceCounter+1 {
		return nil, errors.New("funding: recipient counter out of order")
	}
	if inv.Recipient == "" || l.recipients[inv.Recipient] {
		return nil, errors.New("funding: recipient reused")
	}
	if inv.ProviderID != l.cfg.ProviderID || req.ProviderID != inv.ProviderID || req.AuthorID != inv.AuthorID || req.Network != inv.Network || req.AmountChainUnits != inv.AmountChainUnits || req.RequestID == "" {
		return nil, errors.New("funding: invoice does not match its request")
	}
	chainAmount, err := parseAmount(inv.AmountChainUnits)
	if err != nil {
		return nil, err
	}
	amount, err := fee.AccountingUnits(inv.Network, chainAmount)
	if err != nil {
		return nil, err
	}
	issued, err := wire.ParseTime(inv.IssuedAt)
	if err != nil {
		return nil, err
	}
	expires, err := wire.ParseTime(inv.ExpiresAt)
	if err != nil {
		return nil, err
	}
	if !expires.After(issued) || expires.Sub(issued) > maxInvoiceLifetime {
		return nil, errors.New("funding: invoice lifetime out of range")
	}
	return func() {
		l.invoices[inv.DepositID] = &invoiceState{request: req, invoice: inv, amount: amount, expires: expires}
		l.invoiceByReq[key] = inv.DepositID
		l.outstanding[inv.DepositID] = true
		l.recipients[inv.Recipient] = true
		l.invoiceCounter = r.Counter
	}, nil
}

// Observe verifies a deposit hint against the chain and commits at most one
// disposition for the transfer: a pending observation, a claim or a credit.
func (l *Ledger) Observe(ctx context.Context, p wire.DepositProof, chain faux.Chain, s Sealer) (wire.DepositCredit, Outcome, error) {
	if chain == nil || s == nil {
		return wire.DepositCredit{}, Pending, ErrInvalidRequest
	}
	p.Transfer = cloneTransfer(p.Transfer)
	id, err := l.observeTarget(p)
	if err != nil {
		return wire.DepositCredit{}, Pending, err
	}
	if credit, out, done, err := l.fenced(id, p.DepositID); done {
		return credit, out, err
	}
	t, err := chain.Transfer(ctx, p.Transfer.Network, lookupID(p.Transfer))
	switch {
	case errors.Is(err, faux.ErrNotFound):
		return wire.DepositCredit{}, Pending, nil
	case errors.Is(err, faux.ErrInvalid):
		return wire.DepositCredit{}, Pending, ErrInvalidHint
	case err != nil:
		return wire.DepositCredit{}, Pending, err
	}
	if t.Network != p.Transfer.Network || t.TxID != p.Transfer.TxID || t.TxHash != p.Transfer.TxHash {
		return wire.DepositCredit{}, Pending, ErrInvalidHint
	}
	if p.Transfer.OutputIndex != nil && (t.OutputIndex == nil || int64(*t.OutputIndex) != *p.Transfer.OutputIndex) {
		return wire.DepositCredit{}, Pending, nil
	}
	chainAmount, err := fee.ParsePositiveAmount(t.AmountChainUnits)
	if err != nil {
		return wire.DepositCredit{}, Pending, fmt.Errorf("%w: chain amount", ErrInvalidHint)
	}
	blockTime, err := wire.ParseTime(t.BlockTime)
	if err != nil {
		return wire.DepositCredit{}, Pending, fmt.Errorf("%w: block time", ErrInvalidHint)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if credit, out, done, err := l.consumed(id, p.DepositID); done {
		return credit, out, err
	}
	st := l.invoices[p.DepositID]
	if !paysRecipient(t, st.invoice.Recipient) {
		return wire.DepositCredit{}, Pending, ErrInvalidHint
	}
	now, err := l.mutationTime()
	if err != nil {
		return wire.DepositCredit{}, Pending, err
	}
	claim := record{Kind: kindClaim, At: wire.FormatTime(now), DepositID: p.DepositID, Transfer: &p.Transfer, AmountChainUnits: t.AmountChainUnits}
	expected, _ := new(big.Int).SetString(st.invoice.AmountChainUnits, 10)
	switch {
	case !t.Success || t.Coinbase:
		claim.Reason = ClaimNotSuccessful
	case !final(t):
		if st.pending != nil {
			return wire.DepositCredit{}, Pending, nil
		}
		err := l.commit(record{Kind: kindPending, At: wire.FormatTime(now), DepositID: p.DepositID, Transfer: &p.Transfer, AmountChainUnits: t.AmountChainUnits})
		return wire.DepositCredit{}, Pending, err
	case chainAmount.Cmp(expected) < 0:
		claim.Reason = ClaimUnderpaid
	case chainAmount.Cmp(expected) > 0:
		claim.Reason = ClaimOverpaid
	case blockTime.After(st.expires):
		claim.Reason = ClaimLate
	case st.credited:
		claim.Reason = ClaimInvoiceAlreadyCredited
	}
	if claim.Reason != "" {
		if err := l.commit(claim); err != nil {
			return wire.DepositCredit{}, Pending, err
		}
		return wire.DepositCredit{}, Pending, ErrClaim
	}
	draft := wire.DepositCredit{
		ProviderID:            l.cfg.ProviderID,
		AuthorID:              st.invoice.AuthorID,
		DepositID:             p.DepositID,
		Network:               st.invoice.Network,
		Transfer:              cloneTransfer(p.Transfer),
		AmountChainUnits:      st.invoice.AmountChainUnits,
		AmountAccountingUnits: st.amount.String(),
		CreditSequence:        l.creditSequence + 1,
		CreditedAt:            wire.FormatTime(now),
	}
	sealed, err := s.SealCredit(cloneCredit(draft))
	if err != nil {
		return wire.DepositCredit{}, Pending, err
	}
	sealed = cloneCredit(sealed)
	if sealed.CreditID == "" || sealed.ProviderSignature == "" || !sameCreditFields(sealed, draft) {
		return wire.DepositCredit{}, Pending, fmt.Errorf("%w: sealer changed the credit", ErrInvalidRequest)
	}
	if err := l.commit(record{Kind: kindCredit, At: wire.FormatTime(now), Credit: &sealed}); err != nil {
		return wire.DepositCredit{}, Pending, err
	}
	return cloneCredit(sealed), Created, nil
}

// observeTarget checks the proof against the retained invoice, nonrevealingly for
// another author's invoice, and returns the transfer identity.
func (l *Ledger) observeTarget(p wire.DepositProof) (string, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	st := l.invoices[p.DepositID]
	if st == nil || p.ProviderID != l.cfg.ProviderID || st.invoice.AuthorID != p.AuthorID {
		return "", ErrNotFound
	}
	if p.Transfer.Network != st.invoice.Network {
		return "", ErrInvalidHint
	}
	id := transferKey(p.Transfer)
	if id == "" {
		return "", ErrInvalidHint
	}
	return id, nil
}

// fenced is consumed under the read lock, so retries are answered without a chain lookup.
func (l *Ledger) fenced(transferID, depositID string) (wire.DepositCredit, Outcome, bool, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.consumed(transferID, depositID)
}

// consumed applies the provider-global transfer fence: a transfer that produced
// this invoice's credit returns it; one credited elsewhere or claimed is a claim.
func (l *Ledger) consumed(transferID, depositID string) (wire.DepositCredit, Outcome, bool, error) {
	if c, ok := l.credits[transferID]; ok {
		if c.DepositID == depositID {
			return cloneCredit(c), Existing, true, nil
		}
		return wire.DepositCredit{}, Pending, true, ErrClaim
	}
	if _, ok := l.claimIndex[transferID]; ok {
		return wire.DepositCredit{}, Pending, true, ErrClaim
	}
	return wire.DepositCredit{}, Pending, false, nil
}

func final(t faux.Transfer) bool {
	if t.Network == faux.BitcoinMainnet {
		return t.Confirmations >= faux.BitcoinConfirmations
	}
	return t.Finalized
}

// paysRecipient compares the actual Bitcoin output script, or the lowercase Ethereum address.
func paysRecipient(t faux.Transfer, recipient string) bool {
	if t.Network != faux.BitcoinMainnet {
		return t.Recipient == recipient
	}
	want, err := faux.BitcoinScript(recipient)
	if err != nil {
		return false
	}
	got, err := hex.DecodeString(t.Script)
	return err == nil && bytes.Equal(got, want)
}

// stagedTransfer validates a journaled transfer for an invoice and returns its identity.
func (l *Ledger) stagedTransfer(depositID string, t *wire.DepositTransfer, amountChainUnits string) (*invoiceState, string, *big.Int, error) {
	st := l.invoices[depositID]
	if st == nil || t == nil || t.Network != st.invoice.Network {
		return nil, "", nil, errors.New("funding: transfer record does not match an invoice")
	}
	id := transferKey(*t)
	if id == "" {
		return nil, "", nil, errors.New("funding: transfer record without identity")
	}
	if _, _, done, _ := l.consumed(id, ""); done {
		return nil, "", nil, errors.New("funding: transfer already consumed")
	}
	chainAmount, err := parseAmount(amountChainUnits)
	if err != nil {
		return nil, "", nil, err
	}
	amount, err := fee.AccountingUnits(t.Network, chainAmount)
	if err != nil {
		return nil, "", nil, err
	}
	return st, id, amount, nil
}

func (l *Ledger) stagePending(r record) (func(), error) {
	st, id, amount, err := l.stagedTransfer(r.DepositID, r.Transfer, r.AmountChainUnits)
	if err != nil {
		return nil, err
	}
	if st.pending != nil {
		return nil, errors.New("funding: invoice already has a pending observation")
	}
	return func() {
		st.pending = &pendingObservation{transferID: id, amount: amount}
		l.outstanding[r.DepositID] = true
	}, nil
}

func (l *Ledger) stageClaim(r record, at time.Time) (func(), error) {
	st, id, amount, err := l.stagedTransfer(r.DepositID, r.Transfer, r.AmountChainUnits)
	if err != nil {
		return nil, err
	}
	switch r.Reason {
	case ClaimUnderpaid, ClaimOverpaid, ClaimLate, ClaimInvoiceAlreadyCredited, ClaimNotSuccessful:
	default:
		return nil, fmt.Errorf("funding: unknown claim reason %q", r.Reason)
	}
	c := Claim{
		TransferID:            id,
		Transfer:              cloneTransfer(*r.Transfer),
		DepositID:             r.DepositID,
		AuthorID:              st.invoice.AuthorID,
		Network:               st.invoice.Network,
		AmountChainUnits:      r.AmountChainUnits,
		AmountAccountingUnits: amount.String(),
		Reason:                r.Reason,
		RecordedAt:            at,
	}
	return func() {
		l.claimIndex[id] = len(l.claims)
		l.claims = append(l.claims, c)
		if st.pending != nil && st.pending.transferID == id {
			st.pending = nil
		}
	}, nil
}

func (l *Ledger) stageCredit(r record, at time.Time) (func(), error) {
	if r.Credit == nil {
		return nil, errors.New("funding: credit record incomplete")
	}
	c := cloneCredit(*r.Credit)
	st, id, amount, err := l.stagedTransfer(c.DepositID, &c.Transfer, c.AmountChainUnits)
	if err != nil {
		return nil, err
	}
	switch {
	case st.credited:
		return nil, errors.New("funding: invoice already credited")
	case c.ProviderID != l.cfg.ProviderID || c.AuthorID != st.invoice.AuthorID || c.Network != st.invoice.Network:
		return nil, errors.New("funding: credit does not match its invoice")
	case c.AmountChainUnits != st.invoice.AmountChainUnits || c.AmountAccountingUnits != amount.String():
		return nil, errors.New("funding: credit amount does not match its invoice")
	case c.CreditSequence != l.creditSequence+1:
		return nil, errors.New("funding: credit sequence out of order")
	case c.CreditID == "" || c.ProviderSignature == "" || l.creditIDs[c.CreditID]:
		return nil, errors.New("funding: invalid or duplicate credit ID")
	case c.CreditedAt != r.At:
		return nil, errors.New("funding: credit time does not match its record")
	}
	return func() {
		l.credits[id] = c
		l.creditIDs[c.CreditID] = true
		l.creditSequence = c.CreditSequence
		st.credited = true
		if st.pending != nil && st.pending.transferID == id {
			st.pending = nil
		}
		a := l.touch(c.AuthorID, c.Network, at)
		a.available.Add(a.available, amount)
	}, nil
}

// Claims lists the claim records of one author in the order they were recorded.
func (l *Ledger) Claims(authorID string) []Claim {
	l.mu.RLock()
	defer l.mu.RUnlock()
	var out []Claim
	for _, c := range l.claims {
		if c.AuthorID == authorID {
			out = append(out, cloneClaim(c))
		}
	}
	return out
}

// DisposeClaim records the single durable disposition of a claimed transfer, so
// its funds are never both credited and returned. Repeating the same disposition
// returns the record; a different one is ErrConflict.
func (l *Ledger) DisposeClaim(transferID, disposition string) (Claim, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	i, ok := l.claimIndex[transferID]
	if !ok {
		return Claim{}, ErrNotFound
	}
	if c := l.claims[i]; c.Disposition != "" {
		if c.Disposition != disposition {
			return Claim{}, ErrConflict
		}
		return cloneClaim(c), nil
	}
	if disposition != DispositionCredited && disposition != DispositionReturned {
		return Claim{}, ErrInvalidRequest
	}
	now, err := l.mutationTime()
	if err != nil {
		return Claim{}, err
	}
	if err := l.commit(record{Kind: kindDisposition, At: wire.FormatTime(now), TransferID: transferID, Disposition: disposition}); err != nil {
		return Claim{}, err
	}
	return cloneClaim(l.claims[i]), nil
}

func (l *Ledger) stageDisposition(r record, at time.Time) (func(), error) {
	i, ok := l.claimIndex[r.TransferID]
	if !ok || l.claims[i].Disposition != "" {
		return nil, errors.New("funding: claim missing or already disposed")
	}
	if r.Disposition != DispositionCredited && r.Disposition != DispositionReturned {
		return nil, fmt.Errorf("funding: unknown disposition %q", r.Disposition)
	}
	c := &l.claims[i]
	amount, ok := new(big.Int).SetString(c.AmountAccountingUnits, 10)
	if !ok {
		return nil, errors.New("funding: claim amount")
	}
	return func() {
		c.Disposition, c.DisposedAt = r.Disposition, at
		if r.Disposition == DispositionCredited {
			a := l.touch(c.AuthorID, c.Network, at)
			a.available.Add(a.available, amount)
		}
	}, nil
}

// transferKey is the protocol transfer identity: (network, txid, output_index)
// for Bitcoin and (network, tx_hash) for Ethereum. It is empty for a malformed transfer.
func transferKey(t wire.DepositTransfer) string {
	switch t.Network {
	case faux.BitcoinMainnet:
		if t.TxID == "" || t.TxHash != "" || t.OutputIndex == nil || *t.OutputIndex < 0 {
			return ""
		}
		return t.Network + "/" + t.TxID + "/" + strconv.FormatInt(*t.OutputIndex, 10)
	case faux.EthereumMainnet:
		if t.TxHash == "" || t.TxID != "" || t.OutputIndex != nil {
			return ""
		}
		return t.Network + "/" + t.TxHash
	}
	return ""
}

func lookupID(t wire.DepositTransfer) string {
	if t.Network == faux.BitcoinMainnet {
		return t.TxID
	}
	return t.TxHash
}

func sameTransfer(a, b wire.DepositTransfer) bool {
	if a.OutputIndex == nil || b.OutputIndex == nil {
		return a.Network == b.Network && a.TxID == b.TxID && a.TxHash == b.TxHash && a.OutputIndex == nil && b.OutputIndex == nil
	}
	return a.Network == b.Network && a.TxID == b.TxID && a.TxHash == b.TxHash && *a.OutputIndex == *b.OutputIndex
}

func sameCreditFields(a, b wire.DepositCredit) bool {
	if !sameTransfer(a.Transfer, b.Transfer) {
		return false
	}
	a.Transfer, b.Transfer = wire.DepositTransfer{}, wire.DepositTransfer{}
	a.CreditID, a.ProviderSignature, b.CreditID, b.ProviderSignature = "", "", "", ""
	return a == b
}

func cloneTransfer(t wire.DepositTransfer) wire.DepositTransfer {
	if t.OutputIndex != nil {
		i := *t.OutputIndex
		t.OutputIndex = &i
	}
	return t
}

func cloneCredit(c wire.DepositCredit) wire.DepositCredit {
	c.Transfer = cloneTransfer(c.Transfer)
	return c
}

func cloneClaim(c Claim) Claim {
	c.Transfer = cloneTransfer(c.Transfer)
	return c
}
