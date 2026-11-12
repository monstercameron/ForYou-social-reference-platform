package funding

import (
	"crypto/ed25519"
	"errors"
	"fmt"
	"strings"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

// Signer signs funding objects with a provider's Ed25519 key.
type Signer struct{ Key ed25519.PrivateKey }

// DepositID computes the content ID without the invoice ID and signature.
func DepositID(inv wire.DepositInvoice) (string, error) {
	b, err := canon.EncodeWithout(inv, "deposit_id", "provider_signature")
	if err != nil {
		return "", err
	}
	return keys.DigestID("fydi2:", "deposit-invoice-id", b)
}

// CreditID computes the content ID without the credit ID and signature.
func CreditID(c wire.DepositCredit) (string, error) {
	b, err := canon.EncodeWithout(c, "credit_id", "provider_signature")
	if err != nil {
		return "", err
	}
	return keys.DigestID("fydc2:", "deposit-credit-id", b)
}

// SealInvoice computes the invoice ID and provider signature.
func (s Signer) SealInvoice(inv wire.DepositInvoice) (wire.DepositInvoice, error) {
	if inv.ProviderID != keys.DIDOf(s.Key) {
		return wire.DepositInvoice{}, errors.New("funding: provider_id does not match signing key")
	}
	id, err := DepositID(inv)
	if err != nil {
		return wire.DepositInvoice{}, err
	}
	inv.DepositID = id
	b, err := canon.EncodeWithout(inv, "provider_signature")
	if err != nil {
		return wire.DepositInvoice{}, err
	}
	inv.ProviderSignature, err = keys.Sign(s.Key, "deposit-invoice", b)
	if err != nil {
		return wire.DepositInvoice{}, err
	}
	if err := VerifyInvoice(inv, inv.ProviderID); err != nil {
		return wire.DepositInvoice{}, err
	}
	return inv, nil
}

// SealCredit computes the credit ID and provider signature.
func (s Signer) SealCredit(c wire.DepositCredit) (wire.DepositCredit, error) {
	if c.ProviderID != keys.DIDOf(s.Key) {
		return wire.DepositCredit{}, errors.New("funding: provider_id does not match signing key")
	}
	id, err := CreditID(c)
	if err != nil {
		return wire.DepositCredit{}, err
	}
	c.CreditID = id
	b, err := canon.EncodeWithout(c, "provider_signature")
	if err != nil {
		return wire.DepositCredit{}, err
	}
	c.ProviderSignature, err = keys.Sign(s.Key, "deposit-credit", b)
	if err != nil {
		return wire.DepositCredit{}, err
	}
	if err := VerifyCredit(c, c.ProviderID); err != nil {
		return wire.DepositCredit{}, err
	}
	return c, nil
}

// SignBalance signs a provider-local balance status.
func (s Signer) SignBalance(bal wire.BalanceStatus) (wire.BalanceStatus, error) {
	if bal.ProviderID != keys.DIDOf(s.Key) {
		return wire.BalanceStatus{}, errors.New("funding: provider_id does not match signing key")
	}
	encoded, err := canon.EncodeWithout(bal, "provider_signature")
	if err != nil {
		return wire.BalanceStatus{}, err
	}
	bal.ProviderSignature, err = keys.Sign(s.Key, "balance-status", encoded)
	if err != nil {
		return wire.BalanceStatus{}, err
	}
	if err := VerifyBalance(bal, bal.ProviderID); err != nil {
		return wire.BalanceStatus{}, err
	}
	return bal, nil
}

// SignHistory signs one page of an account's history.
func (s Signer) SignHistory(page wire.AccountHistoryPage) (wire.AccountHistoryPage, error) {
	if page.ProviderID != keys.DIDOf(s.Key) {
		return wire.AccountHistoryPage{}, errors.New("funding: provider_id does not match signing key")
	}
	encoded, err := canon.EncodeWithout(page, "provider_signature")
	if err != nil {
		return wire.AccountHistoryPage{}, err
	}
	page.ProviderSignature, err = keys.Sign(s.Key, "account-history", encoded)
	if err != nil {
		return wire.AccountHistoryPage{}, err
	}
	after := uint64(0)
	if len(page.Entries) > 0 {
		after = uint64(page.Entries[0].Sequence) - 1
	}
	if err := VerifyHistory(page, page.ProviderID, after); err != nil {
		return wire.AccountHistoryPage{}, err
	}
	return page, nil
}

// VerifyHistory checks a history page's schema, its provider signature, and that its
// entries continue the sequence directly after the one the reader already holds.
func VerifyHistory(page wire.AccountHistoryPage, providerID string, after uint64) error {
	if err := checkWire("AccountHistoryPage", page); err != nil {
		return err
	}
	if page.ProviderID != providerID {
		return fieldError("provider_id mismatch")
	}
	for i, entry := range page.Entries {
		if uint64(entry.Sequence) != after+uint64(i)+1 {
			return fieldError("history sequence gap at entry %d", i)
		}
		if _, err := wire.ParseTime(entry.At); err != nil {
			return fieldError("history entry time: %v", err)
		}
		if _, err := fee.ParsePositiveAmount(entry.AmountBaseUnits); err != nil {
			return fieldError("history entry amount: %v", err)
		}
	}
	return verifySignature(page, page.ProviderSignature, providerID, "account-history")
}

// SignWithdrawal signs a provider's withdrawal status.
func (s Signer) SignWithdrawal(w wire.WithdrawalStatus) (wire.WithdrawalStatus, error) {
	if w.ProviderID != keys.DIDOf(s.Key) {
		return wire.WithdrawalStatus{}, errors.New("funding: provider_id does not match signing key")
	}
	encoded, err := canon.EncodeWithout(w, "provider_signature")
	if err != nil {
		return wire.WithdrawalStatus{}, err
	}
	w.ProviderSignature, err = keys.Sign(s.Key, "withdrawal-status", encoded)
	if err != nil {
		return wire.WithdrawalStatus{}, err
	}
	if err := VerifyWithdrawal(w, w.ProviderID); err != nil {
		return wire.WithdrawalStatus{}, err
	}
	return w, nil
}

// VerifyInvoice checks the invoice schema, content ID, recipient, expiry and provider signature.
func VerifyInvoice(inv wire.DepositInvoice, providerID string) error {
	if err := checkWire("DepositInvoice", inv); err != nil {
		return err
	}
	if inv.ProviderID != providerID {
		return fieldError("provider_id mismatch")
	}
	if _, err := fee.ParsePositiveAmount(inv.AmountChainUnits); err != nil {
		return fieldError("amount_chain_units: %v", err)
	}
	if err := faux.ValidateRecipient(inv.Network, inv.Recipient); err != nil {
		return fieldError("recipient: %v", err)
	}
	issued, err := wire.ParseTime(inv.IssuedAt)
	if err != nil {
		return fieldError("issued_at: %v", err)
	}
	expires, err := wire.ParseTime(inv.ExpiresAt)
	if err != nil || !expires.After(issued) || expires.After(issued.Add(24*time.Hour)) {
		return fieldError("expires_at: invalid lifetime")
	}
	id, err := DepositID(inv)
	if err != nil || id != inv.DepositID {
		return fieldError("deposit_id mismatch")
	}
	return verifySignature(inv, inv.ProviderSignature, providerID, "deposit-invoice")
}

// VerifyCredit checks the credit schema, content ID, accounting conversion and signature.
func VerifyCredit(c wire.DepositCredit, providerID string) error {
	if err := checkWire("DepositCredit", c); err != nil {
		return err
	}
	if c.ProviderID != providerID {
		return fieldError("provider_id mismatch")
	}
	if c.Transfer.Network != c.Network {
		return fieldError("transfer.network mismatch")
	}
	chain, err := fee.ParsePositiveAmount(c.AmountChainUnits)
	if err != nil {
		return fieldError("amount_chain_units: %v", err)
	}
	accounting, err := fee.AccountingUnits(c.Network, chain)
	if err != nil {
		return fieldError("amount_accounting_units: %v", err)
	}
	accountingAmount, err := fee.ParsePositiveAmount(c.AmountAccountingUnits)
	if err != nil || accounting.Cmp(accountingAmount) != 0 {
		return fieldError("amount_accounting_units mismatch")
	}
	if c.CreditSequence < 1 {
		return fieldError("credit_sequence: must be positive")
	}
	if _, err := wire.ParseTime(c.CreditedAt); err != nil {
		return fieldError("credited_at: %v", err)
	}
	id, err := CreditID(c)
	if err != nil || id != c.CreditID {
		return fieldError("credit_id mismatch")
	}
	return verifySignature(c, c.ProviderSignature, providerID, "deposit-credit")
}

// VerifyBalance checks the signed balance status and its accounting denomination.
func VerifyBalance(b wire.BalanceStatus, providerID string) error {
	if err := checkWire("BalanceStatus", b); err != nil {
		return err
	}
	if b.ProviderID != providerID {
		return fieldError("provider_id mismatch")
	}
	denomination, err := fee.Denomination(b.Network)
	if err != nil || b.Denomination != denomination {
		return fieldError("denomination mismatch")
	}
	if _, err := fee.ParseAmount(b.Available); err != nil {
		return fieldError("available: %v", err)
	}
	if _, err := fee.ParseAmount(b.Frozen); err != nil {
		return fieldError("frozen: %v", err)
	}
	if _, err := fee.ParseAmount(b.Reserved); err != nil {
		return fieldError("reserved: %v", err)
	}
	if _, err := wire.ParseTime(b.UpdatedAt); err != nil {
		return fieldError("updated_at: %v", err)
	}
	return verifySignature(b, b.ProviderSignature, providerID, "balance-status")
}

// VerifyWithdrawal checks the signed withdrawal status and transfer state.
func VerifyWithdrawal(w wire.WithdrawalStatus, providerID string) error {
	if err := checkWire("WithdrawalStatus", w); err != nil {
		return err
	}
	if w.ProviderID != providerID {
		return fieldError("provider_id mismatch")
	}
	if _, err := fee.ParsePositiveAmount(w.AmountBaseUnits); err != nil {
		return fieldError("amount_base_units: %v", err)
	}
	if err := faux.ValidateRecipient(w.Network, w.Recipient); err != nil {
		return fieldError("recipient: %v", err)
	}
	if err := validateStatusTransfer(w.Network, w.Status, w.Transfer); err != nil {
		return err
	}
	if _, err := wire.ParseTime(w.UpdatedAt); err != nil {
		return fieldError("updated_at: %v", err)
	}
	return verifySignature(w, w.ProviderSignature, providerID, "withdrawal-status")
}

func checkWire(def string, value any) error {
	encoded, err := canon.Encode(value)
	if err != nil {
		return err
	}
	parsed, err := canon.Parse(encoded, canon.Limits{MaxDepth: 32})
	if err != nil {
		return err
	}
	return wire.Check(def, parsed)
}

func verifySignature(value any, signature, providerID, label string) error {
	encoded, err := canon.EncodeWithout(value, "provider_signature")
	if err != nil {
		return err
	}
	if err := keys.Verify(providerID, label, encoded, signature); err != nil {
		return fmt.Errorf("provider_signature: %w", err)
	}
	return nil
}

func fieldError(format string, args ...any) error { return fmt.Errorf("funding: "+format, args...) }

func validTypedID(value, prefix string) bool {
	if !strings.HasPrefix(value, prefix) || len(value) != len(prefix)+64 {
		return false
	}
	for _, char := range value[len(prefix):] {
		if !('0' <= char && char <= '9') && !('a' <= char && char <= 'f') {
			return false
		}
	}
	return true
}
