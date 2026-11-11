package sdk

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/wire"
)

// FundingEventKind names a durable step in one funding attempt.
type FundingEventKind string

const (
	// FundingRequested records the approved request before it is sent.
	FundingRequested FundingEventKind = "requested"
	// FundingInvoiced records the provider invoice after local verification.
	FundingInvoiced FundingEventKind = "invoiced"
	// FundingTransferSent records only the network and transfer identity.
	FundingTransferSent FundingEventKind = "transfer_sent"
	// FundingObservedPending records a transfer that is not yet spendable.
	FundingObservedPending FundingEventKind = "observed_pending"
	// FundingCredited records a provider-signed committed credit.
	FundingCredited FundingEventKind = "credited"
	// FundingFailed records a definitive failed step.
	FundingFailed FundingEventKind = "failed"
)

// FundingEvent is one append-only funding state transition.
type FundingEvent struct {
	// Kind identifies the funding transition.
	Kind FundingEventKind `json:"kind"`
	// RequestID groups events for one client-generated idempotency key.
	RequestID string `json:"request_id"`
	// DepositID identifies the provider invoice when known.
	DepositID string `json:"deposit_id,omitempty"`
	// Network is the chain named by the event.
	Network string `json:"network,omitempty"`
	// AmountChainUnits is the native amount requested by the author.
	AmountChainUnits string `json:"amount_chain_units,omitempty"`
	// Transfer contains only chain network and transfer identity.
	Transfer *wire.DepositTransfer `json:"transfer,omitempty"`
	// Invoice is present only after the invoice has been verified.
	Invoice *wire.DepositInvoice `json:"invoice,omitempty"`
	// CreditID identifies the signed committed credit.
	CreditID string `json:"credit_id,omitempty"`
	// At is the canonical UTC event time.
	At string `json:"at"`
	// Error is the local failure detail, when a step failed definitively.
	Error string `json:"error,omitempty"`
}

// FundingAttempt is the folded durable state for one request_id.
type FundingAttempt struct {
	// RequestID is the first-seen request key.
	RequestID string
	// DepositID is the provider invoice ID, if issued.
	DepositID string
	// Network is the funding chain.
	Network string
	// AmountChainUnits is the requested native amount.
	AmountChainUnits string
	// Invoice is the verified provider invoice, if issued.
	Invoice *wire.DepositInvoice
	// Transfer is the transfer identity that must be reconciled.
	Transfer *wire.DepositTransfer
	// CreditID is the committed signed credit ID, if credited.
	CreditID string
	// Requested reports a durable request event.
	Requested bool
	// Invoiced reports a verified invoice event.
	Invoiced bool
	// TransferSent reports that a transfer identity was recorded before observation.
	TransferSent bool
	// ObservedPending reports that the provider has not yet credited the transfer.
	ObservedPending bool
	// Credited reports that a signed credit was verified.
	Credited bool
	// Failed reports a definitive failure after the request.
	Failed bool
	// LastError is the latest definitive failure detail.
	LastError string
}

// FundingLog durably records funding transitions so uncertain transfers can be reconciled safely.
type FundingLog interface {
	// Append persists one state transition before returning.
	Append(FundingEvent) error
	// Attempts returns events folded by request ID in first-seen order.
	Attempts() ([]FundingAttempt, error)
}

type acFileFundingLog struct {
	mu   sync.Mutex
	path string
}

// FileFundingLog opens an append-only JSON Lines log with private directory and file permissions.
func FileFundingLog(dir string) (FundingLog, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "funding.jsonl")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	if err = file.Chmod(0600); err != nil {
		file.Close()
		return nil, err
	}
	if err = file.Close(); err != nil {
		return nil, err
	}
	return &acFileFundingLog{path: path}, nil
}

func (l *acFileFundingLog) Append(event FundingEvent) error {
	if event.RequestID == "" || event.At == "" || !acValidEventKind(event.Kind) {
		return errors.New("sdk: invalid funding event")
	}
	encoded, err := canon.Encode(event)
	if err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer file.Close()
	if err = file.Chmod(0600); err != nil {
		return err
	}
	if _, err = file.Write(append(encoded, '\n')); err != nil {
		return err
	}
	return file.Sync()
}

func (l *acFileFundingLog) Attempts() ([]FundingAttempt, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	file, err := os.Open(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return []FundingAttempt{}, nil
		}
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 16<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 16<<20 {
		return nil, errors.New("sdk: funding log exceeds limit")
	}
	if len(data) != 0 && data[len(data)-1] != '\n' {
		data = data[:bytes.LastIndexByte(data, '\n')+1]
	}
	if len(data) == 0 {
		return []FundingAttempt{}, nil
	}
	attempts := make([]FundingAttempt, 0)
	indices := make(map[string]int)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if _, err = canon.Parse(line, canon.Limits{MaxBytes: 1 << 20, MaxDepth: canon.MessageLimits.MaxDepth}); err != nil {
			return nil, errors.New("sdk: malformed funding log line")
		}
		var event FundingEvent
		decoder := json.NewDecoder(bytes.NewReader(line))
		decoder.DisallowUnknownFields()
		if err = decoder.Decode(&event); err != nil || event.RequestID == "" || event.At == "" || !acValidEventKind(event.Kind) {
			return nil, errors.New("sdk: malformed funding log event")
		}
		index, exists := indices[event.RequestID]
		if !exists {
			index = len(attempts)
			indices[event.RequestID] = index
			attempts = append(attempts, FundingAttempt{RequestID: event.RequestID})
		}
		acFoldEvent(&attempts[index], event)
	}
	if err = scanner.Err(); err != nil {
		return nil, err
	}
	return attempts, nil
}

// NeedsReconciliation reports a sent transfer without a committed credit; the caller must observe that same transfer and never resend a deposit automatically after a timeout.
func NeedsReconciliation(attempt FundingAttempt) bool {
	return attempt.TransferSent && !attempt.Credited
}

func acValidEventKind(kind FundingEventKind) bool {
	switch kind {
	case FundingRequested, FundingInvoiced, FundingTransferSent, FundingObservedPending, FundingCredited, FundingFailed:
		return true
	}
	return false
}

func acFoldEvent(attempt *FundingAttempt, event FundingEvent) {
	switch event.Kind {
	case FundingRequested:
		attempt.Requested = true
		attempt.Network = event.Network
		attempt.AmountChainUnits = event.AmountChainUnits
	case FundingInvoiced:
		attempt.Invoiced = true
		attempt.DepositID = event.DepositID
		attempt.Invoice = event.Invoice
		attempt.Network = event.Network
		attempt.AmountChainUnits = event.AmountChainUnits
	case FundingTransferSent:
		attempt.TransferSent = true
		attempt.DepositID = event.DepositID
		attempt.Network = event.Network
		attempt.Transfer = event.Transfer
	case FundingObservedPending:
		attempt.ObservedPending = true
	case FundingCredited:
		attempt.Credited = true
		attempt.CreditID = event.CreditID
	case FundingFailed:
		attempt.Failed = true
		attempt.LastError = event.Error
	}
}

var _ FundingLog = (*acFileFundingLog)(nil)
