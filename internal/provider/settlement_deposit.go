package provider

import (
	"context"
	"encoding/hex"
	"fmt"
	"math/big"
	"time"

	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

// settlementFirstKey is the key whose commitment every deposit carries. The core has
// no key rotation, so the provider's only key is its first key.
func settlementFirstKey(s *Service) string { return s.ProviderID() }

// settlementDeposited computes deposited(p, network) from the chain exactly as monitors do.
func settlementDeposited(ctx context.Context, s *Service, network string) (*big.Int, error) {
	commitment, err := settlement.DepositCommitment(settlementFirstKey(s), network)
	if err != nil {
		return nil, err
	}
	var transfers []settlement.Transfer
	var after uint64
	for {
		page, err := s.Chain.Transfers(ctx, faux.Query{Network: network, Data: hex.EncodeToString(commitment[:]), After: after, Limit: 1000})
		if err != nil {
			return nil, fmt.Errorf("provider settlement: list deposits: %w", err)
		}
		for _, item := range page {
			amount, ok := new(big.Int).SetString(item.AmountChainUnits, 10)
			blockTime, timeErr := wire.ParseTime(item.BlockTime)
			data, dataErr := hex.DecodeString(item.Data)
			if !ok || timeErr != nil || dataErr != nil {
				return nil, fmt.Errorf("provider settlement: invalid faux transfer encoding")
			}
			transfers = append(transfers, settlement.Transfer{Network: item.Network, Recipient: item.Recipient, ChainUnits: amount, DataOutputs: [][]byte{data}, Final: item.Finalized, Successful: item.Success, BlockTime: blockTime.UTC()})
			after = item.Sequence
		}
		if len(page) < 1000 {
			break
		}
	}
	return settlement.CountDeposits(transfers, settlementFirstKey(s), network, s.Trust().RecipientCounts)
}

// settlementFoundationRecipient returns the network recipient of the Foundation key
// certificate in force at at.
func settlementFoundationRecipient(s *Service, network string, at time.Time) (string, error) {
	foundationID, ok := s.Trust().CurrentFoundationID(at)
	if !ok {
		return "", fmt.Errorf("provider settlement: no Foundation certificate in force")
	}
	for _, certificate := range s.Genesis().FoundationKeyCertificates {
		if certificate.FoundationID != foundationID {
			continue
		}
		recipient := certificate.Recipients.BitcoinMainnet
		if network == wire.EthereumMainnet {
			recipient = certificate.Recipients.EthereumMainnet
		}
		if recipient != "" && s.Trust().RecipientCounts(network, recipient, at) {
			return recipient, nil
		}
	}
	return "", fmt.Errorf("provider settlement: no certified Foundation recipient for %s", network)
}

// settlementFundClaim makes deposited(p, network) cover the claim's cumulative total
// before it is published: one transfer of the shortfall plus a headroom of 100 fees
// of a 1 KiB post, in whole chain units and at least the minimum deposit. The
// client_ref names the claim, so a retry after a restart cannot send it twice.
func settlementFundClaim(ctx context.Context, s *Service, claim wire.SettlementClaim, now time.Time) error {
	deposited, err := settlementDeposited(ctx, s, claim.Network)
	if err != nil {
		return err
	}
	cumulative, err := wire.PositiveAmount(claim.CumulativeBaseUnits)
	if err != nil {
		return err
	}
	if deposited.Cmp(cumulative) >= 0 {
		return nil
	}
	shortfall := new(big.Int).Sub(cumulative, deposited)
	schedule, err := fee.InForce(s.FeeSchedules(), claim.Network, now)
	if err != nil {
		return err
	}
	headroom, err := fee.Amount(schedule, 1024)
	if err != nil {
		return err
	}
	amount := new(big.Int).Add(shortfall, new(big.Int).Mul(headroom, big.NewInt(100)))
	if claim.Network == wire.BitcoinMainnet {
		amount.Add(amount, big.NewInt(999))
		amount.Div(amount, big.NewInt(1000))
	}
	minimum, err := settlement.MinimumDeposit(claim.Network)
	if err != nil {
		return err
	}
	if amount.Cmp(minimum) < 0 {
		amount.Set(minimum)
	}
	commitment, err := settlement.DepositCommitment(settlementFirstKey(s), claim.Network)
	if err != nil {
		return err
	}
	recipient, err := settlementFoundationRecipient(s, claim.Network, now)
	if err != nil {
		return err
	}
	_, err = s.Chain.Submit(ctx, faux.SubmitRequest{Network: claim.Network, From: faux.DevAddress(claim.Network, s.Config().RecipientLabel), Recipient: recipient, AmountChainUnits: amount.String(), Data: hex.EncodeToString(commitment[:]), ClientRef: fmt.Sprintf("settlement-%s-%d-%s", claim.Network, claim.Sequence, claim.ClaimID)})
	if err != nil {
		return fmt.Errorf("provider settlement: send deposit: %w", err)
	}
	deposited, err = settlementDeposited(ctx, s, claim.Network)
	if err != nil {
		return err
	}
	if deposited.Cmp(cumulative) < 0 {
		return fmt.Errorf("provider settlement: deposit not counted before claim publication")
	}
	return nil
}
