package message

import (
	"strings"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

// FinalSize returns the JCS byte length of the complete message using a fixed-length signature placeholder.
func FinalSize(i wire.Intent, r wire.SubmissionReceipt) (int, error) {
	receiptBytes, err := canon.Encode(r)
	if err != nil {
		return 0, invalidError("receipt encoding", err)
	}
	if len(receiptBytes) > MaxReceiptBytes {
		return 0, invalidError("receipt exceeds size limit", nil)
	}
	if _, err := keys.DecodeB64URL(r.ReceiptID, 32); err != nil {
		return 0, invalidError("receipt ID encoding", err)
	}
	id, err := ID(i)
	if err != nil {
		return 0, err
	}
	m := wire.PostMessage{
		MessageID: id, AuthorID: i.AuthorID, SignerID: i.SignerID, Delegation: i.Delegation,
		Timestamp: i.Timestamp, MessageType: i.MessageType, Version: i.Version,
		SubmissionReceipt: r, Signature: strings.Repeat("A", SignatureLength), Payload: i.Payload,
	}
	canonical, err := canon.Encode(m)
	if err != nil {
		return 0, invalidError("complete message encoding", err)
	}
	return len(canonical), nil
}

// CheckBudget rejects a complete message whose canonical final size exceeds MaxMessageBytes.
// Callers run it before debit and again when any receipt field changes.
func CheckBudget(i wire.Intent, r wire.SubmissionReceipt) error {
	size, err := FinalSize(i, r)
	if err != nil {
		return err
	}
	if size > MaxMessageBytes {
		return tooLargeError("complete message exceeds size limit", nil)
	}
	return nil
}
