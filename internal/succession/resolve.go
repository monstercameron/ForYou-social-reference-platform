package succession

import (
	"fmt"
	"sync"

	"foryou.local/foryou/internal/wire"
)

// Resolution describes whether an author succession link is absent, unique or conflicting.
type Resolution int

const (
	// NoSuccession means no verified succession was held for the old key.
	NoSuccession Resolution = iota
	// Linked means all verified successions for the old key name one successor.
	Linked
	// Unresolved means verified successions name different successor keys.
	Unresolved
)

// ResolveAuthor finds an unambiguous successor among the held author successions.
func ResolveAuthor(oldID string, held []wire.KeySuccession) (newID string, r Resolution) {
	for _, s := range held {
		if s.OldID != oldID || Verify(s) != nil {
			continue
		}
		if r == NoSuccession {
			newID, r = s.NewID, Linked
			continue
		}
		if newID != s.NewID {
			return "", Unresolved
		}
	}
	return newID, r
}

// ProviderLine tracks the monitor-accepted provider key succession chain.
type ProviderLine struct {
	mu         sync.RWMutex
	successors map[string]string
	byID       map[string]wire.KeySuccession
}

// NewProviderLine creates an empty accepted provider succession line.
func NewProviderLine() *ProviderLine {
	return &ProviderLine{successors: make(map[string]string), byID: make(map[string]wire.KeySuccession)}
}

// Accept accepts a verified provider succession under the monitor acceptance rule.
// hasSignedClaims reports whether new_id signed a settlement claim before the claim that names this succession.
func (l *ProviderLine) Accept(s wire.KeySuccession, hasSignedClaims func(did string) bool) error {
	if hasSignedClaims == nil {
		return fmt.Errorf("%w: missing settlement-claim check", ErrInvalid)
	}
	if err := Verify(s); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if successor, exists := l.successors[s.OldID]; exists {
		if successor != s.NewID {
			return ErrConflict
		}
		if hasSignedClaims(s.NewID) {
			return ErrSuccessorSignedClaims
		}
		l.byID[s.SuccessionID] = s
		return nil
	}
	for oldID, existingSuccessor := range l.successors {
		if oldID != s.OldID && (oldID == s.NewID || existingSuccessor == s.NewID) {
			return ErrConflict
		}
	}
	if hasSignedClaims(s.NewID) {
		return ErrSuccessorSignedClaims
	}
	l.successors[s.OldID] = s.NewID
	l.byID[s.SuccessionID] = s
	return nil
}

// Successor returns the accepted immediate successor of oldID.
func (l *ProviderLine) Successor(oldID string) (string, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	successor, ok := l.successors[oldID]
	return successor, ok
}

// ByID returns an accepted succession by its content ID.
func (l *ProviderLine) ByID(successionID string) (wire.KeySuccession, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	s, ok := l.byID[successionID]
	return s, ok
}

// Line returns the accepted sequence of keys starting with firstKey.
func (l *ProviderLine) Line(firstKey string) []string {
	l.mu.RLock()
	defer l.mu.RUnlock()
	line := []string{firstKey}
	for {
		next, ok := l.successors[line[len(line)-1]]
		if !ok {
			return line
		}
		line = append(line, next)
	}
}
