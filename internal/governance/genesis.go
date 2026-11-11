package governance

import (
	"fmt"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/wire"
)

// CheckGenesis verifies the release trust root; callers must also validate fee schedules and distribution policy.
func CheckGenesis(g wire.GenesisManifest) (KeySet, error) {
	if err := schemaCheck("GenesisManifest", g); err != nil {
		return KeySet{}, err
	}
	if g.Release != "1.0" {
		return KeySet{}, fmt.Errorf("%w: unsupported genesis release", ErrInvalid)
	}
	if _, err := wire.ParseTime(g.SignedAt); err != nil {
		return KeySet{}, fmt.Errorf("%w: genesis signed_at", ErrInvalid)
	}
	set := KeySet{IDs: append([]string(nil), g.ReleaseKeyIDs...), Threshold: int(g.ReleaseThreshold)}
	if err := set.Validate(); err != nil {
		return KeySet{}, err
	}
	unsignedBytes, err := canon.EncodeWithout(g, "release_signatures")
	if err != nil {
		return KeySet{}, fmt.Errorf("%w: genesis encoding: %v", ErrInvalid, err)
	}
	unsigned, err := canon.Parse(unsignedBytes, canon.Limits{MaxDepth: 32})
	if err != nil {
		return KeySet{}, fmt.Errorf("%w: genesis encoding: %v", ErrInvalid, err)
	}
	if err := VerifyThreshold("genesis-manifest", unsigned, g.ReleaseSignatures, set); err != nil {
		return KeySet{}, err
	}
	for _, certificate := range g.FoundationKeyCertificates {
		if err := CheckCertificate(certificate, set, true); err != nil {
			return KeySet{}, err
		}
	}
	return set, nil
}
