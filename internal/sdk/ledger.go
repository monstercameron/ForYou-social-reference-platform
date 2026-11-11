package sdk

import (
	"bytes"
	"encoding/json"
	"errors"
	"sort"
	"sync"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/settlement"
)

// Ledger stores provider delinquency records independently of message retention.
type Ledger struct {
	mu      sync.RWMutex
	records map[settlement.DelinquencyRecord]bool
}

type vfLedgerRecord struct {
	ProviderLine string `json:"provider_line"`
	ReceiptID    string `json:"receipt_id"`
}

// NewLedger creates an empty delinquency ledger.
func NewLedger() *Ledger { return &Ledger{records: make(map[settlement.DelinquencyRecord]bool)} }

// Records returns a sorted snapshot of active delinquency records.
func (l *Ledger) Records() []settlement.DelinquencyRecord {
	if l == nil {
		return nil
	}
	l.mu.RLock()
	defer l.mu.RUnlock()
	out := make([]settlement.DelinquencyRecord, 0, len(l.records))
	for r, active := range l.records {
		if active {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].ProviderLine == out[j].ProviderLine {
			return out[i].ReceiptID < out[j].ReceiptID
		}
		return out[i].ProviderLine < out[j].ProviderLine
	})
	return out
}

// Snapshot returns a canonical JSON snapshot of active delinquency records.
func (l *Ledger) Snapshot() ([]byte, error) {
	records := l.Records()
	encoded := make([]vfLedgerRecord, 0, len(records))
	for _, record := range records {
		encoded = append(encoded, vfLedgerRecord{ProviderLine: record.ProviderLine, ReceiptID: record.ReceiptID})
	}
	return canon.Encode(encoded)
}

// RestoreLedger restores a canonical delinquency ledger snapshot.
func RestoreLedger(data []byte) (*Ledger, error) {
	value, err := canon.Parse(data, canon.Limits{MaxBytes: 1 << 20, MaxDepth: 8})
	if err != nil {
		return nil, err
	}
	canonical, err := canon.Marshal(value)
	if err != nil || !bytes.Equal(canonical, data) {
		return nil, errors.New("sdk: ledger snapshot is not canonical JSON")
	}
	if _, ok := value.([]any); !ok {
		return nil, errors.New("sdk: ledger snapshot must be an array")
	}
	var records []vfLedgerRecord
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&records); err != nil {
		return nil, err
	}
	l := NewLedger()
	for i, r := range records {
		if r.ProviderLine == "" || r.ReceiptID == "" {
			return nil, errors.New("sdk: invalid delinquency record")
		}
		if i > 0 && (records[i-1].ProviderLine > r.ProviderLine || records[i-1].ProviderLine == r.ProviderLine && records[i-1].ReceiptID >= r.ReceiptID) {
			return nil, errors.New("sdk: delinquency records are unsorted or duplicated")
		}
		l.records[settlement.DelinquencyRecord{ProviderLine: r.ProviderLine, ReceiptID: r.ReceiptID}] = true
	}
	return l, nil
}

func (l *Ledger) vfAdd(record settlement.DelinquencyRecord) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.records == nil {
		l.records = make(map[settlement.DelinquencyRecord]bool)
	}
	l.records[record] = true
}

func (l *Ledger) vfClear(receiptID string) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for r := range l.records {
		if r.ReceiptID == receiptID {
			delete(l.records, r)
		}
	}
}

func (l *Ledger) vfMap() map[settlement.DelinquencyRecord]bool {
	result := map[settlement.DelinquencyRecord]bool{}
	for _, r := range l.Records() {
		result[r] = true
	}
	return result
}
