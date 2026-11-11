package retention

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

const (
	providerID  = "did:key:z6MkhFwXNFWosLeugvSf4wcL9t3uuRXueGSFTRgSvHhWj5G2"
	providerBID = "did:key:z6Mkjppjwd12cSzdhduNMf3reWgxgkUn7WFM3pnA5f3gFcLj"
)

// Source vector: C:\Users\mreca\Desktop\ForYou-Social-Ecosystem-Specification\examples\settlement.example.json.
type settlementExample struct {
	Declarations []wire.RetentionDeclaration `json:"retention_declarations"`
	Receipt      struct {
		IssuedAt string `json:"issued_at"`
	} `json:"receipt"`
	Claim struct {
		ProviderID    string `json:"provider_id"`
		DeclarationID string `json:"declaration_id"`
	} `json:"settlement_claim"`
}

func loadExample(t *testing.T) settlementExample {
	t.Helper()
	data, err := os.ReadFile("testdata/settlement.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var example settlementExample
	if err := json.Unmarshal(data, &example); err != nil {
		t.Fatal(err)
	}
	return example
}

func providerKey(t *testing.T) keys.PrivateKey {
	t.Helper()
	seed := []byte{32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63}
	key, err := keys.NewKeyFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func protocolTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := wire.ParseTime(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func ownDeclarations(t *testing.T) (wire.RetentionDeclaration, wire.RetentionDeclaration) {
	t.Helper()
	key := providerKey(t)
	payout := wire.PayoutRecipients{BitcoinMainnet: "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4"}
	earlier, err := Declare(key, 315360000, payout, protocolTime(t, "2026-12-01T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	later, err := Declare(key, 315360000, payout, protocolTime(t, "2027-01-20T00:00:00Z"))
	if err != nil {
		t.Fatal(err)
	}
	return earlier, later
}

func TestDeclareMatchesCurrentExample(t *testing.T) {
	t.Run("provider declaration matches corrected vector", func(t *testing.T) {
		key := providerKey(t)
		if got := keys.DIDOf(key); got != providerID {
			t.Fatalf("provider DID = %q; want %q", got, providerID)
		}
		declaration, err := Declare(key, 315360000, wire.PayoutRecipients{BitcoinMainnet: "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4"}, protocolTime(t, "2026-12-01T00:00:00Z"))
		if err != nil {
			t.Fatal(err)
		}
		if declaration.DeclarationID != "fyrd2:16cadc2610d20c84575d03a409c29beee0acf70fb6e963800814340d3acb17d1" {
			t.Errorf("declaration ID = %q", declaration.DeclarationID)
		}
		if declaration.ProviderSignature != "cXISY1ScrLHaEccQpXRkETP60e9MW7_8rxKDxmL5PuIENnbZ4Ax1YkG5Y0TPn9SJJOqp0dJHZR5_N1aVRArpAA" {
			t.Errorf("provider signature = %q", declaration.ProviderSignature)
		}
	})
	t.Run("both example declarations verify", func(t *testing.T) {
		example := loadExample(t)
		if err := Verify(example.Declarations[0]); err != nil {
			t.Errorf("Verify(first declaration) error = %v", err)
		}
		if err := Verify(example.Declarations[1]); err != nil {
			t.Errorf("Verify(second declaration) error = %v", err)
		}
	})
}

func TestVerifyRejectsTampering(t *testing.T) {
	t.Run("changed retention seconds fail", func(t *testing.T) {
		declaration := loadExample(t).Declarations[0]
		declaration.RetentionSeconds++
		if err := Verify(declaration); err == nil {
			t.Fatal("Verify accepted a changed retention period")
		}
	})
}

func TestDeclareRejectsBelowMinimum(t *testing.T) {
	t.Run("one second below minimum", func(t *testing.T) {
		_, err := Declare(providerKey(t), 31535999, wire.PayoutRecipients{}, protocolTime(t, "2026-12-01T00:00:00Z"))
		if !errors.Is(err, ErrBelowMinimum) {
			t.Fatalf("Declare error = %v; want ErrBelowMinimum", err)
		}
	})
}

func TestRetainUntilUsesClaimDeclaration(t *testing.T) {
	t.Run("example receipt is retained for named declaration", func(t *testing.T) {
		example := loadExample(t)
		issuedAt := protocolTime(t, example.Receipt.IssuedAt)
		got, err := RetainUntil(issuedAt, example.Claim.ProviderID, example.Claim.DeclarationID, example.Declarations)
		if err != nil {
			t.Fatal(err)
		}
		if want := protocolTime(t, "2037-01-02T00:00:00Z"); !got.Equal(want) {
			t.Errorf("RetainUntil() = %s; want %s", got, want)
		}
	})
	t.Run("another provider declaration cannot be borrowed", func(t *testing.T) {
		example := loadExample(t)
		_, err := RetainUntil(protocolTime(t, example.Receipt.IssuedAt), example.Claim.ProviderID, example.Declarations[1].DeclarationID, example.Declarations)
		if !errors.Is(err, ErrProviderMismatch) {
			t.Fatalf("RetainUntil error = %v; want ErrProviderMismatch", err)
		}
	})
	t.Run("unknown declaration is reported", func(t *testing.T) {
		example := loadExample(t)
		_, err := RetainUntil(protocolTime(t, example.Receipt.IssuedAt), example.Claim.ProviderID, "fyrd2:"+strings.Repeat("0", 64), example.Declarations)
		if !errors.Is(err, ErrUnknownDeclaration) {
			t.Fatalf("RetainUntil error = %v; want ErrUnknownDeclaration", err)
		}
	})
	t.Run("schema maximum seconds do not overflow duration", func(t *testing.T) {
		declaration, err := Declare(providerKey(t), 9007199254740991, wire.PayoutRecipients{BitcoinMainnet: "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4"}, protocolTime(t, "2026-12-01T00:00:00Z"))
		if err != nil {
			t.Fatal(err)
		}
		issuedAt := time.Unix(1799107200, 0).UTC()
		got, err := RetainUntil(issuedAt, providerID, declaration.DeclarationID, []wire.RetentionDeclaration{declaration})
		if err != nil {
			t.Fatal(err)
		}
		if want := int64(1799107200 + 9007199254740991); got.Unix() != want {
			t.Errorf("RetainUntil Unix() = %d; want %d", got.Unix(), want)
		}
	})
}

func TestForClaim(t *testing.T) {
	t.Run("sequence one uses newest declaration before period start", func(t *testing.T) {
		example := loadExample(t)
		earlier, later := ownDeclarations(t)
		got, err := ForClaim([]wire.RetentionDeclaration{earlier, later, example.Declarations[1]}, providerID, protocolTime(t, "2027-01-05T00:00:00Z"), nil)
		if err != nil {
			t.Fatal(err)
		}
		if got.DeclarationID != "fyrd2:16cadc2610d20c84575d03a409c29beee0acf70fb6e963800814340d3acb17d1" {
			t.Errorf("ForClaim declaration ID = %q; want the 2026-12-01 declaration", got.DeclarationID)
		}
	})
	t.Run("same-key previous declaration is reused when no new declaration fits", func(t *testing.T) {
		earlier, later := ownDeclarations(t)
		previous := &Link{DeclarationID: earlier.DeclarationID, ProviderID: providerID, PeriodEnd: protocolTime(t, "2027-01-21T00:00:00Z")}
		got, err := ForClaim([]wire.RetentionDeclaration{earlier, later}, providerID, protocolTime(t, "2027-01-22T00:00:00Z"), previous)
		if err != nil {
			t.Fatal(err)
		}
		if got.DeclarationID != earlier.DeclarationID {
			t.Errorf("ForClaim declaration ID = %q; want previous %q", got.DeclarationID, earlier.DeclarationID)
		}
	})
	t.Run("new declaration at period start fits previous claim window", func(t *testing.T) {
		previousDeclaration, newDeclaration := ownDeclarations(t)
		previous := &Link{DeclarationID: previousDeclaration.DeclarationID, ProviderID: providerID, PeriodEnd: protocolTime(t, "2027-01-19T00:00:00Z")}
		got, err := ForClaim([]wire.RetentionDeclaration{previousDeclaration, newDeclaration}, providerID, protocolTime(t, "2027-01-20T00:00:00Z"), previous)
		if err != nil {
			t.Fatal(err)
		}
		if got.DeclarationID != newDeclaration.DeclarationID {
			t.Errorf("ForClaim declaration ID = %q; want %q", got.DeclarationID, newDeclaration.DeclarationID)
		}
	})
	t.Run("key succession cannot reuse prior key declaration", func(t *testing.T) {
		key := providerKey(t)
		tooLate, err := Declare(key, 315360000, wire.PayoutRecipients{BitcoinMainnet: "bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4"}, protocolTime(t, "2027-01-20T00:00:00Z"))
		if err != nil {
			t.Fatal(err)
		}
		previous := &Link{DeclarationID: tooLate.DeclarationID, ProviderID: providerBID, PeriodEnd: protocolTime(t, "2027-01-19T00:00:00Z")}
		_, err = ForClaim([]wire.RetentionDeclaration{tooLate}, providerID, protocolTime(t, "2027-01-19T00:00:00Z"), previous)
		if !errors.Is(err, ErrNoFittingDeclaration) {
			t.Fatalf("ForClaim error = %v; want ErrNoFittingDeclaration", err)
		}
	})
}

func TestNewestFirst(t *testing.T) {
	t.Run("same effective time sorts by declaration ID", func(t *testing.T) {
		example := loadExample(t)
		got := NewestFirst(example.Declarations)
		if got[0].DeclarationID != "fyrd2:16cadc2610d20c84575d03a409c29beee0acf70fb6e963800814340d3acb17d1" {
			t.Errorf("first declaration ID = %q", got[0].DeclarationID)
		}
		if got[1].DeclarationID != "fyrd2:a590de1916dcc224cb120b8713b961131d2414a7d2387432bd420242cacb989a" {
			t.Errorf("second declaration ID = %q", got[1].DeclarationID)
		}
	})
}

func TestExpired(t *testing.T) {
	t.Run("equal instant has expired", func(t *testing.T) {
		retainUntil := protocolTime(t, "2037-01-02T00:00:00Z")
		if !Expired(retainUntil, retainUntil) {
			t.Fatal("Expired() = false at retain_until")
		}
	})
	t.Run("one second before has not expired", func(t *testing.T) {
		retainUntil := protocolTime(t, "2037-01-02T00:00:00Z")
		if Expired(retainUntil, retainUntil.Add(-time.Second)) {
			t.Fatal("Expired() = true before retain_until")
		}
	})
}
