package provider

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

const (
	settlementClaimsCollection    = "provider.settlement.claims"
	settlementLeavesCollection    = "provider.settlement.leaves"
	settlementReceiptsCollection  = "provider.settlement.receipts"
	settlementWitnessesCollection = "provider.settlement.witnesses"
	settlementSequencesCollection = "provider.settlement.sequences"
	settlementLastCollection      = "provider.settlement.last"
	settlementCursorsCollection   = "provider.settlement.cursors"
	settlementRetentionCollection = "provider.settlement.retention"
)

// SettlementStore provides durable access to provider settlement objects.
type SettlementStore struct{ db store.Store }

type settlementReceiptLocation struct {
	ClaimID string `json:"claim_id"`
	Index   int    `json:"leaf_index"`
}

// settlementRetention lists the records of a published claim whose retention end
// is still to be set.
type settlementRetention struct {
	ClaimID       string                   `json:"claim_id"`
	ProviderID    string                   `json:"provider_id"`
	DeclarationID string                   `json:"declaration_id"`
	Records       []settlementRetainRecord `json:"records"`
}

type settlementRetainRecord struct {
	MessageID string `json:"message_id"`
	IssuedAt  string `json:"issued_at"`
}

// Settlement returns the provider's journaled settlement store.
func (s *Service) Settlement() *SettlementStore { return &SettlementStore{db: s.Store()} }

// Append stores a published claim atomically with its leaves in tree order and the
// receipts those leaves bind, in the same order. It refuses a claim that does not
// extend the stored chain of its network or that names an already claimed receipt.
func (s *SettlementStore) Append(claim wire.SettlementClaim, leaves []wire.SettlementLeaf, receipts []wire.SubmissionReceipt) error {
	if len(leaves) == 0 || len(leaves) != len(receipts) || int64(len(leaves)) != claim.ReceiptCount {
		return errors.New("provider settlement: claim, leaf and receipt counts differ")
	}
	claimBytes, err := json.Marshal(claim)
	if err != nil {
		return err
	}
	leavesBytes, err := json.Marshal(leaves)
	if err != nil {
		return err
	}
	pending := settlementRetention{ClaimID: claim.ClaimID, ProviderID: claim.ProviderID, DeclarationID: claim.DeclarationID, Records: make([]settlementRetainRecord, len(receipts))}
	for i, r := range receipts {
		pending.Records[i] = settlementRetainRecord{MessageID: r.MessageID, IssuedAt: r.IssuedAt}
	}
	pendingBytes, err := json.Marshal(pending)
	if err != nil {
		return err
	}
	return s.db.Update(func(tx store.Tx) error {
		if _, ok, err := tx.Get(settlementClaimsCollection, claim.ClaimID); err != nil {
			return err
		} else if ok {
			return errors.New("provider settlement: claim already published")
		}
		lastID, hasLast, err := tx.Get(settlementLastCollection, claim.Network)
		if err != nil {
			return err
		}
		switch {
		case !hasLast && (claim.Sequence != 1 || claim.PreviousClaimID != nil):
			return errors.New("provider settlement: first claim of a network must have sequence 1 and no predecessor")
		case hasLast:
			b, ok, err := tx.Get(settlementClaimsCollection, string(lastID))
			if err != nil {
				return err
			}
			if !ok {
				return errors.New("provider settlement: last claim pointer is broken")
			}
			var last wire.SettlementClaim
			if err := json.Unmarshal(b, &last); err != nil {
				return err
			}
			if claim.Sequence != last.Sequence+1 || claim.PreviousClaimID == nil || *claim.PreviousClaimID != last.ClaimID {
				return errors.New("provider settlement: claim does not extend the stored chain")
			}
		}
		for i, r := range receipts {
			if _, ok, err := tx.Get(settlementReceiptsCollection, r.ReceiptID); err != nil {
				return err
			} else if ok {
				return fmt.Errorf("provider settlement: receipt %s already claimed", r.ReceiptID)
			}
			location, err := json.Marshal(settlementReceiptLocation{ClaimID: claim.ClaimID, Index: i})
			if err != nil {
				return err
			}
			if err := tx.Put(settlementReceiptsCollection, r.ReceiptID, location); err != nil {
				return err
			}
		}
		if err := tx.Put(settlementClaimsCollection, claim.ClaimID, claimBytes); err != nil {
			return err
		}
		if err := tx.Put(settlementLeavesCollection, claim.ClaimID, leavesBytes); err != nil {
			return err
		}
		if err := tx.Put(settlementSequencesCollection, settlementClaimSequenceKey(claim.Network, claim.Sequence), []byte(claim.ClaimID)); err != nil {
			return err
		}
		if err := tx.Put(settlementLastCollection, claim.Network, []byte(claim.ClaimID)); err != nil {
			return err
		}
		return tx.Put(settlementRetentionCollection, claim.ClaimID, pendingBytes)
	})
}

// cursor returns the ledger receipt sequence of a network up to which every receipt
// is claimed or excluded.
func (s *SettlementStore) cursor(network string) (uint64, error) {
	var cursor uint64
	err := s.db.View(func(tx store.Tx) error {
		b, ok, err := tx.Get(settlementCursorsCollection, network)
		if err != nil || !ok {
			return err
		}
		cursor, err = strconv.ParseUint(string(b), 10, 64)
		return err
	})
	return cursor, err
}

func (s *SettlementStore) advanceCursor(network string, cursor uint64) error {
	return s.db.Update(func(tx store.Tx) error {
		b, ok, err := tx.Get(settlementCursorsCollection, network)
		if err != nil {
			return err
		}
		if ok {
			old, err := strconv.ParseUint(string(b), 10, 64)
			if err != nil {
				return err
			}
			if cursor <= old {
				return nil
			}
		}
		return tx.Put(settlementCursorsCollection, network, []byte(strconv.FormatUint(cursor, 10)))
	})
}

func (s *SettlementStore) pendingRetention() ([]settlementRetention, error) {
	var pending []settlementRetention
	err := s.db.View(func(tx store.Tx) error {
		return tx.Scan(settlementRetentionCollection, store.Range{}, func(_ string, b []byte) (bool, error) {
			var item settlementRetention
			if err := json.Unmarshal(b, &item); err != nil {
				return false, err
			}
			pending = append(pending, item)
			return true, nil
		})
	})
	return pending, err
}

func (s *SettlementStore) completeRetention(claimID string) error {
	return s.db.Update(func(tx store.Tx) error { return tx.Delete(settlementRetentionCollection, claimID) })
}

func settlementClaimSequenceKey(network string, sequence int64) string {
	return fmt.Sprintf("%s/%020d", network, sequence)
}

// Claims returns claims for a network in sequence order after afterSequence.
func (s *SettlementStore) Claims(network string, afterSequence int64, limit int) ([]wire.SettlementClaim, error) {
	if limit < 1 {
		return nil, nil
	}
	var claims []wire.SettlementClaim
	err := s.db.View(func(tx store.Tx) error {
		return tx.Scan(settlementSequencesCollection, store.Range{Start: settlementClaimSequenceKey(network, afterSequence+1), End: network + "/\xff"}, func(_ string, id []byte) (bool, error) {
			b, ok, err := tx.Get(settlementClaimsCollection, string(id))
			if err != nil || !ok {
				return false, err
			}
			var claim wire.SettlementClaim
			if err := json.Unmarshal(b, &claim); err != nil {
				return false, err
			}
			claims = append(claims, claim)
			return len(claims) < limit, nil
		})
	})
	return claims, err
}

// Claim returns a claim by its content ID.
func (s *SettlementStore) Claim(id string) (wire.SettlementClaim, bool, error) {
	var claim wire.SettlementClaim
	var ok bool
	err := s.db.View(func(tx store.Tx) error {
		b, exists, err := tx.Get(settlementClaimsCollection, id)
		ok = exists
		if err != nil || !exists {
			return err
		}
		return json.Unmarshal(b, &claim)
	})
	return claim, ok, err
}

// Leaves returns a page of leaves for a claim.
func (s *SettlementStore) Leaves(claimID string, offset, limit int) ([]wire.SettlementLeaf, error) {
	if offset < 0 || limit < 1 {
		return nil, nil
	}
	var all, page []wire.SettlementLeaf
	err := s.db.View(func(tx store.Tx) error {
		b, ok, err := tx.Get(settlementLeavesCollection, claimID)
		if err != nil || !ok {
			return err
		}
		return json.Unmarshal(b, &all)
	})
	if err != nil || offset >= len(all) {
		return page, err
	}
	end := offset + limit
	if end > len(all) {
		end = len(all)
	}
	return append(page, all[offset:end]...), nil
}

// Locate returns the claim holding a receipt and the receipt's leaf index in it.
func (s *SettlementStore) Locate(receiptID string) (claimID string, index int, ok bool, err error) {
	var location settlementReceiptLocation
	err = s.db.View(func(tx store.Tx) error {
		b, exists, err := tx.Get(settlementReceiptsCollection, receiptID)
		ok = exists
		if err != nil || !ok {
			return err
		}
		return json.Unmarshal(b, &location)
	})
	return location.ClaimID, location.Index, ok, err
}

// Last returns the latest published claim for a network.
func (s *SettlementStore) Last(network string) (wire.SettlementClaim, bool, error) {
	var claim wire.SettlementClaim
	var ok bool
	err := s.db.View(func(tx store.Tx) error {
		id, exists, err := tx.Get(settlementLastCollection, network)
		if err != nil || !exists {
			ok = exists
			return err
		}
		b, exists, err := tx.Get(settlementClaimsCollection, string(id))
		ok = exists
		if err != nil || !exists {
			return err
		}
		return json.Unmarshal(b, &claim)
	})
	return claim, ok, err
}

// AddWitness stores the first witness from each witness ID for a claim.
func (s *SettlementStore) AddWitness(w wire.SettlementWitness) (bool, error) {
	b, err := json.Marshal(w)
	if err != nil {
		return false, err
	}
	created := false
	err = s.db.Update(func(tx store.Tx) error {
		key := w.ClaimID + "/" + w.WitnessID
		if _, ok, err := tx.Get(settlementWitnessesCollection, key); err != nil {
			return err
		} else if ok {
			return nil
		}
		if err := tx.Put(settlementWitnessesCollection, key, b); err != nil {
			return err
		}
		created = true
		return nil
	})
	return created, err
}

// Witnesses returns the stored witnesses for a claim in witness ID order.
func (s *SettlementStore) Witnesses(claimID string) ([]wire.SettlementWitness, error) {
	var witnesses []wire.SettlementWitness
	err := s.db.View(func(tx store.Tx) error {
		return tx.Scan(settlementWitnessesCollection, store.Prefix(claimID+"/"), func(_ string, b []byte) (bool, error) {
			var w wire.SettlementWitness
			if err := json.Unmarshal(b, &w); err != nil {
				return false, err
			}
			witnesses = append(witnesses, w)
			return true, nil
		})
	})
	return witnesses, err
}
