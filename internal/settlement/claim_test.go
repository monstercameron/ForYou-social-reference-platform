package settlement

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

type claimVectors struct {
	SettlementClaim wire.SettlementClaim        `json:"settlement_claim"`
	SecondClaim     wire.SettlementClaim        `json:"second_claim"`
	Declarations    []wire.RetentionDeclaration `json:"retention_declarations"`
	CommitmentHex   string                      `json:"settlement_deposit_commitment_hex"`
	Succession      wire.KeySuccession          `json:"key_succession"`
}

func loadClaimVectors(t *testing.T) claimVectors {
	t.Helper()
	data, err := os.ReadFile("testdata/claim_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors claimVectors
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	return vectors
}

func testProviderKey(t *testing.T) keys.PrivateKey {
	t.Helper()
	seed, err := hex.DecodeString("202122232425262728292a2b2c2d2e2f303132333435363738393a3b3c3d3e3f")
	if err != nil {
		t.Fatal(err)
	}
	key, err := keys.NewKeyFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func claimInputFromFixture(t *testing.T, c wire.SettlementClaim, previous *wire.SettlementClaim) ClaimInput {
	t.Helper()
	start, err := wire.ParseTime(c.PeriodStart)
	if err != nil {
		t.Fatal(err)
	}
	end, err := wire.ParseTime(c.PeriodEnd)
	if err != nil {
		t.Fatal(err)
	}
	total, err := wire.PositiveAmount(c.TotalBaseUnits)
	if err != nil {
		t.Fatal(err)
	}
	return ClaimInput{Network: c.Network, Previous: previous, PeriodStart: start, PeriodEnd: end, DeclarationID: c.DeclarationID, SuccessionID: c.SuccessionID, ReceiptCount: int(c.ReceiptCount), Total: total, ReceiptsRoot: c.ReceiptsRoot}
}

func TestClaimIDAndVerify(t *testing.T) {
	vectors := loadClaimVectors(t)
	check := func(t *testing.T, claim wire.SettlementClaim) {
		t.Helper()
		if err := VerifyClaimObject(claim); err != nil {
			t.Fatalf("VerifyClaimObject() error = %v", err)
		}
		id, err := ClaimID(claim)
		if err != nil || id != claim.ClaimID {
			t.Fatalf("ClaimID() = %q, %v; want %q", id, err, claim.ClaimID)
		}
	}
	t.Run("first claim", func(t *testing.T) { check(t, vectors.SettlementClaim) })
	t.Run("second claim", func(t *testing.T) { check(t, vectors.SecondClaim) })
	t.Run("changed total fails ID and signature", func(t *testing.T) {
		claim := vectors.SettlementClaim
		claim.TotalBaseUnits = "1"
		if err := VerifyClaimObject(claim); !errors.Is(err, ErrClaimSignature) {
			t.Fatalf("VerifyClaimObject() error = %v; want ErrClaimSignature", err)
		}
	})
}

func TestBuildClaim(t *testing.T) {
	vectors := loadClaimVectors(t)
	key := testProviderKey(t)
	t.Run("first claim matches signed example", func(t *testing.T) {
		got, err := BuildClaim(key, claimInputFromFixture(t, vectors.SettlementClaim, nil))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, vectors.SettlementClaim) {
			t.Fatalf("BuildClaim() = %#v; want %#v", got, vectors.SettlementClaim)
		}
	})
	t.Run("second claim matches signed example", func(t *testing.T) {
		got, err := BuildClaim(key, claimInputFromFixture(t, vectors.SecondClaim, &vectors.SettlementClaim))
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, vectors.SecondClaim) {
			t.Fatalf("BuildClaim() = %#v; want %#v", got, vectors.SecondClaim)
		}
	})
	t.Run("zero receipts rejected", func(t *testing.T) {
		input := claimInputFromFixture(t, vectors.SettlementClaim, nil)
		input.ReceiptCount = 0
		if _, err := BuildClaim(key, input); err == nil {
			t.Fatal("BuildClaim() succeeded with zero receipts")
		}
	})
	t.Run("too many receipts rejected", func(t *testing.T) {
		input := claimInputFromFixture(t, vectors.SettlementClaim, nil)
		input.ReceiptCount = 10001
		if _, err := BuildClaim(key, input); err == nil {
			t.Fatal("BuildClaim() succeeded with 10001 receipts")
		}
	})
	t.Run("period over 24 hours rejected", func(t *testing.T) {
		input := claimInputFromFixture(t, vectors.SettlementClaim, nil)
		input.PeriodEnd = input.PeriodStart.Add(24*time.Hour + time.Second)
		if _, err := BuildClaim(key, input); err == nil {
			t.Fatal("BuildClaim() succeeded with a period over 24 hours")
		}
	})
	t.Run("unsupported network rejected", func(t *testing.T) {
		input := claimInputFromFixture(t, vectors.SettlementClaim, nil)
		input.Network = "testnet"
		if _, err := BuildClaim(key, input); err == nil {
			t.Fatal("BuildClaim() succeeded on an unsupported network")
		}
	})
}
