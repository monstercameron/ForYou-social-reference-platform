package foundation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"

	"foryou.local/foryou/internal/distribution"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/governance"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

type distRecordedTransfer struct {
	Network   string        `json:"network"`
	Recipient string        `json:"recipient"`
	Amount    string        `json:"amount"`
	Transfer  faux.Transfer `json:"transfer"`
}

// distFrozenPlan is everything a quarter's statement for one network is computed
// from, recorded before the first transfer.
type distFrozenPlan struct {
	Input   distribution.Input `json:"input"`
	Reports []QualityReportSet `json:"reports"`
	Claims  []distClaimRecord  `json:"claims"`
}

func (s *Service) distFrozen(network, quarterEnd string) (distFrozenPlan, bool, error) {
	raw, ok := s.FrozenPlan(network, quarterEnd)
	if !ok {
		return distFrozenPlan{}, false, nil
	}
	var plan distFrozenPlan
	if err := json.Unmarshal(raw, &plan); err != nil {
		return distFrozenPlan{}, false, fmt.Errorf("foundation: invalid frozen plan for %s %s: %w", network, quarterEnd, err)
	}
	return plan, true, nil
}

// distFreeze records the collected inputs unless the quarter already has a frozen
// plan, and returns the recorded one as it will be read back after a restart.
func (s *Service) distFreeze(input distribution.Input, reports []QualityReportSet, claims []distClaimRecord) (distFrozenPlan, error) {
	data, err := json.Marshal(distFrozenPlan{Input: input, Reports: reports, Claims: claims})
	if err != nil {
		return distFrozenPlan{}, err
	}
	kept, err := s.FreezePlan(input.Network, wire.FormatTime(input.End), data)
	if err != nil {
		return distFrozenPlan{}, err
	}
	var plan distFrozenPlan
	if err := json.Unmarshal(kept, &plan); err != nil {
		return distFrozenPlan{}, err
	}
	return plan, nil
}

// distPublish freezes the inputs for the network and quarter, or takes the plan
// already frozen, then pays it and publishes the statement.
func (s *Service) distPublish(ctx context.Context, input distribution.Input, reports []QualityReportSet, claims []distClaimRecord) error {
	plan, err := s.distFreeze(input, reports, claims)
	if err != nil {
		return err
	}
	return s.distPay(ctx, plan)
}

// distPay makes the transfers of a frozen plan that are not yet recorded and
// publishes its statement.
func (s *Service) distPay(ctx context.Context, frozen distFrozenPlan) error {
	input, reports, claims := frozen.Input, frozen.Reports, frozen.Claims
	for _, record := range claims {
		data, err := json.Marshal(record)
		if err != nil {
			return err
		}
		if _, err := s.MarkClaimCounted(record.Claim.ClaimID, data); err != nil {
			return err
		}
	}
	plan, err := distribution.Compute(input)
	if err != nil {
		return err
	}
	for _, set := range reports {
		if err := s.StoreQualityReports(set); err != nil {
			return err
		}
	}
	quarterEnd := wire.FormatTime(input.End)
	providerTransfers := make(map[string]wire.DepositTransfer)
	providerPayments := plan.ProviderPayments()
	providerIDs := make([]string, 0, len(providerPayments))
	for id := range providerPayments {
		providerIDs = append(providerIDs, id)
	}
	sort.Strings(providerIDs)
	for _, id := range providerIDs {
		amount := providerPayments[id]
		recipient := ""
		for _, provider := range input.Providers {
			if provider.ChainID == id {
				recipient = provider.PayoutRecipient
				break
			}
		}
		transfer, err := s.distMakeTransfer(ctx, input.Network, quarterEnd, "provider", id, recipient, amount)
		if err != nil {
			return err
		}
		providerTransfers[id] = distDepositTransfer(transfer)
	}
	monitorTransfers := make(map[string]wire.DepositTransfer)
	payouts := s.MonitorPayouts()
	monitorPayments := plan.MonitorPayments()
	monitorIDs := make([]string, 0, len(monitorPayments))
	for id := range monitorPayments {
		monitorIDs = append(monitorIDs, id)
	}
	sort.Strings(monitorIDs)
	for _, id := range monitorIDs {
		amount := monitorPayments[id]
		recipient := payouts[id][input.Network]
		if recipient == "" {
			return fmt.Errorf("foundation: missing %s payout address for monitor %s", input.Network, id)
		}
		// Bitcoin pays whole satoshis and the specification does not say how to pay a seat below one satoshi, so such a seat is published with a null payout.
		if input.Network == wire.BitcoinMainnet && amount.Cmp(big.NewInt(1000)) < 0 {
			continue
		}
		transfer, err := s.distMakeTransfer(ctx, input.Network, quarterEnd, "monitor", id, recipient, amount)
		if err != nil {
			return err
		}
		monitorTransfers[id] = distDepositTransfer(transfer)
	}
	statement, err := plan.Statement(keys.DIDOf(s.cfg.Key), providerTransfers, monitorTransfers)
	if err != nil {
		return err
	}
	_, _, err = s.SignAndLog(governance.LogKindStatement, "distribution-statement", statement)
	return err
}

func (s *Service) distMakeTransfer(ctx context.Context, network, quarterEnd, kind, id, recipient string, baseAmount *big.Int) (faux.Transfer, error) {
	if recipient == "" {
		return faux.Transfer{}, fmt.Errorf("foundation: payout recipient missing for %s %s", kind, id)
	}
	if err := faux.ValidateRecipient(network, recipient); err != nil {
		return faux.Transfer{}, err
	}
	if s.Faux() == nil {
		return faux.Transfer{}, errors.New("foundation: faux chain is not configured")
	}
	amount := new(big.Int).Set(baseAmount)
	if network == wire.BitcoinMainnet {
		// Bitcoin pays whole satoshis: provider amounts are already whole satoshis, and a seat's millisatoshi remainder is not transferred.
		amount.Quo(amount, big.NewInt(1000))
	}
	transferID := DistributionTransferID{Network: network, QuarterEnd: quarterEnd, RecipientKind: kind, RecipientID: id}
	wantAmount := amount.String()
	if raw, ok := s.PayoutTransfer(transferID); ok {
		var saved distRecordedTransfer
		if err := json.Unmarshal(raw, &saved); err != nil {
			return faux.Transfer{}, err
		}
		// A recorded transfer was paid and stands, even if the payout address changed since.
		if saved.Network != network || saved.Amount != wantAmount || !distTransferMatches(saved.Transfer, network, saved.Recipient, wantAmount) {
			return faux.Transfer{}, errors.New("foundation: recorded payout differs from the frozen plan")
		}
		return saved.Transfer, nil
	}
	digest := sha256.Sum256([]byte(network + "\x00" + quarterEnd + "\x00" + kind + "\x00" + id))
	request := faux.SubmitRequest{Network: network, Recipient: recipient, AmountChainUnits: wantAmount, ClientRef: "distribution:" + hex.EncodeToString(digest[:])}
	transfer, err := s.Faux().Submit(ctx, request)
	if err != nil {
		return faux.Transfer{}, err
	}
	if !distTransferMatches(transfer, network, recipient, wantAmount) {
		return faux.Transfer{}, errors.New("foundation: faux chain returned mismatched payout")
	}
	record, err := json.Marshal(distRecordedTransfer{Network: network, Recipient: recipient, Amount: wantAmount, Transfer: transfer})
	if err != nil {
		return faux.Transfer{}, err
	}
	if _, err := s.MarkPayoutMade(transferID, record); err != nil {
		return faux.Transfer{}, err
	}
	return transfer, nil
}

func distTransferMatches(transfer faux.Transfer, network, recipient, amount string) bool {
	if transfer.Network != network || transfer.Recipient != recipient || transfer.AmountChainUnits != amount || !transfer.Success || !transfer.Finalized {
		return false
	}
	if network == wire.BitcoinMainnet {
		return transfer.TxID != "" && transfer.OutputIndex != nil
	}
	return transfer.TxHash != ""
}

func distDepositTransfer(transfer faux.Transfer) wire.DepositTransfer {
	if transfer.Network == wire.BitcoinMainnet {
		index := int64(0)
		if transfer.OutputIndex != nil {
			index = int64(*transfer.OutputIndex)
		}
		return wire.DepositTransfer{Network: transfer.Network, TxID: transfer.TxID, OutputIndex: &index}
	}
	return wire.DepositTransfer{Network: transfer.Network, TxHash: transfer.TxHash}
}
