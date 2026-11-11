package sdk

import (
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

func asProviderKey(t *testing.T) keys.PrivateKey {
	t.Helper()
	key, err := keys.NewKeyFromSeed([]byte{32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63})
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func asSignedManifest(t *testing.T, key keys.PrivateKey, version int64, expires, digest string) (wire.OperatorManifest, []byte) {
	t.Helper()
	m := wire.OperatorManifest{OperatorID: keys.DIDOf(key), ManifestVersion: version, IssuedAt: "2026-01-01T00:00:00Z", ExpiresAt: expires, ServiceRoles: []string{"storage"}, ServiceEndpoints: []string{"https://operator.example"}, SupportedProfiles: []string{"ForYouExportSet-1"}, PolicyURL: "https://operator.example/policy", PolicyDigest: digest, LegalContact: "legal@example.org", AbuseContact: "abuse@example.org", PrivacyContact: "privacy@example.org", AppealURL: "https://operator.example/appeal", MarketsAssessed: []string{}}
	body, err := canon.EncodeWithout(m, "signature")
	if err != nil {
		t.Fatal(err)
	}
	m.Signature, err = keys.Sign(key, "operator-manifest", body)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := canon.Encode(m)
	if err != nil {
		t.Fatal(err)
	}
	return m, raw
}

func TestManifestPins(t *testing.T) {
	t.Run("pins monotonic versions and marks equivocation", func(t *testing.T) {
		key := asProviderKey(t)
		var err error
		now := time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)
		pins := NewManifestPins()
		v2, raw2 := asSignedManifest(t, key, 2, "2026-01-31T00:00:00Z", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		if got := pins.Check(v2.OperatorID, raw2, v2, now); got != ManifestCurrent {
			t.Fatalf("Check(v2) = %v", got)
		}
		v1, raw1 := asSignedManifest(t, key, 1, "2026-01-31T00:00:00Z", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
		if got := pins.Check(v1.OperatorID, raw1, v1, now); got != ManifestRollback {
			t.Fatalf("Check(v1) = %v", got)
		}
		fork, forkRaw := asSignedManifest(t, key, 2, "2026-01-31T00:00:00Z", "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc")
		if got := pins.Check(fork.OperatorID, forkRaw, fork, now); got != ManifestEquivocation {
			t.Fatalf("Check(fork) = %v", got)
		}
		snapshot, err := pins.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		restored, err := RestoreManifestPins(snapshot)
		if err != nil {
			t.Fatal(err)
		}
		if version, _, ok := restored.Pinned(v2.OperatorID); !ok || version != 2 {
			t.Fatalf("restored pin = %d, %v; want version 2", version, ok)
		}
		v3, raw3 := asSignedManifest(t, key, 3, "2026-01-31T00:00:00Z", "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd")
		if got := restored.Check(v3.OperatorID, raw3, v3, now); got != ManifestEquivocation {
			t.Fatalf("Check(v3) = %v", got)
		}
		restored.Reset(v3.OperatorID)
		if got := restored.Check(v3.OperatorID, raw3, v3, now); got != ManifestCurrent {
			t.Fatalf("Check(v3 after reset) = %v", got)
		}
	})
	t.Run("another signing key is rejected", func(t *testing.T) {
		key := asProviderKey(t)
		m, raw := asSignedManifest(t, key, 1, "2026-01-31T00:00:00Z", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		other, err := keys.NewKeyFromSeed([]byte{160, 161, 162, 163, 164, 165, 166, 167, 168, 169, 170, 171, 172, 173, 174, 175, 176, 177, 178, 179, 180, 181, 182, 183, 184, 185, 186, 187, 188, 189, 190, 191})
		if err != nil {
			t.Fatal(err)
		}
		m.Signature, err = keys.Sign(other, "operator-manifest", asManifestBody(t, m))
		if err != nil {
			t.Fatal(err)
		}
		raw, err = canon.Encode(m)
		if err != nil {
			t.Fatal(err)
		}
		if got := NewManifestPins().Check(m.OperatorID, raw, m, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)); got != ManifestBadSignature {
			t.Fatalf("Check() = %v; want ManifestBadSignature", got)
		}
	})
	t.Run("manifest value must match signed bytes", func(t *testing.T) {
		m, raw := asSignedManifest(t, asProviderKey(t), 1, "2026-01-31T00:00:00Z", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		m.PolicyURL = "https://changed.example/policy"
		pins := NewManifestPins()
		if got := pins.Check(m.OperatorID, raw, m, time.Date(2026, 1, 2, 0, 0, 0, 0, time.UTC)); got != ManifestBadSignature {
			t.Fatalf("Check() = %v; want ManifestBadSignature", got)
		}
		if _, _, ok := pins.Pinned(m.OperatorID); ok {
			t.Fatal("mismatched manifest was pinned")
		}
	})
	t.Run("31 day validity period is expired", func(t *testing.T) {
		key := asProviderKey(t)
		m, raw := asSignedManifest(t, key, 1, "2026-02-01T00:00:00Z", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
		if got := NewManifestPins().Check(m.OperatorID, raw, m, time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC)); got != ManifestExpired {
			t.Fatalf("Check() = %v", got)
		}
	})
}

func asManifestBody(t *testing.T, m wire.OperatorManifest) []byte {
	t.Helper()
	body, err := canon.EncodeWithout(m, "signature")
	if err != nil {
		t.Fatal(err)
	}
	return body
}
