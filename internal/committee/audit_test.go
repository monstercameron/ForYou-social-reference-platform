package committee

import (
	"math/big"
	"reflect"
	"testing"

	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

// wantFault checks the first fault Check finds, with lateness on, as the model's Monitor.check.
func wantFault(t *testing.T, m *Monitor, c wire.SettlementClaim, leaves []wire.SettlementLeaf, seenAt string, reason settlement.Reason, provable bool) {
	t.Helper()
	credit, f, err := m.Check(c, leaves, at(t, seenAt), true)
	if err != nil {
		t.Fatalf("Check() error = %v", err)
	}
	if f == nil {
		t.Fatalf("Check() = credit %v, no fault; want %s (provable %t)", credit, reason, provable)
	}
	if f.Reason != reason || f.Provable != provable {
		t.Fatalf("Check() fault = %s (provable %t); want %s (provable %t)", f.Reason, f.Provable, reason, provable)
	}
}

func wantCredit(t *testing.T, m *Monitor, c wire.SettlementClaim, leaves []wire.SettlementLeaf, seenAt, credit string) {
	t.Helper()
	got, f, err := m.Check(c, leaves, at(t, seenAt), true)
	if err != nil || f != nil {
		t.Fatalf("Check() = fault %+v, error %v; want credit %s", f, err, credit)
	}
	if got.String() != credit {
		t.Fatalf("Check() credit = %s; want %s", got, credit)
	}
}

func TestCommitteeWitnessesExampleChain(t *testing.T) {
	w := primed(t)
	c, l := w.second(t, "second")
	if got := w.auditAll(t, c, l, "2027-01-13T12:00:00Z"); got != five("9989392") {
		t.Fatalf("second claim results = %v; want five witnesses with credit 10000000 - 10608", got)
	}
	if c.CumulativeBaseUnits != w.example.SecondClaim.CumulativeBaseUnits || w.example.SecondWitnesses[0].CreditBaseUnits != "9989392" {
		t.Fatalf("second claim cumulative %s does not match the example's second claim", c.CumulativeBaseUnits)
	}
}

func TestCheckRefusals(t *testing.T) {
	t.Run("altered total is bad_signature", func(t *testing.T) {
		w := primed(t)
		c, l := w.second(t, "third")
		c.TotalBaseUnits = "1"
		wantFault(t, w.monitors[0], c, l, "2027-01-13T12:00:00Z", settlement.ReasonBadSignature, true)
	})
	t.Run("period overlapping the previous one is bad_period", func(t *testing.T) {
		w := primed(t)
		c, l := w.claim(t, claimSpec{previous: &w.example.Claim, start: "2027-01-05T12:00:00Z", end: "2027-01-06T12:00:00Z",
			leaves: []wire.SettlementLeaf{leaf("third", w.btcSchedule, "136", "2027-01-05T18:00:00Z")}})
		wantFault(t, w.monitors[0], c, l, "2027-01-06T13:00:00Z", settlement.ReasonBadPeriod, true)
	})
	t.Run("period of 24 hours and one second is bad_period", func(t *testing.T) {
		w := primed(t)
		c, l := w.second(t, "third")
		c.PeriodEnd = "2027-01-13T00:00:01Z"
		c = resign(t, w.provider, c)
		wantFault(t, w.monitors[0], c, l, "2027-01-13T12:00:00Z", settlement.ReasonBadPeriod, true)
	})
	t.Run("seen before period_end is bad_period", func(t *testing.T) {
		w := primed(t)
		c, l := w.second(t, "third")
		wantFault(t, w.monitors[0], c, l, "2027-01-12T23:59:59Z", settlement.ReasonBadPeriod, false)
	})
	t.Run("seen exactly 90 days after period_end is in time", func(t *testing.T) {
		w := primed(t)
		c, l := w.second(t, "third")
		wantCredit(t, w.monitors[0], c, l, "2027-04-13T00:00:00Z", "9989392")
	})
	t.Run("seen more than 90 days after period_end is bad_period", func(t *testing.T) {
		w := primed(t)
		c, l := w.second(t, "third")
		wantFault(t, w.monitors[0], c, l, "2027-04-13T00:00:01Z", settlement.ReasonBadPeriod, false)
	})
	t.Run("first claim whose period is years in the past is bad_period", func(t *testing.T) {
		w := newWorld(t)
		c, l := w.claim(t, claimSpec{start: "2017-03-01T00:00:00Z", end: "2017-03-02T00:00:00Z",
			leaves: []wire.SettlementLeaf{leaf("ancient", w.btcSchedule, "136", "2017-03-01T06:00:00Z")}})
		wantFault(t, w.monitors[0], c, l, "2027-01-06T12:00:00Z", settlement.ReasonBadPeriod, false)
	})
	t.Run("leaf issued outside the period is bad_leaf", func(t *testing.T) {
		w := primed(t)
		c, l := w.claim(t, claimSpec{previous: &w.example.Claim, start: "2027-01-12T00:00:00Z", end: "2027-01-13T00:00:00Z",
			leaves: []wire.SettlementLeaf{leaf("third", w.btcSchedule, "136", "2027-01-13T00:00:00Z")}})
		wantFault(t, w.monitors[0], c, l, "2027-01-13T12:00:00Z", settlement.ReasonBadLeaf, true)
	})
	t.Run("amount 137 is not a possible fee and is bad_leaf", func(t *testing.T) {
		w := primed(t)
		c, l := w.claim(t, claimSpec{previous: &w.example.Claim, start: "2027-01-12T00:00:00Z", end: "2027-01-13T00:00:00Z",
			leaves: []wire.SettlementLeaf{leaf("odd", w.btcSchedule, "137", "2027-01-12T06:00:00Z")}})
		wantFault(t, w.monitors[0], c, l, "2027-01-13T12:00:00Z", settlement.ReasonBadLeaf, true)
	})
	t.Run("schedule not in force at issued_at is bad_leaf", func(t *testing.T) {
		w := newWorld(t)
		c, l := w.claim(t, claimSpec{start: "2026-12-31T00:00:00Z", end: "2027-01-01T00:00:00Z",
			leaves: []wire.SettlementLeaf{leaf("early", w.btcSchedule, "136", "2026-12-31T12:00:00Z")}})
		wantFault(t, w.monitors[0], c, l, "2027-01-01T12:00:00Z", settlement.ReasonBadLeaf, true)
	})
	t.Run("schedule of another network is bad_leaf", func(t *testing.T) {
		w := primed(t)
		c, l := w.claim(t, claimSpec{previous: &w.example.Claim, start: "2027-01-12T00:00:00Z", end: "2027-01-13T00:00:00Z",
			leaves: []wire.SettlementLeaf{leaf("ether", w.ethSchedule, "48900000000", "2027-01-12T06:00:00Z")}})
		wantFault(t, w.monitors[0], c, l, "2027-01-13T12:00:00Z", settlement.ReasonBadLeaf, true)
	})
	t.Run("schedule the monitor does not hold is bad_leaf, not provable", func(t *testing.T) {
		w := primed(t)
		c, l := w.claim(t, claimSpec{previous: &w.example.Claim, start: "2027-01-12T00:00:00Z", end: "2027-01-13T00:00:00Z",
			leaves: []wire.SettlementLeaf{leaf("unheld", "fyfs2:0000000000000000000000000000000000000000000000000000000000000000", "136", "2027-01-12T06:00:00Z")}})
		wantFault(t, w.monitors[0], c, l, "2027-01-13T12:00:00Z", settlement.ReasonBadLeaf, false)
	})
	t.Run("unwitnessed predecessor is bad_chain, not provable", func(t *testing.T) {
		w := primed(t)
		second, _ := w.second(t, "never audited")
		c, l := w.claim(t, claimSpec{previous: &second, start: "2027-01-16T00:00:00Z", end: "2027-01-17T00:00:00Z",
			leaves: []wire.SettlementLeaf{leaf("third", w.btcSchedule, "136", "2027-01-16T06:00:00Z")}})
		wantFault(t, w.monitors[0], c, l, "2027-01-17T12:00:00Z", settlement.ReasonBadChain, false)
	})
	t.Run("receipt hash claimed at an earlier sequence is duplicate_receipt", func(t *testing.T) {
		w := primed(t)
		dup := w.example.Leaves[0]
		dup.IssuedAt = "2027-01-12T06:00:00Z"
		c, l := w.claim(t, claimSpec{previous: &w.example.Claim, start: "2027-01-12T00:00:00Z", end: "2027-01-13T00:00:00Z",
			leaves: []wire.SettlementLeaf{dup}})
		wantFault(t, w.monitors[0], c, l, "2027-01-13T12:00:00Z", settlement.ReasonDuplicateReceipt, true)
	})
	t.Run("receipt count off by one is bad_totals", func(t *testing.T) {
		w := primed(t)
		c, l := w.second(t, "third")
		c.ReceiptCount = 2
		c = resign(t, w.provider, c)
		wantFault(t, w.monitors[0], c, l, "2027-01-13T12:00:00Z", settlement.ReasonBadTotals, true)
	})
	t.Run("too little deposited is unfunded, not provable", func(t *testing.T) {
		w := newWorld(t)
		w.deposits.set(keys.DIDOf(w.provider), btc, 10_000)
		wantFault(t, w.monitors[0], w.example.Claim, w.example.Leaves, "2027-01-06T12:00:00Z", settlement.ReasonUnfunded, false)
	})
}

func TestUnfundedClaimIsWitnessedAfterDeposit(t *testing.T) {
	w := newWorld(t)
	w.deposits.set(keys.DIDOf(w.provider), btc, 10_000)
	if got := w.auditAll(t, w.example.Claim, w.example.Leaves, "2027-01-06T12:00:00Z"); got != five("refused unfunded") {
		t.Fatalf("results before the deposit = %v", got)
	}
	w.deposits.set(keys.DIDOf(w.provider), btc, 30_000)
	if got := w.auditAll(t, w.example.Claim, w.example.Leaves, "2027-01-07T12:00:00Z"); got != five("19528") {
		t.Fatalf("results after the deposit = %v; want five witnesses with credit 30000 - 10472", got)
	}
}

func TestDeclarationRule(t *testing.T) {
	t.Run("declaration effective after period_start is provable bad_period", func(t *testing.T) {
		w := primed(t)
		late := w.declare(t, w.provider, "2027-01-12T00:00:01Z")
		if err := w.monitors[0].RegisterDeclaration(late); err != nil {
			t.Fatal(err)
		}
		c, l := w.claim(t, claimSpec{previous: &w.example.Claim, start: "2027-01-12T00:00:00Z", end: "2027-01-13T00:00:00Z", declaration: late.DeclarationID,
			leaves: []wire.SettlementLeaf{leaf("third", w.btcSchedule, "136", "2027-01-12T06:00:00Z")}})
		wantFault(t, w.monitors[0], c, l, "2027-01-13T12:00:00Z", settlement.ReasonBadPeriod, true)
	})
	t.Run("declaration backdated over the claimed period is provable bad_period", func(t *testing.T) {
		w := primed(t)
		backdated := w.declare(t, w.provider, "2027-01-05T12:00:00Z")
		if err := w.monitors[0].RegisterDeclaration(backdated); err != nil {
			t.Fatal(err)
		}
		c, l := w.claim(t, claimSpec{previous: &w.example.Claim, start: "2027-01-12T00:00:00Z", end: "2027-01-13T00:00:00Z", declaration: backdated.DeclarationID,
			leaves: []wire.SettlementLeaf{leaf("third", w.btcSchedule, "136", "2027-01-12T06:00:00Z")}})
		wantFault(t, w.monitors[0], c, l, "2027-01-13T12:00:00Z", settlement.ReasonBadPeriod, true)
	})
	t.Run("new declaration effective between the claims is accepted", func(t *testing.T) {
		w := primed(t)
		next := w.declare(t, w.provider, "2027-01-10T00:00:00Z")
		if err := w.monitors[0].RegisterDeclaration(next); err != nil {
			t.Fatal(err)
		}
		c, l := w.claim(t, claimSpec{previous: &w.example.Claim, start: "2027-01-12T00:00:00Z", end: "2027-01-13T00:00:00Z", declaration: next.DeclarationID,
			leaves: []wire.SettlementLeaf{leaf("third", w.btcSchedule, "136", "2027-01-12T06:00:00Z")}})
		wantCredit(t, w.monitors[0], c, l, "2027-01-13T12:00:00Z", "9989392")
	})
	t.Run("another provider's declaration is provable bad_period", func(t *testing.T) {
		w := primed(t)
		c, l := w.claim(t, claimSpec{previous: &w.example.Claim, start: "2027-01-12T00:00:00Z", end: "2027-01-13T00:00:00Z", declaration: w.declB.DeclarationID,
			leaves: []wire.SettlementLeaf{leaf("borrowed", w.btcSchedule, "136", "2027-01-12T06:00:00Z")}})
		wantFault(t, w.monitors[0], c, l, "2027-01-13T12:00:00Z", settlement.ReasonBadPeriod, true)
	})
	t.Run("unknown declaration is bad_period, not provable", func(t *testing.T) {
		w := primed(t)
		c, l := w.claim(t, claimSpec{previous: &w.example.Claim, start: "2027-01-12T00:00:00Z", end: "2027-01-13T00:00:00Z",
			declaration: "fyrd2:abababababababababababababababababababababababababababababababab",
			leaves:      []wire.SettlementLeaf{leaf("unknown", w.btcSchedule, "136", "2027-01-12T06:00:00Z")}})
		wantFault(t, w.monitors[0], c, l, "2027-01-13T12:00:00Z", settlement.ReasonBadPeriod, false)
	})
}

// TestKeySuccession follows the model's test_gaps_and_key_succession: PROVIDER hands its chain to PROVIDER_NEXT.
func TestKeySuccession(t *testing.T) {
	w := primed(t)
	next := keys.DIDOf(w.providerNext)
	s := succeed(t, w.provider, w.providerNext, "2027-01-07T00:00:00Z")
	declNext := w.declare(t, w.providerNext, "2027-01-07T00:00:00Z")
	if err := w.monitors[0].RegisterDeclaration(declNext); err != nil {
		t.Fatal(err)
	}
	if err := w.monitors[1].RegisterDeclaration(declNext); err != nil {
		t.Fatal(err)
	}
	if err := w.monitors[2].RegisterDeclaration(declNext); err != nil {
		t.Fatal(err)
	}
	if err := w.monitors[3].RegisterDeclaration(declNext); err != nil {
		t.Fatal(err)
	}
	if err := w.monitors[4].RegisterDeclaration(declNext); err != nil {
		t.Fatal(err)
	}
	newLeaves := []wire.SettlementLeaf{leaf("new key", w.btcSchedule, "136", "2027-01-08T06:00:00Z")}
	c2, l2 := w.claim(t, claimSpec{key: w.providerNext, previous: &w.example.Claim, start: "2027-01-08T00:00:00Z", end: "2027-01-09T00:00:00Z",
		declaration: declNext.DeclarationID, succession: &s.SuccessionID, leaves: newLeaves})

	t.Run("new-key claim before the succession is fetched is unknown_provider_key, not provable", func(t *testing.T) {
		wantFault(t, w.monitors[0], c2, l2, "2027-01-09T12:00:00Z", settlement.ReasonUnknownProviderKey, false)
	})
	if err := w.monitors[0].RegisterSuccession(s); err != nil {
		t.Fatal(err)
	}
	if err := w.monitors[1].RegisterSuccession(s); err != nil {
		t.Fatal(err)
	}
	if err := w.monitors[2].RegisterSuccession(s); err != nil {
		t.Fatal(err)
	}
	if err := w.monitors[3].RegisterSuccession(s); err != nil {
		t.Fatal(err)
	}
	if err := w.monitors[4].RegisterSuccession(s); err != nil {
		t.Fatal(err)
	}
	t.Run("registering the same succession again is a no-op", func(t *testing.T) {
		if err := w.monitors[0].RegisterSuccession(s); err != nil {
			t.Fatalf("RegisterSuccession(again) = %v", err)
		}
	})
	t.Run("the new key belongs to the first key's line", func(t *testing.T) {
		if got := w.monitors[0].Line(next); got != keys.DIDOf(w.provider) {
			t.Fatalf("Line(PROVIDER_NEXT) = %s; want PROVIDER", got)
		}
	})
	t.Run("new key naming no succession is provable unknown_provider_key", func(t *testing.T) {
		c, l := w.claim(t, claimSpec{key: w.providerNext, previous: &w.example.Claim, start: "2027-01-08T00:00:00Z", end: "2027-01-09T00:00:00Z",
			declaration: declNext.DeclarationID, leaves: newLeaves})
		wantFault(t, w.monitors[0], c, l, "2027-01-09T12:00:00Z", settlement.ReasonUnknownProviderKey, true)
	})
	t.Run("new key keeping the old key's declaration is provable bad_period", func(t *testing.T) {
		c, l := w.claim(t, claimSpec{key: w.providerNext, previous: &w.example.Claim, start: "2027-01-08T00:00:00Z", end: "2027-01-09T00:00:00Z",
			succession: &s.SuccessionID, leaves: newLeaves})
		wantFault(t, w.monitors[0], c, l, "2027-01-09T12:00:00Z", settlement.ReasonBadPeriod, true)
	})
	t.Run("all five witness the first new-key claim against the first key's deposits", func(t *testing.T) {
		if got := w.auditAll(t, c2, l2, "2027-01-09T12:00:00Z"); got != five("9989392") {
			t.Fatalf("results = %v; want five witnesses with credit 10000000 - 10608", got)
		}
		if w.deposits.asked[next] != 0 || w.deposits.asked[keys.DIDOf(w.provider)] == 0 {
			t.Fatalf("deposits asked for %v; want the first key only", w.deposits.asked)
		}
	})
	thirdLeaves := []wire.SettlementLeaf{leaf("second claim of the new key", w.btcSchedule, "136", "2027-01-10T06:00:00Z")}
	t.Run("second new-key claim naming the succession again is provable unknown_provider_key", func(t *testing.T) {
		c, l := w.claim(t, claimSpec{key: w.providerNext, previous: &c2, start: "2027-01-10T00:00:00Z", end: "2027-01-11T00:00:00Z",
			declaration: declNext.DeclarationID, succession: &s.SuccessionID, leaves: thirdLeaves})
		wantFault(t, w.monitors[0], c, l, "2027-01-11T12:00:00Z", settlement.ReasonUnknownProviderKey, true)
	})
	c3, l3 := w.claim(t, claimSpec{key: w.providerNext, previous: &c2, start: "2027-01-10T00:00:00Z", end: "2027-01-11T00:00:00Z",
		declaration: declNext.DeclarationID, leaves: thirdLeaves})
	t.Run("second new-key claim without a succession is witnessed by all five", func(t *testing.T) {
		if got := w.auditAll(t, c3, l3, "2027-01-11T12:00:00Z"); got != five("9989256") {
			t.Fatalf("results = %v; want five witnesses with credit 10000000 - 10744", got)
		}
	})
	t.Run("old key signing after the chain moved is provable unknown_provider_key", func(t *testing.T) {
		c, l := w.claim(t, claimSpec{previous: &c3, start: "2027-01-12T00:00:00Z", end: "2027-01-13T00:00:00Z",
			leaves: []wire.SettlementLeaf{leaf("old key again", w.btcSchedule, "136", "2027-01-12T06:00:00Z")}})
		wantFault(t, w.monitors[0], c, l, "2027-01-13T12:00:00Z", settlement.ReasonUnknownProviderKey, true)
	})
	t.Run("old key cannot get a second successor", func(t *testing.T) {
		if err := w.monitors[0].RegisterSuccession(succeed(t, w.provider, w.providerB, "2027-06-01T00:00:00Z")); err == nil {
			t.Fatal("RegisterSuccession accepted a second successor of PROVIDER")
		}
	})
	t.Run("a key cannot be the successor of two keys", func(t *testing.T) {
		if err := w.monitors[0].RegisterSuccession(succeed(t, w.providerB, w.providerNext, "2027-02-01T00:00:00Z")); err == nil {
			t.Fatal("RegisterSuccession accepted PROVIDER_B -> PROVIDER_NEXT")
		}
	})
	t.Run("page by any key of the line lists the new-key witnesses", func(t *testing.T) {
		page := w.monitors[0].Page(next, btc, 1, 10)
		if len(page.Witnesses) != 2 || page.Witnesses[0].ClaimID != c2.ClaimID || page.Witnesses[1].ClaimID != c3.ClaimID {
			t.Fatalf("Page(PROVIDER_NEXT) witnesses = %+v", page.Witnesses)
		}
	})
}

func TestSuccessionOntoKeyThatSignedClaims(t *testing.T) {
	w := newWorld(t)
	w.deposits.set(keys.DIDOf(w.providerB), btc, 10_000)
	c, l := w.claim(t, claimSpec{key: w.providerB, start: "2027-01-05T00:00:00Z", end: "2027-01-06T00:00:00Z", declaration: w.declB.DeclarationID,
		leaves: []wire.SettlementLeaf{leaf("b chain", w.btcSchedule, "136", "2027-01-05T06:00:00Z")}})
	if got := result(audit(t, w.monitors[0], c, l, "2027-01-06T12:00:00Z")); got != "9864" {
		t.Fatalf("PROVIDER_B claim result = %s", got)
	}
	if err := w.monitors[0].RegisterSuccession(succeed(t, w.provider, w.providerB, "2027-02-01T00:00:00Z")); err == nil {
		t.Fatal("RegisterSuccession accepted a successor that already signed a claim")
	}
	if got := w.monitors[0].Line(keys.DIDOf(w.providerB)); got != keys.DIDOf(w.providerB) {
		t.Fatalf("Line(PROVIDER_B) = %s; want its own key", got)
	}
}

func TestOneWitnessPerSequence(t *testing.T) {
	t.Run("a fork after a witness is bad_chain and recorded as equivocation", func(t *testing.T) {
		w := primed(t)
		a, la := w.second(t, "fork a")
		b, lb := w.second(t, "fork b")
		first := audit(t, w.monitors[0], a, la, "2027-01-13T12:00:00Z")
		if result(first) != "9989392" {
			t.Fatalf("fork a = %s", result(first))
		}
		if got := result(audit(t, w.monitors[0], b, lb, "2027-01-13T12:00:00Z")); got != "refused bad_chain" {
			t.Fatalf("fork b = %s; want refused bad_chain", got)
		}
		eq := w.monitors[0].state.Equivocations
		if len(eq) != 1 || eq[0][0].ClaimID != a.ClaimID || eq[0][1].ClaimID != b.ClaimID {
			t.Fatalf("equivocations = %+v; want the pair (a, b)", eq)
		}
		again, err := w.monitors[0].Audit(a, la, at(t, "2027-01-13T12:00:00Z"), at(t, "2027-01-20T00:00:00Z"))
		if err != nil || again.Witness == nil || *again.Witness != *first.Witness {
			t.Fatalf("re-audit of a = %+v, %v; want the identical stored witness", again.Witness, err)
		}
	})
	t.Run("a tampered copy is refused and the corrected claim is witnessed", func(t *testing.T) {
		w := primed(t)
		a, la := w.second(t, "fork a")
		bad := a
		bad.TotalBaseUnits = "1"
		if got := result(audit(t, w.monitors[0], bad, la, "2027-01-13T12:00:00Z")); got != "refused bad_signature" {
			t.Fatalf("tampered = %s", got)
		}
		if got := result(audit(t, w.monitors[0], a, la, "2027-01-13T12:00:00Z")); got != "9989392" {
			t.Fatalf("corrected = %s", got)
		}
		if stored, ok := w.monitors[0].Claim(a.ClaimID); !ok || !reflect.DeepEqual(stored, a) {
			t.Fatalf("Claim() = %+v, %t; want the verified claim", stored, ok)
		}
	})
	t.Run("a tampered copy does not replace the refusal of the verified claim", func(t *testing.T) {
		w := newWorld(t)
		w.deposits.set(keys.DIDOf(w.provider), btc, 10_000)
		if got := result(audit(t, w.monitors[0], w.example.Claim, w.example.Leaves, "2027-01-06T12:00:00Z")); got != "refused unfunded" {
			t.Fatalf("first audit = %s", got)
		}
		bad := w.example.Claim
		bad.TotalBaseUnits = "1"
		if got := result(audit(t, w.monitors[0], bad, w.example.Leaves, "2027-01-06T13:00:00Z")); got != "refused bad_signature" {
			t.Fatalf("tampered = %s", got)
		}
		if stored, ok := w.monitors[0].Claim(w.example.Claim.ClaimID); !ok || !reflect.DeepEqual(stored, w.example.Claim) {
			t.Fatalf("Claim() = %+v; want the verified claim", stored)
		}
		if due := w.monitors[0].ReauditDue(at(t, "2027-01-07T12:00:00Z")); len(due) != 1 || due[0].ClaimID != w.example.Claim.ClaimID {
			t.Fatalf("ReauditDue = %+v; want the unfunded claim", due)
		}
	})
	t.Run("own hold on exactly this claim ends when it passes audit", func(t *testing.T) {
		w := primed(t)
		a, la := w.second(t, "fork a")
		m := w.monitors[0]
		hold, err := settlement.SignHold(m.key, a.ProviderID, btc, 2, []string{a.ClaimID}, at(t, "2027-01-13T01:00:00Z"))
		if err != nil {
			t.Fatal(err)
		}
		m.state.Holds[slotKey(keys.DIDOf(w.provider), btc, 2)] = hold
		if got := result(audit(t, m, a, la, "2027-01-13T12:00:00Z")); got != "9989392" {
			t.Fatalf("held claim = %s", got)
		}
		if len(m.state.Holds) != 0 {
			t.Fatalf("holds = %+v; want the hold dropped", m.state.Holds)
		}
	})
	t.Run("own hold on two claims refuses both as bad_chain", func(t *testing.T) {
		w := primed(t)
		a, la := w.second(t, "fork a")
		b, _ := w.second(t, "fork b")
		m := w.monitors[0]
		ids := []string{a.ClaimID, b.ClaimID}
		if ids[0] > ids[1] {
			ids = []string{b.ClaimID, a.ClaimID}
		}
		hold, err := settlement.SignHold(m.key, a.ProviderID, btc, 2, ids, at(t, "2027-01-13T01:00:00Z"))
		if err != nil {
			t.Fatal(err)
		}
		m.state.Holds[slotKey(keys.DIDOf(w.provider), btc, 2)] = hold
		if got := result(audit(t, m, a, la, "2027-01-13T12:00:00Z")); got != "refused bad_chain" {
			t.Fatalf("claim under a two-claim hold = %s", got)
		}
	})
}

func TestAClaimArrivingAtAHeldSlotReviewsTheHold(t *testing.T) {
	// leafFault builds a sequence-2 claim whose one leaf has an impossible amount, a fault its leaves prove, and a
	// faulty monitor's witness for it.
	leafFault := func(t *testing.T, w *world) (wire.SettlementClaim, []wire.SettlementLeaf, wire.SettlementWitness) {
		t.Helper()
		bad, bl := w.claim(t, claimSpec{previous: &w.example.Claim, start: "2027-01-12T00:00:00Z", end: "2027-01-13T00:00:00Z",
			leaves: []wire.SettlementLeaf{leaf("corrected", w.btcSchedule, "137", "2027-01-12T06:00:00Z")}})
		faulty, err := settlement.SignWitness(w.monitors[4].key, bad, big.NewInt(0), at(t, "2027-01-13T12:00:00Z"))
		if err != nil {
			t.Fatal(err)
		}
		return bad, bl, faulty
	}
	t.Run("hold on A and B with A provably bad witnesses B", func(t *testing.T) {
		w := primed(t)
		bad, bl, faulty := leafFault(t, w)
		good, gl := w.second(t, "corrected")
		honest := *audit(t, w.monitors[0], good, gl, "2027-01-13T12:00:00Z").Witness
		n := w.newcomer(t, "held-pair")
		got, err := n.Takeover([]Candidate{{Claim: bad, Leaves: bl, Witnesses: []wire.SettlementWitness{faulty}}, {Claim: good, Witnesses: []wire.SettlementWitness{honest}}},
			w.committee(), at(t, "2027-02-01T00:00:00Z"), nil, nil, nil, nil)
		wantHold(t, got, err, sortedPair(bad.ClaimID, good.ClaimID))
		if got := result(audit(t, n, good, gl, "2027-02-01T00:00:00Z")); got != "9989392" {
			t.Fatalf("B arriving with its leaves = %s; want a witness with credit 9989392", got)
		}
		if page := n.Page(good.ProviderID, btc, 1, 10); len(page.Holds) != 0 {
			t.Fatalf("page holds = %+v; want the hold ended", page.Holds)
		}
	})
	t.Run("hold on A alone with A provably bad witnesses the correction C", func(t *testing.T) {
		w := primed(t)
		bad, bl, faulty := leafFault(t, w)
		n := w.newcomer(t, "held-single")
		got, err := n.Takeover([]Candidate{{Claim: bad, Leaves: bl, Witnesses: []wire.SettlementWitness{faulty}}}, w.committee(), at(t, "2027-02-01T00:00:00Z"), nil, nil, nil, nil)
		wantHold(t, got, err, []string{bad.ClaimID})
		correction, cl := w.second(t, "corrected")
		if got := result(audit(t, n, correction, cl, "2027-02-01T00:00:00Z")); got != "9989392" {
			t.Fatalf("correction C = %s; want a witness with credit 9989392", got)
		}
		if page := n.Page(correction.ProviderID, btc, 1, 10); len(page.Holds) != 0 {
			t.Fatalf("page holds = %+v; want the hold ended", page.Holds)
		}
	})
	t.Run("hold on two claims that are not provably invalid still refuses", func(t *testing.T) {
		w := primed(t)
		a, la := w.fork(t, "claim A", "2027-01-12T06:00:00Z")
		b, lb := w.fork(t, "claim B", "2027-01-12T07:00:00Z")
		wa := *audit(t, w.monitors[0], a, la, "2027-01-13T12:00:00Z").Witness
		wb := *audit(t, w.monitors[1], b, lb, "2027-01-13T12:00:00Z").Witness
		n := w.newcomer(t, "held-equivocation")
		got, err := n.Takeover([]Candidate{{Claim: a, Leaves: la, Witnesses: []wire.SettlementWitness{wa}}, {Claim: b, Leaves: lb, Witnesses: []wire.SettlementWitness{wb}}},
			w.committee(), at(t, "2027-02-01T00:00:00Z"), nil, nil, nil, nil)
		wantHold(t, got, err, sortedPair(a.ClaimID, b.ClaimID))
		if got := result(audit(t, n, a, la, "2027-02-01T00:00:00Z")); got != "refused bad_chain" {
			t.Fatalf("A under the hold = %s; want refused bad_chain", got)
		}
		page := n.Page(a.ProviderID, btc, 1, 10)
		if len(page.Holds) != 1 || !reflect.DeepEqual(page.Holds[0].ClaimIDs, sortedPair(a.ClaimID, b.ClaimID)) || page.Holds[0].WitnessSignature != got.Hold.WitnessSignature {
			t.Fatalf("page holds = %+v; want the original hold on A and B", page.Holds)
		}
	})
	t.Run("a provably bad claim is dropped from a hold that still blocks", func(t *testing.T) {
		w := primed(t)
		bad, bl, faulty := leafFault(t, w)
		a, la := w.fork(t, "claim A", "2027-01-12T06:00:00Z")
		b, lb := w.fork(t, "claim B", "2027-01-12T07:00:00Z")
		wa := *audit(t, w.monitors[0], a, la, "2027-01-13T12:00:00Z").Witness
		wb := *audit(t, w.monitors[1], b, lb, "2027-01-13T12:00:00Z").Witness
		n := w.newcomer(t, "held-three")
		got, err := n.Takeover([]Candidate{{Claim: bad, Leaves: bl, Witnesses: []wire.SettlementWitness{faulty}},
			{Claim: a, Leaves: la, Witnesses: []wire.SettlementWitness{wa}}, {Claim: b, Leaves: lb, Witnesses: []wire.SettlementWitness{wb}}},
			w.committee(), at(t, "2027-02-01T00:00:00Z"), nil, nil, nil, nil)
		if err != nil || got.Hold == nil || len(got.Hold.ClaimIDs) != 3 {
			t.Fatalf("Takeover() = %+v, %v; want a hold on three claims", got, err)
		}
		if got := result(audit(t, n, a, la, "2027-02-02T00:00:00Z")); got != "refused bad_chain" {
			t.Fatalf("A under the hold = %s; want refused bad_chain", got)
		}
		page := n.Page(a.ProviderID, btc, 1, 10)
		if len(page.Holds) != 1 || !reflect.DeepEqual(page.Holds[0].ClaimIDs, sortedPair(a.ClaimID, b.ClaimID)) || page.Holds[0].HeldAt != "2027-02-02T00:00:00Z" {
			t.Fatalf("page holds = %+v; want a hold on A and B signed at 2027-02-02T00:00:00Z", page.Holds)
		}
		if settlement.VerifyHold(page.Holds[0]) != nil {
			t.Fatal("narrowed hold signature does not verify")
		}
	})
	t.Run("a refusal under a hold is audited again once new data proves the other claim invalid", func(t *testing.T) {
		w := primed(t)
		future := w.declare(t, w.provider, "2027-01-20T00:00:00Z")
		bad, bl := w.claim(t, claimSpec{previous: &w.example.Claim, start: "2027-01-12T00:00:00Z", end: "2027-01-13T00:00:00Z", declaration: future.DeclarationID,
			leaves: []wire.SettlementLeaf{leaf("honest mistake", w.btcSchedule, "136", "2027-01-12T06:00:00Z")}})
		good, gl := w.second(t, "honest mistake")
		faulty, err := settlement.SignWitness(w.monitors[4].key, bad, big.NewInt(0), at(t, "2027-01-13T12:00:00Z"))
		if err != nil {
			t.Fatal(err)
		}
		honest := *audit(t, w.monitors[0], good, gl, "2027-01-13T12:00:00Z").Witness
		n := w.newcomer(t, "held-until-declared")
		got, err := n.Takeover([]Candidate{{Claim: bad, Leaves: bl, Witnesses: []wire.SettlementWitness{faulty}}, {Claim: good, Leaves: gl, Witnesses: []wire.SettlementWitness{honest}}},
			w.committee(), at(t, "2027-02-01T00:00:00Z"), nil, nil, nil, nil)
		wantHold(t, got, err, sortedPair(bad.ClaimID, good.ClaimID))
		if got := result(audit(t, n, good, gl, "2027-02-01T00:00:00Z")); got != "refused bad_chain" {
			t.Fatalf("good claim before the declaration is fetched = %s; want refused bad_chain", got)
		}
		if due := n.ReauditDue(at(t, "2027-02-01T01:00:00Z")); len(due) != 0 {
			t.Fatalf("ReauditDue before new data = %+v; want none", due)
		}
		if err := n.RegisterDeclaration(future); err != nil {
			t.Fatal(err)
		}
		due := n.ReauditDue(at(t, "2027-02-01T01:00:00Z"))
		if len(due) != 1 || due[0].ClaimID != good.ClaimID {
			t.Fatalf("ReauditDue after the declaration = %+v; want the good claim", due)
		}
		if got := result(audit(t, n, good, gl, "2027-02-01T01:00:00Z")); got != "9989392" {
			t.Fatalf("good claim after the declaration = %s; want a witness with credit 9989392", got)
		}
	})
}
