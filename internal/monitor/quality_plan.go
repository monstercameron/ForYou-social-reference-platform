package monitor

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strconv"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/committee"
	"foryou.local/foryou/internal/distribution"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/quality"
	"foryou.local/foryou/internal/retention"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

const (
	qualityMaxFrame       = 10000
	qualityMaxPeriod      = 31 * 24 * time.Hour
	qualityChangesLimit   = 100
	qualityVaultSource    = "Lifetime Vault GET /v1/changes"
	qualityProviderSource = "provider GET /v1/changes (the vault could not be reached)"
)

var (
	// qualityProbes is the planned availability probe count and the retrieval sample cap per period.
	qualityProbes = 24
	// qualityReportDays is the period length in calendar days; zero means 1 for a real calendar and 10 for a dev calendar.
	qualityReportDays = 0
	// The portability per-source floors stand in for the provider's disclosed limits, which no route discloses.
	qualityListingPerSourcePerMinute = 6
	qualityDirectPerSourcePerMinute  = 30
	// qualityProbeTimeout is the one deadline of every probe and mirror read; there are no retries.
	qualityProbeTimeout = 10 * time.Second
	// qualityMirrorPagesPerPass bounds the /v1/changes pages read from one frame source in one pass.
	qualityMirrorPagesPerPass           = 20
	qualitySeedSource         io.Reader = rand.Reader

	qualityPlans         = store.Collection[qualityPlan]{Name: "monitor.quality.plan"}
	qualityRefusals      = store.Collection[qualityRefusal]{Name: "monitor.quality.refusal"}
	qualityRosters       = store.Collection[qualityRoster]{Name: "monitor.quality.roster"}
	qualityMirror        = store.Collection[qualityMirrorRecord]{Name: "monitor.quality.mirror"}
	qualityMirrorCursors = store.Collection[qualityMirrorCursor]{Name: "monitor.quality.mirror.cursor"}
	qualitySends         = store.Collection[qualitySendLog]{Name: "monitor.quality.sent"}

	errQualityDeferred = errors.New("monitor: quality read deferred by the per-source limit")
)

// qualityPlan is committed to the member's journal before its first probe.
type qualityPlan struct {
	ProviderID       string                `json:"provider_id"`
	ProviderURL      string                `json:"provider_url"`
	PeriodStart      string                `json:"period_start"`
	PeriodEnd        string                `json:"period_end"`
	WindowStart      string                `json:"window_start"`
	Members          int                   `json:"members"`
	FrameSource      string                `json:"frame_source"`
	FrameSize        int                   `json:"frame_size"`
	FrameDigest      string                `json:"frame_digest"`
	Excluded         qualityExclusions     `json:"excluded"`
	Seed             string                `json:"seed"`
	SeedCommitment   string                `json:"seed_commitment"`
	Probes           []qualityPlannedProbe `json:"probes"`
	ProbeTimesDigest string                `json:"probe_times_digest"`
	Unpublishable    string                `json:"unpublishable,omitempty"`
}

// qualityPlannedProbe binds a retrieval to the settled receipt that put its message in the frame.
type qualityPlannedProbe struct {
	Kind        string                   `json:"kind"`
	MessageID   *string                  `json:"message_id"`
	StartedAt   string                   `json:"started_at"`
	ReceiptHash string                   `json:"receipt_hash,omitempty"`
	Observation *wire.QualityObservation `json:"observation,omitempty"`
}

type qualityExclusions struct {
	NoClaim       int `json:"no_claim"`
	NoDeclaration int `json:"no_declaration"`
	Equivocation  int `json:"equivocation"`
}

type qualityRefusal struct {
	Reason string `json:"reason"`
}

type qualityRoster struct {
	Providers []Provider `json:"providers"`
}

type qualityMirrorCursor struct {
	After  uint64 `json:"after"`
	Synced bool   `json:"synced"`
}

type qualityMirrorRecord struct {
	MessageID   string `json:"message_id"`
	ProviderID  string `json:"provider_id"`
	Network     string `json:"network"`
	ReceiptHash string `json:"receipt_hash"`
	IssuedAt    string `json:"issued_at"`
}

type qualitySendLog struct {
	Sent []string `json:"sent"`
}

func qualityPeriodLength(day time.Duration) (time.Duration, error) {
	days := qualityReportDays
	if days == 0 {
		days = 10
		if day == 24*time.Hour {
			days = 1
		}
	}
	if day <= 0 || days < 1 || time.Duration(days)*day > qualityMaxPeriod {
		return 0, fmt.Errorf("monitor: quality period of %d days of %s is not between one day and 31 days", days, day)
	}
	return time.Duration(days) * day, nil
}

// qualityPeriodAt returns the report period containing t: aligned to the quarter start and clipped at its end.
func qualityPeriodAt(calendar distribution.Calendar, t time.Time) (time.Time, time.Time, error) {
	quarterStart, quarterEnd := calendar.Quarter(t)
	if quarterStart.IsZero() {
		return time.Time{}, time.Time{}, errors.New("monitor: quality calendar has no quarter")
	}
	length, err := qualityPeriodLength(calendar.Day())
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	start := quarterStart.Add(t.Sub(quarterStart) / length * length)
	end := start.Add(length)
	if end.After(quarterEnd) {
		end = quarterEnd
	}
	return start, end, nil
}

func qualityPlanKey(providerID string, periodStart time.Time) string {
	return providerID + "\x00" + wire.FormatTime(periodStart)
}

// qualityRosterFor fixes the providers one member reports on for one period, reading the committee state only once.
// A provider with a claim this member witnessed in the quarter is on the roster when a configured provider shares its line;
// one without a configured URL cannot be probed and is logged.
func qualityRosterFor(s *Service, member *Member, periodStart time.Time) ([]Provider, error) {
	key := wire.FormatTime(periodStart)
	var roster qualityRoster
	var found bool
	if err := member.DB().View(func(tx store.Tx) error {
		var err error
		roster, found, err = qualityRosters.Get(tx, key)
		return err
	}); err != nil || found {
		return roster.Providers, err
	}
	configured := s.ProviderList()
	quarterStart, quarterEnd := s.Calendar().Quarter(periodStart)
	err := member.View(func(state *committee.Monitor) error {
		lines := make(map[string]bool, len(configured))
		for _, provider := range configured {
			lines[state.Line(provider.ID)] = true
		}
		for _, claim := range state.AdoptedClaims(quarterStart, quarterEnd) {
			if line := state.Line(claim.ProviderID); !lines[line] {
				lines[line] = true
				log.Printf("monitor: quality member %d: provider %s has a settled claim this quarter but no configured URL", member.Index(), claim.ProviderID)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	roster = qualityRoster{Providers: configured}
	return roster.Providers, member.DB().Update(func(tx store.Tx) error {
		var earlier []string
		if err := qualityRosters.Scan(tx, store.Range{End: key}, func(old string, _ qualityRoster) (bool, error) {
			earlier = append(earlier, old)
			return true, nil
		}); err != nil {
			return err
		}
		for _, old := range earlier {
			if err := qualityRosters.Delete(tx, old); err != nil {
				return err
			}
		}
		return qualityRosters.Put(tx, key, roster)
	})
}

// qualityAdmit records one request to a source in a route class unless the requests of the last 60 seconds already reach limit.
func qualityAdmit(s *Service, sourceURL string, kind quality.Kind, limit int, now time.Time) (bool, error) {
	key := sourceURL + "\x00" + string(kind)
	admitted := false
	err := s.DB().Update(func(tx store.Tx) error {
		sends, _, err := qualitySends.Get(tx, key)
		if err != nil {
			return err
		}
		recent := make([]string, 0, len(sends.Sent)+1)
		for _, value := range sends.Sent {
			at, parseErr := wire.ParseTime(value)
			if parseErr == nil && at.After(now.Add(-time.Minute)) && !at.After(now) {
				recent = append(recent, value)
			}
		}
		if len(recent) >= limit {
			return nil
		}
		admitted = true
		return qualitySends.Put(tx, key, qualitySendLog{Sent: append(recent, wire.FormatTime(now))})
	})
	return admitted, err
}

// qualitySyncMirror mirrors source's /v1/changes into the shared journal and reports whether it reached the end of the stream.
// Changes reads count against the source's listing limit.
func qualitySyncMirror(s *Service, ctx context.Context, source Provider, now time.Time) (bool, error) {
	sourceKey := qualitySourceKey(source)
	var cursor qualityMirrorCursor
	if err := s.DB().View(func(tx store.Tx) error {
		var err error
		cursor, _, err = qualityMirrorCursors.Get(tx, sourceKey)
		return err
	}); err != nil {
		return false, err
	}
	client, err := sdk.New(source.URL, sdk.Options{ServiceID: source.ID, AllowLoopbackHTTP: true, HTTPClient: s.HTTPClient(), Timeout: qualityProbeTimeout, Now: s.Now})
	if err != nil {
		return false, err
	}
	for page := 0; page < qualityMirrorPagesPerPass; page++ {
		admitted, err := qualityAdmit(s, source.URL, quality.KindAvailability, qualityListingPerSourcePerMinute, now)
		if err != nil {
			return false, err
		}
		if !admitted {
			return false, errQualityDeferred
		}
		result, err := client.Changes(ctx, cursor.After, qualityChangesLimit)
		if err != nil {
			return false, err
		}
		cursor = qualityMirrorCursor{After: uint64(result.Page.NextAfter), Synced: !result.Page.More}
		if err := s.DB().Update(func(tx store.Tx) error {
			for _, item := range result.Page.Items {
				receipt := item.Message.SubmissionReceipt
				if receipt.MessageID != item.Message.MessageID {
					continue
				}
				hash, err := settlement.ReceiptHash(receipt)
				if err != nil {
					continue
				}
				record := qualityMirrorRecord{MessageID: receipt.MessageID, ProviderID: receipt.ProviderID, Network: receipt.Network, ReceiptHash: hash, IssuedAt: receipt.IssuedAt}
				if err := qualityMirror.Put(tx, sourceKey+"\x00"+receipt.MessageID, record); err != nil {
					return err
				}
			}
			return qualityMirrorCursors.Put(tx, sourceKey, cursor)
		}); err != nil {
			return false, err
		}
		if cursor.Synced {
			return true, nil
		}
	}
	return false, nil
}

func qualitySourceKey(source Provider) string { return source.ID + "\x00" + source.URL }

// qualityVaultUnreachable separates a vault that could not be reached from one that answered with an error.
func qualityVaultUnreachable(err error) bool {
	var public *sdk.Error
	if errors.As(err, &public) && (public.HTTPStatus == http.StatusServiceUnavailable || public.HTTPStatus == http.StatusTooManyRequests) {
		return true
	}
	switch sdk.Classify(err) {
	case sdk.SignalTimeout, sdk.SignalUnavailable, sdk.SignalRateLimited:
		return true
	case sdk.SignalOther:
		return !errors.As(err, &public)
	}
	return false
}

// qualityFrameEntry is one retained settled message and the receipt that settled it.
type qualityFrameEntry struct {
	MessageID   string
	ReceiptHash string
}

// qualityBuildFrame keeps the mirrored records whose receipt names provider's line, sits in a claim this member
// witnessed with leaves and outside any equivocation, and has not reached retain_until.
func qualityBuildFrame(s *Service, member *Member, provider, source Provider, now time.Time) ([]qualityFrameEntry, qualityExclusions, error) {
	sourceKey := qualitySourceKey(source)
	var records []qualityMirrorRecord
	if err := s.DB().View(func(tx store.Tx) error {
		return qualityMirror.Scan(tx, store.Range{Start: sourceKey + "\x00", End: sourceKey + "\x01"}, func(_ string, record qualityMirrorRecord) (bool, error) {
			records = append(records, record)
			return true, nil
		})
	}); err != nil {
		return nil, qualityExclusions{}, err
	}
	var frame []qualityFrameEntry
	var excluded qualityExclusions
	err := member.View(func(state *committee.Monitor) error {
		line := state.Line(provider.ID)
		equivocated := make(map[string]bool)
		for _, proof := range state.EquivocationProofs(provider.ID) {
			for _, claim := range proof.Claims {
				equivocated[claim.ClaimID] = true
			}
		}
		for _, record := range records {
			if state.Line(record.ProviderID) != line {
				continue
			}
			claim, ok := state.ReceiptClaim(record.ProviderID, record.Network, record.ReceiptHash)
			if !ok {
				excluded.NoClaim++
				continue
			}
			if equivocated[claim.ClaimID] {
				excluded.Equivocation++
				continue
			}
			declaration, ok := state.Declaration(claim.DeclarationID)
			if !ok {
				excluded.NoDeclaration++
				continue
			}
			issuedAt, err := wire.ParseTime(record.IssuedAt)
			if err != nil {
				continue
			}
			retainUntil, err := retention.RetainUntil(issuedAt, claim.ProviderID, claim.DeclarationID, []wire.RetentionDeclaration{declaration})
			if err != nil || retention.Expired(retainUntil, now) {
				continue
			}
			frame = append(frame, qualityFrameEntry{MessageID: record.MessageID, ReceiptHash: record.ReceiptHash})
		}
		return nil
	})
	if err != nil {
		return nil, qualityExclusions{}, err
	}
	sort.Slice(frame, func(i, j int) bool { return frame[i].MessageID < frame[j].MessageID })
	if len(frame) > qualityMaxFrame {
		frame = frame[:qualityMaxFrame]
	}
	return frame, excluded, nil
}

// qualityNewPlan draws the seed, the sample and the probe times of one member for one provider and period.
func qualityNewPlan(provider Provider, periodStart, periodEnd, windowStart time.Time, source string, frame []qualityFrameEntry, excluded qualityExclusions, position, members int) (qualityPlan, error) {
	seedBytes := make([]byte, 32)
	if _, err := io.ReadFull(qualitySeedSource, seedBytes); err != nil {
		return qualityPlan{}, fmt.Errorf("monitor: quality seed: %w", err)
	}
	ids := make([]string, len(frame))
	hashes := make(map[string]string, len(frame))
	for i, entry := range frame {
		ids[i] = entry.MessageID
		hashes[entry.MessageID] = entry.ReceiptHash
	}
	seed := keys.EncodeB64URL(seedBytes)
	commitment, err := quality.SeedCommitment(seed)
	if err != nil {
		return qualityPlan{}, err
	}
	frameDigest, err := quality.FrameDigest(ids)
	if err != nil {
		return qualityPlan{}, err
	}
	sample, err := quality.Select(seed, ids, min(qualityProbes, len(ids)))
	if err != nil {
		return qualityPlan{}, err
	}
	availability, err := qualityProbeTimes(seedBytes, quality.KindAvailability, periodStart, windowStart, periodEnd, qualityProbes, qualityListingPerSourcePerMinute, position, members)
	if err != nil {
		return qualityPlan{}, err
	}
	retrieval, err := qualityProbeTimes(seedBytes, quality.KindRetrieval, periodStart, windowStart, periodEnd, len(sample), qualityDirectPerSourcePerMinute, position, members)
	if err != nil {
		return qualityPlan{}, err
	}
	probes := make([]qualityPlannedProbe, 0, len(availability)+len(retrieval))
	for _, at := range availability {
		probes = append(probes, qualityPlannedProbe{Kind: string(quality.KindAvailability), StartedAt: wire.FormatTime(at)})
	}
	for i, at := range retrieval {
		id := sample[i]
		probes = append(probes, qualityPlannedProbe{Kind: string(quality.KindRetrieval), MessageID: &id, StartedAt: wire.FormatTime(at), ReceiptHash: hashes[id]})
	}
	sort.SliceStable(probes, func(i, j int) bool {
		if probes[i].StartedAt != probes[j].StartedAt {
			return probes[i].StartedAt < probes[j].StartedAt
		}
		return probes[i].Kind < probes[j].Kind
	})
	timesDigest, err := qualityProbeTimesDigest(probes)
	if err != nil {
		return qualityPlan{}, err
	}
	return qualityPlan{
		ProviderID: provider.ID, ProviderURL: provider.URL, PeriodStart: wire.FormatTime(periodStart), PeriodEnd: wire.FormatTime(periodEnd),
		WindowStart: wire.FormatTime(windowStart), Members: members, FrameSource: source, FrameSize: len(ids), FrameDigest: frameDigest, Excluded: excluded,
		Seed: seed, SeedCommitment: commitment, Probes: probes, ProbeTimesDigest: timesDigest,
	}, nil
}

// qualityProbeTimesDigest hashes the JCS array of planned {kind, message_id, started_at}, which the observation set later reveals.
func qualityProbeTimesDigest(probes []qualityPlannedProbe) (string, error) {
	type plannedTime struct {
		Kind      string  `json:"kind"`
		MessageID *string `json:"message_id"`
		StartedAt string  `json:"started_at"`
	}
	times := make([]plannedTime, len(probes))
	for i, probe := range probes {
		times[i] = plannedTime{Kind: probe.Kind, MessageID: probe.MessageID, StartedAt: probe.StartedAt}
	}
	encoded, err := canon.Encode(times)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:]), nil
}

// qualityProbeTimes draws count probe times for one route class. Slots of ceil(60s/limit) start at periodStart;
// member position j of members uses only the slots whose index is congruent to j, so the hosted members together
// stay within limit per minute. Slots at or after windowStart are ranked by SHA-256(seed || 0x00 || kind || 0x00 ||
// decimal slot index) and the count lowest-ranked are taken.
func qualityProbeTimes(seed []byte, kind quality.Kind, periodStart, windowStart, periodEnd time.Time, count, limit, position, members int) ([]time.Time, error) {
	if limit < 1 || members < 1 || position < 0 || position >= members {
		return nil, errors.New("monitor: invalid quality rate configuration")
	}
	slot := time.Duration((60+limit-1)/limit) * time.Second
	type rankedSlot struct {
		at   time.Time
		rank [32]byte
	}
	var slots []rankedSlot
	for index := position; periodStart.Add(time.Duration(index) * slot).Before(periodEnd); index += members {
		at := periodStart.Add(time.Duration(index) * slot)
		if at.Before(windowStart) {
			continue
		}
		input := append(append(append([]byte(nil), seed...), 0), kind...)
		input = append(append(input, 0), strconv.Itoa(index)...)
		slots = append(slots, rankedSlot{at: at, rank: sha256.Sum256(input)})
	}
	if len(slots) < count {
		return nil, fmt.Errorf("monitor: quality window %s to %s has %d %s slots for %d probes with %d hosted members at %d requests per minute per source", wire.FormatTime(windowStart), wire.FormatTime(periodEnd), len(slots), kind, count, members, limit)
	}
	sort.Slice(slots, func(i, j int) bool { return string(slots[i].rank[:]) < string(slots[j].rank[:]) })
	times := make([]time.Time, count)
	for i := range times {
		times[i] = slots[i].at
	}
	sort.Slice(times, func(i, j int) bool { return times[i].Before(times[j]) })
	return times, nil
}
