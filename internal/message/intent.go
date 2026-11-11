// Package message verifies signed posts and their admission receipts.
package message

import (
	"errors"
	"fmt"
	"time"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

const (
	// MaxMessageBytes is the maximum canonical complete message size.
	MaxMessageBytes = 1048576
	// MaxReceiptBytes is the maximum canonical submission receipt size.
	MaxReceiptBytes = 8192
	// SignatureLength is the encoded length of a canonical Ed25519 signature.
	SignatureLength = 86
)

var (
	// ErrIDMismatch is the public id_mismatch error.
	ErrIDMismatch = apierr.New(apierr.IDMismatch, errors.New("intent ID mismatch"))
	// ErrMessageTooLarge is the public message_too_large error.
	ErrMessageTooLarge = apierr.New(apierr.MessageTooLarge, errors.New("message exceeds size limit"))
	// ErrInvalidMessage is the public invalid_message error.
	ErrInvalidMessage = apierr.New(apierr.InvalidMessage, errors.New("invalid message"))
)

// IntentOf returns the signed intent, excluding message_id, signature and submission_receipt.
func IntentOf(m wire.PostMessage) wire.Intent {
	return wire.Intent{
		AuthorID: m.AuthorID, SignerID: m.SignerID, Delegation: m.Delegation,
		Timestamp: m.Timestamp, MessageType: m.MessageType, Version: m.Version, Payload: m.Payload,
	}
}

// ID returns the message ID for i using the intent signing domain.
func ID(i wire.Intent) (string, error) {
	canonical, err := canon.Encode(i)
	if err != nil {
		return "", invalidError("intent encoding", err)
	}
	id, err := keys.DigestID("fy1:", "intent", canonical)
	if err != nil {
		return "", invalidError("intent ID", err)
	}
	return id, nil
}

// ValidateIntent checks the intent schema, protocol timestamp and author/signer DIDs.
// Callers must also run content.ValidatePostContent for media and reference semantics.
func ValidateIntent(i wire.Intent) error {
	canonical, err := canon.Encode(i)
	if err != nil {
		return invalidError("intent encoding", err)
	}
	tree, err := canon.Parse(canonical, canon.MessageLimits)
	if errors.Is(err, canon.ErrTooLarge) {
		return tooLargeError("intent exceeds message budget", err)
	}
	if err != nil {
		return invalidError("intent JSON", err)
	}
	if err := wire.Check("AdmissionRequest/properties/intent", tree); err != nil {
		return invalidError("intent schema", err)
	}
	if _, err := wire.ParseTime(i.Timestamp); err != nil {
		return invalidError("intent timestamp", err)
	}
	if _, err := keys.ParseDID(i.AuthorID); err != nil {
		return invalidError("author DID", err)
	}
	if i.SignerID != "" {
		if _, err := keys.ParseDID(i.SignerID); err != nil {
			return invalidError("signer DID", err)
		}
	}
	return nil
}

// CheckTimestamp checks the inclusive admission window of 300 seconds past and 60 seconds future.
func CheckTimestamp(i wire.Intent, now time.Time) error {
	timestamp, err := wire.ParseTime(i.Timestamp)
	if err != nil {
		return invalidError("intent timestamp", err)
	}
	if timestamp.Before(now.Add(-300*time.Second)) || timestamp.After(now.Add(60*time.Second)) {
		return invalidError("intent timestamp outside admission window", nil)
	}
	return nil
}

// Assemble binds a receipt to an intent and returns the unsigned complete post.
func Assemble(i wire.Intent, r wire.SubmissionReceipt) (wire.PostMessage, error) {
	id, err := ID(i)
	if err != nil {
		return wire.PostMessage{}, err
	}
	if r.MessageID != id || r.AuthorID != i.AuthorID {
		return wire.PostMessage{}, invalidError("receipt intent binding", nil)
	}
	return wire.PostMessage{
		MessageID: id, AuthorID: i.AuthorID, SignerID: i.SignerID, Delegation: i.Delegation,
		Timestamp: i.Timestamp, MessageType: i.MessageType, Version: i.Version,
		SubmissionReceipt: r, Payload: i.Payload,
	}, nil
}

// Sign signs m after confirming that priv corresponds to its effective signer.
func Sign(m *wire.PostMessage, priv keys.PrivateKey) error {
	signer := m.AuthorID
	if m.SignerID != "" {
		signer = m.SignerID
	}
	if keys.DIDOf(priv) == "" || keys.DIDOf(priv) != signer {
		return invalidError("message signer does not match key", nil)
	}
	canonical, err := canon.EncodeWithout(*m, "signature")
	if err != nil {
		return invalidError("message signing input", err)
	}
	signature, err := keys.Sign(priv, "message", canonical)
	if err != nil {
		return invalidError("message signature", err)
	}
	m.Signature = signature
	return nil
}

func invalidError(reason string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", ErrInvalidMessage, reason)
	}
	return fmt.Errorf("%w: %s: %w", ErrInvalidMessage, reason, cause)
}

func tooLargeError(reason string, cause error) error {
	if cause == nil {
		return fmt.Errorf("%w: %s", ErrMessageTooLarge, reason)
	}
	return fmt.Errorf("%w: %s: %w", ErrMessageTooLarge, reason, cause)
}
