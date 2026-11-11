package committee

import (
	"testing"

	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

func TestEquivocationProofs(t *testing.T) {
	t.Run("returns one verified proof for a valid fork", func(t *testing.T) {
		w := primed(t)
		monitor := w.monitors[0]
		forkA, leavesA := w.second(t, "equivocation-a")
		forkB, leavesB := w.claim(t, claimSpec{previous: &w.example.Claim, start: "2027-01-12T00:00:00Z", end: "2027-01-13T00:00:00Z",
			leaves: []wire.SettlementLeaf{leaf("equivocation-b", w.btcSchedule, "136", "2027-01-12T06:00:00Z")}})
		if got := result(audit(t, monitor, forkA, leavesA, "2027-01-14T00:00:00Z")); got != "9989392" {
			t.Fatalf("first fork result = %q; want witness", got)
		}
		if got := result(audit(t, monitor, forkB, leavesB, "2027-01-14T00:00:00Z")); got != "refused bad_chain" {
			t.Fatalf("second fork result = %q; want bad_chain refusal", got)
		}
		proofs := monitor.EquivocationProofs(forkA.ProviderID)
		if len(proofs) != 1 || !settlement.VerifyEquivocation(proofs[0]) {
			t.Fatalf("proofs = %d; want one independently valid proof", len(proofs))
		}
		if proofs[0].Predecessor == nil || proofs[0].Predecessor.ClaimID != w.example.Claim.ClaimID || proofs[0].Claims[0].Sequence != 2 {
			t.Fatal("sequence-two proof does not include the example predecessor")
		}
		audit(t, monitor, forkB, leavesB, "2027-01-15T00:00:00Z")
		if again := monitor.EquivocationProofs(forkA.ProviderID); len(again) != 1 {
			t.Fatalf("auditing the same fork twice yielded %d proofs; want one deduplicated proof", len(again))
		}
		badClaim, badLeaves := w.claim(t, claimSpec{previous: &w.example.Claim, start: "2027-01-12T00:00:00Z", end: "2027-01-13T00:00:00Z",
			leaves: []wire.SettlementLeaf{leaf("impossible-fork", w.btcSchedule, "137", "2027-01-12T06:00:00Z")}})
		if got := result(audit(t, monitor, badClaim, badLeaves, "2027-01-14T00:00:00Z")); got != "refused bad_chain" {
			t.Fatalf("invalid fork result = %q; want bad_chain refusal", got)
		}
		if proofs := monitor.EquivocationProofs(forkA.ProviderID); len(proofs) != 1 {
			t.Fatalf("invalid fork changed proof count to %d; want one valid proof", len(proofs))
		}
	})
	t.Run("unrelated key copying the predecessor is not a proof", func(t *testing.T) {
		w := primed(t)
		claimA, leavesA := w.second(t, "line-two")
		monitor := w.monitors[0]
		if got := result(audit(t, monitor, claimA, leavesA, "2027-01-14T00:00:00Z")); got != "9989392" {
			t.Fatalf("honest second claim result = %q; want witness", got)
		}
		claimB, leavesB := w.claim(t, claimSpec{key: w.providerB, previous: &w.example.Claim, start: "2027-01-12T00:00:00Z", end: "2027-01-13T00:00:00Z",
			declaration: w.declB.DeclarationID,
			leaves:      []wire.SettlementLeaf{leaf("unrelated", w.btcSchedule, "136", "2027-01-12T06:00:00Z")}})
		if got := result(audit(t, monitor, claimB, leavesB, "2027-01-14T00:00:00Z")); got != "refused unknown_provider_key" {
			t.Fatalf("unrelated claim result = %q; want unknown_provider_key refusal", got)
		}
		if proofs := monitor.EquivocationProofs(keys.DIDOf(w.providerB)); len(proofs) != 0 {
			t.Fatalf("unrelated provider proofs = %d; want none", len(proofs))
		}
		if proofs := monitor.EquivocationProofs(claimA.ProviderID); len(proofs) != 0 {
			t.Fatalf("honest provider proofs = %d; want none", len(proofs))
		}
	})
}
