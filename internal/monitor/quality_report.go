package monitor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"foryou.local/foryou/internal/quality"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

const (
	qualityMaxMethodology = 1 << 20
	qualityConflicts      = "In the devnet the monitor, the providers and the Foundation are run by the same operator on one machine."
)

var (
	qualityReports  = store.Collection[wire.QualityReport]{Name: "monitor.quality.report"}
	qualityEvidence = store.Collection[qualityEvidenceRecord]{Name: "monitor.quality.evidence"}
)

// qualityEvidenceRecord keeps the exact methodology bytes and observation set a report's digests bind.
type qualityEvidenceRecord struct {
	Methodology  string                     `json:"methodology"`
	Observations wire.QualityObservationSet `json:"observations"`
}

func init() {
	loopSteps = append(loopSteps, loopStep{order: qualityOrder, name: "quality", run: qualityRun})
}

// qualityRun finishes ended periods, plans the current period once per member and provider, and sends due probes.
// Failures are logged per member and provider so that later loop steps still run.
func qualityRun(s *Service, ctx context.Context) error {
	now := s.Now()
	for _, member := range s.Members() {
		if err := qualityFinishEnded(s, member, now); err != nil {
			log.Printf("monitor: quality member %d: finish reports: %v", member.Index(), err)
		}
	}
	periodStart, periodEnd, err := qualityPeriodAt(s.Calendar(), now)
	if err != nil {
		log.Printf("monitor: quality: %v", err)
		return nil
	}
	sources := qualityFrameSources{}
	for position, member := range s.Members() {
		if !s.View().IsMember(member.DID(), periodStart) {
			continue
		}
		roster, err := qualityRosterFor(s, member, periodStart)
		if err != nil {
			log.Printf("monitor: quality member %d: roster: %v", member.Index(), err)
			continue
		}
		for _, provider := range roster {
			if err := qualityRunProvider(s, ctx, &sources, member, position, provider, periodStart, periodEnd, now); err != nil {
				log.Printf("monitor: quality member %d provider %s period %s: %v", member.Index(), provider.ID, wire.FormatTime(periodStart), err)
			}
		}
	}
	return nil
}

func qualityRunProvider(s *Service, ctx context.Context, sources *qualityFrameSources, member *Member, position int, provider Provider, periodStart, periodEnd, now time.Time) error {
	key := qualityPlanKey(provider.ID, periodStart)
	var plan qualityPlan
	var planned, refused bool
	if err := member.DB().View(func(tx store.Tx) error {
		var err error
		if plan, planned, err = qualityPlans.Get(tx, key); err != nil {
			return err
		}
		_, refused, err = qualityRefusals.Get(tx, key)
		return err
	}); err != nil || refused {
		return err
	}
	if !planned {
		source, name, err := sources.ready(s, ctx, provider, now)
		if err != nil {
			return err
		}
		frame, excluded, err := qualityBuildFrame(s, member, provider, source, now)
		if err != nil {
			return err
		}
		windowStart := periodStart
		if now.After(windowStart) {
			windowStart = now
		}
		plan, err = qualityNewPlan(provider, periodStart, periodEnd, windowStart, name, frame, excluded, position, len(s.Members()))
		if err != nil {
			if putErr := member.DB().Update(func(tx store.Tx) error { return qualityRefusals.Put(tx, key, qualityRefusal{Reason: err.Error()}) }); putErr != nil {
				return errors.Join(err, putErr)
			}
			return fmt.Errorf("refused the period: %w", err)
		}
		if err := member.DB().Update(func(tx store.Tx) error { return qualityPlans.Put(tx, key, plan) }); err != nil {
			return err
		}
	}
	return qualityExecuteDue(s, ctx, member, key, &plan, now)
}

// qualityFrameSources syncs each frame source at most once per pass: the vault, or a provider's own changes when the vault cannot be reached.
type qualityFrameSources struct {
	vaultDone bool
	vault     Provider
	vaultErr  error
	fallback  bool
	providers map[string]error
}

func (f *qualityFrameSources) ready(s *Service, ctx context.Context, provider Provider, now time.Time) (Provider, string, error) {
	if !f.vaultDone {
		f.vaultDone = true
		foundationID, ok := s.View().Trust().CurrentFoundationID(now)
		if s.VaultURL() == "" || !ok {
			f.fallback = true
		} else {
			f.vault = Provider{ID: foundationID, URL: s.VaultURL()}
			synced, err := qualitySyncMirror(s, ctx, f.vault, now)
			switch {
			case err != nil && qualityVaultUnreachable(err):
				log.Printf("monitor: quality: vault unreachable, framing from provider changes: %v", err)
				f.fallback = true
			case err != nil:
				f.vaultErr = fmt.Errorf("vault answered GET /v1/changes with an error, no frame: %w", err)
			case !synced:
				f.vaultErr = errors.New("vault mirror is still catching up")
			}
		}
	}
	if !f.fallback {
		return f.vault, qualityVaultSource, f.vaultErr
	}
	if f.providers == nil {
		f.providers = make(map[string]error)
	}
	err, done := f.providers[provider.ID]
	if !done {
		synced, syncErr := qualitySyncMirror(s, ctx, provider, now)
		err = syncErr
		if err == nil && !synced {
			err = errors.New("provider mirror is still catching up")
		}
		f.providers[provider.ID] = err
	}
	return provider, qualityProviderSource, err
}

// qualityFinishEnded signs one report for every plan whose period has ended, stores it with its evidence and drops the plan.
func qualityFinishEnded(s *Service, member *Member, now time.Time) error {
	type ended struct {
		key  string
		plan qualityPlan
	}
	var plans []ended
	if err := member.DB().View(func(tx store.Tx) error {
		return qualityPlans.Scan(tx, store.Range{}, func(key string, plan qualityPlan) (bool, error) {
			end, err := wire.ParseTime(plan.PeriodEnd)
			if err == nil && !now.Before(end) && plan.Unpublishable == "" {
				plans = append(plans, ended{key: key, plan: plan})
			}
			return true, nil
		})
	}); err != nil {
		return err
	}
	var errs []error
	for _, item := range plans {
		report, evidence, err := qualitySignReport(member, item.plan, now)
		if err != nil {
			item.plan.Unpublishable = err.Error()
			errs = append(errs, fmt.Errorf("report for %s withheld: %w", item.key, err))
			if putErr := member.DB().Update(func(tx store.Tx) error { return qualityPlans.Put(tx, item.key, item.plan) }); putErr != nil {
				errs = append(errs, putErr)
			}
			continue
		}
		if err := member.DB().Update(func(tx store.Tx) error {
			if err := qualityEvidence.Put(tx, report.ReportID, evidence); err != nil {
				return err
			}
			if err := qualityReports.Put(tx, item.key, report); err != nil {
				return err
			}
			return qualityPlans.Delete(tx, item.key)
		}); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// qualitySignReport aggregates a plan's observations into a signed report and checks it with quality.Verify before it can be stored.
func qualitySignReport(member *Member, plan qualityPlan, now time.Time) (wire.QualityReport, qualityEvidenceRecord, error) {
	observations := make([]wire.QualityObservation, 0, len(plan.Probes))
	retrievals := 0
	complete := true
	for _, probe := range plan.Probes {
		if probe.Kind == string(quality.KindRetrieval) {
			retrievals++
		}
		if probe.Observation == nil {
			complete = false
			continue
		}
		observations = append(observations, *probe.Observation)
	}
	observations, restrictedShare := quality.ApplyRestrictedCap(observations, retrievals)
	set := wire.QualityObservationSet{ProviderID: plan.ProviderID, MonitorID: member.DID(), PeriodStart: plan.PeriodStart, PeriodEnd: plan.PeriodEnd, Observations: observations}
	aggregates, err := quality.Aggregate(set)
	if err != nil {
		return wire.QualityReport{}, qualityEvidenceRecord{}, err
	}
	observationsDigest, err := quality.ObservationsDigest(set)
	if err != nil {
		return wire.QualityReport{}, qualityEvidenceRecord{}, err
	}
	methodology := qualityMethodology(plan, retrievals, restrictedShare)
	if len(methodology) > qualityMaxMethodology {
		return wire.QualityReport{}, qualityEvidenceRecord{}, errors.New("methodology exceeds 1 MiB")
	}
	methodologyDigest := sha256.Sum256([]byte(methodology))
	coverage := "incomplete"
	if complete {
		coverage = "complete"
	}
	report := wire.QualityReport{
		MonitorID: member.DID(), ProviderID: plan.ProviderID, PeriodStart: plan.PeriodStart, PeriodEnd: plan.PeriodEnd, CreatedAt: wire.FormatTime(now),
		MethodologyDigest: hex.EncodeToString(methodologyDigest[:]), ObservationsDigest: observationsDigest,
		SampleSeed: plan.Seed, SampleSeedCommitment: plan.SeedCommitment, SampleFrameDigest: plan.FrameDigest, SampleCoverage: coverage,
		AvailabilityAttempts: aggregates.AvailabilityAttempts, AvailabilitySuccesses: aggregates.AvailabilitySuccesses,
		RetrievalAttempts: aggregates.RetrievalAttempts, RetrievalPasses: aggregates.RetrievalPasses,
		LatencyP50Ms: aggregates.LatencyP50, LatencyP95Ms: aggregates.LatencyP95, LatencyP99Ms: aggregates.LatencyP99,
		Sponsors: []string{}, Conflicts: qualityConflicts,
	}
	report, err = quality.Sign(report, member.key)
	if err != nil {
		return wire.QualityReport{}, qualityEvidenceRecord{}, err
	}
	if err := quality.Verify(report, set, []byte(methodology)); err != nil {
		return wire.QualityReport{}, qualityEvidenceRecord{}, fmt.Errorf("signed report fails verification: %w", err)
	}
	return report, qualityEvidenceRecord{Methodology: methodology, Observations: set}, nil
}

// qualityMethodology is the deterministic, non-executable description the report's methodology_digest binds.
func qualityMethodology(plan qualityPlan, retrievals, restrictedShareBP int) string {
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }
	line("ForYou reference monitor quality methodology, version 1")
	line("Endpoint: %s", plan.ProviderURL)
	line("Pinned provider key: %s", plan.ProviderID)
	line("Period: %s to %s; probe window from %s (the plan time when the plan was made after the period start).", plan.PeriodStart, plan.PeriodEnd, plan.WindowStart)
	line("Routes: availability GET /v1/messages?limit=1, which passes on HTTP 200 with a valid DiscoveryPage; retrieval GET /v1/messages/{message_id}, which passes on HTTP 200 with a complete body of at most 1 MiB whose message_id equals the requested ID, whose intent ID recomputes, whose author or delegate and receipt signatures verify, and whose receipt is the settled receipt that put the message in the frame. No media, previews or private data are fetched.")
	line("Probe rate: %d availability probes and %d retrieval probes in the period. The provider discloses no machine-readable limits, so the portability per-source floors apply: %d listing requests and %d direct GETs per minute, shared by the %d committee members hosted in this process, which reach the provider from one source address. Slots of ceil(60 s / limit) start at the period start; member position j uses the slots whose index is congruent to j modulo %d. Probe times are the slots at or after the window start with the lowest SHA-256(seed bytes || 0x00 || route class || 0x00 || decimal slot index). A probe runs at the first scheduler pass at or after its planned UTC second, only while fewer than limit requests of its class went to the provider in the preceding 60 seconds; started_at records the planned second. A probe not sent before the period end is missing and the coverage is incomplete.", len(plan.Probes)-retrievals, retrievals, qualityListingPerSourcePerMinute, qualityDirectPerSourcePerMinute, plan.Members, plan.Members)
	line("Probe-time commitment: SHA-256 of the JCS array of planned {kind, message_id, started_at} in planned order = %s, stored before the first probe.", plan.ProbeTimesDigest)
	line("Observation location: one host running all roles; clock: the monitor's UTC clock in whole seconds, not independently timestamped; durations: a local monotonic timer.")
	line("Frame: %s, mirrored incrementally. A message enters the frame when its receipt names a key of the provider's succession line, its receipt is in a claim this monitor witnessed with its leaves, and retain_until (receipt issued_at plus the retention_seconds of the declaration that claim names) has not passed. IDs are sorted by ASCII byte order and the first 10000 kept. Frame size %d; frame digest %s.", plan.FrameSource, plan.FrameSize, plan.FrameDigest)
	line("Exclusions: %d records whose receipt is in no claim this monitor holds with leaves (claims adopted without leaves cannot be checked); %d records whose claim's retention declaration is unknown; %d records whose claim is part of an equivocation (its settlement and governing declaration are uncertain).", plan.Excluded.NoClaim, plan.Excluded.NoDeclaration, plan.Excluded.Equivocation)
	line("Sample: planned size n = %d, without replacement, by quality-sample-rank over the frame; seed commitment %s, drawn from crypto/rand and committed before observing. No independent randomness beacon is used, so seed selection may be biased.", retrievals, plan.SeedCommitment)
	line("Timeout: one deadline of %s per probe, no retries.", qualityProbeTimeout)
	line("Classification: 429 rate_limited; 503 or timeout unavailable with null status; 403 with a legal restriction restricted, capped at 2%% of the sample with the excess counted as fail (restricted share %d basis points); 404 or 410 for a frame record fail; any other non-200 or unvalidated response fail; only validated 200 responses pass.", restrictedShareBP)
	line("Restore scope: no restore drill is run; restore attempts and passes are 0.")
	line("Ownership assessment: replica operators and independent controllers are unknown and reported as null.")
	line("Funding and conflicts: no sponsors. %s", qualityConflicts)
	return b.String()
}
