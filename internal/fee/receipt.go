package fee

import (
	"crypto/ed25519"
	"crypto/rand"
	"fmt"
	"io"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

// NewReceiptID creates a canonical unpadded base64url ID from 32 random bytes.
func NewReceiptID(random io.Reader) (string, error) {
	if random == nil {
		random = rand.Reader
	}
	b := make([]byte, 32)
	if _, err := io.ReadFull(random, b); err != nil {
		return "", err
	}
	return keys.EncodeB64URL(b), nil
}

// SignReceipt signs a receipt under the provider key and returns it only if the signed receipt verifies.
func SignReceipt(r wire.SubmissionReceipt, key ed25519.PrivateKey) (wire.SubmissionReceipt, error) {
	if r.ProviderID != keys.DIDOf(key) {
		return wire.SubmissionReceipt{}, fmt.Errorf("fee: provider_id does not match signing key")
	}
	b, err := canon.EncodeWithout(r, "provider_signature")
	if err != nil {
		return wire.SubmissionReceipt{}, err
	}
	r.ProviderSignature, err = keys.Sign(key, "receipt", b)
	if err != nil {
		return wire.SubmissionReceipt{}, err
	}
	if err := VerifyReceipt(r); err != nil {
		return wire.SubmissionReceipt{}, err
	}
	return r, nil
}

// VerifyReceipt checks the receipt's closed schema, identifiers, timestamps, amount, and signature.
func VerifyReceipt(r wire.SubmissionReceipt) error {
	v, err := wireValue(r)
	if err != nil {
		return err
	}
	if err := wire.Check("SubmissionReceipt", v); err != nil {
		return err
	}
	decoded, err := keys.DecodeB64URL(r.ReceiptID, 32)
	if err != nil {
		return fmt.Errorf("receipt_id: %w", err)
	}
	if keys.EncodeB64URL(decoded) != r.ReceiptID {
		return fmt.Errorf("receipt_id: noncanonical encoding")
	}
	if _, err := ParsePositiveAmount(r.AmountBaseUnits); err != nil {
		return fmt.Errorf("amount_base_units: %w", err)
	}
	if !validID(r.QuoteID, "fyfq2:") || !validID(r.ScheduleID, "fyfs2:") {
		return fmt.Errorf("receipt: invalid quote or schedule ID")
	}
	issued, err := wire.ParseTime(r.IssuedAt)
	if err != nil {
		return fmt.Errorf("issued_at: %w", err)
	}
	authorized, err := wire.ParseTime(r.AuthorizationAt)
	if err != nil || !authorized.Equal(issued) {
		return fmt.Errorf("authorization_at: must equal issued_at")
	}
	b, err := canon.EncodeWithout(r, "provider_signature")
	if err != nil {
		return err
	}
	if err := keys.Verify(r.ProviderID, "receipt", b, r.ProviderSignature); err != nil {
		return fmt.Errorf("provider_signature: %w", err)
	}
	return nil
}

func validID(value, prefix string) bool {
	if len(value) != len(prefix)+64 || value[:len(prefix)] != prefix {
		return false
	}
	for _, c := range value[len(prefix):] {
		if !('0' <= c && c <= '9') && !('a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// CheckReceipt verifies a receipt and its binding to the fee quote.
func CheckReceipt(r wire.SubmissionReceipt, q wire.FeeQuote) error {
	if err := VerifyReceipt(r); err != nil {
		return err
	}
	if err := VerifyQuote(q); err != nil {
		return err
	}
	if r.ProviderID != q.ProviderID {
		return mismatch("provider_id")
	}
	if r.AuthorID != q.AuthorID {
		return mismatch("author_id")
	}
	if r.MessageID != q.MessageID {
		return mismatch("message_id")
	}
	if r.QuoteID != q.QuoteID {
		return mismatch("quote_id")
	}
	if r.ScheduleID != q.ScheduleID {
		return mismatch("schedule_id")
	}
	if r.Network != q.Network {
		return mismatch("network")
	}
	if r.AmountBaseUnits != q.AmountBaseUnits {
		return mismatch("amount_base_units")
	}
	return nil
}
