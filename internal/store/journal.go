package store

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
)

type journal struct {
	dir  string
	file *os.File
}

type journalOperation struct {
	Op string  `json:"op"`
	C  string  `json:"c"`
	K  string  `json:"k"`
	V  *string `json:"v,omitempty"`
}

type journalRecord struct {
	N   uint64             `json:"n"`
	Ops []journalOperation `json:"ops"`
	Sum string             `json:"sum"`
}

type encodedRecord struct {
	N   uint64          `json:"n"`
	Ops json.RawMessage `json:"ops"`
	Sum string          `json:"sum"`
}

type snapshot struct {
	N           uint64                       `json:"n"`
	Collections map[string]map[string]string `json:"collections"`
}

// Open opens a journaled store, creating dir with mode 0700 when needed. One
// process must own a directory at a time; ownership is not enforced.
func Open(dir string) (*DB, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	db := NewMemory()
	if err := db.loadSnapshot(dir); err != nil {
		return nil, err
	}
	journalPath := filepath.Join(dir, "journal.jsonl")
	if err := db.replay(journalPath); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(journalPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syncDir(dir); err != nil {
		file.Close()
		return nil, err
	}
	db.journal = &journal{dir: dir, file: file}
	return db, nil
}

func syncDir(dir string) error {
	if runtime.GOOS == "windows" {
		// Windows does not support syncing a directory handle.
		return nil
	}
	file, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func (db *DB) loadSnapshot(dir string) error {
	data, err := os.ReadFile(filepath.Join(dir, "snapshot.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var saved snapshot
	if err := json.Unmarshal(data, &saved); err != nil {
		return ErrCorrupt
	}
	if saved.Collections == nil {
		saved.Collections = make(map[string]map[string]string)
	}
	for collection, entries := range saved.Collections {
		if collection != sequenceCollection && !validCollection(collection) {
			return ErrCorrupt
		}
		for key, encoded := range entries {
			if validateKey(key) != nil {
				return ErrCorrupt
			}
			value, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil || len(value) > MaxValueBytes {
				return ErrCorrupt
			}
			if db.collections[collection] == nil {
				db.collections[collection] = make(map[string][]byte)
			}
			db.collections[collection][key] = value
		}
	}
	db.n = saved.N
	return nil
}

func (db *DB) replay(path string) error {
	snapshotN := db.n
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	start := 0
	for start < len(data) {
		end := start
		for end < len(data) && data[end] != '\n' {
			end++
		}
		terminated := end < len(data)
		line := data[start:end]
		if !terminated {
			return truncateTail(path, int64(start))
		}
		var record encodedRecord
		if err := json.Unmarshal(line, &record); err != nil {
			if end+1 == len(data) {
				return truncateTail(path, int64(start))
			}
			return ErrCorrupt
		}
		if record.N <= snapshotN {
			start = end + 1
			continue
		}
		var ops []journalOperation
		trimmedOps := bytes.TrimSpace(record.Ops)
		if len(trimmedOps) == 0 || trimmedOps[0] != '[' || json.Unmarshal(record.Ops, &ops) != nil || ops == nil || record.N != db.n+1 || !validChecksum(record.Ops, record.Sum) || db.applyReplay(ops) != nil {
			if end+1 == len(data) {
				return truncateTail(path, int64(start))
			}
			return ErrCorrupt
		}
		db.n = record.N
		start = end + 1
	}
	return nil
}

func truncateTail(path string, size int64) error {
	file, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	defer file.Close()
	if err := file.Truncate(size); err != nil {
		return err
	}
	return file.Sync()
}

func validChecksum(ops json.RawMessage, sum string) bool {
	digest := sha256.Sum256(ops)
	return hex.EncodeToString(digest[:]) == sum
}

func (db *DB) applyReplay(ops []journalOperation) error {
	decoded := make([]operation, 0, len(ops))
	for _, op := range ops {
		if op.C != sequenceCollection && !validCollection(op.C) || validateKey(op.K) != nil {
			return ErrCorrupt
		}
		switch op.Op {
		case "put":
			if op.V == nil {
				return ErrCorrupt
			}
			value, err := base64.StdEncoding.DecodeString(*op.V)
			if err != nil || base64.StdEncoding.EncodeToString(value) != *op.V || len(value) > MaxValueBytes {
				return ErrCorrupt
			}
			decoded = append(decoded, operation{Collection: op.C, Key: op.K, Value: value})
		case "delete":
			if op.V != nil {
				return ErrCorrupt
			}
			decoded = append(decoded, operation{Delete: true, Collection: op.C, Key: op.K})
		default:
			return ErrCorrupt
		}
	}
	for _, op := range decoded {
		if db.collections[op.Collection] == nil {
			db.collections[op.Collection] = make(map[string][]byte)
		}
		if op.Delete {
			delete(db.collections[op.Collection], op.Key)
		} else {
			db.collections[op.Collection][op.Key] = op.Value
		}
	}
	return nil
}

func (j *journal) append(n uint64, ops []operation) error {
	encoded := make([]journalOperation, 0, len(ops))
	for _, op := range ops {
		item := journalOperation{C: op.Collection, K: op.Key}
		if op.Delete {
			item.Op = "delete"
		} else {
			item.Op = "put"
			encodedValue := base64.StdEncoding.EncodeToString(op.Value)
			item.V = &encodedValue
		}
		encoded = append(encoded, item)
	}
	rawOps, err := json.Marshal(encoded)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(rawOps)
	line, err := json.Marshal(encodedRecord{N: n, Ops: rawOps, Sum: hex.EncodeToString(digest[:])})
	if err != nil {
		return err
	}
	line = append(line, '\n')
	if _, err := j.file.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	for len(line) > 0 {
		written, err := j.file.Write(line)
		if err != nil {
			return err
		}
		if written == 0 {
			return io.ErrShortWrite
		}
		line = line[written:]
	}
	return j.file.Sync()
}

func (j *journal) compact(n uint64, collections map[string]map[string][]byte) error {
	saved := snapshot{N: n, Collections: make(map[string]map[string]string, len(collections))}
	for collection, entries := range collections {
		savedEntries := make(map[string]string, len(entries))
		for key, value := range entries {
			savedEntries[key] = base64.StdEncoding.EncodeToString(value)
		}
		saved.Collections[collection] = savedEntries
	}
	data, err := json.Marshal(saved)
	if err != nil {
		return err
	}
	temporary := filepath.Join(j.dir, "snapshot.json.tmp")
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporary, filepath.Join(j.dir, "snapshot.json")); err != nil {
		return err
	}
	if err := syncDir(j.dir); err != nil {
		return err
	}
	if err := j.file.Truncate(0); err != nil {
		return err
	}
	if _, err := j.file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	if err := j.file.Sync(); err != nil {
		return err
	}
	return nil
}

func (j *journal) close() error {
	if j.file == nil {
		return fmt.Errorf("store: journal already closed")
	}
	err := j.file.Close()
	j.file = nil
	return err
}
