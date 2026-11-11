package committee

import (
	"bytes"
	"reflect"
	"testing"

	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

func dueIDs(t *testing.T, m *Monitor, now string) []string {
	t.Helper()
	ids := []string{}
	refs := m.ReauditDue(at(t, now))
	if len(refs) > 0 {
		ids = append(ids, refs[0].ClaimID)
	}
	if len(refs) > 1 {
		t.Fatalf("ReauditDue(%s) = %+v; want at most one claim", now, refs)
	}
	return ids
}

func TestReauditDue(t *testing.T) {
	t.Run("unfunded refusal is due every 24 hours for 7 days", func(t *testing.T) {
		w := newWorld(t)
		w.deposits.set(keys.DIDOf(w.provider), btc, 10_000)
		m := w.monitors[0]
		if got := result(audit(t, m, w.example.Claim, w.example.Leaves, "2027-01-06T12:00:00Z")); got != "refused unfunded" {
			t.Fatalf("audit = %s", got)
		}
		id := w.example.Claim.ClaimID
		if got := dueIDs(t, m, "2027-01-07T11:00:00Z"); len(got) != 0 {
			t.Fatalf("due at +23h = %v", got)
		}
		if got := dueIDs(t, m, "2027-01-07T12:00:00Z"); len(got) != 1 || got[0] != id {
			t.Fatalf("due at +24h = %v", got)
		}
		if got := result(audit(t, m, w.example.Claim, w.example.Leaves, "2027-01-07T12:00:00Z")); got != "refused unfunded" {
			t.Fatalf("re-audit = %s", got)
		}
		if got := dueIDs(t, m, "2027-01-08T11:00:00Z"); len(got) != 0 {
			t.Fatalf("due 23h after the re-audit = %v", got)
		}
		if got := dueIDs(t, m, "2027-01-13T12:00:00Z"); len(got) != 1 {
			t.Fatalf("due at +7d = %v", got)
		}
		if got := dueIDs(t, m, "2027-01-13T12:00:01Z"); len(got) != 0 {
			t.Fatalf("due after 7 days = %v", got)
		}
	})
	t.Run("a witness supersedes the refusal", func(t *testing.T) {
		w := newWorld(t)
		w.deposits.set(keys.DIDOf(w.provider), btc, 10_000)
		m := w.monitors[0]
		audit(t, m, w.example.Claim, w.example.Leaves, "2027-01-06T12:00:00Z")
		w.deposits.set(keys.DIDOf(w.provider), btc, 30_000)
		if got := result(audit(t, m, w.example.Claim, w.example.Leaves, "2027-01-07T12:00:00Z")); got != "19528" {
			t.Fatalf("re-audit = %s", got)
		}
		if got := dueIDs(t, m, "2027-01-08T12:00:00Z"); len(got) != 0 {
			t.Fatalf("due after the witness = %v", got)
		}
	})
	t.Run("unknown declaration refusal is due after the declaration is registered", func(t *testing.T) {
		w := primed(t)
		m := w.monitors[0]
		later := w.declare(t, w.provider, "2027-01-10T00:00:00Z")
		c, l := w.claim(t, claimSpec{previous: &w.example.Claim, start: "2027-01-12T00:00:00Z", end: "2027-01-13T00:00:00Z", declaration: later.DeclarationID,
			leaves: []wire.SettlementLeaf{leaf("third", w.btcSchedule, "136", "2027-01-12T06:00:00Z")}})
		if got := result(audit(t, m, c, l, "2027-01-13T12:00:00Z")); got != "refused bad_period" {
			t.Fatalf("audit = %s", got)
		}
		if got := dueIDs(t, m, "2027-01-13T13:00:00Z"); len(got) != 0 {
			t.Fatalf("due before the declaration = %v", got)
		}
		if err := m.RegisterDeclaration(later); err != nil {
			t.Fatal(err)
		}
		if got := dueIDs(t, m, "2027-01-13T13:00:00Z"); len(got) != 1 || got[0] != c.ClaimID {
			t.Fatalf("due after the declaration = %v", got)
		}
	})
	t.Run("missing predecessor refusal is due after the predecessor is witnessed", func(t *testing.T) {
		w := primed(t)
		m := w.monitors[0]
		second, l2 := w.second(t, "second")
		third, l3 := w.claim(t, claimSpec{previous: &second, start: "2027-01-16T00:00:00Z", end: "2027-01-17T00:00:00Z",
			leaves: []wire.SettlementLeaf{leaf("third", w.btcSchedule, "136", "2027-01-16T06:00:00Z")}})
		if got := result(audit(t, m, third, l3, "2027-01-17T12:00:00Z")); got != "refused bad_chain" {
			t.Fatalf("audit = %s", got)
		}
		if got := dueIDs(t, m, "2027-01-17T13:00:00Z"); len(got) != 0 {
			t.Fatalf("due before the predecessor = %v", got)
		}
		audit(t, m, second, l2, "2027-01-17T13:00:00Z")
		if got := dueIDs(t, m, "2027-01-17T13:00:00Z"); len(got) != 1 || got[0] != third.ClaimID {
			t.Fatalf("due after the predecessor = %v", got)
		}
	})
	t.Run("refusal at a sequence the monitor witnessed since is not due", func(t *testing.T) {
		w := primed(t)
		m := w.monitors[0]
		later := w.declare(t, w.provider, "2027-01-10T00:00:00Z")
		c, l := w.claim(t, claimSpec{previous: &w.example.Claim, start: "2027-01-12T00:00:00Z", end: "2027-01-13T00:00:00Z", declaration: later.DeclarationID,
			leaves: []wire.SettlementLeaf{leaf("third", w.btcSchedule, "136", "2027-01-12T06:00:00Z")}})
		audit(t, m, c, l, "2027-01-13T12:00:00Z")
		other, lo := w.second(t, "other")
		audit(t, m, other, lo, "2027-01-13T12:00:00Z")
		if err := m.RegisterDeclaration(later); err != nil {
			t.Fatal(err)
		}
		if got := dueIDs(t, m, "2027-01-13T13:00:00Z"); len(got) != 0 {
			t.Fatalf("due = %v; the sequence already holds a witness", got)
		}
	})
}

func TestObserveKeepsFirstSeen(t *testing.T) {
	w := newWorld(t)
	first := w.monitors[0].Observe("fysc2:x", at(t, "2027-01-06T12:00:00Z"))
	again := w.monitors[0].Observe("fysc2:x", at(t, "2027-01-08T00:00:00Z"))
	if !first.Equal(at(t, "2027-01-06T12:00:00Z")) || !again.Equal(first) {
		t.Fatalf("Observe = %v then %v; want the first time twice", first, again)
	}
}

// pagedWorld has monitor 0 witness sequence 1 on bitcoin and ethereum, witness sequence 2 on bitcoin,
// and refuse a fork at bitcoin sequence 2.
func pagedWorld(t *testing.T) (*world, wire.SettlementClaim, wire.SettlementClaim, wire.SettlementClaim, wire.SettlementClaim) {
	t.Helper()
	w := newWorld(t)
	m := w.monitors[0]
	w.deposits.set(keys.DIDOf(w.provider), eth, 1_000_000_000_000_000)
	audit(t, m, w.example.Claim, w.example.Leaves, "2027-01-06T12:00:00Z")
	ethClaim, ethLeaves := w.claim(t, claimSpec{network: eth, start: "2027-01-05T00:00:00Z", end: "2027-01-06T00:00:00Z",
		leaves: []wire.SettlementLeaf{leaf("ether", w.ethSchedule, "48900000000", "2027-01-05T06:00:00Z")}})
	if got := result(audit(t, m, ethClaim, ethLeaves, "2027-01-06T12:00:00Z")); got != "999951100000000" {
		t.Fatalf("ethereum claim = %s", got)
	}
	a, la := w.second(t, "fork a")
	b, lb := w.second(t, "fork b")
	audit(t, m, a, la, "2027-01-13T12:00:00Z")
	audit(t, m, b, lb, "2027-01-13T12:00:00Z")
	return w, w.example.Claim, ethClaim, a, b
}

func TestPage(t *testing.T) {
	t.Run("only the requested network and later sequences, with the claims witnessed", func(t *testing.T) {
		w, _, _, a, b := pagedWorld(t)
		page := w.monitors[0].Page(keys.DIDOf(w.provider), btc, 1, 10)
		if len(page.Witnesses) != 1 || page.Witnesses[0].ClaimID != a.ClaimID {
			t.Fatalf("witnesses = %+v; want fork a", page.Witnesses)
		}
		if len(page.Refusals) != 1 || page.Refusals[0].ClaimID != b.ClaimID || page.Refusals[0].Reason != "bad_chain" {
			t.Fatalf("refusals = %+v; want fork b", page.Refusals)
		}
		if len(page.Claims) != 1 || !reflect.DeepEqual(page.Claims[0], a) {
			t.Fatalf("claims = %+v; want fork a only", page.Claims)
		}
		if len(page.Holds) != 0 {
			t.Fatalf("holds = %+v", page.Holds)
		}
	})
	t.Run("the other network", func(t *testing.T) {
		w, _, ethClaim, _, _ := pagedWorld(t)
		page := w.monitors[0].Page(keys.DIDOf(w.provider), eth, 0, 10)
		if len(page.Witnesses) != 1 || page.Witnesses[0].ClaimID != ethClaim.ClaimID || len(page.Refusals) != 0 || len(page.Claims) != 1 {
			t.Fatalf("ethereum page = %+v", page)
		}
	})
	t.Run("whole sequences only, stopping at the first that does not fit", func(t *testing.T) {
		w, first, _, _, _ := pagedWorld(t)
		page := w.monitors[0].Page(keys.DIDOf(w.provider), btc, 0, 1)
		if len(page.Witnesses) != 1 || page.Witnesses[0].ClaimID != first.ClaimID || len(page.Refusals) != 0 {
			t.Fatalf("page with limit 1 = %+v; want sequence 1 alone", page)
		}
	})
}

func TestSnapshotRestore(t *testing.T) {
	t.Run("restored monitor serves the same page and makes the same next decision", func(t *testing.T) {
		w, _, _, _, _ := pagedWorld(t)
		m := w.monitors[0]
		data, err := m.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		restored, err := Restore(w.config(m), data)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := restored.Page(keys.DIDOf(w.provider), btc, 0, 10), m.Page(keys.DIDOf(w.provider), btc, 0, 10); !reflect.DeepEqual(got, want) {
			t.Fatalf("restored page = %+v; want %+v", got, want)
		}
		a, _ := m.Claim(m.Page(keys.DIDOf(w.provider), btc, 1, 10).Witnesses[0].ClaimID)
		third, l3 := w.claim(t, claimSpec{previous: &a, start: "2027-01-14T00:00:00Z", end: "2027-01-15T00:00:00Z",
			leaves: []wire.SettlementLeaf{leaf("third", w.btcSchedule, "136", "2027-01-14T06:00:00Z")}})
		got := audit(t, restored, third, l3, "2027-01-15T12:00:00Z")
		want := audit(t, m, third, l3, "2027-01-15T12:00:00Z")
		if got.Witness == nil || want.Witness == nil || *got.Witness != *want.Witness {
			t.Fatalf("restored audit = %+v; original = %+v", got.Witness, want.Witness)
		}
	})
	t.Run("a refusal recorded before a succession survives restore", func(t *testing.T) {
		w := primed(t)
		m := w.monitors[0]
		s := succeed(t, w.provider, w.providerNext, "2027-01-07T00:00:00Z")
		declNext := w.declare(t, w.providerNext, "2027-01-07T00:00:00Z")
		if err := m.RegisterDeclaration(declNext); err != nil {
			t.Fatal(err)
		}
		c, l := w.claim(t, claimSpec{key: w.providerNext, previous: &w.example.Claim, start: "2027-01-08T00:00:00Z", end: "2027-01-09T00:00:00Z",
			declaration: declNext.DeclarationID, succession: &s.SuccessionID, leaves: []wire.SettlementLeaf{leaf("new key", w.btcSchedule, "136", "2027-01-08T06:00:00Z")}})
		if got := result(audit(t, m, c, l, "2027-01-09T12:00:00Z")); got != "refused unknown_provider_key" {
			t.Fatalf("audit = %s", got)
		}
		if err := m.RegisterSuccession(s); err != nil {
			t.Fatal(err)
		}
		if got := dueIDs(t, m, "2027-01-09T13:00:00Z"); len(got) != 1 || got[0] != c.ClaimID {
			t.Fatalf("due after the succession = %v", got)
		}
		data, err := m.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		restored, err := Restore(w.config(m), data)
		if err != nil {
			t.Fatalf("Restore() = %v", err)
		}
		if got := result(audit(t, restored, c, l, "2027-01-09T12:00:00Z")); got != "9989392" {
			t.Fatalf("restored re-audit = %s", got)
		}
	})
	t.Run("a tampered witness fails to restore", func(t *testing.T) {
		w := primed(t)
		m := w.monitors[0]
		data, err := m.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		witness := m.Page(keys.DIDOf(w.provider), btc, 0, 10).Witnesses[0]
		tampered := bytes.Replace(data, []byte(`"credit_base_units":"9989528"`), []byte(`"credit_base_units":"9989529"`), 1)
		if bytes.Equal(tampered, data) {
			t.Fatalf("snapshot does not contain witness %s", witness.ClaimID)
		}
		if _, err := Restore(w.config(m), tampered); err == nil {
			t.Fatal("Restore accepted a tampered witness")
		}
	})
}

func TestInForceWithin(t *testing.T) {
	w := newWorld(t)
	first := w.example.Genesis.FeeSchedules[0]
	revised := first
	revised.Revision = 2
	revised.EffectiveAt = "2027-03-01T00:00:00Z"
	revised.ExpiresAt = "2028-03-01T00:00:00Z"
	id, err := fee.ScheduleID(revised)
	if err != nil {
		t.Fatal(err)
	}
	revised.ScheduleID = id
	// The revision has no release approval; this fixture tests only the 3,600-second window.
	s := &VerifiedSchedules{entries: []wire.FeeSchedule{first, revised}, byID: map[string]settlement.Schedule{}}
	check := func(t *testing.T, scheduleID, when string, want bool) {
		t.Helper()
		if got := s.InForceWithin(scheduleID, btc, at(t, when)); got != want {
			t.Fatalf("InForceWithin(%s) = %t; want %t", when, got, want)
		}
	}
	t.Run("first schedule one second before it takes effect", func(t *testing.T) { check(t, first.ScheduleID, "2026-12-31T23:59:59Z", false) })
	t.Run("first schedule at its effective_at", func(t *testing.T) { check(t, first.ScheduleID, "2027-01-01T00:00:00Z", true) })
	t.Run("first schedule on another network", func(t *testing.T) {
		if s.InForceWithin(first.ScheduleID, eth, at(t, "2027-02-01T00:00:00Z")) {
			t.Fatal("bitcoin schedule in force on ethereum")
		}
	})
	t.Run("replaced schedule 3,599 seconds after the revision", func(t *testing.T) { check(t, first.ScheduleID, "2027-03-01T00:59:59Z", true) })
	t.Run("replaced schedule 3,600 seconds after the revision", func(t *testing.T) { check(t, first.ScheduleID, "2027-03-01T01:00:00Z", false) })
	t.Run("revision one second before its effective_at", func(t *testing.T) { check(t, revised.ScheduleID, "2027-02-28T23:59:59Z", false) })
	t.Run("revision at its effective_at", func(t *testing.T) { check(t, revised.ScheduleID, "2027-03-01T00:00:00Z", true) })
}

func TestNewSchedulesRejectsMissingApproval(t *testing.T) {
	w := newWorld(t)
	g := w.example.Genesis
	_, err := NewSchedules([]wire.ApprovedFeeSchedule{
		{Schedule: g.FeeSchedules[0], Approval: g.FeeScheduleApprovals[1]},
		{Schedule: g.FeeSchedules[1], Approval: g.FeeScheduleApprovals[0]},
	}, g.ReleaseKeyIDs, g.ReleaseThreshold)
	if err == nil {
		t.Fatal("NewSchedules accepted approvals of other schedules")
	}
}
