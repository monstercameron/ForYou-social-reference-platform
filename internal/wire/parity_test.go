package wire

import (
	"reflect"
	"testing"

	"foryou.local/foryou/internal/apierr"
)

func TestParityTypes(t *testing.T) {
	t.Run("launch types match", func(t *testing.T) {
		if mismatches := paritySchemaMismatches("launch"); len(mismatches) != 0 {
			t.Errorf("parity mismatches: %v", mismatches)
		}
	})
	t.Run("operations types match", func(t *testing.T) {
		if mismatches := paritySchemaMismatches("operations"); len(mismatches) != 0 {
			t.Errorf("parity mismatches: %v", mismatches)
		}
	})
	t.Run("every object def is registered", func(t *testing.T) {
		if mismatches := parityRegistryMismatches(); len(mismatches) != 0 {
			t.Errorf("registry mismatches: %v", mismatches)
		}
	})
	t.Run("apierr.PublicReadError matches", func(t *testing.T) {
		got := parityMismatches("PublicReadError", reflect.TypeOf(apierr.PublicReadError{}))
		if len(got) != 0 {
			t.Errorf("PublicReadError mismatches = %v", got)
		}
	})
	t.Run("detects drift", func(t *testing.T) {
		type parityDriftSettlementHold struct {
			WitnessID        string   `json:"witness_id"`
			ProviderID       string   `json:"provider_id"`
			Network          string   `json:"network"`
			Sequence         int64    `json:"sequence"`
			ClaimIDs         []string `json:"claim_ids"`
			WitnessSignature string   `json:"witness_signature,omitempty"`
		}
		got := parityMismatches("SettlementHold", reflect.TypeOf(parityDriftSettlementHold{}))
		want := []string{
			"SettlementHold.held_at: missing Go field",
			"SettlementHold.witness_signature: field WitnessSignature omitempty disagrees with required",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("parityMismatches() = %v; want %v", got, want)
		}
	})
}
