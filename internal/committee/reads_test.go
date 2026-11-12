package committee

import (
	"reflect"
	"testing"

	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

func TestAdoptedClaims(t *testing.T) {
	t.Run("period end is included when in the half-open range", func(t *testing.T) {
		w := primed(t)
		want := []ClaimRef{{
			ProviderID: w.example.Claim.ProviderID,
			Network:    w.example.Claim.Network,
			ClaimID:    w.example.Claim.ClaimID,
			Sequence:   w.example.Claim.Sequence,
		}}
		if got := w.monitors[0].AdoptedClaims(at(t, "2027-01-05T00:00:00Z"), at(t, "2027-01-07T00:00:00Z")); !reflect.DeepEqual(got, want) {
			t.Fatalf("AdoptedClaims() = %+v; want %+v", got, want)
		}
	})
	t.Run("range ending at period end excludes the claim", func(t *testing.T) {
		w := primed(t)
		if got := w.monitors[0].AdoptedClaims(at(t, "2027-01-05T00:00:00Z"), at(t, "2027-01-06T00:00:00Z")); len(got) != 0 {
			t.Fatalf("AdoptedClaims() = %+v; want no claims", got)
		}
	})
	t.Run("range starting at period end includes the claim", func(t *testing.T) {
		w := primed(t)
		want := []ClaimRef{{
			ProviderID: w.example.Claim.ProviderID,
			Network:    w.example.Claim.Network,
			ClaimID:    w.example.Claim.ClaimID,
			Sequence:   w.example.Claim.Sequence,
		}}
		if got := w.monitors[0].AdoptedClaims(at(t, "2027-01-06T00:00:00Z"), at(t, "2027-01-07T00:00:00Z")); !reflect.DeepEqual(got, want) {
			t.Fatalf("AdoptedClaims() = %+v; want %+v", got, want)
		}
	})
	t.Run("refused claim is excluded", func(t *testing.T) {
		w := primed(t)
		refused, leaves := w.second(t, "late")
		if got := result(audit(t, w.monitors[0], refused, leaves, "2027-04-14T00:00:00Z")); got != "refused bad_period" {
			t.Fatalf("late claim result = %q; want bad_period refusal", got)
		}
		want := []ClaimRef{{
			ProviderID: w.example.Claim.ProviderID,
			Network:    w.example.Claim.Network,
			ClaimID:    w.example.Claim.ClaimID,
			Sequence:   w.example.Claim.Sequence,
		}}
		if got := w.monitors[0].AdoptedClaims(at(t, "2027-01-01T00:00:00Z"), at(t, "2027-05-01T00:00:00Z")); !reflect.DeepEqual(got, want) {
			t.Fatalf("AdoptedClaims() = %+v; want %+v", got, want)
		}
	})
	t.Run("claims are ordered by provider ID", func(t *testing.T) {
		w := primed(t)
		w.deposits.set(keys.DIDOf(w.providerB), btc, 10_000_000)
		other, leaves := w.claim(t, claimSpec{
			key: w.providerB, declaration: w.declB.DeclarationID, start: "2027-01-05T00:00:00Z", end: "2027-01-06T00:00:00Z",
			leaves: []wire.SettlementLeaf{leaf("provider-b", w.btcSchedule, "136", "2027-01-05T06:00:00Z")},
		})
		if got := result(audit(t, w.monitors[0], other, leaves, "2027-01-06T00:00:00Z")); got != "9999864" {
			t.Fatalf("other provider claim result = %q; want funded witness", got)
		}
		want := []ClaimRef{
			{ProviderID: "did:key:z6MkhFwXNFWosLeugvSf4wcL9t3uuRXueGSFTRgSvHhWj5G2", Network: btc, ClaimID: w.example.Claim.ClaimID, Sequence: w.example.Claim.Sequence},
			{ProviderID: "did:key:z6Mkjppjwd12cSzdhduNMf3reWgxgkUn7WFM3pnA5f3gFcLj", Network: btc, ClaimID: other.ClaimID, Sequence: other.Sequence},
		}
		if got := w.monitors[0].AdoptedClaims(at(t, "2027-01-05T00:00:00Z"), at(t, "2027-01-07T00:00:00Z")); !reflect.DeepEqual(got, want) {
			t.Fatalf("AdoptedClaims() = %+v; want provider order %+v", got, want)
		}
	})
}

func TestReceiptClaim(t *testing.T) {
	t.Run("receipt hash returns the witnessed claim", func(t *testing.T) {
		w := primed(t)
		hash := w.example.Leaves[0].ReceiptHash
		got, ok := w.monitors[0].ReceiptClaim(w.example.Claim.ProviderID, btc, hash)
		if !ok || !reflect.DeepEqual(got, w.example.Claim) {
			t.Fatalf("ReceiptClaim() = (%+v, %t); want example claim", got, ok)
		}
	})
	t.Run("receipt hash on another network is absent", func(t *testing.T) {
		w := primed(t)
		if got, ok := w.monitors[0].ReceiptClaim(w.example.Claim.ProviderID, eth, w.example.Leaves[0].ReceiptHash); ok {
			t.Fatalf("ReceiptClaim() = (%+v, true); want no claim", got)
		}
	})
	t.Run("unknown receipt hash is absent", func(t *testing.T) {
		w := primed(t)
		if got, ok := w.monitors[0].ReceiptClaim(w.example.Claim.ProviderID, btc, "unknown"); ok {
			t.Fatalf("ReceiptClaim() = (%+v, true); want no claim", got)
		}
	})
	t.Run("new provider key resolves the old key's claim", func(t *testing.T) {
		w := primed(t)
		if err := w.monitors[0].RegisterSuccession(succeed(t, w.provider, w.providerNext, "2027-01-14T00:00:00Z")); err != nil {
			t.Fatal(err)
		}
		got, ok := w.monitors[0].ReceiptClaim(keys.DIDOf(w.providerNext), btc, w.example.Leaves[0].ReceiptHash)
		if !ok || !reflect.DeepEqual(got, w.example.Claim) {
			t.Fatalf("ReceiptClaim() = (%+v, %t); want old-key example claim", got, ok)
		}
	})
}

func TestDeclaration(t *testing.T) {
	t.Run("registered declaration is returned", func(t *testing.T) {
		w := newWorld(t)
		got, ok := w.monitors[0].Declaration(w.declA.DeclarationID)
		if !ok || got != w.declA {
			t.Fatalf("Declaration() = (%+v, %t); want registered declaration", got, ok)
		}
	})
	t.Run("unknown declaration is absent", func(t *testing.T) {
		w := newWorld(t)
		if got, ok := w.monitors[0].Declaration("unknown"); ok {
			t.Fatalf("Declaration() = (%+v, true); want no declaration", got)
		}
	})
}
