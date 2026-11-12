package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

var flowClient = &http.Client{Timeout: 10 * time.Second}

func TestDevnetEndToEnd(t *testing.T) {
	genesis := devnet.Genesis()
	if len(genesis.FeeSchedules) == 0 || genesis.FeeSchedules[0].Network != wire.BitcoinMainnet || genesis.FeeSchedules[0].Revision != 1 {
		t.Fatal("dev genesis has no bitcoin-mainnet fee schedule 1")
	}
	schedule := genesis.FeeSchedules[0]
	effectiveAt, err := wire.ParseTime(schedule.EffectiveAt)
	if err != nil {
		t.Fatal(err)
	}
	calendar, err := devnet.Calendar(0)
	if err != nil {
		t.Fatal(err)
	}
	quarterStart, quarterEnd := calendar.Quarter(effectiveAt)
	if effectiveAt.After(quarterStart) {
		quarterStart = quarterEnd
		_, quarterEnd = calendar.Quarter(quarterStart)
	}
	start := quarterStart.Add(time.Hour)
	var clock atomic.Int64
	clock.Store(start.Unix())
	now := func() time.Time { return time.Unix(clock.Load(), 0).UTC() }
	ctx := context.Background()
	d, err := startDevnet(ctx, devnetOptions{DataDir: t.TempDir(), Now: now, Loops: false, ClaimPeriod: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Errorf("close devnet: %v", err)
		}
	})

	providerID := keys.DIDOf(devnet.ProviderKey(0))
	foundationID := keys.DIDOf(devnet.FoundationKey())
	statusTransport := &flowStatusTransport{base: http.DefaultTransport, statuses: make(map[string]int)}
	statusClient := *flowClient
	statusClient.Transport = statusTransport
	provider, err := sdk.New(d.URL("provider"), sdk.Options{ServiceID: providerID, AllowLoopbackHTTP: true, Now: now, HTTPClient: &statusClient})
	if err != nil {
		t.Fatal(err)
	}
	foundation, err := sdk.New(d.URL("foundation"), sdk.Options{ServiceID: foundationID, AllowLoopbackHTTP: true, Now: now, HTTPClient: flowClient})
	if err != nil {
		t.Fatal(err)
	}
	root, err := sdk.BuildTrustRoot(sdk.Pin{ReleaseKeyIDs: genesis.ReleaseKeyIDs, Threshold: int(genesis.ReleaseThreshold)}, sdk.Publications{Genesis: genesis, FetchedAt: now()})
	if err != nil {
		t.Fatal(err)
	}
	keystore, err := sdk.OpenKeystore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := keystore.Create("flow-author"); err != nil {
		t.Fatal(err)
	}
	authorKey, err := keystore.PrivateKey("flow-author")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.EnrollEpoch(ctx, authorKey, wire.AuthorizationEpochEnroll{RequestNonce: keys.EncodeB64URL(make([]byte, 32)), NotAfter: wire.FormatTime(now().Add(time.Minute))}); err != nil {
		t.Fatal(err)
	}
	chain := faux.NewClient(d.URL("faux"), nil)
	fundingLog, err := sdk.FileFundingLog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	journal, err := sdk.FileJournal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	publisher := sdk.NewPublisher(provider, root, journal, now)
	var invoice wire.DepositInvoice
	var firstQuote sdk.Quoted
	var firstAdmitted sdk.Admitted
	var firstPublished sdk.Published
	var replyReceipt wire.SubmissionReceipt
	var replyPublished sdk.Published
	var claims wire.SettlementClaimsPage
	var proof wire.SettlementProof
	var verifier *sdk.Verifier
	var monitor0, monitor1, monitor2, monitor3, monitor4 *sdk.Client

	if ok := t.Run("Deposit", func(t *testing.T) {
		invoice, _, err = provider.RequestDeposit(ctx, authorKey, sdk.DepositPlan{Network: wire.BitcoinMainnet, AmountChainUnits: "10000", DisclosureAcknowledged: true, Schedule: schedule, Confirmed: "0", Pending: "0"}, fundingLog)
		if err != nil {
			t.Fatal(err)
		}
		transfer, err := chain.Submit(ctx, faux.SubmitRequest{Network: wire.BitcoinMainnet, From: faux.DevAddress(wire.BitcoinMainnet, "flow-author"), Recipient: invoice.Recipient, AmountChainUnits: invoice.AmountChainUnits, ClientRef: "flow-author-deposit"})
		if err != nil {
			t.Fatal(err)
		}
		index := int64(0)
		observed, err := provider.ObserveDeposit(ctx, authorKey, invoice, wire.DepositTransfer{Network: wire.BitcoinMainnet, TxID: transfer.TxID, OutputIndex: &index}, fundingLog)
		if err != nil || observed.Pending || observed.Credit == nil {
			t.Fatalf("ObserveDeposit = %+v, %v; want a committed DepositCredit", observed, err)
		}
		if observed.Credit.AmountAccountingUnits != "10000000" {
			t.Fatalf("DepositCredit amount = %s; want 10000000 msat", observed.Credit.AmountAccountingUnits)
		}
		if got := statusTransport.status("/v1/deposits/observe"); got != http.StatusCreated {
			t.Fatalf("deposit observation status = %d; want 201", got)
		}
		balance, err := provider.Balance(ctx, authorKey, wire.BitcoinMainnet)
		if err != nil || balance.Available != "10000000" {
			t.Fatalf("Balance = %+v, %v; want 10000000 msat available", balance, err)
		}
	}); !ok {
		return
	}

	if ok := t.Run("Quote", func(t *testing.T) {
		firstQuote, err = publisher.QuoteDraft(ctx, sdk.Draft{ContentFormat: "text/plain", Text: "First flow post"}, authorKey, wire.BitcoinMainnet, nil)
		if err != nil {
			t.Fatal(err)
		}
		if firstQuote.Prepared.BillableBytes > 1024 || firstQuote.Quote.AmountBaseUnits != "136" || firstQuote.Quote.ScheduleID != schedule.ScheduleID {
			t.Fatalf("FeeQuote = %+v, billable_bytes=%d; want schedule 1, 136, and at most 1024 bytes", firstQuote.Quote, firstQuote.Prepared.BillableBytes)
		}
	}); !ok {
		return
	}

	if ok := t.Run("Admission", func(t *testing.T) {
		firstAdmitted, err = publisher.Admit(ctx, authorKey, firstQuote, sdk.Approval{MessageID: firstQuote.Prepared.MessageID, Amount: "136", Network: wire.BitcoinMainnet, ApprovedAt: now()})
		if err != nil {
			t.Fatal(err)
		}
		if got := statusTransport.status("/v1/admissions"); got != http.StatusCreated {
			t.Fatalf("SDK admission status = %d; want 201", got)
		}
		entry, ok, err := journal.Get(firstQuote.Prepared.MessageID)
		if err != nil || !ok {
			t.Fatalf("journal entry = %v, %v; want admission evidence", ok, err)
		}
		status, body, err := flowHTTP(ctx, http.MethodPost, d.URL("provider")+"/v1/admissions", entry.AdmissionRequest)
		if err != nil || status != http.StatusOK {
			t.Fatalf("identical admission retry status = %d, error = %v; want 200", status, err)
		}
		if !bytes.Equal(body, entry.ReceiptRaw) {
			t.Fatalf("identical admission retry body differs from original receipt")
		}
		balance, err := provider.Balance(ctx, authorKey, wire.BitcoinMainnet)
		if err != nil || balance.Available != "9999864" {
			t.Fatalf("Balance after admission = %+v, %v; want 9999864 msat", balance, err)
		}
	}); !ok {
		return
	}

	if ok := t.Run("Post", func(t *testing.T) {
		firstPublished, err = publisher.Finalize(ctx, authorKey, firstAdmitted)
		if err != nil {
			t.Fatal(err)
		}
		if got := statusTransport.status("/v1/messages"); got != http.StatusCreated {
			t.Fatalf("SDK message acceptance status = %d; want 201", got)
		}
		status, body, err := flowHTTP(ctx, http.MethodPost, d.URL("provider")+"/v1/messages", firstPublished.Raw)
		if err != nil || status != http.StatusOK {
			t.Fatalf("identical message retry status = %d, error = %v; want 200", status, err)
		}
		if !bytes.Contains(body, []byte(firstPublished.MessageID)) {
			t.Fatalf("identical message retry response does not identify %s: %s", firstPublished.MessageID, body)
		}
	}); !ok {
		return
	}

	if ok := t.Run("Reply", func(t *testing.T) {
		quoted, quoteErr := publisher.QuoteDraft(ctx, sdk.Draft{ContentFormat: "text/plain", Text: "Flow reply", ReplyTo: firstPublished.MessageID}, authorKey, wire.BitcoinMainnet, nil)
		if quoteErr != nil {
			t.Fatal(quoteErr)
		}
		if quoted.Quote.AmountBaseUnits != "136" || quoted.Quote.ScheduleID != schedule.ScheduleID {
			t.Fatalf("reply FeeQuote = %+v; want schedule 1 amount 136", quoted.Quote)
		}
		admitted, admitErr := publisher.Admit(ctx, authorKey, quoted, sdk.Approval{MessageID: quoted.Prepared.MessageID, Amount: "136", Network: wire.BitcoinMainnet, ApprovedAt: now()})
		if admitErr != nil {
			t.Fatal(admitErr)
		}
		replyReceipt = admitted.Receipt
		replyPublished, err = publisher.Finalize(ctx, authorKey, admitted)
		if err != nil {
			t.Fatal(err)
		}
		var reply wire.PostMessage
		if err := wire.Decode(replyPublished.Raw, "PostMessage", &reply); err != nil || reply.Payload.ReplyTo != firstPublished.MessageID {
			t.Fatalf("reply message = %+v, %v; want reply_to %s", reply, err, firstPublished.MessageID)
		}
		balance, err := provider.Balance(ctx, authorKey, wire.BitcoinMainnet)
		if err != nil || balance.Available != "9999728" {
			t.Fatalf("Balance after reply = %+v, %v; want 9999728 msat", balance, err)
		}
	}); !ok {
		return
	}

	if ok := t.Run("Pending", func(t *testing.T) {
		verifier = sdk.NewVerifier(sdk.VerifierConfig{Root: root, Trusts: func(id string) bool { return id == providerID }, Now: now})
		firstFetched, err := provider.GetMessage(ctx, firstPublished.MessageID)
		if err != nil {
			t.Fatal(err)
		}
		replyFetched, err := provider.GetMessage(ctx, replyPublished.MessageID)
		if err != nil {
			t.Fatal(err)
		}
		firstRecord := verifier.Verify(firstFetched.Raw, firstPublished.MessageID, sdk.Source{Endpoint: firstFetched.Endpoint, ServiceID: firstFetched.ServiceID, Kind: sdk.SourceProvider})
		replyRecord := verifier.Verify(replyFetched.Raw, replyPublished.MessageID, sdk.Source{Endpoint: replyFetched.Endpoint, ServiceID: replyFetched.ServiceID, Kind: sdk.SourceProvider})
		if got := verifier.Settle(firstRecord, nil, nil).Settlement; got != settlement.StatePending {
			t.Fatalf("first settlement state = %q; want pending_settlement", got)
		}
		if got := verifier.Settle(replyRecord, nil, nil).Settlement; got != settlement.StatePending {
			t.Fatalf("reply settlement state = %q; want pending_settlement", got)
		}
		status, body, err := flowHTTP(ctx, http.MethodGet, d.URL("provider")+"/v1/settlements/"+url.PathEscape(firstAdmitted.Receipt.ReceiptID), nil)
		if err != nil || status != http.StatusNotFound || !bytes.Contains(body, []byte(`"status":"not_found"`)) {
			t.Fatalf("pending settlement response = %d %s, %v; want 404 not_found", status, body, err)
		}
		status, body, err = flowHTTP(ctx, http.MethodGet, d.URL("provider")+"/v1/settlements/"+url.PathEscape(replyReceipt.ReceiptID), nil)
		if err != nil || status != http.StatusNotFound || !bytes.Contains(body, []byte(`"status":"not_found"`)) {
			t.Fatalf("reply pending settlement response = %d %s, %v; want 404 not_found", status, body, err)
		}
	}); !ok {
		return
	}

	if ok := t.Run("Read", func(t *testing.T) {
		providerFirst, err := provider.GetMessage(ctx, firstPublished.MessageID)
		if err != nil || !bytes.Equal(providerFirst.Raw, firstPublished.Raw) {
			t.Fatalf("provider first read differs from submitted bytes: %v", err)
		}
		providerReply, err := provider.GetMessage(ctx, replyPublished.MessageID)
		if err != nil || !bytes.Equal(providerReply.Raw, replyPublished.Raw) {
			t.Fatalf("provider reply read differs from submitted bytes: %v", err)
		}
		if err := d.Step(ctx); err != nil {
			t.Fatal(err)
		}
		replica, err := sdk.New(d.URL("replica"), sdk.Options{ServiceID: keys.DIDOf(devnet.ReplicaKey()), AllowLoopbackHTTP: true, Now: now, HTTPClient: flowClient})
		if err != nil {
			t.Fatal(err)
		}
		replicaFirst, err := replica.GetMessage(ctx, firstPublished.MessageID)
		if err != nil || !bytes.Equal(replicaFirst.Raw, firstPublished.Raw) {
			t.Fatalf("replica first read differs from submitted bytes: %v", err)
		}
		replicaReply, err := replica.GetMessage(ctx, replyPublished.MessageID)
		if err != nil || !bytes.Equal(replicaReply.Raw, replyPublished.Raw) {
			t.Fatalf("replica reply read differs from submitted bytes: %v", err)
		}
		changes, err := replica.Changes(ctx, 0, 10)
		if err != nil || len(changes.Page.Items) != 2 {
			t.Fatalf("replica changes = %+v, %v; want both posts", changes.Page, err)
		}
	}); !ok {
		return
	}

	if ok := t.Run("Claim", func(t *testing.T) {
		issued, err := wire.ParseTime(firstAdmitted.Receipt.IssuedAt)
		if err != nil {
			t.Fatal(err)
		}
		periodStart := time.Unix(issued.Unix()-issued.Unix()%int64(time.Hour/time.Second), 0).UTC()
		periodEnd := periodStart.Add(time.Hour)
		clock.Store(periodEnd.Add(2*time.Minute + time.Second).Unix())
		if err := d.Step(ctx); err != nil {
			t.Fatal(err)
		}
		claims, _, err = provider.SettlementClaims(ctx, wire.BitcoinMainnet, 0)
		if err != nil || len(claims.Claims) != 1 {
			t.Fatalf("settlement claims = %+v, %v; want one claim", claims, err)
		}
		claim := claims.Claims[0]
		if claim.Sequence != 1 || claim.PreviousClaimID != nil || claim.PeriodStart != wire.FormatTime(periodStart) || claim.PeriodEnd != wire.FormatTime(periodEnd) || claim.ReceiptCount != 2 || claim.TotalBaseUnits != "272" || claim.CumulativeBaseUnits != "272" {
			t.Fatalf("claim = %+v; want sequence 1, two receipts totaling 272, cumulative 272", claim)
		}
	}); !ok {
		return
	}

	if ok := t.Run("Quorum", func(t *testing.T) {
		deposited := flowSettlementDeposited(t, chain, providerID, genesis.FoundationKeyCertificates[0].Recipients.BitcoinMainnet)
		if deposited.Cmp(big.NewInt(272)) < 0 {
			t.Fatalf("final provider settlement deposits = %s msat; want at least 272", deposited)
		}
		claimed, ok := new(big.Int).SetString(claims.Claims[0].CumulativeBaseUnits, 10)
		if !ok {
			t.Fatalf("invalid cumulative claim amount %q", claims.Claims[0].CumulativeBaseUnits)
		}
		wantCredit := new(big.Int).Sub(deposited, claimed).String()
		monitor0 = flowMonitorClient(t, d, 0, now)
		monitor1 = flowMonitorClient(t, d, 1, now)
		monitor2 = flowMonitorClient(t, d, 2, now)
		monitor3 = flowMonitorClient(t, d, 3, now)
		monitor4 = flowMonitorClient(t, d, 4, now)
		flowCheckWitness(t, monitor0, providerID, claims.Claims[0], wantCredit)
		flowCheckWitness(t, monitor1, providerID, claims.Claims[0], wantCredit)
		flowCheckWitness(t, monitor2, providerID, claims.Claims[0], wantCredit)
		flowCheckWitness(t, monitor3, providerID, claims.Claims[0], wantCredit)
		flowCheckWitness(t, monitor4, providerID, claims.Claims[0], wantCredit)
	}); !ok {
		return
	}

	if ok := t.Run("Proof", func(t *testing.T) {
		proof, _, err = provider.Settlement(ctx, firstAdmitted.Receipt.ReceiptID)
		if err != nil || len(proof.Witnesses) != 5 {
			t.Fatalf("SettlementProof witnesses = %d, %v; want five", len(proof.Witnesses), err)
		}
		verifier = sdk.NewVerifier(sdk.VerifierConfig{Root: root, Trusts: func(id string) bool { return id == providerID }, Now: now})
		record := verifier.Verify(firstPublished.Raw, firstPublished.MessageID, sdk.Source{Endpoint: d.URL("provider"), ServiceID: providerID, Kind: sdk.SourceProvider})
		if settled := verifier.Settle(record, &proof, nil); settled.Settlement != settlement.StateSettled {
			t.Fatalf("proof settlement = %q; want settled", settled.Settlement)
		}
		withoutFirst := proof
		withoutFirst.Witnesses = append([]wire.SettlementWitness(nil), proof.Witnesses[1:]...)
		if settled := verifier.Settle(record, &withoutFirst, nil); settled.Settlement != settlement.StateSettled {
			t.Fatalf("proof without first witness = %q; want settled", settled.Settlement)
		}
		withoutLast := proof
		withoutLast.Witnesses = append([]wire.SettlementWitness(nil), proof.Witnesses[:len(proof.Witnesses)-1]...)
		if settled := verifier.Settle(record, &withoutLast, nil); settled.Settlement != settlement.StateSettled {
			t.Fatalf("proof without last witness = %q; want settled", settled.Settlement)
		}
		three := proof
		three.Witnesses = append([]wire.SettlementWitness(nil), proof.Witnesses[:3]...)
		if settled := verifier.Settle(record, &three, nil); settled.Settlement != settlement.StatePending {
			t.Fatalf("three-witness settlement = %q; want pending_settlement", settled.Settlement)
		}
	}); !ok {
		return
	}

	if ok := t.Run("Vault", func(t *testing.T) {
		if err := d.Step(ctx); err != nil {
			t.Fatal(err)
		}
		receipt, _, err := foundation.VaultReceipt(ctx, firstPublished.MessageID)
		if err != nil {
			t.Fatal(err)
		}
		if err := root.VerifyVaultReceipt(receipt, firstPublished.MessageID, nil); err != nil {
			t.Fatalf("VaultReceipt signature: %v", err)
		}
		vaultMessage, err := foundation.GetMessage(ctx, firstPublished.MessageID)
		if err != nil || !bytes.Equal(vaultMessage.Raw, firstPublished.Raw) {
			t.Fatalf("vault message differs from provider bytes: %v", err)
		}
	}); !ok {
		return
	}

	t.Run("Distribution", func(t *testing.T) {
		cutoff := calendar.CutOff(quarterEnd)
		clock.Store(cutoff.Add(time.Second).Unix())
		page := flowAwaitDistribution(t, ctx, d, foundation, 10*time.Second)
		if len(page.Statements) != 1 {
			t.Fatalf("distribution statements = %d; want one", len(page.Statements))
		}
		statement := page.Statements[0]
		if statement.Network != wire.BitcoinMainnet || statement.PeriodStart != wire.FormatTime(quarterStart) || statement.PeriodEnd != wire.FormatTime(quarterEnd) {
			t.Fatalf("DistributionStatement period/network = %s/%s/%s; want bitcoin-mainnet %s through %s", statement.Network, statement.PeriodStart, statement.PeriodEnd, quarterStart, quarterEnd)
		}
		if statement.ReceivedBaseUnits != "272" || statement.DistributedBaseUnits != "0" || len(statement.Entries) != 1 {
			t.Fatalf("DistributionStatement = %+v; want received 272, distributed 0, and one provider entry", statement)
		}
		entry := statement.Entries[0]
		flowCheckNoQualityReports(t, monitor0, providerID, statement.PeriodEnd)
		flowCheckNoQualityReports(t, monitor1, providerID, statement.PeriodEnd)
		flowCheckNoQualityReports(t, monitor2, providerID, statement.PeriodEnd)
		flowCheckNoQualityReports(t, monitor3, providerID, statement.PeriodEnd)
		flowCheckNoQualityReports(t, monitor4, providerID, statement.PeriodEnd)
		// Values from economics_model.distribute(policy_1, "bitcoin-mainnet", quarter_start, quarter_end, [claim with total 272], {provider: [YEAR]}, {provider: {}}), evaluated by hand from that function.
		if entry.QualityBP != 0 || entry.RetentionYears != 1 || entry.ProvenQuarters != 0 || entry.AmountBaseUnits != "0" || entry.AccruedBaseUnits != "0" {
			t.Fatalf("distribution entry = %+v; want model values quality=0 retention=1 proven=0 amount=0 accrued=0", entry)
		}
		if entry.SettledFeesBaseUnits != "272" {
			t.Fatalf("settled fees = %s; want 272", entry.SettledFeesBaseUnits)
		}
		if len(statement.MonitorPayments) != 5 {
			t.Fatalf("monitor payments = %+v; want five seats of 2 base units", statement.MonitorPayments)
		}
		monitorIDs := [5]string{keys.DIDOf(devnet.MonitorKey(0)), keys.DIDOf(devnet.MonitorKey(1)), keys.DIDOf(devnet.MonitorKey(2)), keys.DIDOf(devnet.MonitorKey(3)), keys.DIDOf(devnet.MonitorKey(4))}
		sort.Strings(monitorIDs[:])
		if statement.MonitorPayments[0].AmountBaseUnits != "2" || statement.MonitorPayments[1].AmountBaseUnits != "2" || statement.MonitorPayments[2].AmountBaseUnits != "2" || statement.MonitorPayments[3].AmountBaseUnits != "2" || statement.MonitorPayments[4].AmountBaseUnits != "2" {
			t.Fatalf("monitor payments = %+v; want five seats of 2 base units", statement.MonitorPayments)
		}
		if statement.MonitorPayments[0].MonitorID != monitorIDs[0] || statement.MonitorPayments[1].MonitorID != monitorIDs[1] || statement.MonitorPayments[2].MonitorID != monitorIDs[2] || statement.MonitorPayments[3].MonitorID != monitorIDs[3] || statement.MonitorPayments[4].MonitorID != monitorIDs[4] {
			t.Fatalf("monitor payment IDs = %+v; want sorted dev monitor DIDs %v", statement.MonitorPayments, monitorIDs)
		}
		if err := root.VerifyFoundationObject(statement, quarterEnd, "distribution-statement", nil); err != nil {
			t.Fatalf("DistributionStatement signature: %v", err)
		}
	})
}

// flowSettlementDeposited sums, in base units, the final successful bitcoin transfers to recipient
// that carry the provider's settlement deposit commitment.
func flowSettlementDeposited(t *testing.T, chain *faux.Client, providerID, recipient string) *big.Int {
	t.Helper()
	commitment, err := settlement.DepositCommitment(providerID, wire.BitcoinMainnet)
	if err != nil {
		t.Fatal(err)
	}
	transfers, err := chain.Transfers(context.Background(), faux.Query{Network: wire.BitcoinMainnet, Data: hex.EncodeToString(commitment[:]), Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	deposited := new(big.Int)
	for _, transfer := range transfers {
		if transfer.Recipient != recipient || !transfer.Finalized || !transfer.Success || transfer.Confirmations < faux.BitcoinConfirmations {
			continue
		}
		chainUnits, ok := new(big.Int).SetString(transfer.AmountChainUnits, 10)
		if !ok {
			t.Fatalf("invalid faux settlement amount %q", transfer.AmountChainUnits)
		}
		deposited.Add(deposited, chainUnits.Mul(chainUnits, big.NewInt(1000)))
	}
	return deposited
}

func flowMonitorClient(t *testing.T, d *devnetRuntime, n int, now func() time.Time) *sdk.Client {
	t.Helper()
	serviceID := keys.DIDOf(devnet.MonitorKey(n))
	client, err := sdk.New(d.URL("monitor")+"/monitors/"+strconv.Itoa(n), sdk.Options{
		ServiceID: serviceID, AllowLoopbackHTTP: true, HTTPClient: flowClient, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func flowCheckWitness(t *testing.T, client *sdk.Client, providerID string, claim wire.SettlementClaim, wantCredit string) {
	t.Helper()
	page, _, err := client.Witnesses(context.Background(), providerID, wire.BitcoinMainnet, 0)
	if err != nil || len(page.Witnesses) != 1 {
		t.Fatalf("%s witnesses = %+v, %v; want one witness", client.ServiceID(), page.Witnesses, err)
	}
	witness := page.Witnesses[0]
	witnessedAt, err := wire.ParseTime(witness.WitnessedAt)
	periodEnd, periodErr := wire.ParseTime(claim.PeriodEnd)
	if err != nil || periodErr != nil || witness.WitnessID != client.ServiceID() || witness.ClaimID != claim.ClaimID || witness.ProviderID != providerID || witness.Network != claim.Network || witness.Sequence != claim.Sequence || witness.PeriodEnd != claim.PeriodEnd || witness.TotalBaseUnits != claim.TotalBaseUnits || witness.CumulativeBaseUnits != claim.CumulativeBaseUnits || witness.ReceiptsRoot != claim.ReceiptsRoot || witness.CreditBaseUnits != wantCredit || witnessedAt.Before(periodEnd) {
		t.Fatalf("%s witness = %+v, parse times = %v/%v; want claim fields, credit %s, and witness at or after period end", client.ServiceID(), witness, err, periodErr, wantCredit)
	}
}

func flowCheckNoQualityReports(t *testing.T, client *sdk.Client, providerID, quarterEnd string) {
	t.Helper()
	page, _, err := client.QualityReports(context.Background(), providerID, quarterEnd)
	if err != nil || len(page.Reports) != 0 {
		t.Fatalf("%s quality reports = %+v, %v; want no reports", client.ServiceID(), page.Reports, err)
	}
}

func flowAwaitDistribution(t *testing.T, ctx context.Context, d *devnetRuntime, foundation *sdk.Client, deadline time.Duration) wire.DistributionPage {
	t.Helper()
	runCtx, cancel := context.WithCancel(ctx)
	runDone := make(chan error, 1)
	go func() { runDone <- d.Service("foundation").Run(runCtx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-runDone; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Foundation Run: %v", err)
		}
	})
	timer := time.NewTimer(deadline)
	defer timer.Stop()
	poll := time.NewTicker(20 * time.Millisecond)
	defer poll.Stop()
	for {
		page, _, err := foundation.Distribution(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Statements) > 0 {
			return page
		}
		select {
		case <-timer.C:
			t.Fatal("Foundation did not publish the distribution statement before the deadline")
		case <-poll.C:
		}
	}
}

func flowHTTP(ctx context.Context, method, endpoint string, body []byte) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return 0, nil, err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := flowClient.Do(request)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(response.Body)
	return response.StatusCode, data, err
}

type flowStatusTransport struct {
	base     http.RoundTripper
	mu       sync.Mutex
	statuses map[string]int
}

func (transport *flowStatusTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	response, err := transport.base.RoundTrip(request)
	if err == nil {
		transport.mu.Lock()
		transport.statuses[request.URL.Path] = response.StatusCode
		transport.mu.Unlock()
	}
	return response, err
}

func (transport *flowStatusTransport) status(path string) int {
	transport.mu.Lock()
	defer transport.mu.Unlock()
	return transport.statuses[path]
}
