package funding

import (
	"encoding/json"
	"os"
	"testing"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

type fundingVectors struct {
	Invoice    wire.DepositInvoice   `json:"invoice"`
	Credit     wire.DepositCredit    `json:"credit"`
	Balance    wire.BalanceStatus    `json:"balance"`
	Withdrawal wire.WithdrawalStatus `json:"withdrawal"`
}

func readFundingVectors(t *testing.T) fundingVectors {
	t.Helper()
	data, err := os.ReadFile("testdata/vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors fundingVectors
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	return vectors
}

func providerSigner(t *testing.T) Signer {
	t.Helper()
	key, err := keys.NewKeyFromSeed([]byte{32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63})
	if err != nil {
		t.Fatal(err)
	}
	return Signer{Key: key}
}

func resignInvoice(t *testing.T, invoice wire.DepositInvoice) wire.DepositInvoice {
	t.Helper()
	signer := providerSigner(t)
	var err error
	invoice.DepositID, err = DepositID(invoice)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := canon.EncodeWithout(invoice, "provider_signature")
	if err != nil {
		t.Fatal(err)
	}
	invoice.ProviderSignature, err = keys.Sign(signer.Key, "deposit-invoice", encoded)
	if err != nil {
		t.Fatal(err)
	}
	return invoice
}

func TestFundingVectors(t *testing.T) {
	vectors := readFundingVectors(t)
	t.Run("invoice verifies and reseals byte for byte", func(t *testing.T) {
		if err := VerifyInvoice(vectors.Invoice, vectors.Invoice.ProviderID); err != nil {
			t.Fatal(err)
		}
		want := vectors.Invoice
		want.DepositID, want.ProviderSignature = "", ""
		got, err := providerSigner(t).SealInvoice(want)
		if err != nil || got != vectors.Invoice {
			t.Fatalf("SealInvoice() = %#v, %v; want vector", got, err)
		}
	})
	t.Run("credit verifies and reseals byte for byte", func(t *testing.T) {
		if err := VerifyCredit(vectors.Credit, vectors.Credit.ProviderID); err != nil {
			t.Fatal(err)
		}
		want := vectors.Credit
		want.CreditID, want.ProviderSignature = "", ""
		got, err := providerSigner(t).SealCredit(want)
		if err != nil || got.CreditID != vectors.Credit.CreditID || got.ProviderSignature != vectors.Credit.ProviderSignature {
			t.Fatalf("SealCredit() = %#v, %v; want vector", got, err)
		}
	})
	t.Run("balance verifies and resigns byte for byte", func(t *testing.T) {
		if err := VerifyBalance(vectors.Balance, vectors.Balance.ProviderID); err != nil {
			t.Fatal(err)
		}
		want := vectors.Balance
		want.ProviderSignature = ""
		got, err := providerSigner(t).SignBalance(want)
		if err != nil || got != vectors.Balance {
			t.Fatalf("SignBalance() = %#v, %v; want vector", got, err)
		}
	})
	t.Run("withdrawal verifies and resigns byte for byte", func(t *testing.T) {
		if err := VerifyWithdrawal(vectors.Withdrawal, vectors.Withdrawal.ProviderID); err != nil {
			t.Fatal(err)
		}
		want := vectors.Withdrawal
		want.ProviderSignature = ""
		got, err := providerSigner(t).SignWithdrawal(want)
		if err != nil || got != vectors.Withdrawal {
			t.Fatalf("SignWithdrawal() = %#v, %v; want vector", got, err)
		}
	})
}

func TestFundingRejectsTampering(t *testing.T) {
	vectors := readFundingVectors(t)
	t.Run("invoice amount changes signature and ID", func(t *testing.T) {
		invoice := vectors.Invoice
		invoice.AmountChainUnits = "50001"
		if err := VerifyInvoice(invoice, invoice.ProviderID); err == nil {
			t.Fatal("VerifyInvoice accepted a changed amount")
		}
	})
	t.Run("credit accounting conversion is exact", func(t *testing.T) {
		credit := vectors.Credit
		credit.AmountAccountingUnits = "50000"
		var err error
		credit.CreditID, err = CreditID(credit)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := canon.EncodeWithout(credit, "provider_signature")
		if err != nil {
			t.Fatal(err)
		}
		credit.ProviderSignature, err = keys.Sign(providerSigner(t).Key, "deposit-credit", encoded)
		if err != nil {
			t.Fatal(err)
		}
		if err := VerifyCredit(credit, credit.ProviderID); err == nil {
			t.Fatal("VerifyCredit accepted an incorrect conversion")
		}
	})
	t.Run("credit transfer network must match", func(t *testing.T) {
		credit := vectors.Credit
		credit.Transfer.Network = wire.EthereumMainnet
		if err := VerifyCredit(credit, credit.ProviderID); err == nil {
			t.Fatal("VerifyCredit accepted a cross-network transfer")
		}
	})
	t.Run("balance denomination follows network", func(t *testing.T) {
		balance := vectors.Balance
		balance.Denomination = "wei"
		if err := VerifyBalance(balance, balance.ProviderID); err == nil {
			t.Fatal("VerifyBalance accepted the wrong denomination")
		}
	})
	t.Run("sent withdrawal requires a transfer", func(t *testing.T) {
		withdrawal := vectors.Withdrawal
		withdrawal.Status = "sent"
		withdrawal = resignWithdrawal(t, withdrawal)
		if err := VerifyWithdrawal(withdrawal, withdrawal.ProviderID); err == nil {
			t.Fatal("VerifyWithdrawal accepted a sent status without transfer")
		}
	})
	t.Run("pending withdrawal rejects a transfer", func(t *testing.T) {
		withdrawal := vectors.Withdrawal
		transfer := wire.DepositTransfer{Network: wire.EthereumMainnet, TxHash: "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"}
		withdrawal.Transfer = &transfer
		withdrawal = resignWithdrawal(t, withdrawal)
		if err := VerifyWithdrawal(withdrawal, withdrawal.ProviderID); err == nil {
			t.Fatal("VerifyWithdrawal accepted a transfer before sending")
		}
	})
}

func resignWithdrawal(t *testing.T, withdrawal wire.WithdrawalStatus) wire.WithdrawalStatus {
	t.Helper()
	encoded, err := canon.EncodeWithout(withdrawal, "provider_signature")
	if err != nil {
		t.Fatal(err)
	}
	withdrawal.ProviderSignature, err = keys.Sign(providerSigner(t).Key, "withdrawal-status", encoded)
	if err != nil {
		t.Fatal(err)
	}
	return withdrawal
}

func TestDepositExpiryBound(t *testing.T) {
	base := readFundingVectors(t).Invoice
	t.Run("expiry at 24 hours accepted", func(t *testing.T) {
		invoice := base
		invoice.ExpiresAt = "2026-11-12T00:00:00Z"
		invoice = resignInvoice(t, invoice)
		if err := VerifyInvoice(invoice, invoice.ProviderID); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("expiry beyond 24 hours rejected", func(t *testing.T) {
		invoice := base
		invoice.ExpiresAt = "2026-11-12T00:00:01Z"
		invoice = resignInvoice(t, invoice)
		if err := VerifyInvoice(invoice, invoice.ProviderID); err == nil {
			t.Fatal("VerifyInvoice accepted expiry beyond 24 hours")
		}
	})
}

func TestSignerRejectsDifferentProvider(t *testing.T) {
	vector := readFundingVectors(t).Invoice
	vector.ProviderID = vector.AuthorID
	if _, err := providerSigner(t).SealInvoice(vector); err == nil {
		t.Fatal("SealInvoice accepted a provider ID for another key")
	}
}
