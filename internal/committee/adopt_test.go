package committee

import (
	"math/big"
	"testing"

	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

func TestAdoptCatchUp(t *testing.T) {
	t.Run("adopts a settled predecessor after the lateness window", func(t *testing.T) {
		w := primed(t)
		claim, leaves := w.second(t, "catch-up")
		witnesses := []wire.SettlementWitness{
			*audit(t, w.monitors[0], claim, leaves, "2027-01-14T00:00:00Z").Witness,
			*audit(t, w.monitors[1], claim, leaves, "2027-01-14T00:00:00Z").Witness,
			*audit(t, w.monitors[2], claim, leaves, "2027-01-14T00:00:00Z").Witness,
			*audit(t, w.monitors[3], claim, leaves, "2027-01-14T00:00:00Z").Witness,
		}
		blind := w.newMonitor(t, "blind")
		if got := result(audit(t, blind, w.example.Claim, w.example.Leaves, "2027-01-06T12:00:00Z")); got != "9989528" {
			t.Fatalf("first claim result = %q; want funded witness", got)
		}
		third, thirdLeaves := w.claim(t, claimSpec{previous: &claim, start: "2027-04-19T00:00:00Z", end: "2027-04-20T00:00:00Z",
			leaves: []wire.SettlementLeaf{leaf("third", w.btcSchedule, "136", "2027-04-19T06:00:00Z")}})
		if got := result(audit(t, blind, third, thirdLeaves, "2027-04-20T00:00:00Z")); got != "refused bad_chain" {
			t.Fatalf("third claim before catch-up = %q; want bad_chain refusal", got)
		}
		committee := w.committee()
		adopted, err := blind.Adopt(claim, witnesses, committee, at(t, "2027-06-01T00:00:00Z"), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if adopted.ClaimID != claim.ClaimID {
			t.Fatalf("adopted claim = %q; want %q", adopted.ClaimID, claim.ClaimID)
		}
		if got := result(audit(t, blind, third, thirdLeaves, "2027-04-20T00:00:00Z")); got != "9989256" {
			t.Fatalf("third claim after catch-up = %q; want funded witness", got)
		}
	})
}

func TestFollow(t *testing.T) {
	t.Run("a minority witness holder follows a settled fork without replacing its witness", func(t *testing.T) {
		w := primed(t)
		forkA, leavesA := w.second(t, "fork-a")
		forkB, leavesB := w.claim(t, claimSpec{previous: &w.example.Claim, start: "2027-01-12T00:00:00Z", end: "2027-01-13T00:00:00Z",
			leaves: []wire.SettlementLeaf{leaf("fork-b", w.btcSchedule, "136", "2027-01-12T07:00:00Z")}})
		forA := []wire.SettlementWitness{
			*audit(t, w.monitors[0], forkA, leavesA, "2027-01-14T00:00:00Z").Witness,
			*audit(t, w.monitors[1], forkA, leavesA, "2027-01-14T00:00:00Z").Witness,
			*audit(t, w.monitors[2], forkA, leavesA, "2027-01-14T00:00:00Z").Witness,
			*audit(t, w.monitors[3], forkA, leavesA, "2027-01-14T00:00:00Z").Witness,
		}
		forkBWitness := *audit(t, w.monitors[4], forkB, leavesB, "2027-01-14T00:00:00Z").Witness
		if forkBWitness.ClaimID != forkB.ClaimID || forkBWitness.CreditBaseUnits != "9989392" {
			t.Fatalf("rival claim witness = %+v; want funded witness", forkBWitness)
		}
		committee := w.committee()
		child, childLeaves := w.claim(t, claimSpec{previous: &forkA, start: "2027-01-14T00:00:00Z", end: "2027-01-15T00:00:00Z",
			leaves: []wire.SettlementLeaf{leaf("followed-child", w.btcSchedule, "136", "2027-01-14T06:00:00Z")}})
		if got := result(audit(t, w.monitors[4], child, childLeaves, "2027-01-16T00:00:00Z")); got != "refused bad_chain" {
			t.Fatalf("child before Follow = %q; want bad_chain refusal", got)
		}
		if err := w.monitors[4].Follow(forkA, forA[:3], committee); err == nil {
			t.Fatal("Follow accepted three witnesses; want no quorum")
		}
		if err := w.monitors[4].Follow(forkA, forA, committee); err != nil {
			t.Fatal(err)
		}
		page := w.monitors[4].Page(forkB.ProviderID, btc, 0, 10)
		if len(page.Witnesses) != 2 || page.Witnesses[1].ClaimID != forkB.ClaimID {
			t.Fatal("Follow withdrew or replaced the original witness")
		}
		if got := w.auditAll(t, child, childLeaves, "2027-01-16T00:00:00Z"); got != five("9989256") {
			t.Fatalf("child after Follow = %v; want five funded witnesses", got)
		}
		if settlement.Settled(settlement.CountWitnesses(forkB, []wire.SettlementWitness{forkBWitness}, committee, nil), committee) {
			t.Fatal("minority fork settled with only its original witness")
		}
	})
}

func TestAdoptAncestors(t *testing.T) {
	t.Run("follows settled ancestors while keeping a conflicting witness", func(t *testing.T) {
		w := primed(t)
		committee := w.committee()
		newcomer := w.newMonitor(t, "ancestor-adopter")
		rival, rivalLeaves := w.claim(t, claimSpec{start: "2027-01-01T00:00:00Z", end: "2027-01-02T00:00:00Z",
			leaves: []wire.SettlementLeaf{leaf("ancestor-rival", w.btcSchedule, "10472", "2027-01-01T06:00:00Z")}})
		if got := result(audit(t, newcomer, rival, rivalLeaves, "2027-01-06T12:00:00Z")); got != "9989528" {
			t.Fatalf("rival ancestor result = %q; want witness", got)
		}
		child, childLeaves := w.second(t, "ancestor-child")
		witnesses := []wire.SettlementWitness{
			*audit(t, w.monitors[0], child, childLeaves, "2027-01-14T00:00:00Z").Witness,
			*audit(t, w.monitors[1], child, childLeaves, "2027-01-14T00:00:00Z").Witness,
			*audit(t, w.monitors[2], child, childLeaves, "2027-01-14T00:00:00Z").Witness,
			*audit(t, w.monitors[3], child, childLeaves, "2027-01-14T00:00:00Z").Witness,
		}
		if _, err := newcomer.Adopt(child, witnesses, committee, at(t, "2027-01-14T00:00:00Z"), nil, nil); err == nil {
			t.Fatal("Adopt succeeded without the predecessor ancestor")
		}
		if _, err := newcomer.Adopt(child, witnesses, committee, at(t, "2027-01-14T00:00:00Z"), nil, []wire.SettlementClaim{w.example.Claim}); err != nil {
			t.Fatal(err)
		}
		page := newcomer.Page(w.example.Claim.ProviderID, btc, 0, 10)
		if len(page.Witnesses) != 2 || page.Witnesses[0].ClaimID != rival.ClaimID {
			t.Fatal("adopting the settled ancestor replaced the conflicting sequence-one witness")
		}
		third, thirdLeaves := w.claim(t, claimSpec{previous: &child, start: "2027-01-14T00:00:00Z", end: "2027-01-15T00:00:00Z",
			leaves: []wire.SettlementLeaf{leaf("ancestor-third", w.btcSchedule, "136", "2027-01-14T06:00:00Z")}})
		if got := result(audit(t, newcomer, third, thirdLeaves, "2027-01-16T00:00:00Z")); got != "9989256" {
			t.Fatalf("third claim after ancestor adoption = %q; want funded witness", got)
		}
	})
}

func TestAdoptFailureDoesNotFollowAncestors(t *testing.T) {
	t.Run("an unfunded adoption leaves no followed claim or equivocation", func(t *testing.T) {
		w := primed(t)
		committee := w.committee()
		monitor := w.newMonitor(t, "unfunded-adopter")
		rival, rivalLeaves := w.claim(t, claimSpec{start: "2027-01-01T00:00:00Z", end: "2027-01-02T00:00:00Z",
			leaves: []wire.SettlementLeaf{leaf("unfunded-rival", w.btcSchedule, "10472", "2027-01-01T06:00:00Z")}})
		if got := result(audit(t, monitor, rival, rivalLeaves, "2027-01-06T12:00:00Z")); got != "9989528" {
			t.Fatalf("rival witness = %q; want funded witness", got)
		}
		child, leaves := w.second(t, "unfunded-adoption")
		witnesses := []wire.SettlementWitness{
			*audit(t, w.monitors[0], child, leaves, "2027-01-14T00:00:00Z").Witness,
			*audit(t, w.monitors[1], child, leaves, "2027-01-14T00:00:00Z").Witness,
			*audit(t, w.monitors[2], child, leaves, "2027-01-14T00:00:00Z").Witness,
			*audit(t, w.monitors[3], child, leaves, "2027-01-14T00:00:00Z").Witness,
		}
		w.deposits.set(child.ProviderID, child.Network, 0)
		if _, err := monitor.Adopt(child, witnesses, committee, at(t, "2027-01-14T00:00:00Z"), nil, []wire.SettlementClaim{w.example.Claim}); err == nil {
			t.Fatal("Adopt accepted an unfunded claim")
		}
		page := monitor.Page(child.ProviderID, btc, 0, 10)
		if len(page.Witnesses) != 1 || page.Witnesses[0].ClaimID != rival.ClaimID || len(monitor.EquivocationProofs(child.ProviderID)) != 0 {
			t.Fatal("failed Adopt left a followed ancestor, witness, or equivocation")
		}
	})
}

func TestSettledChildAdoptsItsAncestors(t *testing.T) {
	t.Run("adoption follows the settled fork and clears its ancestor hold", func(t *testing.T) {
		w := primed(t)
		committee := w.committee()
		a1, a1Leaves := w.second(t, "ancestor-a1")
		b1, b1Leaves := w.claim(t, claimSpec{previous: &w.example.Claim, start: "2027-01-12T00:00:00Z", end: "2027-01-13T00:00:00Z",
			leaves: []wire.SettlementLeaf{leaf("ancestor-b1", w.btcSchedule, "136", "2027-01-12T07:00:00Z")}})
		wa1 := []wire.SettlementWitness{
			*audit(t, w.monitors[0], a1, a1Leaves, "2027-01-14T00:00:00Z").Witness,
			*audit(t, w.monitors[1], a1, a1Leaves, "2027-01-14T00:00:00Z").Witness,
			*audit(t, w.monitors[2], a1, a1Leaves, "2027-01-14T00:00:00Z").Witness,
		}
		if got := result(audit(t, w.monitors[4], b1, b1Leaves, "2027-01-14T00:00:00Z")); got != "9989392" {
			t.Fatalf("rival b1 witness = %q", got)
		}
		a2, a2Leaves := w.claim(t, claimSpec{previous: &a1, start: "2027-01-14T00:00:00Z", end: "2027-01-15T00:00:00Z",
			leaves: []wire.SettlementLeaf{leaf("ancestor-a2", w.btcSchedule, "136", "2027-01-14T06:00:00Z")}})
		wa2 := []wire.SettlementWitness{
			*audit(t, w.monitors[0], a2, a2Leaves, "2027-01-16T00:00:00Z").Witness,
			*audit(t, w.monitors[1], a2, a2Leaves, "2027-01-16T00:00:00Z").Witness,
			*audit(t, w.monitors[2], a2, a2Leaves, "2027-01-16T00:00:00Z").Witness,
		}
		faultyWitness, err := settlement.SignWitness(w.monitors[3].key, a2, big.NewInt(9_989_256), at(t, "2027-01-16T00:00:00Z"))
		if err != nil {
			t.Fatal(err)
		}
		wa2 = append(wa2, faultyWitness)
		if _, err := w.monitors[4].Adopt(a2, wa2, committee, at(t, "2027-01-16T00:00:00Z"), nil, nil); err == nil {
			t.Fatal("Adopt accepted a2 without a1")
		}
		fresh := w.newMonitor(t, "wrong-ancestor")
		if _, err := fresh.Adopt(a2, wa2, committee, at(t, "2027-01-16T00:00:00Z"), nil, []wire.SettlementClaim{b1}); err == nil {
			t.Fatal("Adopt accepted a rival ancestor that does not link to a2")
		}
		if _, err := w.monitors[4].Adopt(a2, wa2, committee, at(t, "2027-01-16T00:00:00Z"), nil, []wire.SettlementClaim{a1}); err != nil {
			t.Fatal(err)
		}
		page := w.monitors[4].Page(a1.ProviderID, btc, 0, 10)
		if len(page.Witnesses) != 3 || page.Witnesses[1].ClaimID != b1.ClaimID {
			t.Fatal("following a1 replaced the monitor's b1 witness")
		}
		a3, a3Leaves := w.claim(t, claimSpec{previous: &a2, start: "2027-01-16T00:00:00Z", end: "2027-01-17T00:00:00Z",
			leaves: []wire.SettlementLeaf{leaf("ancestor-a3", w.btcSchedule, "136", "2027-01-16T06:00:00Z")}})
		if got := result(audit(t, w.monitors[4], a3, a3Leaves, "2027-01-18T00:00:00Z")); got != "9989120" {
			t.Fatalf("a3 after follow = %q; want funded witness", got)
		}
		held := w.newMonitor(t, "ancestor-hold")
		initial, err := held.Takeover([]Candidate{{Claim: a1, Leaves: nil, Witnesses: wa1}}, committee, at(t, "2027-01-14T00:00:00Z"), nil, nil, nil, nil)
		if err != nil || initial.Hold == nil {
			t.Fatalf("initial takeover = %+v, %v; want ancestor hold", initial, err)
		}
		adopted, err := held.Takeover([]Candidate{{Claim: a2, Leaves: a2Leaves, Witnesses: wa2}}, committee,
			at(t, "2027-01-16T00:00:00Z"), nil, nil, map[string][]wire.SettlementClaim{a2.ClaimID: {a1}}, nil)
		if err != nil || adopted.Witness == nil {
			t.Fatalf("child takeover = %+v, %v; want adopted witness", adopted, err)
		}
		if page := held.Page(a1.ProviderID, btc, 0, 10); len(page.Holds) != 0 {
			t.Fatalf("ancestor hold remains after child adoption: %d", len(page.Holds))
		}
	})
}
