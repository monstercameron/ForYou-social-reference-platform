package settlement

import (
	"errors"
	"testing"

	"foryou.local/foryou/internal/wire"
)

func TestCheckLink(t *testing.T) {
	vectors := loadClaimVectors(t)
	checkKind := func(t *testing.T, claim wire.SettlementClaim, previous *wire.SettlementClaim, successions SuccessionLookup, want LinkKind) {
		t.Helper()
		err := CheckLink(claim, previous, successions)
		linkErr, ok := err.(*LinkError)
		if !ok || linkErr.Kind != want {
			t.Fatalf("CheckLink() error = %v; want LinkError kind %d", err, want)
		}
	}
	t.Run("first claim without predecessor", func(t *testing.T) {
		if err := CheckLink(vectors.SettlementClaim, nil, nil); err != nil {
			t.Fatalf("CheckLink() error = %v", err)
		}
	})
	t.Run("second claim follows predecessor", func(t *testing.T) {
		if err := CheckLink(vectors.SecondClaim, &vectors.SettlementClaim, nil); err != nil {
			t.Fatalf("CheckLink() error = %v", err)
		}
	})
	t.Run("wrong sequence is chain error", func(t *testing.T) {
		claim := vectors.SecondClaim
		claim.Sequence++
		checkKind(t, claim, &vectors.SettlementClaim, nil, LinkChain)
	})
	t.Run("wrong cumulative total is chain error", func(t *testing.T) {
		claim := vectors.SecondClaim
		claim.CumulativeBaseUnits = "10609"
		checkKind(t, claim, &vectors.SettlementClaim, nil, LinkChain)
	})
	t.Run("overlapping period is period error", func(t *testing.T) {
		claim := vectors.SecondClaim
		claim.PeriodStart = "2027-01-05T12:00:00Z"
		claim.PeriodEnd = "2027-01-06T12:00:00Z"
		checkKind(t, claim, &vectors.SettlementClaim, nil, LinkPeriod)
	})
	t.Run("period over 24 hours is period error", func(t *testing.T) {
		claim := vectors.SecondClaim
		claim.PeriodEnd = "2027-01-13T00:00:01Z"
		checkKind(t, claim, &vectors.SettlementClaim, nil, LinkPeriod)
	})
	t.Run("same key cannot name succession", func(t *testing.T) {
		claim := vectors.SecondClaim
		id := vectors.Succession.SuccessionID
		claim.SuccessionID = &id
		checkKind(t, claim, &vectors.SettlementClaim, nil, LinkSuccession)
	})
	t.Run("new key requires succession", func(t *testing.T) {
		claim := vectors.SecondClaim
		claim.ProviderID = vectors.Succession.NewID
		checkKind(t, claim, &vectors.SettlementClaim, nil, LinkSuccession)
	})
	t.Run("missing named succession is a key error", func(t *testing.T) {
		claim := vectors.SecondClaim
		claim.ProviderID = vectors.Succession.NewID
		id := vectors.Succession.SuccessionID
		claim.SuccessionID = &id
		checkKind(t, claim, &vectors.SettlementClaim, func(string) (wire.KeySuccession, bool) { return wire.KeySuccession{}, false }, LinkKey)
	})
	t.Run("succession effective after claim start is provable failure", func(t *testing.T) {
		claim := vectors.SecondClaim
		claim.ProviderID = vectors.Succession.NewID
		id := vectors.Succession.SuccessionID
		claim.SuccessionID = &id
		succession := vectors.Succession
		succession.EffectiveAt = "2027-01-13T00:00:00Z"
		checkKind(t, claim, &vectors.SettlementClaim, func(string) (wire.KeySuccession, bool) { return succession, true }, LinkSuccession)
	})
	t.Run("valid succession bridges claim periods", func(t *testing.T) {
		claim := vectors.SecondClaim
		claim.ProviderID = vectors.Succession.NewID
		id := vectors.Succession.SuccessionID
		claim.SuccessionID = &id
		succession := vectors.Succession
		succession.EffectiveAt = "2027-01-10T00:00:00Z"
		if err := CheckLink(claim, &vectors.SettlementClaim, func(string) (wire.KeySuccession, bool) { return succession, true }); err != nil {
			t.Fatalf("CheckLink() error = %v", err)
		}
	})
	t.Run("network mismatch is key error", func(t *testing.T) {
		previous := vectors.SettlementClaim
		previous.Network = wire.EthereumMainnet
		checkKind(t, vectors.SecondClaim, &previous, nil, LinkKey)
	})
	t.Run("unsupported network is rejected", func(t *testing.T) {
		claim := vectors.SecondClaim
		claim.Network = "testnet"
		previous := vectors.SettlementClaim
		previous.Network = "testnet"
		if err := CheckLink(claim, &previous, nil); err == nil {
			t.Fatal("CheckLink() succeeded on an unsupported network")
		} else if errors.As(err, new(*LinkError)) {
			t.Fatalf("unsupported network error = %v; want network error", err)
		}
	})
}

func TestCheckDeclaration(t *testing.T) {
	vectors := loadClaimVectors(t)
	declarations := map[string]wire.RetentionDeclaration{
		vectors.Declarations[0].DeclarationID: vectors.Declarations[0],
		vectors.Declarations[1].DeclarationID: vectors.Declarations[1],
	}
	lookup := func(id string) (wire.RetentionDeclaration, bool) {
		declaration, ok := declarations[id]
		return declaration, ok
	}
	t.Run("example declaration is accepted", func(t *testing.T) {
		if err := CheckDeclaration(vectors.SettlementClaim, nil, lookup); err != nil {
			t.Fatalf("CheckDeclaration() error = %v", err)
		}
	})
	t.Run("declaration effective after claim start is provable failure", func(t *testing.T) {
		late := vectors.Declarations[0]
		late.DeclarationID = "fyrd2:late-declaration"
		late.EffectiveAt = "2027-01-06T00:00:01Z"
		declarations[late.DeclarationID] = late
		claim := vectors.SettlementClaim
		claim.DeclarationID = late.DeclarationID
		if err := CheckDeclaration(claim, nil, lookup); !errors.Is(err, ErrDeclarationRule) {
			t.Fatalf("CheckDeclaration() error = %v; want ErrDeclarationRule", err)
		}
	})
	t.Run("declaration cannot be backdated across predecessor period", func(t *testing.T) {
		backdated := vectors.Declarations[0]
		backdated.DeclarationID = "fyrd2:backdated-declaration"
		backdated.EffectiveAt = "2027-01-05T00:00:00Z"
		declarations[backdated.DeclarationID] = backdated
		claim := vectors.SecondClaim
		claim.DeclarationID = backdated.DeclarationID
		if err := CheckDeclaration(claim, &vectors.SettlementClaim, lookup); !errors.Is(err, ErrDeclarationRule) {
			t.Fatalf("CheckDeclaration() error = %v; want ErrDeclarationRule", err)
		}
	})
	t.Run("other provider declaration fails", func(t *testing.T) {
		other := vectors.Declarations[1]
		claim := vectors.SettlementClaim
		claim.DeclarationID = other.DeclarationID
		if err := CheckDeclaration(claim, nil, lookup); !errors.Is(err, ErrDeclarationRule) {
			t.Fatalf("CheckDeclaration() error = %v; want ErrDeclarationRule", err)
		}
	})
	t.Run("unknown declaration is not a provable fault", func(t *testing.T) {
		claim := vectors.SettlementClaim
		claim.DeclarationID = "fyrd2:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		if err := CheckDeclaration(claim, nil, lookup); !errors.Is(err, ErrDeclarationUnknown) {
			t.Fatalf("CheckDeclaration() error = %v; want ErrDeclarationUnknown", err)
		}
	})
	t.Run("successor key must name its own declaration", func(t *testing.T) {
		claim := vectors.SecondClaim
		claim.ProviderID = vectors.Succession.NewID
		if err := CheckDeclaration(claim, &vectors.SettlementClaim, lookup); !errors.Is(err, ErrDeclarationRule) {
			t.Fatalf("CheckDeclaration() error = %v; want ErrDeclarationRule", err)
		}
	})
}
