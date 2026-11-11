package keys

import (
	"crypto/ed25519"
	"errors"
	"fmt"
)

var (
	// ErrBadKey identifies an invalid key or DID.
	ErrBadKey = errors.New("keys: bad key")
	// ErrBadEncoding identifies a noncanonical or malformed encoding.
	ErrBadEncoding = errors.New("keys: bad encoding")
	// ErrBadSignature identifies an invalid signature.
	ErrBadSignature = errors.New("keys: bad signature")
	// ErrBadLabel identifies a label outside the protocol grammar.
	ErrBadLabel = errors.New("keys: bad label")
)

// VerifyStrict verifies a pure Ed25519 signature under the ForYou strict profile.
func VerifyStrict(pub, msg, sig []byte) error {
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: public key length", ErrBadKey)
	}
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("%w: signature length", ErrBadSignature)
	}
	if _, err := strictPoint(pub); err != nil {
		return fmt.Errorf("%w: public point", ErrBadKey)
	}
	if _, err := strictPoint(sig[:32]); err != nil {
		return fmt.Errorf("%w: R point", ErrBadSignature)
	}
	if littleEndianInt(sig[32:]).Cmp(groupOrder) >= 0 {
		return fmt.Errorf("%w: scalar out of range", ErrBadSignature)
	}
	if !ed25519.Verify(ed25519.PublicKey(pub), msg, sig) {
		return ErrBadSignature
	}
	return nil
}

// Verify checks a DID, canonical base64url signature and domain-separated message.
func Verify(did, label string, canonical []byte, signature string) error {
	pub, err := ParseDID(did)
	if err != nil {
		return err
	}
	sig, err := DecodeB64URL(signature, ed25519.SignatureSize)
	if err != nil {
		return err
	}
	input, err := SigningInput(label, canonical)
	if err != nil {
		return err
	}
	return VerifyStrict(pub, input, sig)
}
