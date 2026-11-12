package provider

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/delegation"
	"foryou.local/foryou/internal/discovery"
	"foryou.local/foryou/internal/distribution"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/funding"
	"foryou.local/foryou/internal/governance"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/succession"
	"foryou.local/foryou/internal/wire"
)

// Service is the provider HTTP service and its shared cross-area components.
type Service struct {
	// Chain is the configured faux chain interface.
	Chain faux.Chain
	// Ledger is the provider's funding ledger.
	Ledger *funding.Ledger
	// ServiceAuth authenticates root-authorized administrative requests.
	ServiceAuth *delegation.ServiceAuth
	// Authority stores authorization epochs and revocations.
	Authority *delegation.Authority
	// Successions is the verified key succession registry.
	Successions       *succession.Registry
	cfg               Config
	content, controls store.Store
	handler           http.Handler
	reads             *limiter
	records           *Records
	snapshots         *discovery.Snapshots
	mu                sync.Mutex
	loops             []loop
	registering       bool
	closed            bool
	stop              chan struct{}
	stopOnce          sync.Once
	runWG             sync.WaitGroup
	highWater         time.Time
	controlsState     ControlState
	policies          []wire.DistributionPolicy
	schedules         []wire.FeeSchedule
	adoptMu           sync.RWMutex
	visibility        *visibility
	gen               uint64
	fundingJournal    funding.Journal
	admissionMu       sync.Mutex
}
type loop struct {
	name     string
	interval time.Duration
	fn       func(context.Context, time.Time) error
}

var routeGroups []func(*Service, *http.ServeMux)

// New validates, opens storage, wires shared components, and constructs the route mux.
func New(cfg Config) (*Service, error) {
	var err error
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	cfg = cloneConfig(cfg)
	s := &Service{cfg: cfg, Chain: cfg.Faux, controlsState: allowControls{}, stop: make(chan struct{})}
	if cfg.ContentStore != nil {
		s.content = cfg.ContentStore
	} else if cfg.DataDir == "" {
		s.content = store.NewMemory()
	} else {
		v, e := store.Open(filepath.Join(cfg.DataDir, "content"))
		if e != nil {
			return nil, e
		}
		s.content = v
	}
	if cfg.ControlStore != nil {
		s.controls = cfg.ControlStore
	} else if cfg.DataDir == "" {
		s.controls = store.NewMemory()
	} else {
		v, e := store.Open(filepath.Join(cfg.DataDir, "controls"))
		if e != nil {
			s.content.Close()
			return nil, e
		}
		s.controls = v
	}
	fail := func(err error) (*Service, error) {
		_ = s.content.Close()
		_ = s.controls.Close()
		if s.fundingJournal != nil {
			_ = closeJournal(s.fundingJournal)
		}
		return nil, err
	}
	if err := s.initializeShared(); err != nil {
		return fail(err)
	}
	s.records = &Records{content: s.content, index: discovery.NewIndex(), now: s.Now, owner: s, states: map[string]recordMeta{}}
	if err = s.loadRecords(); err != nil {
		return fail(err)
	}
	s.reads = newLimiter(cfg.Reads, cfg.Now)
	s.visibility = &visibility{service: s, hidden: make(map[string]uint64)}
	s.snapshots, err = discovery.NewSnapshots(cfg.Snapshots, cfg.Now, cfg.Random)
	if err != nil {
		return fail(err)
	}
	if err = s.ensureDeclaration(); err != nil {
		return fail(err)
	}
	mux := http.NewServeMux()
	s.registering = true
	registerHealth(s, mux)
	for _, group := range routeGroups {
		group(s, mux)
	}
	s.registering = false
	s.handler = mux
	return s, nil
}
func (s *Service) loadRecords() error {
	return s.content.View(func(tx store.Tx) error {
		var max uint64
		if err := tx.Scan("provider.records", store.Range{}, func(id string, b []byte) (bool, error) {
			var m recordMeta
			if e := json.Unmarshal(b, &m); e != nil {
				return false, e
			}
			s.records.states[id] = m
			if m.Sequence > max {
				max = m.Sequence
			}
			if m.State == RecordRetained {
				msg, ok, e := tx.Get("provider.message_bytes", id)
				if e != nil {
					return false, e
				}
				if !ok {
					log.Printf("provider: retained record %s lacks stored bytes; withheld until an identical retry repairs it", id)
					return true, nil
				}
				parsed, e := discovery.ParseRecord(msg, m.Sequence)
				if e != nil {
					return false, e
				}
				s.records.index.Add(parsed)
			}
			return true, nil
		}); err != nil {
			return err
		}
		if err := tx.Scan("provider.sequence", store.Range{}, func(key string, _ []byte) (bool, error) {
			n := sequenceValue(key)
			if n > max {
				max = n
			}
			return true, nil
		}); err != nil {
			return err
		}
		s.records.index.ObserveSequence(max)
		return nil
	})
}

// Handler returns the provider mux assembled during New.
func (s *Service) Handler() http.Handler { return s.handler }

// Run executes registered route-group loops until ctx is cancelled.
func (s *Service) Run(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	ls := append([]loop(nil), s.loops...)
	s.runWG.Add(len(ls))
	s.mu.Unlock()
	if len(ls) == 0 {
		return nil
	}
	var wg sync.WaitGroup
	for _, l := range ls {
		wg.Add(1)
		go func(l loop) {
			defer wg.Done()
			defer s.runWG.Done()
			ticker := time.NewTicker(l.interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-s.stop:
					return
				case <-ticker.C:
					if err := l.fn(ctx, s.Now()); err != nil {
						log.Printf("provider loop %s: %v", l.name, err)
					}
					if err := s.persistClock(); err != nil {
						log.Printf("provider clock persistence: %v", err)
					}
					if err := s.PersistSuccessions(); err != nil {
						log.Printf("provider succession persistence: %v", err)
					}
				}
			}
		}(l)
	}
	wg.Wait()
	return nil
}

// RunOnce runs registered loops once in their registration order and persists core state.
func (s *Service) RunOnce(ctx context.Context) error {
	s.mu.Lock()
	ls := append([]loop(nil), s.loops...)
	s.mu.Unlock()
	var first error
	for _, l := range ls {
		if err := l.fn(ctx, s.Now()); err != nil && first == nil {
			first = err
		}
	}
	clockErr := s.persistClock()
	successionErr := s.PersistSuccessions()
	if first != nil {
		return first
	}
	if clockErr != nil {
		return clockErr
	}
	return successionErr
}

// Close stops future work and closes provider stores and the funding journal.
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.stopOnce.Do(func() { close(s.stop) })
	s.mu.Unlock()
	s.runWG.Wait()
	var errs []error
	errs = append(errs, s.persistClock(), s.PersistSuccessions())
	if s.content != nil {
		errs = append(errs, s.content.Close())
	}
	if s.controls != nil {
		errs = append(errs, s.controls.Close())
	}
	if s.fundingJournal != nil {
		errs = append(errs, closeJournal(s.fundingJournal))
	}
	return errors.Join(errs...)
}
func closeJournal(j funding.Journal) error {
	if c, ok := j.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// Every registers a periodic route-group task during mux construction only.
func (s *Service) Every(name string, interval time.Duration, fn func(context.Context, time.Time) error) {
	if !s.registering || interval <= 0 || fn == nil {
		panic("provider: Every may only be called during route registration")
	}
	s.loops = append(s.loops, loop{name, interval, fn})
}

// Now returns the configured UTC clock truncated to seconds.
func (s *Service) Now() time.Time { return s.cfg.Now().UTC().Truncate(time.Second) }

// CheckClock rejects issuance after a clock rollback greater than 60 seconds.
func (s *Service) CheckClock() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.Now()
	if !s.highWater.IsZero() && now.Before(s.highWater.Add(-60*time.Second)) {
		return ErrClockUncertain
	}
	if now.After(s.highWater) {
		s.highWater = now
	}
	return nil
}
func (s *Service) persistClock() error {
	s.mu.Lock()
	now := s.Now()
	if now.After(s.highWater) {
		s.highWater = now
	}
	high := s.highWater
	s.mu.Unlock()
	return s.controls.Update(func(tx store.Tx) error {
		return tx.Put("provider.core.clock", "high_water", []byte(high.Format(time.RFC3339)))
	})
}

// ProviderID returns the provider's signing DID.
func (s *Service) ProviderID() string { return keys.DIDOf(s.cfg.Key) }

// Sign signs an object using its ForYou domain label.
func (s *Service) Sign(label string, unsigned any) (string, error) {
	b, e := canon.Encode(unsigned)
	if e != nil {
		return "", e
	}
	return keys.Sign(s.cfg.Key, label, b)
}

// Key returns the provider private signing key.
func (s *Service) Key() keys.PrivateKey { return append(keys.PrivateKey(nil), s.cfg.Key...) }

// Store returns the content store shared by provider route groups.
func (s *Service) Store() store.Store { return s.content }

// ControlStore returns the independently recoverable control store.
func (s *Service) ControlStore() store.Store { return s.controls }

// Genesis returns the configured trust root.
func (s *Service) Genesis() wire.GenesisManifest { return cloneGenesis(s.cfg.Genesis) }

// Trust returns the configured governance trust verifier.
func (s *Service) Trust() *governance.Trust { return s.cfg.Trust }

// Policies returns the adopted distribution policies.
func (s *Service) Policies() []wire.DistributionPolicy {
	s.adoptMu.RLock()
	defer s.adoptMu.RUnlock()
	result := append([]wire.DistributionPolicy(nil), s.policies...)
	for i := range result {
		result[i] = clonePolicy(result[i])
	}
	return result
}

// FeeSchedules returns verified, release-approved fee schedules.
func (s *Service) FeeSchedules() []wire.FeeSchedule {
	s.adoptMu.RLock()
	defer s.adoptMu.RUnlock()
	return append([]wire.FeeSchedule(nil), s.schedules...)
}

// Dev reports whether this service uses the supported local development mode.
func (s *Service) Dev() bool { return s.cfg.Dev }

// DataDir returns the configured role data directory.
func (s *Service) DataDir() string { return s.cfg.DataDir }

// Config returns a shallow copy of the provider configuration.
func (s *Service) Config() Config { return cloneConfig(s.cfg) }

// Records returns durable message records and their discovery index.
func (s *Service) Records() *Records { return s.records }

// Snapshots returns the bounded in-memory discovery snapshot manager.
func (s *Service) Snapshots() *discovery.Snapshots { return s.snapshots }

// Visibility returns the current record restriction and generation view.
func (s *Service) Visibility() discovery.Visibility { return s.visibility }

// PersistSuccessions stores the registry snapshot in the control store.
func (s *Service) PersistSuccessions() error {
	b, e := s.Successions.Snapshot()
	if e != nil {
		return e
	}
	return s.controls.Update(func(tx store.Tx) error { return tx.Put("provider.successions", "snapshot", b) })
}

// AddSchedule verifies and adopts a release-approved fee schedule.
func (s *Service) AddSchedule(a wire.ApprovedFeeSchedule) error {
	s.adoptMu.Lock()
	defer s.adoptMu.Unlock()
	root, e := governance.CheckGenesis(s.cfg.Genesis)
	if e != nil {
		return e
	}
	if e = s.verifySchedule(a, root.IDs, root.Threshold); e != nil {
		return e
	}
	for _, existing := range s.schedules {
		if existing.Network == a.Schedule.Network && existing.Revision == a.Schedule.Revision {
			if existing == a.Schedule {
				return nil
			}
			return errors.New("provider: conflicting fee schedule revision")
		}
	}
	next := append(append([]wire.FeeSchedule(nil), s.schedules...), a.Schedule)
	if a.Schedule.Revision > 1 {
		other := fee.BitcoinMainnet
		if a.Schedule.Network == fee.BitcoinMainnet {
			other = fee.EthereumMainnet
		}
		paired := false
		for _, candidate := range next {
			if candidate.Network == other && candidate.Revision > 1 && candidate.EffectiveAt == a.Schedule.EffectiveAt {
				paired = true
			}
		}
		if paired {
			if e = fee.CheckScheduleSet(next); e != nil {
				return e
			}
		}
	}
	s.schedules = next
	sort.Slice(s.schedules, func(i, j int) bool {
		if s.schedules[i].Network != s.schedules[j].Network {
			return s.schedules[i].Network < s.schedules[j].Network
		}
		return s.schedules[i].Revision < s.schedules[j].Revision
	})
	return nil
}

// AddPolicy verifies a policy against the adopted history before making it available.
func (s *Service) AddPolicy(p wire.DistributionPolicy) error {
	s.adoptMu.Lock()
	defer s.adoptMu.Unlock()
	if e := distribution.CheckPolicy(p, s.policies, s.cfg.Trust); e != nil {
		return e
	}
	s.policies = append(s.policies, clonePolicy(p))
	sort.Slice(s.policies, func(i, j int) bool { return s.policies[i].EffectiveAt < s.policies[j].EffectiveAt })
	return nil
}

func clonePolicy(p wire.DistributionPolicy) wire.DistributionPolicy {
	p.Monitors = append([]string(nil), p.Monitors...)
	p.ReleaseSignatures = append([]wire.ReleaseSignature(nil), p.ReleaseSignatures...)
	return p
}

func cloneGenesis(g wire.GenesisManifest) wire.GenesisManifest {
	g.ReleaseKeyIDs = append([]string(nil), g.ReleaseKeyIDs...)
	g.ReleaseSignatures = append([]wire.ReleaseSignature(nil), g.ReleaseSignatures...)
	g.FeeSchedules = append([]wire.FeeSchedule(nil), g.FeeSchedules...)
	g.FeeScheduleApprovals = append([]wire.FeeScheduleApproval(nil), g.FeeScheduleApprovals...)
	for i := range g.FeeScheduleApprovals {
		g.FeeScheduleApprovals[i].ReleaseSignatures = append([]wire.ReleaseSignature(nil), g.FeeScheduleApprovals[i].ReleaseSignatures...)
	}
	g.FoundationKeyCertificates = append([]wire.FoundationKeyCertificate(nil), g.FoundationKeyCertificates...)
	for i := range g.FoundationKeyCertificates {
		g.FoundationKeyCertificates[i].ReleaseSignatures = append([]wire.ReleaseSignature(nil), g.FoundationKeyCertificates[i].ReleaseSignatures...)
	}
	g.DistributionPolicy = clonePolicy(g.DistributionPolicy)
	return g
}

func cloneConfig(c Config) Config {
	c.Key = append(keys.PrivateKey(nil), c.Key...)
	c.Genesis = cloneGenesis(c.Genesis)
	c.Policies = append([]wire.DistributionPolicy(nil), c.Policies...)
	for i := range c.Policies {
		c.Policies[i] = clonePolicy(c.Policies[i])
	}
	c.FeeSchedules = append([]wire.ApprovedFeeSchedule(nil), c.FeeSchedules...)
	for i := range c.FeeSchedules {
		c.FeeSchedules[i].Approval.ReleaseSignatures = append([]wire.ReleaseSignature(nil), c.FeeSchedules[i].Approval.ReleaseSignatures...)
	}
	c.Operator.MarketsAssessed = append([]string(nil), c.Operator.MarketsAssessed...)
	return c
}

// ErrClockUncertain identifies a rollback greater than the allowed 60 seconds.
var ErrClockUncertain = errors.New("provider: clock uncertain")
