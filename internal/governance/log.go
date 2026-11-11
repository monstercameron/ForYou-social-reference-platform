package governance

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

const logMaxSafeInteger int64 = 1<<53 - 1

// LogKind identifies a type of object recorded in the Foundation log.
type LogKind string

const (
	// LogKindVaultReceipt identifies a logged vault receipt.
	LogKindVaultReceipt LogKind = "vault_receipt"
	// LogKindStatement identifies a logged distribution statement.
	LogKindStatement LogKind = "statement"
	// LogKindVaultStatus identifies a logged vault status report.
	LogKindVaultStatus LogKind = "vault_status"
)

// ErrNotFound indicates that an object hash is not in the requested log tree.
var ErrNotFound = errors.New("governance: log object not found")

// Log stores Foundation object hashes in append order.
type Log struct {
	mu      sync.RWMutex
	entries []wire.FoundationLogEntry
	indices map[string]int64
}

// NewLog reloads a log journal after validating its indices, kinds, and hashes.
func NewLog(entries []wire.FoundationLogEntry) (*Log, error) {
	log := &Log{entries: make([]wire.FoundationLogEntry, 0, len(entries)), indices: make(map[string]int64, len(entries))}
	for i, entry := range entries {
		if int64(i) > logMaxSafeInteger || entry.Index != int64(i) {
			return nil, fmt.Errorf("governance: log index %d is not sequential", entry.Index)
		}
		if !logValidKind(LogKind(entry.Kind)) || !logValidHash(entry.ObjectHash) {
			return nil, fmt.Errorf("governance: invalid log entry at index %d", entry.Index)
		}
		if _, ok := log.indices[entry.ObjectHash]; ok {
			return nil, fmt.Errorf("governance: duplicate log object hash at index %d", entry.Index)
		}
		log.entries = append(log.entries, entry)
		log.indices[entry.ObjectHash] = entry.Index
	}
	return log, nil
}

// Append records an object hash once and returns its entry and whether it was added.
func (l *Log) Append(kind LogKind, objectHash string) (wire.FoundationLogEntry, bool, error) {
	if !logValidKind(kind) {
		return wire.FoundationLogEntry{}, false, fmt.Errorf("governance: invalid log kind %q", kind)
	}
	if !logValidHash(objectHash) {
		return wire.FoundationLogEntry{}, false, errors.New("governance: invalid object hash")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if index, ok := l.indices[objectHash]; ok {
		return l.entries[index], false, nil
	}
	if int64(len(l.entries)) >= logMaxSafeInteger {
		return wire.FoundationLogEntry{}, false, errors.New("governance: log size exceeds safe integer limit")
	}
	entry := wire.FoundationLogEntry{Index: int64(len(l.entries)), Kind: string(kind), ObjectHash: objectHash}
	l.entries = append(l.entries, entry)
	l.indices[objectHash] = entry.Index
	return entry, true, nil
}

// Len returns the number of logged objects.
func (l *Log) Len() int64 {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return int64(len(l.entries))
}

// Entries returns at most limit entries after after, in index order.
func (l *Log) Entries(after int64, limit int) []wire.FoundationLogEntry {
	if after < -1 || limit <= 0 {
		return []wire.FoundationLogEntry{}
	}
	if limit > 1000 {
		limit = 1000
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	if after >= int64(len(l.entries))-1 {
		return []wire.FoundationLogEntry{}
	}
	start := after + 1
	end := start + int64(limit)
	if end < start || end > int64(len(l.entries)) {
		end = int64(len(l.entries))
	}
	return append([]wire.FoundationLogEntry(nil), l.entries[int(start):int(end)]...)
}

// Index returns the index of an object hash when it is present.
func (l *Log) Index(objectHash string) (int64, bool) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	index, ok := l.indices[objectHash]
	return index, ok
}

// Root returns the RFC 6962 Merkle Tree Hash of the first size entries.
func (l *Log) Root(size int64) (string, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if size < 1 || size > logMaxSafeInteger || size > int64(len(l.entries)) {
		return "", errors.New("governance: invalid log root size")
	}
	return logTreeRoot(logEntryHashes(l.entries[:int(size)]))
}

// Proof returns the RFC 6962 audit path for an object in the first size entries.
func (l *Log) Proof(objectHash string, size int64) (wire.FoundationLogProof, error) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if size < 1 || size > logMaxSafeInteger || size > int64(len(l.entries)) {
		return wire.FoundationLogProof{}, errors.New("governance: invalid proof size")
	}
	index, ok := l.indices[objectHash]
	if !ok || index >= size {
		return wire.FoundationLogProof{}, ErrNotFound
	}
	path, err := logAuditPath(int(index), logEntryHashes(l.entries[:int(size)]))
	if err != nil || len(path) > 64 {
		return wire.FoundationLogProof{}, errors.New("governance: invalid audit path")
	}
	return wire.FoundationLogProof{Index: index, Size: size, AuditPath: path}, nil
}

// SignHead signs a Foundation log head with the key matching foundationID.
// A head of size 0 must carry 64 zeros as root, following log_head in the reference model.
func SignHead(key ed25519.PrivateKey, foundationID string, root string, size int64, signedAt time.Time) (wire.FoundationLogHead, error) {
	if keys.DIDOf(key) == "" || keys.DIDOf(key) != foundationID {
		return wire.FoundationLogHead{}, errors.New("governance: foundation ID does not match signing key")
	}
	if size < 0 || size > logMaxSafeInteger || !logValidHash(root) {
		return wire.FoundationLogHead{}, errors.New("governance: invalid log head root or size")
	}
	// The specification leaves the empty root undefined; this follows log_head in tests/economics_model.py.
	if size == 0 && root != strings.Repeat("0", 64) {
		return wire.FoundationLogHead{}, errors.New("governance: empty log head must use zero root")
	}
	signedTime := wire.FormatTime(signedAt)
	if _, err := wire.ParseTime(signedTime); err != nil {
		return wire.FoundationLogHead{}, fmt.Errorf("governance: invalid signed time: %w", err)
	}
	head := wire.FoundationLogHead{FoundationID: foundationID, Size: size, Root: root, SignedAt: signedTime}
	canonical, err := canon.EncodeWithout(head, "foundation_signature")
	if err != nil {
		return wire.FoundationLogHead{}, err
	}
	head.FoundationSignature, err = keys.Sign(key, "foundation-log-head", canonical)
	if err != nil {
		return wire.FoundationLogHead{}, err
	}
	return head, nil
}

// TreeRoot returns the RFC 6962 Merkle Tree Hash of object hashes.
func TreeRoot(hashes []string) (string, error) {
	return logTreeRoot(hashes)
}

// HeadsConsistent checks that each list's first head.Size hashes reproduce its root and that the
// shorter head's hashes are a prefix of the longer head's hashes. Lists may contain later entries.
func HeadsConsistent(a, b []string, headA, headB wire.FoundationLogHead) bool {
	if !logHeadHashesMatch(a, headA) || !logHeadHashesMatch(b, headB) {
		return false
	}
	compareSize := headA.Size
	if headB.Size < compareSize {
		compareSize = headB.Size
	}
	for i := int64(0); i < compareSize; i++ {
		if a[int(i)] != b[int(i)] {
			return false
		}
	}
	return true
}

func logHeadHashesMatch(hashes []string, head wire.FoundationLogHead) bool {
	if head.Size < 0 || head.Size > logMaxSafeInteger || int64(len(hashes)) < head.Size || !logValidHash(head.Root) {
		return false
	}
	if head.Size == 0 {
		return head.Root == strings.Repeat("0", 64)
	}
	root, err := logTreeRoot(hashes[:int(head.Size)])
	return err == nil && root == head.Root
}

func logTreeRoot(hashes []string) (string, error) {
	if len(hashes) == 0 || int64(len(hashes)) > logMaxSafeInteger {
		return "", errors.New("governance: tree must contain a safe positive number of leaves")
	}
	for _, hash := range hashes {
		if !logValidHash(hash) {
			return "", errors.New("governance: invalid tree leaf hash")
		}
	}
	return hex.EncodeToString(logMerkleTreeHash(hashes)), nil
}

func logMerkleTreeHash(hashes []string) []byte {
	if len(hashes) == 1 {
		return logLeafHash(hashes[0])
	}
	split := logTreeSplit(len(hashes))
	return logNodeHash(logMerkleTreeHash(hashes[:split]), logMerkleTreeHash(hashes[split:]))
}

func logAuditPath(index int, hashes []string) ([]string, error) {
	if index < 0 || index >= len(hashes) || len(hashes) == 0 {
		return nil, errors.New("governance: audit path index out of range")
	}
	if len(hashes) == 1 {
		return []string{}, nil
	}
	split := logTreeSplit(len(hashes))
	if index < split {
		path, err := logAuditPath(index, hashes[:split])
		if err != nil {
			return nil, err
		}
		return append(path, hex.EncodeToString(logMerkleTreeHash(hashes[split:]))), nil
	}
	path, err := logAuditPath(index-split, hashes[split:])
	if err != nil {
		return nil, err
	}
	return append(path, hex.EncodeToString(logMerkleTreeHash(hashes[:split]))), nil
}

func logTreeSplit(size int) int {
	split := 1
	for split*2 < size {
		split *= 2
	}
	return split
}

func logLeafHash(objectHash string) []byte {
	decoded, _ := hex.DecodeString(objectHash)
	input := make([]byte, 1+len(decoded))
	copy(input[1:], decoded)
	digest := sha256.Sum256(input)
	return digest[:]
}

func logNodeHash(left, right []byte) []byte {
	input := make([]byte, 1, 1+len(left)+len(right))
	input[0] = 1
	input = append(input, left...)
	input = append(input, right...)
	digest := sha256.Sum256(input)
	return digest[:]
}

func logEntryHashes(entries []wire.FoundationLogEntry) []string {
	hashes := make([]string, len(entries))
	for i, entry := range entries {
		hashes[i] = entry.ObjectHash
	}
	return hashes
}

func logValidKind(kind LogKind) bool {
	return kind == LogKindVaultReceipt || kind == LogKindStatement || kind == LogKindVaultStatus
}

func logValidHash(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
