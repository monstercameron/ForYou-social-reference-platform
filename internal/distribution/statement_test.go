package distribution

import (
	"encoding/json"
	"math/big"
	"os"
	"reflect"
	"testing"
	"time"

	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

type statementFixture struct {
	FoundationID string `json:"foundation_id"`
	PeriodStart  string `json:"period_start"`
	PeriodEnd    string `json:"period_end"`
	CutOff       string `json:"cut_off"`
	Network      string `json:"network"`
	Policy       struct {
		PolicyID          string   `json:"policy_id"`
		Revision          int64    `json:"revision"`
		VaultShareBP      int64    `json:"vault_share_bp"`
		MonitorShareBP    int64    `json:"monitor_share_bp"`
		MaxRetentionYears int64    `json:"max_retention_years"`
		MinReports        int64    `json:"min_reports"`
		Monitors          []string `json:"monitors"`
	} `json:"policy"`
	Providers []fixtureProvider `json:"providers"`
}

type fixtureProvider struct {
	ProviderID      string  `json:"provider_id"`
	SettledFees     string  `json:"settled_fees"`
	RetentionSecond []int64 `json:"retention_seconds"`
	PayoutRecipient string  `json:"payout_recipient"`
	Reports         []struct {
		ID           string `json:"id"`
		MonitorIndex int    `json:"monitor_index"`
		StartDay     int    `json:"start_day"`
		EndDay       int    `json:"end_day"`
		Score        int    `json:"score"`
	} `json:"reports"`
}

func exampleInput(t *testing.T) (Input, string) {
	t.Helper()
	data, err := os.ReadFile("testdata/statement_inputs.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture statementFixture
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	start, err := time.Parse(time.RFC3339, fixture.PeriodStart)
	if err != nil {
		t.Fatal(err)
	}
	end, err := time.Parse(time.RFC3339, fixture.PeriodEnd)
	if err != nil {
		t.Fatal(err)
	}
	cutoff, err := time.Parse(time.RFC3339, fixture.CutOff)
	if err != nil {
		t.Fatal(err)
	}
	policy := wire.DistributionPolicy{PolicyID: fixture.Policy.PolicyID, Revision: fixture.Policy.Revision, VaultShareBP: fixture.Policy.VaultShareBP, MonitorShareBP: fixture.Policy.MonitorShareBP, MaxRetentionYears: fixture.Policy.MaxRetentionYears, MinReports: fixture.Policy.MinReports, Monitors: fixture.Policy.Monitors}
	in := Input{Policy: policy, Network: fixture.Network, Start: start, End: end, Day: 24 * time.Hour, CutOff: cutoff, Seats: make(map[string]SeatEvidence)}
	addMember := func(id string) {
		in.Members = append(in.Members, MemberSpan{MonitorID: id, Start: start, End: end})
		in.Seats[id] = SeatEvidence{}
	}
	addMember(policy.Monitors[0])
	addMember(policy.Monitors[1])
	addMember(policy.Monitors[2])
	addMember(policy.Monitors[3])
	addMember(policy.Monitors[4])
	providerInput := func(provider fixtureProvider) {
		fees, ok := new(big.Int).SetString(provider.SettledFees, 10)
		if !ok {
			t.Fatalf("invalid fees %q", provider.SettledFees)
		}
		item := ProviderInput{ChainID: provider.ProviderID, SettledFees: fees, DeclaredSeconds: provider.RetentionSecond, PayoutRecipient: provider.PayoutRecipient}
		addReport := func(index int) {
			report := provider.Reports[index]
			score := report.Score
			item.Reports = append(item.Reports, ReportSpan{ReportID: report.ID, MonitorID: policy.Monitors[report.MonitorIndex], Start: start.Add(time.Duration(report.StartDay) * 24 * time.Hour), End: start.Add(time.Duration(report.EndDay) * 24 * time.Hour), CreatedAt: cutoff, Score: &score})
		}
		addReport(0)
		addReport(1)
		addReport(2)
		addReport(3)
		in.Providers = append(in.Providers, item)
	}
	providerInput(fixture.Providers[0])
	providerInput(fixture.Providers[1])
	return in, fixture.FoundationID
}

func entryFor(t *testing.T, statement wire.DistributionStatement, id string) wire.DistributionEntry {
	t.Helper()
	if len(statement.Entries) > 0 && statement.Entries[0].ProviderID == id {
		return statement.Entries[0]
	}
	if len(statement.Entries) > 1 && statement.Entries[1].ProviderID == id {
		return statement.Entries[1]
	}
	t.Fatalf("statement has no entry for %s", id)
	return wire.DistributionEntry{}
}

func assemble(t *testing.T, plan Plan) wire.DistributionStatement {
	t.Helper()
	statement, err := plan.Statement("foundation", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return statement
}

func testPolicy() wire.DistributionPolicy {
	return wire.DistributionPolicy{PolicyID: "policy", VaultShareBP: 5000, MonitorShareBP: 500, MaxRetentionYears: 10, MinReports: 3, Monitors: []string{"m0", "m1", "m2", "m3", "m4"}}
}

func quarterInput(network string) Input {
	start := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2027, 4, 1, 0, 0, 0, 0, time.UTC)
	in := Input{Policy: testPolicy(), Network: network, Start: start, End: end, Day: 24 * time.Hour, CutOff: end.Add(14 * 24 * time.Hour), Seats: make(map[string]SeatEvidence)}
	addMember := func(id string) {
		in.Members = append(in.Members, MemberSpan{MonitorID: id, Start: start, End: end})
		in.Seats[id] = SeatEvidence{Counted: 1}
	}
	addMember(in.Policy.Monitors[0])
	addMember(in.Policy.Monitors[1])
	addMember(in.Policy.Monitors[2])
	addMember(in.Policy.Monitors[3])
	addMember(in.Policy.Monitors[4])
	return in
}

func fullReports(in Input, score int, first, last int) []ReportSpan {
	makeReport := func(id string) ReportSpan {
		value := score
		return ReportSpan{ReportID: id, MonitorID: id, Start: in.Start.Add(time.Duration(first) * in.Day), End: in.Start.Add(time.Duration(last) * in.Day), CreatedAt: in.CutOff, Score: &value}
	}
	return []ReportSpan{makeReport(in.Policy.Monitors[0]), makeReport(in.Policy.Monitors[1]), makeReport(in.Policy.Monitors[2]), makeReport(in.Policy.Monitors[3]), makeReport(in.Policy.Monitors[4])}
}

func markSeatsUnwitnessed(in *Input) {
	in.Seats[in.Policy.Monitors[0]] = SeatEvidence{Counted: 1}
	in.Seats[in.Policy.Monitors[1]] = SeatEvidence{Counted: 1}
	in.Seats[in.Policy.Monitors[2]] = SeatEvidence{Counted: 1}
	in.Seats[in.Policy.Monitors[3]] = SeatEvidence{Counted: 1}
	in.Seats[in.Policy.Monitors[4]] = SeatEvidence{Counted: 1}
}

func TestComputeAndAssemble(t *testing.T) {
	in, foundationID := exampleInput(t)
	plan, err := Compute(in)
	if err != nil {
		t.Fatal(err)
	}
	statement, err := plan.Statement(foundationID, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	entryA := entryFor(t, statement, in.Providers[0].ChainID)
	if !reflect.DeepEqual(entryA.ReportIDs, []string{"a0", "a1", "a2", "a3"}) {
		t.Errorf("provider A report IDs = %v; want sorted filtered reports", entryA.ReportIDs)
	}
	entryA.ReportIDs = []string{}
	entryB := entryFor(t, statement, in.Providers[1].ChainID)
	entryB.ReportIDs = []string{}
	statement.Entries = []wire.DistributionEntry{entryA, entryB}
	seed := []byte{128, 129, 130, 131, 132, 133, 134, 135, 136, 137, 138, 139, 140, 141, 142, 143, 144, 145, 146, 147, 148, 149, 150, 151, 152, 153, 154, 155, 156, 157, 158, 159}
	key, err := keys.NewKeyFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := SignStatement(statement, key)
	if err != nil {
		t.Fatal(err)
	}
	if signed.StatementID != "fyds2:79bd581f9293f49eac5b1e71e4ebf3bbda26e5477b1aba9183b87d388e7f1b81" {
		t.Errorf("statement ID = %s; want example ID", signed.StatementID)
	}
	if signed.FoundationSignature != "20r6vXKWaoyEtr4IsVSYjsz08hzDc1e4vMeUq9IAIvCtIm330wjeu7QHTa7aaBU8Dn3hhBmVQL3mPy1hsrs5AQ" {
		t.Errorf("signature = %s; want example signature", signed.FoundationSignature)
	}
	if err := CheckStatementID(signed); err != nil {
		t.Errorf("CheckStatementID() error = %v", err)
	}
}

func TestQualityCoverageAndCommitteeFiltering(t *testing.T) {
	t.Run("new provider activity permits two thirds of overlap", func(t *testing.T) {
		in := quarterInput(wire.BitcoinMainnet)
		in.Providers = []ProviderInput{{ChainID: "p", SettledFees: big.NewInt(1), DeclaredSeconds: []int64{31_536_000}, ActiveFrom: in.Start.Add(60 * in.Day), Reports: fullReports(in, 9000, 60, 90)}}
		plan, err := Compute(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := entryFor(t, assemble(t, plan), "p").QualityBP; got != 9000 {
			t.Errorf("quality_bp = %d; want 9000", got)
		}
	})
	t.Run("full quarter requires sixty days despite late report", func(t *testing.T) {
		in := quarterInput(wire.BitcoinMainnet)
		in.Providers = []ProviderInput{{ChainID: "p", SettledFees: big.NewInt(1), DeclaredSeconds: []int64{31_536_000}, Reports: fullReports(in, 9000, 60, 90)}}
		plan, err := Compute(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := entryFor(t, assemble(t, plan), "p").QualityBP; got != 0 {
			t.Errorf("quality_bp = %d; want 0", got)
		}
	})
	t.Run("shortened activity still requires twenty days", func(t *testing.T) {
		in := quarterInput(wire.BitcoinMainnet)
		in.Providers = []ProviderInput{{ChainID: "p", SettledFees: big.NewInt(1), DeclaredSeconds: []int64{31_536_000}, ActiveFrom: in.Start.Add(60 * in.Day), Reports: fullReports(in, 9000, 60, 79)}}
		plan, err := Compute(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := entryFor(t, assemble(t, plan), "p").QualityBP; got != 0 {
			t.Errorf("quality_bp = %d; want 0", got)
		}
	})
	t.Run("two committee reports are below minimum", func(t *testing.T) {
		in := quarterInput(wire.BitcoinMainnet)
		reports := fullReports(in, 9000, 0, 90)[:2]
		in.Providers = []ProviderInput{{ChainID: "p", SettledFees: big.NewInt(1), DeclaredSeconds: []int64{31_536_000}, Reports: reports}}
		plan, err := Compute(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := entryFor(t, assemble(t, plan), "p").QualityBP; got != 0 {
			t.Errorf("quality_bp = %d; want 0", got)
		}
	})
	t.Run("noncommittee report is omitted", func(t *testing.T) {
		in := quarterInput(wire.BitcoinMainnet)
		reports := fullReports(in, 9000, 0, 90)[:2]
		score := 9000
		reports = append(reports, ReportSpan{ReportID: "outsider", MonitorID: "outsider", Start: in.Start, End: in.End, CreatedAt: in.CutOff, Score: &score})
		in.Providers = []ProviderInput{{ChainID: "p", SettledFees: big.NewInt(1), DeclaredSeconds: []int64{31_536_000}, Reports: reports}}
		plan, err := Compute(in)
		if err != nil {
			t.Fatal(err)
		}
		entry := entryFor(t, assemble(t, plan), "p")
		if entry.QualityBP != 0 || !reflect.DeepEqual(entry.ReportIDs, []string{"m0", "m1"}) {
			t.Errorf("entry quality/report IDs = %d/%v; want 0 and only committee reports", entry.QualityBP, entry.ReportIDs)
		}
	})
	t.Run("out of quarter and late reports are filtered", func(t *testing.T) {
		in := quarterInput(wire.BitcoinMainnet)
		reports := fullReports(in, 9000, 0, 90)[:3]
		score := 9000
		reports = append(reports,
			ReportSpan{ReportID: "before", MonitorID: "m3", Start: in.Start.Add(-in.Day), End: in.Start, CreatedAt: in.CutOff, Score: &score},
			ReportSpan{ReportID: "after", MonitorID: "m3", Start: in.End, End: in.End.Add(in.Day), CreatedAt: in.CutOff, Score: &score},
			ReportSpan{ReportID: "late", MonitorID: "m4", Start: in.Start, End: in.End, CreatedAt: in.CutOff.Add(time.Second), Score: &score},
		)
		in.Providers = []ProviderInput{{ChainID: "p", SettledFees: big.NewInt(1), DeclaredSeconds: []int64{31_536_000}, Reports: reports}}
		plan, err := Compute(in)
		if err != nil {
			t.Fatal(err)
		}
		entry := entryFor(t, assemble(t, plan), "p")
		if entry.QualityBP != 9000 || !reflect.DeepEqual(entry.ReportIDs, []string{"m0", "m1", "m2"}) {
			t.Errorf("filtered entry quality/report IDs = %d/%v; want 9000 and m0-m2", entry.QualityBP, entry.ReportIDs)
		}
	})
	t.Run("lower median resists one low monitor", func(t *testing.T) {
		in := quarterInput(wire.BitcoinMainnet)
		reports := fullReports(in, 9000, 0, 90)
		low := 0
		reports[0].Score = &low
		high := 9100
		reports[2].Score = &high
		in.Providers = []ProviderInput{{ChainID: "p", SettledFees: big.NewInt(1), DeclaredSeconds: []int64{31_536_000}, Reports: reports}}
		plan, err := Compute(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := entryFor(t, assemble(t, plan), "p").QualityBP; got != 9000 {
			t.Errorf("quality_bp = %d; want 9000", got)
		}
	})
}

func TestRetentionAndProvenQuarters(t *testing.T) {
	check := func(t *testing.T, fees int64, years []int64, prior int64, score int, wantYears, wantProven int64) {
		t.Helper()
		in := quarterInput(wire.BitcoinMainnet)
		in.Providers = []ProviderInput{{ChainID: "p", SettledFees: big.NewInt(fees), DeclaredSeconds: years, Reports: fullReports(in, score, 0, 90)}}
		if prior >= 0 {
			in.Earlier = []wire.DistributionStatement{{Entries: []wire.DistributionEntry{{ProviderID: "p", AccruedBaseUnits: "0", ProvenQuarters: prior}}}}
		}
		plan, err := Compute(in)
		if err != nil {
			t.Fatal(err)
		}
		entry := entryFor(t, assemble(t, plan), "p")
		if entry.RetentionYears != wantYears || entry.ProvenQuarters != wantProven {
			t.Errorf("retention/proven = %d/%d; want %d/%d", entry.RetentionYears, entry.ProvenQuarters, wantYears, wantProven)
		}
	}
	t.Run("new big provider proves first quarter", func(t *testing.T) { check(t, 10_000_000, []int64{315_360_000}, -1, 9000, 1, 1) })
	t.Run("tiny quarter carries prior proof", func(t *testing.T) { check(t, 10608, []int64{315_360_000}, 7, 9000, 2, 7) })
	t.Run("quality below threshold does not prove", func(t *testing.T) { check(t, 10_000_000, []int64{315_360_000}, -1, 4999, 1, 0) })
	t.Run("proof grows retention by quarter count", func(t *testing.T) { check(t, 10_000_000, []int64{315_360_000}, 7, 9000, 3, 8) })
	t.Run("shortest declaration caps retention", func(t *testing.T) { check(t, 10_000_000, []int64{315_360_000, 31_536_000}, 40, 9000, 1, 41) })
	t.Run("policy maximum caps retention", func(t *testing.T) { check(t, 10_000_000, []int64{3_153_600_000}, 40, 9000, 10, 41) })
	t.Run("skipped quarter carries most recent nonempty entry", func(t *testing.T) {
		in := quarterInput(wire.BitcoinMainnet)
		in.Providers = []ProviderInput{{ChainID: "p", SettledFees: big.NewInt(10_000_000), DeclaredSeconds: []int64{315_360_000}, Reports: fullReports(in, 9000, 0, 90)}}
		in.Earlier = []wire.DistributionStatement{{Entries: []wire.DistributionEntry{}}, {Entries: []wire.DistributionEntry{{ProviderID: "p", AccruedBaseUnits: "0", ProvenQuarters: 7}}}}
		plan, err := Compute(in)
		if err != nil {
			t.Fatal(err)
		}
		entry := entryFor(t, assemble(t, plan), "p")
		if entry.ProvenQuarters != 8 || entry.RetentionYears != 3 {
			t.Errorf("proven/retention = %d/%d; want 8/3", entry.ProvenQuarters, entry.RetentionYears)
		}
	})
	t.Run("skipped quarters before older history do not reset count", func(t *testing.T) {
		in := quarterInput(wire.BitcoinMainnet)
		in.Providers = []ProviderInput{{ChainID: "p", SettledFees: big.NewInt(10_000_000), DeclaredSeconds: []int64{315_360_000}, Reports: fullReports(in, 9000, 0, 90)}}
		in.Earlier = []wire.DistributionStatement{{Entries: []wire.DistributionEntry{}}, {Entries: []wire.DistributionEntry{{ProviderID: "p", AccruedBaseUnits: "0", ProvenQuarters: 2}}}, {Entries: []wire.DistributionEntry{{ProviderID: "p", AccruedBaseUnits: "0", ProvenQuarters: 7}}}}
		plan, err := Compute(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := entryFor(t, assemble(t, plan), "p").ProvenQuarters; got != 3 {
			t.Errorf("proven_quarters = %d; want 3", got)
		}
	})
	t.Run("carried accrual creates entry without current claims", func(t *testing.T) {
		in := quarterInput(wire.BitcoinMainnet)
		in.Earlier = []wire.DistributionStatement{{Entries: []wire.DistributionEntry{{ProviderID: "p", AccruedBaseUnits: "42", ProvenQuarters: 7}}}}
		plan, err := Compute(in)
		if err != nil {
			t.Fatal(err)
		}
		entry := entryFor(t, assemble(t, plan), "p")
		if entry.AccruedBaseUnits != "42" || entry.ProvenQuarters != 7 {
			t.Errorf("carried entry = accrued %s, proven %d; want 42 and 7", entry.AccruedBaseUnits, entry.ProvenQuarters)
		}
	})
}

func TestProviderAmountBounds(t *testing.T) {
	check := func(t *testing.T, years, proven int64, score, wantRetention int) {
		t.Helper()
		in := quarterInput(wire.BitcoinMainnet)
		in.Policy.MaxRetentionYears = 10
		in.Providers = []ProviderInput{{ChainID: "p", SettledFees: big.NewInt(10_000_000), DeclaredSeconds: []int64{years * 31_536_000}, Reports: fullReports(in, score, 0, 90)}}
		in.Earlier = []wire.DistributionStatement{{Entries: []wire.DistributionEntry{{ProviderID: "p", AccruedBaseUnits: "0", ProvenQuarters: proven}}}}
		plan, err := Compute(in)
		if err != nil {
			t.Fatal(err)
		}
		entry := entryFor(t, assemble(t, plan), "p")
		bound := big.NewInt(10_000_000 * 5000 / 10000)
		amount, ok := new(big.Int).SetString(entry.AmountBaseUnits, 10)
		if !ok || amount.Cmp(bound) > 0 {
			t.Errorf("amount = %s; must not exceed %s", entry.AmountBaseUnits, bound)
		}
		if entry.RetentionYears != int64(wantRetention) {
			t.Errorf("retention_years = %d; want %d", entry.RetentionYears, wantRetention)
		}
	}
	t.Run("large retention and proof", func(t *testing.T) { check(t, 100, 400, 10000, 10) })
	t.Run("first year", func(t *testing.T) { check(t, 1, 0, 10000, 1) })
	t.Run("near maximum proven quarters", func(t *testing.T) { check(t, 10, 39, 9999, 10) })
	t.Run("low quality at maximum retention", func(t *testing.T) { check(t, 10, 40, 1, 10) })
}

func TestProviderIndependence(t *testing.T) {
	check := func(t *testing.T, extraScore int) {
		t.Helper()
		base := quarterInput(wire.BitcoinMainnet)
		provider := ProviderInput{ChainID: "A", SettledFees: big.NewInt(10608), DeclaredSeconds: []int64{315_360_000}, Reports: fullReports(base, 9000, 0, 90)}
		base.Providers = []ProviderInput{provider}
		alone, err := Compute(base)
		if err != nil {
			t.Fatal(err)
		}
		base.Providers = append(base.Providers, ProviderInput{ChainID: "B", SettledFees: big.NewInt(10_000_000), DeclaredSeconds: []int64{31_536_000}, Reports: fullReports(base, extraScore, 0, 90)})
		withOther, err := Compute(base)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := entryFor(t, assemble(t, withOther), "A"), entryFor(t, assemble(t, alone), "A"); !reflect.DeepEqual(got, want) {
			t.Errorf("provider A entry changed with provider B: got %#v, want %#v", got, want)
		}
	}
	t.Run("other provider has low score", func(t *testing.T) { check(t, 1) })
	t.Run("other provider has perfect score", func(t *testing.T) { check(t, 10000) })
}

func TestAccrualAndBitcoinPayout(t *testing.T) {
	t.Run("below minimum accrues even with recipient", func(t *testing.T) {
		in, _ := exampleInput(t)
		markSeatsUnwitnessed(&in)
		plan, err := Compute(in)
		if err != nil {
			t.Fatal(err)
		}
		statement := assemble(t, plan)
		entry := entryFor(t, statement, in.Providers[0].ChainID)
		if entry.Payout != nil || entry.AccruedBaseUnits != entry.AmountBaseUnits || len(plan.ProviderPayments()) != 0 || statement.DistributedBaseUnits != "0" {
			t.Errorf("small accrual produced payout/payment: entry %#v, payments %v, distributed %s", entry, plan.ProviderPayments(), statement.DistributedBaseUnits)
		}
	})
	t.Run("Bitcoin pays whole satoshis and carries remainder", func(t *testing.T) {
		in := quarterInput(wire.BitcoinMainnet)
		markSeatsUnwitnessed(&in)
		reports := fullReports(in, 10000, 0, 90)
		in.Providers = []ProviderInput{{ChainID: "p", SettledFees: big.NewInt(10_000_000), DeclaredSeconds: []int64{31_536_000}, PayoutRecipient: "bc1recipient", Reports: reports}}
		in.Earlier = []wire.DistributionStatement{{Entries: []wire.DistributionEntry{{ProviderID: "p", AccruedBaseUnits: "8000123"}}}}
		plan, err := Compute(in)
		if err != nil {
			t.Fatal(err)
		}
		if got := plan.ProviderPayments()["p"].String(); got != "10750000" {
			t.Errorf("provider payment = %s; want 10750000", got)
		}
		transfers := map[string]wire.DepositTransfer{"p": {Network: wire.BitcoinMainnet, TxID: "tx"}}
		statement, err := plan.Statement("foundation", transfers, nil)
		if err != nil {
			t.Fatal(err)
		}
		entry := entryFor(t, statement, "p")
		if entry.AccruedBaseUnits != "123" || statement.DistributedBaseUnits != "10750000" {
			t.Errorf("accrual/distributed = %s/%s; want 123/10750000", entry.AccruedBaseUnits, statement.DistributedBaseUnits)
		}
	})
}

func TestMonitorSeats(t *testing.T) {
	in := quarterInput(wire.EthereumMainnet)
	in.Policy.MonitorShareBP = 1000
	in.Providers = []ProviderInput{{ChainID: "p", SettledFees: big.NewInt(5000)}}
	in.Seats = map[string]SeatEvidence{"m0": {100, 100}, "m1": {95, 100}, "m2": {94, 100}, "m3": {0, 100}, "m4": {40, 40}}
	plan, err := Compute(in)
	if err != nil {
		t.Fatal(err)
	}
	payments := plan.MonitorPayments()
	if len(payments) != 3 || payments["m0"].String() != "100" || payments["m1"].String() != "100" || payments["m4"].String() != "100" {
		t.Errorf("MonitorPayments() = %v; want m0, m1, m4 at 100", payments)
	}
	transfers := map[string]wire.DepositTransfer{"m0": {Network: wire.EthereumMainnet, TxHash: "0"}, "m1": {Network: wire.EthereumMainnet, TxHash: "1"}, "m4": {Network: wire.EthereumMainnet, TxHash: "4"}}
	statement, err := plan.Statement("foundation", nil, transfers)
	if err != nil {
		t.Fatal(err)
	}
	if len(statement.MonitorPayments) != 5 || statement.MonitorPayments[0].Payout == nil || statement.MonitorPayments[1].Payout == nil || statement.MonitorPayments[2].Payout != nil || statement.MonitorPayments[3].Payout != nil || statement.MonitorPayments[4].Payout == nil {
		t.Errorf("monitor payout flags do not match earned seats: %#v", statement.MonitorPayments)
	}
}

func TestStatementTransferValidation(t *testing.T) {
	t.Run("missing paid provider transfer rejected", func(t *testing.T) {
		in := quarterInput(wire.EthereumMainnet)
		in.Providers = []ProviderInput{{ChainID: "p", SettledFees: big.NewInt(10_000_000_000_000_000), DeclaredSeconds: []int64{31_536_000}, PayoutRecipient: "0xrecipient", Reports: fullReports(in, 10000, 0, 90)}}
		markSeatsUnwitnessed(&in)
		plan, err := Compute(in)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := plan.Statement("foundation", nil, nil); err == nil {
			t.Error("Statement() accepted missing provider transfer")
		}
	})
	t.Run("transfer for unpaid provider rejected", func(t *testing.T) {
		in := quarterInput(wire.BitcoinMainnet)
		in.Providers = []ProviderInput{{ChainID: "p", SettledFees: big.NewInt(10), DeclaredSeconds: []int64{31_536_000}}}
		plan, err := Compute(in)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := plan.Statement("foundation", map[string]wire.DepositTransfer{"p": {Network: wire.BitcoinMainnet}}, nil); err == nil {
			t.Error("Statement() accepted transfer for unpaid provider")
		}
	})
	t.Run("transfer for unpaid monitor seat rejected", func(t *testing.T) {
		in := quarterInput(wire.BitcoinMainnet)
		in.Seats["m0"] = SeatEvidence{Counted: 1}
		plan, err := Compute(in)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := plan.Statement("foundation", nil, map[string]wire.DepositTransfer{"m0": {Network: wire.BitcoinMainnet}}); err == nil {
			t.Error("Statement() accepted transfer for unpaid monitor")
		}
	})
}

func TestComputeRejectsInvalidInputs(t *testing.T) {
	t.Run("unknown network", func(t *testing.T) {
		in := quarterInput("other")
		if _, err := Compute(in); err == nil {
			t.Error("Compute() accepted unknown network")
		}
	})
	t.Run("negative settled fees", func(t *testing.T) {
		in := quarterInput(wire.BitcoinMainnet)
		in.Providers = []ProviderInput{{ChainID: "p", SettledFees: big.NewInt(-1)}}
		if _, err := Compute(in); err == nil {
			t.Error("Compute() accepted negative settled fees")
		}
	})
	t.Run("vault share below minimum", func(t *testing.T) {
		in := quarterInput(wire.BitcoinMainnet)
		in.Policy.VaultShareBP = 2499
		if _, err := Compute(in); err == nil {
			t.Error("Compute() accepted vault share 2499")
		}
	})
	t.Run("monitor share above maximum", func(t *testing.T) {
		in := quarterInput(wire.BitcoinMainnet)
		in.Policy.MonitorShareBP = 1001
		if _, err := Compute(in); err == nil {
			t.Error("Compute() accepted monitor share 1001")
		}
	})
	t.Run("fewer than five policy monitors", func(t *testing.T) {
		in := quarterInput(wire.BitcoinMainnet)
		in.Policy.Monitors = in.Policy.Monitors[:4]
		if _, err := Compute(in); err == nil {
			t.Error("Compute() accepted four policy monitors")
		}
	})
	t.Run("missing seat evidence", func(t *testing.T) {
		in := quarterInput(wire.BitcoinMainnet)
		delete(in.Seats, "m0")
		if _, err := Compute(in); err == nil {
			t.Error("Compute() accepted absent seat evidence")
		}
	})
}
