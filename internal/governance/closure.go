package governance

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

// VerifyLogHead verifies a Foundation log head's signature and shape.
func VerifyLogHead(h wire.FoundationLogHead) error {
	if err := schemaCheck("FoundationLogHead", h); err != nil {
		return err
	}
	signedAt, err := wire.ParseTime(h.SignedAt)
	if err != nil || signedAt.IsZero() {
		return fmt.Errorf("%w: log head time", ErrInvalid)
	}
	unsigned, err := canon.EncodeWithout(h, "foundation_signature")
	if err != nil {
		return fmt.Errorf("%w: log head encoding: %v", ErrInvalid, err)
	}
	if err := keys.Verify(h.FoundationID, "foundation-log-head", unsigned, h.FoundationSignature); err != nil {
		return fmt.Errorf("%w: log head signature: %v", ErrInvalid, err)
	}
	return nil
}

// RootFromProof verifies an RFC 6962 inclusion path and returns its lowercase root hash.
func RootFromProof(objectHash string, p wire.FoundationLogProof) (string, error) {
	if err := schemaCheck("FoundationLogProof", p); err != nil {
		return "", err
	}
	if len(objectHash) != 64 || !lowerHexHashes([]string{objectHash}) || p.Size <= 0 || p.Index < 0 || p.Index >= p.Size {
		return "", fmt.Errorf("%w: log proof index or object hash", ErrInvalid)
	}
	objectBytes, _ := hex.DecodeString(objectHash)
	leafInput := append([]byte{0}, objectBytes...)
	root := sha256.Sum256(leafInput)
	pathIndex := 0
	var visit func(index, size int64) error
	visit = func(index, size int64) error {
		if size == 1 {
			return nil
		}
		k := int64(1)
		for k <= (size-1)/2 {
			k *= 2
		}
		var err error
		if index < k {
			err = visit(index, k)
		} else {
			err = visit(index-k, size-k)
		}
		if err != nil {
			return err
		}
		if pathIndex >= len(p.AuditPath) {
			return fmt.Errorf("%w: short log proof", ErrInvalid)
		}
		siblingHex := p.AuditPath[pathIndex]
		pathIndex++
		if len(siblingHex) != 64 || !lowerHexHashes([]string{siblingHex}) {
			return fmt.Errorf("%w: log proof hash", ErrInvalid)
		}
		sibling, _ := hex.DecodeString(siblingHex)
		input := make([]byte, 1, 65)
		input[0] = 1
		if index < k {
			input = append(input, root[:]...)
			input = append(input, sibling...)
		} else {
			input = append(input, sibling...)
			input = append(input, root[:]...)
		}
		root = sha256.Sum256(input)
		return nil
	}
	if err := visit(p.Index, p.Size); err != nil {
		return "", err
	}
	if pathIndex != len(p.AuditPath) {
		return "", fmt.Errorf("%w: long log proof", ErrInvalid)
	}
	return hex.EncodeToString(root[:]), nil
}

// CheckClosure validates a closure, its release threshold and its signed log head.
func CheckClosure(c wire.FoundationKeyClosure, set KeySet) error {
	if err := schemaCheck("FoundationKeyClosure", c); err != nil {
		return err
	}
	closedAt, err := wire.ParseTime(c.ClosedAt)
	if err != nil {
		return fmt.Errorf("%w: closure time", ErrInvalid)
	}
	headAt, err := wire.ParseTime(c.Head.SignedAt)
	if err != nil || c.Head.FoundationID != c.FoundationID || closedAt.Before(headAt) {
		return fmt.Errorf("%w: closure head or time", ErrInvalid)
	}
	if !sortedUnique(c.Voided) || !lowerHexHashes(c.Voided) {
		return fmt.Errorf("%w: closure voided hashes", ErrInvalid)
	}
	if err := VerifyLogHead(c.Head); err != nil {
		return err
	}
	unsigned, err := canon.EncodeWithout(c, "release_signatures")
	if err != nil {
		return fmt.Errorf("%w: closure encoding: %v", ErrInvalid, err)
	}
	parsed, err := canon.Parse(unsigned, canon.Limits{MaxDepth: 32})
	if err != nil {
		return fmt.Errorf("%w: closure encoding: %v", ErrInvalid, err)
	}
	return VerifyThreshold("foundation-key-closure", parsed, c.ReleaseSignatures, set)
}
