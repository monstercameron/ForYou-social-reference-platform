package governance

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"foryou.local/foryou/internal/keys"
)

// testdata/settlement.example.json is copied from examples/settlement.example.json of the ForYou specification, release 1.0.
func governanceExample(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile("testdata/settlement.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var example map[string]json.RawMessage
	if err := json.Unmarshal(raw, &example); err != nil {
		t.Fatal(err)
	}
	return example
}

func decodeExample[T any](t *testing.T, example map[string]json.RawMessage, key string) T {
	t.Helper()
	var result T
	if err := json.Unmarshal(example[key], &result); err != nil {
		t.Fatalf("decode %s: %v", key, err)
	}
	return result
}

func syntheticReleaseKeys(t *testing.T) []ed25519.PrivateKey {
	t.Helper()
	seed0 := sha256.Sum256([]byte("synthetic release key 0"))
	seed1 := sha256.Sum256([]byte("synthetic release key 1"))
	seed2 := sha256.Sum256([]byte("synthetic release key 2"))
	seed3 := sha256.Sum256([]byte("synthetic release key 3"))
	seed4 := sha256.Sum256([]byte("synthetic release key 4"))
	return []ed25519.PrivateKey{
		ed25519.NewKeyFromSeed(seed0[:]),
		ed25519.NewKeyFromSeed(seed1[:]),
		ed25519.NewKeyFromSeed(seed2[:]),
		ed25519.NewKeyFromSeed(seed3[:]),
		ed25519.NewKeyFromSeed(seed4[:]),
	}
}

func syntheticFoundationKey() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed([]byte{128, 129, 130, 131, 132, 133, 134, 135, 136, 137, 138, 139, 140, 141, 142, 143, 144, 145, 146, 147, 148, 149, 150, 151, 152, 153, 154, 155, 156, 157, 158, 159})
}

func syntheticProviderKey() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed([]byte{32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63})
}

func syntheticProviderBKey() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed([]byte{160, 161, 162, 163, 164, 165, 166, 167, 168, 169, 170, 171, 172, 173, 174, 175, 176, 177, 178, 179, 180, 181, 182, 183, 184, 185, 186, 187, 188, 189, 190, 191})
}

func syntheticProviderNextKey() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed([]byte{192, 193, 194, 195, 196, 197, 198, 199, 200, 201, 202, 203, 204, 205, 206, 207, 208, 209, 210, 211, 212, 213, 214, 215, 216, 217, 218, 219, 220, 221, 222, 223})
}

func syntheticMonitorKeys() []ed25519.PrivateKey {
	seed0 := sha256.Sum256([]byte("synthetic monitor 0"))
	seed1 := sha256.Sum256([]byte("synthetic monitor 1"))
	seed2 := sha256.Sum256([]byte("synthetic monitor 2"))
	seed3 := sha256.Sum256([]byte("synthetic monitor 3"))
	seed4 := sha256.Sum256([]byte("synthetic monitor 4"))
	return []ed25519.PrivateKey{
		ed25519.NewKeyFromSeed(seed0[:]),
		ed25519.NewKeyFromSeed(seed1[:]),
		ed25519.NewKeyFromSeed(seed2[:]),
		ed25519.NewKeyFromSeed(seed3[:]),
		ed25519.NewKeyFromSeed(seed4[:]),
	}
}
func TestThresholdSignatures(t *testing.T) {
	privateKeys := syntheticReleaseKeys(t)
	set := KeySet{IDs: []string{keys.DIDOf(privateKeys[0]), keys.DIDOf(privateKeys[1]), keys.DIDOf(privateKeys[2]), keys.DIDOf(privateKeys[3]), keys.DIDOf(privateKeys[4])}, Threshold: 4}
	unsigned := map[string]any{"approved": true}
	t.Run("four signatures meet threshold", func(t *testing.T) {
		signatures, err := SignRelease("distribution-policy", unsigned, privateKeys[:4])
		if err != nil {
			t.Fatal(err)
		}
		if err := VerifyThreshold("distribution-policy", unsigned, signatures, set); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("three signatures do not meet threshold", func(t *testing.T) {
		signatures, err := SignRelease("distribution-policy", unsigned, privateKeys[:3])
		if err != nil {
			t.Fatal(err)
		}
		if err := VerifyThreshold("distribution-policy", unsigned, signatures, set); !errors.Is(err, ErrInvalid) {
			t.Fatalf("VerifyThreshold error = %v; want ErrInvalid", err)
		}
	})
	t.Run("failing in-set signature rejects even above threshold", func(t *testing.T) {
		signatures, err := SignRelease("distribution-policy", unsigned, privateKeys)
		if err != nil {
			t.Fatal(err)
		}
		signatures[2].Signature = signatures[3].Signature
		if err := VerifyThreshold("distribution-policy", unsigned, signatures, set); !errors.Is(err, ErrInvalid) {
			t.Fatalf("VerifyThreshold error = %v; want ErrInvalid", err)
		}
	})
	t.Run("out-of-set signature ignored", func(t *testing.T) {
		signatures, err := SignRelease("distribution-policy", unsigned, []ed25519.PrivateKey{privateKeys[0], privateKeys[1], privateKeys[2], privateKeys[3], syntheticMonitorKeys()[0]})
		if err != nil {
			t.Fatal(err)
		}
		if err := VerifyThreshold("distribution-policy", unsigned, signatures, set); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("repeated signer rejected", func(t *testing.T) {
		signatures, err := SignRelease("distribution-policy", unsigned, privateKeys[:4])
		if err != nil {
			t.Fatal(err)
		}
		signatures[1] = signatures[0]
		if err := VerifyThreshold("distribution-policy", unsigned, signatures, set); !errors.Is(err, ErrInvalid) {
			t.Fatalf("VerifyThreshold error = %v; want ErrInvalid", err)
		}
	})
}
