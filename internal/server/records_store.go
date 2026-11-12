package server

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

// nrStoreLimit bounds the verified local copy; the oldest fetched records go first.
const nrStoreLimit = 20000

const nrCursorFile = "cursors.json"

// nrEntry is one verified record held in the local copy, with its exact bytes.
type nrEntry struct {
	Raw                []byte                `json:"raw"`
	Source             nrStoredSource        `json:"source"`
	Seen               []string              `json:"seen"`
	Settlement         string                `json:"settlement"`
	Proof              *wire.SettlementProof `json:"proof,omitempty"`
	SettledUnderPolicy string                `json:"settled_under_policy,omitempty"`
	RecheckBy          time.Time             `json:"recheck_by,omitzero"`
	FetchedAt          time.Time             `json:"fetched_at"`
	ProofCheckedAt     time.Time             `json:"proof_checked_at,omitzero"`
	// Mine marks a record this client published; it is never evicted.
	Mine    bool `json:"mine,omitempty"`
	message wire.TransportMessage
}

type nrStoredSource struct {
	Endpoint  string `json:"endpoint"`
	ServiceID string `json:"service_id"`
	Kind      int    `json:"kind"`
}

func (e *nrEntry) source() sdk.Source {
	return sdk.Source{Endpoint: e.Source.Endpoint, ServiceID: e.Source.ServiceID, Kind: sdk.SourceKind(e.Source.Kind)}
}

// nrCursor is the incremental-sync state of one source, keyed by its provider_id.
type nrCursor struct {
	NextAfter uint64    `json:"next_after"`
	SkipUntil time.Time `json:"skip_until,omitzero"`
	LastRun   time.Time `json:"last_run,omitzero"`
	Stored    int       `json:"stored"`
	Rejected  int       `json:"rejected"`
	More      bool      `json:"more"`
	Failure   string    `json:"failure,omitempty"`
}

type nrSuccessions struct {
	fetched time.Time
	items   []wire.KeySuccession
}

// nrStore is the persistent verified copy under {DataDir}/records/: one file per
// record named by the hex of its message ID, and the per-source sync cursors.
type nrStore struct {
	dir         string
	mu          sync.Mutex
	entries     map[string]*nrEntry
	cursors     map[string]nrCursor
	successions map[string]nrSuccessions
}

// nrOpenStore loads the local copy. Signatures are checked again lazily, when a
// record is next read, against the trust root in force then.
func nrOpenStore(dir string) (*nrStore, error) {
	s := &nrStore{dir: dir, entries: make(map[string]*nrEntry), cursors: make(map[string]nrCursor), successions: make(map[string]nrSuccessions)}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return s, err
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		return s, err
	}
	var failures []error
	for _, file := range files {
		name := file.Name()
		if name == nrCursorFile {
			data, err := os.ReadFile(filepath.Join(dir, name))
			if err == nil {
				err = json.Unmarshal(data, &s.cursors)
			}
			if err != nil {
				failures = append(failures, err)
			}
			continue
		}
		hexID, ok := strings.CutSuffix(name, ".json")
		if !ok || !canonicalMessageID("fy1:"+hexID) {
			continue
		}
		entry, err := nrReadEntry(filepath.Join(dir, name), "fy1:"+hexID)
		if err != nil {
			failures = append(failures, err)
			continue
		}
		s.entries["fy1:"+hexID] = entry
	}
	return s, errors.Join(failures...)
}

func nrReadEntry(path, id string) (*nrEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var entry nrEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return nil, err
	}
	if err := wire.Decode(entry.Raw, "TransportMessage", &entry.message); err != nil {
		return nil, err
	}
	if entry.message.MessageID != id {
		return nil, errors.New("stored record does not match its file name")
	}
	return &entry, nil
}

func (s *nrStore) path(id string) string {
	return filepath.Join(s.dir, strings.TrimPrefix(id, "fy1:")+".json")
}

// put stores a verified record, or adds the source to one already held. A
// record from its provider's own routes replaces a mirror or vault source,
// because only that origin can make a receipt pending; the held settlement
// state is replaced only by one judged from the held source, or by a proof.
func (s *nrStore) put(rec sdk.Record, now time.Time, mine bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, held := s.entries[rec.ID]
	changed := !held
	if !held {
		entry = &nrEntry{Raw: rec.Raw, message: rec.Message, FetchedAt: now}
		s.entries[rec.ID] = entry
	}
	if !held || entry.Source.Kind != int(sdk.SourceProvider) && rec.Source.Kind == sdk.SourceProvider {
		entry.Source = nrStoredSource{Endpoint: rec.Source.Endpoint, ServiceID: rec.Source.ServiceID, Kind: int(rec.Source.Kind)}
		changed = true
	}
	if !slices.Contains(entry.Seen, rec.Source.Endpoint) {
		entry.Seen = append(entry.Seen, rec.Source.Endpoint)
		changed = true
	}
	if mine && !entry.Mine {
		entry.Mine = true
		changed = true
	}
	judged := entry.Source.Endpoint == rec.Source.Endpoint || rec.Settlement == settlement.StateSettled
	if !held || judged && (string(rec.Settlement) != entry.Settlement || rec.Proof != nil && entry.Proof == nil) {
		nrApplySettlement(entry, rec)
		changed = true
	}
	if !changed {
		return nil
	}
	s.evict()
	return s.write(rec.ID, entry)
}

func nrApplySettlement(entry *nrEntry, rec sdk.Record) {
	entry.Settlement = string(rec.Settlement)
	entry.Proof = rec.Proof
	entry.SettledUnderPolicy = rec.SettledUnderPolicy
	entry.RecheckBy = rec.RecheckBy
}

// settle records a fresh settlement result and when proofs were last looked up.
func (s *nrStore) settle(rec sdk.Record, checkedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[rec.ID]
	if !ok {
		return nil
	}
	nrApplySettlement(entry, rec)
	entry.ProofCheckedAt = checkedAt
	return s.write(rec.ID, entry)
}

func (s *nrStore) write(id string, entry *nrEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	return saveLedger(s.path(id), data)
}

// evict drops the oldest fetched records over the limit, never one published here.
func (s *nrStore) evict() {
	for len(s.entries) > nrStoreLimit {
		oldest := ""
		for id, entry := range s.entries {
			if entry.Mine {
				continue
			}
			if oldest == "" || entry.FetchedAt.Before(s.entries[oldest].FetchedAt) {
				oldest = id
			}
		}
		if oldest == "" {
			return
		}
		delete(s.entries, oldest)
		_ = os.Remove(s.path(oldest))
	}
}

func (s *nrStore) remove(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.entries, id)
	_ = os.Remove(s.path(id))
}

// get returns a copy of one held entry.
func (s *nrStore) get(id string) (nrEntry, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.entries[id]
	if !ok {
		return nrEntry{}, false
	}
	return *entry, true
}

// list returns copies of the held entries that match, in signed-timestamp
// order, newest first, then by message ID.
func (s *nrStore) list(match func(*nrEntry) bool) []nrEntry {
	s.mu.Lock()
	out := make([]nrEntry, 0, len(s.entries))
	for _, entry := range s.entries {
		if match(entry) {
			out = append(out, *entry)
		}
	}
	s.mu.Unlock()
	slices.SortFunc(out, func(a, b nrEntry) int {
		if a.message.Timestamp != b.message.Timestamp {
			return strings.Compare(b.message.Timestamp, a.message.Timestamp)
		}
		return strings.Compare(a.message.MessageID, b.message.MessageID)
	})
	return out
}

func (s *nrStore) cursor(providerID string) nrCursor {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursors[providerID]
}

func (s *nrStore) setCursor(providerID string, c nrCursor) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cursors[providerID] = c
	data, err := json.Marshal(s.cursors)
	if err != nil {
		return err
	}
	return saveLedger(filepath.Join(s.dir, nrCursorFile), data)
}

func (s *nrStore) successionsDue(providerID string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	held, ok := s.successions[providerID]
	return !ok || !now.Before(held.fetched.Add(24*time.Hour))
}

// touchSuccessions marks an attempt, keeping what was held, so a failing
// provider is asked again only the next day.
func (s *nrStore) touchSuccessions(providerID string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	held := s.successions[providerID]
	held.fetched = now
	s.successions[providerID] = held
}

func (s *nrStore) setSuccessions(providerID string, items []wire.KeySuccession, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.successions[providerID] = nrSuccessions{fetched: now, items: items}
}

func (s *nrStore) allSuccessions() []wire.KeySuccession {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []wire.KeySuccession
	for _, held := range s.successions {
		out = append(out, held.items...)
	}
	return out
}
