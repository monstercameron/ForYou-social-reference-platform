package settlement

import (
	"math/big"
	"testing"
	"time"

	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

func TestQuorumAndMaxFaulty(t *testing.T) {
	check := func(t *testing.T, n, wantQuorum, wantFaulty int) {
		t.Helper()
		if got := Quorum(n); got != wantQuorum {
			t.Fatalf("Quorum(%d) = %d; want %d", n, got, wantQuorum)
		}
		if got := MaxFaulty(n); got != wantFaulty {
			t.Fatalf("MaxFaulty(%d) = %d; want %d", n, got, wantFaulty)
		}
	}
	t.Run("five members", func(t *testing.T) { check(t, 5, 4, 1) })
	t.Run("six members", func(t *testing.T) {
		if got := Quorum(6); got != 5 {
			t.Fatalf("Quorum(6) = %d; want 5", got)
		}
	})
	t.Run("seven members", func(t *testing.T) { check(t, 7, 5, 2) })
}

func TestCountWitnessesAndSettled(t *testing.T) {
	vectors := loadWitnessVectors(t)
	members := vectors.Monitors
	check := func(t *testing.T, witnesses []wire.SettlementWitness, want int, settled bool) {
		t.Helper()
		counted := CountWitnesses(vectors.SecondClaim, witnesses, members, nil)
		if len(counted) != want {
			t.Fatalf("counted %d monitors; want %d", len(counted), want)
		}
		if got := Settled(counted, members); got != settled {
			t.Fatalf("Settled() = %v; want %v", got, settled)
		}
	}
	t.Run("four witnesses settle", func(t *testing.T) { check(t, vectors.SecondClaimWitnesses[:4], 4, true) })
	t.Run("three witnesses do not settle", func(t *testing.T) { check(t, vectors.SecondClaimWitnesses[:3], 3, false) })
	t.Run("duplicate signature counts once", func(t *testing.T) {
		check(t, []wire.SettlementWitness{vectors.SecondClaimWitnesses[0], vectors.SecondClaimWitnesses[0], vectors.SecondClaimWitnesses[0], vectors.SecondClaimWitnesses[0]}, 1, false)
	})
	t.Run("smallest valid credit is retained regardless of order", func(t *testing.T) {
		at, err := wire.ParseTime(vectors.SecondClaimWitnesses[0].WitnessedAt)
		if err != nil {
			t.Fatal(err)
		}
		lowerCredit, err := SignWitness(monitorKey(t, "0"), vectors.SecondClaim, big.NewInt(1), at)
		if err != nil {
			t.Fatal(err)
		}
		first := CountWitnesses(vectors.SecondClaim, []wire.SettlementWitness{vectors.SecondClaimWitnesses[0], lowerCredit}, members, nil)
		second := CountWitnesses(vectors.SecondClaim, []wire.SettlementWitness{lowerCredit, vectors.SecondClaimWitnesses[0]}, members, nil)
		firstCredit, firstOK := first[lowerCredit.WitnessID]
		secondCredit, secondOK := second[lowerCredit.WitnessID]
		if !firstOK || !secondOK || firstCredit.String() != "1" || secondCredit.String() != "1" {
			t.Fatalf("credits = %v and %v; want 1 in both orders", firstCredit, secondCredit)
		}
	})
	t.Run("cutoff after witnesses includes all", func(t *testing.T) {
		before, _ := wire.ParseTime("2027-01-13T12:00:01Z")
		if got := len(CountWitnesses(vectors.SecondClaim, vectors.SecondClaimWitnesses, members, &before)); got != 5 {
			t.Fatalf("counted %d; want 5", got)
		}
	})
	t.Run("cutoff at witness time excludes all", func(t *testing.T) {
		before, _ := wire.ParseTime("2027-01-13T12:00:00Z")
		if got := len(CountWitnesses(vectors.SecondClaim, vectors.SecondClaimWitnesses, members, &before)); got != 0 {
			t.Fatalf("counted %d; want 0", got)
		}
	})
	t.Run("witnesses for another claim count zero", func(t *testing.T) {
		otherClaim := vectors.SecondClaim
		otherClaim.ClaimID = "another-claim"
		counted := CountWitnesses(otherClaim, vectors.SecondClaimWitnesses, members, nil)
		if len(counted) != 0 {
			t.Fatalf("counted %d witnesses for another claim; want 0", len(counted))
		}
	})
	t.Run("witness by a key outside the committee counts zero", func(t *testing.T) {
		seed := []byte{32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63}
		foreign, err := keys.NewKeyFromSeed(seed)
		if err != nil {
			t.Fatal(err)
		}
		witness, err := SignWitness(foreign, vectors.SecondClaim, big.NewInt(1), time.Date(2027, time.January, 13, 12, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		counted := CountWitnesses(vectors.SecondClaim, []wire.SettlementWitness{witness}, members, nil)
		if len(counted) != 0 {
			t.Fatalf("counted %d foreign witnesses; want 0", len(counted))
		}
	})
	t.Run("duplicate member IDs use distinct quorum", func(t *testing.T) {
		counted := map[string]*big.Int{"a": big.NewInt(1), "b": big.NewInt(1), "c": big.NewInt(1), "d": big.NewInt(1)}
		if !Settled(counted, []string{"a", "a", "b", "c", "d", "e"}) {
			t.Fatal("four of five distinct members did not settle")
		}
	})
}
