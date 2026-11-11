package settlement

import (
	"encoding/json"
	"math/big"
	"reflect"
	"testing"
	"time"

	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/succession"
	"foryou.local/foryou/internal/wire"
)

func TestBuildAndVerifyProof(t *testing.T) {
	example := readSettlementTreeExample(t)
	context := exampleProofContext(t)
	t.Run("example proof settles with the minimum witness credit", func(t *testing.T) {
		credit, err := VerifyProof(example.Receipt, example.SettlementProof, context)
		if err != nil {
			t.Fatalf("VerifyProof() error = %v", err)
		}
		if credit.Cmp(big.NewInt(9989528)) != 0 {
			t.Fatalf("credit = %s; want 9989528", credit)
		}
		if got := ReceiptState(example.Receipt, &example.SettlementProof, context); got != StateSettled {
			t.Fatalf("ReceiptState() = %q; want %q", got, StateSettled)
		}
	})
	t.Run("empty committee cannot settle", func(t *testing.T) {
		if got := ReceiptState(example.Receipt, &example.SettlementProof, ProofContext{}); got != StateUnsettled {
			t.Fatalf("ReceiptState() = %q; want %q", got, StateUnsettled)
		}
	})
	t.Run("nil proof is unsettled", func(t *testing.T) {
		if got := ReceiptState(example.Receipt, nil, context); got != StateUnsettled {
			t.Fatalf("ReceiptState() = %q; want %q", got, StateUnsettled)
		}
	})
	t.Run("builder derives the example audit path", func(t *testing.T) {
		proof, err := BuildProof(example.SettlementClaim, example.SettlementLeaves, int(example.SettlementProof.LeafIndex), example.SettlementProof.Witnesses, nil)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(proof, example.SettlementProof) {
			t.Fatalf("BuildProof() = %#v; want %#v", proof, example.SettlementProof)
		}
	})
	t.Run("builder omits an empty chain", func(t *testing.T) {
		proof, err := BuildProof(example.SettlementClaim, example.SettlementLeaves, int(example.SettlementProof.LeafIndex), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(proof)
		if err != nil || containsJSONField(encoded, `"chain"`) {
			t.Fatalf("empty chain encoding = %s, %v; want chain omitted", encoded, err)
		}
	})
	t.Run("builder rejects 513 witnesses", func(t *testing.T) {
		if _, err := BuildProof(example.SettlementClaim, example.SettlementLeaves, 0, make([]wire.SettlementWitness, 513), nil); err == nil {
			t.Fatal("BuildProof accepted 513 witnesses")
		}
	})
	t.Run("builder rejects 1001 chain claims", func(t *testing.T) {
		if _, err := BuildProof(example.SettlementClaim, example.SettlementLeaves, 0, nil, make([]wire.SettlementClaim, 1001)); err == nil {
			t.Fatal("BuildProof accepted 1001 chain claims")
		}
	})
	t.Run("builder rejects an out of range index", func(t *testing.T) {
		if _, err := BuildProof(example.SettlementClaim, example.SettlementLeaves, len(example.SettlementLeaves), nil, nil); err == nil {
			t.Fatal("BuildProof accepted an out of range index")
		}
	})
	t.Run("a witnessed child settles its ancestor", func(t *testing.T) {
		vectors := loadWitnessVectors(t)
		proof := example.SettlementProof
		proof.Chain = []wire.SettlementClaim{vectors.SecondClaim}
		proof.Witnesses = vectors.SecondClaimWitnesses
		credit, err := VerifyProof(example.Receipt, proof, context)
		if err != nil {
			t.Fatalf("VerifyProof() with child claim error = %v", err)
		}
		if credit.Cmp(big.NewInt(9989392)) != 0 {
			t.Fatalf("credit = %s; want 9989392", credit)
		}
		proof.Chain = nil
		if got := ReceiptState(example.Receipt, &proof, context); got != StateUnsettled {
			t.Fatalf("ReceiptState() without chain = %q; want %q", got, StateUnsettled)
		}
	})
	t.Run("previous committee quorum counts only before handover", func(t *testing.T) {
		vectors := loadWitnessVectors(t)
		proof := example.SettlementProof
		proof.Chain = []wire.SettlementClaim{vectors.SecondClaim}
		proof.Witnesses = vectors.SecondClaimWitnesses
		changedAt := time.Date(2027, time.February, 1, 0, 0, 0, 0, time.UTC)
		newCommittee := []string{vectors.Monitors[0], vectors.Monitors[1], keys.DIDOf(monitorKey(t, "5")), keys.DIDOf(monitorKey(t, "6")), keys.DIDOf(monitorKey(t, "7"))}
		context := ProofContext{Committee: newCommittee, PreviousCommittee: vectors.Monitors, ChangedAt: &changedAt}
		credit, err := VerifyProof(example.Receipt, proof, context)
		if err != nil || credit.Cmp(big.NewInt(9989392)) != 0 {
			t.Fatalf("VerifyProof() during handover = %v, %v; want credit 9989392", credit, err)
		}
	})
	t.Run("new committee witnesses do not settle by themselves", func(t *testing.T) {
		vectors := loadWitnessVectors(t)
		proof := example.SettlementProof
		proof.Chain = []wire.SettlementClaim{vectors.SecondClaim}
		proof.Witnesses = vectors.SecondClaimWitnesses
		committee := []string{vectors.Monitors[0], vectors.Monitors[1], keys.DIDOf(monitorKey(t, "5")), keys.DIDOf(monitorKey(t, "6")), keys.DIDOf(monitorKey(t, "7"))}
		if got := ReceiptState(example.Receipt, &proof, ProofContext{Committee: committee}); got != StateUnsettled {
			t.Fatalf("ReceiptState() = %q; want %q", got, StateUnsettled)
		}
	})
	t.Run("departed and new committee witnesses are not pooled", func(t *testing.T) {
		vectors := loadWitnessVectors(t)
		proof := example.SettlementProof
		proof.Chain = []wire.SettlementClaim{vectors.SecondClaim}
		newKey := monitorKey(t, "5")
		newWitness := mustSignWitness(t, newKey, vectors.SecondClaim, time.Date(2027, 1, 14, 12, 0, 0, 0, time.UTC))
		proof.Witnesses = append(append([]wire.SettlementWitness{}, vectors.SecondClaimWitnesses[2:]...), newWitness)
		committee := []string{vectors.SecondClaimWitnesses[0].WitnessID, vectors.SecondClaimWitnesses[1].WitnessID, keys.DIDOf(newKey), keys.DIDOf(monitorKey(t, "6")), keys.DIDOf(monitorKey(t, "7"))}
		changedAt := time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC)
		ctx := ProofContext{Committee: committee, PreviousCommittee: vectors.Monitors, ChangedAt: &changedAt}
		if got := ReceiptState(example.Receipt, &proof, ctx); got != StateUnsettled {
			t.Fatalf("ReceiptState() = %q; want %q", got, StateUnsettled)
		}
	})
	t.Run("old committee witnesses at the change do not count", func(t *testing.T) {
		vectors := loadWitnessVectors(t)
		proof := example.SettlementProof
		proof.Chain = []wire.SettlementClaim{vectors.SecondClaim}
		proof.Witnesses = []wire.SettlementWitness{
			redateWitness(t, vectors.SecondClaimWitnesses[0], monitorKey(t, "0"), "2027-02-01T00:00:00Z"),
			redateWitness(t, vectors.SecondClaimWitnesses[1], monitorKey(t, "1"), "2027-02-01T00:00:00Z"),
			redateWitness(t, vectors.SecondClaimWitnesses[2], monitorKey(t, "2"), "2027-02-01T00:00:00Z"),
			redateWitness(t, vectors.SecondClaimWitnesses[3], monitorKey(t, "3"), "2027-02-01T00:00:00Z"),
		}
		changedAt := time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC)
		ctx := ProofContext{Committee: []string{"new-1", "new-2", "new-3"}, PreviousCommittee: vectors.Monitors, ChangedAt: &changedAt}
		if got := ReceiptState(example.Receipt, &proof, ctx); got != StateUnsettled {
			t.Fatalf("ReceiptState() = %q; want %q", got, StateUnsettled)
		}
	})
	t.Run("previous committee without change time is inactive", func(t *testing.T) {
		vectors := loadWitnessVectors(t)
		proof := example.SettlementProof
		proof.Chain = []wire.SettlementClaim{vectors.SecondClaim}
		proof.Witnesses = vectors.SecondClaimWitnesses
		if got := ReceiptState(example.Receipt, &proof, ProofContext{Committee: []string{"new"}, PreviousCommittee: vectors.Monitors}); got != StateUnsettled {
			t.Fatalf("ReceiptState() = %q; want %q", got, StateUnsettled)
		}
	})
	t.Run("Foundation signatures cannot supply a quorum", func(t *testing.T) {
		vectors := loadWitnessVectors(t)
		proof := example.SettlementProof
		proof.Chain = []wire.SettlementClaim{vectors.SecondClaim}
		key := foundationKey(t)
		at := time.Date(2027, 1, 14, 12, 0, 0, 0, time.UTC)
		proof.Witnesses = []wire.SettlementWitness{
			mustSignWitness(t, key, vectors.SecondClaim, at), mustSignWitness(t, key, vectors.SecondClaim, at),
			mustSignWitness(t, key, vectors.SecondClaim, at), mustSignWitness(t, key, vectors.SecondClaim, at), mustSignWitness(t, key, vectors.SecondClaim, at),
		}
		if got := ReceiptState(example.Receipt, &proof, exampleProofContext(t)); got != StateUnsettled {
			t.Fatalf("ReceiptState() = %q; want %q", got, StateUnsettled)
		}
	})
	t.Run("provider signature cannot supply a witness", func(t *testing.T) {
		vectors := loadWitnessVectors(t)
		proof := example.SettlementProof
		proof.Chain = []wire.SettlementClaim{vectors.SecondClaim}
		provider := testProviderKey(t)
		at := time.Date(2027, 1, 14, 12, 0, 0, 0, time.UTC)
		proof.Witnesses = []wire.SettlementWitness{
			mustSignWitness(t, provider, vectors.SecondClaim, at), mustSignWitness(t, provider, vectors.SecondClaim, at),
			mustSignWitness(t, provider, vectors.SecondClaim, at), mustSignWitness(t, provider, vectors.SecondClaim, at),
		}
		if got := ReceiptState(example.Receipt, &proof, exampleProofContext(t)); got != StateUnsettled {
			t.Fatalf("ReceiptState() = %q; want %q", got, StateUnsettled)
		}
	})
	t.Run("second claim requires a distinct quorum of unchanged witnesses", func(t *testing.T) {
		vectors := loadWitnessVectors(t)
		proof := example.SettlementProof
		proof.Chain = []wire.SettlementClaim{vectors.SecondClaim}
		proof.Witnesses = vectors.SecondClaimWitnesses[:3]
		if got := ReceiptState(example.Receipt, &proof, exampleProofContext(t)); got != StateUnsettled {
			t.Fatalf("three witnesses state = %q; want %q", got, StateUnsettled)
		}
		proof.Witnesses = []wire.SettlementWitness{vectors.SecondClaimWitnesses[0], vectors.SecondClaimWitnesses[0], vectors.SecondClaimWitnesses[0], vectors.SecondClaimWitnesses[0]}
		if got := ReceiptState(example.Receipt, &proof, exampleProofContext(t)); got != StateUnsettled {
			t.Fatalf("duplicate witnesses state = %q; want %q", got, StateUnsettled)
		}
		proof.Witnesses = []wire.SettlementWitness{
			vectors.SecondClaimWitnesses[0], vectors.SecondClaimWitnesses[1], vectors.SecondClaimWitnesses[2],
			vectors.SecondClaimWitnesses[3], vectors.SecondClaimWitnesses[4],
		}
		proof.Witnesses[0].CreditBaseUnits = "1"
		proof.Witnesses[1].CreditBaseUnits = "1"
		proof.Witnesses[2].CreditBaseUnits = "1"
		proof.Witnesses[3].CreditBaseUnits = "1"
		proof.Witnesses[4].CreditBaseUnits = "1"
		if got := ReceiptState(example.Receipt, &proof, exampleProofContext(t)); got != StateUnsettled {
			t.Fatalf("tampered witness state = %q; want %q", got, StateUnsettled)
		}
	})
	t.Run("second claim witnesses do not match the first claim", func(t *testing.T) {
		proof := example.SettlementProof
		proof.Witnesses = loadWitnessVectors(t).SecondClaimWitnesses
		if got := ReceiptState(example.Receipt, &proof, exampleProofContext(t)); got != StateUnsettled {
			t.Fatalf("ReceiptState() = %q; want %q", got, StateUnsettled)
		}
	})
	t.Run("another receipt with a changed message ID is not bound", func(t *testing.T) {
		receipt := example.Receipt
		receipt.MessageID += "-other"
		receipt = resignReceipt(t, receipt, testProviderKey(t))
		if got := ReceiptState(receipt, &example.SettlementProof, exampleProofContext(t)); got != StateUnsettled {
			t.Fatalf("ReceiptState() = %q; want %q", got, StateUnsettled)
		}
	})
	t.Run("oversized witnesses and audit paths are rejected", func(t *testing.T) {
		proof := example.SettlementProof
		proof.Witnesses = make([]wire.SettlementWitness, 513)
		if _, err := VerifyProof(example.Receipt, proof, context); err == nil {
			t.Fatal("VerifyProof accepted 513 witnesses")
		}
		proof = example.SettlementProof
		proof.AuditPath = make([]wire.AuditNode, 15)
		if _, err := VerifyProof(example.Receipt, proof, context); err == nil {
			t.Fatal("VerifyProof accepted an audit path of length 15")
		}
	})
	t.Run("negative index and oversized receipt count are rejected", func(t *testing.T) {
		proof := example.SettlementProof
		proof.LeafIndex = -1
		if _, err := VerifyProof(example.Receipt, proof, context); err == nil {
			t.Fatal("VerifyProof accepted a negative index")
		}
		proof = example.SettlementProof
		proof.Claim.ReceiptCount = 10001
		if _, err := VerifyProof(example.Receipt, proof, context); err == nil {
			t.Fatal("VerifyProof accepted an out of range receipt count")
		}
	})
	t.Run("a succession-linked child settles its ancestor", func(t *testing.T) {
		scenario := buildAncestorScenario(t)
		proof, err := BuildProof(scenario.claimA, []wire.SettlementLeaf{scenario.leafA}, 0, fourWitnesses(t, scenario.child), []wire.SettlementClaim{scenario.child})
		if err != nil {
			t.Fatal(err)
		}
		if got := ReceiptState(scenario.receiptA, &proof, scenario.context); got != StateSettled {
			t.Fatalf("ancestor state = %q; want %q", got, StateSettled)
		}
		proof.Chain = nil
		if got := ReceiptState(scenario.receiptA, &proof, scenario.context); got != StateUnsettled {
			t.Fatalf("ancestor without chain state = %q; want %q", got, StateUnsettled)
		}
	})
	t.Run("a rival fork is not an ancestor of the witnessed child", func(t *testing.T) {
		scenario := buildAncestorScenario(t)
		proof, err := BuildProof(scenario.claimB, []wire.SettlementLeaf{scenario.leafB}, 0, fourWitnesses(t, scenario.child), []wire.SettlementClaim{scenario.child})
		if err != nil {
			t.Fatal(err)
		}
		if got := ReceiptState(scenario.receiptB, &proof, scenario.context); got != StateUnsettled {
			t.Fatalf("rival fork state = %q; want %q", got, StateUnsettled)
		}
	})
	t.Run("provider key change requires a succession", func(t *testing.T) {
		scenario := buildAncestorScenario(t)
		childKey := providerNextKey(t)
		input := claimInputFromFixture(t, scenario.child, &scenario.claimA)
		withoutSuccession, err := BuildClaim(childKey, input)
		if err != nil {
			t.Fatal(err)
		}
		proof, err := BuildProof(scenario.claimA, []wire.SettlementLeaf{scenario.leafA}, 0, fourWitnesses(t, withoutSuccession), []wire.SettlementClaim{withoutSuccession})
		if err != nil {
			t.Fatal(err)
		}
		if got := ReceiptState(scenario.receiptA, &proof, scenario.context); got != StateUnsettled {
			t.Fatalf("missing succession state = %q; want %q", got, StateUnsettled)
		}
	})
	t.Run("supplied succession within period bounds settles", func(t *testing.T) {
		scenario := buildAncestorScenario(t)
		childKey := providerNextKey(t)
		effectiveAt, _ := wire.ParseTime("2027-01-13T00:00:00Z")
		keySuccession, err := succession.New(testProviderKey(t), childKey, effectiveAt)
		if err != nil {
			t.Fatal(err)
		}
		input := claimInputFromFixture(t, scenario.child, &scenario.claimA)
		input.SuccessionID = &keySuccession.SuccessionID
		rotated, err := BuildClaim(childKey, input)
		if err != nil {
			t.Fatal(err)
		}
		proof, err := BuildProof(scenario.claimA, []wire.SettlementLeaf{scenario.leafA}, 0, fourWitnesses(t, rotated), []wire.SettlementClaim{rotated})
		if err != nil {
			t.Fatal(err)
		}
		ctx := scenario.context
		ctx.Successions = func(id string) (wire.KeySuccession, bool) { return keySuccession, id == keySuccession.SuccessionID }
		if got := ReceiptState(scenario.receiptA, &proof, ctx); got != StateSettled {
			t.Fatalf("succession-linked ancestor state = %q; want %q", got, StateSettled)
		}
		ctx.Successions = nil
		if got := ReceiptState(scenario.receiptA, &proof, ctx); got != StateUnsettled {
			t.Fatalf("missing held succession state = %q; want %q", got, StateUnsettled)
		}
	})
	t.Run("succession after child period start does not link", func(t *testing.T) {
		scenario := buildAncestorScenario(t)
		childKey := providerNextKey(t)
		effectiveAt, _ := wire.ParseTime("2027-01-14T00:00:00Z")
		keySuccession, err := succession.New(testProviderKey(t), childKey, effectiveAt)
		if err != nil {
			t.Fatal(err)
		}
		input := claimInputFromFixture(t, scenario.child, &scenario.claimA)
		input.SuccessionID = &keySuccession.SuccessionID
		rotated, err := BuildClaim(childKey, input)
		if err != nil {
			t.Fatal(err)
		}
		proof, err := BuildProof(scenario.claimA, []wire.SettlementLeaf{scenario.leafA}, 0, fourWitnesses(t, rotated), []wire.SettlementClaim{rotated})
		if err != nil {
			t.Fatal(err)
		}
		ctx := scenario.context
		ctx.Successions = func(id string) (wire.KeySuccession, bool) { return keySuccession, id == keySuccession.SuccessionID }
		if got := ReceiptState(scenario.receiptA, &proof, ctx); got != StateUnsettled {
			t.Fatalf("misdated succession state = %q; want %q", got, StateUnsettled)
		}
	})
	t.Run("schema bounds reject before verification", func(t *testing.T) {
		proof := example.SettlementProof
		proof.Chain = make([]wire.SettlementClaim, proofChainLimit+1)
		if _, err := VerifyProof(example.Receipt, proof, context); err == nil {
			t.Fatal("VerifyProof accepted an oversized chain")
		}
	})
}
