package sdk

import (
	"foryou.local/foryou/internal/succession"
	"foryou.local/foryou/internal/wire"
)

// SuccessionView summarizes verified author-key succession assertions held locally.
type SuccessionView struct {
	DID, NewID     string
	Resolution     succession.Resolution
	AssertedByOld  bool
	Valid, Invalid []wire.KeySuccession
}

// ResolveAuthorSuccession verifies held assertions for did; unrelated successions are ignored. A link means the old key asserted it, not recovery of a lost key.
func ResolveAuthorSuccession(did string, held []wire.KeySuccession) SuccessionView {
	v := SuccessionView{DID: did, Resolution: succession.NoSuccession, Valid: []wire.KeySuccession{}, Invalid: []wire.KeySuccession{}}
	for _, s := range held {
		if s.OldID != did {
			continue
		}
		if err := succession.Verify(s); err != nil {
			v.Invalid = append(v.Invalid, s)
		} else {
			v.Valid = append(v.Valid, s)
		}
	}
	v.NewID, v.Resolution = succession.ResolveAuthor(did, v.Valid)
	v.AssertedByOld = v.Resolution == succession.Linked
	return v
}
