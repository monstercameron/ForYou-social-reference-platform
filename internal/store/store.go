package store

import (
	"errors"
	"path/filepath"
)

var (
	// ErrClosed reports use of a closed store.
	ErrClosed = errors.New("store: closed")
	// ErrReadOnly reports a write attempted in a view transaction.
	ErrReadOnly = errors.New("store: write in read-only transaction")
	// ErrTxDone reports use of a transaction after its callback returned.
	ErrTxDone = errors.New("store: transaction finished")
	// ErrCorrupt reports invalid journal or snapshot data.
	ErrCorrupt = errors.New("store: corrupt journal")
	// ErrFailed reports a journal write failure requiring reopen.
	ErrFailed = errors.New("store: journal write failed; reopen required")
	// ErrInvalidName reports an invalid collection or counter name.
	ErrInvalidName = errors.New("store: invalid collection or counter name")
	// ErrInvalidKey reports an invalid key.
	ErrInvalidKey = errors.New("store: invalid key")
	// ErrTooLarge reports a value larger than MaxValueBytes.
	ErrTooLarge = errors.New("store: value too large")
)

// MaxValueBytes bounds one stored value.
const MaxValueBytes = 8 << 20

// Store holds named collections with atomic multi-key transactions.
type Store interface {
	// View reads one committed state without allowing writes.
	View(fn func(tx Tx) error) error
	// Update commits all transaction writes atomically.
	Update(fn func(tx Tx) error) error
	// Close releases store resources.
	Close() error
}

// Tx is one transaction; it is valid only during the function it was passed to.
type Tx interface {
	// Get returns a copy of one value, if present.
	Get(collection, key string) (value []byte, ok bool, err error)
	// Put stores a copy of one value.
	Put(collection, key string, value []byte) error
	// Delete removes one value.
	Delete(collection, key string) error
	// Scan visits matching keys in ascending byte order.
	Scan(collection string, r Range, fn func(key string, value []byte) (bool, error)) error
	// NextSequence reserves the next value for a counter in this transaction.
	NextSequence(counter string) (uint64, error)
}

// Range is the half-open key interval [Start, End); an empty End is unbounded.
type Range struct {
	// Start is the inclusive lower key boundary.
	Start string
	// End is the exclusive upper key boundary; empty means unbounded.
	End string
}

// Prefix returns a range containing keys beginning with p.
func Prefix(p string) Range { return Range{Start: p, End: p + "\xff"} }

// DataDir returns the role-specific development data directory.
func DataDir(root, role string) string { return filepath.Join(root, "data", role) }
