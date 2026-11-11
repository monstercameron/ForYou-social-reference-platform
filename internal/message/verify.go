package message

import (
	"errors"
	"fmt"
	"time"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

// DelegationCheck verifies a delegation for the enclosing intent at the receipt's authorization time.
type DelegationCheck func(d wire.Delegation, i wire.Intent, providerID string, at time.Time) error

// VerifyOptions supplies the relay clock and historical delegation verifier.
type VerifyOptions struct {
	// Now is the verifier's clock used for receipt future-time checks.
	Now time.Time
	// Delegation checks historical delegation validity at authorization_at.
	Delegation DelegationCheck
}

// Verify validates message structure, IDs, receipt and message signatures, delegation, and final size.
// Callers must also run content.ValidatePostContent and verify provider trust, quote/schedule bindings,
// fee rules, and settlement state in their owning packages.
func Verify(m wire.PostMessage, opts VerifyOptions) error {
	i := IntentOf(m)
	if err := ValidateIntent(i); err != nil {
		return err
	}
	id, err := ID(i)
	if err != nil {
		return err
	}
	if id != m.MessageID {
		return fmt.Errorf("%w: recomputed message ID does not match", ErrIDMismatch)
	}
	r := m.SubmissionReceipt
	receiptBytes, err := canon.Encode(r)
	if err != nil {
		return invalidError("receipt encoding", err)
	}
	if len(receiptBytes) > MaxReceiptBytes {
		return invalidError("receipt exceeds size limit", nil)
	}
	receiptTree, err := canon.Parse(receiptBytes, canon.MessageLimits)
	if err != nil {
		return invalidError("receipt JSON", err)
	}
	if err := wire.Check("SubmissionReceipt", receiptTree); err != nil {
		return invalidError("receipt schema", err)
	}
	if r.MessageID != m.MessageID {
		return invalidError("receipt message ID binding", nil)
	}
	if r.AuthorID != m.AuthorID {
		return invalidError("receipt author binding", nil)
	}
	if r.AuthorizationAt != r.IssuedAt {
		return invalidError("receipt authorization_at binding", nil)
	}
	if _, err := keys.DecodeB64URL(r.ReceiptID, 32); err != nil {
		return invalidError("receipt ID encoding", err)
	}
	authorizationAt, err := wire.ParseTime(r.AuthorizationAt)
	if err != nil {
		return invalidError("receipt authorization_at", err)
	}
	issuedAt, err := wire.ParseTime(r.IssuedAt)
	if err != nil {
		return invalidError("receipt issued_at", err)
	}
	receiptCanonical, err := canon.EncodeWithout(r, "provider_signature")
	if err != nil {
		return invalidError("receipt signing input", err)
	}
	if err := keys.Verify(r.ProviderID, "receipt", receiptCanonical, r.ProviderSignature); err != nil {
		return invalidError("receipt signature", err)
	}
	if issuedAt.After(opts.Now.Add(60 * time.Second)) {
		return invalidError("receipt issued_at more than 60 seconds ahead", nil)
	}
	signer := m.AuthorID
	if m.SignerID != "" {
		signer = m.SignerID
	}
	messageCanonical, err := canon.EncodeWithout(m, "signature")
	if err != nil {
		return invalidError("message signing input", err)
	}
	if err := keys.Verify(signer, "message", messageCanonical, m.Signature); err != nil {
		return invalidError("message signature", err)
	}
	if m.Delegation != nil {
		if opts.Delegation == nil {
			return invalidError("delegation verifier is required", nil)
		}
		if err := opts.Delegation(*m.Delegation, i, r.ProviderID, authorizationAt); err != nil {
			var publicError *apierr.Error
			if errors.As(err, &publicError) {
				return err
			}
			return invalidError("delegation", err)
		}
	}
	complete, err := canon.Encode(m)
	if err != nil {
		return invalidError("complete message encoding", err)
	}
	if len(complete) > MaxMessageBytes {
		return tooLargeError("complete message exceeds size limit", nil)
	}
	return nil
}
