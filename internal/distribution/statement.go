package distribution

import (
	"crypto/ed25519"
	"fmt"
	"math/big"
	"sort"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

const (
	bitcoinMinimum  = int64(10_000_000)
	ethereumMinimum = int64(1_000_000_000_000_000)
	secondsPerYear  = int64(31_536_000)
)

// ReportSpan contains a report's period and its caller-computed reference score.
type ReportSpan struct {
	ReportID   string
	MonitorID  string
	Start, End time.Time
	CreatedAt  time.Time
	Score      *int // Nil when the reference score is unknown.
}

// ProviderInput contains the counted claims and public evidence for one first-key chain.
type ProviderInput struct {
	ChainID         string // The first key of the provider chain.
	SettledFees     *big.Int
	DeclaredSeconds []int64   // Retention seconds of declarations named by counted claims.
	ActiveFrom      time.Time // Zero when provider activity began before the quarter.
	Reports         []ReportSpan
	PayoutRecipient string // Empty when no declaration names a recipient for this network.
}

// MemberSpan describes one continuous stretch of committee membership in the quarter.
type MemberSpan struct {
	MonitorID  string
	Start, End time.Time
}

// SeatEvidence gives the public claim witness counts for one monitor.
type SeatEvidence struct{ Witnessed, Counted int }

// Input contains already-collected data for one network and calendar quarter.
type Input struct {
	Policy     wire.DistributionPolicy
	Network    string
	Start, End time.Time
	Day        time.Duration
	CutOff     time.Time
	Members    []MemberSpan // One span per stretch of committee membership inside the quarter.
	Providers  []ProviderInput
	Earlier    []wire.DistributionStatement // This network's earlier statements, most recent first.
	Seats      map[string]SeatEvidence      // One entry per policy monitor.
}

type plannedEntry struct {
	entry wire.DistributionEntry
	paid  *big.Int
}

type plannedMonitor struct {
	amount *big.Int
	paid   bool
}

// Plan holds a calculation result from Compute.
type Plan struct {
	input       Input
	entries     []plannedEntry
	monitors    map[string]plannedMonitor
	received    *big.Int
	distributed *big.Int
}

// ProviderPayments returns base units to transfer, keyed by provider chain ID.
func (p Plan) ProviderPayments() map[string]*big.Int {
	result := make(map[string]*big.Int)
	for _, entry := range p.entries {
		if entry.paid.Sign() > 0 {
			result[entry.entry.ProviderID] = new(big.Int).Set(entry.paid)
		}
	}
	return result
}

// MonitorPayments returns base units to transfer, keyed by monitor ID.
func (p Plan) MonitorPayments() map[string]*big.Int {
	result := make(map[string]*big.Int)
	for id, monitor := range p.monitors {
		if monitor.paid {
			result[id] = new(big.Int).Set(monitor.amount)
		}
	}
	return result
}

// Compute calculates provider allocations, accruals, and monitor seats from public inputs.
func Compute(in Input) (Plan, error) {
	if in.Network != wire.BitcoinMainnet && in.Network != wire.EthereumMainnet {
		return Plan{}, fmt.Errorf("unknown network %q", in.Network)
	}
	if !in.End.After(in.Start) || in.Day <= 0 {
		return Plan{}, fmt.Errorf("invalid quarter or day length")
	}
	p := in.Policy
	if p.VaultShareBP < 2500 || p.VaultShareBP > 10000 || p.MonitorShareBP < 0 || p.MonitorShareBP > 1000 || p.MonitorShareBP > p.VaultShareBP || p.MaxRetentionYears <= 0 || p.MaxRetentionYears > 1000 || p.MinReports < 3 || p.MinReports > 1000 || len(p.Monitors) < 5 || len(p.Monitors) > 256 {
		return Plan{}, fmt.Errorf("invalid distribution policy bounds")
	}
	monitorSet := make(map[string]bool, len(p.Monitors))
	for _, id := range p.Monitors {
		if id == "" || monitorSet[id] {
			return Plan{}, fmt.Errorf("invalid or duplicate policy monitor")
		}
		monitorSet[id] = true
	}
	carried := make(map[string]wire.DistributionEntry)
	for _, old := range in.Earlier {
		for _, e := range old.Entries {
			if _, ok := carried[e.ProviderID]; !ok {
				carried[e.ProviderID] = e
			}
		}
	}
	providers := append([]ProviderInput(nil), in.Providers...)
	sort.Slice(providers, func(i, j int) bool { return providers[i].ChainID < providers[j].ChainID })
	plan := Plan{input: in, received: new(big.Int), distributed: new(big.Int), monitors: make(map[string]plannedMonitor)}
	seen := make(map[string]bool)
	for _, provider := range providers {
		if provider.ChainID == "" || seen[provider.ChainID] {
			return Plan{}, fmt.Errorf("invalid or duplicate provider chain")
		}
		seen[provider.ChainID] = true
		fees := new(big.Int)
		if provider.SettledFees != nil {
			fees.Set(provider.SettledFees)
		}
		if fees.Sign() < 0 {
			return Plan{}, fmt.Errorf("negative settled fees")
		}
		plan.received.Add(plan.received, fees)
		prior, hasPrior := carried[provider.ChainID]
		for _, seconds := range provider.DeclaredSeconds {
			if seconds < 0 {
				return Plan{}, fmt.Errorf("negative retention declaration for %s", provider.ChainID)
			}
		}
		accrued := new(big.Int)
		if hasPrior {
			var ok bool
			accrued, ok = new(big.Int).SetString(prior.AccruedBaseUnits, 10)
			if !ok {
				return Plan{}, fmt.Errorf("invalid carried accrual for %s", provider.ChainID)
			}
		}
		if accrued.Sign() < 0 {
			return Plan{}, fmt.Errorf("negative carried accrual")
		}
		proven := prior.ProvenQuarters
		if proven < 0 {
			return Plan{}, fmt.Errorf("negative carried proven quarters")
		}
		ids, quality, err := providerQuality(in, provider)
		if err != nil {
			return Plan{}, err
		}
		minimum := new(big.Int).SetInt64(minimumFor(in.Network))
		if fees.Cmp(minimum) >= 0 && quality >= 5000 {
			proven++
		}
		years := retentionYears(provider.DeclaredSeconds, p.MaxRetentionYears, proven)
		amount := providerAmount(fees, p.VaultShareBP, quality, years, p.MaxRetentionYears)
		owed := new(big.Int).Add(accrued, amount)
		paid := new(big.Int)
		if provider.PayoutRecipient != "" && owed.Cmp(minimum) >= 0 {
			paid.Set(owed)
			if in.Network == wire.BitcoinMainnet {
				paid.Quo(paid, big.NewInt(1000))
				paid.Mul(paid, big.NewInt(1000))
			}
		}
		remaining := new(big.Int).Sub(owed, paid)
		if fees.Sign() == 0 && accrued.Sign() == 0 {
			continue
		}
		entry := wire.DistributionEntry{ProviderID: provider.ChainID, SettledFeesBaseUnits: fees.String(), QualityBP: int64(quality), ReportIDs: ids, RetentionYears: int64(years), ProvenQuarters: proven, AmountBaseUnits: amount.String(), AccruedBaseUnits: remaining.String()}
		if len(ids) > 4096 {
			return Plan{}, fmt.Errorf("too many report IDs for %s", provider.ChainID)
		}
		if paid.Sign() > 0 {
			plan.distributed.Add(plan.distributed, paid)
		}
		plan.entries = append(plan.entries, plannedEntry{entry: entry, paid: paid})
	}
	for id, old := range carried {
		if seen[id] {
			continue
		}
		accrued, ok := new(big.Int).SetString(old.AccruedBaseUnits, 10)
		if !ok {
			return Plan{}, fmt.Errorf("invalid carried accrual for %s", id)
		}
		if accrued.Sign() < 0 {
			return Plan{}, fmt.Errorf("negative carried accrual")
		}
		if old.ProvenQuarters < 0 {
			return Plan{}, fmt.Errorf("negative carried proven quarters")
		}
		if accrued.Sign() == 0 {
			continue
		}
		plan.entries = append(plan.entries, plannedEntry{entry: wire.DistributionEntry{ProviderID: id, SettledFeesBaseUnits: "0", ReportIDs: []string{}, RetentionYears: 0, ProvenQuarters: old.ProvenQuarters, AmountBaseUnits: "0", AccruedBaseUnits: accrued.String()}, paid: new(big.Int)})
	}
	sort.Slice(plan.entries, func(i, j int) bool { return plan.entries[i].entry.ProviderID < plan.entries[j].entry.ProviderID })
	seat := new(big.Int).Mul(plan.received, big.NewInt(p.MonitorShareBP))
	seat.Quo(seat, big.NewInt(10000))
	seat.Quo(seat, big.NewInt(int64(len(p.Monitors))))
	for _, id := range p.Monitors {
		if _, ok := in.Seats[id]; !ok {
			return Plan{}, fmt.Errorf("missing seat evidence for %s", id)
		}
		evidence := in.Seats[id]
		if evidence.Counted < 0 || evidence.Witnessed < 0 || evidence.Witnessed > evidence.Counted {
			return Plan{}, fmt.Errorf("invalid seat evidence for %s", id)
		}
		witnessed := new(big.Int).Mul(big.NewInt(int64(evidence.Witnessed)), big.NewInt(100))
		counted := new(big.Int).Mul(big.NewInt(int64(evidence.Counted)), big.NewInt(95))
		earned := witnessed.Cmp(counted) >= 0
		amount := new(big.Int)
		if earned {
			amount.Set(seat)
		}
		plan.monitors[id] = plannedMonitor{amount: amount, paid: earned && seat.Sign() > 0}
	}
	return plan, nil
}

// Statement assembles the unsigned statement, requiring transfers for provider payouts and rejecting transfers for unpaid seats.
func (p Plan) Statement(foundationID string, providers, monitors map[string]wire.DepositTransfer) (wire.DistributionStatement, error) {
	if foundationID == "" {
		return wire.DistributionStatement{}, fmt.Errorf("foundation ID is required")
	}
	result := wire.DistributionStatement{FoundationID: foundationID, Network: p.input.Network, PeriodStart: p.input.Start.UTC().Format(time.RFC3339), PeriodEnd: p.input.End.UTC().Format(time.RFC3339), PolicyID: p.input.Policy.PolicyID, ReceivedBaseUnits: p.received.String(), DistributedBaseUnits: p.distributed.String(), MonitorPayments: []wire.MonitorPayment{}, Entries: []wire.DistributionEntry{}}
	for _, item := range p.entries {
		transfer, exists := providers[item.entry.ProviderID]
		if item.paid.Sign() > 0 {
			if !exists {
				return wire.DistributionStatement{}, fmt.Errorf("missing provider transfer for %s", item.entry.ProviderID)
			}
			if transfer.Network != p.input.Network {
				return wire.DistributionStatement{}, fmt.Errorf("provider transfer network mismatch")
			}
			item.entry.Payout = &transfer
		} else if exists {
			return wire.DistributionStatement{}, fmt.Errorf("transfer supplied for unpaid provider %s", item.entry.ProviderID)
		}
		result.Entries = append(result.Entries, item.entry)
	}
	for id := range providers {
		found := false
		for _, e := range p.entries {
			if e.entry.ProviderID == id && e.paid.Sign() > 0 {
				found = true
			}
		}
		if !found {
			return wire.DistributionStatement{}, fmt.Errorf("unexpected provider transfer for %s", id)
		}
	}
	monitorIDs := append([]string(nil), p.input.Policy.Monitors...)
	sort.Strings(monitorIDs)
	for _, id := range monitorIDs {
		allocation := p.monitors[id]
		transfer, exists := monitors[id]
		if allocation.paid && exists {
			if transfer.Network != p.input.Network {
				return wire.DistributionStatement{}, fmt.Errorf("monitor transfer network mismatch")
			}
			result.MonitorPayments = append(result.MonitorPayments, wire.MonitorPayment{MonitorID: id, AmountBaseUnits: allocation.amount.String(), Payout: &transfer})
		} else {
			if exists {
				return wire.DistributionStatement{}, fmt.Errorf("transfer supplied for unpaid monitor %s", id)
			}
			result.MonitorPayments = append(result.MonitorPayments, wire.MonitorPayment{MonitorID: id, AmountBaseUnits: allocation.amount.String()})
		}
	}
	for id := range monitors {
		if !p.monitors[id].paid {
			return wire.DistributionStatement{}, fmt.Errorf("unexpected monitor transfer for %s", id)
		}
	}
	var err error
	result.StatementID, err = StatementID(result)
	if err != nil {
		return wire.DistributionStatement{}, err
	}
	return result, nil
}

// StatementID computes the typed ID excluding statement_id and foundation_signature.
func StatementID(s wire.DistributionStatement) (string, error) {
	canonical, err := canon.EncodeWithout(s, "statement_id", "foundation_signature")
	if err != nil {
		return "", err
	}
	return keys.DigestID("fyds2:", "distribution-statement-id", canonical)
}

// CheckStatementID verifies the statement's typed content ID.
func CheckStatementID(s wire.DistributionStatement) error {
	got, err := StatementID(s)
	if err != nil {
		return err
	}
	if got != s.StatementID {
		return fmt.Errorf("statement ID mismatch")
	}
	return nil
}

// SignStatement assigns the content ID and signs the statement under the Foundation domain label.
func SignStatement(s wire.DistributionStatement, key ed25519.PrivateKey) (wire.DistributionStatement, error) {
	id, err := StatementID(s)
	if err != nil {
		return wire.DistributionStatement{}, err
	}
	s.StatementID = id
	canonical, err := canon.EncodeWithout(s, "foundation_signature")
	if err != nil {
		return wire.DistributionStatement{}, err
	}
	s.FoundationSignature, err = keys.Sign(key, "distribution-statement", canonical)
	if err != nil {
		return wire.DistributionStatement{}, err
	}
	return s, nil
}

func minimumFor(network string) int64 {
	if network == wire.BitcoinMainnet {
		return bitcoinMinimum
	}
	return ethereumMinimum
}

func providerAmount(fees *big.Int, vaultBP int64, quality, years int, maxYears int64) *big.Int {
	retentionBP := int64(5000) + 5000*int64(years)/maxYears
	n := new(big.Int).Mul(fees, big.NewInt(10000-vaultBP))
	n.Mul(n, big.NewInt(int64(quality)))
	n.Mul(n, big.NewInt(retentionBP))
	return n.Quo(n, big.NewInt(1_000_000_000_000))
}

func retentionYears(declarations []int64, max int64, proven int64) int {
	declared := int64(0)
	if len(declarations) > 0 {
		declared = int64(^uint64(0) >> 1)
		for _, seconds := range declarations {
			years := seconds / secondsPerYear
			if years < declared {
				declared = years
			}
		}
	}
	years := int64(1) + proven/4
	if years > max {
		years = max
	}
	if len(declarations) == 0 || declared < years {
		years = declared
	}
	if years < 0 {
		years = 0
	}
	return int(years)
}

// providerQuality uses ceil(2 * overlapDays / 3) required days when provider activity or monitor membership began or ended inside the quarter, and 60 days otherwise.
func providerQuality(in Input, provider ProviderInput) ([]string, int, error) {
	members := make(map[string][]MemberSpan)
	for _, m := range in.Members {
		if m.End.After(m.Start) && m.Start.Before(in.End) && m.End.After(in.Start) {
			members[m.MonitorID] = append(members[m.MonitorID], m)
		}
	}
	monitorReports := make(map[string][]ReportSpan)
	ids := []string{}
	for _, r := range provider.Reports {
		if !r.End.After(r.Start) {
			return nil, 0, fmt.Errorf("invalid report span %s", r.ReportID)
		}
		if r.Start.Before(in.Start) || r.End.After(in.End) || r.CreatedAt.After(in.CutOff) || len(members[r.MonitorID]) == 0 {
			continue
		}
		if r.Score != nil && (*r.Score < 0 || *r.Score > 10000) {
			return nil, 0, fmt.Errorf("invalid score for report %s", r.ReportID)
		}
		ids = append(ids, r.ReportID)
		monitorReports[r.MonitorID] = append(monitorReports[r.MonitorID], r)
	}
	sort.Strings(ids)
	ids = uniqueStrings(ids)
	scores := []int{}
	for monitor := range monitorReports {
		reports := monitorReports[monitor]
		required := time.Duration(60) * in.Day
		overlapDays := make(map[int64]struct{})
		for _, m := range members[monitor] {
			start := maxTime(m.Start, in.Start)
			end := minTime(m.End, in.End)
			if provider.ActiveFrom.After(start) {
				start = provider.ActiveFrom
			}
			if end.After(start) {
				firstDay := int64(start.Sub(in.Start) / in.Day)
				lastDay := int64((end.Add(-time.Nanosecond).Sub(in.Start)) / in.Day)
				for day := firstDay; day <= lastDay; day++ {
					overlapDays[day] = struct{}{}
				}
			}
		}
		partial := provider.ActiveFrom.After(in.Start) && provider.ActiveFrom.Before(in.End)
		for _, m := range members[monitor] {
			if m.Start.After(in.Start) && m.Start.Before(in.End) || m.End.After(in.Start) && m.End.Before(in.End) {
				partial = true
			}
		}
		if partial {
			days := int64(len(overlapDays))
			required = time.Duration((2*days+2)/3) * in.Day
		}
		if score, ok := MonitorScore(reports, required); ok {
			scores = append(scores, score)
		}
	}
	quality := 0
	if int64(len(scores)) >= in.Policy.MinReports {
		quality = LowerMedian(scores)
	}
	return ids, quality, nil
}

func uniqueStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	out := values[:1]
	for _, v := range values[1:] {
		if v != out[len(out)-1] {
			out = append(out, v)
		}
	}
	return out
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}

func maxTime(a, b time.Time) time.Time {
	if a.After(b) {
		return a
	}
	return b
}
