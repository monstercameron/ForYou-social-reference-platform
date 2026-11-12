package monitor

import (
	"bytes"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"sync"

	"foryou.local/foryou/internal/committee"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/store"
)

const monitorSnapshotKey = "state"

// Member is one hosted committee identity with its own state and journal.
type Member struct {
	index     int
	key       keys.PrivateKey
	did       string
	monitor   *committee.Monitor
	mu        sync.Mutex
	db        *store.DB
	schedules settlement.Schedules
	deposits  committee.DepositSource
	snapshot  []byte
}

func openMember(root string, index int, key keys.PrivateKey, schedules settlement.Schedules, deposits committee.DepositSource) (*Member, error) {
	if len(key) == 0 || keys.DIDOf(key) == "" {
		return nil, fmt.Errorf("monitor: invalid member key at index %d", index)
	}
	db, err := store.Open(filepath.Join(root, strconv.Itoa(index)))
	if err != nil {
		return nil, fmt.Errorf("monitor: open member %d journal: %w", index, err)
	}
	cfg := committee.Config{Key: key, Schedules: schedules, Deposits: deposits}
	data, ok := []byte(nil), false
	if err := db.View(func(tx store.Tx) error {
		var readErr error
		data, ok, readErr = tx.Get(monitorSnapshotCollection, monitorSnapshotKey)
		return readErr
	}); err != nil {
		_ = db.Close()
		return nil, err
	}
	var monitor *committee.Monitor
	if ok {
		monitor, err = committee.Restore(cfg, data)
	} else {
		monitor, err = committee.New(cfg)
	}
	if err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("monitor: restore member %d: %w", index, err)
	}
	if !ok {
		data, err = monitor.Snapshot()
		if err == nil {
			err = db.Update(func(tx store.Tx) error { return tx.Put(monitorSnapshotCollection, monitorSnapshotKey, data) })
		}
		if err != nil {
			_ = db.Close()
			return nil, err
		}
	}
	return &Member{index: index, key: append(keys.PrivateKey(nil), key...), did: keys.DIDOf(key), monitor: monitor, db: db, schedules: schedules, deposits: deposits, snapshot: append([]byte(nil), data...)}, nil
}

// Index returns this member's route number.
func (m *Member) Index() int { return m.index }

// DID returns this member's did:key identifier.
func (m *Member) DID() string { return m.did }

// DB returns this member's typed-collection database.
func (m *Member) DB() *store.DB { return m.db }

// Do serializes a committee state change and journals it before returning when the state changed.
func (m *Member) Do(fn func(*committee.Monitor) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if fn == nil {
		return errors.New("monitor: nil member operation")
	}
	if err := fn(m.monitor); err != nil {
		return errors.Join(err, m.restoreSnapshot())
	}
	data, err := m.monitor.Snapshot()
	if err != nil {
		return errors.Join(err, m.restoreSnapshot())
	}
	if bytes.Equal(data, m.snapshot) {
		return nil
	}
	err = m.db.Update(func(tx store.Tx) error { return tx.Put(monitorSnapshotCollection, monitorSnapshotKey, data) })
	if err != nil {
		return errors.Join(err, m.restoreSnapshot())
	}
	m.snapshot = append(m.snapshot[:0], data...)
	return nil
}

// View runs fn on a snapshot copy for reads without writing a journal snapshot.
// A change made inside fn is lost and corrupts nothing durable.
func (m *Member) View(fn func(*committee.Monitor) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if fn == nil {
		return errors.New("monitor: nil member view")
	}
	monitor, err := committee.Restore(committee.Config{Key: m.key, Schedules: m.schedules, Deposits: m.deposits}, m.snapshot)
	if err != nil {
		return fmt.Errorf("monitor: restore member %d for view: %w", m.index, err)
	}
	return fn(monitor)
}

// read runs fn on the live monitor under the member lock. fn must not change state; nothing is journaled.
func (m *Member) read(fn func(*committee.Monitor)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(m.monitor)
}

func (m *Member) restoreSnapshot() error {
	restored, err := committee.Restore(committee.Config{Key: m.key, Schedules: m.schedules, Deposits: m.deposits}, m.snapshot)
	if err != nil {
		return fmt.Errorf("monitor: restore member %d after failed operation: %w", m.index, err)
	}
	m.monitor = restored
	return nil
}

// Close flushes and closes this member's journal.
func (m *Member) Close() error {
	compactErr := m.db.Compact()
	closeErr := m.db.Close()
	return errors.Join(compactErr, closeErr)
}
