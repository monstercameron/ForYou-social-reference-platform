package discovery

import (
	"math"
	"sort"
	"sync"
	"time"
)

// Index stores records in ordered global and predicate-specific views.
type Index struct {
	mu          sync.RWMutex
	records     map[string]Record
	ordered     []Record
	authors     map[string][]Record
	replies     map[string][]Record
	references  map[string][]Record
	supersedes  map[string][]Record
	sequences   []Record
	maxSequence uint64
}

// NewIndex creates an empty concurrent record index.
func NewIndex() *Index {
	return &Index{
		records:    make(map[string]Record),
		authors:    make(map[string][]Record),
		replies:    make(map[string][]Record),
		references: make(map[string][]Record),
		supersedes: make(map[string][]Record),
	}
}

// Add inserts a record unless its message ID is already present.
func (x *Index) Add(record Record) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	if _, exists := x.records[record.MessageID]; exists {
		return false
	}
	if record.Sequence > x.maxSequence {
		x.maxSequence = record.Sequence
	}
	record = cloneRecord(record)
	x.records[record.MessageID] = record
	x.ordered = insertRecord(x.ordered, record)
	x.authors[record.AuthorID] = insertRecord(x.authors[record.AuthorID], record)
	if record.ReplyTo != "" {
		x.replies[record.ReplyTo] = insertRecord(x.replies[record.ReplyTo], record)
	}
	if record.Supersedes != "" {
		x.supersedes[record.Supersedes] = insertRecord(x.supersedes[record.Supersedes], record)
	}
	seen := make(map[string]struct{}, len(record.RefTargets))
	for _, target := range record.RefTargets {
		if target == "" {
			continue
		}
		if _, exists := seen[target]; exists {
			continue
		}
		seen[target] = struct{}{}
		x.references[target] = insertRecord(x.references[target], record)
	}
	sequenceIndex := sort.Search(len(x.sequences), func(i int) bool {
		return !beforeSequence(x.sequences[i], record)
	})
	x.sequences = append(x.sequences, Record{})
	copy(x.sequences[sequenceIndex+1:], x.sequences[sequenceIndex:])
	x.sequences[sequenceIndex] = record
	return true
}

// Remove removes a record from every view without reducing the observed sequence maximum.
func (x *Index) Remove(messageID string) bool {
	x.mu.Lock()
	defer x.mu.Unlock()
	record, exists := x.records[messageID]
	if !exists {
		return false
	}
	delete(x.records, messageID)
	x.ordered = removeRecord(x.ordered, record)
	x.authors[record.AuthorID] = removeRecord(x.authors[record.AuthorID], record)
	deleteEmpty(x.authors, record.AuthorID)
	if record.ReplyTo != "" {
		x.replies[record.ReplyTo] = removeRecord(x.replies[record.ReplyTo], record)
		deleteEmpty(x.replies, record.ReplyTo)
	}
	if record.Supersedes != "" {
		x.supersedes[record.Supersedes] = removeRecord(x.supersedes[record.Supersedes], record)
		deleteEmpty(x.supersedes, record.Supersedes)
	}
	seen := make(map[string]struct{}, len(record.RefTargets))
	for _, target := range record.RefTargets {
		if _, exists := seen[target]; exists {
			continue
		}
		seen[target] = struct{}{}
		x.references[target] = removeRecord(x.references[target], record)
		deleteEmpty(x.references, target)
	}
	sequenceIndex := sort.Search(len(x.sequences), func(i int) bool {
		return !beforeSequence(x.sequences[i], record)
	})
	if sequenceIndex < len(x.sequences) && x.sequences[sequenceIndex].MessageID == messageID {
		x.sequences = append(x.sequences[:sequenceIndex], x.sequences[sequenceIndex+1:]...)
	}
	return true
}

// Get returns a copy of the indexed record, if present.
func (x *Index) Get(messageID string) (Record, bool) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	record, exists := x.records[messageID]
	return cloneRecord(record), exists
}

// Select returns every matching record in listing order.
func (x *Index) Select(query Query) []Record {
	records, _ := x.SelectWithin(query, math.MaxInt64)
	return records
}

// SelectWithin returns every matching record in listing order. It stops scanning and returns ErrBusy as soon
// as the matches would total more than budget bytes, so the result is never truncated.
func (x *Index) SelectWithin(query Query, budget int64) ([]Record, error) {
	x.mu.RLock()
	defer x.mu.RUnlock()
	view := x.view(query)
	start, end := timestampRange(view, query.From, query.hasFrom(), query.Until, query.hasUntil())
	var held int64
	var result []Record
	for _, record := range view[start:end] {
		if !query.Match(record) {
			continue
		}
		held += int64(len(record.Bytes))
		if held > budget {
			return nil, ErrBusy
		}
		result = append(result, cloneRecord(record))
	}
	return result, nil
}

func (x *Index) view(query Query) []Record {
	switch query.Route {
	case AuthorMessages:
		return x.authors[query.AuthorID]
	case Replies:
		return x.replies[query.Target]
	case References:
		return x.references[query.Target]
	case Versions:
		return x.supersedes[query.Target]
	case Messages:
		return x.ordered
	default:
		return nil
	}
}

// After returns up to max present records whose sequence is greater than after.
func (x *Index) After(after uint64, max int) []Record {
	if max <= 0 {
		return nil
	}
	x.mu.RLock()
	defer x.mu.RUnlock()
	start := sort.Search(len(x.sequences), func(i int) bool { return x.sequences[i].Sequence > after })
	end := len(x.sequences)
	if max < end-start {
		end = start + max
	}
	result := make([]Record, 0, end-start)
	for _, record := range x.sequences[start:end] {
		result = append(result, cloneRecord(record))
	}
	return result
}

// ObserveSequence records a sequence even when its message is no longer indexed.
func (x *Index) ObserveSequence(sequence uint64) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if sequence > x.maxSequence {
		x.maxSequence = sequence
	}
}

// MaxSequence returns the greatest sequence ever assigned to this index.
func (x *Index) MaxSequence() uint64 {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return x.maxSequence
}

// Len returns the number of indexed message IDs.
func (x *Index) Len() int {
	x.mu.RLock()
	defer x.mu.RUnlock()
	return len(x.records)
}

func timestampRange(records []Record, from time.Time, hasFrom bool, until time.Time, hasUntil bool) (int, int) {
	start, end := 0, len(records)
	if hasUntil {
		start = sort.Search(len(records), func(i int) bool { return records[i].Timestamp.Before(until) })
	}
	if hasFrom {
		end = sort.Search(len(records), func(i int) bool { return records[i].Timestamp.Before(from) })
	}
	if end < start {
		return start, start
	}
	return start, end
}

func deleteEmpty(index map[string][]Record, key string) {
	if len(index[key]) == 0 {
		delete(index, key)
	}
}

func beforeSequence(a, b Record) bool {
	return a.Sequence < b.Sequence || a.Sequence == b.Sequence && a.MessageID < b.MessageID
}
