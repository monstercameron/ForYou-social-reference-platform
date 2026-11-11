package fee

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

// MaxQuoteLifetime is the maximum lifetime of a signed fee quote.
const MaxQuoteLifetime = 3600 * time.Second

var (
	// ErrQuoteMismatch identifies quote fields that do not match client expectations.
	ErrQuoteMismatch = errors.New("fee: quote mismatch")
	// ErrQuoteExpired identifies a quote that is no longer usable.
	ErrQuoteExpired = errors.New("fee: quote expired")
)

// QuoteRequest contains the immutable intent details and quote interval.
type QuoteRequest struct {
	ProviderID, AuthorID, MessageID string
	BillableBytes                   int
	PreflightAt, ExpiresAt          time.Time
}

// Expectation contains client-recomputed values used to validate a provider quote.
type Expectation struct {
	ProviderID, AuthorID, MessageID string
	BillableBytes                   int
}

// QuoteID computes the quote identifier without its ID and provider signature.
func QuoteID(q wire.FeeQuote) (string, error) {
	b, err := canon.EncodeWithout(q, "quote_id", "provider_signature")
	if err != nil {
		return "", err
	}
	return keys.DigestID("fyfq2:", "fee-quote-id", b)
}

// IssueQuote creates and signs a quote for the schedule effective at preflight.
func IssueQuote(r QuoteRequest, s wire.FeeSchedule, schedules []wire.FeeSchedule, key ed25519.PrivateKey) (wire.FeeQuote, error) {
	if err := ValidateSchedule(s); err != nil {
		return wire.FeeQuote{}, err
	}
	preflightAt := r.PreflightAt.UTC().Truncate(time.Second)
	expiresAt := r.ExpiresAt.UTC().Truncate(time.Second)
	current, err := InForce(schedules, s.Network, preflightAt)
	if err != nil || current.ScheduleID != s.ScheduleID {
		return wire.FeeQuote{}, fmt.Errorf("fee: schedule_id not in force")
	}
	if r.BillableBytes < 1 || r.BillableBytes > MaxBillableBytes {
		return wire.FeeQuote{}, fmt.Errorf("fee: billable_bytes out of range")
	}
	if !expiresAt.After(preflightAt) || expiresAt.Sub(preflightAt) > MaxQuoteLifetime {
		return wire.FeeQuote{}, fmt.Errorf("fee: invalid quote expiry")
	}
	if r.ProviderID != keys.DIDOf(key) {
		return wire.FeeQuote{}, fmt.Errorf("fee: provider_id does not match signing key")
	}
	amount, err := Amount(s, r.BillableBytes)
	if err != nil {
		return wire.FeeQuote{}, err
	}
	q := wire.FeeQuote{ProviderID: r.ProviderID, AuthorID: r.AuthorID, MessageID: r.MessageID, ScheduleID: s.ScheduleID, BillableBytes: int64(r.BillableBytes), Network: s.Network, AmountBaseUnits: FormatAmount(amount), PreflightAt: wire.FormatTime(preflightAt), ExpiresAt: wire.FormatTime(expiresAt)}
	id, err := QuoteID(q)
	if err != nil {
		return wire.FeeQuote{}, err
	}
	q.QuoteID = id
	b, err := canon.EncodeWithout(q, "provider_signature")
	if err != nil {
		return wire.FeeQuote{}, err
	}
	q.ProviderSignature, err = keys.Sign(key, "fee-quote", b)
	if err != nil {
		return wire.FeeQuote{}, err
	}
	if err := VerifyQuote(q); err != nil {
		return wire.FeeQuote{}, err
	}
	return q, nil
}

// VerifyQuote checks the quote schema, ID, signature, amount, and lifetime.
func VerifyQuote(q wire.FeeQuote) error {
	v, err := wireValue(q)
	if err != nil {
		return err
	}
	if err := wire.Check("FeeQuote", v); err != nil {
		return err
	}
	if q.BillableBytes < 1 || q.BillableBytes > MaxBillableBytes {
		return fmt.Errorf("billable_bytes: out of range")
	}
	if _, err := Denomination(q.Network); err != nil {
		return fmt.Errorf("network: %w", err)
	}
	if _, err := ParsePositiveAmount(q.AmountBaseUnits); err != nil {
		return fmt.Errorf("amount_base_units: %w", err)
	}
	id, err := QuoteID(q)
	if err != nil || id != q.QuoteID {
		return fmt.Errorf("quote_id: mismatch")
	}
	preflight, err := wire.ParseTime(q.PreflightAt)
	if err != nil {
		return fmt.Errorf("preflight_at: %w", err)
	}
	expires, err := wire.ParseTime(q.ExpiresAt)
	if err != nil || !expires.After(preflight) || expires.Sub(preflight) > MaxQuoteLifetime {
		return fmt.Errorf("expires_at: invalid lifetime")
	}
	b, err := canon.EncodeWithout(q, "provider_signature")
	if err != nil {
		return err
	}
	if err := keys.Verify(q.ProviderID, "fee-quote", b, q.ProviderSignature); err != nil {
		return fmt.Errorf("provider_signature: %w", err)
	}
	return nil
}

// CheckQuote compares a verified provider quote with client-recomputed intent and schedule data.
func CheckQuote(q wire.FeeQuote, schedules []wire.FeeSchedule, want Expectation, now time.Time) error {
	if err := VerifyQuote(q); err != nil {
		return err
	}
	preflight, _ := wire.ParseTime(q.PreflightAt)
	var named wire.FeeSchedule
	for _, candidate := range schedules {
		if candidate.ScheduleID == q.ScheduleID {
			named = candidate
			break
		}
	}
	if named.ScheduleID == "" {
		return mismatch("schedule_id")
	}
	if q.Network != named.Network {
		return mismatch("network")
	}
	s, err := InForce(schedules, named.Network, preflight)
	if err != nil || s.ScheduleID != named.ScheduleID {
		return mismatch("schedule_id")
	}
	if int64(want.BillableBytes) != q.BillableBytes {
		return mismatch("billable_bytes")
	}
	amount, err := Amount(s, want.BillableBytes)
	if err != nil {
		return mismatch("billable_bytes")
	}
	if FormatAmount(amount) != q.AmountBaseUnits {
		return mismatch("amount_base_units")
	}
	if q.ProviderID != want.ProviderID {
		return mismatch("provider_id")
	}
	if q.AuthorID != want.AuthorID {
		return mismatch("author_id")
	}
	if q.MessageID != want.MessageID {
		return mismatch("message_id")
	}
	expires, _ := wire.ParseTime(q.ExpiresAt)
	if !now.Before(expires) {
		return ErrQuoteExpired
	}
	return nil
}

func mismatch(field string) error { return fmt.Errorf("%w: %s", ErrQuoteMismatch, field) }

// PreflightAuthorizationObject contains the fields covered by a preflight signature.
type PreflightAuthorizationObject struct {
	ProviderID string `json:"provider_id"`
	AuthorID   string `json:"author_id"`
	MessageID  string `json:"message_id"`
	ScheduleID string `json:"schedule_id"`
}

// PreflightAuthorization constructs the object signed for fee preflight.
func PreflightAuthorization(providerID, authorID, messageID, scheduleID string) PreflightAuthorizationObject {
	return PreflightAuthorizationObject{ProviderID: providerID, AuthorID: authorID, MessageID: messageID, ScheduleID: scheduleID}
}

// SignPreflight signs a preflight authorization with its label-specific canonical input.
func SignPreflight(a PreflightAuthorizationObject, key ed25519.PrivateKey) (string, error) {
	b, err := canon.Encode(a)
	if err != nil {
		return "", err
	}
	return keys.Sign(key, "fee-preflight", b)
}

// VerifyPreflight verifies a preflight authorization by the supplied signer.
func VerifyPreflight(a PreflightAuthorizationObject, signerID, signature string) error {
	b, err := canon.Encode(a)
	if err != nil {
		return err
	}
	return keys.Verify(signerID, "fee-preflight", b, signature)
}

// AdmissionAuthorization binds admission authorization to provider, author, message, and quote.
func AdmissionAuthorization(providerID, authorID, messageID, quoteID string) wire.AdmissionAuthorization {
	return wire.AdmissionAuthorization{ProviderID: providerID, AuthorID: authorID, MessageID: messageID, QuoteID: quoteID}
}

// SignAdmission signs admission authorization with its label-specific canonical input.
func SignAdmission(a wire.AdmissionAuthorization, key ed25519.PrivateKey) (string, error) {
	b, err := canon.Encode(a)
	if err != nil {
		return "", err
	}
	return keys.Sign(key, "admission", b)
}

// VerifyAdmission verifies admission authorization by the supplied signer.
func VerifyAdmission(a wire.AdmissionAuthorization, signerID, signature string) error {
	b, err := canon.Encode(a)
	if err != nil {
		return err
	}
	return keys.Verify(signerID, "admission", b, signature)
}
