package succession

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"
	"sync"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/wire"
)

// Registry holds the successions published by one provider.
type Registry struct {
	mu          sync.RWMutex
	maxPerOld   int
	successions map[string]map[string]wire.KeySuccession
}

// NewRegistry creates a succession registry bounded per old key.
func NewRegistry(maxPerOld int) *Registry {
	return &Registry{maxPerOld: maxPerOld, successions: make(map[string]map[string]wire.KeySuccession)}
}

// Add stores a verified succession, including valid conflicts, and reports whether it was new.
func (r *Registry) Add(s wire.KeySuccession) (added bool, err error) {
	if err := Verify(s); err != nil {
		return false, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	byID := r.successions[s.OldID]
	if _, exists := byID[s.SuccessionID]; exists {
		return false, nil
	}
	if len(byID) >= r.maxPerOld {
		return false, ErrRegistryFull
	}
	if byID == nil {
		byID = make(map[string]wire.KeySuccession)
		r.successions[s.OldID] = byID
	}
	byID[s.SuccessionID] = s
	return true, nil
}

// ForOld returns all held successions for did in protocol-time and ID order.
func (r *Registry) ForOld(did string) []wire.KeySuccession {
	r.mu.RLock()
	defer r.mu.RUnlock()
	byID := r.successions[did]
	result := make([]wire.KeySuccession, 0, len(byID))
	for _, s := range byID {
		result = append(result, s)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].EffectiveAt != result[j].EffectiveAt {
			return result[i].EffectiveAt < result[j].EffectiveAt
		}
		return result[i].SuccessionID < result[j].SuccessionID
	})
	return result
}

// Snapshot encodes the registry as deterministic JSON for the provider journal.
func (r *Registry) Snapshot() ([]byte, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	oldIDs := make([]string, 0, len(r.successions))
	for oldID := range r.successions {
		oldIDs = append(oldIDs, oldID)
	}
	sort.Strings(oldIDs)
	entries := make([]wire.KeySuccession, 0)
	for _, oldID := range oldIDs {
		byID := r.successions[oldID]
		ids := make([]string, 0, len(byID))
		for id := range byID {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		for _, id := range ids {
			entries = append(entries, byID[id])
		}
	}
	data, err := json.Marshal(entries)
	if err != nil {
		return nil, fmt.Errorf("succession: snapshot: %w", err)
	}
	return data, nil
}

// RestoreRegistry parses a snapshot and re-verifies every stored succession.
func RestoreRegistry(data []byte, maxPerOld int) (*Registry, error) {
	value, err := canon.Parse(data, canon.Limits{MaxDepth: 32})
	if err != nil {
		return nil, fmt.Errorf("%w: snapshot JSON", ErrInvalid)
	}
	entries, ok := value.([]any)
	if !ok {
		return nil, fmt.Errorf("%w: snapshot shape", ErrInvalid)
	}
	r := NewRegistry(maxPerOld)
	for _, entry := range entries {
		if err := wire.Check("KeySuccession", entry); err != nil {
			return nil, fmt.Errorf("%w: snapshot entry shape", ErrInvalid)
		}
		encoded, err := canon.Marshal(entry)
		if err != nil {
			return nil, fmt.Errorf("%w: snapshot entry", ErrInvalid)
		}
		decoder := json.NewDecoder(bytes.NewReader(encoded))
		decoder.DisallowUnknownFields()
		var s wire.KeySuccession
		if err := decoder.Decode(&s); err != nil {
			return nil, fmt.Errorf("%w: snapshot entry", ErrInvalid)
		}
		added, err := r.Add(s)
		if err != nil {
			return nil, fmt.Errorf("%w: snapshot entry verification", ErrInvalid)
		}
		if !added {
			return nil, fmt.Errorf("%w: duplicate snapshot entry", ErrInvalid)
		}
	}
	return r, nil
}
