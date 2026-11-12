package committee

import (
	"math/big"
	"reflect"
	"testing"
	"time"

	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

// onlyEthereumSchedule is a schedule source that has not fetched the bitcoin schedule.
type onlyEthereumSchedule struct {
	scheduleID string
	schedule   settlement.Schedule
}

func (s onlyEthereumSchedule) Lookup(id string) (settlement.Schedule, bool) {
	return s.schedule, id == s.scheduleID
}

func (onlyEthereumSchedule) InForceWithin(string, string, time.Time) bool { return true }

func (w *world) committee() []string {
	return []string{w.monitors[0].ID(), w.monitors[1].ID(), w.monitors[2].ID(), w.monitors[3].ID(), w.monitors[4].ID()}
}

// firstWitnesses returns four of the five witnesses of the example's first claim in a primed world.
func (w *world) firstWitnesses() []wire.SettlementWitness {
	return []wire.SettlementWitness{
		w.monitors[0].Page(w.example.Claim.ProviderID, btc, 0, 1).Witnesses[0],
		w.monitors[1].Page(w.example.Claim.ProviderID, btc, 0, 1).Witnesses[0],
		w.monitors[2].Page(w.example.Claim.ProviderID, btc, 0, 1).Witnesses[0],
		w.monitors[3].Page(w.example.Claim.ProviderID, btc, 0, 1).Witnesses[0],
	}
}

// newcomer is a new monitor that adopted the example's first claim from the five-member committee, as the model's new_monitor.
func (w *world) newcomer(t *testing.T, label string) *Monitor {
	t.Helper()
	m := w.newMonitor(t, label)
	if _, err := m.Adopt(w.example.Claim, w.firstWitnesses(), w.committee(), at(t, "2027-01-06T12:00:00Z"), nil, nil); err != nil {
		t.Fatal(err)
	}
	return m
}

// fork builds a valid sequence-2 claim on the example's first claim with one leaf issued at issuedAt.
func (w *world) fork(t *testing.T, label, issuedAt string) (wire.SettlementClaim, []wire.SettlementLeaf) {
	t.Helper()
	return w.claim(t, claimSpec{previous: &w.example.Claim, start: "2027-01-12T00:00:00Z", end: "2027-01-13T00:00:00Z",
		leaves: []wire.SettlementLeaf{leaf(label, w.btcSchedule, "136", issuedAt)}})
}

func sortedPair(a, b string) []string {
	if b < a {
		return []string{b, a}
	}
	return []string{a, b}
}

func wantHold(t *testing.T, r TakeoverResult, err error, claimIDs []string) {
	t.Helper()
	if err != nil || r.Witness != nil || r.Hold == nil {
		t.Fatalf("Takeover() = %+v, %v; want a hold and no witness", r, err)
	}
	if !reflect.DeepEqual(r.Hold.ClaimIDs, claimIDs) {
		t.Fatalf("hold claim_ids = %v; want %v", r.Hold.ClaimIDs, claimIDs)
	}
	if settlement.VerifyHold(*r.Hold) != nil {
		t.Fatal("hold signature does not verify")
	}
}

func wantWitness(t *testing.T, r TakeoverResult, err error, claimID string) {
	t.Helper()
	if err != nil || r.Hold != nil || r.Witness == nil || r.Witness.ClaimID != claimID {
		t.Fatalf("Takeover() = %+v, %v; want only a witness for %s", r, err, claimID)
	}
}

func TestHandoverWait(t *testing.T) {
	t.Run("waits 48 hours when a known period ended before activation", func(t *testing.T) {
		effective := at(t, "2027-02-01T00:00:00Z")
		got := HandoverWait(effective, []time.Time{effective.Add(-time.Second)})
		if want := at(t, "2027-02-03T00:00:00Z"); !got.Equal(want) {
			t.Fatalf("HandoverWait() = %s; want %s", got, want)
		}
	})
	t.Run("does not wait without an earlier known period", func(t *testing.T) {
		effective := at(t, "2027-02-01T00:00:00Z")
		if got := HandoverWait(effective, nil); !got.Equal(effective) {
			t.Fatalf("HandoverWait() = %s; want %s", got, effective)
		}
	})
}

func TestCommitteeHandoverAdoptsSettledHistory(t *testing.T) {
	w := primed(t)
	outgoing := w.committee()
	second, leaves := w.second(t, "handover-second")
	secondWitnesses := []wire.SettlementWitness{
		*audit(t, w.monitors[0], second, leaves, "2027-01-14T00:00:00Z").Witness,
		*audit(t, w.monitors[1], second, leaves, "2027-01-14T00:00:00Z").Witness,
		*audit(t, w.monitors[2], second, leaves, "2027-01-14T00:00:00Z").Witness,
		*audit(t, w.monitors[3], second, leaves, "2027-01-14T00:00:00Z").Witness,
	}
	longAfter := at(t, "2028-06-01T00:00:00Z")
	adopt := func(t *testing.T, label string) (*Monitor, wire.SettlementWitness) {
		t.Helper()
		m := w.newMonitor(t, label)
		if _, err := m.Adopt(w.example.Claim, w.firstWitnesses(), outgoing, longAfter, nil, nil); err != nil {
			t.Fatalf("Adopt(first claim) error = %v; want no lateness refusal", err)
		}
		witness, err := m.Adopt(second, secondWitnesses, outgoing, longAfter, nil, nil)
		if err != nil {
			t.Fatalf("Adopt(second claim) error = %v", err)
		}
		return m, witness
	}
	n1, w1 := adopt(t, "incoming-1")
	n2, w2 := adopt(t, "incoming-2")
	n3, w3 := adopt(t, "incoming-3")
	alternative, altLeaves := w.fork(t, "handover-alternative", "2027-01-12T07:00:00Z")
	t.Run("the continuing witness and the adopted ones settle under the incoming committee", func(t *testing.T) {
		incoming := []string{w.monitors[3].ID(), w.monitors[4].ID(), n1.ID(), n2.ID(), n3.ID()}
		ws := []wire.SettlementWitness{secondWitnesses[3], w1, w2, w3}
		if !settlement.Settled(settlement.CountWitnesses(second, ws, incoming, nil), incoming) {
			t.Fatal("second claim is not settled by one continuing and three adopted witnesses")
		}
	})
	t.Run("re-adopting returns the stored witness", func(t *testing.T) {
		again, err := n1.Adopt(second, secondWitnesses, outgoing, at(t, "2028-07-01T00:00:00Z"), nil, nil)
		if err != nil || again != w1 {
			t.Fatalf("Adopt() again = %+v, %v; want the stored witness", again, err)
		}
	})
	t.Run("a different claim at an adopted sequence cannot be adopted", func(t *testing.T) {
		if _, err := n1.Adopt(alternative, secondWitnesses, outgoing, longAfter, nil, nil); err == nil {
			t.Fatal("Adopt() accepted a different claim at an adopted sequence")
		}
	})
	t.Run("a claim without an outgoing quorum cannot be adopted", func(t *testing.T) {
		other := w.newcomer(t, "no-quorum-adopter")
		if _, err := other.Adopt(second, secondWitnesses[:3], outgoing, longAfter, nil, nil); err == nil {
			t.Fatal("Adopt() accepted three of five witnesses")
		}
		if _, err := other.Adopt(alternative, secondWitnesses, outgoing, longAfter, nil, nil); err == nil {
			t.Fatal("Adopt() counted witnesses of another claim")
		}
		if _, err := other.Adopt(alternative, nil, outgoing, longAfter, nil, nil); err == nil {
			t.Fatal("Adopt() accepted a claim with no witnesses")
		}
	})
	t.Run("the audit refuses the alternative", func(t *testing.T) {
		if got := result(audit(t, n1, alternative, altLeaves, "2028-06-01T00:00:00Z")); got != "refused bad_chain" {
			t.Fatalf("alternative audit = %s; want refused bad_chain", got)
		}
	})
}

func TestHandoverWithSplitWitnessesAtSevenMonitors(t *testing.T) {
	w := primed(t)
	e1, e2 := w.newMonitor(t, "5"), w.newMonitor(t, "6")
	if got := result(audit(t, e1, w.example.Claim, w.example.Leaves, "2027-01-06T12:00:00Z")); got != "9989528" {
		t.Fatalf("monitor 5 first claim = %s", got)
	}
	if got := result(audit(t, e2, w.example.Claim, w.example.Leaves, "2027-01-06T12:00:00Z")); got != "9989528" {
		t.Fatalf("monitor 6 first claim = %s", got)
	}
	old := []string{w.monitors[0].ID(), w.monitors[1].ID(), w.monitors[2].ID(), w.monitors[3].ID(), w.monitors[4].ID(), e1.ID(), e2.ID()}
	x, lx := w.claim(t, claimSpec{previous: &w.example.Claim, start: "2027-01-12T00:00:00Z", end: "2027-01-13T00:00:00Z",
		leaves: []wire.SettlementLeaf{leaf("x large", w.btcSchedule, "680", "2027-01-12T06:00:00Z")}})
	y, ly := w.fork(t, "y tiny", "2027-01-12T07:00:00Z")
	wx := []wire.SettlementWitness{
		*audit(t, w.monitors[0], x, lx, "2027-01-13T12:00:00Z").Witness,
		*audit(t, w.monitors[1], x, lx, "2027-01-13T12:00:00Z").Witness,
		*audit(t, w.monitors[2], x, lx, "2027-01-13T12:00:00Z").Witness,
	}
	wy := []wire.SettlementWitness{
		*audit(t, w.monitors[3], y, ly, "2027-01-13T12:00:00Z").Witness,
		*audit(t, w.monitors[4], y, ly, "2027-01-13T12:00:00Z").Witness,
		*audit(t, e1, y, ly, "2027-01-13T12:00:00Z").Witness,
		*audit(t, e2, y, ly, "2027-01-13T12:00:00Z").Witness,
	}
	newcomer := w.newMonitor(t, "seven-newcomer")
	firstSeven := append(w.firstWitnesses(), w.monitors[4].Page(w.example.Claim.ProviderID, btc, 0, 1).Witnesses[0])
	if _, err := newcomer.Adopt(w.example.Claim, firstSeven, old, at(t, "2027-02-01T00:00:00Z"), nil, nil); err != nil {
		t.Fatal(err)
	}
	t.Run("the newcomer witnesses neither of two valid claims", func(t *testing.T) {
		got, err := newcomer.Takeover([]Candidate{{Claim: x, Leaves: lx, Witnesses: wx[1:]}, {Claim: y, Leaves: ly, Witnesses: wy}}, old,
			at(t, "2027-02-01T00:00:00Z"), nil, nil, nil, nil)
		wantHold(t, got, err, sortedPair(x.ClaimID, y.ClaimID))
	})
	t.Run("Y stays at four of seven under the new committee", func(t *testing.T) {
		replaced := []string{w.monitors[0].ID(), w.monitors[1].ID(), w.monitors[3].ID(), w.monitors[4].ID(), e1.ID(), e2.ID(), newcomer.ID()}
		if settlement.Settled(settlement.CountWitnesses(y, wy, replaced, nil), replaced) {
			t.Fatal("Y settled with four of seven witnesses")
		}
	})
}

func TestHandoverBlocksOnAClaimItCannotProveInvalid(t *testing.T) {
	w := primed(t)
	a, la := w.fork(t, "claim A", "2027-01-12T06:00:00Z")
	b, lb := w.fork(t, "claim B", "2027-01-12T07:00:00Z")
	visibleA := []wire.SettlementWitness{
		*audit(t, w.monitors[0], a, la, "2027-01-13T12:00:00Z").Witness,
		*audit(t, w.monitors[3], a, la, "2027-01-13T12:00:00Z").Witness,
		*audit(t, w.monitors[4], a, la, "2027-01-13T12:00:00Z").Witness,
	}
	wb := []wire.SettlementWitness{
		*audit(t, w.monitors[1], b, lb, "2027-01-13T12:00:00Z").Witness,
		*audit(t, w.monitors[2], b, lb, "2027-01-13T12:00:00Z").Witness,
	}
	both := sortedPair(a.ClaimID, b.ClaimID)
	withheld := []Candidate{{Claim: a, Witnesses: visibleA}, {Claim: b, Leaves: lb, Witnesses: wb}}
	t.Run("first newcomer holds when A's leaves are withheld", func(t *testing.T) {
		got, err := w.newcomer(t, "withheld-1").Takeover(withheld, w.committee(), at(t, "2027-02-01T00:00:00Z"), nil, nil, nil, nil)
		wantHold(t, got, err, both)
	})
	t.Run("second newcomer holds when A's leaves are withheld", func(t *testing.T) {
		got, err := w.newcomer(t, "withheld-2").Takeover(withheld, w.committee(), at(t, "2027-02-01T00:00:00Z"), nil, nil, nil, nil)
		wantHold(t, got, err, both)
	})
	t.Run("an old but valid claim with its leaves still blocks", func(t *testing.T) {
		late := w.newMonitor(t, "too-old")
		if _, err := late.Adopt(w.example.Claim, w.firstWitnesses(), w.committee(), at(t, "2028-01-01T00:00:00Z"), nil, nil); err != nil {
			t.Fatal(err)
		}
		got, err := late.Takeover([]Candidate{{Claim: a, Leaves: la, Witnesses: visibleA}, {Claim: b, Leaves: lb, Witnesses: wb}}, w.committee(),
			at(t, "2028-01-01T00:00:00Z"), nil, nil, nil, nil)
		wantHold(t, got, err, both)
	})
	t.Run("an unfunded single claim blocks", func(t *testing.T) {
		poor := w.newcomer(t, "poor")
		w.deposits.set(keys.DIDOf(w.provider), btc, 0)
		got, err := poor.Takeover([]Candidate{{Claim: a, Leaves: la, Witnesses: visibleA[:1]}}, w.committee(), at(t, "2027-02-01T00:00:00Z"), nil, nil, nil, nil)
		wantHold(t, got, err, []string{a.ClaimID})
	})
	t.Run("B stays unsettled under the new committee", func(t *testing.T) {
		replaced := []string{w.monitors[0].ID(), w.monitors[1].ID(), w.monitors[2].ID(), keys.DIDOf(w.providerB), keys.DIDOf(w.providerNext)}
		if settlement.Settled(settlement.CountWitnesses(b, wb, replaced, nil), replaced) {
			t.Fatal("B settled with two witnesses")
		}
	})
}

func TestABlockSurvivesASecondCommitteeChange(t *testing.T) {
	w := primed(t)
	h3, h4, f := w.monitors[2], w.monitors[3], w.monitors[4]
	a, la := w.fork(t, "claim A", "2027-01-12T06:00:00Z")
	b, lb := w.fork(t, "claim B", "2027-01-12T07:00:00Z")
	audit(t, w.monitors[0], a, la, "2027-01-13T12:00:00Z")
	audit(t, w.monitors[1], a, la, "2027-01-13T12:00:00Z")
	visibleA := []wire.SettlementWitness{*audit(t, h3, a, la, "2027-01-13T12:00:00Z").Witness}
	wb := []wire.SettlementWitness{
		*audit(t, h4, b, lb, "2027-01-13T12:00:00Z").Witness,
		*audit(t, f, b, lb, "2027-01-13T12:00:00Z").Witness,
	}
	c0 := w.committee()
	n1, n2 := w.newcomer(t, "n1"), w.newcomer(t, "n2")
	c1 := []string{h3.ID(), h4.ID(), f.ID(), n1.ID(), n2.ID()}
	first := []Candidate{{Claim: a, Witnesses: visibleA}, {Claim: b, Leaves: lb, Witnesses: wb}}
	got1, err1 := n1.Takeover(first, c0, at(t, "2027-02-01T00:00:00Z"), nil, nil, nil, nil)
	got2, err2 := n2.Takeover(first, c0, at(t, "2027-02-01T00:00:00Z"), nil, nil, nil, nil)
	second := []Candidate{{Claim: b, Leaves: lb, Witnesses: wb}, {Claim: a}}
	changed := at(t, "2027-03-05T00:00:00Z")
	t.Run("the first newcomers hold on both claims", func(t *testing.T) {
		wantHold(t, got1, err1, sortedPair(a.ClaimID, b.ClaimID))
		wantHold(t, got2, err2, sortedPair(a.ClaimID, b.ClaimID))
	})
	t.Run("a hold is served with the claims it names", func(t *testing.T) {
		page := n2.Page(a.ProviderID, btc, 1, 10)
		if len(page.Holds) != 1 || page.Holds[0].WitnessSignature != got2.Hold.WitnessSignature || len(page.Claims) != 2 {
			t.Fatalf("page = %d holds, %d claims; want the hold and claims A and B", len(page.Holds), len(page.Claims))
		}
	})
	t.Run("the second newcomers are bound by the continuing monitor's hold", func(t *testing.T) {
		m1, err := w.newcomer(t, "m1").Takeover(second, c1, changed, []wire.SettlementHold{*got2.Hold}, c0, nil, nil)
		wantHold(t, m1, err, sortedPair(a.ClaimID, b.ClaimID))
		m2, err := w.newcomer(t, "m2").Takeover(second, c1, changed, []wire.SettlementHold{*got2.Hold}, c0, nil, nil)
		wantHold(t, m2, err, sortedPair(a.ClaimID, b.ClaimID))
		if settlement.Settled(settlement.CountWitnesses(b, wb, c1, nil), c1) {
			t.Fatal("B settled with two witnesses")
		}
	})
	t.Run("without the hold a newcomer would complete B", func(t *testing.T) {
		got, err := w.newcomer(t, "naive").Takeover([]Candidate{{Claim: b, Leaves: lb, Witnesses: wb}}, c1, changed, nil, nil, nil, nil)
		wantWitness(t, got, err, b.ClaimID)
	})
	t.Run("a forged hold is ignored", func(t *testing.T) {
		forged := *got2.Hold
		forged.ClaimIDs = []string{a.ClaimID}
		got, err := w.newcomer(t, "forged").Takeover(second, c1, changed, []wire.SettlementHold{forged}, nil, nil, nil)
		wantWitness(t, got, err, b.ClaimID)
	})
	t.Run("a hold naming a claim nobody produced is ignored", func(t *testing.T) {
		ghost, err := settlement.SignHold(f.key, a.ProviderID, btc, 2, []string{"fysc2:abababababababababababababababababababababababababababababababab"}, changed)
		if err != nil {
			t.Fatal(err)
		}
		got, err := w.newcomer(t, "ghost").Takeover([]Candidate{{Claim: b, Leaves: lb, Witnesses: wb}}, c1, changed, []wire.SettlementHold{ghost}, nil, nil, nil)
		wantWitness(t, got, err, b.ClaimID)
	})
	t.Run("a hold by a key outside the outgoing committee is ignored", func(t *testing.T) {
		stranger, err := settlement.SignHold(w.newMonitor(t, "stranger").key, a.ProviderID, btc, 2, got2.Hold.ClaimIDs, changed)
		if err != nil {
			t.Fatal(err)
		}
		got, err := w.newcomer(t, "stranger-check").Takeover(second, c1, changed, []wire.SettlementHold{stranger}, nil, nil, nil)
		wantWitness(t, got, err, b.ClaimID)
	})
}

func TestTakeoverKeepsAnEarlierWitness(t *testing.T) {
	w := primed(t)
	a, la := w.fork(t, "claim A", "2027-01-12T06:00:00Z")
	b, lb := w.fork(t, "claim B", "2027-01-12T07:00:00Z")
	wa := []wire.SettlementWitness{
		*audit(t, w.monitors[0], a, la, "2027-01-13T12:00:00Z").Witness,
		*audit(t, w.monitors[1], a, la, "2027-01-13T12:00:00Z").Witness,
	}
	wb := []wire.SettlementWitness{
		*audit(t, w.monitors[3], b, lb, "2027-01-13T12:00:00Z").Witness,
		*audit(t, w.monitors[4], b, lb, "2027-01-13T12:00:00Z").Witness,
	}
	t.Run("two valid known claims give no hold and the witness stays", func(t *testing.T) {
		rejoining := w.newcomer(t, "rejoining-both")
		kept := *audit(t, rejoining, a, la, "2027-01-13T12:00:00Z").Witness
		got, err := rejoining.Takeover([]Candidate{{Claim: a, Leaves: la, Witnesses: wa}, {Claim: b, Leaves: lb, Witnesses: wb}}, w.committee(),
			at(t, "2027-02-01T00:00:00Z"), nil, nil, nil, nil)
		if err != nil || got.Hold != nil || got.Witness == nil || *got.Witness != kept {
			t.Fatalf("Takeover() = %+v, %v; want only the kept witness", got, err)
		}
		page := rejoining.Page(a.ProviderID, btc, 1, 10)
		if len(page.Holds) != 0 || len(page.Refusals) != 0 || len(page.Witnesses) != 1 || page.Witnesses[0] != kept {
			t.Fatalf("page = %d witnesses, %d refusals, %d holds; want only the witness for A", len(page.Witnesses), len(page.Refusals), len(page.Holds))
		}
	})
	t.Run("the only valid other claim is not witnessed", func(t *testing.T) {
		rejoining := w.newcomer(t, "rejoining-other")
		kept := *audit(t, rejoining, a, la, "2027-01-13T12:00:00Z").Witness
		got, err := rejoining.Takeover([]Candidate{{Claim: b, Leaves: lb, Witnesses: wb}}, w.committee(), at(t, "2027-02-01T00:00:00Z"), nil, nil, nil, nil)
		if err != nil || got.Hold != nil || got.Witness == nil || *got.Witness != kept {
			t.Fatalf("Takeover() = %+v, %v; want only the kept witness", got, err)
		}
		page := rejoining.Page(a.ProviderID, btc, 1, 10)
		if len(page.Holds) != 0 || len(page.Refusals) != 0 || len(page.Witnesses) != 1 || page.Witnesses[0] != kept {
			t.Fatalf("page = %d witnesses, %d refusals, %d holds; want only the witness for A", len(page.Witnesses), len(page.Refusals), len(page.Holds))
		}
	})
}

func TestAHoldOnASingleClaimEndsWhenItPassesAudit(t *testing.T) {
	w := primed(t)
	claim, leaves := w.second(t, "hold-cleared")
	witness := *audit(t, w.monitors[0], claim, leaves, "2027-01-14T00:00:00Z").Witness
	blind := w.newcomer(t, "blind")
	w.deposits.set(keys.DIDOf(w.provider), btc, 0)
	got, err := blind.Takeover([]Candidate{{Claim: claim, Leaves: leaves, Witnesses: []wire.SettlementWitness{witness}}}, w.committee(),
		at(t, "2027-02-01T00:00:00Z"), nil, nil, nil, nil)
	wantHold(t, got, err, []string{claim.ClaimID})
	w.deposits.set(keys.DIDOf(w.provider), btc, 10_000_000)
	if got := result(audit(t, blind, claim, leaves, "2027-02-01T00:00:00Z")); got != "9989392" {
		t.Fatalf("audit once the deposit is visible = %s; want a witness", got)
	}
	if page := blind.Page(claim.ProviderID, btc, 0, 10); len(page.Holds) != 0 {
		t.Fatalf("page holds = %d; want the hold ended", len(page.Holds))
	}
}

func TestALateSignedDeclarationCannotDisplaceAClaim(t *testing.T) {
	w := primed(t)
	a, la := w.fork(t, "claim A", "2027-01-12T06:00:00Z")
	visibleA := []wire.SettlementWitness{
		*audit(t, w.monitors[0], a, la, "2027-01-13T12:00:00Z").Witness,
		*audit(t, w.monitors[1], a, la, "2027-01-13T12:00:00Z").Witness,
		*audit(t, w.monitors[2], a, la, "2027-01-13T12:00:00Z").Witness,
	}
	d2 := w.declare(t, w.provider, "2027-01-08T00:00:00Z")
	b, lb := w.claim(t, claimSpec{previous: &w.example.Claim, start: "2027-01-12T00:00:00Z", end: "2027-01-13T00:00:00Z", declaration: d2.DeclarationID,
		leaves: []wire.SettlementLeaf{leaf("claim B", w.btcSchedule, "136", "2027-01-12T07:00:00Z")}})
	faulty, err := settlement.SignWitness(w.monitors[4].key, b, big.NewInt(0), at(t, "2027-01-13T12:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	check := func(t *testing.T, label string) {
		t.Helper()
		n := w.newcomer(t, label)
		if err := n.RegisterDeclaration(d2); err != nil {
			t.Fatal(err)
		}
		if credit, f, err := n.Check(a, la, at(t, "2027-02-01T00:00:00Z"), false); err != nil || f != nil || credit.String() != "9989392" {
			t.Fatalf("Check(A) = %v, %+v, %v; want credit 9989392 while holding D2", credit, f, err)
		}
		got, err := n.Takeover([]Candidate{{Claim: a, Leaves: la, Witnesses: visibleA}, {Claim: b, Leaves: lb, Witnesses: []wire.SettlementWitness{faulty}}},
			w.committee(), at(t, "2027-02-01T00:00:00Z"), nil, nil, nil, nil)
		wantHold(t, got, err, sortedPair(a.ClaimID, b.ClaimID))
	}
	t.Run("first newcomer holds", func(t *testing.T) { check(t, "late-declaration-1") })
	t.Run("second newcomer holds", func(t *testing.T) { check(t, "late-declaration-2") })
}

func TestProvableFaultsDoNotBlockTheCorrection(t *testing.T) {
	t.Run("a future declaration", func(t *testing.T) {
		w := primed(t)
		future := w.declare(t, w.provider, "2027-01-20T00:00:00Z")
		bad, bl := w.claim(t, claimSpec{previous: &w.example.Claim, start: "2027-01-12T00:00:00Z", end: "2027-01-13T00:00:00Z", declaration: future.DeclarationID,
			leaves: []wire.SettlementLeaf{leaf("honest mistake", w.btcSchedule, "136", "2027-01-12T06:00:00Z")}})
		good, gl := w.second(t, "honest mistake")
		faulty, err := settlement.SignWitness(w.monitors[4].key, bad, big.NewInt(0), at(t, "2027-01-13T12:00:00Z"))
		if err != nil {
			t.Fatal(err)
		}
		wgood := []wire.SettlementWitness{
			*audit(t, w.monitors[2], good, gl, "2027-01-13T13:00:00Z").Witness,
			*audit(t, w.monitors[3], good, gl, "2027-01-13T13:00:00Z").Witness,
		}
		n := w.newcomer(t, "future-declaration")
		if err := n.RegisterDeclaration(future); err != nil {
			t.Fatal(err)
		}
		got, err := n.Takeover([]Candidate{{Claim: bad, Leaves: bl, Witnesses: []wire.SettlementWitness{faulty}}, {Claim: good, Leaves: gl, Witnesses: wgood}},
			w.committee(), at(t, "2027-02-01T00:00:00Z"), nil, nil, nil, nil)
		wantWitness(t, got, err, good.ClaimID)
		if page := n.Page(good.ProviderID, btc, 0, 10); len(page.Holds) != 0 {
			t.Fatalf("page holds = %d; want none", len(page.Holds))
		}
	})
	t.Run("a misdated succession", func(t *testing.T) {
		w := primed(t)
		late := succeed(t, w.provider, w.providerNext, "2027-01-20T00:00:00Z")
		fixed := succeed(t, w.provider, w.providerNext, "2027-01-12T00:00:00Z")
		decl := w.declare(t, w.providerNext, "2027-01-12T00:00:00Z")
		spec := func(s wire.KeySuccession) claimSpec {
			return claimSpec{key: w.providerNext, previous: &w.example.Claim, start: "2027-01-12T00:00:00Z", end: "2027-01-13T00:00:00Z",
				declaration: decl.DeclarationID, succession: &s.SuccessionID,
				leaves: []wire.SettlementLeaf{leaf("new key", w.btcSchedule, "136", "2027-01-12T06:00:00Z")}}
		}
		bad, bl := w.claim(t, spec(late))
		good, gl := w.claim(t, spec(fixed))
		faulty, err := settlement.SignWitness(w.monitors[4].key, bad, big.NewInt(0), at(t, "2027-01-13T12:00:00Z"))
		if err != nil {
			t.Fatal(err)
		}
		honest, err := settlement.SignWitness(w.monitors[3].key, good, big.NewInt(9_989_392), at(t, "2027-01-13T13:00:00Z"))
		if err != nil {
			t.Fatal(err)
		}
		n := w.newcomer(t, "misdated-succession")
		if err := n.RegisterDeclaration(decl); err != nil {
			t.Fatal(err)
		}
		if err := n.RegisterSuccession(late); err != nil {
			t.Fatal(err)
		}
		if err := n.RegisterSuccession(fixed); err != nil {
			t.Fatal(err)
		}
		got, err := n.Takeover([]Candidate{{Claim: bad, Leaves: bl, Witnesses: []wire.SettlementWitness{faulty}}, {Claim: good, Leaves: gl, Witnesses: []wire.SettlementWitness{honest}}},
			w.committee(), at(t, "2027-02-01T00:00:00Z"), nil, nil, nil, nil)
		wantWitness(t, got, err, good.ClaimID)
	})
	t.Run("a period one second over 24 hours", func(t *testing.T) {
		w := primed(t)
		fixed, fl := w.second(t, "corrected")
		long, ll := w.second(t, "corrected")
		long.PeriodEnd = "2027-01-13T00:00:01Z"
		long = resign(t, w.provider, long)
		faulty, err := settlement.SignWitness(w.monitors[4].key, long, big.NewInt(0), at(t, "2027-01-13T12:00:00Z"))
		if err != nil {
			t.Fatal(err)
		}
		honest := *audit(t, w.monitors[0], fixed, fl, "2027-01-13T12:00:00Z").Witness
		got, err := w.newcomer(t, "period-fault").Takeover([]Candidate{{Claim: long, Leaves: ll, Witnesses: []wire.SettlementWitness{faulty}},
			{Claim: fixed, Leaves: fl, Witnesses: []wire.SettlementWitness{honest}}}, w.committee(), at(t, "2027-02-01T00:00:00Z"), nil, nil, nil, nil)
		wantWitness(t, got, err, fixed.ClaimID)
	})
	t.Run("an impossible leaf amount", func(t *testing.T) {
		w := primed(t)
		good, gl := w.second(t, "corrected")
		bad, bl := w.claim(t, claimSpec{previous: &w.example.Claim, start: "2027-01-12T00:00:00Z", end: "2027-01-13T00:00:00Z",
			leaves: []wire.SettlementLeaf{leaf("corrected", w.btcSchedule, "137", "2027-01-12T06:00:00Z")}})
		faulty, err := settlement.SignWitness(w.monitors[4].key, bad, big.NewInt(0), at(t, "2027-01-13T12:00:00Z"))
		if err != nil {
			t.Fatal(err)
		}
		honest := *audit(t, w.monitors[0], good, gl, "2027-01-13T12:00:00Z").Witness
		got, err := w.newcomer(t, "leaf-fault").Takeover([]Candidate{{Claim: bad, Leaves: bl, Witnesses: []wire.SettlementWitness{faulty}},
			{Claim: good, Leaves: gl, Witnesses: []wire.SettlementWitness{honest}}}, w.committee(), at(t, "2027-02-01T00:00:00Z"), nil, nil, nil, nil)
		wantWitness(t, got, err, good.ClaimID)
	})
	t.Run("an unfetched schedule proves nothing and blocks", func(t *testing.T) {
		w := primed(t)
		fixed, fl := w.second(t, "corrected")
		other, ol := w.second(t, "other")
		honest := *audit(t, w.monitors[0], fixed, fl, "2027-01-13T12:00:00Z").Witness
		faulty, err := settlement.SignWitness(w.monitors[1].key, other, big.NewInt(0), at(t, "2027-01-13T12:00:00Z"))
		if err != nil {
			t.Fatal(err)
		}
		eth, ok := w.schedules.Lookup(w.ethSchedule)
		if !ok {
			t.Fatal("example ethereum schedule is missing")
		}
		blind, err := New(Config{Key: w.newMonitor(t, "schedule-blind").key, Schedules: onlyEthereumSchedule{scheduleID: w.ethSchedule, schedule: eth}, Deposits: w.deposits})
		if err != nil {
			t.Fatal(err)
		}
		if err := blind.RegisterDeclaration(w.declA); err != nil {
			t.Fatal(err)
		}
		if _, err := blind.Adopt(w.example.Claim, w.firstWitnesses(), w.committee(), at(t, "2027-01-06T12:00:00Z"), nil, nil); err != nil {
			t.Fatal(err)
		}
		got, err := blind.Takeover([]Candidate{{Claim: fixed, Leaves: fl, Witnesses: []wire.SettlementWitness{honest}},
			{Claim: other, Leaves: ol, Witnesses: []wire.SettlementWitness{faulty}}}, w.committee(), at(t, "2027-02-01T00:00:00Z"), nil, nil, nil, nil)
		wantHold(t, got, err, sortedPair(fixed.ClaimID, other.ClaimID))
	})
}

func TestHandoverIgnoresAClaimThatFailsAudit(t *testing.T) {
	t.Run("with an outgoing quorum it adopts without re-auditing", func(t *testing.T) {
		w := primed(t)
		second, leaves := w.second(t, "second")
		ws := []wire.SettlementWitness{
			*audit(t, w.monitors[0], second, leaves, "2027-01-13T12:00:00Z").Witness,
			*audit(t, w.monitors[1], second, leaves, "2027-01-13T12:00:00Z").Witness,
			*audit(t, w.monitors[2], second, leaves, "2027-01-13T12:00:00Z").Witness,
			*audit(t, w.monitors[3], second, leaves, "2027-01-13T12:00:00Z").Witness,
		}
		got, err := w.newcomer(t, "quorum").Takeover([]Candidate{{Claim: second, Leaves: leaves, Witnesses: ws}}, w.committee(),
			at(t, "2028-01-01T00:00:00Z"), nil, nil, nil, nil)
		wantWitness(t, got, err, second.ClaimID)
	})
	t.Run("candidates signed by a successor key share the old key's sequence", func(t *testing.T) {
		w := primed(t)
		old, ol := w.second(t, "old key")
		next, nl := w.claim(t, claimSpec{key: w.providerNext, previous: &w.example.Claim, start: "2027-01-12T00:00:00Z", end: "2027-01-13T00:00:00Z",
			leaves: []wire.SettlementLeaf{leaf("new key", w.btcSchedule, "136", "2027-01-12T07:00:00Z")}})
		honest := *audit(t, w.monitors[0], old, ol, "2027-01-13T12:00:00Z").Witness
		n := w.newcomer(t, "successor-candidates")
		if err := n.RegisterSuccession(succeed(t, w.provider, w.providerNext, "2027-06-01T00:00:00Z")); err != nil {
			t.Fatal(err)
		}
		got, err := n.Takeover([]Candidate{{Claim: old, Leaves: ol, Witnesses: []wire.SettlementWitness{honest}}, {Claim: next, Leaves: nl}}, w.committee(),
			at(t, "2027-02-01T00:00:00Z"), nil, nil, nil, nil)
		wantWitness(t, got, err, old.ClaimID)
	})
}

func TestSnapshotAfterAdoptFollowAndHold(t *testing.T) {
	w := primed(t)
	n := w.newcomer(t, "snapshot")
	a2, la2 := w.second(t, "snapshot-a2")
	wa2 := []wire.SettlementWitness{
		*audit(t, w.monitors[0], a2, la2, "2027-01-14T00:00:00Z").Witness,
		*audit(t, w.monitors[1], a2, la2, "2027-01-14T00:00:00Z").Witness,
		*audit(t, w.monitors[2], a2, la2, "2027-01-14T00:00:00Z").Witness,
		*audit(t, w.monitors[3], a2, la2, "2027-01-14T00:00:00Z").Witness,
	}
	b2, lb2 := w.fork(t, "snapshot-b2", "2027-01-12T07:00:00Z")
	if got := result(audit(t, n, b2, lb2, "2027-01-14T00:00:00Z")); got != "9989392" {
		t.Fatalf("b2 = %s; want a witness", got)
	}
	if err := n.Follow(a2, wa2, w.committee()); err != nil {
		t.Fatal(err)
	}
	a3, la3 := w.claim(t, claimSpec{previous: &a2, start: "2027-01-14T00:00:00Z", end: "2027-01-15T00:00:00Z",
		leaves: []wire.SettlementLeaf{leaf("snapshot-a3", w.btcSchedule, "136", "2027-01-14T06:00:00Z")}})
	b3, lb3 := w.claim(t, claimSpec{previous: &a2, start: "2027-01-14T00:00:00Z", end: "2027-01-15T00:00:00Z",
		leaves: []wire.SettlementLeaf{leaf("snapshot-b3", w.btcSchedule, "136", "2027-01-14T07:00:00Z")}})
	wa3 := *audit(t, w.monitors[0], a3, la3, "2027-01-16T00:00:00Z").Witness
	wb3 := *audit(t, w.monitors[1], b3, lb3, "2027-01-16T00:00:00Z").Witness
	held, err := n.Takeover([]Candidate{{Claim: a3, Leaves: la3, Witnesses: []wire.SettlementWitness{wa3}}, {Claim: b3, Leaves: lb3, Witnesses: []wire.SettlementWitness{wb3}}},
		w.committee(), at(t, "2027-02-01T00:00:00Z"), nil, nil, nil, nil)
	wantHold(t, held, err, sortedPair(a3.ClaimID, b3.ClaimID))
	data, err := n.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := Restore(w.config(n), data)
	if err != nil {
		t.Fatalf("Restore() error = %v", err)
	}
	if got, want := restored.Page(a2.ProviderID, btc, 0, 10), n.Page(a2.ProviderID, btc, 0, 10); !reflect.DeepEqual(got, want) {
		t.Fatalf("restored page = %+v; want %+v", got, want)
	}
	if got := restored.EquivocationProofs(a2.ProviderID); len(got) != 1 || !settlement.VerifyEquivocation(got[0]) {
		t.Fatalf("restored proofs = %d; want the b2/a2 proof", len(got))
	}
	if got := result(audit(t, restored, a3, la3, "2027-02-02T00:00:00Z")); got != "refused bad_chain" {
		t.Fatalf("a3 under the restored two-claim hold = %s; want refused bad_chain", got)
	}
}
