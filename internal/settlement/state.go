package settlement

import (
	"time"

	"foryou.local/foryou/internal/wire"
)

const pendingLimit = 7 * 24 * time.Hour

// State is a verifier's classification of a receipt's settlement status.
type State string

// The receipt states of Proofs and receipt states in docs/03-economics.md.
const (
	StateSettled   State = "settled"
	StatePending   State = "pending_settlement"
	StateUnsettled State = "unsettled"
)

// DelinquencyRecord identifies a receipt and its provider succession line.
type DelinquencyRecord struct{ ProviderLine, ReceiptID string }

// PendingInput contains local trust and source facts used for pending classification.
type PendingInput struct {
	Receipt           wire.SubmissionReceipt
	FromProviderRoute bool
	TrustedProvider   bool
	ProviderLine      string
}

// PendingState classifies an unsettled receipt using source, age, trust and local delinquency records.
func PendingState(in PendingInput, records map[DelinquencyRecord]bool, now time.Time) State {
	now = now.Truncate(time.Second)
	if !in.FromProviderRoute || !in.TrustedProvider {
		return StateUnsettled
	}
	issuedAt, err := wire.ParseTime(in.Receipt.IssuedAt)
	if err != nil || now.Before(issuedAt) {
		return StateUnsettled
	}
	age := now.Sub(issuedAt)
	if age >= pendingLimit {
		return StateUnsettled
	}
	for record, active := range records {
		if active && record.ProviderLine == in.ProviderLine {
			return StateUnsettled
		}
	}
	return StatePending
}

// Delinquent reports a provider-route receipt at least seven days old and its durable record key.
func Delinquent(in PendingInput, now time.Time) (DelinquencyRecord, bool) {
	now = now.Truncate(time.Second)
	if !in.FromProviderRoute {
		return DelinquencyRecord{}, false
	}
	issuedAt, err := wire.ParseTime(in.Receipt.IssuedAt)
	if err != nil || now.Before(issuedAt) || now.Sub(issuedAt) < pendingLimit {
		return DelinquencyRecord{}, false
	}
	return DelinquencyRecord{ProviderLine: in.ProviderLine, ReceiptID: in.Receipt.ReceiptID}, true
}
