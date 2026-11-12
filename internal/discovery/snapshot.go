package discovery

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

var (
	// ErrUnknownCursor reports a missing, expired, or mismatched cursor.
	ErrUnknownCursor = errors.New("discovery: unknown, expired or mismatched cursor")
	// ErrBusy reports snapshot capacity or work-limit exhaustion.
	ErrBusy = errors.New("discovery: snapshot capacity or work limit reached")
	// ErrUnavailable reports that paging could not continue safely.
	ErrUnavailable = errors.New("discovery: cannot continue safely")
)

const defaultMaxSnapshots = 1024
const defaultMaxBytes = 256 << 20
const defaultLifetime = 15 * time.Minute
const defaultScanBudget = 1000
const maxMessagesPerPage = 100
const cursorBytes = 32
const cursorPlaceholder = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

// SnapshotLimits bounds the number, retained bytes, lifetime, and scan work of snapshots.
type SnapshotLimits struct {
	MaxSnapshots int
	MaxBytes     int64
	Lifetime     time.Duration
	ScanBudget   int
}

// Validate checks limits after applying defaults to zero-valued fields.
func (l SnapshotLimits) Validate() error {
	l = effectiveLimits(l)
	if l.MaxSnapshots <= 0 || l.MaxBytes <= 0 || l.ScanBudget <= 0 {
		return fmt.Errorf("discovery: snapshot count, byte and scan limits must be positive")
	}
	if l.Lifetime < 15*time.Minute || l.Lifetime > time.Hour || l.Lifetime%time.Second != 0 {
		return fmt.Errorf("discovery: snapshot lifetime %v outside [15m, 60m] or not whole seconds", l.Lifetime)
	}
	return nil
}

func effectiveLimits(l SnapshotLimits) SnapshotLimits {
	if l.MaxSnapshots == 0 {
		l.MaxSnapshots = defaultMaxSnapshots
	}
	if l.MaxBytes == 0 {
		l.MaxBytes = defaultMaxBytes
	}
	if l.Lifetime == 0 {
		l.Lifetime = defaultLifetime
	}
	if l.ScanBudget == 0 {
		l.ScanBudget = defaultScanBudget
	}
	return l
}

// Visibility reports current deliverability and the generation of the last restriction.
type Visibility interface {
	Generation() uint64
	Visible(messageID string) (visible bool, hiddenAt uint64, err error)
}

// Entry is one frozen snapshot member with its exact JCS message bytes.
type Entry struct {
	MessageID string
	Bytes     []byte
}

type snapshot struct {
	binding    string
	limit      int
	entries    []Entry
	suppressed map[string]struct{}
	created    uint64
	generated  time.Time
	expires    time.Time
	bytes      int64
	positions  map[int]string
}

type cursorRef struct {
	snapshot *snapshot
	position int
}

// Snapshots stores bounded, repeatable listing snapshots and their opaque cursors.
type Snapshots struct {
	mu       sync.Mutex
	limits   SnapshotLimits
	now      func() time.Time
	random   io.Reader
	entries  map[*snapshot]struct{}
	cursors  map[string]cursorRef
	heldByte int64
	pending  int
}

// NewSnapshots creates a bounded snapshot manager.
func NewSnapshots(limits SnapshotLimits, now func() time.Time, random io.Reader) (*Snapshots, error) {
	limits = effectiveLimits(limits)
	if err := limits.Validate(); err != nil {
		return nil, err
	}
	if now == nil {
		now = time.Now
	}
	if random == nil {
		random = rand.Reader
	}
	return &Snapshots{
		limits:  limits,
		now:     now,
		random:  random,
		entries: make(map[*snapshot]struct{}),
		cursors: make(map[string]cursorRef),
	}, nil
}

// Limits returns the effective snapshot limits.
func (s *Snapshots) Limits() SnapshotLimits {
	return s.limits
}

// Start freezes matching visible entries and serves the first page.
func (s *Snapshots) Start(binding string, limit int, vis Visibility, produce func() ([]Entry, error)) ([]byte, error) {
	if produce == nil {
		return nil, ErrUnavailable
	}
	return s.StartBudgeted(binding, limit, vis, func(int64) ([]Entry, error) { return produce() })
}

// StartBudgeted is Start for a producer that stops early. produce receives the snapshot bytes still available
// and returns ErrBusy once its matches would exceed them, so a range is never materialized past the cap.
func (s *Snapshots) StartBudgeted(binding string, limit int, vis Visibility, produce func(budget int64) ([]Entry, error)) ([]byte, error) {
	s.mu.Lock()
	now := s.now().UTC()
	s.evictExpired(now)
	if limit < 1 || limit > maxMessagesPerPage || vis == nil || produce == nil {
		s.mu.Unlock()
		return nil, ErrUnavailable
	}
	if len(s.entries)+s.pending >= s.limits.MaxSnapshots || s.heldByte >= s.limits.MaxBytes {
		s.mu.Unlock()
		return nil, ErrBusy
	}
	budget := s.limits.MaxBytes - s.heldByte
	s.pending++
	s.mu.Unlock()

	created := vis.Generation()
	produced, err := produce(budget)
	if err != nil {
		s.releasePending()
		if errors.Is(err, ErrBusy) {
			return nil, ErrBusy
		}
		return nil, ErrUnavailable
	}
	frozen := make([]Entry, 0, len(produced))
	seen := make(map[string]struct{}, len(produced))
	var bytesHeld int64
	for _, entry := range produced {
		if _, ok := seen[entry.MessageID]; ok {
			continue
		}
		seen[entry.MessageID] = struct{}{}
		visible, _, err := vis.Visible(entry.MessageID)
		if err != nil {
			s.releasePending()
			return nil, ErrUnavailable
		}
		if !visible {
			continue
		}
		copyEntry := Entry{MessageID: entry.MessageID, Bytes: append([]byte(nil), entry.Bytes...)}
		frozen = append(frozen, copyEntry)
		bytesHeld += int64(len(copyEntry.Bytes))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending--
	s.evictExpired(s.now().UTC())
	if bytesHeld > s.limits.MaxBytes-s.heldByte {
		return nil, ErrBusy
	}
	generated := now.Truncate(time.Second)
	current := &snapshot{
		binding: binding, limit: limit, entries: frozen, suppressed: make(map[string]struct{}),
		created: created, generated: generated, expires: generated.Add(s.limits.Lifetime),
		bytes: bytesHeld, positions: make(map[int]string),
	}
	s.entries[current] = struct{}{}
	s.heldByte += bytesHeld
	page, err := s.page(current, 0, limit, vis)
	if err != nil {
		s.removeSnapshot(current)
		return nil, err
	}
	return page, nil
}

// Continue serves the page identified by a previously issued cursor.
func (s *Snapshots) Continue(binding, cursor string, limit int, vis Visibility) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now().UTC()
	s.evictExpired(now)
	ref, ok := s.cursors[cursor]
	if !ok || ref.snapshot.binding != binding || ref.snapshot.limit != limit || limit < 1 || limit > maxMessagesPerPage || vis == nil {
		return nil, ErrUnknownCursor
	}
	if !now.Before(ref.snapshot.expires) {
		return nil, ErrUnknownCursor
	}
	return s.page(ref.snapshot, ref.position, limit, vis)
}

func (s *Snapshots) page(current *snapshot, position, limit int, vis Visibility) ([]byte, error) {
	end := position
	messages := make([][]byte, 0, limit)
	pageBytes := discoveryPageFixedSize(current.generated, current.expires)
	for end < len(current.entries) && end-position < s.limits.ScanBudget && len(messages) < limit {
		entry := current.entries[end]
		visible, hiddenAt, err := vis.Visible(entry.MessageID)
		if err != nil {
			return nil, ErrUnavailable
		}
		if !visible || hiddenAt > current.created {
			current.suppressed[entry.MessageID] = struct{}{}
			end++
			continue
		}
		if _, suppressed := current.suppressed[entry.MessageID]; suppressed {
			end++
			continue
		}
		candidateBytes := pageBytes + len(entry.Bytes)
		if len(messages) > 0 {
			candidateBytes++
		}
		if candidateBytes > MaxPageBytes {
			if len(messages) == 0 {
				return nil, ErrUnavailable
			}
			break
		}
		messages = append(messages, entry.Bytes)
		pageBytes = candidateBytes
		end++
	}
	if end < len(current.entries) && end == position {
		return nil, ErrBusy
	}
	next := ""
	if end < len(current.entries) {
		var err error
		next, err = s.cursor(current, end)
		if err != nil {
			return nil, ErrUnavailable
		}
	}
	return EncodeDiscoveryPage(current.generated, current.expires, messages, next), nil
}

func (s *Snapshots) releasePending() {
	s.mu.Lock()
	s.pending--
	s.mu.Unlock()
}

func (s *Snapshots) cursor(current *snapshot, position int) (string, error) {
	if existing := current.positions[position]; existing != "" {
		return existing, nil
	}
	for {
		raw := make([]byte, cursorBytes)
		if _, err := io.ReadFull(s.random, raw); err != nil {
			return "", err
		}
		encoded := base64.RawURLEncoding.EncodeToString(raw)
		if _, exists := s.cursors[encoded]; exists {
			continue
		}
		current.positions[position] = encoded
		s.cursors[encoded] = cursorRef{snapshot: current, position: position}
		return encoded, nil
	}
}

func (s *Snapshots) evictExpired(now time.Time) {
	for current := range s.entries {
		if now.Before(current.expires) {
			continue
		}
		s.removeSnapshot(current)
	}
}

func (s *Snapshots) removeSnapshot(current *snapshot) {
	delete(s.entries, current)
	s.heldByte -= current.bytes
	for _, cursor := range current.positions {
		delete(s.cursors, cursor)
	}
}
