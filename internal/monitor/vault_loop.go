package monitor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"foryou.local/foryou/internal/committee"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

const (
	vaultChangesPageLimit = 100
	vaultClaimsPageLimit  = 100
	vaultChainLimit       = 1000
)

var (
	// vaultRecordsPerPass bounds the change records, retries included, handled per provider in one pass.
	vaultRecordsPerPass = 500
	// vaultHorizon is how long after a receipt's issued_at an undelivered message is retried.
	vaultHorizon = 30 * 24 * time.Hour

	vaultCursors = store.Collection[vaultCursor]{Name: "monitor.vault.cursor"}
	vaultPending = store.Collection[vaultPendingRecord]{Name: "monitor.vault.pending"}
)

type vaultCursor struct {
	After uint64 `json:"after"`
}

// vaultPendingRecord keeps the exact record bytes the provider served, because the vault receives those bytes.
type vaultPendingRecord struct {
	ProviderURL string          `json:"provider_url"`
	ProviderID  string          `json:"provider_id"`
	Sequence    int64           `json:"sequence"`
	Message     json.RawMessage `json:"message"`
}

func init() {
	loopSteps = append(loopSteps, loopStep{order: vaultDeliveryOrder, name: "vault delivery", run: vaultDeliver})
}

// vaultDeliver runs once per process: one delivery satisfies every hosted member's vault duty.
func vaultDeliver(s *Service, ctx context.Context) error {
	now := s.Now()
	if !vaultOnCommittee(s, now) || s.VaultURL() == "" {
		return nil
	}
	foundationID, ok := s.View().Trust().CurrentFoundationID(now)
	if !ok {
		log.Printf("monitor: vault delivery: no current Foundation key")
		return nil
	}
	vault, err := sdk.New(s.VaultURL(), sdk.Options{ServiceID: foundationID, AllowLoopbackHTTP: true, HTTPClient: s.HTTPClient(), Now: s.Now})
	if err != nil {
		log.Printf("monitor: vault delivery: %v", err)
		return nil
	}
	proofContext := settlement.ProofContext{Committee: s.View().CommitteeAt(now)}
	if previous, changedAt, ok := s.View().PreviousCommittee(now); ok {
		proofContext.PreviousCommittee = previous
		proofContext.ChangedAt = &changedAt
	}
	for _, providerURL := range s.Providers() {
		if err := vaultFollowProvider(s, ctx, providerURL, vault, proofContext); err != nil {
			log.Printf("monitor: vault delivery from %s: %v", providerURL, err)
		}
	}
	return nil
}

func vaultOnCommittee(s *Service, now time.Time) bool {
	for _, member := range s.Members() {
		if s.View().IsMember(member.DID(), now) {
			return true
		}
	}
	return false
}

// vaultProviderClient pins the provider to the operator key of its self-signed manifest. The core
// configures provider URLs only; nothing delivered depends on this pin, because every delivery
// carries a settlement proof verified against the committee.
func vaultProviderClient(s *Service, ctx context.Context, providerURL string, placeholderID string) (*sdk.Client, string, error) {
	// OperatorManifest does not read the pin, so the bootstrap client may use any valid did:key.
	bootstrap, err := sdk.New(providerURL, sdk.Options{ServiceID: placeholderID, AllowLoopbackHTTP: true, HTTPClient: s.HTTPClient(), Now: s.Now})
	if err != nil {
		return nil, "", err
	}
	manifest, raw, err := bootstrap.OperatorManifest(ctx)
	if err != nil {
		return nil, "", err
	}
	if status := sdk.NewManifestPins().Check(manifest.OperatorID, raw, manifest, s.Now()); status != sdk.ManifestCurrent {
		return nil, "", fmt.Errorf("operator manifest is %s", status)
	}
	client, err := sdk.New(providerURL, sdk.Options{ServiceID: manifest.OperatorID, AllowLoopbackHTTP: true, HTTPClient: s.HTTPClient(), Now: s.Now})
	if err != nil {
		return nil, "", err
	}
	return client, manifest.OperatorID, nil
}

func vaultFollowProvider(s *Service, ctx context.Context, providerURL string, vault *sdk.Client, proofContext settlement.ProofContext) error {
	provider, providerID, err := vaultProviderClient(s, ctx, providerURL, vault.ServiceID())
	if err != nil {
		return err
	}
	budget := vaultRecordsPerPass
	retries, err := vaultListPending(s, providerURL, providerID)
	if err != nil {
		return err
	}
	for _, record := range retries {
		if budget <= 0 {
			return nil
		}
		budget--
		if vaultAttempt(s, ctx, provider, vault, record, proofContext) {
			continue
		}
		if err := s.DB().Update(func(tx store.Tx) error { return vaultPending.Delete(tx, vaultPendingKey(record)) }); err != nil {
			return err
		}
	}
	cursorKey := vaultCursorKey(providerURL, providerID)
	var cursor vaultCursor
	if err := s.DB().View(func(tx store.Tx) error {
		var getErr error
		cursor, _, getErr = vaultCursors.Get(tx, cursorKey)
		return getErr
	}); err != nil {
		return err
	}
	for budget > 0 {
		result, err := provider.Changes(ctx, cursor.After, min(vaultChangesPageLimit, budget))
		if err != nil {
			return err
		}
		exact, err := vaultExactMessages(result.Raw, len(result.Page.Items))
		if err != nil {
			return err
		}
		var kept []vaultPendingRecord
		for i, item := range result.Page.Items {
			budget--
			record := vaultPendingRecord{ProviderURL: providerURL, ProviderID: providerID, Sequence: item.Sequence, Message: exact[i]}
			if vaultAttempt(s, ctx, provider, vault, record, proofContext) {
				kept = append(kept, record)
			}
		}
		cursor.After = uint64(result.Page.NextAfter)
		if err := s.DB().Update(func(tx store.Tx) error {
			for _, record := range kept {
				if err := vaultPending.Put(tx, vaultPendingKey(record), record); err != nil {
					return err
				}
			}
			return vaultCursors.Put(tx, cursorKey, cursor)
		}); err != nil {
			return err
		}
		if !result.Page.More || len(result.Page.Items) == 0 {
			return nil
		}
	}
	return nil
}

// vaultAttempt makes sure the vault holds the record and reports whether it must be retried on a later pass.
func vaultAttempt(s *Service, ctx context.Context, provider, vault *sdk.Client, record vaultPendingRecord, proofContext settlement.ProofContext) bool {
	var message wire.PostMessage
	if err := wire.Decode(record.Message, "PostMessage", &message); err != nil {
		log.Printf("monitor: vault delivery: undecodable record %d: %v", record.Sequence, err)
		return false
	}
	err := vaultEnsureHeld(s, ctx, provider, vault, record, message, proofContext)
	if err == nil {
		return false
	}
	log.Printf("monitor: vault delivery of %s: %v", message.MessageID, err)
	var final vaultFinalError
	if errors.As(err, &final) {
		return false
	}
	issuedAt, parseErr := wire.ParseTime(message.SubmissionReceipt.IssuedAt)
	return parseErr == nil && s.Now().Before(issuedAt.Add(vaultHorizon))
}

// vaultFinalError marks a refusal that a later pass would not change.
type vaultFinalError struct{ err error }

func (e vaultFinalError) Error() string { return e.err.Error() }
func (e vaultFinalError) Unwrap() error { return e.err }

func vaultEnsureHeld(s *Service, ctx context.Context, provider, vault *sdk.Client, record vaultPendingRecord, message wire.PostMessage, proofContext settlement.ProofContext) error {
	held, err := vaultHeld(s, ctx, vault, message.MessageID)
	if err != nil || held {
		return err
	}
	proof, err := vaultProof(s, ctx, provider, record.ProviderID, message.SubmissionReceipt, proofContext)
	if err != nil {
		return err
	}
	receipt, _, _, err := vault.DeliverToVault(ctx, wire.VaultDelivery{Message: record.Message, Proof: proof})
	if err != nil {
		var public *sdk.Error
		if errors.As(err, &public) && (public.HTTPStatus == http.StatusUnprocessableEntity || public.HTTPStatus == http.StatusForbidden) {
			return vaultFinalError{err}
		}
		return err
	}
	return vaultVerifyReceipt(s, receipt)
}

func vaultHeld(s *Service, ctx context.Context, vault *sdk.Client, messageID string) (bool, error) {
	receipt, _, err := vault.VaultReceipt(ctx, messageID)
	var public *sdk.Error
	if errors.As(err, &public) && public.HTTPStatus == http.StatusNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return vaultVerifyReceipt(s, receipt) == nil, nil
}

func vaultVerifyReceipt(s *Service, receipt wire.VaultReceipt) error {
	storedAt, err := wire.ParseTime(receipt.StoredAt)
	if err != nil {
		return fmt.Errorf("vault receipt stored_at: %w", err)
	}
	return s.View().Trust().VerifyFoundation(receipt, storedAt, "vault-receipt", nil)
}

// vaultProof prefers the provider's own proof and otherwise assembles one from public claim data.
func vaultProof(s *Service, ctx context.Context, provider *sdk.Client, providerID string, receipt wire.SubmissionReceipt, proofContext settlement.ProofContext) (wire.SettlementProof, error) {
	proof, _, err := provider.Settlement(ctx, receipt.ReceiptID)
	if err == nil {
		if _, verifyErr := settlement.VerifyProof(receipt, proof, proofContext); verifyErr == nil {
			return proof, nil
		}
	}
	return vaultAssembleProof(s, ctx, provider, providerID, receipt, proofContext)
}

// vaultAssembleProof finds the claim whose period holds the receipt, then the earliest claim at or after it
// with a committee quorum. Chains across a key succession are not built: the committee state on master
// holds no verified successions to check them against.
func vaultAssembleProof(s *Service, ctx context.Context, provider *sdk.Client, providerID string, receipt wire.SubmissionReceipt, proofContext settlement.ProofContext) (wire.SettlementProof, error) {
	issuedAt, err := wire.ParseTime(receipt.IssuedAt)
	if err != nil {
		return wire.SettlementProof{}, err
	}
	claims, err := vaultClaimsFrom(ctx, provider, providerID, receipt.Network, issuedAt)
	if err != nil {
		return wire.SettlementProof{}, err
	}
	leaves, err := vaultLeaves(ctx, provider, claims[0])
	if err != nil {
		return wire.SettlementProof{}, err
	}
	hash, err := settlement.ReceiptHash(receipt)
	if err != nil {
		return wire.SettlementProof{}, err
	}
	index := -1
	for i, leaf := range leaves {
		if leaf.ReceiptHash == hash {
			index = i
			break
		}
	}
	if index < 0 {
		return wire.SettlementProof{}, errors.New("receipt is not a leaf of the claim covering its issued_at")
	}
	memberWitnesses := vaultMemberWitnesses(s, providerID, receipt.Network, claims[0].Sequence-1)
	for i, witnessed := range claims {
		witnesses := memberWitnesses[witnessed.ClaimID]
		if page, _, err := provider.ClaimWitnesses(ctx, witnessed.ClaimID); err == nil {
			witnesses = append(witnesses, page.Witnesses...)
		}
		if len(witnesses) == 0 {
			continue
		}
		proof, err := settlement.BuildProof(claims[0], leaves, index, witnesses, claims[1:i+1])
		if err != nil {
			continue
		}
		if _, err := settlement.VerifyProof(receipt, proof, proofContext); err == nil {
			return proof, nil
		}
	}
	return wire.SettlementProof{}, errors.New("receipt is not settled")
}

// vaultClaimsFrom returns the claim whose half-open period holds issuedAt, followed by the later claims of the same key.
func vaultClaimsFrom(ctx context.Context, provider *sdk.Client, providerID, network string, issuedAt time.Time) ([]wire.SettlementClaim, error) {
	var claims []wire.SettlementClaim
	var after uint64
	for len(claims) <= vaultChainLimit {
		page, _, err := provider.SettlementClaims(ctx, network, after)
		if err != nil {
			return nil, err
		}
		for _, claim := range page.Claims {
			if len(claims) > 0 {
				if claim.ProviderID != providerID || claim.Sequence != claims[len(claims)-1].Sequence+1 || len(claims) > vaultChainLimit {
					return claims, nil
				}
				claims = append(claims, claim)
				continue
			}
			start, startErr := wire.ParseTime(claim.PeriodStart)
			end, endErr := wire.ParseTime(claim.PeriodEnd)
			if claim.ProviderID == providerID && startErr == nil && endErr == nil && !issuedAt.Before(start) && issuedAt.Before(end) {
				claims = append(claims, claim)
			}
		}
		if len(page.Claims) < vaultClaimsPageLimit {
			break
		}
		after = uint64(page.Claims[len(page.Claims)-1].Sequence)
	}
	if len(claims) == 0 {
		return nil, errors.New("no published claim covers the receipt")
	}
	return claims, nil
}

func vaultLeaves(ctx context.Context, provider *sdk.Client, claim wire.SettlementClaim) ([]wire.SettlementLeaf, error) {
	leaves := make([]wire.SettlementLeaf, 0, claim.ReceiptCount)
	for int64(len(leaves)) < claim.ReceiptCount {
		page, _, err := provider.ClaimLeaves(ctx, claim.ClaimID, uint64(len(leaves)))
		if err != nil {
			return nil, err
		}
		if len(page.Leaves) == 0 {
			return nil, errors.New("claim leaves end before receipt_count")
		}
		leaves = append(leaves, page.Leaves...)
	}
	if int64(len(leaves)) != claim.ReceiptCount {
		return nil, errors.New("claim leaves exceed receipt_count")
	}
	return leaves, nil
}

// vaultMemberWitnesses reads the hosted members' witness pages in process; Member.Do is the only access to that state.
func vaultMemberWitnesses(s *Service, providerID, network string, afterSequence int64) map[string][]wire.SettlementWitness {
	byClaim := make(map[string][]wire.SettlementWitness)
	for _, member := range s.Members() {
		if err := member.Do(func(m *committee.Monitor) error {
			for _, witness := range m.Page(providerID, network, afterSequence, vaultChainLimit+1).Witnesses {
				byClaim[witness.ClaimID] = append(byClaim[witness.ClaimID], witness)
			}
			return nil
		}); err != nil {
			log.Printf("monitor: vault delivery: read member %d page: %v", member.Index(), err)
		}
	}
	return byClaim
}

// vaultExactMessages returns each item's message bytes exactly as the provider served them.
func vaultExactMessages(raw []byte, count int) ([]json.RawMessage, error) {
	var page struct {
		Items []struct {
			Message json.RawMessage `json:"message"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &page); err != nil || len(page.Items) != count {
		return nil, errors.New("changes page items do not match their exact bytes")
	}
	messages := make([]json.RawMessage, count)
	for i, item := range page.Items {
		messages[i] = append(json.RawMessage(nil), item.Message...)
	}
	return messages, nil
}

func vaultCursorKey(providerURL, providerID string) string { return providerURL + "\x00" + providerID }

func vaultPendingKey(record vaultPendingRecord) string {
	return vaultCursorKey(record.ProviderURL, record.ProviderID) + "\x00" + strconv.FormatInt(record.Sequence, 10)
}

func vaultListPending(s *Service, providerURL, providerID string) ([]vaultPendingRecord, error) {
	var records []vaultPendingRecord
	err := s.DB().View(func(tx store.Tx) error {
		return vaultPending.Scan(tx, store.Range{}, func(_ string, record vaultPendingRecord) (bool, error) {
			if record.ProviderURL == providerURL && record.ProviderID == providerID {
				records = append(records, record)
			}
			return true, nil
		})
	})
	return records, err
}
