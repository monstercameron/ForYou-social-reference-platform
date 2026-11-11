package succession

import (
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

const (
	authorID   = "did:key:z6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd"
	providerID = "did:key:z6MkhFwXNFWosLeugvSf4wcL9t3uuRXueGSFTRgSvHhWj5G2"
	delegateID = "did:key:z6Mkgxj2R3HLtQRpPnvfvpuKEceSqf3tZHBjdmZ3fFz3JHGG"
)

func fixture(t *testing.T) wire.KeySuccession {
	t.Helper()
	data, err := os.ReadFile("testdata/succession-vector.json")
	if err != nil {
		t.Fatal(err)
	}
	var s wire.KeySuccession
	if err := json.Unmarshal(data, &s); err != nil {
		t.Fatal(err)
	}
	return s
}

func key(t *testing.T, start byte) keys.PrivateKey {
	t.Helper()
	var seed []byte
	switch start {
	case 0:
		seed = []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31}
	case 32:
		seed = []byte{32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63}
	case 64:
		seed = []byte{64, 65, 66, 67, 68, 69, 70, 71, 72, 73, 74, 75, 76, 77, 78, 79, 80, 81, 82, 83, 84, 85, 86, 87, 88, 89, 90, 91, 92, 93, 94, 95}
	default:
		t.Fatalf("unknown test seed start %d", start)
	}
	priv, err := keys.NewKeyFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	return priv
}

func succession(t *testing.T, oldKey, newKey keys.PrivateKey, date string) wire.KeySuccession {
	t.Helper()
	effectiveAt, err := wire.ParseTime(date)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(oldKey, newKey, effectiveAt)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestID(t *testing.T) {
	s := fixture(t)
	effectiveAt, _ := wire.ParseTime(s.EffectiveAt)
	t.Run("vector", func(t *testing.T) {
		got, err := ID(s.OldID, s.NewID, effectiveAt)
		if err != nil || got != s.SuccessionID {
			t.Fatalf("ID() = %q, %v; want %q", got, err, s.SuccessionID)
		}
	})
}

func TestNewMatchesSpecificationVector(t *testing.T) {
	s := fixture(t)
	effectiveAt, _ := wire.ParseTime(s.EffectiveAt)
	got, err := New(key(t, 0), key(t, 64), effectiveAt)
	if err != nil || got != s {
		t.Fatalf("New() = %#v, %v; want %#v", got, err, s)
	}
}

func TestVerify(t *testing.T) {
	valid := fixture(t)
	t.Run("specification vector", func(t *testing.T) {
		if err := Verify(valid); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("identical keys", func(t *testing.T) {
		s := valid
		s.NewID = s.OldID
		if err := Verify(s); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Verify() error = %v; want ErrInvalid", err)
		}
	})
	t.Run("noncanonical DID", func(t *testing.T) {
		s := valid
		s.OldID = "did:key:z" + "1" + s.OldID[len("did:key:z"):]
		if err := Verify(s); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Verify() error = %v; want ErrInvalid", err)
		}
	})
	t.Run("tampered time", func(t *testing.T) {
		s := valid
		s.EffectiveAt = "2026-12-02T00:00:00Z"
		if err := Verify(s); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Verify() error = %v; want ErrInvalid", err)
		}
	})
	t.Run("altered ID", func(t *testing.T) {
		s := valid
		s.SuccessionID = "fyks2:b3156461bf70d3de6fa794f8f1d1cb6a0c7ac71674aa717c26004612b0c808b6"
		if err := Verify(s); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Verify() error = %v; want ErrInvalid", err)
		}
	})
	t.Run("swapped signatures", func(t *testing.T) {
		s := valid
		s.OldSignature, s.NewSignature = s.NewSignature, s.OldSignature
		if err := Verify(s); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Verify() error = %v; want ErrInvalid", err)
		}
	})
	t.Run("wrong acceptance domain", func(t *testing.T) {
		newKey := key(t, 64)
		s := succession(t, key(t, 0), newKey, valid.EffectiveAt)
		encoded, err := canon.Encode(successionCore{OldID: s.OldID, NewID: s.NewID, EffectiveAt: s.EffectiveAt})
		if err != nil {
			t.Fatal(err)
		}
		s.NewSignature, err = keys.Sign(newKey, "key-succession", encoded)
		if err != nil {
			t.Fatal(err)
		}
		if err := Verify(s); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Verify() error = %v; want ErrInvalid", err)
		}
	})
	t.Run("missing field", func(t *testing.T) {
		s := valid
		s.NewSignature = ""
		if err := Verify(s); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Verify() error = %v; want ErrInvalid", err)
		}
	})
}

func TestSignerAt(t *testing.T) {
	s := fixture(t)
	effectiveAt, _ := wire.ParseTime(s.EffectiveAt)
	t.Run("one second before", func(t *testing.T) {
		if got := SignerAt(s, effectiveAt.Add(-time.Second)); got != authorID {
			t.Fatalf("SignerAt() = %q; want %q", got, authorID)
		}
	})
	t.Run("at effective time", func(t *testing.T) {
		if got := SignerAt(s, effectiveAt); got != delegateID {
			t.Fatalf("SignerAt() = %q; want %q", got, delegateID)
		}
	})
}

func TestResolveAuthor(t *testing.T) {
	first := fixture(t)
	second := succession(t, key(t, 0), key(t, 32), "2027-01-01T00:00:00Z")
	t.Run("none", func(t *testing.T) {
		if got, resolution := ResolveAuthor(authorID, nil); got != "" || resolution != NoSuccession {
			t.Fatalf("ResolveAuthor() = %q, %v; want empty, NoSuccession", got, resolution)
		}
	})
	t.Run("one link", func(t *testing.T) {
		if got, resolution := ResolveAuthor(authorID, []wire.KeySuccession{first}); got != delegateID || resolution != Linked {
			t.Fatalf("ResolveAuthor() = %q, %v; want delegate, Linked", got, resolution)
		}
	})
	t.Run("same pair at two dates", func(t *testing.T) {
		later := succession(t, key(t, 0), key(t, 64), "2027-01-01T00:00:00Z")
		if got, resolution := ResolveAuthor(authorID, []wire.KeySuccession{first, later}); got != delegateID || resolution != Linked {
			t.Fatalf("ResolveAuthor() = %q, %v; want delegate, Linked", got, resolution)
		}
	})
	t.Run("conflicting successors", func(t *testing.T) {
		if got, resolution := ResolveAuthor(authorID, []wire.KeySuccession{first, second}); got != "" || resolution != Unresolved {
			t.Fatalf("ResolveAuthor() = %q, %v; want empty, Unresolved", got, resolution)
		}
	})
	t.Run("invalid entry is ignored", func(t *testing.T) {
		invalid := first
		invalid.NewSignature = "bad"
		if got, resolution := ResolveAuthor(authorID, []wire.KeySuccession{invalid, first}); got != delegateID || resolution != Linked {
			t.Fatalf("ResolveAuthor() = %q, %v; want delegate, Linked", got, resolution)
		}
	})
}

func TestProviderLine(t *testing.T) {
	first := succession(t, key(t, 32), key(t, 0), "2026-12-01T00:00:00Z")
	secondDate := succession(t, key(t, 32), key(t, 0), "2027-01-01T00:00:00Z")
	conflicting := succession(t, key(t, 32), key(t, 64), "2027-02-01T00:00:00Z")
	t.Run("allows pair with separate dates and later rotation", func(t *testing.T) {
		line := NewProviderLine()
		noClaims := func(string) bool { return false }
		if err := line.Accept(first, noClaims); err != nil {
			t.Fatal(err)
		}
		if err := line.Accept(secondDate, noClaims); err != nil {
			t.Fatal(err)
		}
		later := succession(t, key(t, 0), key(t, 64), "2027-03-01T00:00:00Z")
		if err := line.Accept(later, noClaims); err != nil {
			t.Fatal(err)
		}
		if got := line.Line(providerID); len(got) != 3 || got[0] != providerID || got[1] != authorID || got[2] != delegateID {
			t.Fatalf("Line() = %v; want provider, author, delegate", got)
		}
		if got, ok := line.ByID(secondDate.SuccessionID); !ok || got != secondDate {
			t.Fatalf("ByID() = %#v, %t; want second dated succession", got, ok)
		}
		if got, ok := line.ByID(first.SuccessionID); !ok || got != first {
			t.Fatalf("ByID() = %#v, %t; want first dated succession", got, ok)
		}
		if got, ok := line.Successor(providerID); !ok || got != authorID {
			t.Fatalf("Successor() = %q, %t; want author, true", got, ok)
		}
	})
	t.Run("rejects different successor for old key", func(t *testing.T) {
		line := NewProviderLine()
		if err := line.Accept(first, func(string) bool { return false }); err != nil {
			t.Fatal(err)
		}
		if err := line.Accept(conflicting, func(string) bool { return false }); !errors.Is(err, ErrConflict) {
			t.Fatalf("Accept() error = %v; want ErrConflict", err)
		}
	})
	t.Run("rejects key already in another succession", func(t *testing.T) {
		line := NewProviderLine()
		other := succession(t, key(t, 32), key(t, 0), "2026-12-01T00:00:00Z")
		if err := line.Accept(other, func(string) bool { return false }); err != nil {
			t.Fatal(err)
		}
		candidate := succession(t, key(t, 64), key(t, 0), "2027-01-01T00:00:00Z")
		if err := line.Accept(candidate, func(string) bool { return false }); !errors.Is(err, ErrConflict) {
			t.Fatalf("Accept() error = %v; want ErrConflict", err)
		}
	})
	t.Run("rejects missing claim check", func(t *testing.T) {
		if err := NewProviderLine().Accept(first, nil); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Accept() error = %v; want ErrInvalid", err)
		}
	})
	t.Run("rejects successor that signed claims", func(t *testing.T) {
		line := NewProviderLine()
		if err := line.Accept(first, func(did string) bool { return did == authorID }); !errors.Is(err, ErrSuccessorSignedClaims) {
			t.Fatalf("Accept() error = %v; want ErrSuccessorSignedClaims", err)
		}
	})
}

func TestRegistry(t *testing.T) {
	first := fixture(t)
	later := succession(t, key(t, 0), key(t, 64), "2027-01-01T00:00:00Z")
	conflict := succession(t, key(t, 0), key(t, 32), "2026-11-01T00:00:00Z")
	t.Run("stores duplicates and conflicts in order and round-trips", func(t *testing.T) {
		registry := NewRegistry(3)
		if added, err := registry.Add(first); err != nil || !added {
			t.Fatalf("Add() = %t, %v; want true, nil", added, err)
		}
		if added, err := registry.Add(first); err != nil || added {
			t.Fatalf("duplicate Add() = %t, %v; want false, nil", added, err)
		}
		if _, err := registry.Add(later); err != nil {
			t.Fatal(err)
		}
		if _, err := registry.Add(conflict); err != nil {
			t.Fatal(err)
		}
		before := registry.ForOld(authorID)
		if len(before) != 3 || before[0].SuccessionID != conflict.SuccessionID || before[1].SuccessionID != first.SuccessionID || before[2].SuccessionID != later.SuccessionID {
			t.Fatalf("ForOld() = %#v; want time ordered successions", before)
		}
		data, err := registry.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		restored, err := RestoreRegistry(data, 3)
		if err != nil {
			t.Fatal(err)
		}
		after := restored.ForOld(authorID)
		if len(after) != len(before) || after[0] != before[0] || after[1] != before[1] || after[2] != before[2] {
			t.Fatalf("restored ForOld() = %#v; want %#v", after, before)
		}
	})
	t.Run("enforces per-key bound", func(t *testing.T) {
		registry := NewRegistry(1)
		if _, err := registry.Add(first); err != nil {
			t.Fatal(err)
		}
		if _, err := registry.Add(later); !errors.Is(err, ErrRegistryFull) {
			t.Fatalf("Add() error = %v; want ErrRegistryFull", err)
		}
	})
	t.Run("restore rejects tampered signature", func(t *testing.T) {
		registry := NewRegistry(1)
		if _, err := registry.Add(first); err != nil {
			t.Fatal(err)
		}
		data, err := registry.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		var entries []map[string]any
		if err := json.Unmarshal(data, &entries); err != nil {
			t.Fatal(err)
		}
		entries[0]["new_signature"] = "bad"
		tampered, err := json.Marshal(entries)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := RestoreRegistry(tampered, 1); !errors.Is(err, ErrInvalid) {
			t.Fatalf("RestoreRegistry() error = %v; want ErrInvalid", err)
		}
	})
	t.Run("restore rejects unknown fields", func(t *testing.T) {
		data := []byte(`[{"succession_id":"fyks2:a3156461bf70d3de6fa794f8f1d1cb6a0c7ac71674aa717c26004612b0c808b6","old_id":"did:key:z6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd","new_id":"did:key:z6Mkgxj2R3HLtQRpPnvfvpuKEceSqf3tZHBjdmZ3fFz3JHGG","effective_at":"2026-12-01T00:00:00Z","old_signature":"r_Zxt-fw5D9i8N9UgSaYi1FBeRALA9FbfZQF6MnO2nnX8kf8cBBT0jcu-1LhC4LUCbc46yQIyaEGL5Yqk1ZuDw","new_signature":"0JyWdFLhAHFOQKimHD13oKxTVvbW45tm3YR43wo94KM1FihVlMDGUarOv-3mk0ZLFWVz1qyD3ViRB5yb06S4BQ","extra":true}]`)
		if _, err := RestoreRegistry(data, 1); !errors.Is(err, ErrInvalid) {
			t.Fatalf("RestoreRegistry() error = %v; want ErrInvalid", err)
		}
	})
	t.Run("restore rejects missing field", func(t *testing.T) {
		data := []byte(`[{"succession_id":"fyks2:a3156461bf70d3de6fa794f8f1d1cb6a0c7ac71674aa717c26004612b0c808b6","old_id":"did:key:z6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd","new_id":"did:key:z6Mkgxj2R3HLtQRpPnvfvpuKEceSqf3tZHBjdmZ3fFz3JHGG","effective_at":"2026-12-01T00:00:00Z","old_signature":"r_Zxt-fw5D9i8N9UgSaYi1FBeRALA9FbfZQF6MnO2nnX8kf8cBBT0jcu-1LhC4LUCbc46yQIyaEGL5Yqk1ZuDw"}]`)
		if _, err := RestoreRegistry(data, 1); !errors.Is(err, ErrInvalid) {
			t.Fatalf("RestoreRegistry() error = %v; want ErrInvalid", err)
		}
	})
}
