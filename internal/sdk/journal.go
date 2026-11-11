package sdk

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/wire"
)

// JournalEntry is the durable evidence and progress for one publication.
type JournalEntry struct {
	// MessageID identifies the signed intent this entry tracks.
	MessageID string `json:"message_id"`
	// State is one of the publication journal states.
	State string `json:"state"`
	// Draft is the original local content.
	Draft Draft `json:"draft"`
	// Prepared contains the frozen intent and local fee calculation.
	Prepared Prepared `json:"prepared"`
	// Quote contains the provider quote once obtained.
	Quote wire.FeeQuote `json:"quote,omitempty"`
	// QuoteRaw contains the exact provider quote response.
	QuoteRaw []byte `json:"quote_raw,omitempty"`
	// AdmissionRequest contains the exact authorized request bytes.
	AdmissionRequest []byte `json:"admission_request,omitempty"`
	// Receipt contains the verified admission receipt.
	Receipt wire.SubmissionReceipt `json:"receipt,omitempty"`
	// ReceiptRaw contains the exact provider receipt response.
	ReceiptRaw []byte `json:"receipt_raw,omitempty"`
	// FinalMessage contains the exact signed message bytes for retry.
	FinalMessage []byte `json:"final_message,omitempty"`
	// ProviderEndpoint is the pinned provider base URL used for requests.
	ProviderEndpoint string `json:"provider_endpoint"`
	// CreatedAt is the canonical UTC entry creation time.
	CreatedAt string `json:"created_at"`
	// UpdatedAt is the canonical UTC state update time.
	UpdatedAt string `json:"updated_at"`
	// Deadline is the receipt issuance time plus the preparation period.
	Deadline string `json:"deadline,omitempty"`
	// Rejection is the validated public error name when rejected.
	Rejection string `json:"rejection,omitempty"`
}

// Journal stores publication evidence and state durably.
type Journal interface {
	// Put atomically persists an entry.
	Put(JournalEntry) error
	// Get loads one entry by message ID.
	Get(string) (JournalEntry, bool, error)
	// List returns all retained entries in stable order.
	List() ([]JournalEntry, error)
}

type pbFileJournal struct {
	mu  sync.Mutex
	dir string
}

// FileJournal opens a directory-backed journal with atomic private file writes.
func FileJournal(dir string) (Journal, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0700); err != nil {
		return nil, err
	}
	return &pbFileJournal{dir: dir}, nil
}

func (j *pbFileJournal) Put(entry JournalEntry) error {
	if !pbValidMessageID(entry.MessageID) || !pbValidJournalState(entry.State) {
		return errors.New("sdk: invalid journal entry")
	}
	data, err := canon.Encode(entry)
	if err != nil {
		return err
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	tmp, err := os.CreateTemp(j.dir, ".publish-*.tmp")
	if err != nil {
		return err
	}
	tempName := tmp.Name()
	defer os.Remove(tempName)
	if err = tmp.Chmod(0600); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(tempName, filepath.Join(j.dir, entry.MessageID[4:]+".json")); err != nil {
		return err
	}
	return nil
}

func (j *pbFileJournal) Get(messageID string) (JournalEntry, bool, error) {
	if !pbValidMessageID(messageID) {
		return JournalEntry{}, false, errors.New("sdk: invalid message ID")
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	file, err := os.Open(filepath.Join(j.dir, messageID[4:]+".json"))
	if errors.Is(err, os.ErrNotExist) {
		return JournalEntry{}, false, nil
	}
	if err != nil {
		return JournalEntry{}, false, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 4<<20+1))
	if err != nil {
		return JournalEntry{}, false, err
	}
	if len(data) > 4<<20 {
		return JournalEntry{}, false, errors.New("sdk: publication journal entry exceeds limit")
	}
	var entry JournalEntry
	if _, err = canon.Parse(data, canon.Limits{MaxBytes: 4 << 20, MaxDepth: canon.MessageLimits.MaxDepth}); err != nil {
		return JournalEntry{}, false, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&entry); err != nil || entry.MessageID != messageID || !pbValidJournalState(entry.State) {
		return JournalEntry{}, false, errors.New("sdk: malformed publication journal entry")
	}
	return entry, true, nil
}

func (j *pbFileJournal) List() ([]JournalEntry, error) {
	j.mu.Lock()
	files, err := os.ReadDir(j.dir)
	j.mu.Unlock()
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(files))
	for _, file := range files {
		if file.IsDir() || !strings.HasSuffix(file.Name(), ".json") {
			continue
		}
		id := "fy1:" + strings.TrimSuffix(file.Name(), ".json")
		if pbValidMessageID(id) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	entries := make([]JournalEntry, 0, len(ids))
	for _, id := range ids {
		entry, ok, getErr := j.Get(id)
		if getErr != nil {
			return nil, getErr
		}
		if ok {
			entries = append(entries, entry)
		}
	}
	return entries, nil
}

func pbValidMessageID(id string) bool {
	if len(id) != 68 || !strings.HasPrefix(id, "fy1:") {
		return false
	}
	for _, char := range id[4:] {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func pbValidJournalState(state string) bool {
	switch state {
	case "prepared", "quoted", "authorized", "admitted", "signed", "accepted", "rejected":
		return true
	}
	return false
}
