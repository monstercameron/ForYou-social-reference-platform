package funding

import (
	"errors"
	"testing"
	"time"

	"foryou.local/foryou/internal/wire"
)

func TestValidateDepositRequest(t *testing.T) {
	vectors := readFundingVectors(t)
	providerID, authorID := vectors.Invoice.ProviderID, vectors.Invoice.AuthorID
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	base := wire.DepositRequest{ProviderID: providerID, AuthorID: authorID, Network: wire.BitcoinMainnet, AmountChainUnits: "50000", RequestID: "IdDapjW_HBS7FYQt7K18DMXgeyN2RI-kNqwRzcC67N8", NotAfter: "2026-11-11T00:01:00Z"}
	t.Run("maximum deadline accepted", func(t *testing.T) {
		if err := ValidateDepositRequest(base, providerID, authorID, now); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("equal deadline rejected", func(t *testing.T) {
		request := base
		request.NotAfter = "2026-11-11T00:00:00Z"
		if err := ValidateDepositRequest(request, providerID, authorID, now); !errors.Is(err, ErrWindow) {
			t.Fatalf("ValidateDepositRequest() error = %v; want ErrWindow", err)
		}
	})
	t.Run("deadline beyond window rejected", func(t *testing.T) {
		request := base
		request.NotAfter = "2026-11-11T00:01:01Z"
		if err := ValidateDepositRequest(request, providerID, authorID, now); !errors.Is(err, ErrWindow) {
			t.Fatalf("ValidateDepositRequest() error = %v; want ErrWindow", err)
		}
	})
	t.Run("author binding rejected", func(t *testing.T) {
		if err := ValidateDepositRequest(base, providerID, providerID, now); !errors.Is(err, ErrBinding) {
			t.Fatalf("ValidateDepositRequest() error = %v; want ErrBinding", err)
		}
	})
	t.Run("noncanonical nonce rejected", func(t *testing.T) {
		request := base
		request.RequestID = "IdDapjW_HBS7FYQt7K18DMXgeyN2RI-kNqwRzcC67N9"
		if err := ValidateDepositRequest(request, providerID, authorID, now); !errors.Is(err, ErrInvalidRequest) {
			t.Fatal("ValidateDepositRequest accepted a noncanonical nonce")
		}
	})
	t.Run("zero amount rejected", func(t *testing.T) {
		request := base
		request.AmountChainUnits = "0"
		if err := ValidateDepositRequest(request, providerID, authorID, now); !errors.Is(err, ErrInvalidRequest) {
			t.Fatal("ValidateDepositRequest accepted a zero amount")
		}
	})
}

func TestValidateDepositProof(t *testing.T) {
	vectors := readFundingVectors(t)
	proof := wire.DepositProof{ProviderID: vectors.Invoice.ProviderID, AuthorID: vectors.Invoice.AuthorID, DepositID: vectors.Invoice.DepositID, Transfer: vectors.Credit.Transfer}
	t.Run("valid untrusted hint accepted", func(t *testing.T) {
		if err := ValidateDepositProof(proof, proof.ProviderID, proof.AuthorID); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("cross-network transfer rejected", func(t *testing.T) {
		mismatched := proof
		mismatched.Transfer.Network = wire.EthereumMainnet
		if err := ValidateDepositProof(mismatched, proof.ProviderID, proof.AuthorID); !errors.Is(err, ErrInvalidRequest) {
			t.Fatal("ValidateDepositProof accepted a Bitcoin transfer with Ethereum network")
		}
	})
}

func TestValidateWithdrawalRequest(t *testing.T) {
	vectors := readFundingVectors(t)
	base := wire.WithdrawalRequest{ProviderID: vectors.Withdrawal.ProviderID, AuthorID: vectors.Withdrawal.AuthorID, Network: wire.BitcoinMainnet, AmountBaseUnits: "2000", Recipient: "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4", RequestID: "IdDapjW_HBS7FYQt7K18DMXgeyN2RI-kNqwRzcC67N8"}
	t.Run("whole satoshi withdrawal accepted", func(t *testing.T) {
		if err := ValidateWithdrawalRequest(base, base.ProviderID, base.AuthorID); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("fractional satoshi withdrawal rejected", func(t *testing.T) {
		request := base
		request.AmountBaseUnits = "1500"
		if err := ValidateWithdrawalRequest(request, base.ProviderID, base.AuthorID); !errors.Is(err, ErrInvalidRequest) {
			t.Fatal("ValidateWithdrawalRequest accepted fractional satoshis")
		}
	})
	t.Run("bad Bitcoin checksum rejected", func(t *testing.T) {
		request := base
		request.Recipient = "bc1qw508d4ejxtdg4y5r3zarvary0c5xw7kv8f3t4"
		if err := ValidateWithdrawalRequest(request, base.ProviderID, base.AuthorID); !errors.Is(err, ErrInvalidRequest) {
			t.Fatal("ValidateWithdrawalRequest accepted a bad address checksum")
		}
	})
	t.Run("uppercase Ethereum address rejected", func(t *testing.T) {
		request := base
		request.Network = wire.EthereumMainnet
		request.Recipient = "0x71db08fc23fc139f388b9825f1e2d4a211872B22"
		if err := ValidateWithdrawalRequest(request, base.ProviderID, base.AuthorID); !errors.Is(err, ErrInvalidRequest) {
			t.Fatal("ValidateWithdrawalRequest accepted uppercase Ethereum hex")
		}
	})
}

func TestSameRecipient(t *testing.T) {
	t.Run("same Bitcoin script matches", func(t *testing.T) {
		equal, err := SameRecipient(wire.BitcoinMainnet, "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4", "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4")
		if err != nil || !equal {
			t.Fatalf("SameRecipient() = %v, %v; want true", equal, err)
		}
	})
	t.Run("different Bitcoin scripts differ", func(t *testing.T) {
		equal, err := SameRecipient(wire.BitcoinMainnet, "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4", "1BvBMSEYstWetqTFn5Au4m4GFg7xJaNVN2")
		if err != nil || equal {
			t.Fatalf("SameRecipient() = %v, %v; want false", equal, err)
		}
	})
	t.Run("invalid address returns error", func(t *testing.T) {
		if _, err := SameRecipient(wire.BitcoinMainnet, "not-an-address", "not-an-address"); err == nil {
			t.Fatal("SameRecipient accepted an invalid address")
		}
	})
}

func TestCheckInvoice(t *testing.T) {
	vectors := readFundingVectors(t)
	base := wire.DepositRequest{ProviderID: vectors.Invoice.ProviderID, AuthorID: vectors.Invoice.AuthorID, Network: vectors.Invoice.Network, AmountChainUnits: vectors.Invoice.AmountChainUnits}
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	t.Run("matching signed invoice accepted", func(t *testing.T) {
		if err := CheckInvoice(vectors.Invoice, base, base.ProviderID, now); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("network mismatch names field", func(t *testing.T) {
		request := base
		request.Network = wire.EthereumMainnet
		if err := CheckInvoice(vectors.Invoice, request, base.ProviderID, now); err == nil || err.Error() != "funding: network mismatch" {
			t.Fatalf("CheckInvoice() error = %v; want network mismatch", err)
		}
	})
	t.Run("amount mismatch names field", func(t *testing.T) {
		request := base
		request.AmountChainUnits = "50001"
		if err := CheckInvoice(vectors.Invoice, request, base.ProviderID, now); err == nil || err.Error() != "funding: amount_chain_units mismatch" {
			t.Fatalf("CheckInvoice() error = %v; want amount mismatch", err)
		}
	})
	t.Run("author mismatch names field", func(t *testing.T) {
		request := base
		request.AuthorID = request.ProviderID
		if err := CheckInvoice(vectors.Invoice, request, base.ProviderID, now); err == nil || err.Error() != "funding: author_id mismatch" {
			t.Fatalf("CheckInvoice() error = %v; want author mismatch", err)
		}
	})
	t.Run("expired invoice rejected", func(t *testing.T) {
		if err := CheckInvoice(vectors.Invoice, base, base.ProviderID, time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)); err == nil || err.Error() != "funding: expires_at: invoice expired" {
			t.Fatalf("CheckInvoice() error = %v; want expires_at error", err)
		}
	})
}
