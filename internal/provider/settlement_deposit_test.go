package provider

import (
	"context"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

type settlementRefusingChain struct{ faux.Chain }

func (settlementRefusingChain) Submit(context.Context, faux.SubmitRequest) (faux.Transfer, error) {
	return faux.Transfer{}, errors.New("chain unavailable")
}

func settlementDeposits(t *testing.T, s *Service) []faux.Transfer {
	t.Helper()
	commitment, err := settlement.DepositCommitment(s.ProviderID(), wire.BitcoinMainnet)
	if err != nil {
		t.Fatal(err)
	}
	transfers, err := s.Chain.Transfers(context.Background(), faux.Query{Network: wire.BitcoinMainnet, Data: hex.EncodeToString(commitment[:]), Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	return transfers
}

func TestSettlementClaimIsStoredOnlyWhenFunded(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	s, _ := settlementTestService(t, &now, "")
	defer s.Close()
	settlementFundAuthor(t, s, now)
	settlementAdmit(t, s, time.Date(2026, 11, 11, 12, 0, 10, 0, time.UTC), 40001)
	now = time.Date(2026, 11, 11, 12, 3, 0, 0, time.UTC)
	chain := s.Chain

	s.Chain = settlementRefusingChain{chain}
	if err := s.RunOnce(context.Background()); err == nil {
		t.Error("RunOnce with an unfunded claim and no deposit reported no error")
	}
	if claims, err := s.Settlement().Claims(wire.BitcoinMainnet, 0, 10); err != nil || len(claims) != 0 {
		t.Fatalf("claims without a deposit = %d, %v; want 0", len(claims), err)
	}

	s.Chain = chain
	claims := settlementRun(t, s)
	if len(claims) != 1 {
		t.Fatalf("claims after the deposit = %d, want 1", len(claims))
	}
	deposits := settlementDeposits(t, s)
	if len(deposits) != 1 {
		t.Fatalf("deposits = %d, want 1", len(deposits))
	}
	if want := devnet.Recipient(devnet.Foundation0Label, wire.BitcoinMainnet); deposits[0].Recipient != want {
		t.Errorf("deposit recipient = %s, want the Foundation's %s", deposits[0].Recipient, want)
	}
	// 136 msat claimed plus 100 fees of 1 KiB (13,600 msat) is 14 sat, raised to the 10,000 sat minimum.
	if deposits[0].AmountChainUnits != "10000" || !deposits[0].Finalized {
		t.Errorf("deposit = %s sat, final %t; want 10000 sat, final", deposits[0].AmountChainUnits, deposits[0].Finalized)
	}
	if len(settlementRun(t, s)) != 1 || len(settlementDeposits(t, s)) != 1 {
		t.Error("a second run published or deposited again")
	}
}

func TestSettlementPublisherUsesExistingDeposit(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	s, _ := settlementTestService(t, &now, "")
	defer s.Close()
	settlementFundAuthor(t, s, now)
	commitment, err := settlement.DepositCommitment(s.ProviderID(), wire.BitcoinMainnet)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Chain.Submit(context.Background(), faux.SubmitRequest{Network: wire.BitcoinMainnet, Recipient: devnet.Recipient(devnet.Foundation0Label, wire.BitcoinMainnet), AmountChainUnits: "10000", Data: hex.EncodeToString(commitment[:])}); err != nil {
		t.Fatal(err)
	}
	settlementAdmit(t, s, time.Date(2026, 11, 11, 12, 0, 10, 0, time.UTC), 40002)

	now = time.Date(2026, 11, 11, 12, 3, 0, 0, time.UTC)
	if claims := settlementRun(t, s); len(claims) != 1 {
		t.Fatalf("claims = %d, want 1", len(claims))
	}
	if deposits := settlementDeposits(t, s); len(deposits) != 1 {
		t.Errorf("deposits = %d; want only the existing one", len(deposits))
	}
}
