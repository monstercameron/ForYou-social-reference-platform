package keys

import (
	"crypto/ed25519"
	"fmt"
	"math/big"
	"strings"
)

const base58Alphabet = "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz"

// DID encodes an Ed25519 public key as a canonical did:key identifier, or returns an empty string for a wrong-length key.
func DID(pub ed25519.PublicKey) string {
	if len(pub) != ed25519.PublicKeySize {
		return ""
	}
	material := append([]byte{0xed, 0x01}, pub...)
	return "did:key:z" + encodeBase58(material)
}

// ParseDID parses and strictly validates an Ed25519 did:key identifier.
func ParseDID(did string) (ed25519.PublicKey, error) {
	const prefix = "did:key:z"
	const encodedKeyLength = 47 // A canonical encoding of ed 01 and a 32-byte key is always 47 characters.
	if !strings.HasPrefix(did, prefix) || strings.ContainsAny(did, "?#") {
		return nil, fmt.Errorf("%w: DID format", ErrBadKey)
	}
	encoded := strings.TrimPrefix(did, prefix)
	if len(encoded) != encodedKeyLength {
		return nil, fmt.Errorf("%w: DID length", ErrBadKey)
	}
	decoded, ok := decodeBase58(encoded)
	if !ok || len(decoded) != 34 || decoded[0] != 0xed || decoded[1] != 0x01 || encodeBase58(decoded) != encoded {
		return nil, fmt.Errorf("%w: DID encoding", ErrBadKey)
	}
	pub := ed25519.PublicKey(decoded[2:])
	if _, err := strictPoint(pub); err != nil {
		return nil, fmt.Errorf("%w: DID public key", ErrBadKey)
	}
	return pub, nil
}

func encodeBase58(value []byte) string {
	zeros := 0
	for zeros < len(value) && value[zeros] == 0 {
		zeros++
	}
	n := new(big.Int).SetBytes(value)
	base := big.NewInt(58)
	var reversed strings.Builder
	for n.Sign() > 0 {
		quotient, remainder := new(big.Int), new(big.Int)
		quotient.QuoRem(n, base, remainder)
		reversed.WriteByte(base58Alphabet[remainder.Int64()])
		n = quotient
	}
	for i := 0; i < zeros; i++ {
		reversed.WriteByte(base58Alphabet[0])
	}
	encoded := reversed.String()
	result := []byte(encoded)
	for i, j := 0, len(result)-1; i < j; i, j = i+1, j-1 {
		result[i], result[j] = result[j], result[i]
	}
	return string(result)
}

func decodeBase58(encoded string) ([]byte, bool) {
	n := new(big.Int)
	base := big.NewInt(58)
	for i := 0; i < len(encoded); i++ {
		digit := strings.IndexByte(base58Alphabet, encoded[i])
		if digit < 0 {
			return nil, false
		}
		n.Mul(n, base)
		n.Add(n, big.NewInt(int64(digit)))
	}
	decoded := n.Bytes()
	zeros := 0
	for zeros < len(encoded) && encoded[zeros] == base58Alphabet[0] {
		zeros++
	}
	return append(make([]byte, zeros), decoded...), true
}
