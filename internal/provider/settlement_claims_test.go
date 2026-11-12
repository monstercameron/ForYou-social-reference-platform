package provider

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math/big"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/funding"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/retention"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

type settlementTestSealer struct{}

func (settlementTestSealer) SealInvoice(v wire.DepositInvoice) (wire.DepositInvoice, error) {
	v.DepositID = "fydi2:" + fmt.Sprintf("%064x", 1)
	v.ProviderSignature = "test"
	return v, nil
}

func (settlementTestSealer) SealCredit(v wire.DepositCredit) (wire.DepositCredit, error) {
	v.CreditID = "fydc2:" + fmt.Sprintf("%064x", 1)
	v.ProviderSignature = "test"
	return v, nil
}

func settlementTestService(t *testing.T, now *time.Time, dataDir string) (*Service, Config) {
	t.Helper()
	cfg, err := TestConfig(func() time.Time { return *now })
	if err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = dataDir
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return s, cfg
}

func settlementFundAuthor(t *testing.T, s *Service, now time.Time) {
	t.Helper()
	authorID := keys.DIDOf(devnet.ReplicaKey())
	request := wire.DepositRequest{ProviderID: s.ProviderID(), AuthorID: authorID, Network: wire.BitcoinMainnet, AmountChainUnits: "10000", RequestID: "settlement-test-funding", NotAfter: wire.FormatTime(now.Add(time.Hour))}
	invoice, _, err := s.Ledger.IssueInvoice(request, big.NewInt(1000000000), settlementTestSealer{})
	if err != nil {
		t.Fatal(err)
	}
	transfer, err := s.Chain.Submit(context.Background(), faux.SubmitRequest{Network: wire.BitcoinMainnet, Recipient: invoice.Recipient, AmountChainUnits: "10000"})
	if err != nil {
		t.Fatal(err)
	}
	index := int64(*transfer.OutputIndex)
	proof := wire.DepositProof{ProviderID: s.ProviderID(), AuthorID: authorID, DepositID: invoice.DepositID, Transfer: wire.DepositTransfer{Network: transfer.Network, TxID: transfer.TxID, OutputIndex: &index}}
	if _, _, err := s.Ledger.Observe(context.Background(), proof, s.Chain, settlementTestSealer{}); err != nil {
		t.Fatal(err)
	}
}

// settlementSignReceipt signs as fee.SignReceipt does, without its strict self-check,
// so the split test can issue 10,001 receipts quickly.
func settlementSignReceipt(s *Service) func(wire.SubmissionReceipt) (wire.SubmissionReceipt, error) {
	return func(r wire.SubmissionReceipt) (wire.SubmissionReceipt, error) {
		b, err := canon.EncodeWithout(r, "provider_signature")
		if err != nil {
			return r, err
		}
		r.ProviderSignature, err = keys.Sign(s.Key(), "receipt", b)
		return r, err
	}
}

func settlementAdmit(t *testing.T, s *Service, at time.Time, index int) wire.SubmissionReceipt {
	t.Helper()
	return settlementAdmitMessage(t, s, at, fmt.Sprintf("fy1:%064x", index+1), index)
}

func settlementAdmitMessage(t *testing.T, s *Service, at time.Time, messageID string, index int) wire.SubmissionReceipt {
	t.Helper()
	schedule := s.FeeSchedules()[0]
	quoteID := fmt.Sprintf("fyfq2:%064x", index+1)
	quote := wire.FeeQuote{ProviderID: s.ProviderID(), AuthorID: keys.DIDOf(devnet.ReplicaKey()), MessageID: messageID, ScheduleID: schedule.ScheduleID, QuoteID: quoteID, BillableBytes: 1, Network: wire.BitcoinMainnet, AmountBaseUnits: "136", PreflightAt: wire.FormatTime(at), ExpiresAt: wire.FormatTime(at.Add(5 * time.Minute)), ProviderSignature: "test"}
	draft := funding.AdmissionDraft{Request: wire.AdmissionRequest{ProviderID: s.ProviderID(), Authorization: "test", QuoteID: quoteID}, AuthorID: quote.AuthorID, MessageID: messageID, Quote: quote, At: at}
	result, _, err := s.Ledger.Admit(context.Background(), draft, func(time.Time) error { return nil }, settlementSignReceipt(s))
	if err != nil {
		t.Fatal(err)
	}
	return result.Receipt
}

func settlementRun(t *testing.T, s *Service) []wire.SettlementClaim {
	t.Helper()
	if err := s.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	claims, err := s.Settlement().Claims(wire.BitcoinMainnet, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	return claims
}

func settlementCheckStored(t *testing.T, s *Service, claim wire.SettlementClaim) {
	t.Helper()
	if err := settlement.VerifyClaimObject(claim); err != nil {
		t.Errorf("claim %d signature: %v", claim.Sequence, err)
	}
	leaves, err := s.Settlement().Leaves(claim.ClaimID, 0, 2000)
	if err != nil {
		t.Fatal(err)
	}
	if err := settlement.CheckTotals(claim, leaves); err != nil {
		t.Errorf("claim %d totals: %v", claim.Sequence, err)
	}
}

func settlementCheckLocated(t *testing.T, s *Service, receipt wire.SubmissionReceipt, claimID string) {
	t.Helper()
	got, _, ok, err := s.Settlement().Locate(receipt.ReceiptID)
	if err != nil || !ok || got != claimID {
		t.Errorf("Locate(%s) = %q, %t, %v; want claim %s", receipt.ReceiptID, got, ok, err, claimID)
	}
}

func TestSettlementPublisherWaitsForPeriodEnd(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	s, _ := settlementTestService(t, &now, "")
	defer s.Close()
	settlementFundAuthor(t, s, now)
	receipt := settlementAdmit(t, s, time.Date(2026, 11, 11, 12, 0, 10, 0, time.UTC), 1)

	now = time.Date(2026, 11, 11, 12, 0, 50, 0, time.UTC)
	if claims := settlementRun(t, s); len(claims) != 0 {
		t.Fatalf("claims before the period end = %d, want 0", len(claims))
	}
	now = time.Date(2026, 11, 11, 12, 3, 0, 0, time.UTC)
	claims := settlementRun(t, s)
	if len(claims) != 1 {
		t.Fatalf("claims after the period = %d, want 1", len(claims))
	}
	if claims[0].PeriodStart != "2026-11-11T12:00:00Z" || claims[0].PeriodEnd != "2026-11-11T12:01:00Z" || claims[0].ReceiptCount != 1 || claims[0].TotalBaseUnits != "136" || claims[0].SuccessionID != nil {
		t.Errorf("claim = %+v; want [12:00, 12:01) with one 136 msat receipt and no succession", claims[0])
	}
	settlementCheckStored(t, s, claims[0])
	settlementCheckLocated(t, s, receipt, claims[0].ClaimID)
}

func TestSettlementPublisherStartsFirstClaimAtItsDeclaration(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 30, 0, time.UTC)
	s, _ := settlementTestService(t, &now, "")
	defer s.Close()
	settlementFundAuthor(t, s, now)
	receipt := settlementAdmit(t, s, time.Date(2026, 11, 11, 12, 0, 40, 0, time.UTC), 1)
	declaration, ok := s.CurrentDeclaration()
	if !ok || declaration.EffectiveAt != "2026-11-11T12:00:30Z" {
		t.Fatalf("declaration = %+v; want one in effect from the start at 12:00:30", declaration)
	}

	now = time.Date(2026, 11, 11, 12, 3, 0, 0, time.UTC)
	claims := settlementRun(t, s)
	if len(claims) != 1 {
		t.Fatalf("claims for a provider started inside a period = %d, want 1", len(claims))
	}
	if claims[0].PeriodStart != "2026-11-11T12:00:30Z" || claims[0].PeriodEnd != "2026-11-11T12:01:00Z" || claims[0].DeclarationID != declaration.DeclarationID || claims[0].ReceiptCount != 1 {
		t.Errorf("claim = %+v; want [12:00:30, 12:01) naming the first declaration with one receipt", claims[0])
	}
	settlementCheckStored(t, s, claims[0])
	settlementCheckLocated(t, s, receipt, claims[0].ClaimID)
}

func TestSettlementPublisherLinksPeriods(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	s, _ := settlementTestService(t, &now, "")
	defer s.Close()
	settlementFundAuthor(t, s, now)
	first := settlementAdmit(t, s, time.Date(2026, 11, 11, 12, 0, 10, 0, time.UTC), 11)
	second := settlementAdmit(t, s, time.Date(2026, 11, 11, 12, 0, 20, 0, time.UTC), 12)
	now = time.Date(2026, 11, 11, 12, 2, 0, 0, time.UTC)
	third := settlementAdmit(t, s, time.Date(2026, 11, 11, 12, 2, 10, 0, time.UTC), 13)

	now = time.Date(2026, 11, 11, 12, 5, 0, 0, time.UTC)
	claims := settlementRun(t, s)
	if len(claims) != 2 {
		t.Fatalf("claims = %d, want 2 (the empty minute 12:01 gets none)", len(claims))
	}
	if claims[0].Sequence != 1 || claims[0].PreviousClaimID != nil || claims[0].PeriodStart != "2026-11-11T12:00:00Z" || claims[0].PeriodEnd != "2026-11-11T12:01:00Z" || claims[0].TotalBaseUnits != "272" || claims[0].CumulativeBaseUnits != "272" {
		t.Errorf("first claim = %+v", claims[0])
	}
	if claims[1].Sequence != 2 || claims[1].PreviousClaimID == nil || *claims[1].PreviousClaimID != claims[0].ClaimID || claims[1].PeriodStart != "2026-11-11T12:02:00Z" || claims[1].PeriodEnd != "2026-11-11T12:03:00Z" || claims[1].TotalBaseUnits != "136" || claims[1].CumulativeBaseUnits != "408" {
		t.Errorf("second claim = %+v", claims[1])
	}
	settlementCheckStored(t, s, claims[0])
	settlementCheckStored(t, s, claims[1])
	settlementCheckLocated(t, s, first, claims[0].ClaimID)
	settlementCheckLocated(t, s, second, claims[0].ClaimID)
	settlementCheckLocated(t, s, third, claims[1].ClaimID)
}

func TestSettlementPublisherKeepsEarlierCommittedReceipt(t *testing.T) {
	now := time.Date(2026, 11, 11, 11, 0, 0, 0, time.UTC)
	s, _ := settlementTestService(t, &now, "")
	defer s.Close()
	now = time.Date(2026, 11, 11, 12, 1, 10, 0, time.UTC)
	settlementFundAuthor(t, s, now)
	later := settlementAdmit(t, s, time.Date(2026, 11, 11, 12, 1, 10, 0, time.UTC), 51)
	earlier := settlementAdmit(t, s, time.Date(2026, 11, 11, 12, 0, 50, 0, time.UTC), 52)

	now = time.Date(2026, 11, 11, 12, 3, 30, 0, time.UTC)
	if claims := settlementRun(t, s); len(claims) != 1 || claims[0].PeriodEnd != "2026-11-11T12:01:00Z" {
		t.Fatalf("claims while 12:01 is open = %+v; want only [12:00, 12:01)", claims)
	}
	now = time.Date(2026, 11, 11, 12, 4, 0, 0, time.UTC)
	claims := settlementRun(t, s)
	if len(claims) != 2 || claims[1].PeriodStart != "2026-11-11T12:01:00Z" || claims[1].ReceiptCount != 1 {
		t.Fatalf("claims = %+v; want a second claim for [12:01, 12:02)", claims)
	}
	settlementCheckLocated(t, s, earlier, claims[0].ClaimID)
	settlementCheckLocated(t, s, later, claims[1].ClaimID)
}

func TestSettlementPublisherSplitsLargePeriod(t *testing.T) {
	now := time.Date(2026, 11, 11, 11, 0, 0, 0, time.UTC)
	s, _ := settlementTestService(t, &now, "")
	defer s.Close()
	now = time.Date(2026, 11, 11, 12, 0, 20, 0, time.UTC)
	settlementFundAuthor(t, s, now)
	for index := 0; index < 10000; index++ {
		settlementAdmit(t, s, now, 100+index)
	}
	last := settlementAdmit(t, s, time.Date(2026, 11, 11, 12, 0, 21, 0, time.UTC), 10100)

	now = time.Date(2026, 11, 11, 12, 3, 0, 0, time.UTC)
	claims := settlementRun(t, s)
	if len(claims) != 2 || claims[0].ReceiptCount != 10000 || claims[1].ReceiptCount != 1 {
		t.Fatalf("split claims = %d; want 10000 and 1 leaves", len(claims))
	}
	if claims[0].PeriodStart != "2026-11-11T12:00:00Z" || claims[0].PeriodEnd != "2026-11-11T12:00:21Z" || claims[1].PeriodStart != "2026-11-11T12:00:21Z" || claims[1].PeriodEnd != "2026-11-11T12:01:00Z" {
		t.Errorf("split periods = [%s, %s), [%s, %s)", claims[0].PeriodStart, claims[0].PeriodEnd, claims[1].PeriodStart, claims[1].PeriodEnd)
	}
	if claims[0].TotalBaseUnits != "1360000" || claims[1].CumulativeBaseUnits != "1360136" {
		t.Errorf("split totals = %s, cumulative %s; want 1360000 and 1360136", claims[0].TotalBaseUnits, claims[1].CumulativeBaseUnits)
	}
	settlementCheckStored(t, s, claims[1])
	settlementCheckLocated(t, s, last, claims[1].ClaimID)
}

func TestSettlementPublisherChoosesDeclarationAtPeriodStart(t *testing.T) {
	now := time.Date(2026, 11, 11, 11, 0, 0, 0, time.UTC)
	s, _ := settlementTestService(t, &now, "")
	defer s.Close()
	initial := s.Declarations()[0]
	before, err := retention.Declare(s.Key(), initial.RetentionSeconds+86400, initial.PayoutRecipients, time.Date(2026, 11, 11, 11, 30, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	after, err := retention.Declare(s.Key(), initial.RetentionSeconds+172800, initial.PayoutRecipients, time.Date(2026, 11, 11, 12, 0, 30, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AddDeclaration(before); err != nil {
		t.Fatal(err)
	}
	if err := s.AddDeclaration(after); err != nil {
		t.Fatal(err)
	}
	now = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	settlementFundAuthor(t, s, now)
	settlementAdmit(t, s, time.Date(2026, 11, 11, 12, 0, 10, 0, time.UTC), 20001)

	now = time.Date(2026, 11, 11, 12, 3, 0, 0, time.UTC)
	claims := settlementRun(t, s)
	if len(claims) != 1 || claims[0].DeclarationID != before.DeclarationID {
		t.Fatalf("claims = %+v; want one naming the declaration effective at 11:30", claims)
	}
}

func TestSettlementPublisherSetsRetentionOfClaimedRecords(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	s, _ := settlementTestService(t, &now, "")
	defer s.Close()
	id := "fy1:4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f"
	if _, err := s.Records().Accept(Acceptance{MessageID: id, Bytes: loadExample(t, 0), IntentDigest: sha256.Sum256([]byte("intent")), ReceiptID: "receipt", IssuedAt: now}); err != nil {
		t.Fatal(err)
	}
	settlementFundAuthor(t, s, now)
	issued := time.Date(2026, 11, 11, 12, 0, 10, 0, time.UTC)
	settlementAdmitMessage(t, s, issued, id, 60001)
	if due := s.Records().DueForExpiry(issued.AddDate(10, 0, 0)); len(due) != 0 {
		t.Fatalf("records due before any claim = %v, want none", due)
	}

	now = time.Date(2026, 11, 11, 12, 3, 0, 0, time.UTC)
	settlementRun(t, s)
	end := issued.Add(time.Duration(s.Declarations()[0].RetentionSeconds) * time.Second)
	if due := s.Records().DueForExpiry(end.Add(-time.Second)); len(due) != 0 {
		t.Errorf("records due a second before issued_at plus the declared retention = %v, want none", due)
	}
	if due := s.Records().DueForExpiry(end); len(due) != 1 || due[0] != id {
		t.Errorf("records due at issued_at plus the declared retention = %v, want [%s]", due, id)
	}
}

func TestSettlementPublisherResumesAfterRestart(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	s, cfg := settlementTestService(t, &now, t.TempDir())
	settlementFundAuthor(t, s, now)
	first := settlementAdmit(t, s, time.Date(2026, 11, 11, 12, 0, 10, 0, time.UTC), 30001)
	now = time.Date(2026, 11, 11, 12, 3, 0, 0, time.UTC)
	second := settlementAdmit(t, s, now, 30002)
	if claims := settlementRun(t, s); len(claims) != 1 {
		t.Fatalf("claims before restart = %d, want 1", len(claims))
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now = time.Date(2026, 11, 11, 12, 6, 0, 0, time.UTC)
	claims := settlementRun(t, s)
	if len(claims) != 2 || claims[1].Sequence != 2 || claims[1].PreviousClaimID == nil || *claims[1].PreviousClaimID != claims[0].ClaimID || claims[1].ReceiptCount != 1 || claims[1].CumulativeBaseUnits != "272" {
		t.Fatalf("claims after restart = %+v", claims)
	}
	settlementCheckLocated(t, s, first, claims[0].ClaimID)
	settlementCheckLocated(t, s, second, claims[1].ClaimID)
	commitment, err := settlement.DepositCommitment(s.ProviderID(), wire.BitcoinMainnet)
	if err != nil {
		t.Fatal(err)
	}
	transfers, err := s.Chain.Transfers(context.Background(), faux.Query{Network: wire.BitcoinMainnet, Data: fmt.Sprintf("%x", commitment), Limit: 10})
	if err != nil || len(transfers) != 1 {
		t.Fatalf("settlement deposits = %d, %v; want the first one only", len(transfers), err)
	}
}

func TestSettlementStoreSurvivesRestart(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 30, 0, time.UTC)
	s, cfg := settlementTestService(t, &now, t.TempDir())
	claim := wire.SettlementClaim{ClaimID: "fysc2:claim-one", ProviderID: s.ProviderID(), Network: wire.BitcoinMainnet, Sequence: 1, ReceiptCount: 1}
	leaves := []wire.SettlementLeaf{{ReceiptHash: "receipt-hash", ScheduleID: "schedule", AmountBaseUnits: "136", IssuedAt: "2026-11-11T11:59:00Z"}}
	receipts := []wire.SubmissionReceipt{{ReceiptID: "receipt-one", MessageID: "fy1:one", IssuedAt: "2026-11-11T11:59:00Z"}}
	if err := s.Settlement().Append(claim, leaves, receipts); err != nil {
		t.Fatal(err)
	}
	gap := claim
	gap.ClaimID, gap.Sequence = "fysc2:claim-gap", 3
	if err := s.Settlement().Append(gap, leaves, []wire.SubmissionReceipt{{ReceiptID: "receipt-gap"}}); err == nil {
		t.Error("a claim with a sequence gap was appended")
	}
	witness := wire.SettlementWitness{ClaimID: claim.ClaimID, WitnessID: "did:key:witness"}
	if added, err := s.Settlement().AddWitness(witness); err != nil || !added {
		t.Fatalf("first AddWitness = %t, %v; want true", added, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got, ok, err := s.Settlement().Claim(claim.ClaimID); err != nil || !ok || got.ClaimID != claim.ClaimID {
		t.Errorf("Claim after restart = %+v, %t, %v", got, ok, err)
	}
	if got, ok, err := s.Settlement().Last(wire.BitcoinMainnet); err != nil || !ok || got.ClaimID != claim.ClaimID {
		t.Errorf("Last after restart = %+v, %t, %v", got, ok, err)
	}
	if page, err := s.Settlement().Leaves(claim.ClaimID, 0, 1); err != nil || len(page) != 1 || page[0] != leaves[0] {
		t.Errorf("Leaves after restart = %+v, %v", page, err)
	}
	if claimID, index, ok, err := s.Settlement().Locate("receipt-one"); err != nil || !ok || claimID != claim.ClaimID || index != 0 {
		t.Errorf("Locate after restart = %q, %d, %t, %v", claimID, index, ok, err)
	}
	if added, err := s.Settlement().AddWitness(witness); err != nil || added {
		t.Errorf("second AddWitness = %t, %v; want false", added, err)
	}
	if witnesses, err := s.Settlement().Witnesses(claim.ClaimID); err != nil || len(witnesses) != 1 {
		t.Errorf("Witnesses after restart = %+v, %v; want one", witnesses, err)
	}
}
