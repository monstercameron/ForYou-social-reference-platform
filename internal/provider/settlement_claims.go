package provider

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math/big"
	"net/http"
	"sort"
	"sync"
	"time"

	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/funding"
	"foryou.local/foryou/internal/retention"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

const (
	settlementReceiptPageSize = 1000
	settlementMaxLeaves       = 10000
	// settlementClockSkew bounds how far before the claim run a later receipt's issued_at
	// can lie: the clock may step back 60 seconds (CheckClock) and an admission's time
	// may be 60 seconds before the clock. A period is claimed only after both.
	settlementClockSkew    = 2 * time.Minute
	settlementPublishLimit = 24 * time.Hour
)

var settlementNetworks = [...]string{wire.BitcoinMainnet, wire.EthereumMainnet}

type settlementSchedules struct{ schedules []wire.FeeSchedule }

func (s settlementSchedules) Lookup(id string) (settlement.Schedule, bool) {
	for _, schedule := range s.schedules {
		if schedule.ScheduleID == id {
			base, baseOK := new(big.Int).SetString(schedule.BaseFeeBaseUnits, 10)
			rate, rateOK := new(big.Int).SetString(schedule.PerKiBFeeBaseUnits, 10)
			return settlement.Schedule{BaseFee: base, PerKiBFee: rate}, baseOK && rateOK
		}
	}
	return settlement.Schedule{}, false
}

func (s settlementSchedules) InForceWithin(id, network string, at time.Time) bool {
	boundary := at.Add(-time.Hour)
	if current, err := fee.InForce(s.schedules, network, boundary); err == nil && current.ScheduleID == id {
		return true
	}
	for _, schedule := range s.schedules {
		if schedule.ScheduleID != id || schedule.Network != network {
			continue
		}
		effective, err := wire.ParseTime(schedule.EffectiveAt)
		if err == nil && effective.After(boundary) && !effective.After(at) {
			return true
		}
	}
	return false
}

var _ settlement.Schedules = settlementSchedules{}

type settlementReceipt struct {
	entry  funding.ReceiptEntry
	leaf   wire.SettlementLeaf
	issued time.Time
}

type settlementPeriod struct {
	start, end time.Time
	receipts   []settlementReceipt
}

// settlementClaimsMu serializes claim publication with marking a provider-caused
// finalization failure, so a receipt is either claimed or marked, never both.
var settlementClaimsMu sync.Mutex

func init() {
	routeGroups = append(routeGroups, func(s *Service, _ *http.ServeMux) {
		s.Every("settlement-claims", time.Minute, func(ctx context.Context, now time.Time) error {
			settlementClaimsMu.Lock()
			defer settlementClaimsMu.Unlock()
			return s.publishSettlementClaims(ctx, now)
		})
	})
}

func (s *Service) publishSettlementClaims(ctx context.Context, now time.Time) error {
	s.retainSettledRecords()
	var errs []error
	for _, network := range settlementNetworks {
		if err := s.publishNetworkClaims(ctx, network, now); err != nil {
			errs = append(errs, fmt.Errorf("provider settlement %s: %w", network, err))
		}
	}
	return errors.Join(errs...)
}

// publishNetworkClaims claims every ended period of one network in order and stops
// at the first period it cannot publish, so no receipt is ever left behind a later claim.
func (s *Service) publishNetworkClaims(ctx context.Context, network string, now time.Time) error {
	st := s.Settlement()
	cursor, err := st.cursor(network)
	if err != nil {
		return err
	}
	var entries []funding.ReceiptEntry
	for after := cursor; ; {
		page := s.Ledger.Receipts(network, after, settlementReceiptPageSize)
		entries = append(entries, page...)
		if len(page) < settlementReceiptPageSize {
			break
		}
		after = page[len(page)-1].Sequence
	}
	last, hasLast, err := st.Last(network)
	if err != nil {
		return err
	}
	var previous *wire.SettlementClaim
	var floor time.Time
	if hasLast {
		previous = &last
		if floor, err = wire.ParseTime(last.PeriodEnd); err != nil {
			return err
		}
	} else if floor, err = s.firstDeclarationAt(); err != nil {
		return err
	}
	open := make(map[uint64]bool)
	var unclaimed []settlementReceipt
	for _, entry := range entries {
		r := entry.Receipt
		if r.ProviderID != s.ProviderID() || s.Ledger.Restored(r.AuthorID, r.MessageID) || s.Ledger.ProviderFailed(r.AuthorID, r.MessageID) {
			continue
		}
		if _, _, located, err := st.Locate(r.ReceiptID); err != nil {
			return err
		} else if located {
			continue
		}
		issued, err := wire.ParseTime(r.IssuedAt)
		if err != nil {
			return fmt.Errorf("receipt %s: %w", r.ReceiptID, err)
		}
		if previous != nil && issued.Before(floor) {
			log.Printf("provider settlement: receipt %s on %s issued at %s lies in the claimed period ending %s", r.ReceiptID, network, r.IssuedAt, last.PeriodEnd)
			return fmt.Errorf("receipt %s lies in an already claimed period", r.ReceiptID)
		}
		leaf, err := settlement.LeafOf(r)
		if err != nil {
			return err
		}
		open[entry.Sequence] = true
		unclaimed = append(unclaimed, settlementReceipt{entry: entry, leaf: leaf, issued: issued})
	}
	sort.SliceStable(unclaimed, func(i, j int) bool { return unclaimed[i].issued.Before(unclaimed[j].issued) })
	periods, planErr := settlementPlan(unclaimed, s.Config().ClaimPeriod, floor, now)
	if planErr != nil {
		log.Printf("provider settlement: %s: %v", network, planErr)
	}
	for _, p := range periods {
		if !now.Before(p.end.Add(settlementPublishLimit)) {
			log.Printf("provider settlement: claim for %s period ending %s is still unpublished", network, wire.FormatTime(p.end))
		}
	}
	var publishErr error
	for _, p := range periods {
		claim, err := s.publishSettlementPeriod(ctx, st, network, p, previous, now)
		if err != nil {
			publishErr = err
			break
		}
		previous = &claim
		for _, r := range p.receipts {
			delete(open, r.entry.Sequence)
		}
	}
	advanced := cursor
	for _, entry := range entries {
		if open[entry.Sequence] {
			break
		}
		advanced = entry.Sequence
	}
	if advanced > cursor {
		if err := st.advanceCursor(network, advanced); err != nil {
			return err
		}
	}
	if publishErr != nil {
		return publishErr
	}
	return planErr
}

// firstDeclarationAt returns when the provider's first retention declaration took
// effect. A first claim cannot start earlier: the declaration it names must be in
// effect at its period start, and a provider rarely starts on a period boundary.
func (s *Service) firstDeclarationAt() (time.Time, error) {
	declarations := s.Declarations()
	if len(declarations) == 0 {
		return time.Time{}, retention.ErrNoFittingDeclaration
	}
	return wire.ParseTime(declarations[len(declarations)-1].EffectiveAt)
}

// settlementPlan cuts receipts sorted by issued_at into claim periods: aligned to
// the claim period since the Unix epoch, starting no earlier than floor, ended at
// least settlementClockSkew before now, and split at issued_at boundaries so no
// claim exceeds settlementMaxLeaves. It returns the periods before any it cannot cut.
func settlementPlan(receipts []settlementReceipt, period time.Duration, floor, now time.Time) ([]settlementPeriod, error) {
	seconds := int64(period / time.Second)
	if seconds < 1 || period > 24*time.Hour {
		return nil, errors.New("invalid claim period")
	}
	var periods []settlementPeriod
	for i := 0; i < len(receipts); {
		unix := receipts[i].issued.Unix()
		start := time.Unix(unix-unix%seconds, 0).UTC()
		end := start.Add(period)
		if now.Before(end.Add(settlementClockSkew)) {
			break
		}
		if start.Before(floor) {
			start = floor
		}
		j := i
		for j < len(receipts) && receipts[j].issued.Before(end) {
			j++
		}
		group := receipts[i:j]
		for len(group) > settlementMaxLeaves {
			k := settlementMaxLeaves
			for k > 0 && group[k-1].issued.Equal(group[k].issued) {
				k--
			}
			if k == 0 {
				return periods, fmt.Errorf("more than %d receipts issued at %s; not publishing", settlementMaxLeaves, wire.FormatTime(group[0].issued))
			}
			cut := group[k].issued
			periods = append(periods, settlementPeriod{start: start, end: cut, receipts: group[:k]})
			start, group = cut, group[k:]
		}
		periods = append(periods, settlementPeriod{start: start, end: end, receipts: group})
		i = j
	}
	return periods, nil
}

func (s *Service) publishSettlementPeriod(ctx context.Context, st *SettlementStore, network string, p settlementPeriod, previous *wire.SettlementClaim, now time.Time) (wire.SettlementClaim, error) {
	leaves := make([]wire.SettlementLeaf, len(p.receipts))
	byHash := make(map[string]wire.SubmissionReceipt, len(p.receipts))
	total := new(big.Int)
	for i, r := range p.receipts {
		leaves[i] = r.leaf
		byHash[r.leaf.ReceiptHash] = r.entry.Receipt
		amount, err := wire.PositiveAmount(r.leaf.AmountBaseUnits)
		if err != nil {
			return wire.SettlementClaim{}, fmt.Errorf("receipt %s amount: %w", r.entry.Receipt.ReceiptID, err)
		}
		total.Add(total, amount)
	}
	leaves = settlement.SortLeaves(leaves)
	if err := settlement.CheckLeafSet(leaves); err != nil {
		return wire.SettlementClaim{}, settlementBug("leaf set", err)
	}
	root, err := settlement.TreeRoot(leaves)
	if err != nil {
		return wire.SettlementClaim{}, settlementBug("tree", err)
	}
	var link *retention.Link
	if previous != nil {
		end, err := wire.ParseTime(previous.PeriodEnd)
		if err != nil {
			return wire.SettlementClaim{}, err
		}
		link = &retention.Link{DeclarationID: previous.DeclarationID, ProviderID: previous.ProviderID, PeriodEnd: end}
	}
	declarations := s.Declarations()
	declaration, err := retention.ForClaim(declarations, s.ProviderID(), p.start, link)
	if err != nil {
		log.Printf("provider settlement: no retention declaration fits the %s period starting %s: %v", network, wire.FormatTime(p.start), err)
		return wire.SettlementClaim{}, err
	}
	claim, err := settlement.BuildClaim(s.Key(), settlement.ClaimInput{Network: network, Previous: previous, PeriodStart: p.start, PeriodEnd: p.end, DeclarationID: declaration.DeclarationID, ReceiptCount: len(leaves), Total: total, ReceiptsRoot: fmt.Sprintf("%x", root.Hash)})
	if err != nil {
		return wire.SettlementClaim{}, settlementBug("build", err)
	}
	schedules := settlementSchedules{s.FeeSchedules()}
	for _, leaf := range leaves {
		if err := settlement.CheckLeaf(claim, leaf, schedules); err != nil {
			return wire.SettlementClaim{}, settlementBug("leaf", err)
		}
	}
	if err := settlement.CheckTotals(claim, leaves); err != nil {
		return wire.SettlementClaim{}, settlementBug("totals", err)
	}
	if err := settlement.CheckLink(claim, previous, nil); err != nil {
		return wire.SettlementClaim{}, settlementBug("link", err)
	}
	if err := settlement.CheckDeclaration(claim, previous, func(id string) (wire.RetentionDeclaration, bool) {
		for _, d := range declarations {
			if d.DeclarationID == id {
				return d, true
			}
		}
		return wire.RetentionDeclaration{}, false
	}); err != nil {
		return wire.SettlementClaim{}, settlementBug("declaration", err)
	}
	if err := settlementFundClaim(ctx, s, claim, now); err != nil {
		return wire.SettlementClaim{}, err
	}
	receipts := make([]wire.SubmissionReceipt, len(leaves))
	for i, leaf := range leaves {
		receipts[i] = byHash[leaf.ReceiptHash]
	}
	if err := st.Append(claim, leaves, receipts); err != nil {
		return wire.SettlementClaim{}, err
	}
	s.retainSettledRecords()
	return claim, nil
}

func settlementBug(check string, err error) error {
	log.Printf("provider settlement: bug: claim fails its own %s check: %v", check, err)
	return fmt.Errorf("claim fails its own %s check: %w", check, err)
}

// retainSettledRecords sets the retention end of every held record of a published
// claim. A failure is logged and retried on the next run; it never blocks claims.
func (s *Service) retainSettledRecords() {
	st := s.Settlement()
	pending, err := st.pendingRetention()
	if err != nil {
		log.Printf("provider settlement: read pending retention: %v", err)
		return
	}
	for _, p := range pending {
		if err := s.retainClaimRecords(p); err != nil {
			log.Printf("provider settlement: retention for claim %s: %v", p.ClaimID, err)
			continue
		}
		if err := st.completeRetention(p.ClaimID); err != nil {
			log.Printf("provider settlement: complete retention for claim %s: %v", p.ClaimID, err)
		}
	}
}

func (s *Service) retainClaimRecords(p settlementRetention) error {
	declarations := s.Declarations()
	for _, record := range p.Records {
		_, state, err := s.Records().Lookup(record.MessageID)
		if err != nil {
			return err
		}
		if state != RecordRetained {
			continue
		}
		issued, err := wire.ParseTime(record.IssuedAt)
		if err != nil {
			return err
		}
		until, err := retention.RetainUntil(issued, p.ProviderID, p.DeclarationID, declarations)
		if err != nil {
			return err
		}
		if err := s.Records().SetRetainUntil(record.MessageID, until); err != nil && !errors.Is(err, ErrRetentionShortened) {
			return err
		}
	}
	return nil
}
