package sdk

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

// acSchedule1 loads fee schedule 1 for bitcoin-mainnet from the specification vector.
func acSchedule1(t *testing.T) wire.FeeSchedule {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "ac", "fee-schedule-1-bitcoin.json"))
	if err != nil {
		t.Fatal(err)
	}
	var schedule wire.FeeSchedule
	if err := wire.Decode(data, "FeeSchedule", &schedule); err != nil {
		t.Fatal(err)
	}
	return schedule
}

func acPlan(t *testing.T, amount, confirmed string) DepositPlan {
	t.Helper()
	return DepositPlan{Network: wire.BitcoinMainnet, AmountChainUnits: amount, DisclosureAcknowledged: true, Schedule: acSchedule1(t), Confirmed: confirmed, Pending: "0"}
}

func acFileLog(t *testing.T) (FundingLog, string) {
	t.Helper()
	dir := t.TempDir()
	log, err := FileFundingLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	return log, dir
}

var acKindLine = regexp.MustCompile(`(?m)^.*"kind":"([a-z_]+)".*$`)

// acLoggedKinds returns the comma-separated event kinds in file order from a FileFundingLog directory.
func acLoggedKinds(t *testing.T, dir string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "funding.jsonl"))
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(strings.Fields(acKindLine.ReplaceAllString(string(data), "$1")), ",")
}

func acTransfer() wire.DepositTransfer {
	index := int64(0)
	return wire.DepositTransfer{Network: wire.BitcoinMainnet, TxID: strings.Repeat("ab", 32), OutputIndex: &index}
}

func TestACRequestDeposit(t *testing.T) {
	t.Run("10000 satoshis on bitcoin-mainnet is invoiced and logged", func(t *testing.T) {
		p := acNewProvider(t, nil)
		log, dir := acFileLog(t)
		invoice, request, err := p.client.RequestDeposit(context.Background(), acAuthorKey(), acPlan(t, "10000", "0"), log)
		if err != nil {
			t.Fatal(err)
		}
		if invoice.AmountChainUnits != "10000" || invoice.AuthorID != keys.DIDOf(acAuthorKey()) || request.ProviderID != keys.DIDOf(acProviderKey()) {
			t.Fatalf("invoice %+v for request %+v; want 10000 sat for the author at the pinned provider", invoice, request)
		}
		if got := acLoggedKinds(t, dir); got != "requested,invoiced" {
			t.Fatalf("logged events = %s; want requested,invoiced", got)
		}
		if p.issued() != 1 || p.count("POST /v1/deposits") != 1 {
			t.Fatalf("challenges/deposits = %d/%d; want 1/1", p.issued(), p.count("POST /v1/deposits"))
		}
	})
	t.Run("invoice amount differs by one satoshi", func(t *testing.T) {
		acCheckInvoiceRejected(t, func(p *acProvider) {
			p.mutateInvoice = func(inv *wire.DepositInvoice) { inv.AmountChainUnits = "10001" }
		})
	})
	t.Run("recipient belongs to another network", func(t *testing.T) {
		acCheckInvoiceRejected(t, func(p *acProvider) {
			p.mutateInvoice = func(inv *wire.DepositInvoice) { inv.Recipient = faux.DevAddress(wire.EthereumMainnet, "ac-invoice") }
		})
	})
	t.Run("expires_at 25 hours after issued_at", func(t *testing.T) {
		acCheckInvoiceRejected(t, func(p *acProvider) {
			p.mutateInvoice = func(inv *wire.DepositInvoice) { inv.ExpiresAt = wire.FormatTime(acNow.Add(25 * time.Hour)) }
		})
	})
	t.Run("signature by another key", func(t *testing.T) {
		acCheckInvoiceRejected(t, func(p *acProvider) { p.invoiceSigner = acOtherKey() })
	})
}

func acCheckInvoiceRejected(t *testing.T, setup func(*acProvider)) {
	t.Helper()
	p := acNewProvider(t, setup)
	log, dir := acFileLog(t)
	_, _, err := p.client.RequestDeposit(context.Background(), acAuthorKey(), acPlan(t, "10000", "0"), log)
	if !errors.Is(err, ErrInvoiceRejected) {
		t.Fatalf("RequestDeposit error = %v; want ErrInvoiceRejected", err)
	}
	if got := acLoggedKinds(t, dir); got != "requested,failed" {
		t.Fatalf("logged events = %s; want requested,failed", got)
	}
}

func TestACRequestDepositRefusals(t *testing.T) {
	t.Run("bitcoin-testnet", func(t *testing.T) {
		plan := acPlan(t, "10000", "0")
		plan.Network = "bitcoin-testnet"
		acCheckRefused(t, plan, ErrUnsupportedNetwork)
	})
	t.Run("disclosure not acknowledged", func(t *testing.T) {
		plan := acPlan(t, "10000", "0")
		plan.DisclosureAcknowledged = false
		acCheckRefused(t, plan, ErrDisclosureRequired)
	})
	t.Run("130000 sat confirmed plus 7000 sat exceeds the 136000000 msat cap", func(t *testing.T) {
		acCheckRefused(t, acPlan(t, "7000", "130000000"), ErrCustodyCap)
	})
	t.Run("130000 sat confirmed plus 6000 sat reaches the cap and is invoiced", func(t *testing.T) {
		p := acNewProvider(t, nil)
		log, _ := acFileLog(t)
		invoice, _, err := p.client.RequestDeposit(context.Background(), acAuthorKey(), acPlan(t, "6000", "130000000"), log)
		if err != nil || invoice.AmountChainUnits != "6000" {
			t.Fatalf("RequestDeposit = %+v, %v; want a 6000 sat invoice", invoice, err)
		}
	})
}

func acCheckRefused(t *testing.T, plan DepositPlan, want error) {
	t.Helper()
	p := acNewProvider(t, nil)
	log, dir := acFileLog(t)
	_, _, err := p.client.RequestDeposit(context.Background(), acAuthorKey(), plan, log)
	if !errors.Is(err, want) {
		t.Fatalf("RequestDeposit error = %v; want %v", err, want)
	}
	if p.issued() != 0 || p.count("POST /v1/deposits") != 0 || acLoggedKinds(t, dir) != "" {
		t.Fatalf("refusal sent %d challenges, %d deposits and logged %q; want nothing", p.issued(), p.count("POST /v1/deposits"), acLoggedKinds(t, dir))
	}
}

func TestACObserveDeposit(t *testing.T) {
	t.Run("202 pending then a verified 201 credit", func(t *testing.T) {
		p := acNewProvider(t, func(p *acProvider) { p.pendingObserves = 1 })
		log, dir := acFileLog(t)
		invoice, _, err := p.client.RequestDeposit(context.Background(), acAuthorKey(), acPlan(t, "10000", "0"), log)
		if err != nil {
			t.Fatal(err)
		}
		first, err := p.client.ObserveDeposit(context.Background(), acAuthorKey(), invoice, acTransfer(), log)
		if err != nil || !first.Pending || first.Credit != nil {
			t.Fatalf("first observation = %+v, %v; want pending without credit", first, err)
		}
		second, err := p.client.ObserveDeposit(context.Background(), acAuthorKey(), invoice, acTransfer(), log)
		if err != nil || second.Pending || second.Credit == nil || second.Credit.AmountAccountingUnits != "10000000" || second.Credit.DepositID != invoice.DepositID {
			t.Fatalf("second observation = %+v, %v; want a 10000000 msat credit for the invoice", second, err)
		}
		if got := acLoggedKinds(t, dir); got != "requested,invoiced,transfer_sent,observed_pending,transfer_sent,credited" {
			t.Fatalf("logged events = %s", got)
		}
		attempts, err := log.Attempts()
		if err != nil || len(attempts) != 1 || NeedsReconciliation(attempts[0]) {
			t.Fatalf("attempts = %+v, %v; want one credited attempt", attempts, err)
		}
	})
	t.Run("credit for another deposit_id is rejected", func(t *testing.T) {
		p := acNewProvider(t, func(p *acProvider) {
			p.mutateCredit = func(c *wire.DepositCredit) { c.DepositID = "fydi2:" + strings.Repeat("0", 64) }
		})
		log, _ := acFileLog(t)
		invoice, _, err := p.client.RequestDeposit(context.Background(), acAuthorKey(), acPlan(t, "10000", "0"), log)
		if err != nil {
			t.Fatal(err)
		}
		result, err := p.client.ObserveDeposit(context.Background(), acAuthorKey(), invoice, acTransfer(), log)
		if err == nil || result.Credit != nil {
			t.Fatalf("ObserveDeposit = %+v, %v; want the foreign credit rejected", result, err)
		}
		attempts, err := log.Attempts()
		if err != nil || len(attempts) != 1 || !NeedsReconciliation(attempts[0]) {
			t.Fatalf("attempts = %+v, %v; want the transfer still awaiting reconciliation", attempts, err)
		}
	})
}

func TestACReconciliationAfterObserveTimeout(t *testing.T) {
	gate := make(chan struct{})
	p := acNewProvider(t, func(p *acProvider) { p.observeGate = gate })
	t.Cleanup(func() { close(gate) })
	log, dir := acFileLog(t)
	invoice, _, err := p.client.RequestDeposit(context.Background(), acAuthorKey(), acPlan(t, "10000", "0"), log)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := p.client.ObserveDeposit(ctx, acAuthorKey(), invoice, acTransfer(), log); !IsAmbiguous(err) {
		t.Fatalf("ObserveDeposit error = %v; want an ambiguous timeout", err)
	}
	reopened, err := FileFundingLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	attempts, err := reopened.Attempts()
	if err != nil || len(attempts) != 1 || !NeedsReconciliation(attempts[0]) || attempts[0].Transfer == nil || attempts[0].Transfer.TxID != acTransfer().TxID {
		t.Fatalf("attempts = %+v, %v; want the sent transfer awaiting reconciliation", attempts, err)
	}
	if p.count("POST /v1/deposits") != 1 || p.count("POST /v1/deposits/observe") != 1 {
		t.Fatalf("deposits/observations = %d/%d; want 1/1", p.count("POST /v1/deposits"), p.count("POST /v1/deposits/observe"))
	}
}

func TestACFileFundingLogIgnoresTornFinalLine(t *testing.T) {
	log, dir := acFileLog(t)
	if err := log.Append(FundingEvent{Kind: FundingRequested, RequestID: "r1", Network: wire.BitcoinMainnet, AmountChainUnits: "10000", At: "2026-11-11T12:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	file, err := os.OpenFile(filepath.Join(dir, "funding.jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(`{"kind":"credited","request_id":"r1"`); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	attempts, err := log.Attempts()
	if err != nil || len(attempts) != 1 || !attempts[0].Requested || attempts[0].Credited {
		t.Fatalf("attempts = %+v, %v; want the requested attempt without the torn credit", attempts, err)
	}
}

func TestACBalance(t *testing.T) {
	t.Run("signed balance verifies", func(t *testing.T) {
		p := acNewProvider(t, nil)
		balance, err := p.client.Balance(context.Background(), acAuthorKey(), wire.BitcoinMainnet)
		if err != nil || balance.Available != "10000000" || balance.Reserved != "136" {
			t.Fatalf("Balance = %+v, %v; want the provider's signed assertion", balance, err)
		}
	})
	t.Run("balance signed by another key is rejected", func(t *testing.T) {
		p := acNewProvider(t, func(p *acProvider) { p.balanceSigner = acOtherKey() })
		if _, err := p.client.Balance(context.Background(), acAuthorKey(), wire.BitcoinMainnet); Classify(err) != SignalProtocol {
			t.Fatalf("Balance error = %v; want a protocol error", err)
		}
	})
}

func TestACWithdraw(t *testing.T) {
	recipient := faux.DevAddress(wire.BitcoinMainnet, "ac-withdrawal")
	requestID := keys.EncodeB64URL([]byte("ac-withdrawal-request-0000000001"))
	t.Run("new request then an exact retry with the same request_id", func(t *testing.T) {
		p := acNewProvider(t, nil)
		first, err := p.client.Withdraw(context.Background(), acAuthorKey(), wire.BitcoinMainnet, "5000000", recipient, requestID)
		if err != nil || first.Status != "pending" || first.RequestID != requestID {
			t.Fatalf("first Withdraw = %+v, %v", first, err)
		}
		retry, err := p.client.Withdraw(context.Background(), acAuthorKey(), wire.BitcoinMainnet, "5000000", recipient, requestID)
		if err != nil || retry != first {
			t.Fatalf("retry Withdraw = %+v, %v; want the original status", retry, err)
		}
		if p.count("POST /v1/withdrawals") != 2 || p.issued() != 2 {
			t.Fatalf("withdrawals/challenges = %d/%d; want 2/2 (a fresh challenge per attempt)", p.count("POST /v1/withdrawals"), p.issued())
		}
	})
	t.Run("402 insufficient_balance is a public error", func(t *testing.T) {
		p := acNewProvider(t, func(p *acProvider) { p.insufficient = "9000000" })
		_, err := p.client.Withdraw(context.Background(), acAuthorKey(), wire.BitcoinMainnet, "9000000", recipient, requestID)
		var public *Error
		if !errors.As(err, &public) || public.HTTPStatus != 402 || public.Status != "insufficient_balance" {
			t.Fatalf("Withdraw error = %v; want 402 insufficient_balance", err)
		}
	})
}

func TestACAdmission(t *testing.T) {
	messageID := "fy1:" + strings.Repeat("a", 64)
	t.Run("committed receipt verifies", func(t *testing.T) {
		p := acNewProvider(t, nil)
		receipt, err := p.client.Admission(context.Background(), acAuthorKey(), messageID)
		if err != nil || receipt.MessageID != messageID {
			t.Fatalf("Admission = %+v, %v", receipt, err)
		}
	})
	t.Run("receipt for another message is rejected", func(t *testing.T) {
		p := acNewProvider(t, func(p *acProvider) { p.receiptMessage = "fy1:" + strings.Repeat("b", 64) })
		if _, err := p.client.Admission(context.Background(), acAuthorKey(), messageID); Classify(err) != SignalProtocol {
			t.Fatalf("Admission error = %v; want a protocol error", err)
		}
	})
}
