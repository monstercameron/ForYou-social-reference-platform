package governance

import (
	"fmt"
	"sort"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/wire"
)

// Timeline is the validated sequence of release key sets.
type Timeline struct {
	genesis KeySet
	updates []timelineUpdate
}

type timelineUpdate struct {
	effectiveAt time.Time
	set         KeySet
	prior       map[string]struct{}
}

// UpdateID calculates the content identifier for a release key-set update.
func UpdateID(u wire.ReleaseKeySetUpdate) (string, error) {
	return digestID("fyku2:", "release-key-set-update-id", u, "update_id", "release_signatures")
}

// NewTimeline validates and applies a complete update chain from genesis.
func NewTimeline(genesis KeySet, updates []wire.ReleaseKeySetUpdate) (*Timeline, error) {
	if err := genesis.Validate(); err != nil {
		return nil, err
	}
	bySequence := make(map[int64]wire.ReleaseKeySetUpdate, len(updates))
	for _, update := range updates {
		if old, ok := bySequence[update.Sequence]; ok {
			if old.UpdateID != update.UpdateID {
				return nil, ErrForkedRootOfTrust
			}
			oldBytes, oldErr := canon.Encode(old)
			updateBytes, updateErr := canon.Encode(update)
			if oldErr != nil || updateErr != nil || string(oldBytes) != string(updateBytes) {
				return nil, fmt.Errorf("%w: conflicting duplicate key-set update", ErrInvalid)
			}
			continue
		}
		bySequence[update.Sequence] = update
	}
	ordered := make([]wire.ReleaseKeySetUpdate, 0, len(bySequence))
	for sequence := int64(1); sequence <= int64(len(bySequence)); sequence++ {
		update, ok := bySequence[sequence]
		if !ok {
			return nil, fmt.Errorf("%w: missing update sequence %d", ErrInvalid, sequence)
		}
		ordered = append(ordered, update)
	}
	timeline := &Timeline{genesis: cloneKeySet(genesis)}
	previousID := ""
	var previousAt time.Time
	current := cloneKeySet(genesis)
	for index, update := range ordered {
		if err := schemaCheck("ReleaseKeySetUpdate", update); err != nil {
			return nil, err
		}
		wantID, err := digestID("fyku2:", "release-key-set-update-id", update, "update_id", "release_signatures")
		if err != nil || update.UpdateID != wantID {
			return nil, fmt.Errorf("%w: key-set update ID", ErrInvalid)
		}
		if update.Sequence != int64(index+1) || (index == 0 && update.PreviousUpdateID != nil) || (index > 0 && (update.PreviousUpdateID == nil || *update.PreviousUpdateID != previousID)) {
			return nil, fmt.Errorf("%w: key-set update chain", ErrInvalid)
		}
		effectiveAt, err := wire.ParseTime(update.EffectiveAt)
		if err != nil || (index > 0 && !effectiveAt.After(previousAt)) {
			return nil, fmt.Errorf("%w: key-set effective time", ErrInvalid)
		}
		set := KeySet{IDs: append([]string(nil), update.ReleaseKeyIDs...), Threshold: int(update.ReleaseThreshold)}
		if err := set.Validate(); err != nil {
			return nil, err
		}
		unsignedBytes, err := canon.EncodeWithout(update, "release_signatures")
		if err != nil {
			return nil, fmt.Errorf("%w: update encoding: %v", ErrInvalid, err)
		}
		unsigned, err := canon.Parse(unsignedBytes, canon.Limits{MaxDepth: 32})
		if err != nil {
			return nil, fmt.Errorf("%w: update encoding: %v", ErrInvalid, err)
		}
		if err := VerifyThreshold("release-key-set-update", unsigned, update.ReleaseSignatures, current); err != nil {
			return nil, err
		}
		if !sortedUnique(update.PriorObjects) || !lowerHexHashes(update.PriorObjects) {
			return nil, fmt.Errorf("%w: prior_objects order or digest", ErrInvalid)
		}
		prior := make(map[string]struct{}, len(update.PriorObjects))
		for _, hash := range update.PriorObjects {
			prior[hash] = struct{}{}
		}
		timeline.updates = append(timeline.updates, timelineUpdate{effectiveAt: effectiveAt, set: cloneKeySet(set), prior: prior})
		previousID, previousAt, current = update.UpdateID, effectiveAt, set
	}
	return timeline, nil
}

// SetAt returns the set in force at at and requires carry-over for a superseded signer set.
func (t *Timeline) SetAt(at time.Time, signed any) (KeySet, error) {
	if t == nil {
		return KeySet{}, fmt.Errorf("%w: nil timeline", ErrInvalid)
	}
	index := 0
	for i, update := range t.updates {
		if !update.effectiveAt.After(at) {
			index = i + 1
		}
	}
	set := t.genesis
	if index > 0 {
		set = t.updates[index-1].set
	}
	if signed != nil && index < len(t.updates) {
		hash, err := ObjectHash(signed)
		if err != nil {
			return KeySet{}, err
		}
		if _, ok := t.updates[index].prior[hash]; !ok {
			return KeySet{}, fmt.Errorf("%w: object not carried over by key-set update", ErrInvalid)
		}
	}
	return cloneKeySet(set), nil
}

func cloneKeySet(set KeySet) KeySet {
	return KeySet{IDs: append([]string(nil), set.IDs...), Threshold: set.Threshold}
}

func sortedUnique(values []string) bool {
	return sort.SliceIsSorted(values, func(i, j int) bool { return values[i] < values[j] }) && uniqueStrings(values)
}

func uniqueStrings(values []string) bool {
	for i := 1; i < len(values); i++ {
		if values[i] == values[i-1] {
			return false
		}
	}
	return true
}

func lowerHexHashes(values []string) bool {
	for _, value := range values {
		if len(value) != 64 {
			return false
		}
		for _, r := range value {
			if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
				return false
			}
		}
	}
	return true
}
