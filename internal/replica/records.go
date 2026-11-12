package replica

import (
	"errors"
	"fmt"
	"regexp"
	"sync"
	"time"

	"foryou.local/foryou/internal/discovery"
	"foryou.local/foryou/internal/store"
)

const (
	recordsCollection  = "replica.records"
	bytesCollection    = "replica.message_bytes"
	sequenceCollection = "replica.sequence"
	cursorsCollection  = "replica.cursors"
	controlsCollection = "replica.controls"
	controlCounter     = "replica.control_generation"
)

// ErrCursorRegression reports a source cursor lower than its committed cursor.
var ErrCursorRegression = errors.New("replica: next_after moved backwards")

var (
	errInvalidControl       = errors.New("replica: invalid control")
	controlMessageIDPattern = regexp.MustCompile(`^fy1:[0-9a-f]{64}$`)
)

// ControlStatus is the public status a committed control gives a record. The values are the
// status names of docs/04-compliance.md; the status alone decides which body a read answers with.
type ControlStatus string

const (
	// StatusRestricted refuses a record whose existence may be disclosed but whose reason may not.
	StatusRestricted ControlStatus = "restricted"
	// StatusPolicyRestricted refuses a record for a disclosed policy reason.
	StatusPolicyRestricted ControlStatus = "policy_restricted"
	// StatusLegalRestricted refuses a record for a disclosed legal reason.
	StatusLegalRestricted ControlStatus = "legal_restricted"
	// StatusNotFound answers a record whose existence may not be disclosed exactly like an unknown ID.
	StatusNotFound ControlStatus = "not_found"
)

// Copy is one exact, already verified source message and its source-local sequence.
type Copy struct {
	// MessageID identifies the exact message encoded by Bytes.
	MessageID string
	// Bytes contains the original message representation and is stored unchanged.
	Bytes []byte
	// SourceSequence is the sequence assigned by the source provider.
	SourceSequence uint64
}

type recordMetadata struct {
	MessageID        string `json:"message_id"`
	Sequence         uint64 `json:"sequence"`
	SourceProviderID string `json:"source_provider_id"`
	SourceSequence   uint64 `json:"source_sequence"`
	ImportedAt       string `json:"imported_at"`
}

// Records durably stores exact copied message bytes and indexes them by local sequence.
type Records struct {
	db       store.Store
	index    *discovery.Index
	now      func() time.Time
	controls *controlState
}

func newRecords(db store.Store, index *discovery.Index, now func() time.Time) *Records {
	return &Records{db: db, index: index, now: now, controls: &controlState{records: make(map[string]controlRecord)}}
}

// Import atomically stores new verified copies and advances one source cursor.
func (r *Records) Import(providerID string, batch []Copy, nextAfter uint64) (int, error) {
	type parsedCopy struct {
		record         discovery.Record
		sourceSequence uint64
	}
	parsed := make([]parsedCopy, 0, len(batch))
	seen := make(map[string]struct{}, len(batch))
	for _, copied := range batch {
		record, err := discovery.ParseRecord(copied.Bytes, 0)
		if err != nil {
			return 0, fmt.Errorf("replica: parse copied record: %w", err)
		}
		if record.MessageID != copied.MessageID {
			return 0, fmt.Errorf("replica: copied message ID does not match bytes")
		}
		if _, exists := seen[copied.MessageID]; exists {
			continue
		}
		seen[copied.MessageID] = struct{}{}
		parsed = append(parsed, parsedCopy{record: record, sourceSequence: copied.SourceSequence})
	}
	// Holding the control lock through the index update keeps a concurrent removal from being re-indexed.
	r.controls.mu.RLock()
	defer r.controls.mu.RUnlock()
	if r.controls.err != nil {
		return 0, r.controls.err
	}
	metadataCollection := store.Collection[recordMetadata]{Name: recordsCollection}
	cursorCollection := store.Collection[uint64]{Name: cursorsCollection}
	controlCollection := store.Collection[controlRecord]{Name: controlsCollection}
	var additions []discovery.Record
	added := 0
	err := r.db.Update(func(tx store.Tx) error {
		current, exists, err := cursorCollection.Get(tx, providerID)
		if err != nil {
			return err
		}
		if exists && nextAfter < current {
			return ErrCursorRegression
		}
		for _, copied := range parsed {
			record := copied.record
			if control, controlled, err := controlCollection.Get(tx, record.MessageID); err != nil {
				return err
			} else if controlled && control.Removed {
				continue
			}
			if _, exists, err := tx.Get(recordsCollection, record.MessageID); err != nil {
				return err
			} else if exists {
				continue
			}
			sequence, err := tx.NextSequence("replica.acceptance")
			if err != nil {
				return err
			}
			metadata := recordMetadata{
				MessageID: record.MessageID, Sequence: sequence, SourceProviderID: providerID,
				SourceSequence: copied.sourceSequence, ImportedAt: r.now().UTC().Truncate(time.Second).Format("2006-01-02T15:04:05Z"),
			}
			if err := metadataCollection.Put(tx, record.MessageID, metadata); err != nil {
				return err
			}
			if err := tx.Put(bytesCollection, record.MessageID, record.Bytes); err != nil {
				return err
			}
			if err := tx.Put(sequenceCollection, fmt.Sprintf("%020d", sequence), []byte(record.MessageID)); err != nil {
				return err
			}
			record.Sequence = sequence
			additions = append(additions, record)
			added++
		}
		return cursorCollection.Put(tx, providerID, nextAfter)
	})
	if err != nil {
		return 0, err
	}
	for _, record := range additions {
		r.index.Add(record)
	}
	return added, nil
}

// Cursor returns the last committed next_after value for a source provider.
func (r *Records) Cursor(providerID string) uint64 {
	var cursor uint64
	_ = r.db.View(func(tx store.Tx) error {
		value, exists, err := (store.Collection[uint64]{Name: cursorsCollection}).Get(tx, providerID)
		if exists && err == nil {
			cursor = value
		}
		return err
	})
	return cursor
}

// Lookup returns a copy of the exact stored message bytes.
func (r *Records) Lookup(id string) ([]byte, bool) {
	var value []byte
	var ok bool
	_ = r.db.View(func(tx store.Tx) error {
		var err error
		value, ok, err = tx.Get(bytesCollection, id)
		return err
	})
	return value, ok
}

// Select returns indexed records matching a discovery query.
func (r *Records) Select(q discovery.Query) []discovery.Record { return r.index.Select(q) }

// After returns records with a local sequence greater than after.
func (r *Records) After(after uint64, max int) []discovery.Record { return r.index.After(after, max) }

// MaxSequence returns the greatest sequence assigned by this replica.
func (r *Records) MaxSequence() uint64 { return r.index.MaxSequence() }

func (r *Records) restore() error {
	metadataCollection := store.Collection[recordMetadata]{Name: recordsCollection}
	return r.db.View(func(tx store.Tx) error {
		return metadataCollection.Scan(tx, store.Range{}, func(_ string, metadata recordMetadata) (bool, error) {
			bytes, ok, err := tx.Get(bytesCollection, metadata.MessageID)
			if err != nil {
				return false, err
			}
			if !ok {
				return false, fmt.Errorf("missing bytes for %s", metadata.MessageID)
			}
			record, err := discovery.ParseRecord(bytes, metadata.Sequence)
			if err != nil {
				return false, err
			}
			if record.MessageID != metadata.MessageID {
				return false, errors.New("record key does not match message ID")
			}
			r.index.Add(record)
			r.index.ObserveSequence(metadata.Sequence)
			return true, nil
		})
	})
}

func (r *Records) recoverControls() {
	state := &controlState{records: make(map[string]controlRecord)}
	r.controls = state
	collection := store.Collection[controlRecord]{Name: controlsCollection}
	err := r.db.Update(func(tx store.Tx) error {
		return collection.Scan(tx, store.Range{}, func(messageID string, record controlRecord) (bool, error) {
			state.records[messageID] = record
			state.generation = max(state.generation, record.Sequence)
			if !record.Removed {
				return true, nil
			}
			if err := deleteRecord(tx, messageID); err != nil {
				return false, err
			}
			r.index.Remove(messageID)
			return true, nil
		})
	})
	if err != nil {
		state.err = fmt.Errorf("replica: control state unavailable: %w", err)
		state.records = nil
	}
}

func (r *Records) commitControl(messageID string, status ControlStatus, remove bool) (uint64, error) {
	if !controlMessageIDPattern.MatchString(messageID) || !validControlStatus(status) {
		return 0, errInvalidControl
	}
	state := r.controls
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.err != nil {
		return 0, state.err
	}
	collection := store.Collection[controlRecord]{Name: controlsCollection}
	var next controlRecord
	err := r.db.Update(func(tx store.Tx) error {
		sequence, err := tx.NextSequence(controlCounter)
		if err != nil {
			return err
		}
		current, _, err := collection.Get(tx, messageID)
		if err != nil {
			return err
		}
		next = controlRecord{Status: status, Removed: current.Removed || remove, Sequence: sequence}
		if err := collection.Put(tx, messageID, next); err != nil {
			return err
		}
		if remove {
			return deleteRecord(tx, messageID)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	state.records[messageID] = next
	state.generation = next.Sequence
	if remove {
		r.index.Remove(messageID)
	}
	return next.Sequence, nil
}

func deleteRecord(tx store.Tx, messageID string) error {
	metadata, exists, err := (store.Collection[recordMetadata]{Name: recordsCollection}).Get(tx, messageID)
	if err != nil {
		return err
	}
	if exists {
		if err := tx.Delete(sequenceCollection, fmt.Sprintf("%020d", metadata.Sequence)); err != nil {
			return err
		}
	}
	if err := tx.Delete(recordsCollection, messageID); err != nil {
		return err
	}
	return tx.Delete(bytesCollection, messageID)
}

func validControlStatus(status ControlStatus) bool {
	switch status {
	case StatusRestricted, StatusPolicyRestricted, StatusLegalRestricted, StatusNotFound:
		return true
	}
	return false
}

// controlState is the replica's own operator control state. A record with an entry here is hidden
// from this replica's responses; the replica never consults or applies any other provider's controls.
type controlState struct {
	mu         sync.RWMutex
	generation uint64
	records    map[string]controlRecord
	err        error
}

// controlRecord is the durable current control of one record. Sequence is the control generation
// that committed it, and Removed keeps a legally required removal from being lifted or re-ingested.
type controlRecord struct {
	Status   ControlStatus `json:"status"`
	Removed  bool          `json:"removed"`
	Sequence uint64        `json:"sequence"`
}

func (c *controlState) lookup(messageID string) (controlRecord, bool, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.err != nil {
		return controlRecord{}, false, c.err
	}
	record, controlled := c.records[messageID]
	return record, controlled, nil
}

func (c *controlState) failure() error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.err
}

// Visibility hides every record that carries a committed control from this replica's responses.
func (s *Service) Visibility() discovery.Visibility {
	return replicaVisibility{controls: s.records.controls}
}

type replicaVisibility struct{ controls *controlState }

func (v replicaVisibility) Generation() uint64 {
	v.controls.mu.RLock()
	defer v.controls.mu.RUnlock()
	return v.controls.generation
}

func (v replicaVisibility) Visible(messageID string) (bool, uint64, error) {
	record, controlled, err := v.controls.lookup(messageID)
	if err != nil {
		return false, 0, err
	}
	if controlled {
		return false, record.Sequence, nil
	}
	return true, 0, nil
}
