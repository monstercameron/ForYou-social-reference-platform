package monitor

import (
	"context"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"foryou.local/foryou/internal/committee"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

const alAuditCollectionName = "audit.state"

var alAuditValues = store.Collection[alAuditState]{Name: alAuditCollectionName}
var alMaxClaimsPerPass = 100

type alAuditState struct {
	Fetched   uint64                   `json:"fetched"`
	NextIndex uint64                   `json:"next_index"`
	Providers []string                 `json:"providers"`
	Pending   []wire.SettlementWitness `json:"pending"`
	Undecided []alUndecidedClaim       `json:"undecided"`
}

type alUndecidedClaim struct {
	Claim     wire.SettlementClaim `json:"claim"`
	FirstSeen time.Time            `json:"first_seen"`
	Handover  bool                 `json:"handover,omitempty"`
}

func init() {
	loopSteps = append(loopSteps, loopStep{order: auditOrder, name: "settlement audit", run: alAudit})
}

func alAudit(s *Service, ctx context.Context) error {
	now := s.Now()
	providers := s.Providers()
	declarationsByProvider := make(map[string][]wire.RetentionDeclaration, len(providers))
	members := s.Members()
	if len(members) > 0 {
		for _, providerURL := range providers {
			client, err := alProviderClient(s, members[0], providerURL)
			if err != nil {
				log.Printf("monitor: provider client %s: %v", providerURL, err)
				continue
			}
			declarations, _, err := client.Retention(ctx)
			if err != nil {
				log.Printf("monitor: fetch retention from %s: %v", providerURL, err)
				continue
			}
			declarationsByProvider[providerURL] = declarations.Declarations
		}
	}
	for _, member := range members {
		for _, providerURL := range providers {
			client, err := alProviderClient(s, member, providerURL)
			if err != nil {
				log.Printf("monitor: provider client %s: %v", providerURL, err)
				continue
			}
			for _, declaration := range declarationsByProvider[providerURL] {
				if err := member.Do(func(core *committee.Monitor) error { return core.RegisterDeclaration(declaration) }); err != nil {
					log.Printf("monitor: reject retention declaration %s: %v", declaration.DeclarationID, err)
				}
			}
			if !s.View().IsMember(member.DID(), now) {
				continue
			}
			joinedAt, joined := s.View().JoinedAt(member.DID(), now)
			for _, network := range []string{wire.BitcoinMainnet, wire.EthereumMainnet} {
				if peerAuditBlocked(s, member, providerURL, network, now) {
					continue
				}
				alAuditNetwork(s, ctx, member, client, providerURL, network, now, joinedAt, joined)
			}
		}
		alDeliverPending(s, ctx, member)
		alLogUndecided(member, now)
	}
	return nil
}

// alProviderClient uses the member DID as a canonical SDK pin because provider URLs have no identity mapping in Service; claim objects and dependencies are still verified independently.
func alProviderClient(s *Service, member *Member, providerURL string) (*sdk.Client, error) {
	if _, err := url.ParseRequestURI(providerURL); err != nil {
		return nil, err
	}
	return sdk.New(providerURL, sdk.Options{ServiceID: member.DID(), AllowLoopbackHTTP: true, HTTPClient: s.HTTPClient(), Now: s.config.Now})
}

func alStateKey(providerURL, network string) string { return providerURL + "\x00" + network }

func alReadState(member *Member, key string) (alAuditState, error) {
	var state alAuditState
	err := member.DB().View(func(tx store.Tx) error {
		var ok bool
		var err error
		state, ok, err = alAuditValues.Get(tx, key)
		if !ok && err == nil {
			state.Pending = []wire.SettlementWitness{}
			state.Undecided = []alUndecidedClaim{}
		}
		return err
	})
	return state, err
}

func alWriteState(member *Member, key string, state alAuditState) error {
	return member.DB().Update(func(tx store.Tx) error { return alAuditValues.Put(tx, key, state) })
}

func alAuditNetwork(s *Service, ctx context.Context, member *Member, client *sdk.Client, providerURL, network string, now, joinedAt time.Time, joined bool) {
	key := alStateKey(providerURL, network)
	state, err := alReadState(member, key)
	if err != nil {
		log.Printf("monitor: read audit state: %v", err)
		return
	}
	for _, claim := range alFetchClaims(ctx, client, providerURL, network, state.Fetched, alMaxClaimsPerPass) {
		var seen time.Time
		if err := member.Do(func(core *committee.Monitor) error { seen = core.Observe(claim.ClaimID, now); return nil }); err != nil {
			log.Printf("monitor: observe claim %s: %v", claim.ClaimID, err)
			break
		}
		state.Undecided = append(state.Undecided, alUndecidedClaim{Claim: claim, FirstSeen: seen})
		state.Providers = alAddProvider(state.Providers, claim.ProviderID)
		state.Fetched = uint64(claim.Sequence)
	}
	due := alDueWork(member, &state, network, now)
	audited := 0
	for _, entry := range due {
		if audited >= alMaxClaimsPerPass {
			break
		}
		audited++
		if outcome, ok := alAuditClaim(ctx, member, client, entry, now); ok && outcome.Witness != nil {
			state.Pending = alUpsertWitness(state.Pending, *outcome.Witness)
		}
	}
	decided := make(map[string]bool)
	count := len(state.Undecided)
	for offset := 0; offset < count && audited < alMaxClaimsPerPass; offset++ {
		index := (int(state.NextIndex%uint64(count)) + offset) % count
		entry := state.Undecided[index]
		if entry.Handover {
			continue
		}
		if periodEnd, parseErr := wire.ParseTime(entry.Claim.PeriodEnd); joined && parseErr == nil && periodEnd.Before(joinedAt) {
			state.Undecided[index].Handover = true
			continue
		}
		audited++
		outcome, ok := alAuditClaim(ctx, member, client, entry, now)
		if !ok {
			continue
		}
		decided[entry.Claim.ClaimID] = true
		if outcome.Witness != nil {
			state.Pending = alUpsertWitness(state.Pending, *outcome.Witness)
		}
	}
	if count > 0 {
		state.NextIndex = (state.NextIndex + uint64(audited)) % uint64(count)
	}
	remaining := make([]alUndecidedClaim, 0, len(state.Undecided))
	for _, entry := range state.Undecided {
		if !decided[entry.Claim.ClaimID] {
			remaining = append(remaining, entry)
		}
	}
	state.Undecided = remaining
	if err := alWriteState(member, key, state); err != nil {
		log.Printf("monitor: persist audit state: %v", err)
	}
}

// alDueWork drops handover claims the member now holds and returns the refused claims of this provider and network that are due for another audit.
func alDueWork(member *Member, state *alAuditState, network string, now time.Time) []alUndecidedClaim {
	var work []alUndecidedClaim
	if err := member.Do(func(core *committee.Monitor) error {
		kept := make([]alUndecidedClaim, 0, len(state.Undecided))
		for _, entry := range state.Undecided {
			if _, held := core.Claim(entry.Claim.ClaimID); !entry.Handover || !held {
				kept = append(kept, entry)
			}
		}
		state.Undecided = kept
		for _, ref := range core.ReauditDue(now) {
			if ref.Network != network || !alHasProvider(state.Providers, ref.ProviderID) {
				continue
			}
			if claim, ok := core.Claim(ref.ClaimID); ok {
				work = append(work, alUndecidedClaim{Claim: claim, FirstSeen: core.Observe(ref.ClaimID, now)})
			}
		}
		return nil
	}); err != nil {
		log.Printf("monitor: list claims for re-audit: %v", err)
	}
	return work
}

func alAuditClaim(ctx context.Context, member *Member, client *sdk.Client, entry alUndecidedClaim, now time.Time) (committee.Outcome, bool) {
	leaves, ok := alFetchLeaves(ctx, client, entry.Claim)
	if !ok {
		return committee.Outcome{}, false
	}
	alFetchSuccession(ctx, member, client, entry.Claim)
	var outcome committee.Outcome
	if err := member.Do(func(core *committee.Monitor) error {
		var auditErr error
		outcome, auditErr = core.Audit(entry.Claim, leaves, entry.FirstSeen, now)
		return auditErr
	}); err != nil {
		log.Printf("monitor: audit claim %s: %v", entry.Claim.ClaimID, err)
		return committee.Outcome{}, false
	}
	return outcome, true
}

func alAddProvider(providers []string, providerID string) []string {
	if alHasProvider(providers, providerID) {
		return providers
	}
	return append(providers, providerID)
}

func alHasProvider(providers []string, providerID string) bool {
	for _, provider := range providers {
		if provider == providerID {
			return true
		}
	}
	return false
}

func alLogUndecided(member *Member, now time.Time) {
	if err := member.DB().View(func(tx store.Tx) error {
		return alAuditValues.Scan(tx, store.Range{}, func(_ string, state alAuditState) (bool, error) {
			for _, claim := range state.Undecided {
				if !claim.Handover && now.Sub(claim.FirstSeen) >= 24*time.Hour {
					log.Printf("monitor: claim %s remains undecided after 24 hours", claim.Claim.ClaimID)
				}
			}
			return true, nil
		})
	}); err != nil {
		log.Printf("monitor: log undecided claims: %v", err)
	}
}

func alFetchClaims(ctx context.Context, client *sdk.Client, providerURL, network string, after uint64, limit int) []wire.SettlementClaim {
	claims := make([]wire.SettlementClaim, 0, limit)
	for len(claims) < limit {
		page, _, err := client.SettlementClaims(ctx, network, after)
		if err != nil {
			log.Printf("monitor: fetch claims from %s: %v", providerURL, err)
			break
		}
		if len(page.Claims) == 0 {
			break
		}
		for _, claim := range page.Claims {
			if claim.Network == network {
				claims = append(claims, claim)
			}
			if len(claims) == limit {
				break
			}
		}
		last := page.Claims[len(page.Claims)-1].Sequence
		if last <= 0 || uint64(last) <= after {
			break
		}
		after = uint64(last)
		if len(page.Claims) < 100 {
			break
		}
	}
	return claims
}

func alFetchLeaves(ctx context.Context, client *sdk.Client, claim wire.SettlementClaim) ([]wire.SettlementLeaf, bool) {
	leaves := make([]wire.SettlementLeaf, 0, claim.ReceiptCount)
	for offset := uint64(0); int64(len(leaves)) < claim.ReceiptCount; {
		page, _, err := client.ClaimLeaves(ctx, claim.ClaimID, offset)
		if err != nil || len(page.Leaves) == 0 || len(page.Leaves) > 2000 {
			return nil, false
		}
		leaves = append(leaves, page.Leaves...)
		if int64(len(leaves)) > claim.ReceiptCount {
			return nil, false
		}
		offset += uint64(len(page.Leaves))
	}
	return leaves, int64(len(leaves)) == claim.ReceiptCount
}

func alFetchSuccession(ctx context.Context, member *Member, client *sdk.Client, claim wire.SettlementClaim) {
	if claim.PreviousClaimID == nil || claim.Sequence <= 1 {
		return
	}
	var previous wire.SettlementClaim
	var held bool
	if err := member.Do(func(core *committee.Monitor) error { previous, held = core.Claim(*claim.PreviousClaimID); return nil }); err != nil {
		return
	}
	if !held {
		page, _, err := client.SettlementClaims(ctx, claim.Network, uint64(claim.Sequence-2))
		if err != nil {
			log.Printf("monitor: fetch predecessor for succession %s: %v", claim.ClaimID, err)
			return
		}
		for _, candidate := range page.Claims {
			if candidate.Sequence == claim.Sequence-1 && candidate.ClaimID == *claim.PreviousClaimID {
				previous = candidate
				held = true
				break
			}
		}
	}
	if !held || previous.ProviderID == claim.ProviderID {
		return
	}
	successions, _, _, err := client.Successions(ctx, previous.ProviderID)
	if err != nil {
		log.Printf("monitor: fetch succession for %s: %v", previous.ProviderID, err)
		return
	}
	for _, succession := range successions {
		if err := member.Do(func(core *committee.Monitor) error { return core.RegisterSuccession(succession) }); err != nil {
			log.Printf("monitor: reject succession %s: %v", succession.SuccessionID, err)
		}
	}
}

func alUpsertWitness(pending []wire.SettlementWitness, witness wire.SettlementWitness) []wire.SettlementWitness {
	for i := range pending {
		if pending[i].WitnessID == witness.WitnessID && pending[i].ClaimID == witness.ClaimID {
			pending[i] = witness
			return pending
		}
	}
	return append(pending, witness)
}

func alDeliverPending(s *Service, ctx context.Context, member *Member) {
	var states []struct {
		key   string
		state alAuditState
	}
	if err := member.DB().View(func(tx store.Tx) error {
		return alAuditValues.Scan(tx, store.Range{}, func(key string, state alAuditState) (bool, error) {
			states = append(states, struct {
				key   string
				state alAuditState
			}{key, state})
			return true, nil
		})
	}); err != nil {
		log.Printf("monitor: read pending witnesses: %v", err)
		return
	}
	sort.Slice(states, func(i, j int) bool { return states[i].key < states[j].key })
	for _, stored := range states {
		providerURL, _, ok := strings.Cut(stored.key, "\x00")
		if !ok {
			log.Printf("monitor: invalid pending witness key")
			continue
		}
		client, err := alProviderClient(s, member, providerURL)
		if err != nil {
			continue
		}
		for _, witness := range stored.state.Pending {
			status, err := client.DeliverWitness(ctx, witness)
			if err != nil || status != http.StatusCreated && status != http.StatusOK {
				continue
			}
			stored.state.Pending = alRemoveWitness(stored.state.Pending, witness)
			if err := alWriteState(member, stored.key, stored.state); err != nil {
				log.Printf("monitor: persist witness delivery: %v", err)
			}
		}
	}
}

func alRemoveWitness(pending []wire.SettlementWitness, witness wire.SettlementWitness) []wire.SettlementWitness {
	result := pending[:0]
	for _, current := range pending {
		if current.WitnessID != witness.WitnessID || current.ClaimID != witness.ClaimID {
			result = append(result, current)
		}
	}
	return result
}
