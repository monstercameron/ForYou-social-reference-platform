package store

import (
	"encoding/json"
	"errors"
	"math"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"
)

const sequenceCollection = "_sequences"

type operation struct {
	Delete     bool
	Collection string
	Key        string
	Value      []byte
}

// DB implements Store in memory or backed by a journal.
type DB struct {
	mu          sync.RWMutex
	collections map[string]map[string][]byte
	journal     *journal
	n           uint64
	closed      bool
	failed      bool
}

// NewMemory returns an empty in-memory database.
func NewMemory() *DB { return &DB{collections: make(map[string]map[string][]byte)} }

// View runs a read-only transaction against one committed state. fn must not call View or Update on the same DB.
func (db *DB) View(fn func(tx Tx) error) error {
	if fn == nil {
		return errors.New("store: nil transaction function")
	}
	db.mu.RLock()
	defer db.mu.RUnlock()
	if db.closed {
		return ErrClosed
	}
	tx := &transaction{db: db, readOnly: true, active: true}
	defer tx.finish()
	return fn(tx)
}

// Update runs a serialized read-write transaction and publishes it only after
// durable commit. For idempotency, read the key inside one Update; when present,
// return its stored result, otherwise check, store the result and all effects
// (such as debit, receipt and prepared intent) together. Updates provide the
// only serialization; the store adds no implicit application-level locking.
// fn must not call View or Update on the same DB.
func (db *DB) Update(fn func(tx Tx) error) error {
	if fn == nil {
		return errors.New("store: nil transaction function")
	}
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return ErrClosed
	}
	if db.failed {
		return ErrFailed
	}
	tx := &transaction{db: db, writes: make(map[string]map[string]*[]byte), active: true}
	defer tx.finish()
	if err := fn(tx); err != nil {
		return err
	}
	ops := tx.operations()
	if len(ops) == 0 {
		return nil
	}
	if db.journal != nil {
		if err := db.journal.append(db.n+1, ops); err != nil {
			db.failed = true
			return err
		}
		db.n++
	}
	tx.apply()
	return nil
}

// Compact replaces the journal with a snapshot of the current state.
func (db *DB) Compact() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return ErrClosed
	}
	if db.failed {
		return ErrFailed
	}
	if db.journal == nil {
		return nil
	}
	return db.journal.compact(db.n, db.collections)
}

// Close releases the journal file and prevents later operations.
func (db *DB) Close() error {
	db.mu.Lock()
	defer db.mu.Unlock()
	if db.closed {
		return ErrClosed
	}
	db.closed = true
	if db.journal != nil {
		return db.journal.close()
	}
	return nil
}

type transaction struct {
	db       *DB
	writes   map[string]map[string]*[]byte
	readOnly bool
	active   bool
}

func (tx *transaction) finish() { tx.active = false }

func (tx *transaction) check() error {
	if !tx.active {
		return ErrTxDone
	}
	return nil
}

func validCollection(name string) bool {
	if len(name) == 0 || len(name) > 128 || name[0] == '_' {
		return false
	}
	parts := strings.Split(name, ".")
	if len(parts) < 2 {
		return false
	}
	for _, part := range parts {
		if len(part) == 0 || part[0] < 'a' || part[0] > 'z' {
			return false
		}
		for _, ch := range part[1:] {
			if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '_' {
				return false
			}
		}
	}
	return true
}

func validateKey(key string) error {
	if len(key) == 0 || len(key) > 1024 || !utf8.ValidString(key) {
		return ErrInvalidKey
	}
	return nil
}

func (tx *transaction) validate(collection, key string) error {
	if !validCollection(collection) {
		return ErrInvalidName
	}
	return validateKey(key)
}

func (tx *transaction) getInternal(collection, key string) ([]byte, bool, error) {
	if err := validateKey(key); err != nil {
		return nil, false, err
	}
	if tx.writes != nil {
		if entries := tx.writes[collection]; entries != nil {
			if value, ok := entries[key]; ok {
				if value == nil {
					return nil, false, nil
				}
				return clone(*value), true, nil
			}
		}
	}
	value, ok := tx.db.collections[collection][key]
	return clone(value), ok, nil
}

func (tx *transaction) putInternal(collection, key string, value []byte) error {
	if err := validateKey(key); err != nil {
		return err
	}
	if len(value) > MaxValueBytes {
		return ErrTooLarge
	}
	if tx.writes[collection] == nil {
		tx.writes[collection] = make(map[string]*[]byte)
	}
	copyValue := clone(value)
	tx.writes[collection][key] = &copyValue
	return nil
}

func clone(value []byte) []byte { return append([]byte(nil), value...) }

func (tx *transaction) Get(collection, key string) ([]byte, bool, error) {
	if err := tx.check(); err != nil {
		return nil, false, err
	}
	if !validCollection(collection) {
		return nil, false, ErrInvalidName
	}
	return tx.getInternal(collection, key)
}

func (tx *transaction) Put(collection, key string, value []byte) error {
	if err := tx.check(); err != nil {
		return err
	}
	if tx.readOnly {
		return ErrReadOnly
	}
	if !validCollection(collection) {
		return ErrInvalidName
	}
	return tx.putInternal(collection, key, value)
}

func (tx *transaction) Delete(collection, key string) error {
	if err := tx.check(); err != nil {
		return err
	}
	if tx.readOnly {
		return ErrReadOnly
	}
	if err := tx.validate(collection, key); err != nil {
		return err
	}
	if tx.writes[collection] == nil {
		tx.writes[collection] = make(map[string]*[]byte)
	}
	tx.writes[collection][key] = nil
	return nil
}

func (tx *transaction) Scan(collection string, r Range, fn func(key string, value []byte) (bool, error)) error {
	if err := tx.check(); err != nil {
		return err
	}
	if !validCollection(collection) {
		return ErrInvalidName
	}
	if fn == nil {
		return errors.New("store: nil scan function")
	}
	keys := make(map[string]struct{})
	for key := range tx.db.collections[collection] {
		keys[key] = struct{}{}
	}
	if entries := tx.writes[collection]; entries != nil {
		for key, value := range entries {
			if value == nil {
				delete(keys, key)
			} else {
				keys[key] = struct{}{}
			}
		}
	}
	ordered := make([]string, 0, len(keys))
	for key := range keys {
		if key >= r.Start && (r.End == "" || key < r.End) {
			ordered = append(ordered, key)
		}
	}
	sort.Strings(ordered)
	for _, key := range ordered {
		value, _, err := tx.Get(collection, key)
		if err != nil {
			return err
		}
		more, err := fn(key, value)
		if err != nil || !more {
			return err
		}
	}
	return nil
}

func (tx *transaction) NextSequence(counter string) (uint64, error) {
	if err := tx.check(); err != nil {
		return 0, err
	}
	if tx.readOnly {
		return 0, ErrReadOnly
	}
	if !validCollection(counter) {
		return 0, ErrInvalidName
	}
	data, ok, err := tx.getInternal(sequenceCollection, counter)
	if err != nil {
		return 0, err
	}
	var current uint64
	if ok {
		if err := json.Unmarshal(data, &current); err != nil {
			return 0, ErrCorrupt
		}
	}
	if current == math.MaxUint64 {
		return 0, errors.New("store: sequence exhausted")
	}
	next := current + 1
	encoded, err := json.Marshal(next)
	if err != nil {
		return 0, err
	}
	if err := tx.putInternal(sequenceCollection, counter, encoded); err != nil {
		return 0, err
	}
	return next, nil
}

func (tx *transaction) operations() []operation {
	ops := make([]operation, 0)
	collections := make([]string, 0, len(tx.writes))
	for collection := range tx.writes {
		collections = append(collections, collection)
	}
	sort.Strings(collections)
	for _, collection := range collections {
		keys := make([]string, 0, len(tx.writes[collection]))
		for key := range tx.writes[collection] {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			value := tx.writes[collection][key]
			if value == nil {
				ops = append(ops, operation{Delete: true, Collection: collection, Key: key})
			} else {
				ops = append(ops, operation{Collection: collection, Key: key, Value: clone(*value)})
			}
		}
	}
	return ops
}

func (tx *transaction) apply() {
	for collection, entries := range tx.writes {
		if tx.db.collections[collection] == nil {
			tx.db.collections[collection] = make(map[string][]byte)
		}
		for key, value := range entries {
			if value == nil {
				delete(tx.db.collections[collection], key)
			} else {
				tx.db.collections[collection][key] = clone(*value)
			}
		}
	}
}
