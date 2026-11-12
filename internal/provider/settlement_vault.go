package provider

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"sync"
	"time"

	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

const (
	vaultCollection = "provider.settlement.vault"
	vaultPassSize   = 500
	// vaultLateAfter is when an outstanding delivery is logged, a day before the
	// seven-day delivery deadline after settlement.
	vaultLateAfter = 6 * 24 * time.Hour
	// vaultRetryInitial is the wait after the vault's first 422 refusal of a delivery;
	// each further refusal doubles it up to vaultRetryMax.
	vaultRetryInitial = time.Minute
	vaultRetryMax     = time.Hour
)

var vaultClients = struct {
	sync.RWMutex
	clients map[*Service]*sdk.Client
}{clients: make(map[*Service]*sdk.Client)}

// vaultDeliveryJournal records a settled message's delivery. SettledAt is when this
// provider first observed the receipt settled; State is pending, delivered or failed.
// Only a 403 legal refusal marks a delivery failed. A 422 keeps it pending: Refusals
// counts those answers and RetryAt is the earliest time the next attempt may be made,
// because the proof and witnesses the vault rejects can change.
type vaultDeliveryJournal struct {
	State         string `json:"state"`
	SettledAt     string `json:"settled_at,omitempty"`
	LateWarningAt string `json:"late_warning_at,omitempty"`
	Refusals      int    `json:"refusals,omitempty"`
	RetryAt       string `json:"retry_at,omitempty"`
}

func init() {
	routeGroups = append(routeGroups, vaultRegister)
}

func vaultRegister(s *Service, _ *http.ServeMux) {
	var mu sync.Mutex
	s.Every("vault-delivery", time.Minute, func(ctx context.Context, now time.Time) error {
		mu.Lock()
		defer mu.Unlock()
		return vaultDeliverSettled(ctx, s, now)
	})
}

// SetVault names the Foundation vault that the delivery loop sends settled messages to.
// Until called, the loop does not attempt delivery.
func (s *Service) SetVault(vault *sdk.Client) {
	vaultClients.Lock()
	if vault == nil {
		delete(vaultClients.clients, s)
	} else {
		vaultClients.clients[s] = vault
	}
	vaultClients.Unlock()
}

func vaultClient(s *Service) *sdk.Client {
	vaultClients.RLock()
	defer vaultClients.RUnlock()
	return vaultClients.clients[s]
}

func vaultDeliverSettled(ctx context.Context, s *Service, now time.Time) error {
	vault := vaultClient(s)
	if vault == nil {
		return nil
	}
	after := uint64(0)
	for {
		records := s.Records().After(after, vaultPassSize)
		if len(records) == 0 {
			return nil
		}
		for _, record := range records {
			after = record.Sequence
			messageBytes, state, err := s.Records().Lookup(record.MessageID)
			if err != nil {
				log.Printf("provider: vault delivery %s: %v", record.MessageID, err)
				continue
			}
			if state != RecordRetained {
				continue
			}
			if err := vaultDeliverRecord(ctx, s, vault, messageBytes, now); err != nil {
				log.Printf("provider: vault delivery %s: %v", record.MessageID, err)
			}
		}
		if len(records) < vaultPassSize {
			return nil
		}
	}
}

func vaultDeliverRecord(ctx context.Context, s *Service, vault *sdk.Client, messageBytes []byte, now time.Time) error {
	if len(messageBytes) == 0 {
		return nil
	}
	var message wire.PostMessage
	if err := wire.Decode(messageBytes, "PostMessage", &message); err != nil {
		return err
	}
	if message.SubmissionReceipt.ProviderID != s.ProviderID() {
		return nil
	}
	journal, exists, err := vaultReadJournal(s, message.MessageID)
	if err != nil {
		return err
	}
	if exists && (journal.State == "delivered" || journal.State == "failed") {
		return nil
	}
	restriction, err := s.Restriction(message.MessageID)
	if err != nil {
		return err
	}
	if restriction != Unrestricted {
		return nil
	}
	proof, err := settleBuildProof(s, message.SubmissionReceipt)
	if errors.Is(err, errSettleNotLocated) {
		return nil
	}
	if err != nil {
		return err
	}
	if !settleReceiptIsSettled(s, message.SubmissionReceipt, proof) {
		return nil
	}
	if journal.SettledAt == "" {
		journal.SettledAt = wire.FormatTime(now)
		journal.State = "pending"
		if err := vaultWriteJournal(s, message.MessageID, journal); err != nil {
			return err
		}
	}
	settledAt, err := wire.ParseTime(journal.SettledAt)
	if err != nil {
		return err
	}
	if now.Sub(settledAt) >= vaultLateAfter && journal.LateWarningAt == "" {
		log.Printf("provider: vault delivery for %s remains outstanding more than six days after settlement", message.MessageID)
		journal.LateWarningAt = wire.FormatTime(now)
		if err := vaultWriteJournal(s, message.MessageID, journal); err != nil {
			return err
		}
	}
	if journal.RetryAt != "" {
		retryAt, err := wire.ParseTime(journal.RetryAt)
		if err != nil {
			return err
		}
		if now.Before(retryAt) {
			return nil
		}
	}
	delivery := wire.VaultDelivery{Message: append(json.RawMessage(nil), messageBytes...), Proof: proof}
	receipt, _, status, err := vault.DeliverToVault(ctx, delivery)
	if err != nil {
		var public *sdk.Error
		if errors.As(err, &public) {
			switch public.HTTPStatus {
			case http.StatusForbidden:
				journal.State = "failed"
			case http.StatusUnprocessableEntity:
				journal.Refusals++
				journal.RetryAt = wire.FormatTime(now.Add(vaultRetryDelay(journal.Refusals)))
			default:
				return err
			}
			if err := vaultWriteJournal(s, message.MessageID, journal); err != nil {
				return err
			}
		}
		return err
	}
	if status != http.StatusCreated && status != http.StatusOK {
		return errors.New("provider: unexpected vault delivery status")
	}
	if receipt.MessageID != message.MessageID {
		return errors.New("provider: vault receipt message ID differs from delivery")
	}
	storedAt, err := wire.ParseTime(receipt.StoredAt)
	if err != nil {
		return err
	}
	if err := s.Trust().VerifyFoundation(receipt, storedAt, "vault-receipt", nil); err != nil {
		return err
	}
	journal.State = "delivered"
	return vaultWriteJournal(s, message.MessageID, journal)
}

func vaultRetryDelay(refusals int) time.Duration {
	delay := vaultRetryInitial
	for i := 1; i < refusals && delay < vaultRetryMax; i++ {
		delay *= 2
	}
	if delay > vaultRetryMax {
		return vaultRetryMax
	}
	return delay
}

func vaultReadJournal(s *Service, messageID string) (vaultDeliveryJournal, bool, error) {
	var journal vaultDeliveryJournal
	var exists bool
	err := s.Store().View(func(tx store.Tx) error {
		data, ok, err := tx.Get(vaultCollection, messageID)
		exists = ok
		if err != nil || !ok {
			return err
		}
		return json.Unmarshal(data, &journal)
	})
	return journal, exists, err
}

func vaultWriteJournal(s *Service, messageID string, journal vaultDeliveryJournal) error {
	data, err := json.Marshal(journal)
	if err != nil {
		return err
	}
	return s.Store().Update(func(tx store.Tx) error { return tx.Put(vaultCollection, messageID, data) })
}
