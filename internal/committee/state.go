package committee

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/retention"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/succession"
	"foryou.local/foryou/internal/wire"
)

type monitorState struct {
	Claims             map[string]wire.SettlementClaim             `json:"claims"`
	Witnessed          map[string]wire.SettlementWitness           `json:"witnessed"`
	WitnessLeaves      map[string][]wire.SettlementLeaf            `json:"witness_leaves"`
	Refusals           map[string]refusalRecord                    `json:"refusals"`
	UnverifiedRefusals map[string]refusalRecord                    `json:"unverified_refusals"`
	Holds              map[string]wire.SettlementHold              `json:"holds"`
	HeldLeaves         map[string]map[string][]wire.SettlementLeaf `json:"held_leaves"`
	Followed           map[string]wire.SettlementClaim             `json:"followed"`
	Equivocations      [][2]wire.SettlementClaim                   `json:"equivocations"`
	FirstSeen          map[string]time.Time                        `json:"first_seen"`
	Declarations       map[string]wire.RetentionDeclaration        `json:"declarations"`
	Successions        []wire.KeySuccession                        `json:"successions"`
	ReceiptIndex       map[string]map[string]int64                 `json:"receipt_index"`
}

type refusalRecord struct {
	Refusal       wire.SettlementRefusal `json:"refusal"`
	Claim         wire.SettlementClaim   `json:"claim"`
	Leaves        []wire.SettlementLeaf  `json:"leaves"`
	Recheck       bool                   `json:"recheck"`
	Dependency    dependency             `json:"dependency"`
	Held          bool                   `json:"held,omitempty"`
	FirstUnfunded *time.Time             `json:"first_unfunded,omitempty"`
}

type snapshot struct {
	State monitorState `json:"state"`
}

func newMonitorState() monitorState {
	return monitorState{
		Claims: make(map[string]wire.SettlementClaim), Witnessed: make(map[string]wire.SettlementWitness),
		WitnessLeaves: make(map[string][]wire.SettlementLeaf), Refusals: make(map[string]refusalRecord), UnverifiedRefusals: make(map[string]refusalRecord),
		Holds: make(map[string]wire.SettlementHold), HeldLeaves: make(map[string]map[string][]wire.SettlementLeaf), Followed: make(map[string]wire.SettlementClaim),
		Equivocations: make([][2]wire.SettlementClaim, 0), FirstSeen: make(map[string]time.Time),
		Declarations: make(map[string]wire.RetentionDeclaration), Successions: make([]wire.KeySuccession, 0),
		ReceiptIndex: make(map[string]map[string]int64),
	}
}

func slotKey(line, network string, sequence int64) string {
	return fmt.Sprintf("%s\x00%s\x00%d", line, network, sequence)
}

func cloneClaim(c wire.SettlementClaim) wire.SettlementClaim {
	if c.PreviousClaimID != nil {
		id := *c.PreviousClaimID
		c.PreviousClaimID = &id
	}
	if c.SuccessionID != nil {
		id := *c.SuccessionID
		c.SuccessionID = &id
	}
	return c
}
func chainKey(line, network string) string { return line + "\x00" + network }

func refusalKey(c wire.SettlementClaim) string {
	return fmt.Sprintf("%s\x00%s\x00%d\x00%s", c.ProviderID, c.Network, c.Sequence, c.ClaimID)
}

func (m *Monitor) recordAdopted(c wire.SettlementClaim, leaves []wire.SettlementLeaf) {
	m.state.Claims[c.ClaimID] = cloneClaim(c)
	m.state.WitnessLeaves[c.ClaimID] = append([]wire.SettlementLeaf(nil), leaves...)
	chain := chainKey(m.Line(c.ProviderID), c.Network)
	if m.state.ReceiptIndex[chain] == nil {
		m.state.ReceiptIndex[chain] = make(map[string]int64)
	}
	for _, leaf := range leaves {
		m.state.ReceiptIndex[chain][leaf.ReceiptHash] = c.Sequence
	}
	m.markDependentClaimsDue(c)
	for slot := range m.state.HeldLeaves {
		if _, held := m.state.Holds[slot]; !held {
			delete(m.state.HeldLeaves, slot)
		}
	}
}

func (m *Monitor) markDependentClaimsDue(predecessor wire.SettlementClaim) {
	for key, record := range m.state.Refusals {
		if record.Dependency == dependencyPredecessor && record.Claim.Sequence == predecessor.Sequence+1 && record.Claim.Network == predecessor.Network && m.Line(record.Claim.ProviderID) == m.Line(predecessor.ProviderID) {
			record.Recheck = true
			record.Dependency = dependencyNone
			m.state.Refusals[key] = record
		}
		if record.Held && record.Claim.Sequence == predecessor.Sequence+1 && record.Claim.Network == predecessor.Network && m.Line(record.Claim.ProviderID) == m.Line(predecessor.ProviderID) {
			record.Recheck = true
			m.state.Refusals[key] = record
		}
	}
}

func (m *Monitor) markDataAvailable(kind dependency) {
	for key, record := range m.state.Refusals {
		// New public data may prove a held claim invalid, so a refusal under a hold is audited again too.
		if record.Dependency == kind || record.Held {
			record.Recheck = true
			m.state.Refusals[key] = record
		}
	}
}

// ReauditDue returns claims whose refusals need another audit based on time or newly held data.
func (m *Monitor) ReauditDue(now time.Time) []ClaimRef {
	var due []ClaimRef
	for _, record := range m.state.Refusals {
		line := m.Line(record.Claim.ProviderID)
		slot := slotKey(line, record.Claim.Network, record.Claim.Sequence)
		if _, witnessed := m.state.Witnessed[slot]; witnessed {
			continue
		}
		if _, followed := m.state.Followed[slot]; followed {
			continue
		}
		refusedAt, _ := wire.ParseTime(record.Refusal.RefusedAt)
		ready := record.Recheck
		if _, held := m.state.Holds[slot]; record.Held && !held {
			ready = true
		}
		if record.Dependency == dependencySchedule {
			ready = len(record.Leaves) > 0
			for _, leaf := range record.Leaves {
				if _, ok := m.schedules.Lookup(leaf.ScheduleID); !ok {
					ready = false
					break
				}
			}
		}
		if record.Refusal.Reason == string(settlement.ReasonUnfunded) && record.FirstUnfunded != nil {
			elapsed := now.Sub(*record.FirstUnfunded)
			ready = elapsed >= 24*time.Hour && elapsed <= 7*24*time.Hour && now.Sub(refusedAt) >= 24*time.Hour
		}
		if ready {
			due = append(due, ClaimRef{ProviderID: record.Claim.ProviderID, Network: record.Claim.Network, ClaimID: record.Claim.ClaimID, Sequence: record.Claim.Sequence})
		}
	}
	sort.Slice(due, func(i, j int) bool {
		if due[i].ProviderID != due[j].ProviderID {
			return due[i].ProviderID < due[j].ProviderID
		}
		if due[i].Network != due[j].Network {
			return due[i].Network < due[j].Network
		}
		if due[i].Sequence != due[j].Sequence {
			return due[i].Sequence < due[j].Sequence
		}
		return due[i].ClaimID < due[j].ClaimID
	})
	return due
}

// Claim returns a signed claim object held by this monitor.
func (m *Monitor) Claim(claimID string) (wire.SettlementClaim, bool) {
	c, ok := m.state.Claims[claimID]
	return cloneClaim(c), ok
}

// Page returns this monitor's settlement records for one provider line and network.
func (m *Monitor) Page(providerID, network string, afterSequence int64, limit int) wire.MonitorWitnessesPage {
	page := wire.MonitorWitnessesPage{Witnesses: []wire.SettlementWitness{}, Refusals: []wire.SettlementRefusal{}, Holds: []wire.SettlementHold{}, Claims: []wire.SettlementClaim{}}
	if limit <= 0 {
		return page
	}
	line := m.Line(providerID)
	type entry struct {
		seq     int64
		id      string
		witness *wire.SettlementWitness
		refusal *wire.SettlementRefusal
		hold    *wire.SettlementHold
	}
	entries := make([]entry, 0)
	for slot, w := range m.state.Witnessed {
		if slotHasPrefix(slot, line, network) && w.Sequence > afterSequence {
			copy := w
			entries = append(entries, entry{seq: w.Sequence, id: w.ClaimID, witness: &copy})
		}
	}
	for _, r := range m.state.Refusals {
		if m.Line(r.Claim.ProviderID) == line && r.Claim.Network == network && r.Claim.Sequence > afterSequence {
			copy := r.Refusal
			entries = append(entries, entry{seq: copy.Sequence, id: copy.ClaimID, refusal: &copy})
		}
	}
	for _, r := range m.state.UnverifiedRefusals {
		if m.Line(r.Claim.ProviderID) == line && r.Claim.Network == network && r.Claim.Sequence > afterSequence {
			copy := r.Refusal
			entries = append(entries, entry{seq: copy.Sequence, id: copy.ClaimID, refusal: &copy})
		}
	}
	for slot, h := range m.state.Holds {
		if slotHasPrefix(slot, line, network) && h.Sequence > afterSequence {
			copy := h
			entries = append(entries, entry{seq: h.Sequence, id: slot, hold: &copy})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].seq != entries[j].seq {
			return entries[i].seq < entries[j].seq
		}
		return entries[i].id < entries[j].id
	})
	type pageCounts struct{ witnesses, refusals, holds int }
	counts := make(map[int64]pageCounts)
	for _, item := range entries {
		group := counts[item.seq]
		if item.witness != nil {
			group.witnesses++
		}
		if item.refusal != nil {
			group.refusals++
		}
		if item.hold != nil {
			group.holds++
		}
		counts[item.seq] = group
	}
	accepted := make(map[int64]bool)
	used := pageCounts{}
	for _, item := range entries {
		if accepted[item.seq] {
			continue
		}
		group := counts[item.seq]
		if used.witnesses+group.witnesses <= limit && used.refusals+group.refusals <= limit && used.holds+group.holds <= limit {
			accepted[item.seq] = true
			used.witnesses += group.witnesses
			used.refusals += group.refusals
			used.holds += group.holds
		} else {
			break
		}
	}
	for _, item := range entries {
		if !accepted[item.seq] {
			continue
		}
		if item.witness != nil {
			page.Witnesses = append(page.Witnesses, *item.witness)
			if c, ok := m.state.Claims[item.witness.ClaimID]; ok {
				page.Claims = appendClaimOnce(page.Claims, c)
			}
		}
		if item.refusal != nil {
			page.Refusals = append(page.Refusals, *item.refusal)
		}
		if item.hold != nil {
			page.Holds = append(page.Holds, *item.hold)
			for _, id := range item.hold.ClaimIDs {
				if c, ok := m.state.Claims[id]; ok {
					page.Claims = appendClaimOnce(page.Claims, c)
				}
			}
		}
	}
	return page
}

func slotHasPrefix(slot, line, network string) bool {
	prefix := line + "\x00" + network + "\x00"
	return len(slot) >= len(prefix) && slot[:len(prefix)] == prefix
}
func appendClaimOnce(claims []wire.SettlementClaim, claim wire.SettlementClaim) []wire.SettlementClaim {
	for _, existing := range claims {
		if existing.ClaimID == claim.ClaimID {
			return claims
		}
	}
	return append(claims, cloneClaim(claim))
}

// Snapshot encodes the complete monitor state as JSON for journaling.
func (m *Monitor) Snapshot() ([]byte, error) { return json.Marshal(snapshot{State: m.state}) }

// Restore reconstructs a monitor and re-verifies every signed object in its snapshot.
func Restore(cfg Config, data []byte) (*Monitor, error) {
	monitor, err := New(cfg)
	if err != nil {
		return nil, err
	}
	parsed, err := canon.Parse(data, canon.Limits{MaxDepth: 64})
	if err != nil {
		return nil, fmt.Errorf("committee: invalid snapshot: %w", err)
	}
	encoded, err := canon.Marshal(parsed)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var saved snapshot
	if err := decoder.Decode(&saved); err != nil {
		return nil, err
	}
	if err := normalizeState(&saved.State); err != nil {
		return nil, err
	}
	monitor.state = saved.State
	line := succession.NewProviderLine()
	for _, s := range monitor.state.Successions {
		// Each stored succession passed this historical signed-claims check when accepted.
		if err := line.Accept(s, func(string) bool { return false }); err != nil {
			return nil, fmt.Errorf("committee: invalid succession state: %w", err)
		}
	}
	monitor.line = line
	for id, d := range monitor.state.Declarations {
		if id != d.DeclarationID || retention.Verify(d) != nil {
			return nil, errors.New("committee: invalid declaration state")
		}
	}
	for id, c := range monitor.state.Claims {
		if id != c.ClaimID || settlement.VerifyClaimObject(c) != nil {
			return nil, errors.New("committee: invalid claim state")
		}
	}
	expectedReceipts := make(map[string]map[string]int64)
	for slot, w := range monitor.state.Witnessed {
		c, ok := monitor.state.Claims[w.ClaimID]
		if !ok || w.WitnessID != monitor.ID() || !settlement.WitnessMatches(w, c) || settlement.VerifyWitness(w) != nil || slot != slotKey(monitor.Line(c.ProviderID), c.Network, c.Sequence) {
			return nil, errors.New("committee: invalid witness state")
		}
		leaves, ok := monitor.state.WitnessLeaves[c.ClaimID]
		if !ok || len(leaves) > 0 && settlement.CheckTotals(c, leaves) != nil {
			return nil, errors.New("committee: invalid witnessed leaves")
		}
		chain := chainKey(monitor.Line(c.ProviderID), c.Network)
		if expectedReceipts[chain] == nil {
			expectedReceipts[chain] = make(map[string]int64)
		}
		for _, leaf := range leaves {
			if sequence, exists := expectedReceipts[chain][leaf.ReceiptHash]; exists && sequence != c.Sequence {
				return nil, errors.New("committee: invalid receipt index")
			}
			expectedReceipts[chain][leaf.ReceiptHash] = c.Sequence
		}
	}
	if len(expectedReceipts) != len(monitor.state.ReceiptIndex) {
		return nil, errors.New("committee: invalid receipt index")
	}
	for chain, hashes := range expectedReceipts {
		stored := monitor.state.ReceiptIndex[chain]
		if len(stored) != len(hashes) {
			return nil, errors.New("committee: invalid receipt index")
		}
		for hash, sequence := range hashes {
			if stored[hash] != sequence {
				return nil, errors.New("committee: invalid receipt index")
			}
		}
	}
	for key, r := range monitor.state.Refusals {
		claimErr := settlement.VerifyClaimObject(r.Claim)
		badSignatureRefusal := r.Refusal.Reason == string(settlement.ReasonBadSignature)
		if r.Refusal.WitnessID != monitor.ID() || settlement.VerifyRefusal(r.Refusal) != nil || r.Refusal.ClaimID != r.Claim.ClaimID || claimErr != nil || badSignatureRefusal || key != refusalKey(r.Claim) {
			return nil, errors.New("committee: invalid refusal state")
		}
	}
	for key, r := range monitor.state.UnverifiedRefusals {
		if r.Refusal.WitnessID != monitor.ID() || settlement.VerifyRefusal(r.Refusal) != nil || r.Refusal.ClaimID != r.Claim.ClaimID || settlement.VerifyClaimObject(r.Claim) == nil || (r.Refusal.Reason != string(settlement.ReasonBadSignature) && r.Refusal.Reason != string(settlement.ReasonBadChain)) || key != refusalKey(r.Claim) {
			return nil, errors.New("committee: invalid unverified-claim refusal state")
		}
	}
	for slot, h := range monitor.state.Holds {
		if h.WitnessID != monitor.ID() || settlement.VerifyHold(h) != nil || slot != slotKey(monitor.Line(h.ProviderID), h.Network, h.Sequence) {
			return nil, errors.New("committee: invalid hold state")
		}
		for _, claimID := range h.ClaimIDs {
			if _, ok := monitor.state.Claims[claimID]; !ok {
				return nil, errors.New("committee: hold refers to unknown claim")
			}
		}
	}
	for slot, byClaim := range monitor.state.HeldLeaves {
		h, ok := monitor.state.Holds[slot]
		if !ok {
			return nil, errors.New("committee: held leaves without a hold")
		}
		for claimID := range byClaim {
			if !contains(h.ClaimIDs, claimID) {
				return nil, errors.New("committee: held leaves for a claim the hold does not name")
			}
		}
	}
	for _, pair := range monitor.state.Equivocations {
		if settlement.VerifyClaimObject(pair[0]) != nil || settlement.VerifyClaimObject(pair[1]) != nil {
			return nil, errors.New("committee: invalid equivocation state")
		}
	}
	for key, c := range monitor.state.Followed {
		if settlement.VerifyClaimObject(c) != nil || key != slotKey(monitor.Line(c.ProviderID), c.Network, c.Sequence) {
			return nil, errors.New("committee: invalid followed claim state")
		}
	}
	return monitor, nil
}

func normalizeState(s *monitorState) error {
	if s.Claims == nil || s.Witnessed == nil || s.WitnessLeaves == nil || s.Refusals == nil || s.UnverifiedRefusals == nil || s.Holds == nil || s.Followed == nil || s.FirstSeen == nil || s.Declarations == nil || s.ReceiptIndex == nil {
		return errors.New("committee: incomplete snapshot state")
	}
	if s.Successions == nil {
		s.Successions = []wire.KeySuccession{}
	}
	if s.Equivocations == nil {
		s.Equivocations = [][2]wire.SettlementClaim{}
	}
	if s.HeldLeaves == nil {
		s.HeldLeaves = make(map[string]map[string][]wire.SettlementLeaf)
	}
	return nil
}
