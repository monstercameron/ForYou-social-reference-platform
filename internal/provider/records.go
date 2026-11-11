package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"foryou.local/foryou/internal/discovery"
	"foryou.local/foryou/internal/retention"
	"foryou.local/foryou/internal/store"
)

var (
	// ErrIDCollision identifies distinct intents sharing one message ID.
	ErrIDCollision = errors.New("provider: distinct intents share a message ID")
	// ErrRemoved identifies a message permanently removed by a control.
	ErrRemoved = errors.New("provider: record removed by a control")
	// ErrNotExpired identifies a record whose retention deadline has not arrived.
	ErrNotExpired = errors.New("provider: retain_until not reached")
	// ErrRetentionShortened identifies an attempt to move a deadline earlier.
	ErrRetentionShortened = errors.New("provider: retain_until cannot move earlier")
)

// Acceptance is a fully verified message ready for the durable acceptance commit.
type Acceptance struct {
	// MessageID is the content-derived message identifier.
	MessageID string
	// Bytes is the exact JCS encoding of the complete signed message.
	Bytes []byte
	// IntentDigest is SHA-256 of the exact canonical intent.
	IntentDigest [32]byte
	// ReceiptID binds this acceptance to its provider receipt.
	ReceiptID string
	// IssuedAt is the receipt issuance time.
	IssuedAt time.Time
}

// Accepted reports the assigned sequence and whether this call created the record.
type Accepted struct {
	// Sequence is the original acceptance sequence.
	Sequence uint64
	// Created is true only when this call committed a new record.
	Created bool
}

// RecordState is the public retrieval state of a message record.
type RecordState string

const (
	// RecordRetained is a message currently held for public retrieval.
	RecordRetained RecordState = "retained"
	// RecordExpired is a message whose retention deadline has passed.
	RecordExpired RecordState = "expired"
	// RecordRemoved is a message removed by a durable control.
	RecordRemoved RecordState = "removed"
	// RecordUnknown is an ID absent from the record index.
	RecordUnknown RecordState = "unknown"
)

type recordMeta struct {
	MessageID    string      `json:"message_id"`
	Sequence     uint64      `json:"sequence"`
	ReceiptID    string      `json:"receipt_id"`
	IssuedAt     string      `json:"issued_at"`
	IntentSHA256 string      `json:"intent_sha256"`
	AcceptedAt   string      `json:"accepted_at"`
	RetainUntil  *string     `json:"retain_until"`
	State        RecordState `json:"state"`
}

// Records owns durable acceptance state and the in-memory discovery index.
type Records struct {
	mu      sync.RWMutex
	content store.Store
	index   *discovery.Index
	now     func() time.Time
	owner   *Service
	states  map[string]recordMeta
}

// Restriction is the public-read control applied to a retained record.
type Restriction string

const (
	// Unrestricted permits public delivery.
	Unrestricted Restriction = ""
	// PolicyRestricted reports a policy restriction.
	PolicyRestricted Restriction = "policy_restricted"
	// LegalRestricted reports a legal restriction.
	LegalRestricted Restriction = "legal_restricted"
	// Restricted reports a disclosable restriction.
	Restricted Restriction = "restricted"
	// Concealed hides whether the message exists.
	Concealed Restriction = "not_found"
)

// ControlState reports durable restrictions and control-store recovery readiness.
type ControlState interface {
	// Restriction returns the durable visibility status for a message.
	Restriction(messageID string) (Restriction, error)
	// Ready reports whether acknowledged control state is recoverable.
	Ready() error
}

type allowControls struct{}

func (allowControls) Restriction(string) (Restriction, error) { return Unrestricted, nil }
func (allowControls) Ready() error                            { return nil }

// SetControls installs the compliance control-state implementation during route registration.
func (s *Service) SetControls(c ControlState) {
	if !s.registering {
		panic("provider: controls may only be installed during registration")
	}
	s.controlsState = c
	if c == nil {
		s.controlsState = allowControls{}
	}
}

// Restriction returns the current restriction or an error when control state is uncertain.
func (s *Service) Restriction(id string) (Restriction, error) { return s.controlsState.Restriction(id) }

// MarkHidden advances snapshot visibility after a restriction, expiry, or removal.
func (s *Service) MarkHidden(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.gen++
	s.visibility.hidden[id] = s.gen
}

type visibility struct {
	service *Service
	hidden  map[string]uint64
}

func (v *visibility) Generation() uint64 {
	v.service.mu.Lock()
	defer v.service.mu.Unlock()
	return v.service.gen
}

func (v *visibility) Visible(id string) (bool, uint64, error) {
	st := v.service.records.state(id)
	v.service.mu.Lock()
	hidden := v.hidden[id]
	v.service.mu.Unlock()
	if st != RecordRetained {
		return false, hidden, nil
	}
	restriction, err := v.service.Restriction(id)
	if err != nil {
		return false, hidden, err
	}
	return restriction == Unrestricted, hidden, nil
}

// Accept durably stores a verified message and indexes it only after the commit succeeds.
// An existing record is reported as held (Created false, original sequence) only when its
// bytes are stored and readable: a retained record whose bytes are missing is repaired from
// a.Bytes in the same transaction first, and a failed repair returns the error (503). An
// expired record is reported with its sequence without restoring bytes, since its retention
// obligation has ended. A removed record returns ErrRemoved and is never restored.
func (r *Records) Accept(a Acceptance) (Accepted, error) {
	parsed, err := discovery.ParseRecord(a.Bytes, 1)
	if err != nil {
		return Accepted{}, err
	}
	if parsed.MessageID != a.MessageID {
		return Accepted{}, fmt.Errorf("provider: message ID mismatch")
	}
	var out Accepted
	var add discovery.Record
	var committed recordMeta
	r.mu.Lock()
	defer r.mu.Unlock()
	err = r.content.Update(func(tx store.Tx) error {
		if raw, ok, e := tx.Get("provider.records", a.MessageID); e != nil {
			return e
		} else if ok {
			var m recordMeta
			if e = json.Unmarshal(raw, &m); e != nil {
				return e
			}
			if m.State == RecordRemoved {
				return ErrRemoved
			}
			if m.IntentSHA256 != hexDigest(a.IntentDigest) {
				return ErrIDCollision
			}
			if m.State == RecordExpired {
				out = Accepted{Sequence: m.Sequence}
				return nil
			}
			b, exists, e := tx.Get("provider.message_bytes", a.MessageID)
			if e != nil {
				return e
			}
			if !exists || len(b) == 0 {
				if e = tx.Put("provider.message_bytes", a.MessageID, a.Bytes); e != nil {
					return e
				}
				add, e = discovery.ParseRecord(a.Bytes, m.Sequence)
				if e != nil {
					return e
				}
			}
			committed = m
			out = Accepted{Sequence: m.Sequence}
			return nil
		}
		seq, e := tx.NextSequence("provider.acceptance")
		if e != nil {
			return e
		}
		if seq > 1<<53-1 {
			return fmt.Errorf("provider: acceptance sequence exceeds safe integer")
		}
		m := recordMeta{MessageID: a.MessageID, Sequence: seq, ReceiptID: a.ReceiptID, IssuedAt: a.IssuedAt.UTC().Format(time.RFC3339), IntentSHA256: hexDigest(a.IntentDigest), AcceptedAt: r.now().UTC().Format(time.RFC3339), State: RecordRetained}
		encoded, e := json.Marshal(m)
		if e != nil {
			return e
		}
		if e = tx.Put("provider.message_bytes", a.MessageID, a.Bytes); e != nil {
			return e
		}
		if e = tx.Put("provider.records", a.MessageID, encoded); e != nil {
			return e
		}
		if e = tx.Put("provider.sequence", fmt.Sprintf("%020d", seq), []byte(a.MessageID)); e != nil {
			return e
		}
		committed = m
		add, e = discovery.ParseRecord(a.Bytes, seq)
		if e != nil {
			return e
		}
		out = Accepted{Sequence: seq, Created: true}
		return nil
	})
	if err != nil {
		return Accepted{}, err
	}
	if out.Created || add.MessageID != "" {
		r.states[a.MessageID] = committed
		r.index.Add(add)
	}
	return out, nil
}

// Lookup returns exact stored message bytes and the durable record state.
func (r *Records) Lookup(id string) ([]byte, RecordState, error) {
	var b []byte
	state := RecordUnknown
	err := r.content.View(func(tx store.Tx) error {
		raw, ok, e := tx.Get("provider.records", id)
		if e != nil {
			return e
		}
		if !ok {
			return nil
		}
		var m recordMeta
		if e = json.Unmarshal(raw, &m); e != nil {
			return e
		}
		state = m.State
		if state == RecordRetained {
			var exists bool
			b, exists, e = tx.Get("provider.message_bytes", id)
			if e != nil {
				return e
			}
			if !exists {
				return errors.New("provider: retained record bytes unavailable")
			}
			return nil
		}
		return nil
	})
	return b, state, err
}

// Select returns records matching a discovery query.
func (r *Records) Select(q discovery.Query) []discovery.Record { return r.index.Select(q) }

// After returns indexed records after an acceptance sequence.
func (r *Records) After(after uint64, max int) []discovery.Record { return r.index.After(after, max) }

// MaxSequence returns the greatest acceptance sequence, including removed records.
func (r *Records) MaxSequence() uint64 { return r.index.MaxSequence() }

// SetRetainUntil durably sets a record's first claim-backed expiry.
func (r *Records) SetRetainUntil(id string, t time.Time) error {
	return r.changeMeta(id, func(m *recordMeta) error {
		v := t.UTC().Truncate(time.Second).Format(time.RFC3339)
		if m.RetainUntil != nil {
			old, _ := time.Parse(time.RFC3339, *m.RetainUntil)
			next, _ := time.Parse(time.RFC3339, v)
			if next.Before(old) {
				return ErrRetentionShortened
			}
			if next.Equal(old) {
				return nil
			}
		}
		m.RetainUntil = &v
		return nil
	})
}

// DueForExpiry lists retained records whose declared retention has ended.
func (r *Records) DueForExpiry(now time.Time) []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var ids []string
	for id, m := range r.states {
		if m.State == RecordRetained && m.RetainUntil != nil {
			t, e := time.Parse(time.RFC3339, *m.RetainUntil)
			if e == nil && retention.Expired(t, now) {
				ids = append(ids, id)
			}
		}
	}
	sort.Slice(ids, func(i, j int) bool { return r.states[ids[i]].Sequence < r.states[ids[j]].Sequence })
	return ids
}

// Expire removes bytes after the retention instant and hides the record from discovery.
func (r *Records) Expire(id string, now time.Time) error {
	return r.removeState(id, RecordExpired, func(m recordMeta) error {
		if m.State != RecordRetained || m.RetainUntil == nil {
			return ErrNotExpired
		}
		t, e := time.Parse(time.RFC3339, *m.RetainUntil)
		if e != nil || !retention.Expired(t, now) {
			return ErrNotExpired
		}
		return nil
	})
}

// Remove permanently removes message bytes after a durable legal-removal control.
func (r *Records) Remove(id string) error {
	return r.removeState(id, RecordRemoved, func(m recordMeta) error { return nil })
}
func (r *Records) changeMeta(id string, change func(*recordMeta) error) error {
	var updated recordMeta
	r.mu.Lock()
	defer r.mu.Unlock()
	err := r.content.Update(func(tx store.Tx) error {
		b, ok, e := tx.Get("provider.records", id)
		if e != nil {
			return e
		}
		if !ok {
			return errors.New("provider: record not found")
		}
		var m recordMeta
		if e = json.Unmarshal(b, &m); e != nil {
			return e
		}
		if e = change(&m); e != nil {
			return e
		}
		b, e = json.Marshal(m)
		if e != nil {
			return e
		}
		if e = tx.Put("provider.records", id, b); e != nil {
			return e
		}
		updated = m
		return nil
	})
	if err != nil {
		return err
	}
	r.states[id] = updated
	return nil
}
func (r *Records) removeState(id string, state RecordState, check func(recordMeta) error) error {
	var updated recordMeta
	changed := false
	r.mu.Lock()
	defer r.mu.Unlock()
	err := r.content.Update(func(tx store.Tx) error {
		b, ok, e := tx.Get("provider.records", id)
		if e != nil {
			return e
		}
		var m recordMeta
		if !ok {
			if state != RecordRemoved {
				return check(recordMeta{State: RecordUnknown})
			}
			m = recordMeta{MessageID: id, State: RecordRemoved}
		} else if e = json.Unmarshal(b, &m); e != nil {
			return e
		}
		if m.State == RecordRemoved && ok {
			updated = m
			return nil
		}
		if e = check(m); e != nil {
			return e
		}
		m.State = state
		b, e = json.Marshal(m)
		if e != nil {
			return e
		}
		if e = tx.Put("provider.records", id, b); e != nil {
			return e
		}
		if e = tx.Delete("provider.message_bytes", id); e != nil {
			return e
		}
		updated = m
		changed = true
		return nil
	})
	if err == nil {
		if changed {
			r.states[id] = updated
			r.index.Remove(id)
			r.owner.MarkHidden(id)
		}
	}
	return err
}
func (r *Records) state(id string) RecordState {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if m, ok := r.states[id]; ok {
		return m.State
	}
	return RecordUnknown
}
func hexDigest(b [32]byte) string { return fmt.Sprintf("%x", b) }
func sequenceValue(key string) uint64 {
	n, _ := strconv.ParseUint(strings.TrimLeft(key, "0"), 10, 64)
	return n
}
