package delegation

import (
	"encoding/json"
	"errors"
	"sync"
	"time"

	"foryou.local/foryou/internal/wire"
)

// AuthorRecord is the durable authorization state for one root author.
type AuthorRecord struct {
	// Version is the compare-and-swap version persisted by the store.
	Version uint64
	// State is the latest provider-signed authorization state.
	State wire.AuthorizationEpochState
	// EnrollNonces records enrollment requests until their expiry.
	EnrollNonces map[string]NonceUse
	// ControlNonces records control request digests for the author.
	ControlNonces map[string]string
	// Revoked maps delegation IDs to their validity end times.
	Revoked map[string]time.Time
}

// NonceUse records the request digest and expiry for an enrollment nonce.
type NonceUse struct {
	// Digest is the SHA-256 hex digest of the canonical request.
	Digest string
	// NotAfter is the latest time for which the request may be replayed.
	NotAfter time.Time
}

// EpochStore atomically persists one authorization record per author.
type EpochStore interface {
	// Load returns the record for authorID or an unregistered version-zero record.
	Load(authorID string) (AuthorRecord, error)
	// Commit atomically writes next when the stored version equals prevVersion.
	Commit(authorID string, prevVersion uint64, next AuthorRecord) error
}

// MemoryEpochStore stores authorization records in memory with atomic version checks.
type MemoryEpochStore struct {
	mu      sync.Mutex
	records map[string]AuthorRecord
}

// NewMemoryEpochStore creates an empty in-memory authorization store.
func NewMemoryEpochStore() *MemoryEpochStore {
	return &MemoryEpochStore{records: make(map[string]AuthorRecord)}
}

// Load returns the current record or an unregistered record at version zero.
func (m *MemoryEpochStore) Load(authorID string) (AuthorRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.records[authorID]
	if !ok {
		return emptyRecord(authorID), nil
	}
	return cloneRecord(record)
}

// Commit atomically replaces a record if its stored version matches prevVersion.
func (m *MemoryEpochStore) Commit(authorID string, prevVersion uint64, next AuthorRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	current := m.records[authorID]
	if current.Version != prevVersion {
		return ErrStale
	}
	if next.Version != prevVersion+1 {
		return errors.New("delegation: invalid record version")
	}
	cloned, err := cloneRecord(next)
	if err != nil {
		return err
	}
	m.records[authorID] = cloned
	return nil
}

// Snapshot encodes all authorization records for durable provider journaling.
func (m *MemoryEpochStore) Snapshot() ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return json.Marshal(m.records)
}

// RestoreMemoryEpochStore restores a memory store from a prior snapshot.
func RestoreMemoryEpochStore(data []byte) (*MemoryEpochStore, error) {
	var records map[string]AuthorRecord
	if err := json.Unmarshal(data, &records); err != nil {
		return nil, err
	}
	if records == nil {
		records = make(map[string]AuthorRecord)
	}
	return &MemoryEpochStore{records: records}, nil
}

func emptyRecord(authorID string) AuthorRecord {
	return AuthorRecord{State: wire.AuthorizationEpochState{AuthorID: authorID, StateRevision: 0, Status: "unregistered"}, EnrollNonces: map[string]NonceUse{}, ControlNonces: map[string]string{}, Revoked: map[string]time.Time{}}
}

func cloneRecord(record AuthorRecord) (AuthorRecord, error) {
	encoded, err := json.Marshal(record)
	if err != nil {
		return AuthorRecord{}, err
	}
	var cloned AuthorRecord
	err = json.Unmarshal(encoded, &cloned)
	if err == nil {
		if cloned.EnrollNonces == nil {
			cloned.EnrollNonces = map[string]NonceUse{}
		}
		if cloned.ControlNonces == nil {
			cloned.ControlNonces = map[string]string{}
		}
		if cloned.Revoked == nil {
			cloned.Revoked = map[string]time.Time{}
		}
	}
	return cloned, err
}
