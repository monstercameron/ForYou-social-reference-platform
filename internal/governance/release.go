package governance

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

var (
	// ErrInvalid identifies an invalid governance object or signature.
	ErrInvalid = errors.New("governance: invalid object")
	// ErrForkedRootOfTrust identifies rival release-key updates at one sequence.
	ErrForkedRootOfTrust = errors.New("governance: forked root of trust")
)

// KeySet is a release signing set and its required signature threshold.
type KeySet struct {
	IDs       []string
	Threshold int
}

// Validate checks that the set has at least five canonical, distinct keys and a two-thirds threshold.
func (s KeySet) Validate() error {
	if len(s.IDs) < 5 || s.Threshold <= 0 || s.Threshold > len(s.IDs) || s.Threshold*3 < 2*len(s.IDs) {
		return fmt.Errorf("%w: release key set size or threshold", ErrInvalid)
	}
	seen := make(map[string]struct{}, len(s.IDs))
	for _, id := range s.IDs {
		if _, err := keys.ParseDID(id); err != nil {
			return fmt.Errorf("%w: release key ID: %v", ErrInvalid, err)
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("%w: duplicate release key ID", ErrInvalid)
		}
		seen[id] = struct{}{}
	}
	return nil
}

// SignRelease signs an unsigned object with each supplied release key.
func SignRelease(label string, unsigned any, signers []ed25519.PrivateKey) ([]wire.ReleaseSignature, error) {
	canonical, err := canon.Encode(unsigned)
	if err != nil {
		return nil, fmt.Errorf("%w: encode signing object: %v", ErrInvalid, err)
	}
	result := make([]wire.ReleaseSignature, 0, len(signers))
	seen := make(map[string]struct{}, len(signers))
	for _, signer := range signers {
		id := keys.DIDOf(signer)
		if id == "" {
			return nil, fmt.Errorf("%w: private key", ErrInvalid)
		}
		if _, ok := seen[id]; ok {
			return nil, fmt.Errorf("%w: duplicate signer", ErrInvalid)
		}
		seen[id] = struct{}{}
		signature, err := keys.Sign(signer, label, canonical)
		if err != nil {
			return nil, fmt.Errorf("%w: sign: %v", ErrInvalid, err)
		}
		result = append(result, wire.ReleaseSignature{ReleaseKeyID: id, Signature: signature})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ReleaseKeyID < result[j].ReleaseKeyID })
	return result, nil
}

// VerifyThreshold verifies the sorted release signatures over unsigned.
func VerifyThreshold(label string, unsigned any, sigs []wire.ReleaseSignature, set KeySet) error {
	if err := set.Validate(); err != nil {
		return err
	}
	canonical, err := canon.Encode(unsigned)
	if err != nil {
		return fmt.Errorf("%w: encode signing object: %v", ErrInvalid, err)
	}
	input, err := keys.SigningInput(label, canonical)
	if err != nil {
		return fmt.Errorf("%w: signing label: %v", ErrInvalid, err)
	}
	allowed := make(map[string]struct{}, len(set.IDs))
	for _, id := range set.IDs {
		allowed[id] = struct{}{}
	}
	good := 0
	previous := ""
	for _, entry := range sigs {
		if _, err := keys.ParseDID(entry.ReleaseKeyID); err != nil {
			return fmt.Errorf("%w: signature key ID: %v", ErrInvalid, err)
		}
		if entry.ReleaseKeyID <= previous {
			return fmt.Errorf("%w: release signatures not strictly sorted", ErrInvalid)
		}
		previous = entry.ReleaseKeyID
		signature, err := keys.DecodeB64URL(entry.Signature, ed25519.SignatureSize)
		if err != nil {
			return fmt.Errorf("%w: malformed release signature: %v", ErrInvalid, err)
		}
		if _, ok := allowed[entry.ReleaseKeyID]; !ok {
			continue
		}
		pub, _ := keys.ParseDID(entry.ReleaseKeyID)
		if keys.VerifyStrict(pub, input, signature) != nil {
			return fmt.Errorf("%w: invalid in-set release signature", ErrInvalid)
		}
		good++
	}
	if good < set.Threshold {
		return fmt.Errorf("%w: release threshold not met", ErrInvalid)
	}
	return nil
}

// ObjectHash returns lowercase SHA-256 hex of an object's complete JCS bytes.
func ObjectHash(obj any) (string, error) {
	canonical, err := canon.Encode(obj)
	if err != nil {
		return "", fmt.Errorf("%w: encode object: %v", ErrInvalid, err)
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func schemaCheck(name string, value any) error {
	canonical, err := canon.Encode(value)
	if err != nil {
		return fmt.Errorf("%w: encode %s: %v", ErrInvalid, name, err)
	}
	parsed, err := canon.Parse(canonical, canon.Limits{MaxDepth: 32})
	if err != nil {
		return fmt.Errorf("%w: parse %s: %v", ErrInvalid, name, err)
	}
	if err := wire.Check(name, parsed); err != nil {
		return fmt.Errorf("%w: %s: %v", ErrInvalid, name, err)
	}
	return nil
}

func releaseSchema(label string) (string, bool) {
	switch label {
	case "release-key-set-update":
		return "ReleaseKeySetUpdate", true
	case "foundation-key-certificate":
		return "FoundationKeyCertificate", true
	case "foundation-key-closure":
		return "FoundationKeyClosure", true
	case "genesis-manifest":
		return "GenesisManifest", true
	case "fee-schedule-approval":
		return "FeeScheduleApproval", true
	case "distribution-policy":
		return "DistributionPolicy", true
	default:
		return "", false
	}
}

func digestID(prefix, label string, obj any, omit ...string) (string, error) {
	canonical, err := canon.EncodeWithout(obj, omit...)
	if err != nil {
		return "", fmt.Errorf("%w: encode ID object: %v", ErrInvalid, err)
	}
	id, err := keys.DigestID(prefix, label, canonical)
	if err != nil {
		return "", fmt.Errorf("%w: ID: %v", ErrInvalid, err)
	}
	return id, nil
}
