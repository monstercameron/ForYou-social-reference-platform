package foundation

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/content"
	"foryou.local/foryou/internal/delegation"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/message"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/succession"
	"foryou.local/foryou/internal/wire"
)

const (
	vaultDeliveryMaxBody = 4 << 20
	// vaultDeliveryMaxSuccessionKeys bounds the outbound succession lookups one unauthenticated delivery can cause.
	vaultDeliveryMaxSuccessionKeys = 4
	// vaultDeliveryNoQuorumMessage is the missing-quorum text returned by settlement.VerifyProof.
	vaultDeliveryNoQuorumMessage = "settlement: step 4: no committee quorum"
)

var vaultDeliveryMergeMu sync.Mutex

func init() {
	routeGroups = append(routeGroups, vaultDeliveryRoutes)
}

func vaultDeliveryRoutes(s *Service, mux *http.ServeMux) {
	mux.HandleFunc("PUT /v1/vault/messages/{message_id}", s.vaultDeliveryPut)
	mux.HandleFunc("GET /v1/vault/receipts/{message_id}", s.vaultDeliveryGetReceipt)
}

func (s *Service) vaultDeliveryPut(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	pathID := r.PathValue("message_id")
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, vaultDeliveryMaxBody))
	if err != nil {
		vaultDeliveryWriteError(w, apierr.InvalidMessage, 0)
		return
	}
	var delivery wire.VaultDelivery
	if err := wire.DecodeLimits(body, canon.Limits{MaxBytes: vaultDeliveryMaxBody, MaxDepth: 32}, "VaultDelivery", &delivery); err != nil || !vaultDeliveryCanonicalID(pathID) {
		vaultDeliveryWriteError(w, apierr.InvalidMessage, 0)
		return
	}
	var posted wire.PostMessage
	if err := wire.DecodeLimits(delivery.Message, canon.Limits{MaxBytes: vaultDeliveryMaxBody, MaxDepth: 32}, "TransportMessage", &posted); err != nil || posted.MessageID != pathID {
		vaultDeliveryWriteError(w, apierr.InvalidMessage, 0)
		return
	}
	if len(delivery.Message) > message.MaxMessageBytes {
		vaultDeliveryWriteError(w, apierr.MessageTooLarge, 0)
		return
	}
	if !vaultDeliveryCanonicalBytes(delivery.Message) {
		vaultDeliveryWriteError(w, apierr.InvalidMessage, 0)
		return
	}
	now := s.Clock()
	if err := s.vaultDeliveryVerify(r.Context(), posted, delivery.Proof, now); err != nil {
		if strings.Contains(err.Error(), vaultDeliveryNoQuorumMessage) {
			vaultDeliveryWriteError(w, apierr.NotReady, 0)
		} else {
			vaultDeliveryWriteError(w, apierr.InvalidMessage, 0)
		}
		return
	}
	if !s.keyCertifiedAt(now) {
		vaultDeliveryWriteError(w, apierr.Unavailable, apierr.DefaultRetryAfterSeconds)
		return
	}
	if status, restricted := vaultDeliveryLegalRestriction(posted); restricted {
		vaultDeliveryWriteError(w, status, 0)
		return
	}
	if held, ok := s.VaultRecord(posted.MessageID); ok {
		vaultDeliveryMergeMu.Lock()
		defer vaultDeliveryMergeMu.Unlock()
		held, ok = s.VaultRecord(posted.MessageID)
		if !ok {
			vaultDeliveryWriteError(w, apierr.Unavailable, apierr.DefaultRetryAfterSeconds)
			return
		}
		merged, changed := vaultDeliveryMergeProof(held.Proof, delivery.Proof)
		if changed {
			proofContext := s.vaultDeliveryProofContext(r.Context(), now, merged)
			if _, err := settlement.VerifyProof(posted.SubmissionReceipt, merged, proofContext); err == nil {
				if err := s.ReplaceVaultProof(posted.MessageID, merged); err != nil {
					vaultDeliveryWriteError(w, apierr.Unavailable, apierr.DefaultRetryAfterSeconds)
					return
				}
			}
		}
		vaultDeliveryWriteJSON(w, http.StatusOK, held.Receipt)
		return
	}
	if other, ok := s.VaultRecordByReceiptID(posted.SubmissionReceipt.ReceiptID); ok && other.MessageID != posted.MessageID {
		vaultDeliveryWriteError(w, apierr.InvalidMessage, 0)
		return
	}
	record, added, err := s.StoreVaultRecord(delivery.Message, delivery.Proof, now)
	if err != nil {
		if other, ok := s.VaultRecordByReceiptID(posted.SubmissionReceipt.ReceiptID); ok && other.MessageID != posted.MessageID {
			vaultDeliveryWriteError(w, apierr.InvalidMessage, 0)
			return
		}
		vaultDeliveryWriteError(w, apierr.Unavailable, apierr.DefaultRetryAfterSeconds)
		return
	}
	status := http.StatusOK
	if added {
		status = http.StatusCreated
	}
	vaultDeliveryWriteJSON(w, status, record.Receipt)
}

func (s *Service) vaultDeliveryGetReceipt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	messageID := r.PathValue("message_id")
	if !vaultDeliveryCanonicalID(messageID) {
		vaultDeliveryWriteError(w, apierr.InvalidQuery, 0)
		return
	}
	record, ok := s.VaultRecord(messageID)
	if !ok {
		vaultDeliveryWriteError(w, apierr.NotFound, 0)
		return
	}
	vaultDeliveryWriteJSON(w, http.StatusOK, record.Receipt)
}

func (s *Service) vaultDeliveryVerify(ctx context.Context, posted wire.PostMessage, proof wire.SettlementProof, now time.Time) error {
	delegationCheck := func(d wire.Delegation, intent wire.Intent, providerID string, at time.Time) error {
		return delegation.CheckIntentDelegation(d, intent, providerID, at)
	}
	if err := message.Verify(posted, message.VerifyOptions{Now: now, Delegation: delegationCheck}); err != nil {
		return err
	}
	if err := content.ValidatePostContent(posted.Payload); err != nil {
		return err
	}
	if err := fee.VerifyReceipt(posted.SubmissionReceipt); err != nil {
		return err
	}
	messageAt, messageTimeErr := wire.ParseTime(posted.Timestamp)
	issuedAt, receiptTimeErr := wire.ParseTime(posted.SubmissionReceipt.IssuedAt)
	if messageTimeErr != nil || receiptTimeErr != nil || issuedAt.Before(messageAt.Add(-60*time.Second)) || issuedAt.After(messageAt.Add(300*time.Second)) {
		return errors.New("vault: receipt issued_at is outside the message timestamp window")
	}
	intentBytes, err := canon.Encode(message.IntentOf(posted))
	if err != nil {
		return err
	}
	var schedule *wire.FeeSchedule
	for _, approved := range s.FeeSchedules() {
		if approved.Schedule.ScheduleID == posted.SubmissionReceipt.ScheduleID {
			candidate := approved.Schedule
			schedule = &candidate
			break
		}
	}
	if schedule == nil || schedule.ProfileMajor != posted.Version.Major || schedule.ProfileMinor != posted.Version.Minor {
		return errors.New("vault: receipt fee schedule is not approved")
	}
	if schedule.Network != posted.SubmissionReceipt.Network {
		return errors.New("vault: fee schedule network differs from receipt")
	}
	amount, err := fee.Amount(*schedule, len(intentBytes))
	if err != nil {
		return err
	}
	paid, err := fee.ParsePositiveAmount(posted.SubmissionReceipt.AmountBaseUnits)
	if err != nil || amount.Cmp(paid) != 0 {
		return errors.New("vault: receipt amount does not match message fee")
	}
	_, err = settlement.VerifyProof(posted.SubmissionReceipt, proof, s.vaultDeliveryProofContext(ctx, now, proof))
	return err
}

func (s *Service) vaultDeliveryProofContext(ctx context.Context, now time.Time, proof wire.SettlementProof) settlement.ProofContext {
	policies := s.Policies()
	policy, ok := s.PolicyInForce(now)
	if !ok {
		return settlement.ProofContext{}
	}
	proofContext := settlement.ProofContext{Committee: policy.Monitors}
	proofContext.Successions = s.vaultDeliverySuccessions(ctx, proof)
	currentIndex := -1
	for index := len(policies) - 1; index >= 0; index-- {
		if policies[index].PolicyID == policy.PolicyID {
			currentIndex = index
			break
		}
	}
	for previous := currentIndex - 1; previous >= 0; previous-- {
		if vaultDeliverySameCommittee(policies[previous].Monitors, policy.Monitors) {
			continue
		}
		changePolicy := policies[previous+1]
		changedAt, err := wire.ParseTime(changePolicy.EffectiveAt)
		if err != nil || now.Before(changedAt) || !now.Before(changedAt.Add(30*24*time.Hour)) {
			return proofContext
		}
		proofContext.PreviousCommittee = append([]string(nil), policies[previous].Monitors...)
		proofContext.ChangedAt = &changedAt
		return proofContext
	}
	return proofContext
}

func (s *Service) vaultDeliverySuccessions(ctx context.Context, proof wire.SettlementProof) settlement.SuccessionLookup {
	claims := append([]wire.SettlementClaim{proof.Claim}, proof.Chain...)
	monitors := s.Monitors()
	verified := make(map[string]wire.KeySuccession)
	monitorIDs := make([]string, 0, len(monitors))
	for monitorID := range monitors {
		monitorIDs = append(monitorIDs, monitorID)
	}
	sort.Strings(monitorIDs)
	fetched := make(map[string]struct{})
	for index := 1; index < len(claims); index++ {
		later := claims[index]
		if later.SuccessionID == nil {
			continue
		}
		oldID := claims[index-1].ProviderID
		if _, exists := fetched[oldID]; exists || len(fetched) >= vaultDeliveryMaxSuccessionKeys {
			continue
		}
		fetched[oldID] = struct{}{}
		for _, monitorID := range monitorIDs {
			baseURL := monitors[monitorID]
			client, err := sdk.New(baseURL, sdk.Options{ServiceID: monitorID, AllowLoopbackHTTP: s.cfg.Dev})
			if err != nil {
				continue
			}
			items, _, _, err := client.Successions(ctx, oldID)
			if err != nil {
				continue
			}
			for _, item := range items {
				if succession.Verify(item) == nil {
					verified[item.SuccessionID] = item
				}
			}
			break
		}
	}
	return func(id string) (wire.KeySuccession, bool) {
		item, ok := verified[id]
		return item, ok
	}
}

// vaultDeliveryLegalRestriction is a placeholder because the platform has no legal-restriction source.
func vaultDeliveryLegalRestriction(_ wire.PostMessage) (apierr.Status, bool) {
	return "", false
}

func vaultDeliverySameCommittee(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	set := make(map[string]struct{}, len(left))
	for _, id := range left {
		set[id] = struct{}{}
	}
	for _, id := range right {
		if _, ok := set[id]; !ok {
			return false
		}
	}
	return true
}

func vaultDeliveryMergeProof(held, incoming wire.SettlementProof) (wire.SettlementProof, bool) {
	merged := held
	if vaultDeliveryFinalClaim(incoming).Sequence > vaultDeliveryFinalClaim(held).Sequence {
		merged.Claim = incoming.Claim
		merged.Chain = append([]wire.SettlementClaim(nil), incoming.Chain...)
	}
	finalClaim := vaultDeliveryFinalClaim(merged)
	merged.Witnesses = append([]wire.SettlementWitness(nil), held.Witnesses...)
	changed := false
	for _, witness := range incoming.Witnesses {
		if settlement.VerifyWitness(witness) != nil || !settlement.WitnessMatches(witness, finalClaim) || vaultDeliveryContainsWitness(merged.Witnesses, witness) {
			continue
		}
		merged.Witnesses = append(merged.Witnesses, witness)
		changed = true
	}
	return merged, changed
}

func vaultDeliveryFinalClaim(proof wire.SettlementProof) wire.SettlementClaim {
	if len(proof.Chain) > 0 {
		return proof.Chain[len(proof.Chain)-1]
	}
	return proof.Claim
}

func vaultDeliveryContainsWitness(witnesses []wire.SettlementWitness, candidate wire.SettlementWitness) bool {
	for _, witness := range witnesses {
		if witness == candidate {
			return true
		}
	}
	return false
}

func vaultDeliveryCanonicalID(value string) bool {
	if len(value) != 68 || !strings.HasPrefix(value, "fy1:") {
		return false
	}
	decoded, err := hex.DecodeString(value[4:])
	return err == nil && hex.EncodeToString(decoded) == value[4:]
}

func vaultDeliveryCanonicalBytes(value []byte) bool {
	canonical, err := canon.Canonicalize(value, canon.Limits{MaxBytes: vaultDeliveryMaxBody, MaxDepth: 32})
	return err == nil && bytes.Equal(canonical, value)
}

func vaultDeliveryWriteJSON(w http.ResponseWriter, status int, value any) {
	encoded, err := json.Marshal(value)
	if err != nil {
		vaultDeliveryWriteError(w, apierr.Unavailable, apierr.DefaultRetryAfterSeconds)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
}

func vaultDeliveryWriteError(w http.ResponseWriter, status apierr.Status, retry int) {
	apierr.Write(w, apierr.PublicReadError{Status: status, RetryAfterSeconds: retry})
}
