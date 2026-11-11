package keys

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

// PrivateKey names the standard library Ed25519 private key type.
type PrivateKey = ed25519.PrivateKey

var labelPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

var idPrefixes = map[string]struct{}{
	"fy1:": {}, "fyd1:": {}, "fye1:": {}, "fyfs2:": {}, "fydi2:": {},
	"fydc2:": {}, "fyfq2:": {}, "fyqr2:": {}, "fydp2:": {}, "fyds2:": {},
	"fyrd2:": {}, "fyks2:": {}, "fyfc2:": {}, "fysc2:": {}, "fyku2:": {},
}

// NewKeyFromSeed creates a pure Ed25519 private key from a 32-byte seed.
func NewKeyFromSeed(seed []byte) (PrivateKey, error) {
	if len(seed) != ed25519.SeedSize {
		return nil, fmt.Errorf("%w: seed length", ErrBadKey)
	}
	return ed25519.NewKeyFromSeed(seed), nil
}

// DIDOf returns the did:key identifier for the public half of priv, or an empty string for a wrong-length key.
func DIDOf(priv PrivateKey) string {
	if len(priv) != ed25519.PrivateKeySize {
		return ""
	}
	return DID(priv.Public().(ed25519.PublicKey))
}

// SigningInput constructs the ForYou/1 domain-separated input for canonical bytes.
func SigningInput(label string, canonical []byte) ([]byte, error) {
	if !labelPattern.MatchString(label) {
		return nil, ErrBadLabel
	}
	input := make([]byte, 0, len("ForYou/1/")+len(label)+1+len(canonical))
	input = append(input, "ForYou/1/"...)
	input = append(input, label...)
	input = append(input, '\n')
	input = append(input, canonical...)
	return input, nil
}

// Sign signs canonical bytes under the supplied ForYou domain label.
func Sign(priv PrivateKey, label string, canonical []byte) (string, error) {
	if len(priv) != ed25519.PrivateKeySize {
		return "", fmt.Errorf("%w: private key length", ErrBadKey)
	}
	input, err := SigningInput(label, canonical)
	if err != nil {
		return "", err
	}
	return EncodeB64URL(ed25519.Sign(priv, input)), nil
}

// DigestID returns a typed content ID from the domain-separated canonical bytes.
func DigestID(prefix, label string, canonical []byte) (string, error) {
	if _, ok := idPrefixes[prefix]; !ok || !strings.HasSuffix(prefix, ":") {
		return "", fmt.Errorf("keys: unsupported ID prefix %q", prefix)
	}
	input, err := SigningInput(label, canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(input)
	return prefix + hex.EncodeToString(digest[:]), nil
}
