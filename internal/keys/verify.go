package keys

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
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

// The strict point checks multiply by the group order in math/big and cost
// milliseconds each. Verification is a pure function of the key, message and
// signature, so a result that passed is remembered by the SHA-256 of all three,
// and a public key that passed its point check is remembered by its bytes.
// Failures are never remembered. Both sets are bounded and start again when full.
var (
	verifiedSignatures = &digestSet{limit: 16384}
	strictPublicKeys   = &digestSet{limit: 4096}
	// honestPrivateKeys holds digests of whole private keys whose public half was
	// recomputed from the seed, so the derivation is done once per key.
	honestPrivateKeys = &digestSet{limit: 1024}
)

type digestSet struct {
	mu    sync.Mutex
	seen  map[[sha256.Size]byte]struct{}
	limit int
}

func (d *digestSet) has(digest [sha256.Size]byte) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	_, ok := d.seen[digest]
	return ok
}

func (d *digestSet) add(digest [sha256.Size]byte) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.seen == nil || len(d.seen) >= d.limit {
		d.seen = make(map[[sha256.Size]byte]struct{})
	}
	d.seen[digest] = struct{}{}
}

// rememberOwnSignature records a signature this process just made, so verifying it
// again in this process skips the point checks. It does so only when the key's
// public half is the one its seed derives: that point is a multiple of the base
// point, and the R of an honest signature is too, so both pass the strict checks.
func rememberOwnSignature(priv PrivateKey, msg, sig []byte) {
	pub := priv.Public().(ed25519.PublicKey)
	whole := sha256.Sum256(priv)
	if !honestPrivateKeys.has(whole) {
		if !pub.Equal(ed25519.NewKeyFromSeed(priv.Seed()).Public()) {
			return
		}
		honestPrivateKeys.add(whole)
	}
	if bytes.Equal(sig[:32], identityEncoding) {
		return
	}
	var key [sha256.Size]byte
	copy(key[:], pub)
	strictPublicKeys.add(key)
	verifiedSignatures.add(signatureDigest(pub, msg, sig))
}

func signatureDigest(pub, msg, sig []byte) [sha256.Size]byte {
	hash := sha256.New()
	hash.Write(pub)
	hash.Write(sig)
	hash.Write(msg)
	var digest [sha256.Size]byte
	hash.Sum(digest[:0])
	return digest
}

// VerifyStrict verifies a pure Ed25519 signature under the ForYou strict profile.
func VerifyStrict(pub, msg, sig []byte) error {
	if len(pub) != ed25519.PublicKeySize {
		return fmt.Errorf("%w: public key length", ErrBadKey)
	}
	if len(sig) != ed25519.SignatureSize {
		return fmt.Errorf("%w: signature length", ErrBadSignature)
	}
	digest := signatureDigest(pub, msg, sig)
	if verifiedSignatures.has(digest) {
		return nil
	}
	var key [sha256.Size]byte
	copy(key[:], pub)
	if !strictPublicKeys.has(key) {
		if _, err := strictPoint(pub); err != nil {
			return fmt.Errorf("%w: public point", ErrBadKey)
		}
		strictPublicKeys.add(key)
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
	verifiedSignatures.add(digest)
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
