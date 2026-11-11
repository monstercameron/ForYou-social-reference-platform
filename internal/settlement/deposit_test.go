package settlement

import (
	"encoding/hex"
	"math/big"
	"testing"
	"time"

	"foryou.local/foryou/internal/wire"
)

const providerID = "did:key:z6MkhFwXNFWosLeugvSf4wcL9t3uuRXueGSFTRgSvHhWj5G2"

func TestDepositCommitment(t *testing.T) {
	vectors := loadClaimVectors(t)
	commitment, err := DepositCommitment(providerID, wire.BitcoinMainnet)
	if err != nil {
		t.Fatal(err)
	}
	if got := hex.EncodeToString(commitment[:]); got != vectors.CommitmentHex {
		t.Fatalf("bitcoin commitment = %q; want %q", got, vectors.CommitmentHex)
	}
	ethereum, err := DepositCommitment(providerID, wire.EthereumMainnet)
	if err != nil {
		t.Fatal(err)
	}
	if ethereum == commitment {
		t.Fatal("ethereum commitment matched bitcoin commitment")
	}
	t.Run("unsupported network rejected", func(t *testing.T) {
		if _, err := DepositCommitment(providerID, "testnet"); err == nil {
			t.Fatal("DepositCommitment() succeeded on an unsupported network")
		}
	})
}

func TestCountDeposits(t *testing.T) {
	bitcoinCommitment, err := DepositCommitment(providerID, wire.BitcoinMainnet)
	if err != nil {
		t.Fatal(err)
	}
	otherCommitment, err := DepositCommitment("did:key:another-provider", wire.BitcoinMainnet)
	if err != nil {
		t.Fatal(err)
	}
	commitmentData := bitcoinCommitment[:]
	validRecipient := func(network, address string, at time.Time) bool {
		return network == wire.BitcoinMainnet && address == "foundation-recipient" && !at.Before(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) && at.Before(time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC))
	}
	count := func(t *testing.T, transfers []Transfer, recipient RecipientCounts, want string) {
		t.Helper()
		got, err := CountDeposits(transfers, providerID, wire.BitcoinMainnet, recipient)
		if err != nil {
			t.Fatalf("CountDeposits() error = %v", err)
		}
		if got.String() != want {
			t.Fatalf("CountDeposits() = %s; want %s", got, want)
		}
	}
	valid := Transfer{Network: wire.BitcoinMainnet, Recipient: "foundation-recipient", ChainUnits: big.NewInt(10000), DataOutputs: [][]byte{commitmentData}, Final: true, Successful: true, BlockTime: time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)}
	t.Run("minimum bitcoin transfer counts in millisatoshis", func(t *testing.T) { count(t, []Transfer{valid}, validRecipient, "10000000") })
	t.Run("subminimum transfer is ignored", func(t *testing.T) {
		transfer := valid
		transfer.ChainUnits = big.NewInt(9999)
		count(t, []Transfer{transfer}, validRecipient, "0")
	})
	t.Run("another provider commitment is ignored", func(t *testing.T) {
		transfer := valid
		transfer.DataOutputs = [][]byte{otherCommitment[:]}
		count(t, []Transfer{transfer}, validRecipient, "0")
	})
	t.Run("another recipient is ignored", func(t *testing.T) {
		transfer := valid
		transfer.Recipient = "other-recipient"
		count(t, []Transfer{transfer}, validRecipient, "0")
	})
	t.Run("wrong commitment bytes are ignored", func(t *testing.T) {
		transfer := valid
		transfer.DataOutputs = [][]byte{[]byte("wrong")}
		count(t, []Transfer{transfer}, validRecipient, "0")
	})
	t.Run("multiple bitcoin data outputs are ignored", func(t *testing.T) {
		transfer := valid
		transfer.DataOutputs = [][]byte{commitmentData, commitmentData}
		count(t, []Transfer{transfer}, validRecipient, "0")
	})
	t.Run("nonfinal transfer is ignored", func(t *testing.T) {
		transfer := valid
		transfer.Final = false
		count(t, []Transfer{transfer}, validRecipient, "0")
	})
	t.Run("failed transfer is ignored", func(t *testing.T) {
		transfer := valid
		transfer.Successful = false
		count(t, []Transfer{transfer}, validRecipient, "0")
	})
	t.Run("before certificate validity is ignored", func(t *testing.T) {
		transfer := valid
		transfer.BlockTime = time.Date(2026, 11, 30, 23, 59, 59, 0, time.UTC)
		count(t, []Transfer{transfer}, validRecipient, "0")
	})
	t.Run("at closure time is ignored", func(t *testing.T) {
		transfer := valid
		transfer.BlockTime = time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC)
		count(t, []Transfer{transfer}, validRecipient, "0")
	})
	t.Run("ethereum minimum counts in wei", func(t *testing.T) {
		ethereumCommitment, err := DepositCommitment(providerID, wire.EthereumMainnet)
		if err != nil {
			t.Fatal(err)
		}
		transfer := Transfer{Network: wire.EthereumMainnet, Recipient: "foundation-recipient", ChainUnits: new(big.Int).Exp(big.NewInt(10), big.NewInt(15), nil), DataOutputs: [][]byte{ethereumCommitment[:]}, Final: true, Successful: true}
		got, err := CountDeposits([]Transfer{transfer}, providerID, wire.EthereumMainnet, func(string, string, time.Time) bool { return true })
		if err != nil || got.String() != "1000000000000000" {
			t.Fatalf("CountDeposits() = %v, %v; want 1000000000000000", got, err)
		}
	})
	t.Run("ethereum contract recipient is ignored", func(t *testing.T) {
		ethereumCommitment, err := DepositCommitment(providerID, wire.EthereumMainnet)
		if err != nil {
			t.Fatal(err)
		}
		transfer := Transfer{Network: wire.EthereumMainnet, ChainUnits: new(big.Int).Exp(big.NewInt(10), big.NewInt(15), nil), DataOutputs: [][]byte{ethereumCommitment[:]}, RecipientHasCode: true, Final: true, Successful: true}
		got, err := CountDeposits([]Transfer{transfer}, providerID, wire.EthereumMainnet, func(string, string, time.Time) bool { return true })
		if err != nil || got.Sign() != 0 {
			t.Fatalf("CountDeposits() = %v, %v; want zero", got, err)
		}
	})
	t.Run("unsupported network is error", func(t *testing.T) {
		if _, err := CountDeposits(nil, providerID, "testnet", nil); err == nil {
			t.Fatal("CountDeposits() succeeded on an unsupported network")
		}
	})
}

func TestBaseUnitsAndMinimum(t *testing.T) {
	t.Run("bitcoin minimum", func(t *testing.T) {
		got, err := MinimumDeposit(wire.BitcoinMainnet)
		if err != nil || got.String() != "10000" {
			t.Fatalf("MinimumDeposit(bitcoin) = %v, %v; want 10000", got, err)
		}
	})
	t.Run("ethereum minimum", func(t *testing.T) {
		got, err := MinimumDeposit(wire.EthereumMainnet)
		if err != nil || got.String() != "1000000000000000" {
			t.Fatalf("MinimumDeposit(ethereum) = %v, %v; want 1000000000000000", got, err)
		}
	})
	t.Run("bitcoin satoshis convert to millisatoshis", func(t *testing.T) {
		got, err := BaseUnits(wire.BitcoinMainnet, big.NewInt(10000))
		if err != nil || got.String() != "10000000" {
			t.Fatalf("BaseUnits(bitcoin) = %v, %v; want 10000000", got, err)
		}
	})
	t.Run("ethereum wei remains unchanged", func(t *testing.T) {
		got, err := BaseUnits(wire.EthereumMainnet, big.NewInt(15))
		if err != nil || got.String() != "15" {
			t.Fatalf("BaseUnits(ethereum) = %v, %v; want 15", got, err)
		}
	})
}

func TestCredit(t *testing.T) {
	vectors := loadClaimVectors(t)
	credit, err := Credit(big.NewInt(10000000), vectors.SettlementClaim)
	if err != nil || credit.String() != "9989528" {
		t.Fatalf("Credit() = %v, %v; want 9989528", credit, err)
	}
	credit, err = Credit(big.NewInt(1000), vectors.SettlementClaim)
	if err != nil || credit.String() != "-9472" {
		t.Fatalf("Credit() = %v, %v; want -9472", credit, err)
	}
}
