package delegation

import (
	"errors"
	"math"
	"sync"
	"time"

	"foryou.local/foryou/internal/wire"
)

var challengeErrGenerationExhausted = errors.New("serviceauth: generation exhausted")

// ChallengeStore provides linearizable issuance, lookup, generation and consumption.
type ChallengeStore interface {
	// Generation reports the current issuance generation.
	Generation() (uint64, error)
	// AdvanceGeneration atomically advances to a generation that has never been used.
	AdvanceGeneration() (uint64, error)
	// Insert atomically evicts expired records and applies issuance limits before storing rec.
	// It returns ErrRateLimited or ErrSaturated when the respective limit is reached.
	Insert(rec ChallengeRecord, limits Limits, now time.Time) error
	// Get returns a record by its challenge value, if present.
	Get(challenge string) (ChallengeRecord, bool, error)
	// Consume succeeds for exactly one caller and only for an unused record of generation that expires after now.
	Consume(challenge string, generation uint64, now time.Time) (bool, error)
}

// ChallengeRecord stores a challenge with its issuance source and recovery generation.
type ChallengeRecord struct {
	Challenge wire.ServiceChallenge
	// Source is the caller-supplied opaque rate-limit key, such as a remote address.
	Source     string
	Protected  bool
	Generation uint64
	Used       bool
	ExpiresAt  time.Time
}

// MemoryChallengeStore is a single-process linearizable store. A restart loses every earlier challenge, so all challenges issued before it are rejected and their generation is not reused in effect.
type MemoryChallengeStore struct {
	mu         sync.Mutex
	generation uint64
	records    map[string]ChallengeRecord
}

// NewMemoryChallengeStore creates an empty in-memory challenge store.
func NewMemoryChallengeStore() *MemoryChallengeStore {
	return &MemoryChallengeStore{records: make(map[string]ChallengeRecord)}
}

// Limits are the disclosed global, per-source and protected issuance capacities.
type Limits struct {
	// MaxOutstanding caps all unexpired stored challenges, including consumed ones.
	MaxOutstanding int
	// MaxPerSource caps unexpired stored challenges from one source.
	MaxPerSource int
	// ReservedProtected keeps this many global slots for protected route shapes.
	ReservedProtected int
}

// Generation reports the current generation.
func (s *MemoryChallengeStore) Generation() (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.generation, nil
}

// AdvanceGeneration increments and returns the generation.
func (s *MemoryChallengeStore) AdvanceGeneration() (uint64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.generation == math.MaxUint64 {
		return 0, challengeErrGenerationExhausted
	}
	s.generation++
	return s.generation, nil
}

// Insert evicts expired entries, checks limits and stores rec atomically.
func (s *MemoryChallengeStore) Insert(rec ChallengeRecord, limits Limits, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if rec.Generation != s.generation {
		return ErrStateUncertain
	}
	for id, existing := range s.records {
		if !existing.ExpiresAt.After(now) {
			delete(s.records, id)
		}
	}
	if _, exists := s.records[rec.Challenge.Challenge]; exists {
		return ErrStateUncertain
	}
	perSource, unprotected := 0, 0
	for _, existing := range s.records {
		if existing.Source == rec.Source {
			perSource++
		}
		if !existing.Protected {
			unprotected++
		}
	}
	if perSource >= limits.MaxPerSource {
		return ErrRateLimited
	}
	if len(s.records) >= limits.MaxOutstanding || !rec.Protected && unprotected >= limits.MaxOutstanding-limits.ReservedProtected {
		return ErrSaturated
	}
	s.records[rec.Challenge.Challenge] = rec
	return nil
}

// Get returns the stored record for challenge, if present.
func (s *MemoryChallengeStore) Get(challenge string) (ChallengeRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[challenge]
	return record, ok, nil
}

// Consume marks an unexpired unused record used for exactly one caller.
func (s *MemoryChallengeStore) Consume(challenge string, generation uint64, now time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.records[challenge]
	if !ok || record.Used || generation != s.generation || record.Generation != generation || !record.ExpiresAt.After(now) {
		return false, nil
	}
	record.Used = true
	s.records[challenge] = record
	return true, nil
}
