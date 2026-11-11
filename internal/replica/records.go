package replica

import (
	"errors"
	"fmt"
	"time"

	"foryou.local/foryou/internal/discovery"
	"foryou.local/foryou/internal/store"
)

const (
	recordsCollection  = "replica.records"
	bytesCollection    = "replica.message_bytes"
	sequenceCollection = "replica.sequence"
	cursorsCollection  = "replica.cursors"
)

// ErrCursorRegression reports a source cursor lower than its committed cursor.
var ErrCursorRegression = errors.New("replica: next_after moved backwards")

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
	db    store.Store
	index *discovery.Index
	now   func() time.Time
}

func newRecords(db store.Store, index *discovery.Index, now func() time.Time) *Records {
	return &Records{db: db, index: index, now: now}
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
	metadataCollection := store.Collection[recordMetadata]{Name: recordsCollection}
	cursorCollection := store.Collection[uint64]{Name: cursorsCollection}
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

// Visibility treats this independently operated mirror as holding its own copy under its own key;
// a provider's later restriction does not bind the replica, as described by docs/04-compliance.md.
func (s *Service) Visibility() discovery.Visibility { return replicaVisibility{} }

type replicaVisibility struct{}

func (replicaVisibility) Generation() uint64                   { return 0 }
func (replicaVisibility) Visible(string) (bool, uint64, error) { return true, 0, nil }
