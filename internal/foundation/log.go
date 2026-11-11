package foundation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/governance"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

var (
	// ErrNoValidCertificate means the Foundation key lacks a certificate at the object's signed time.
	ErrNoValidCertificate = errors.New("foundation: no valid certificate for signing key")
	// ErrUncertifiedFoundationKey means no configured Foundation certificate names the signing key.
	ErrUncertifiedFoundationKey = errors.New("foundation: configured key has no certificate")
	// ErrInvalidLogObject means a log kind and signing label do not identify a supported publication.
	ErrInvalidLogObject = errors.New("foundation: invalid log object kind or label")
)

func init() {
	backgroundLoops = append(backgroundLoops, runHeadLoop)
}

// SignAndLog signs a Foundation publication, validates it against current trust and appends it durably.
func (s *Service) SignAndLog(kind governance.LogKind, label string, obj any) ([]byte, wire.FoundationLogEntry, error) {
	if !validKindLabel(kind, label) {
		return nil, wire.FoundationLogEntry{}, ErrInvalidLogObject
	}
	s.appendMu.Lock()
	defer s.appendMu.Unlock()
	if label != "vault-receipt" {
		key, err := publicationKey(label, obj)
		if err != nil {
			return nil, wire.FoundationLogEntry{}, err
		}
		s.state.mu.RLock()
		_, alreadyPublished := s.state.statements[key]
		if label == "vault-status" {
			_, alreadyPublished = s.state.statuses[key]
		}
		s.state.mu.RUnlock()
		if alreadyPublished {
			return nil, wire.FoundationLogEntry{}, errors.New("foundation: publication already exists")
		}
	}
	signed, signedValue, signedAt, err := s.signObject(label, obj)
	if err != nil {
		return nil, wire.FoundationLogEntry{}, err
	}
	trust := s.Trust()
	if trust == nil {
		return nil, wire.FoundationLogEntry{}, errors.New("foundation: trust unavailable")
	}
	if err := trust.VerifyFoundation(signedValue, signedAt, label, nil); err != nil {
		if !s.keyCertifiedAt(signedAt) {
			return nil, wire.FoundationLogEntry{}, ErrNoValidCertificate
		}
		return nil, wire.FoundationLogEntry{}, fmt.Errorf("foundation: self-verification failed: %w", err)
	}
	hash := sha256.Sum256(signed)
	objectHash := hex.EncodeToString(hash[:])
	if index, ok := s.log.Index(objectHash); ok {
		entries := s.log.Entries(index-1, 1)
		if len(entries) == 1 {
			return signed, entries[0], nil
		}
	}
	entry := wire.FoundationLogEntry{Index: s.log.Len(), Kind: string(kind), ObjectHash: objectHash}
	err = s.db.Update(func(tx store.Tx) error {
		if err := tx.Put(objectsCollection, objectHash, signed); err != nil {
			return err
		}
		if err := putJSON(tx, entriesCollection, logIndexKey(entry.Index), entry); err != nil {
			return err
		}
		if label == "distribution-statement" {
			network := signedValue["network"].(string)
			periodEnd := signedValue["period_end"].(string)
			if err := tx.Put(statementsCollection, network+"|"+periodEnd, signed); err != nil {
				return err
			}
		} else if label == "vault-status" {
			periodEnd := signedValue["period_end"].(string)
			if err := tx.Put(statusesCollection, periodEnd, signed); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, wire.FoundationLogEntry{}, err
	}
	if _, added, err := s.log.Append(kind, objectHash); err != nil || !added {
		if err != nil {
			return nil, wire.FoundationLogEntry{}, err
		}
		return nil, wire.FoundationLogEntry{}, errors.New("foundation: log append unexpectedly duplicated")
	}
	s.state.mu.Lock()
	s.state.objects[objectHash] = append([]byte(nil), signed...)
	s.state.entries = append(s.state.entries, entry)
	if label == "distribution-statement" {
		s.state.statements[signedValue["network"].(string)+"|"+signedValue["period_end"].(string)] = append([]byte(nil), signed...)
	} else if label == "vault-status" {
		s.state.statuses[signedValue["period_end"].(string)] = append([]byte(nil), signed...)
	}
	s.state.mu.Unlock()
	return signed, entry, nil
}

// StoreVaultRecord persists one message and its signed receipt in the same journal transaction as its log entry.
func (s *Service) StoreVaultRecord(message []byte, proof wire.SettlementProof, storedAt time.Time) (VaultRecord, bool, error) {
	canonical, err := canon.Canonicalize(message, canon.Limits{MaxDepth: 32})
	if err != nil || string(canonical) != string(message) {
		return VaultRecord{}, false, errors.New("foundation: message must be exact JCS bytes")
	}
	value, err := canon.Parse(message, canon.Limits{MaxDepth: 32})
	if err != nil {
		return VaultRecord{}, false, err
	}
	messageObject, ok := value.(map[string]any)
	if !ok {
		return VaultRecord{}, false, errors.New("foundation: message must be an object")
	}
	messageID, ok := messageObject["message_id"].(string)
	if !ok || messageID == "" {
		return VaultRecord{}, false, errors.New("foundation: message_id is required")
	}
	submission, ok := messageObject["submission_receipt"].(map[string]any)
	if !ok {
		return VaultRecord{}, false, errors.New("foundation: submission_receipt is required")
	}
	settlementReceiptID, ok := submission["receipt_id"].(string)
	if !ok || settlementReceiptID == "" {
		return VaultRecord{}, false, errors.New("foundation: submission receipt_id is required")
	}
	s.appendMu.Lock()
	defer s.appendMu.Unlock()
	s.state.mu.RLock()
	if record, exists := s.state.vaultByMessage[messageID]; exists {
		s.state.mu.RUnlock()
		return cloneVaultRecord(record), false, nil
	}
	_, receiptHeld := s.state.vaultByReceipt[settlementReceiptID]
	s.state.mu.RUnlock()
	if receiptHeld {
		return VaultRecord{}, false, errors.New("foundation: settlement receipt ID already held for another message")
	}
	storedAt = storedAt.UTC().Truncate(time.Second)
	receipt := wire.VaultReceipt{FoundationID: keys.DIDOf(s.cfg.Key), MessageID: messageID, StoredAt: wire.FormatTime(storedAt)}
	signed, signedValue, signedTime, err := s.signObject("vault-receipt", receipt)
	if err != nil {
		return VaultRecord{}, false, err
	}
	if err := s.Trust().VerifyFoundation(signedValue, signedTime, "vault-receipt", nil); err != nil {
		return VaultRecord{}, false, fmt.Errorf("foundation: receipt self-verification: %w", err)
	}
	if err := json.Unmarshal(signed, &receipt); err != nil {
		return VaultRecord{}, false, err
	}
	hashBytes := sha256.Sum256(signed)
	receiptID := hex.EncodeToString(hashBytes[:])
	entry := wire.FoundationLogEntry{Index: s.log.Len(), Kind: string(governance.LogKindVaultReceipt), ObjectHash: receiptID}
	var record VaultRecord
	wasExisting := false
	err = s.db.Update(func(tx store.Tx) error {
		if data, exists, err := tx.Get(vaultCollection, messageID); err != nil {
			return err
		} else if exists {
			wasExisting = true
			return json.Unmarshal(data, &record)
		}
		sequence, err := tx.NextSequence(sequenceCounter)
		if err != nil {
			return err
		}
		record = VaultRecord{MessageID: messageID, Message: append([]byte(nil), message...), Proof: proof, Sequence: sequence, Receipt: receipt, SettlementReceiptID: settlementReceiptID, VaultReceiptHash: receiptID, StoredSize: int64(len(message))}
		if err := putJSON(tx, vaultCollection, messageID, record); err != nil {
			return err
		}
		if err := tx.Put(objectsCollection, receiptID, signed); err != nil {
			return err
		}
		if err := putJSON(tx, entriesCollection, logIndexKey(entry.Index), entry); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return VaultRecord{}, false, err
	}
	if wasExisting {
		return cloneVaultRecord(record), false, nil
	}
	if _, added, err := s.log.Append(governance.LogKindVaultReceipt, receiptID); err != nil || !added {
		if err != nil {
			return VaultRecord{}, false, err
		}
		return VaultRecord{}, false, errors.New("foundation: vault log append unexpectedly duplicated")
	}
	s.state.mu.Lock()
	s.state.objects[receiptID] = append([]byte(nil), signed...)
	s.state.entries = append(s.state.entries, entry)
	s.state.vaultByMessage[messageID] = cloneVaultRecord(record)
	s.state.vaultByReceipt[settlementReceiptID] = messageID
	s.state.mu.Unlock()
	return cloneVaultRecord(record), true, nil
}

// ReplaceVaultProof refreshes stored settlement witnesses without changing the sequence or receipt.
func (s *Service) ReplaceVaultProof(messageID string, proof wire.SettlementProof) error {
	s.appendMu.Lock()
	defer s.appendMu.Unlock()
	s.state.mu.RLock()
	record, ok := s.state.vaultByMessage[messageID]
	s.state.mu.RUnlock()
	if !ok {
		return errors.New("foundation: vault record not found")
	}
	record.Proof = proof
	if err := s.db.Update(func(tx store.Tx) error { return putJSON(tx, vaultCollection, messageID, record) }); err != nil {
		return err
	}
	s.state.mu.Lock()
	s.state.vaultByMessage[messageID] = cloneVaultRecord(record)
	s.state.mu.Unlock()
	return nil
}

// LatestHead returns the newest signed head, if one exists.
func (s *Service) LatestHead() (wire.FoundationLogHead, bool) {
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()
	if len(s.state.heads) == 0 {
		return wire.FoundationLogHead{}, false
	}
	return s.state.heads[len(s.state.heads)-1], true
}

func (s *Service) signObject(label string, obj any) ([]byte, map[string]any, time.Time, error) {
	unsigned, err := canon.EncodeWithout(obj, "foundation_signature")
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	value, err := canon.Parse(unsigned, canon.Limits{MaxDepth: 32})
	if err != nil {
		return nil, nil, time.Time{}, err
	}
	object, ok := value.(map[string]any)
	if !ok || object["foundation_id"] != keys.DIDOf(s.cfg.Key) {
		return nil, nil, time.Time{}, errors.New("foundation: object foundation_id does not match key")
	}
	field := timeField(label)
	timeText, ok := object[field].(string)
	if !ok {
		return nil, nil, time.Time{}, fmt.Errorf("foundation: missing %s", field)
	}
	signedAt, err := wire.ParseTime(timeText)
	if err != nil || signedAt.Nanosecond() != 0 {
		return nil, nil, time.Time{}, fmt.Errorf("foundation: invalid signed time %s", field)
	}
	trust := s.Trust()
	if trust == nil || !s.keyCertifiedAt(signedAt) {
		return nil, nil, signedAt, ErrNoValidCertificate
	}
	signature, err := keys.Sign(s.cfg.Key, label, unsigned)
	if err != nil {
		return nil, nil, signedAt, err
	}
	object["foundation_signature"] = signature
	signed, err := canon.Marshal(object)
	if err != nil {
		return nil, nil, signedAt, err
	}
	return signed, object, signedAt, nil
}

func validKindLabel(kind governance.LogKind, label string) bool {
	switch kind {
	case governance.LogKindVaultReceipt:
		return label == "vault-receipt"
	case governance.LogKindStatement:
		return label == "distribution-statement"
	case governance.LogKindVaultStatus:
		return label == "vault-status"
	default:
		return false
	}
}

func timeField(label string) string {
	switch label {
	case "vault-receipt":
		return "stored_at"
	case "distribution-statement", "vault-status":
		return "period_end"
	default:
		return ""
	}
}

func publicationKey(label string, obj any) (string, error) {
	unsigned, err := canon.EncodeWithout(obj, "foundation_signature")
	if err != nil {
		return "", err
	}
	value, err := canon.Parse(unsigned, canon.Limits{MaxDepth: 32})
	if err != nil {
		return "", err
	}
	object, ok := value.(map[string]any)
	if !ok {
		return "", errors.New("foundation: publication must be an object")
	}
	periodEnd, ok := object["period_end"].(string)
	if !ok || periodEnd == "" {
		return "", errors.New("foundation: missing period_end")
	}
	if label == "distribution-statement" {
		network, ok := object["network"].(string)
		if !ok || network == "" {
			return "", errors.New("foundation: missing network")
		}
		return network + "|" + periodEnd, nil
	}
	return periodEnd, nil
}

func putJSON(tx store.Tx, collection, key string, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return tx.Put(collection, key, data)
}

func logIndexKey(index int64) string { return fmt.Sprintf("%020d", index) }

func latestHeadSizeLocked(s *state) int64 {
	if len(s.heads) == 0 {
		return 0
	}
	return s.heads[len(s.heads)-1].Size
}

func runHeadLoop(ctx context.Context, s *Service) {
	ticker := time.NewTicker(s.cfg.HeadInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = s.headTick(s.Clock())
		}
	}
}

func (s *Service) headTick(now time.Time) (bool, error) {
	s.appendMu.Lock()
	defer s.appendMu.Unlock()
	s.state.mu.RLock()
	lastSize := latestHeadSizeLocked(s.state)
	s.state.mu.RUnlock()
	size := s.log.Len()
	if size == 0 || size <= lastSize {
		return false, nil
	}
	signedAt := now.UTC().Truncate(time.Second)
	trust := s.Trust()
	if trust == nil || !s.keyCertifiedAt(signedAt) {
		return false, ErrNoValidCertificate
	}
	root, err := s.log.Root(size)
	if err != nil {
		return false, err
	}
	head, err := governance.SignHead(s.cfg.Key, keys.DIDOf(s.cfg.Key), root, size, signedAt)
	if err != nil {
		return false, err
	}
	if err := trust.VerifyFoundation(head, signedAt, "foundation-log-head", nil); err != nil {
		return false, err
	}
	if err := s.db.Update(func(tx store.Tx) error { return putJSON(tx, headsCollection, logIndexKey(size), head) }); err != nil {
		return false, err
	}
	s.state.mu.Lock()
	s.state.heads = append(s.state.heads, head)
	s.state.mu.Unlock()
	return true, nil
}

func (s *Service) keyCertifiedAt(at time.Time) bool {
	id := keys.DIDOf(s.cfg.Key)
	certificates := append([]wire.FoundationKeyCertificate(nil), s.cfg.Genesis.FoundationKeyCertificates...)
	if s.cfg.Certificates != nil {
		certificates = append(certificates, s.cfg.Certificates(s.Clock())...)
	}
	for _, certificate := range certificates {
		if certificate.FoundationID != id {
			continue
		}
		from, fromErr := wire.ParseTime(certificate.ValidFrom)
		until, untilErr := wire.ParseTime(certificate.ValidUntil)
		if fromErr != nil || untilErr != nil || at.Before(from) || !at.Before(until) {
			continue
		}
		closed := false
		for _, closure := range s.cfg.Closures {
			if closure.FoundationID == id {
				closed = true
				break
			}
		}
		if !closed {
			return true
		}
	}
	return false
}
