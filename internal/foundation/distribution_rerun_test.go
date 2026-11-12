package foundation

import (
	"context"
	"encoding/json"
	"testing"

	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

func TestDistRerunAfterPartialPayoutPaysTheFrozenPlan(t *testing.T) {
	fixture := distTestQuarterFixture(t, -1, false)
	service := fixture.service
	// Payouts go in DID order, which for the devnet monitors is 1, 0, 3, 2.
	monitor0 := keys.DIDOf(devnet.MonitorKey(0))
	monitor1 := keys.DIDOf(devnet.MonitorKey(1))
	monitor2 := keys.DIDOf(devnet.MonitorKey(2))
	monitor3 := keys.DIDOf(devnet.MonitorKey(3))
	address := service.cfg.MonitorPayouts[monitor2]
	delete(service.cfg.MonitorPayouts, monitor2)
	*fixture.now = fixture.cutoff
	if err := service.distRunDue(context.Background(), fixture.cutoff); err == nil {
		t.Fatal("distRunDue succeeded without a payout address for a paid monitor")
	}
	paid := func(t *testing.T) []faux.Transfer {
		t.Helper()
		transfers, err := fixture.chain.Transfers(context.Background(), faux.Query{Network: wire.BitcoinMainnet, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		return transfers
	}
	first := paid(t)
	t.Run("three monitors are paid before the run stops", func(t *testing.T) {
		if len(first) != 3 {
			t.Fatalf("transfers = %d; want 3", len(first))
		}
		if _, exists := service.DistributionStatement(wire.BitcoinMainnet, wire.FormatTime(fixture.quarterEnd)); exists {
			t.Error("a statement was published before every payout was made")
		}
	})
	fixture.stopServers()
	service.cfg.MonitorPayouts[monitor2] = address
	service.cfg.MonitorPayouts[monitor0] = map[string]string{wire.BitcoinMainnet: devnet.Recipient("monitor/0-moved", wire.BitcoinMainnet)}
	if err := service.distRunDue(context.Background(), fixture.cutoff); err != nil {
		t.Fatalf("rerun with the evidence offline = %v; want the frozen plan paid", err)
	}
	raw, exists := service.DistributionStatement(wire.BitcoinMainnet, wire.FormatTime(fixture.quarterEnd))
	if !exists {
		t.Fatal("Bitcoin distribution statement was not published by the rerun")
	}
	var statement wire.DistributionStatement
	if err := json.Unmarshal(raw, &statement); err != nil {
		t.Fatal(err)
	}
	t.Run("the rerun makes only the missing transfer", func(t *testing.T) {
		all := paid(t)
		if len(all) != 4 || all[0].TxID != first[0].TxID || all[1].TxID != first[1].TxID || all[2].TxID != first[2].TxID {
			t.Fatalf("transfers = %+v; want the first three and one more", all)
		}
	})
	t.Run("the statement names the transfers that were made", func(t *testing.T) {
		all := paid(t)
		payout := make(map[string]string)
		for _, payment := range statement.MonitorPayments {
			if payment.Payout != nil && payment.AmountBaseUnits == "100000" {
				payout[payment.MonitorID] = payment.Payout.TxID
			}
		}
		if len(statement.MonitorPayments) != 5 || len(payout) != 4 {
			t.Fatalf("monitor payments = %+v; want four seats paid 100000 and one without a payout", statement.MonitorPayments)
		}
		if payout[monitor1] != all[0].TxID || payout[monitor0] != all[1].TxID || payout[monitor3] != all[2].TxID || payout[monitor2] != all[3].TxID {
			t.Errorf("payouts = %+v; want each monitor's recorded transfer", payout)
		}
	})
	t.Run("the other network's statement is published from its frozen plan", func(t *testing.T) {
		if _, exists := service.DistributionStatement(wire.EthereumMainnet, wire.FormatTime(fixture.quarterEnd)); !exists {
			t.Error("Ethereum distribution statement was not published")
		}
	})
}
