package sdk

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/content"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/message"
	"foryou.local/foryou/internal/wire"
)

var (
	// ErrUpdateAuthor identifies an update whose verified target has another author.
	ErrUpdateAuthor = errors.New("sdk: update target has another author")
	// ErrUpdateThread identifies an update whose reply_to differs from its target.
	ErrUpdateThread = errors.New("sdk: update target belongs to another thread")
	// ErrNoSchedule identifies a network without a release-approved current schedule.
	ErrNoSchedule = errors.New("sdk: no approved fee schedule in force")
	// ErrTooLarge identifies a final message that exceeds the protocol budget.
	ErrTooLarge = errors.New("sdk: final message exceeds protocol budget")
	// ErrQuoteMismatch identifies a provider quote that differs from local calculations.
	ErrQuoteMismatch = errors.New("sdk: quote mismatch")
	// ErrNotApproved identifies an approval that does not cover the exact quoted content and fee.
	ErrNotApproved = errors.New("sdk: exact quote was not approved")
	// ErrInsufficientBalance identifies a confirmed balance below the quoted fee; nothing is authorized.
	ErrInsufficientBalance = errors.New("sdk: confirmed balance does not cover the quoted fee")
	// ErrStale identifies a preparation or quote that should be recreated.
	ErrStale = errors.New("sdk: preparation is stale; prepare and quote again")
	// ErrOutcomeUnknown identifies an ambiguous result that requires replaying stored bytes.
	ErrOutcomeUnknown = errors.New("sdk: publication outcome unknown")
	// ErrPendingAuthorization identifies an unresolved authorization for the message ID.
	ErrPendingAuthorization = errors.New("sdk: authorization already pending")
	// ErrDeadlinePassed identifies a first finalization after its preparation deadline.
	ErrDeadlinePassed = errors.New("sdk: preparation deadline passed")
)

// Draft contains the exact post content to be signed and published.
type Draft struct {
	// ContentFormat is text/plain or text/markdown.
	ContentFormat string
	// Text is the exact signed text payload.
	Text string
	// MediaSlots contains external media metadata, without media bytes.
	MediaSlots []wire.MediaReference
	// Refs contains signed structured references.
	Refs []wire.PostReference
	// ReplyTo identifies the parent message for a comment.
	ReplyTo string
	// Supersedes identifies the prior version for an update.
	Supersedes string
}

// TargetEvidence is a fully verified record used to validate an update relationship.
type TargetEvidence struct {
	// Message is a fully verified target record.
	Message wire.PostMessage
}

// Prepared contains a locally validated intent and recomputed fee details.
type Prepared struct {
	// Intent is the exact signed intent without a receipt or signature.
	Intent wire.Intent
	// MessageID is the digest of Intent.
	MessageID string
	// BillableBytes is the UTF-8 JCS length of Intent.
	BillableBytes int
	// Fee is the locally recomputed integer base-unit amount.
	Fee string
	// Denomination is the base-unit name for Network.
	Denomination string
	// Network is the schedule network.
	Network string
	// ScheduleID is the release-approved schedule used for the local fee.
	ScheduleID string
	// Timestamp is the intent timestamp.
	Timestamp time.Time
	// Findings contains local heuristic secret-scan results.
	Findings []Finding
}

// Quoted combines a prepared intent with the provider's signed fee quote and exact response bytes.
type Quoted struct {
	// Prepared is the locally recomputed intent and fee.
	Prepared Prepared
	// Quote is the provider-signed fee quote.
	Quote wire.FeeQuote
	// Raw is the exact quote response body.
	Raw []byte
}

// Approval binds user approval to one message ID, fee amount, network and approval time.
type Approval struct {
	// MessageID, Amount and Network identify the exact approved quote.
	MessageID, Amount, Network string
	// ApprovedAt is when the user approved the exact content and fee.
	ApprovedAt time.Time
}

// Admitted contains the verified provider receipt and preparation deadline.
type Admitted struct {
	// Quoted is the approved quote and prepared intent.
	Quoted Quoted
	// Receipt is the verified provider admission receipt.
	Receipt wire.SubmissionReceipt
	// Deadline is the preparation deadline derived from receipt issuance.
	Deadline time.Time
}

// Published is returned only after the provider accepts the signed message.
type Published struct {
	// MessageID is the accepted message identifier.
	MessageID string
	// Raw contains the canonical signed message bytes.
	Raw []byte
	// Retry is true when the provider returned 200 for an identical retry.
	Retry bool
}

// Publisher coordinates locally verified quotes, admission, signing and durable retries.
type Publisher struct {
	client  *Client
	root    *TrustRoot
	journal Journal
	now     func() time.Time
	// mu serializes journal state checks with their writes so one message ID never gets two authorizations.
	mu sync.Mutex
}

// NewPublisher creates a publisher using the supplied clock or the wall clock.
func NewPublisher(c *Client, root *TrustRoot, j Journal, now func() time.Time) *Publisher {
	if now == nil {
		now = time.Now
	}
	return &Publisher{client: c, root: root, journal: j, now: now}
}

// Prepare validates and freezes a draft, derives its message ID, and computes the approved local fee.
func (p *Publisher) Prepare(d Draft, author ed25519.PrivateKey, network string, target *TargetEvidence) (Prepared, error) {
	if p == nil || p.root == nil || p.client == nil || p.journal == nil {
		return Prepared{}, errors.New("sdk: publisher is not configured")
	}
	authorID := keys.DIDOf(author)
	if authorID == "" {
		return Prepared{}, errors.New("sdk: invalid author key")
	}
	if d.Supersedes != "" && target != nil && target.Message.MessageID != d.Supersedes {
		return Prepared{}, errors.New("sdk: target evidence message_id differs from supersedes")
	}
	if d.Supersedes != "" && target != nil {
		if target.Message.AuthorID != authorID {
			return Prepared{}, ErrUpdateAuthor
		}
		if !message.SameReplyTo(target.Message.Payload, wire.PostPayload{ReplyTo: d.ReplyTo}) {
			return Prepared{}, ErrUpdateThread
		}
	}
	payload := pbPayload(d)
	if err := content.ValidatePostContent(payload); err != nil {
		return Prepared{}, err
	}
	now := p.now().UTC().Truncate(time.Second)
	i := wire.Intent{AuthorID: authorID, Timestamp: wire.FormatTime(now), MessageType: "post", Version: wire.Version{Major: 2, Minor: 0}, Payload: payload}
	if err := message.ValidateIntent(i); err != nil {
		return Prepared{}, err
	}
	id, err := message.ID(i)
	if err != nil {
		return Prepared{}, err
	}
	intentBytes, err := canon.Encode(i)
	if err != nil {
		return Prepared{}, err
	}
	schedule, ok := p.root.ScheduleInForce(network, now)
	if !ok {
		return Prepared{}, ErrNoSchedule
	}
	amount, err := fee.Amount(schedule, len(intentBytes))
	if err != nil {
		return Prepared{}, err
	}
	denomination, err := fee.Denomination(network)
	if err != nil {
		return Prepared{}, err
	}
	prepared := Prepared{Intent: i, MessageID: id, BillableBytes: len(intentBytes), Fee: fee.FormatAmount(amount), Denomination: denomination, Network: network, ScheduleID: schedule.ScheduleID, Timestamp: now, Findings: ScanDraft(d)}
	placeholder := wire.SubmissionReceipt{ProviderID: p.client.ServiceID(), AuthorID: authorID, MessageID: id, ReceiptID: strings.Repeat("A", 43), QuoteID: "fyfq2:" + strings.Repeat("0", 64), ScheduleID: schedule.ScheduleID, Network: network, AmountBaseUnits: prepared.Fee, AuthorizationAt: wire.FormatTime(now), IssuedAt: wire.FormatTime(now), ProviderSignature: strings.Repeat("A", 86)}
	placeholderBytes, err := canon.Encode(placeholder)
	if err != nil {
		return Prepared{}, err
	}
	finalSize, err := message.FinalSize(i, placeholder)
	if err != nil {
		return Prepared{}, err
	}
	if finalSize-len(placeholderBytes)+message.MaxReceiptBytes > message.MaxMessageBytes {
		return Prepared{}, ErrTooLarge
	}
	_, err = p.pbTransition(id, true, func(entry *JournalEntry) {
		*entry = JournalEntry{MessageID: id, State: "prepared", Draft: d, Prepared: prepared, ProviderEndpoint: p.client.BaseURL(), CreatedAt: wire.FormatTime(now), UpdatedAt: wire.FormatTime(now)}
	})
	if err != nil {
		return Prepared{}, err
	}
	return prepared, nil
}

// QuoteDraft prepares, quotes, and fully validates a draft before returning it for user review.
func (p *Publisher) QuoteDraft(ctx context.Context, d Draft, author ed25519.PrivateKey, network string, target *TargetEvidence) (Quoted, error) {
	prepared, err := p.Prepare(d, author, network, target)
	if err != nil {
		return Quoted{}, err
	}
	return p.Quote(ctx, author, prepared)
}

// Quote obtains and verifies a provider quote against the local intent, fee and pinned trust root.
func (p *Publisher) Quote(ctx context.Context, key ed25519.PrivateKey, prepared Prepared) (Quoted, error) {
	if err := p.pbValidatePrepared(prepared, key); err != nil {
		return Quoted{}, err
	}
	if _, err := p.pbTransition(prepared.MessageID, false, func(*JournalEntry) {}); err != nil {
		return Quoted{}, err
	}
	providerID := p.client.ServiceID()
	authorID := keys.DIDOf(key)
	auth, err := fee.SignPreflight(fee.PreflightAuthorization(providerID, authorID, prepared.MessageID, prepared.ScheduleID), key)
	if err != nil {
		return Quoted{}, err
	}
	intent, err := canon.Encode(prepared.Intent)
	if err != nil {
		return Quoted{}, err
	}
	request := wire.FeePreflightRequest{ProviderID: providerID, AuthorID: authorID, ScheduleID: prepared.ScheduleID, Intent: intent, Authorization: auth}
	body, err := canon.Encode(request)
	if err != nil {
		return Quoted{}, err
	}
	res, err := p.client.send(ctx, http.MethodPost, "v1/fee-quotes", nil, body, nil, 1<<20)
	if err != nil {
		return Quoted{}, err
	}
	if res.status != http.StatusOK {
		return Quoted{}, rdProtocol("unexpected fee quote success status")
	}
	var quote wire.FeeQuote
	if err := wire.Decode(res.body, "FeeQuote", &quote); err != nil {
		return Quoted{}, rdProtocol("invalid fee quote")
	}
	quoted := Quoted{Prepared: prepared, Quote: quote, Raw: append([]byte(nil), res.body...)}
	if err := p.pbCheckQuoted(quoted, p.now()); err != nil {
		return Quoted{}, err
	}
	_, err = p.pbTransition(prepared.MessageID, false, func(entry *JournalEntry) {
		entry.State, entry.Quote, entry.QuoteRaw, entry.UpdatedAt = "quoted", quote, quoted.Raw, wire.FormatTime(p.now())
	})
	if err != nil {
		return Quoted{}, err
	}
	return quoted, nil
}

// Admit verifies approval and quote freshness, journals authorization before debit, and verifies the receipt.
func (p *Publisher) Admit(ctx context.Context, key ed25519.PrivateKey, q Quoted, approval Approval) (Admitted, error) {
	if err := p.pbValidatePrepared(q.Prepared, key); err != nil {
		return Admitted{}, err
	}
	if err := p.pbCheckQuoted(q, p.now()); err != nil {
		return Admitted{}, err
	}
	if approval.MessageID != q.Prepared.MessageID || approval.Amount != q.Quote.AmountBaseUnits || approval.Network != q.Quote.Network || approval.ApprovedAt.IsZero() {
		return Admitted{}, ErrNotApproved
	}
	if p.now().Sub(q.Prepared.Timestamp) > 240*time.Second {
		return Admitted{}, ErrStale
	}
	balance, err := p.client.Balance(ctx, key, q.Quote.Network)
	if err != nil {
		return Admitted{}, err
	}
	available, err := fee.ParseAmount(balance.Available)
	if err != nil {
		return Admitted{}, err
	}
	amount, err := fee.ParseAmount(q.Quote.AmountBaseUnits)
	if err != nil {
		return Admitted{}, err
	}
	if available.Cmp(amount) < 0 {
		return Admitted{}, ErrInsufficientBalance
	}
	authorization, err := fee.SignAdmission(fee.AdmissionAuthorization(p.client.ServiceID(), q.Quote.AuthorID, q.Prepared.MessageID, q.Quote.QuoteID), key)
	if err != nil {
		return Admitted{}, err
	}
	request := wire.AdmissionRequest{ProviderID: p.client.ServiceID(), Intent: q.Prepared.Intent, Authorization: authorization, QuoteID: q.Quote.QuoteID}
	requestBytes, err := canon.Encode(request)
	if err != nil {
		return Admitted{}, err
	}
	entry, err := p.pbTransition(q.Prepared.MessageID, false, func(entry *JournalEntry) {
		entry.State, entry.Quote, entry.QuoteRaw, entry.AdmissionRequest, entry.UpdatedAt = "authorized", q.Quote, append([]byte(nil), q.Raw...), requestBytes, wire.FormatTime(p.now())
	})
	if err != nil {
		return Admitted{}, err
	}
	return p.pbSendAdmission(ctx, entry)
}

// Publish admits and finalizes the exact previously quoted intent.
func (p *Publisher) Publish(ctx context.Context, key ed25519.PrivateKey, q Quoted, approval Approval) (Published, error) {
	admitted, err := p.Admit(ctx, key, q, approval)
	if err != nil {
		return Published{}, err
	}
	return p.Finalize(ctx, key, admitted)
}

// Finalize attaches the verified receipt, signs the complete message, journals the exact bytes and submits them.
// The record counts as published only once the provider accepts it. At or after the preparation deadline
// (receipt issued_at plus 30 days) the provider refuses a first finalization and there is no automatic refund,
// so Finalize refuses with ErrDeadlinePassed; already signed bytes are still resubmitted, because an accepted
// identical retry is recovered before the deadline check.
func (p *Publisher) Finalize(ctx context.Context, key ed25519.PrivateKey, admitted Admitted) (Published, error) {
	entry, ok, err := p.journal.Get(admitted.Quoted.Prepared.MessageID)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("sdk: admitted journal entry missing")
		}
		return Published{}, err
	}
	if entry.State == "signed" {
		return p.pbSendFinal(ctx, entry)
	}
	if entry.State != "admitted" {
		return Published{}, errors.New("sdk: publication is not admitted")
	}
	if entry.Prepared.MessageID != admitted.Quoted.Prepared.MessageID || entry.Receipt != admitted.Receipt || entry.Quote != admitted.Quoted.Quote {
		return Published{}, errors.New("sdk: admitted value differs from durable publication evidence")
	}
	if err := fee.CheckReceipt(entry.Receipt, entry.Quote); err != nil || entry.Receipt.ProviderID != p.client.ServiceID() {
		return Published{}, errors.New("sdk: durable receipt does not match pinned quote")
	}
	deadline, err := wire.ParseTime(entry.Deadline)
	if err != nil {
		return Published{}, err
	}
	if !p.now().Before(deadline) {
		return Published{}, ErrDeadlinePassed
	}
	m, err := message.Assemble(entry.Prepared.Intent, entry.Receipt)
	if err != nil {
		return Published{}, err
	}
	if err := message.Sign(&m, key); err != nil {
		return Published{}, err
	}
	if err := message.Verify(m, message.VerifyOptions{Now: p.now()}); err != nil {
		return Published{}, err
	}
	raw, err := canon.Encode(m)
	if err != nil {
		return Published{}, err
	}
	entry.State, entry.FinalMessage, entry.UpdatedAt = "signed", raw, wire.FormatTime(p.now())
	if err := p.journal.Put(entry); err != nil {
		return Published{}, err
	}
	return p.pbSendFinal(ctx, entry)
}

// Resume retries only the exact durable request for an unresolved publication.
func (p *Publisher) Resume(ctx context.Context, key ed25519.PrivateKey, messageID string) (JournalEntry, error) {
	entry, ok, err := p.journal.Get(messageID)
	if err != nil || !ok {
		if err == nil {
			err = errors.New("sdk: publication journal entry not found")
		}
		return JournalEntry{}, err
	}
	switch entry.State {
	case "authorized":
		_, err = p.pbSendAdmission(ctx, entry)
	case "admitted":
		_, err = p.Finalize(ctx, key, Admitted{Quoted: Quoted{Prepared: entry.Prepared, Quote: entry.Quote, Raw: entry.QuoteRaw}, Receipt: entry.Receipt, Deadline: pbTime(entry.Deadline)})
	case "signed":
		_, err = p.pbSendFinal(ctx, entry)
	case "prepared", "quoted", "accepted", "rejected":
		return entry, nil
	default:
		err = errors.New("sdk: unknown publication state")
	}
	updated, _, getErr := p.journal.Get(messageID)
	if getErr != nil {
		return JournalEntry{}, getErr
	}
	return updated, err
}

// pbTransition applies update to the journal entry for id while it is still before authorization.
// An entry at or after authorization is never overwritten: a new authorization for its message ID is refused.
func (p *Publisher) pbTransition(id string, create bool, update func(*JournalEntry)) (JournalEntry, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	entry, ok, err := p.journal.Get(id)
	if err != nil {
		return JournalEntry{}, err
	}
	if !ok && !create {
		return JournalEntry{}, errors.New("sdk: prepared journal entry missing")
	}
	switch entry.State {
	case "authorized", "admitted", "signed", "accepted":
		return JournalEntry{}, ErrPendingAuthorization
	case "rejected":
		return JournalEntry{}, errors.New("sdk: publication was definitively rejected; prepare a new intent")
	}
	update(&entry)
	if err := p.journal.Put(entry); err != nil {
		return JournalEntry{}, err
	}
	return entry, nil
}

func (p *Publisher) pbValidatePrepared(prepared Prepared, key ed25519.PrivateKey) error {
	if prepared.Intent.AuthorID != keys.DIDOf(key) {
		return errors.New("sdk: author key does not match intent")
	}
	if err := message.ValidateIntent(prepared.Intent); err != nil {
		return err
	}
	id, err := message.ID(prepared.Intent)
	if err != nil {
		return err
	}
	intentBytes, err := canon.Encode(prepared.Intent)
	if err != nil {
		return err
	}
	if id != prepared.MessageID || len(intentBytes) != prepared.BillableBytes {
		return fmt.Errorf("%w: intent or billable_bytes", ErrQuoteMismatch)
	}
	intentTime, err := wire.ParseTime(prepared.Intent.Timestamp)
	if err != nil || !intentTime.Equal(prepared.Timestamp.UTC().Truncate(time.Second)) {
		return fmt.Errorf("%w: timestamp", ErrQuoteMismatch)
	}
	if p.root == nil || p.client == nil {
		return errors.New("sdk: publisher is not configured")
	}
	schedule, ok := p.root.ScheduleInForce(prepared.Network, prepared.Timestamp)
	if !ok || schedule.ScheduleID != prepared.ScheduleID {
		return ErrNoSchedule
	}
	amount, err := fee.Amount(schedule, len(intentBytes))
	if err != nil {
		return err
	}
	if fee.FormatAmount(amount) != prepared.Fee {
		return fmt.Errorf("%w: local amount", ErrQuoteMismatch)
	}
	return nil
}

func (p *Publisher) pbCheckQuoted(q Quoted, now time.Time) error {
	if err := fee.VerifyQuote(q.Quote); err != nil {
		return fmt.Errorf("%w: provider_signature: %v", ErrQuoteMismatch, err)
	}
	id, err := message.ID(q.Prepared.Intent)
	if err != nil {
		return fmt.Errorf("%w: intent", ErrQuoteMismatch)
	}
	intentBytes, err := canon.Encode(q.Prepared.Intent)
	if err != nil {
		return fmt.Errorf("%w: intent", ErrQuoteMismatch)
	}
	if id != q.Prepared.MessageID {
		return fmt.Errorf("%w: message_id", ErrQuoteMismatch)
	}
	if len(intentBytes) != q.Prepared.BillableBytes {
		return fmt.Errorf("%w: billable_bytes", ErrQuoteMismatch)
	}
	if q.Quote.ProviderID != p.client.ServiceID() {
		return fmt.Errorf("%w: provider_id", ErrQuoteMismatch)
	}
	expect := fee.Expectation{ProviderID: p.client.ServiceID(), AuthorID: q.Prepared.Intent.AuthorID, MessageID: id, BillableBytes: len(intentBytes)}
	if err := fee.CheckQuote(q.Quote, p.root.schedules, expect, now); errors.Is(err, fee.ErrQuoteExpired) {
		return ErrStale
	} else if err != nil {
		return fmt.Errorf("%w: %v", ErrQuoteMismatch, err)
	}
	schedule, ok := p.root.ScheduleInForce(q.Prepared.Network, q.Prepared.Timestamp)
	if !ok || q.Quote.ScheduleID != q.Prepared.ScheduleID || q.Quote.Network != q.Prepared.Network || q.Quote.ScheduleID != schedule.ScheduleID {
		return fmt.Errorf("%w: schedule_id", ErrQuoteMismatch)
	}
	amount, err := fee.Amount(schedule, len(intentBytes))
	if err != nil || q.Quote.AmountBaseUnits != fee.FormatAmount(amount) || q.Quote.AmountBaseUnits != q.Prepared.Fee {
		return fmt.Errorf("%w: amount_base_units", ErrQuoteMismatch)
	}
	preflight, _ := wire.ParseTime(q.Quote.PreflightAt)
	expires, _ := wire.ParseTime(q.Quote.ExpiresAt)
	if expires.After(preflight.Add(3600 * time.Second)) {
		return fmt.Errorf("%w: expires_at", ErrQuoteMismatch)
	}
	if !now.Add(30 * time.Second).Before(expires) {
		return ErrStale
	}
	if len(q.Raw) == 0 {
		return fmt.Errorf("%w: raw quote", ErrQuoteMismatch)
	}
	var rawQuote wire.FeeQuote
	if err := wire.Decode(q.Raw, "FeeQuote", &rawQuote); err != nil {
		return fmt.Errorf("%w: raw quote", ErrQuoteMismatch)
	}
	canonicalRawQuote, err := canon.Encode(rawQuote)
	if err != nil {
		return fmt.Errorf("%w: raw quote", ErrQuoteMismatch)
	}
	canonicalQuote, err := canon.Encode(q.Quote)
	if err != nil || string(canonicalRawQuote) != string(canonicalQuote) {
		return fmt.Errorf("%w: raw quote", ErrQuoteMismatch)
	}
	return nil
}

func (p *Publisher) pbSendAdmission(ctx context.Context, entry JournalEntry) (Admitted, error) {
	res, err := p.client.send(ctx, http.MethodPost, "v1/admissions", nil, entry.AdmissionRequest, nil, 1<<20)
	if err != nil {
		if IsAmbiguous(err) {
			return Admitted{}, fmt.Errorf("%w: %w", ErrOutcomeUnknown, err)
		}
		return Admitted{}, errors.Join(err, p.pbReject(entry, err))
	}
	if res.status != http.StatusCreated && res.status != http.StatusOK {
		return Admitted{}, rdProtocol("unexpected admission success status")
	}
	var receipt wire.SubmissionReceipt
	if err := wire.Decode(res.body, "SubmissionReceipt", &receipt); err != nil {
		return Admitted{}, rdProtocol("invalid submission receipt")
	}
	if err := fee.CheckReceipt(receipt, entry.Quote); err != nil || receipt.ProviderID != p.client.ServiceID() {
		return Admitted{}, rdProtocol("receipt does not match quote")
	}
	issued, err := wire.ParseTime(receipt.IssuedAt)
	if err != nil {
		return Admitted{}, rdProtocol("invalid receipt time")
	}
	deadline := issued.Add(30 * 24 * time.Hour)
	entry.State, entry.Receipt, entry.ReceiptRaw, entry.Deadline, entry.UpdatedAt = "admitted", receipt, append([]byte(nil), res.body...), wire.FormatTime(deadline), wire.FormatTime(p.now())
	if err := p.journal.Put(entry); err != nil {
		return Admitted{}, err
	}
	return Admitted{Quoted: Quoted{Prepared: entry.Prepared, Quote: entry.Quote, Raw: entry.QuoteRaw}, Receipt: receipt, Deadline: deadline}, nil
}

func (p *Publisher) pbSendFinal(ctx context.Context, entry JournalEntry) (Published, error) {
	res, err := p.client.send(ctx, http.MethodPost, "v1/messages", nil, entry.FinalMessage, nil, 1<<20)
	if err != nil {
		if IsAmbiguous(err) {
			return Published{}, fmt.Errorf("%w: %w", ErrOutcomeUnknown, err)
		}
		return Published{}, errors.Join(err, p.pbReject(entry, err))
	}
	if res.status != http.StatusCreated && res.status != http.StatusOK {
		return Published{}, rdProtocol("unexpected message acceptance status")
	}
	var acceptance wire.MessageAcceptance
	if err := wire.Decode(res.body, "MessageAcceptance", &acceptance); err != nil || acceptance.MessageID != entry.MessageID || acceptance.Status != "accepted" {
		return Published{}, rdProtocol("invalid message acceptance")
	}
	entry.State, entry.UpdatedAt = "accepted", wire.FormatTime(p.now())
	if err := p.journal.Put(entry); err != nil {
		return Published{}, err
	}
	return Published{MessageID: entry.MessageID, Raw: append([]byte(nil), entry.FinalMessage...), Retry: res.status == http.StatusOK}, nil
}

func (p *Publisher) pbReject(entry JournalEntry, err error) error {
	var serviceError *Error
	if errors.As(err, &serviceError) {
		entry.State, entry.Rejection, entry.UpdatedAt = "rejected", serviceError.Status, wire.FormatTime(p.now())
		return p.journal.Put(entry)
	}
	return nil
}

func pbPayload(d Draft) wire.PostPayload {
	p := wire.PostPayload{ContentFormat: d.ContentFormat, Text: d.Text, ReplyTo: d.ReplyTo, Supersedes: d.Supersedes}
	if d.MediaSlots != nil {
		slots := append([]wire.MediaReference(nil), d.MediaSlots...)
		p.MediaSlots = &slots
	}
	if d.Refs != nil {
		refs := append([]wire.PostReference(nil), d.Refs...)
		p.Refs = &refs
	}
	return p
}

func pbTime(value string) time.Time { t, _ := wire.ParseTime(value); return t }
