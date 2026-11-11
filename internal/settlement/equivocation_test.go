package settlement

import (
	"encoding/json"
	"math/big"
	"testing"
	"time"

	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/succession"
	"foryou.local/foryou/internal/wire"
)

func TestVerifyEquivocation(t *testing.T) {
	predecessor, a, b := forkClaims(t)
	t.Run("two signed forks link to the predecessor", func(t *testing.T) {
		proof := BuildEquivocationProof(a, b, &predecessor, nil)
		if !VerifyEquivocation(proof) {
			t.Fatal("VerifyEquivocation rejected valid forks")
		}
		encoded, err := json.Marshal(proof)
		if err != nil || !containsJSONField(encoded, `"successions":[]`) {
			t.Fatalf("proof JSON = %s, %v; want empty successions array", encoded, err)
		}
	})
	t.Run("missing predecessor is not proof", func(t *testing.T) {
		proof := BuildEquivocationProof(a, b, nil, nil)
		if VerifyEquivocation(proof) {
			t.Fatal("VerifyEquivocation accepted forks without predecessor")
		}
	})
	t.Run("same claim is not equivocation", func(t *testing.T) {
		if VerifyEquivocation(BuildEquivocationProof(a, a, &predecessor, nil)) {
			t.Fatal("VerifyEquivocation accepted one claim twice")
		}
	})
	t.Run("unrelated signing key cannot copy predecessor reference", func(t *testing.T) {
		vectors := loadClaimVectors(t)
		input := claimInputFromFixture(t, vectors.SecondClaim, &predecessor)
		counterfeit, err := BuildClaim(providerBKey(t), input)
		if err != nil {
			t.Fatal(err)
		}
		proof := BuildEquivocationProof(a, counterfeit, &predecessor, nil)
		if VerifyEquivocation(proof) {
			t.Fatal("VerifyEquivocation accepted unrelated provider key")
		}
	})
	t.Run("edited fork without a new signature is invalid", func(t *testing.T) {
		altered := b
		altered.TotalBaseUnits = "1"
		if VerifyEquivocation(BuildEquivocationProof(a, altered, &predecessor, nil)) {
			t.Fatal("VerifyEquivocation accepted an edited claim")
		}
	})
	t.Run("named succession must be carried by the proof", func(t *testing.T) {
		vectors := loadClaimVectors(t)
		effectiveAt, err := wire.ParseTime(predecessor.PeriodEnd)
		if err != nil {
			t.Fatal(err)
		}
		keySuccession, err := succession.New(testProviderKey(t), providerBKey(t), effectiveAt)
		if err != nil {
			t.Fatal(err)
		}
		input := claimInputFromFixture(t, vectors.SecondClaim, &predecessor)
		input.SuccessionID = &keySuccession.SuccessionID
		named, err := BuildClaim(providerBKey(t), input)
		if err != nil {
			t.Fatal(err)
		}
		if VerifyEquivocation(BuildEquivocationProof(a, named, &predecessor, nil)) {
			t.Fatal("VerifyEquivocation accepted a missing succession")
		}
		proof := BuildEquivocationProof(a, named, &predecessor, []wire.KeySuccession{keySuccession})
		if !VerifyEquivocation(proof) {
			t.Fatal("VerifyEquivocation rejected a carried succession")
		}
		wrongPredecessor := vectors.SecondClaim
		if VerifyEquivocation(BuildEquivocationProof(a, named, &wrongPredecessor, []wire.KeySuccession{keySuccession})) {
			t.Fatal("VerifyEquivocation accepted the wrong predecessor")
		}
		proof.Successions[0].OldSignature = proof.Successions[0].NewSignature
		if VerifyEquivocation(proof) {
			t.Fatal("VerifyEquivocation accepted a tampered succession")
		}
	})
	t.Run("proof rejects more than two successions", func(t *testing.T) {
		proof := BuildEquivocationProof(a, b, &predecessor, nil)
		proof.Successions = []wire.KeySuccession{{}, {}, {}}
		if VerifyEquivocation(proof) {
			t.Fatal("VerifyEquivocation accepted three successions")
		}
	})
	t.Run("malformed proof shape is false", func(t *testing.T) {
		if VerifyEquivocation(wire.EquivocationProof{Claims: []wire.SettlementClaim{a}}) {
			t.Fatal("VerifyEquivocation accepted one claim")
		}
	})
	t.Run("sequence one forks require the same provider key", func(t *testing.T) {
		firstClaim := func(t *testing.T, key keys.PrivateKey, amount int64) wire.SettlementClaim {
			t.Helper()
			claim, err := BuildClaim(key, ClaimInput{
				Network: wire.BitcoinMainnet, PeriodStart: time.Date(2027, time.March, 1, 0, 0, 0, 0, time.UTC),
				PeriodEnd:     time.Date(2027, time.March, 2, 0, 0, 0, 0, time.UTC),
				DeclarationID: "fyrd2:synthetic", ReceiptCount: 1, Total: big.NewInt(amount),
				ReceiptsRoot: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			})
			if err != nil {
				t.Fatal(err)
			}
			return claim
		}
		a := firstClaim(t, testProviderKey(t), 10)
		b := firstClaim(t, testProviderKey(t), 11)
		if !VerifyEquivocation(BuildEquivocationProof(a, b, nil, nil)) {
			t.Fatal("VerifyEquivocation rejected sequence one forks from one provider")
		}
		unrelated := firstClaim(t, providerBKey(t), 11)
		if VerifyEquivocation(BuildEquivocationProof(a, unrelated, nil, nil)) {
			t.Fatal("VerifyEquivocation accepted sequence one claims from different providers")
		}
	})
}
