package foundation

import (
	"context"
	"net/url"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

func init() {
	backgroundLoops = append(backgroundLoops, vaultRefreshLoop)
}

func vaultRefreshLoop(ctx context.Context, s *Service) {
	ticker := time.NewTicker(s.cfg.HeadInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			vaultRefreshHeld(ctx, s, s.Clock())
		}
	}
}

func vaultRefreshHeld(ctx context.Context, s *Service, now time.Time) {
	policy, ok := s.PolicyInForce(now)
	if !ok {
		return
	}
	monitors := s.Monitors()
	for _, record := range s.VaultRecords() {
		claim := vaultDeliveryFinalClaim(record.Proof)
		if settlement.Settled(settlement.CountWitnesses(claim, record.Proof.Witnesses, policy.Monitors, nil), policy.Monitors) {
			continue
		}
		proofContext := s.vaultDeliveryProofContext(ctx, now, record.Proof)
		incoming := record.Proof
		incoming.Witnesses = nil
		for _, monitorID := range policy.Monitors {
			baseURL, exists := monitors[monitorID]
			if !exists {
				continue
			}
			parsed, err := url.Parse(baseURL)
			if err != nil {
				continue
			}
			client, err := sdk.New(baseURL, sdk.Options{ServiceID: monitorID, AllowLoopbackHTTP: s.cfg.Dev && parsed.Scheme == "http"})
			if err != nil {
				continue
			}
			sequence := uint64(claim.Sequence - 1)
			page, _, err := client.Witnesses(ctx, claim.ProviderID, claim.Network, sequence)
			if err != nil {
				continue
			}
			for _, witness := range page.Witnesses {
				if witness.WitnessID != monitorID || !settlement.WitnessMatches(witness, claim) || settlement.VerifyWitness(witness) != nil {
					continue
				}
				incoming.Witnesses = append(incoming.Witnesses, witness)
			}
		}
		if len(incoming.Witnesses) == 0 {
			continue
		}
		vaultDeliveryMergeMu.Lock()
		latest, exists := s.VaultRecord(record.MessageID)
		if !exists {
			vaultDeliveryMergeMu.Unlock()
			continue
		}
		merged, changed := vaultDeliveryMergeProof(latest.Proof, incoming)
		if changed {
			if _, err := settlement.VerifyProof(vaultRefreshReceipt(latest), merged, proofContext); err == nil {
				_ = s.ReplaceVaultProof(record.MessageID, merged)
			}
		}
		vaultDeliveryMergeMu.Unlock()
	}
}

func vaultRefreshReceipt(record VaultRecord) wire.SubmissionReceipt {
	var posted wire.PostMessage
	if err := wire.DecodeLimits(record.Message, canon.MessageLimits, "TransportMessage", &posted); err != nil {
		return wire.SubmissionReceipt{}
	}
	return posted.SubmissionReceipt
}
