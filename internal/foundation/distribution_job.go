package foundation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"sort"
	"time"

	"foryou.local/foryou/internal/distribution"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/quality"
	"foryou.local/foryou/internal/retention"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/succession"
	"foryou.local/foryou/internal/wire"
)

func init() {
	backgroundLoops = append(backgroundLoops, distLoop)
}

func distLoop(ctx context.Context, s *Service) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if err := s.distRunDue(ctx, s.Clock()); err != nil {
			log.Printf("foundation distribution: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

type distClaimRecord struct {
	Network string               `json:"network"`
	Quarter string               `json:"quarter_end"`
	Claim   wire.SettlementClaim `json:"claim"`
}

type distProviderData struct {
	chain        []wire.SettlementClaim
	declarations map[string]wire.RetentionDeclaration
	successions  map[string]wire.KeySuccession
	witnesses    map[string][]wire.SettlementWitness
	equivocated  map[int64]bool
	reports      map[string][]wire.QualityReport
}

func (s *Service) distRunDue(ctx context.Context, now time.Time) error {
	if ctx == nil {
		return errors.New("foundation: nil distribution context")
	}
	var firstPolicy time.Time
	for _, policy := range s.Policies() {
		at, err := wire.ParseTime(policy.EffectiveAt)
		if err == nil && (firstPolicy.IsZero() || at.Before(firstPolicy)) {
			firstPolicy = at
		}
	}
	if firstPolicy.IsZero() {
		return nil
	}
	cal := s.Calendar()
	policyStart, _ := cal.Quarter(firstPolicy)
	started, _ := cal.Quarter(s.FirstStarted())
	start := policyStart
	if started.After(start) {
		start = started
	}
	_, end := cal.Quarter(now)
	_, quarterEnd := cal.Quarter(start)
	for !quarterEnd.After(end) {
		if now.Before(cal.CutOff(quarterEnd)) {
			break
		}
		if err := s.distRunQuarter(ctx, quarterEnd); err != nil {
			return err
		}
		_, quarterEnd = cal.Quarter(quarterEnd)
	}
	return nil
}

func (s *Service) distRunQuarter(ctx context.Context, end time.Time) error {
	start, _ := s.Calendar().Quarter(end.Add(-time.Nanosecond))
	policy, ok := s.PolicyInForce(end.Add(-time.Second))
	if !ok {
		return fmt.Errorf("foundation: no distribution policy for %s", end)
	}
	// Every network's inputs are frozen before the first transfer, so a rerun after a
	// partial payout pays the plan it started and needs no evidence fetched again.
	plans := make([]distFrozenPlan, 0, 2)
	for _, network := range []string{wire.BitcoinMainnet, wire.EthereumMainnet} {
		if _, exists := s.DistributionStatement(network, wire.FormatTime(end)); exists {
			continue
		}
		plan, frozen, err := s.distFrozen(network, wire.FormatTime(end))
		if err != nil {
			return err
		}
		if !frozen {
			input, sets, claims, err := s.distCollect(ctx, network, start, end, policy)
			if err != nil {
				return err
			}
			if plan, err = s.distFreeze(input, sets, claims); err != nil {
				return err
			}
		}
		plans = append(plans, plan)
	}
	for _, plan := range plans {
		if err := s.distPay(ctx, plan); err != nil {
			return err
		}
	}
	return s.vaultStatusPublish(end)
}

func (s *Service) distCollect(ctx context.Context, network string, start, end time.Time, policy wire.DistributionPolicy) (distribution.Input, []QualityReportSet, []distClaimRecord, error) {
	cutoff := s.Calendar().CutOff(end)
	policies := s.Policies()
	membership := distribution.Membership(policies, start, end)
	input := distribution.Input{Policy: policy, Network: network, Start: start, End: end, Day: s.Calendar().Day(), CutOff: cutoff, Seats: make(map[string]distribution.SeatEvidence)}
	for monitor, spans := range membership {
		for _, span := range spans {
			input.Members = append(input.Members, distribution.MemberSpan{MonitorID: monitor, Start: span.Start, End: span.End})
		}
	}
	providers := s.Providers()
	monitors := s.Monitors()
	monitorSet := make(map[string]bool, len(membership)+len(policy.Monitors))
	for id := range membership {
		monitorSet[id] = true
	}
	for _, id := range policy.Monitors {
		monitorSet[id] = true
	}
	monitorIDs := make([]string, 0, len(monitorSet))
	for id := range monitorSet {
		if monitors[id] == "" {
			return input, nil, nil, fmt.Errorf("foundation: missing monitor URL for %s", id)
		}
		monitorIDs = append(monitorIDs, id)
	}
	sort.Strings(monitorIDs)
	providerData := make([]distProviderData, 0, len(providers))
	countedRecords := make([]distClaimRecord, 0)
	for _, baseURL := range providers {
		// SDK pinning labels the endpoint only; claim signatures and chain links establish provider identity.
		client, err := sdk.New(baseURL, sdk.Options{ServiceID: keys.DIDOf(s.cfg.Key), AllowLoopbackHTTP: s.cfg.Dev, Now: s.Clock})
		if err != nil {
			return input, nil, nil, err
		}
		data, err := s.distFetchProvider(ctx, client, network, monitorIDs, monitors, end)
		if err != nil {
			return input, nil, nil, err
		}
		providerData = append(providerData, data)
	}
	for i := range providerData {
		data := &providerData[i]
		if len(data.chain) == 0 {
			continue
		}
		firstID := data.chain[0].ProviderID
		allCounted := make(map[string]bool)
		highestByCutoff := make(map[time.Time]int64)
		getHighest := func(at time.Time) int64 {
			if highest, ok := highestByCutoff[at]; ok {
				return highest
			}
			highest := distHighestSettledSequence(data, policies, at)
			highestByCutoff[at] = highest
			return highest
		}
		settledSequence := getHighest(cutoff)
		for _, claim := range data.chain {
			stored, already := s.CountedClaim(claim.ClaimID)
			if already {
				var record distClaimRecord
				if err := json.Unmarshal(stored, &record); err != nil {
					return input, nil, nil, fmt.Errorf("foundation: invalid counted claim %s: %w", claim.ClaimID, err)
				}
				allCounted[claim.ClaimID] = record.Network == network && record.Quarter == wire.FormatTime(end)
				if allCounted[claim.ClaimID] {
					countedRecords = append(countedRecords, record)
				}
				continue
			}
			if claim.Sequence > settledSequence {
				continue
			}
			periodEnd, err := wire.ParseTime(claim.PeriodEnd)
			if err != nil || !periodEnd.Before(end) {
				continue
			}
			_, claimQuarterEnd := s.Calendar().Quarter(periodEnd)
			if claimQuarterEnd.Before(end) {
				if claim.Sequence <= getHighest(s.Calendar().CutOff(claimQuarterEnd)) {
					continue
				}
			} else if !claimQuarterEnd.Equal(end) {
				continue
			}
			allCounted[claim.ClaimID] = true
			countedRecords = append(countedRecords, distClaimRecord{Network: network, Quarter: wire.FormatTime(end), Claim: claim})
		}
		provider := distribution.ProviderInput{ChainID: firstID, SettledFees: new(big.Int), DeclaredSeconds: []int64{}}
		var latest *wire.SettlementClaim
		var activeClaim *wire.SettlementClaim
		for _, claim := range data.chain {
			if !allCounted[claim.ClaimID] {
				continue
			}
			amount, err := wire.PositiveAmount(claim.TotalBaseUnits)
			if err != nil {
				return input, nil, nil, err
			}
			provider.SettledFees.Add(provider.SettledFees, amount)
			decl, ok := data.declarations[claim.DeclarationID]
			if !ok {
				return input, nil, nil, fmt.Errorf("foundation: counted claim %s names unavailable declaration %s", claim.ClaimID, claim.DeclarationID)
			}
			provider.DeclaredSeconds = append(provider.DeclaredSeconds, decl.RetentionSeconds)
			latest = &claim
		}
		for _, claim := range data.chain {
			if claim.Sequence > settledSequence {
				continue
			}
			periodEnd, err := wire.ParseTime(claim.PeriodEnd)
			if err == nil && !periodEnd.Before(start) && periodEnd.Before(end) {
				activeClaim = &claim
			}
			break
		}
		if activeClaim != nil {
			provider.ActiveFrom, _ = wire.ParseTime(activeClaim.PeriodStart)
		}
		if latest != nil {
			// The latest counted claim selects the payout recipient because the specification does not identify a declaration.
			provider.PayoutRecipient = distDeclarationRecipient(data.declarations[latest.DeclarationID], network)
			// An invalid declared address accrues the amount instead of making a payout impossible.
			if faux.ValidateRecipient(network, provider.PayoutRecipient) != nil {
				provider.PayoutRecipient = ""
			}
		}
		providerKeys := distProviderKeys(data.chain)
		for _, monitor := range monitorIDs {
			reports := data.reports[monitor]
			for _, report := range reports {
				if !distContains(providerKeys, report.ProviderID) {
					continue
				}
				reportStart, _ := wire.ParseTime(report.PeriodStart)
				reportEnd, _ := wire.ParseTime(report.PeriodEnd)
				created, _ := wire.ParseTime(report.CreatedAt)
				if report.MonitorID != monitor || created.After(cutoff) || reportStart.Before(start) || reportEnd.After(end) {
					continue
				}
				score := quality.ReferenceScore(report)
				var scoreInt *int
				if score != nil {
					v := int(*score)
					scoreInt = &v
				}
				provider.Reports = append(provider.Reports, distribution.ReportSpan{ReportID: report.ReportID, MonitorID: monitor, Start: reportStart, End: reportEnd, CreatedAt: created, Score: scoreInt})
			}
		}
		input.Providers = append(input.Providers, provider)
	}
	for _, monitor := range policy.Monitors {
		counted, witnessed := 0, 0
		for _, record := range countedRecords {
			periodEnd, _ := wire.ParseTime(record.Claim.PeriodEnd)
			inside := false
			for _, span := range membership[monitor] {
				if !periodEnd.Before(span.Start) && periodEnd.Before(span.End) {
					inside = true
					break
				}
			}
			if !inside || distClaimEquivocated(providerData, record.Claim) {
				continue
			}
			counted++
			for _, data := range providerData {
				if !distChainHas(data.chain, record.Claim.ClaimID) {
					continue
				}
				if distMonitorWitnessed(data.witnesses[monitor], monitor, record.Claim, cutoff) {
					witnessed++
				}
				break
			}
		}
		input.Seats[monitor] = distribution.SeatEvidence{Counted: counted, Witnessed: witnessed}
	}
	earlier, err := distEarlierStatements(s, network, end)
	if err != nil {
		return input, nil, nil, err
	}
	input.Earlier = earlier
	sets := []QualityReportSet{}
	for _, data := range providerData {
		if len(data.chain) == 0 {
			continue
		}
		for _, providerKey := range distProviderKeys(data.chain) {
			used := []wire.QualityReport{}
			for _, monitor := range monitorIDs {
				reports := data.reports[monitor]
				for _, report := range reports {
					if report.ProviderID == providerKey && distReportUsed(input.Providers, report.ReportID) {
						used = append(used, report)
					}
				}
			}
			sets = append(sets, QualityReportSet{ProviderID: providerKey, QuarterEnd: wire.FormatTime(end), Reports: used})
		}
	}
	return input, sets, countedRecords, nil
}

func (s *Service) distFetchProvider(ctx context.Context, client *sdk.Client, network string, monitorIDs []string, monitorURLs map[string]string, end time.Time) (distProviderData, error) {
	data := distProviderData{declarations: make(map[string]wire.RetentionDeclaration), successions: make(map[string]wire.KeySuccession), witnesses: make(map[string][]wire.SettlementWitness), equivocated: make(map[int64]bool), reports: make(map[string][]wire.QualityReport)}
	declarations, _, err := client.Retention(ctx)
	if err != nil {
		return data, err
	}
	for _, declaration := range declarations.Declarations {
		if err := retention.Verify(declaration); err != nil {
			return data, err
		}
		data.declarations[declaration.DeclarationID] = declaration
	}
	var after uint64
	for {
		page, _, err := client.SettlementClaims(ctx, network, after)
		if err != nil {
			return data, err
		}
		if len(page.Claims) == 0 {
			break
		}
		data.chain = append(data.chain, page.Claims...)
		lastSequence := uint64(page.Claims[len(page.Claims)-1].Sequence)
		if lastSequence <= after {
			return data, errors.New("foundation: provider claim page did not advance")
		}
		after = lastSequence
	}
	providerKeys := distProviderKeys(data.chain)
	for _, did := range providerKeys {
		successions, _, _, err := client.Successions(ctx, did)
		if err != nil {
			return data, err
		}
		for _, item := range successions {
			if err := succession.Verify(item); err != nil {
				return data, err
			}
			data.successions[item.SuccessionID] = item
		}
	}
	for i, claim := range data.chain {
		if claim.Network != network {
			return data, fmt.Errorf("foundation: provider returned a claim for network %s while querying %s", claim.Network, network)
		}
		if err := settlement.VerifyClaimObject(claim); err != nil {
			return data, err
		}
		var previous *wire.SettlementClaim
		if i > 0 {
			previous = &data.chain[i-1]
		}
		if err := settlement.CheckLink(claim, previous, func(id string) (wire.KeySuccession, bool) { value, ok := data.successions[id]; return value, ok }); err != nil {
			return data, err
		}
		if err := settlement.CheckDeclaration(claim, previous, func(id string) (wire.RetentionDeclaration, bool) {
			value, ok := data.declarations[id]
			return value, ok
		}); err != nil {
			return data, err
		}
	}
	if len(data.chain) == 0 {
		return data, nil
	}
	witnessMonitors := append([]string(nil), monitorIDs...)
	sort.Strings(witnessMonitors)
	for _, monitor := range witnessMonitors {
		baseURL := monitorURLs[monitor]
		client, err := sdk.New(baseURL, sdk.Options{ServiceID: monitor, AllowLoopbackHTTP: s.cfg.Dev, Now: s.Clock})
		if err != nil {
			return data, err
		}
		var afterSequence uint64
		for _, providerKey := range providerKeys {
			afterSequence = 0
			for {
				page, _, err := client.Witnesses(ctx, providerKey, network, afterSequence)
				if err != nil {
					return data, err
				}
				// Items that fail verification are not public evidence and are skipped, so one faulty monitor cannot stall distribution.
				var lastSequence uint64
				for _, witness := range page.Witnesses {
					if witness.Sequence > 0 && uint64(witness.Sequence) > lastSequence {
						lastSequence = uint64(witness.Sequence)
					}
					if witness.WitnessID != monitor || witness.Network != network || witness.ProviderID != providerKey || witness.Sequence > data.chain[len(data.chain)-1].Sequence || settlement.VerifyWitness(witness) != nil {
						continue
					}
					data.witnesses[monitor] = append(data.witnesses[monitor], witness)
				}
				for _, refusal := range page.Refusals {
					if refusal.Sequence > 0 && uint64(refusal.Sequence) > lastSequence {
						lastSequence = uint64(refusal.Sequence)
					}
				}
				for _, hold := range page.Holds {
					if hold.Sequence > 0 && uint64(hold.Sequence) > lastSequence {
						lastSequence = uint64(hold.Sequence)
					}
				}
				if lastSequence <= afterSequence {
					break
				}
				afterSequence = lastSequence
			}
		}
		for _, providerKey := range providerKeys {
			equivocations, _, err := client.Equivocations(ctx, providerKey)
			if err != nil {
				return data, err
			}
			for _, proof := range equivocations.Proofs {
				if settlement.VerifyEquivocation(proof) && distProofBelongs(data.chain, proof) {
					data.equivocated[proof.Claims[0].Sequence] = true
				}
			}
		}
		for _, providerKey := range providerKeys {
			reports, _, err := client.QualityReports(ctx, providerKey, wire.FormatTime(end))
			if err != nil {
				if ctx.Err() != nil {
					return data, ctx.Err()
				}
				// The specification gives a monitor without reports for a provider no score for it, so the statement continues without them.
				log.Printf("foundation distribution: quality reports for provider %s from monitor %s: %v", providerKey, monitor, err)
				continue
			}
			for _, report := range reports.Reports {
				if report.MonitorID != monitor || report.ProviderID != providerKey || quality.CheckReport(report) != nil {
					continue
				}
				// A report counts only when its evidence is served and its values recompute from it.
				if _, _, err := client.QualityEvidence(ctx, report); err != nil {
					if ctx.Err() != nil {
						return data, ctx.Err()
					}
					log.Printf("foundation distribution: report %s from monitor %s is not used: %v", report.ReportID, monitor, err)
					continue
				}
				data.reports[monitor] = append(data.reports[monitor], report)
			}
		}
	}
	return data, nil
}

func distProviderKeys(chain []wire.SettlementClaim) []string {
	seen := make(map[string]bool)
	keys := []string{}
	for _, claim := range chain {
		if !seen[claim.ProviderID] {
			seen[claim.ProviderID] = true
			keys = append(keys, claim.ProviderID)
		}
	}
	sort.Strings(keys)
	return keys
}

// distHighestSettledSequence returns the highest chain sequence with a quorum of one committee by cutoff, or 0;
// every claim at or below it is settled because a settled claim settles its ancestors.
func distHighestSettledSequence(data *distProviderData, policies []wire.DistributionPolicy, cutoff time.Time) int64 {
	byClaim := make(map[string][]wire.SettlementWitness)
	for _, witnesses := range data.witnesses {
		for _, witness := range witnesses {
			byClaim[witness.ClaimID] = append(byClaim[witness.ClaimID], witness)
		}
	}
	before := cutoff.Add(time.Second)
	for i := len(data.chain) - 1; i >= 0; i-- {
		candidate := data.chain[i]
		witnesses := byClaim[candidate.ClaimID]
		if len(witnesses) == 0 {
			continue
		}
		for _, committee := range distCommitteesIn(policies, distClaimTime(candidate), cutoff) {
			if settlement.Settled(settlement.CountWitnesses(candidate, witnesses, committee.Monitors, &before), committee.Monitors) {
				return candidate.Sequence
			}
		}
	}
	return 0
}

func distMonitorWitnessed(witnesses []wire.SettlementWitness, monitor string, claim wire.SettlementClaim, cutoff time.Time) bool {
	for _, witness := range witnesses {
		at, err := wire.ParseTime(witness.WitnessedAt)
		if witness.WitnessID == monitor && settlement.WitnessMatches(witness, claim) && err == nil && !at.After(cutoff) {
			return true
		}
	}
	return false
}

func distCommitteesIn(policies []wire.DistributionPolicy, start, end time.Time) []wire.DistributionPolicy {
	committees := make([]wire.DistributionPolicy, 0)
	first, ok := distribution.InForce(policies, start)
	if ok {
		committees = append(committees, first)
	}
	for _, policy := range policies {
		at, err := wire.ParseTime(policy.EffectiveAt)
		if err == nil && at.After(start) && !at.After(end) {
			committees = append(committees, policy)
		}
	}
	return committees
}

func distChainHas(chain []wire.SettlementClaim, id string) bool {
	for _, claim := range chain {
		if claim.ClaimID == id {
			return true
		}
	}
	return false
}

func distProofBelongs(chain []wire.SettlementClaim, proof wire.EquivocationProof) bool {
	if distChainHas(chain, proof.Claims[0].ClaimID) || distChainHas(chain, proof.Claims[1].ClaimID) {
		return true
	}
	if proof.Predecessor == nil || !distChainHas(chain, proof.Predecessor.ClaimID) {
		return false
	}
	for _, claim := range proof.Claims {
		if claim.PreviousClaimID != nil && *claim.PreviousClaimID == proof.Predecessor.ClaimID {
			return true
		}
	}
	return false
}

func distClaimEquivocated(data []distProviderData, claim wire.SettlementClaim) bool {
	for _, provider := range data {
		for _, item := range provider.chain {
			if item.ClaimID == claim.ClaimID {
				return provider.equivocated[claim.Sequence]
			}
		}
	}
	return false
}

func distContains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func distReportUsed(providers []distribution.ProviderInput, id string) bool {
	for _, provider := range providers {
		for _, report := range provider.Reports {
			if report.ReportID == id {
				return true
			}
		}
	}
	return false
}

func distClaimTime(c wire.SettlementClaim) time.Time {
	value, _ := wire.ParseTime(c.PeriodEnd)
	return value
}

// distDeclarationRecipient chooses the named network address from the latest counted claim's declaration.
func distDeclarationRecipient(declaration wire.RetentionDeclaration, network string) string {
	if network == wire.BitcoinMainnet {
		return declaration.PayoutRecipients.BitcoinMainnet
	}
	return declaration.PayoutRecipients.EthereumMainnet
}

func distEarlierStatements(s *Service, network string, end time.Time) ([]wire.DistributionStatement, error) {
	statements := []wire.DistributionStatement{}
	quarterEnd, _ := s.Calendar().Quarter(end.Add(-time.Second))
	for !quarterEnd.Before(s.FirstStarted()) {
		data, ok := s.DistributionStatement(network, wire.FormatTime(quarterEnd))
		if ok {
			var statement wire.DistributionStatement
			if err := json.Unmarshal(data, &statement); err != nil {
				return nil, fmt.Errorf("foundation: invalid earlier distribution statement for %s: %w", quarterEnd, err)
			}
			statements = append(statements, statement)
		}
		quarterEnd, _ = s.Calendar().Quarter(quarterEnd.Add(-time.Second))
	}
	return statements, nil
}
