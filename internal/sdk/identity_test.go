package sdk

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/succession"
	"foryou.local/foryou/internal/wire"
)

func asSuccessionFixture(t *testing.T) wire.KeySuccession {
	t.Helper()
	data, err := os.ReadFile("testdata/as/settlement.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Succession wire.KeySuccession `json:"key_succession"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	return f.Succession
}

func TestResolveAuthorSuccession(t *testing.T) {
	t.Run("ignores unrelated entries", func(t *testing.T) {
		got := ResolveAuthorSuccession("did:key:unknown", nil)
		if got.Resolution != succession.NoSuccession || got.NewID != "" || got.AssertedByOld {
			t.Fatalf("view = %#v", got)
		}
	})
	t.Run("example succession links with old key assertion", func(t *testing.T) {
		s := asSuccessionFixture(t)
		got := ResolveAuthorSuccession(s.OldID, []wire.KeySuccession{s})
		if got.Resolution != succession.Linked || got.NewID != s.NewID || !got.AssertedByOld || len(got.Valid) != 1 {
			t.Fatalf("view = %#v", got)
		}
	})
	t.Run("valid conflicting successor remains unresolved", func(t *testing.T) {
		s := asSuccessionFixture(t)
		oldSeed := []byte{32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63}
		newSeed := []byte{160, 161, 162, 163, 164, 165, 166, 167, 168, 169, 170, 171, 172, 173, 174, 175, 176, 177, 178, 179, 180, 181, 182, 183, 184, 185, 186, 187, 188, 189, 190, 191}
		oldKey, err := keys.NewKeyFromSeed(oldSeed)
		if err != nil {
			t.Fatal(err)
		}
		newKey, err := keys.NewKeyFromSeed(newSeed)
		if err != nil {
			t.Fatal(err)
		}
		conflict, err := succession.New(oldKey, newKey, time.Date(2027, 7, 1, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		got := ResolveAuthorSuccession(s.OldID, []wire.KeySuccession{s, conflict})
		if got.Resolution != succession.Unresolved || got.NewID != "" || got.AssertedByOld {
			t.Fatalf("view = %#v", got)
		}
	})
	t.Run("invalid signature is reported separately", func(t *testing.T) {
		s := asSuccessionFixture(t)
		s.NewSignature = "broken"
		got := ResolveAuthorSuccession(s.OldID, []wire.KeySuccession{s})
		if len(got.Invalid) != 1 || len(got.Valid) != 0 || got.Resolution != succession.NoSuccession {
			t.Fatalf("view = %#v", got)
		}
	})
}
