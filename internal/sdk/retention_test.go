package sdk

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"foryou.local/foryou/internal/retention"
	"foryou.local/foryou/internal/wire"
)

func TestRetainUntil(t *testing.T) {
	t.Run("example uses declaration named by claim", func(t *testing.T) {
		data, err := os.ReadFile("testdata/as/settlement.example.json")
		if err != nil {
			t.Fatal(err)
		}
		var f struct {
			Declarations []wire.RetentionDeclaration `json:"retention_declarations"`
			Receipt      wire.SubmissionReceipt      `json:"receipt"`
			Claim        wire.SettlementClaim        `json:"settlement_claim"`
		}
		if err := json.Unmarshal(data, &f); err != nil {
			t.Fatal(err)
		}
		got, err := RetainUntil(f.Receipt, f.Claim, f.Declarations)
		if err != nil {
			t.Fatal(err)
		}
		if got.DeclarationID != "fyrd2:16cadc2610d20c84575d03a409c29beee0acf70fb6e963800814340d3acb17d1" || got.RetentionSeconds != 315360000 || got.RetainUntil.Format(time.RFC3339) != "2037-01-02T00:00:00Z" {
			t.Fatalf("retention view = %#v", got)
		}
	})
	t.Run("missing declaration errors", func(t *testing.T) {
		data, err := os.ReadFile("testdata/as/settlement.example.json")
		if err != nil {
			t.Fatal(err)
		}
		var f struct {
			Receipt wire.SubmissionReceipt `json:"receipt"`
			Claim   wire.SettlementClaim   `json:"settlement_claim"`
		}
		if err := json.Unmarshal(data, &f); err != nil {
			t.Fatal(err)
		}
		f.Claim.DeclarationID = "fyrd2:missing"
		_, err = RetainUntil(f.Receipt, f.Claim, nil)
		if !errors.Is(err, retention.ErrUnknownDeclaration) {
			t.Fatalf("RetainUntil error = %v; want ErrUnknownDeclaration", err)
		}
	})
	t.Run("different provider declaration is rejected", func(t *testing.T) {
		data, err := os.ReadFile("testdata/as/settlement.example.json")
		if err != nil {
			t.Fatal(err)
		}
		var f struct {
			Declarations []wire.RetentionDeclaration `json:"retention_declarations"`
			Receipt      wire.SubmissionReceipt      `json:"receipt"`
			Claim        wire.SettlementClaim        `json:"settlement_claim"`
		}
		if err := json.Unmarshal(data, &f); err != nil {
			t.Fatal(err)
		}
		f.Claim.DeclarationID = f.Declarations[1].DeclarationID
		_, err = RetainUntil(f.Receipt, f.Claim, f.Declarations)
		if !errors.Is(err, retention.ErrProviderMismatch) {
			t.Fatalf("RetainUntil error = %v; want ErrProviderMismatch", err)
		}
	})
}
