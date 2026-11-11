package funding

import (
	"bytes"
	"errors"
	"fmt"
	"math/big"
	"time"

	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

var (
	// ErrBinding identifies an author or provider binding mismatch.
	ErrBinding = errors.New("funding: author or provider binding mismatch")
	// ErrWindow identifies a request deadline outside its permitted window.
	ErrWindow = errors.New("funding: request deadline outside permitted window")
	// ErrInvalidRequest identifies a structurally or semantically invalid request.
	ErrInvalidRequest = errors.New("funding: invalid request")
)

// ValidateDepositRequest checks a new deposit request against its authenticated identities and deadline.
func ValidateDepositRequest(req wire.DepositRequest, providerID, authorID string, now time.Time) error {
	if err := checkWire("DepositRequest", req); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if req.ProviderID != providerID || req.AuthorID != authorID {
		return ErrBinding
	}
	if _, err := fee.ParsePositiveAmount(req.AmountChainUnits); err != nil {
		return fmt.Errorf("%w: amount_chain_units", ErrInvalidRequest)
	}
	if err := canonicalNonce(req.RequestID); err != nil {
		return fmt.Errorf("%w: request_id", ErrInvalidRequest)
	}
	deadline, err := wire.ParseTime(req.NotAfter)
	if err != nil {
		return fmt.Errorf("%w: not_after", ErrInvalidRequest)
	}
	if !deadline.After(now) || deadline.After(now.Add(time.Minute)) {
		return ErrWindow
	}
	return nil
}

// ValidateDepositProof validates an untrusted chain lookup hint, not evidence of payment.
func ValidateDepositProof(p wire.DepositProof, providerID, authorID string) error {
	if err := checkWire("DepositProof", p); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if p.ProviderID != providerID || p.AuthorID != authorID {
		return ErrBinding
	}
	if !validTypedID(p.DepositID, "fydi2:") {
		return fmt.Errorf("%w: deposit_id", ErrInvalidRequest)
	}
	if err := validateTransfer(p.Transfer); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	return nil
}

// ValidateWithdrawalRequest checks a new withdrawal's bindings, amount, nonce and recipient.
func ValidateWithdrawalRequest(req wire.WithdrawalRequest, providerID, authorID string) error {
	if err := checkWire("WithdrawalRequest", req); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidRequest, err)
	}
	if req.ProviderID != providerID || req.AuthorID != authorID {
		return ErrBinding
	}
	amount, err := fee.ParsePositiveAmount(req.AmountBaseUnits)
	if err != nil {
		return fmt.Errorf("%w: amount_base_units", ErrInvalidRequest)
	}
	if err := canonicalNonce(req.RequestID); err != nil {
		return fmt.Errorf("%w: request_id", ErrInvalidRequest)
	}
	if err := faux.ValidateRecipient(req.Network, req.Recipient); err != nil {
		return fmt.Errorf("%w: recipient", ErrInvalidRequest)
	}
	if req.Network == wire.BitcoinMainnet && new(big.Int).Mod(amount, big.NewInt(1000)).Sign() != 0 {
		return fmt.Errorf("%w: bitcoin withdrawals must be whole satoshis", ErrInvalidRequest)
	}
	return nil
}

// SameRecipient compares recipients by their actual Bitcoin output scripts.
func SameRecipient(network, a, b string) (bool, error) {
	if err := faux.ValidateRecipient(network, a); err != nil {
		return false, err
	}
	if err := faux.ValidateRecipient(network, b); err != nil {
		return false, err
	}
	if network == wire.BitcoinMainnet {
		left, err := faux.BitcoinScript(a)
		if err != nil {
			return false, err
		}
		right, err := faux.BitcoinScript(b)
		if err != nil {
			return false, err
		}
		return bytes.Equal(left, right), nil
	}
	return a == b, nil
}

// CheckInvoice verifies the pinned provider's invoice against the approved request.
func CheckInvoice(inv wire.DepositInvoice, req wire.DepositRequest, providerID string, now time.Time) error {
	if err := VerifyInvoice(inv, providerID); err != nil {
		return err
	}
	if inv.ProviderID != providerID || inv.ProviderID != req.ProviderID {
		return fieldError("provider_id mismatch")
	}
	if inv.AuthorID != req.AuthorID {
		return fieldError("author_id mismatch")
	}
	if inv.Network != req.Network {
		return fieldError("network mismatch")
	}
	if inv.AmountChainUnits != req.AmountChainUnits {
		return fieldError("amount_chain_units mismatch")
	}
	expires, err := wire.ParseTime(inv.ExpiresAt)
	if err != nil || !now.Before(expires) {
		return fieldError("expires_at: invoice expired")
	}
	return nil
}

func canonicalNonce(value string) error {
	_, err := keys.DecodeB64URL(value, 32)
	return err
}

func validateTransfer(transfer wire.DepositTransfer) error {
	if err := checkWire("DepositTransfer", transfer); err != nil {
		return err
	}
	if transfer.Network != wire.BitcoinMainnet && transfer.Network != wire.EthereumMainnet {
		return errors.New("transfer network")
	}
	return nil
}

func validateStatusTransfer(network, status string, transfer *wire.DepositTransfer) error {
	switch status {
	case "pending", "failed":
		if transfer != nil {
			return fieldError("transfer must be null unless status is sent")
		}
	case "sent":
		if transfer == nil {
			return fieldError("transfer required for sent status")
		}
		if transfer.Network != network {
			return fieldError("transfer.network mismatch")
		}
		if err := validateTransfer(*transfer); err != nil {
			return fieldError("transfer: %v", err)
		}
	default:
		return fieldError("status invalid")
	}
	return nil
}
