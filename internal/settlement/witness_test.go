package settlement

import (
	"crypto/sha256"
	"encoding/json"
	"math/big"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

type witnessVectors struct {
	SecondClaim          wire.SettlementClaim     `json:"second_claim"`
	SecondClaimWitnesses []wire.SettlementWitness `json:"second_claim_witnesses"`
	SettlementRefusal    wire.SettlementRefusal   `json:"settlement_refusal_example"`
	Monitors             []string                 `json:"monitors"`
}

func loadWitnessVectors(t *testing.T) witnessVectors {
	t.Helper()
	data, err := os.ReadFile("testdata/witness_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors witnessVectors
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	return vectors
}

func monitorKey(t *testing.T, n string) keys.PrivateKey {
	t.Helper()
	seed := sha256.Sum256([]byte("synthetic monitor " + n))
	key, err := keys.NewKeyFromSeed(seed[:])
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestVerifyWitness(t *testing.T) {
	vectors := loadWitnessVectors(t)
	check := func(t *testing.T, witness wire.SettlementWitness) {
		t.Helper()
		if err := VerifyWitness(witness); err != nil {
			t.Fatalf("VerifyWitness() error = %v", err)
		}
		if !WitnessMatches(witness, vectors.SecondClaim) {
			t.Fatal("witness does not match second claim")
		}
	}
	t.Run("valid committee witness", func(t *testing.T) { check(t, vectors.SecondClaimWitnesses[0]) })
	t.Run("valid second witness", func(t *testing.T) { check(t, vectors.SecondClaimWitnesses[1]) })
	t.Run("valid third witness", func(t *testing.T) { check(t, vectors.SecondClaimWitnesses[2]) })
	t.Run("valid fourth witness", func(t *testing.T) { check(t, vectors.SecondClaimWitnesses[3]) })
	t.Run("valid fifth witness", func(t *testing.T) { check(t, vectors.SecondClaimWitnesses[4]) })
	t.Run("credit mutation fails", func(t *testing.T) {
		witness := vectors.SecondClaimWitnesses[0]
		witness.CreditBaseUnits = "1"
		if VerifyWitness(witness) == nil {
			t.Fatal("VerifyWitness accepted modified credit")
		}
	})
	t.Run("period end cannot be witnessed early", func(t *testing.T) {
		witness := vectors.SecondClaimWitnesses[0]
		witness.WitnessedAt = "2027-01-12T23:59:59Z"
		encoded, err := canon.EncodeWithout(witness, "witness_signature")
		if err != nil {
			t.Fatal(err)
		}
		witness.WitnessSignature, err = keys.Sign(monitorKey(t, "0"), witnessLabel, encoded)
		if err != nil {
			t.Fatal(err)
		}
		if VerifyWitness(witness) == nil {
			t.Fatal("VerifyWitness accepted a pre-period witness")
		}
	})
}

func TestSignWitnessReproducesExample(t *testing.T) {
	vectors := loadWitnessVectors(t)
	key := monitorKey(t, "0")
	if got := keys.DIDOf(key); got != vectors.SecondClaimWitnesses[0].WitnessID {
		t.Fatalf("monitor 0 DID = %q; witness has %q", got, vectors.SecondClaimWitnesses[0].WitnessID)
	}
	expected := vectors.SecondClaimWitnesses[0]
	credit, ok := new(big.Int).SetString(expected.CreditBaseUnits, 10)
	if !ok {
		t.Fatal("invalid example credit")
	}
	at, err := wire.ParseTime(expected.WitnessedAt)
	if err != nil {
		t.Fatal(err)
	}
	got, err := SignWitness(key, vectors.SecondClaim, credit, at)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("SignWitness() = %#v; want %#v", got, expected)
	}
}

func TestSyntheticMonitorDIDsMatchExample(t *testing.T) {
	vectors := loadWitnessVectors(t)
	got := []string{
		keys.DIDOf(monitorKey(t, "0")),
		keys.DIDOf(monitorKey(t, "1")),
		keys.DIDOf(monitorKey(t, "2")),
		keys.DIDOf(monitorKey(t, "3")),
		keys.DIDOf(monitorKey(t, "4")),
	}
	sort.Strings(got)
	want := append([]string(nil), vectors.Monitors...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("synthetic monitor DIDs = %v; want %v", got, want)
	}
}

func TestVerifyRefusal(t *testing.T) {
	vectors := loadWitnessVectors(t)
	t.Run("example refusal", func(t *testing.T) {
		if err := VerifyRefusal(vectors.SettlementRefusal); err != nil {
			t.Fatalf("VerifyRefusal() error = %v", err)
		}
	})
	t.Run("unknown refusal reason rejected", func(t *testing.T) {
		r := vectors.SettlementRefusal
		r.Reason = "content"
		if err := VerifyRefusal(r); err == nil {
			t.Fatal("VerifyRefusal accepted an unknown reason")
		}
		if _, err := SignRefusal(monitorKey(t, "0"), wire.SettlementClaim{}, Reason("content"), time.Date(2027, time.January, 13, 0, 0, 0, 0, time.UTC)); err == nil {
			t.Fatal("SignRefusal accepted an unknown reason")
		}
	})
}

func TestHold(t *testing.T) {
	key := monitorKey(t, "0")
	at, _ := time.Parse(time.RFC3339, "2027-01-13T12:00:00Z")
	t.Run("claim IDs are sorted", func(t *testing.T) {
		hold, err := SignHold(key, "provider", "bitcoin-mainnet", 2, []string{"claim-b", "claim-a"}, at)
		if err != nil {
			t.Fatal(err)
		}
		if hold.ClaimIDs[0] != "claim-a" || hold.ClaimIDs[1] != "claim-b" {
			t.Fatalf("claim IDs = %v; want ascending", hold.ClaimIDs)
		}
		if err := VerifyHold(hold); err != nil {
			t.Fatalf("VerifyHold() error = %v", err)
		}
	})
	t.Run("empty IDs rejected", func(t *testing.T) {
		if _, err := SignHold(key, "provider", "bitcoin-mainnet", 2, nil, at); err == nil {
			t.Fatal("SignHold accepted no claims")
		}
	})
	t.Run("more than 64 IDs rejected", func(t *testing.T) {
		if _, err := SignHold(key, "provider", "bitcoin-mainnet", 2, make([]string, 65), at); err == nil {
			t.Fatal("SignHold accepted 65 claims")
		}
	})
	t.Run("duplicate IDs rejected", func(t *testing.T) {
		if _, err := SignHold(key, "provider", "bitcoin-mainnet", 2, []string{"same", "same"}, at); err == nil {
			t.Fatal("SignHold accepted duplicate claims")
		}
	})
	t.Run("edited hold fails verification", func(t *testing.T) {
		hold, err := SignHold(key, "provider", "bitcoin-mainnet", 2, []string{"claim-a"}, at)
		if err != nil {
			t.Fatal(err)
		}
		hold.ClaimIDs[0] = "claim-b"
		if VerifyHold(hold) == nil {
			t.Fatal("VerifyHold accepted edited claim IDs")
		}
	})
	t.Run("validly signed duplicate IDs fail verification", func(t *testing.T) {
		hold := wire.SettlementHold{
			WitnessID: keys.DIDOf(key), ProviderID: "provider", Network: "bitcoin-mainnet",
			Sequence: 2, ClaimIDs: []string{"same", "same"}, HeldAt: wire.FormatTime(at),
		}
		encoded, err := canon.EncodeWithout(hold, "witness_signature")
		if err != nil {
			t.Fatal(err)
		}
		hold.WitnessSignature, err = keys.Sign(key, holdLabel, encoded)
		if err != nil {
			t.Fatal(err)
		}
		if VerifyHold(hold) == nil {
			t.Fatal("VerifyHold accepted signed duplicate claim IDs")
		}
	})
}
