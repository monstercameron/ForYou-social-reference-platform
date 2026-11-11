package keys

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// The fixtures are byte-for-byte copies of ../ForYou-Social-Ecosystem-Specification/tests/strict-ed25519-vectors.json and security-vectors.json.
type strictVectorFile struct {
	Cases []strictVector `json:"cases"`
}

type strictVector struct {
	Name         string `json:"name"`
	PublicKeyHex string `json:"public_key_hex"`
	MessageHex   string `json:"message_hex"`
	SignatureHex string `json:"signature_hex"`
	Valid        bool   `json:"valid"`
}

type securityVectorFile struct {
	Message struct {
		AuthorID  string `json:"author_id"`
		MessageID string `json:"message_id"`
		Signature string `json:"signature"`
	} `json:"message"`
	IntentPreimageHex string `json:"intent_preimage_hex"`
	AuthorPublicKey   string `json:"author_public_key"`
}

func TestVerifyStrictVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/strict-ed25519-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors strictVectorFile
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	check := func(t *testing.T, name string, want bool, wantErr error) {
		t.Helper()
		var vector *strictVector
		for i := range vectors.Cases {
			if vectors.Cases[i].Name == name {
				vector = &vectors.Cases[i]
				break
			}
		}
		if vector == nil {
			t.Fatalf("strict vector %q not found", name)
		}
		if vector.Valid != want {
			t.Fatalf("strict vector %q valid = %t; want %t", name, vector.Valid, want)
		}
		pub, err := hex.DecodeString(vector.PublicKeyHex)
		if err != nil {
			t.Fatal(err)
		}
		msg, err := hex.DecodeString(vector.MessageHex)
		if err != nil {
			t.Fatal(err)
		}
		sig, err := hex.DecodeString(vector.SignatureHex)
		if err != nil {
			t.Fatal(err)
		}
		verifyErr := VerifyStrict(pub, msg, sig)
		if (verifyErr == nil) != want {
			t.Fatalf("VerifyStrict(%q) error = %v; want success %t", name, verifyErr, want)
		}
		if wantErr != nil && !errors.Is(verifyErr, wantErr) {
			t.Fatalf("VerifyStrict(%q) error = %v; want errors.Is(_, %v)", name, verifyErr, wantErr)
		}
	}
	t.Run("valid", func(t *testing.T) { check(t, "valid", true, nil) })
	t.Run("message_modified", func(t *testing.T) { check(t, "message_modified", false, ErrBadSignature) })
	t.Run("identity_public_key", func(t *testing.T) { check(t, "identity_public_key", false, ErrBadKey) })
	t.Run("identity_R", func(t *testing.T) { check(t, "identity_R", false, ErrBadSignature) })
	t.Run("order_two_public_key", func(t *testing.T) { check(t, "order_two_public_key", false, ErrBadKey) })
	t.Run("order_two_R", func(t *testing.T) { check(t, "order_two_R", false, ErrBadSignature) })
	t.Run("mixed_order_public_key", func(t *testing.T) { check(t, "mixed_order_public_key", false, ErrBadKey) })
	t.Run("mixed_order_R", func(t *testing.T) { check(t, "mixed_order_R", false, ErrBadSignature) })
	t.Run("noncanonical_y_public_key", func(t *testing.T) { check(t, "noncanonical_y_public_key", false, ErrBadKey) })
	t.Run("noncanonical_y_R", func(t *testing.T) { check(t, "noncanonical_y_R", false, ErrBadSignature) })
	t.Run("invalid_x_sign_public_key", func(t *testing.T) { check(t, "invalid_x_sign_public_key", false, ErrBadKey) })
	t.Run("invalid_x_sign_R", func(t *testing.T) { check(t, "invalid_x_sign_R", false, ErrBadSignature) })
	t.Run("scalar_equal_L", func(t *testing.T) { check(t, "scalar_equal_L", false, ErrBadSignature) })
	t.Run("cofactored_only_forgery", func(t *testing.T) { check(t, "cofactored_only_forgery", false, ErrBadKey) })
}

func TestVerifyStrictRejectsWhatStdlibAccepts(t *testing.T) {
	check := func(t *testing.T, name, publicKeyHex, signatureHex string, wantErr error) {
		t.Helper()
		pub, err := hex.DecodeString(publicKeyHex)
		if err != nil {
			t.Fatal(err)
		}
		sig, err := hex.DecodeString(signatureHex)
		if err != nil {
			t.Fatal(err)
		}
		msg, err := hex.DecodeString("466f72596f752073747269637420766563746f72207631")
		if err != nil {
			t.Fatal(err)
		}
		if !ed25519.Verify(ed25519.PublicKey(pub), msg, sig) {
			t.Fatalf("ed25519.Verify(%q) rejected the standard-library acceptance vector", name)
		}
		if err := VerifyStrict(pub, msg, sig); !errors.Is(err, wantErr) {
			t.Fatalf("VerifyStrict(%q) error = %v; want errors.Is(_, %v)", name, err, wantErr)
		}
	}
	t.Run("identity_public_key_accepted_by_stdlib", func(t *testing.T) {
		// Built with A = identity and R = B, S = 1; the standard verifier accepts the equation.
		check(t, "identity_public_key_accepted_by_stdlib", "0100000000000000000000000000000000000000000000000000000000000000", "58666666666666666666666666666666666666666666666666666666666666660100000000000000000000000000000000000000000000000000000000000000", ErrBadKey)
	})
	t.Run("identity_R_accepted_by_stdlib", func(t *testing.T) {
		// Built with A = B and R = identity, with S = k mod L for the fixed message.
		check(t, "identity_R_accepted_by_stdlib", "5866666666666666666666666666666666666666666666666666666666666666", "0100000000000000000000000000000000000000000000000000000000000000ac39a6e06c406b7425d721988d0c2269167ba7d405b195461b294ff1d48ffb0f", ErrBadSignature)
	})
	t.Run("mixed_order_public_key_accepted_by_stdlib", func(t *testing.T) {
		// Built with A = B plus the order-two point and R = [3]B, with even k so the uncofactored equation holds.
		check(t, "mixed_order_public_key_accepted_by_stdlib", "9599999999999999999999999999999999999999999999999999999999999999", "d4b4f5784868c3020403246717ec169ff79e26608ea126a1ab69ee77d1b16712ad4d59dca2b9795ffbc796a4ea4d2c6907e39eed960a5f9ab26312ba4d09f70e", ErrBadKey)
	})
}

func TestDID(t *testing.T) {
	security := readSecurityVector(t)
	pub, err := hex.DecodeString("03a107bff3ce10be1d70dd18e74bc09967e4d6309ba50d5f1ddc8664125531b8")
	if err != nil {
		t.Fatal(err)
	}
	want := "did:key:z6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd"
	if security.Message.AuthorID != want {
		t.Fatalf("security author_id = %q; want %q", security.Message.AuthorID, want)
	}
	fixtureKey, err := DecodeB64URL(security.AuthorPublicKey, ed25519.PublicKeySize)
	if err != nil || !bytes.Equal(fixtureKey, pub) {
		t.Fatalf("security author_public_key = %x, %v; want %x", fixtureKey, err, pub)
	}
	if got := DID(ed25519.PublicKey(pub)); got != want {
		t.Fatalf("DID() = %q; want %q", got, want)
	}
	parsed, err := ParseDID(want)
	if err != nil || !bytes.Equal(parsed, pub) {
		t.Fatalf("ParseDID() = %x, %v; want %x", parsed, err, pub)
	}
	checkBad := func(t *testing.T, did string) {
		t.Helper()
		if _, err := ParseDID(did); !errors.Is(err, ErrBadKey) {
			t.Fatalf("ParseDID(%q) error = %v; want ErrBadKey", did, err)
		}
	}
	t.Run("mixed_order", func(t *testing.T) { checkBad(t, "did:key:z6MkpXEduLbfEpxGpadTPFodnKeo8qCZdotBqyemjeahLyVA") })
	t.Run("identity", func(t *testing.T) { checkBad(t, "did:key:z6MkeXATEjyXENzBXBxgC5EHk2JE5aqd7qMGGtDpLUH1e2Sj") })
	t.Run("order_two", func(t *testing.T) { checkBad(t, "did:key:z6MkvQQfodDS9hpfvSLcFA5f2iCB9tBXk3PE5b1P8VVsjtRt") })
	t.Run("fragment", func(t *testing.T) { checkBad(t, want+"#key-1") })
	t.Run("extra_leading_one", func(t *testing.T) { checkBad(t, "did:key:z1"+strings.TrimPrefix(want, "did:key:z")) })
}

func TestSigningInputAndDigestID(t *testing.T) {
	security := readSecurityVector(t)
	preimage, err := hex.DecodeString(security.IntentPreimageHex)
	if err != nil {
		t.Fatal(err)
	}
	canonical := preimage[16:]
	got, err := SigningInput("intent", canonical)
	if err != nil || !bytes.Equal(got, preimage) {
		t.Fatalf("SigningInput() = %x, %v; want %x", got, err, preimage)
	}
	id, err := DigestID("fy1:", "intent", canonical)
	wantID := "fy1:27e02e49a7cebf57976046e00d00ddd5e502cf700394f80c514adb5551bcab37"
	if err != nil || id != wantID || id != security.Message.MessageID {
		t.Fatalf("DigestID() = %q, %v; want %q", id, err, wantID)
	}
	t.Run("label_with_newline", func(t *testing.T) {
		if _, err := SigningInput("intent\n", nil); !errors.Is(err, ErrBadLabel) {
			t.Fatalf("SigningInput error = %v; want ErrBadLabel", err)
		}
	})
	t.Run("uppercase_label", func(t *testing.T) {
		if _, err := SigningInput("Intent", nil); !errors.Is(err, ErrBadLabel) {
			t.Fatalf("SigningInput error = %v; want ErrBadLabel", err)
		}
	})
	t.Run("unknown_prefix", func(t *testing.T) {
		if _, err := DigestID("fy2:", "intent", canonical); err == nil {
			t.Fatal("DigestID accepted unknown prefix")
		}
	})
	t.Run("display name record", func(t *testing.T) {
		// The first record of examples/name-record.example.json in the specification, without name_id and author_signature.
		record, err := hex.DecodeString("7b22617574686f725f6964223a226469643a6b65793a7a364d6b656852676637794a62676147665973646f41734b6442504533646a324359686f775164636a71534a67765664222c22646973706c61795f6e616d65223a225a6fc3ab204578616d706c65222c226973737565645f6174223a22323032362d31312d31315430303a30303a30305a222c2270726576696f75735f6e616d655f6964223a6e756c6c7d")
		if err != nil {
			t.Fatal(err)
		}
		want := "fynr2:ef06e1a717b60c8bef8f21d78ddea36ce8ec2a99232d044acf9a90f07a8af7c6"
		if got, err := DigestID("fynr2:", "name-record-id", record); err != nil || got != want {
			t.Fatalf("DigestID() = %q, %v; want %q", got, err, want)
		}
	})
}

func TestSigningRoundTrip(t *testing.T) {
	seed := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31}
	priv, err := NewKeyFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	canonical := []byte(`{"synthetic":true}`)
	signature, err := Sign(priv, "message", canonical)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(DIDOf(priv), "message", canonical, signature); err != nil {
		t.Fatalf("Verify() failed: %v", err)
	}
	if err := Verify(DIDOf(priv), "receipt", canonical, signature); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("Verify() wrong-label error = %v; want ErrBadSignature", err)
	}
	changed := append([]byte(nil), canonical...)
	changed[0] ^= 1
	if err := Verify(DIDOf(priv), "message", changed, signature); !errors.Is(err, ErrBadSignature) {
		t.Fatalf("Verify() tamper error = %v; want ErrBadSignature", err)
	}
}

func TestB64URLEncoding(t *testing.T) {
	security := readSecurityVector(t)
	if _, err := DecodeB64URL(security.Message.Signature, 64); err != nil {
		t.Fatalf("DecodeB64URL(security signature) failed: %v", err)
	}
	changed := security.Message.Signature[:len(security.Message.Signature)-1] + "B"
	if changed == security.Message.Signature {
		changed = security.Message.Signature[:len(security.Message.Signature)-1] + "A"
	}
	if _, err := DecodeB64URL(changed, 64); !errors.Is(err, ErrBadEncoding) {
		t.Fatalf("DecodeB64URL(noncanonical tail) error = %v; want ErrBadEncoding", err)
	}
	if _, err := DecodeB64URL(security.Message.Signature+"==", 64); !errors.Is(err, ErrBadEncoding) {
		t.Fatalf("DecodeB64URL(padded) error = %v; want ErrBadEncoding", err)
	}
	if _, err := DecodeB64URL(security.Message.Signature[:85], 64); !errors.Is(err, ErrBadEncoding) {
		t.Fatalf("DecodeB64URL(short) error = %v; want ErrBadEncoding", err)
	}
}

func readSecurityVector(t *testing.T) securityVectorFile {
	t.Helper()
	data, err := os.ReadFile("testdata/security-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector securityVectorFile
	if err := json.Unmarshal(data, &vector); err != nil {
		t.Fatal(err)
	}
	return vector
}
