package foundation

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

const (
	objectsCollection    = "foundation.objects"
	entriesCollection    = "foundation.entries"
	headsCollection      = "foundation.heads"
	vaultCollection      = "foundation.vault"
	statementsCollection = "foundation.statements"
	statusesCollection   = "foundation.statuses"
	reportsCollection    = "foundation.reports"
	claimsCollection     = "foundation.claims"
	payoutsCollection    = "foundation.payouts"
	plansCollection      = "foundation.plans"
	metaCollection       = "foundation.meta"
	sequenceCounter      = "foundation.vault_sequence"
	firstStartedKey      = "first_started"
)

// VaultRecord is one retained message, its settlement evidence, receipt and acceptance sequence.
type VaultRecord struct {
	MessageID           string               `json:"message_id"`
	Message             []byte               `json:"message"`
	Proof               wire.SettlementProof `json:"proof"`
	Sequence            uint64               `json:"sequence"`
	Receipt             wire.VaultReceipt    `json:"receipt"`
	SettlementReceiptID string               `json:"settlement_receipt_id"`
	VaultReceiptHash    string               `json:"vault_receipt_hash"`
	StoredSize          int64                `json:"stored_size"`
}

// QualityReportSet stores reports used for one provider and quarter.
type QualityReportSet struct {
	ProviderID string               `json:"provider_id"`
	QuarterEnd string               `json:"quarter_end"`
	Reports    []wire.QualityReport `json:"reports"`
}

// DistributionTransferID identifies one payout within a distribution quarter.
type DistributionTransferID struct {
	Network       string `json:"network"`
	QuarterEnd    string `json:"quarter_end"`
	RecipientKind string `json:"recipient_kind"`
	RecipientID   string `json:"recipient_id"`
}

type state struct {
	mu             sync.RWMutex
	db             *store.DB
	objects        map[string][]byte
	entries        []wire.FoundationLogEntry
	heads          []wire.FoundationLogHead
	vaultByMessage map[string]VaultRecord
	vaultByReceipt map[string]string
	statements     map[string][]byte
	statuses       map[string][]byte
	reports        map[string]QualityReportSet
	claims         map[string]json.RawMessage
	payouts        map[string]json.RawMessage
	plans          map[string]json.RawMessage
	firstStarted   time.Time
}

func openState(db *store.DB, now time.Time) (*state, error) {
	s := &state{
		db: db, objects: make(map[string][]byte), vaultByMessage: make(map[string]VaultRecord),
		vaultByReceipt: make(map[string]string), statements: make(map[string][]byte),
		statuses: make(map[string][]byte), reports: make(map[string]QualityReportSet),
		claims: make(map[string]json.RawMessage), payouts: make(map[string]json.RawMessage),
		plans: make(map[string]json.RawMessage),
	}
	err := db.Update(func(tx store.Tx) error {
		data, ok, err := tx.Get(metaCollection, firstStartedKey)
		if err != nil {
			return err
		}
		if !ok {
			s.firstStarted = now.UTC()
			data, err = json.Marshal(s.firstStarted)
			if err != nil {
				return err
			}
			return tx.Put(metaCollection, firstStartedKey, data)
		}
		return json.Unmarshal(data, &s.firstStarted)
	})
	if err != nil {
		return nil, err
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *state) load() error {
	return s.db.View(func(tx store.Tx) error {
		if err := tx.Scan(objectsCollection, store.Prefix(""), func(key string, value []byte) (bool, error) {
			s.objects[key] = append([]byte(nil), value...)
			return true, nil
		}); err != nil {
			return err
		}
		if err := tx.Scan(entriesCollection, store.Prefix(""), func(_ string, value []byte) (bool, error) {
			var entry wire.FoundationLogEntry
			if err := json.Unmarshal(value, &entry); err != nil {
				return false, err
			}
			s.entries = append(s.entries, entry)
			return true, nil
		}); err != nil {
			return err
		}
		sort.Slice(s.entries, func(i, j int) bool { return s.entries[i].Index < s.entries[j].Index })
		if err := tx.Scan(headsCollection, store.Prefix(""), func(_ string, value []byte) (bool, error) {
			var head wire.FoundationLogHead
			if err := json.Unmarshal(value, &head); err != nil {
				return false, err
			}
			s.heads = append(s.heads, head)
			return true, nil
		}); err != nil {
			return err
		}
		sort.Slice(s.heads, func(i, j int) bool { return s.heads[i].Size < s.heads[j].Size })
		if err := tx.Scan(vaultCollection, store.Prefix(""), func(key string, value []byte) (bool, error) {
			var record VaultRecord
			if err := json.Unmarshal(value, &record); err != nil {
				return false, err
			}
			s.vaultByMessage[key] = record
			s.vaultByReceipt[record.SettlementReceiptID] = key
			return true, nil
		}); err != nil {
			return err
		}
		if err := tx.Scan(statementsCollection, store.Prefix(""), func(key string, value []byte) (bool, error) {
			s.statements[key] = append([]byte(nil), value...)
			return true, nil
		}); err != nil {
			return err
		}
		if err := tx.Scan(statusesCollection, store.Prefix(""), func(key string, value []byte) (bool, error) {
			s.statuses[key] = append([]byte(nil), value...)
			return true, nil
		}); err != nil {
			return err
		}
		if err := tx.Scan(reportsCollection, store.Prefix(""), func(key string, value []byte) (bool, error) {
			var reports QualityReportSet
			if err := json.Unmarshal(value, &reports); err != nil {
				return false, err
			}
			s.reports[key] = reports
			return true, nil
		}); err != nil {
			return err
		}
		if err := tx.Scan(claimsCollection, store.Prefix(""), func(key string, value []byte) (bool, error) {
			s.claims[key] = append(json.RawMessage(nil), value...)
			return true, nil
		}); err != nil {
			return err
		}
		if err := tx.Scan(payoutsCollection, store.Prefix(""), func(key string, value []byte) (bool, error) {
			s.payouts[key] = append(json.RawMessage(nil), value...)
			return true, nil
		}); err != nil {
			return err
		}
		if err := tx.Scan(plansCollection, store.Prefix(""), func(key string, value []byte) (bool, error) {
			s.plans[key] = append(json.RawMessage(nil), value...)
			return true, nil
		}); err != nil {
			return err
		}
		return nil
	})
}

// FirstStarted returns the persisted time this Foundation data directory first started.
func (s *Service) FirstStarted() time.Time { return s.state.firstStarted }

// LogObject returns the exact stored JCS bytes for a logged object hash.
func (s *Service) LogObject(hash string) ([]byte, bool) {
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()
	value, ok := s.state.objects[hash]
	return append([]byte(nil), value...), ok
}

// LogEntries returns entries after index in append order.
func (s *Service) LogEntries(after int64, limit int) []wire.FoundationLogEntry {
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()
	if after < -1 || limit <= 0 {
		return []wire.FoundationLogEntry{}
	}
	start := after + 1
	if start >= int64(len(s.state.entries)) {
		return []wire.FoundationLogEntry{}
	}
	end := start + int64(limit)
	if end > int64(len(s.state.entries)) || end < start {
		end = int64(len(s.state.entries))
	}
	return append([]wire.FoundationLogEntry(nil), s.state.entries[int(start):int(end)]...)
}

// SignedHeads returns every persisted signed log head in increasing size order.
func (s *Service) SignedHeads() []wire.FoundationLogHead {
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()
	return append([]wire.FoundationLogHead(nil), s.state.heads...)
}

// VaultRecord returns the retained record for a message ID.
func (s *Service) VaultRecord(messageID string) (VaultRecord, bool) {
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()
	record, ok := s.state.vaultByMessage[messageID]
	return cloneVaultRecord(record), ok
}

// VaultRecordByReceiptID returns a record by its settlement receipt ID.
func (s *Service) VaultRecordByReceiptID(receiptID string) (VaultRecord, bool) {
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()
	messageID, ok := s.state.vaultByReceipt[receiptID]
	if !ok {
		return VaultRecord{}, false
	}
	return cloneVaultRecord(s.state.vaultByMessage[messageID]), true
}

// VaultRecords returns all retained records in acceptance sequence order.
func (s *Service) VaultRecords() []VaultRecord {
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()
	result := make([]VaultRecord, 0, len(s.state.vaultByMessage))
	for _, record := range s.state.vaultByMessage {
		result = append(result, cloneVaultRecord(record))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Sequence < result[j].Sequence })
	return result
}

func cloneVaultRecord(record VaultRecord) VaultRecord {
	record.Message = append([]byte(nil), record.Message...)
	record.Proof.AuditPath = append([]wire.AuditNode(nil), record.Proof.AuditPath...)
	record.Proof.Witnesses = append([]wire.SettlementWitness(nil), record.Proof.Witnesses...)
	record.Proof.Chain = append([]wire.SettlementClaim(nil), record.Proof.Chain...)
	cloneClaimPointers(&record.Proof.Claim)
	for i := range record.Proof.Chain {
		cloneClaimPointers(&record.Proof.Chain[i])
	}
	return record
}

func cloneClaimPointers(claim *wire.SettlementClaim) {
	if claim.PreviousClaimID != nil {
		value := *claim.PreviousClaimID
		claim.PreviousClaimID = &value
	}
	if claim.SuccessionID != nil {
		value := *claim.SuccessionID
		claim.SuccessionID = &value
	}
}

// DistributionStatement returns the signed statement bytes for a network and quarter end.
func (s *Service) DistributionStatement(network, periodEnd string) ([]byte, bool) {
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()
	value, ok := s.state.statements[network+"|"+periodEnd]
	return append([]byte(nil), value...), ok
}

// VaultStatus returns the signed vault status bytes for a quarter end.
func (s *Service) VaultStatus(periodEnd string) ([]byte, bool) {
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()
	value, ok := s.state.statuses[periodEnd]
	return append([]byte(nil), value...), ok
}

// QualityReports returns the reports stored for a provider and quarter.
func (s *Service) QualityReports(providerID, quarterEnd string) []wire.QualityReport {
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()
	reports := s.state.reports[providerID+"|"+quarterEnd].Reports
	result := append([]wire.QualityReport(nil), reports...)
	for i := range result {
		result[i].Sponsors = append([]string(nil), reports[i].Sponsors...)
		cloneQualityPointers(&result[i])
	}
	return result
}

// StoreQualityReports records the report set used for one provider and quarter.
func (s *Service) StoreQualityReports(set QualityReportSet) error {
	key := set.ProviderID + "|" + set.QuarterEnd
	copySet := QualityReportSet{ProviderID: set.ProviderID, QuarterEnd: set.QuarterEnd, Reports: append([]wire.QualityReport(nil), set.Reports...)}
	for i := range set.Reports {
		copySet.Reports[i].Sponsors = append([]string(nil), set.Reports[i].Sponsors...)
		cloneQualityPointers(&copySet.Reports[i])
	}
	if err := s.db.Update(func(tx store.Tx) error { return putJSON(tx, reportsCollection, key, copySet) }); err != nil {
		return err
	}
	s.state.mu.Lock()
	s.state.reports[key] = copySet
	s.state.mu.Unlock()
	return nil
}

func cloneQualityPointers(report *wire.QualityReport) {
	if report.LatencyP50Ms != nil {
		value := *report.LatencyP50Ms
		report.LatencyP50Ms = &value
	}
	if report.LatencyP95Ms != nil {
		value := *report.LatencyP95Ms
		report.LatencyP95Ms = &value
	}
	if report.LatencyP99Ms != nil {
		value := *report.LatencyP99Ms
		report.LatencyP99Ms = &value
	}
	if report.ReplicaOperators != nil {
		value := *report.ReplicaOperators
		report.ReplicaOperators = &value
	}
	if report.IndependentControllers != nil {
		value := *report.IndependentControllers
		report.IndependentControllers = &value
	}
}

// ClaimCounted reports whether a claim has already been counted by the distribution job.
func (s *Service) ClaimCounted(claimID string) bool {
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()
	_, ok := s.state.claims[claimID]
	return ok
}

// CountedClaim returns the stored claim bytes when the distribution job counted claimID.
func (s *Service) CountedClaim(claimID string) (json.RawMessage, bool) {
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()
	value, ok := s.state.claims[claimID]
	return append(json.RawMessage(nil), value...), ok
}

// MarkClaimCounted records a claim in the distribution job ledger exactly once.
func (s *Service) MarkClaimCounted(claimID string, claim json.RawMessage) (bool, error) {
	if len(claim) == 0 || !json.Valid(claim) {
		return false, fmt.Errorf("foundation: invalid claim JSON")
	}
	added := false
	err := s.db.Update(func(tx store.Tx) error {
		if _, ok, err := tx.Get(claimsCollection, claimID); err != nil || ok {
			return err
		}
		if err := tx.Put(claimsCollection, claimID, claim); err != nil {
			return err
		}
		added = true
		return nil
	})
	if err != nil {
		return false, err
	}
	if added {
		s.state.mu.Lock()
		s.state.claims[claimID] = append(json.RawMessage(nil), claim...)
		s.state.mu.Unlock()
	}
	return added, nil
}

// PayoutMade reports whether the ledger already records a transfer for id.
func (s *Service) PayoutMade(id DistributionTransferID) bool {
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()
	_, ok := s.state.payouts[payoutKey(id)]
	return ok
}

// PayoutTransfer returns the recorded transfer for a distribution payout identity.
func (s *Service) PayoutTransfer(id DistributionTransferID) (json.RawMessage, bool) {
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()
	value, ok := s.state.payouts[payoutKey(id)]
	return append(json.RawMessage(nil), value...), ok
}

// MarkPayoutMade records a payout transfer exactly once.
func (s *Service) MarkPayoutMade(id DistributionTransferID, transfer json.RawMessage) (bool, error) {
	if len(transfer) == 0 || !json.Valid(transfer) {
		return false, fmt.Errorf("foundation: invalid transfer JSON")
	}
	key := payoutKey(id)
	added := false
	err := s.db.Update(func(tx store.Tx) error {
		if _, ok, err := tx.Get(payoutsCollection, key); err != nil || ok {
			return err
		}
		if err := tx.Put(payoutsCollection, key, transfer); err != nil {
			return err
		}
		added = true
		return nil
	})
	if err != nil {
		return false, err
	}
	if added {
		s.state.mu.Lock()
		s.state.payouts[key] = append(json.RawMessage(nil), transfer...)
		s.state.mu.Unlock()
	}
	return added, nil
}

// FrozenPlan returns the distribution inputs frozen for one network and quarter.
func (s *Service) FrozenPlan(network, quarterEnd string) (json.RawMessage, bool) {
	s.state.mu.RLock()
	defer s.state.mu.RUnlock()
	value, ok := s.state.plans[network+"|"+quarterEnd]
	return append(json.RawMessage(nil), value...), ok
}

// FreezePlan records the distribution inputs for one network and quarter exactly
// once and returns the recorded inputs, which are the first ones frozen.
func (s *Service) FreezePlan(network, quarterEnd string, plan json.RawMessage) (json.RawMessage, error) {
	if len(plan) == 0 || !json.Valid(plan) {
		return nil, fmt.Errorf("foundation: invalid plan JSON")
	}
	key := network + "|" + quarterEnd
	kept := append(json.RawMessage(nil), plan...)
	err := s.db.Update(func(tx store.Tx) error {
		existing, ok, err := tx.Get(plansCollection, key)
		if err != nil {
			return err
		}
		if ok {
			kept = append(json.RawMessage(nil), existing...)
			return nil
		}
		return tx.Put(plansCollection, key, plan)
	})
	if err != nil {
		return nil, err
	}
	s.state.mu.Lock()
	s.state.plans[key] = append(json.RawMessage(nil), kept...)
	s.state.mu.Unlock()
	return kept, nil
}

func payoutKey(id DistributionTransferID) string {
	return strings.Join([]string{id.Network, id.QuarterEnd, id.RecipientKind, id.RecipientID}, "|")
}
