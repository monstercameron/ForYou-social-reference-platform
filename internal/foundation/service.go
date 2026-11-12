package foundation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sync"
	"time"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/distribution"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/governance"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

var routeGroups []func(*Service, *http.ServeMux)
var backgroundLoops []func(context.Context, *Service)

const (
	controlsCollection   = "foundation.controls"
	legalRestrictionsKey = "legal_restrictions"
)

// legalRestrictions is the committed Foundation legal-restriction state. Restricted maps each restricted
// message ID to the generation whose commit restricted it.
type legalRestrictions struct {
	Generation uint64            `json:"generation"`
	Restricted map[string]uint64 `json:"restricted"`
}

// Service serves Foundation publications and the Lifetime Vault.
type Service struct {
	cfg              Config
	db               *store.DB
	state            *state
	log              *governance.Log
	appendMu         sync.Mutex
	trustMu          sync.Mutex
	trustCertIDs     []string
	trust            *governance.Trust
	controlMu        sync.RWMutex
	controls         legalRestrictions
	controlsErr      error
	successionSource func(context.Context, string) ([]wire.KeySuccession, error)
	successionBudget int
}

// New validates configuration, verifies the trust root and replays the data directory.
func New(cfg Config) (*Service, error) {
	if cfg.Clock == nil {
		cfg.Clock = time.Now
	}
	if cfg.HeadInterval == 0 {
		cfg.HeadInterval = time.Hour
	}
	cfg = cloneConfig(cfg)
	now := cfg.Clock().UTC().Truncate(time.Second)
	trust, err := cfg.validate(now)
	if err != nil {
		return nil, err
	}
	db, err := store.Open(cfg.DataDir)
	if err != nil {
		return nil, err
	}
	savedState, err := openState(db, now)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	log, err := governance.NewLog(savedState.entries)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := validateReplay(savedState, log); err != nil {
		_ = db.Close()
		return nil, err
	}
	controls, controlsErr := recoverLegalRestrictions(db)
	s := &Service{
		cfg: cfg, db: db, state: savedState, log: log, trust: trust,
		controls: controls, controlsErr: controlsErr,
		successionBudget: vaultDeliveryMaxSuccessionLookups,
	}
	s.successionSource = s.monitorSuccessions
	s.trustCertIDs = certificateIDs(certificatesAt(cfg, now))
	return s, nil
}

// recoverLegalRestrictions loads the committed legal restrictions. An unreadable record leaves the vault unavailable
// rather than failing startup, so every vault read and delivery fails closed.
func recoverLegalRestrictions(db *store.DB) (legalRestrictions, error) {
	controls := legalRestrictions{Restricted: map[string]uint64{}}
	err := db.View(func(tx store.Tx) error {
		data, ok, err := tx.Get(controlsCollection, legalRestrictionsKey)
		if err != nil || !ok {
			return err
		}
		return json.Unmarshal(data, &controls)
	})
	if err != nil {
		return legalRestrictions{}, err
	}
	if controls.Restricted == nil {
		controls.Restricted = map[string]uint64{}
	}
	return controls, nil
}

// CommitLegalRestriction durably restricts messageID and returns the control generation that committed it.
// Restricting an already restricted message changes nothing and returns the current generation.
func (s *Service) CommitLegalRestriction(messageID string) (uint64, error) {
	s.controlMu.Lock()
	defer s.controlMu.Unlock()
	if s.controlsErr != nil {
		return 0, s.controlsErr
	}
	if _, restricted := s.controls.Restricted[messageID]; restricted {
		return s.controls.Generation, nil
	}
	next := legalRestrictions{Generation: s.controls.Generation + 1, Restricted: make(map[string]uint64, len(s.controls.Restricted)+1)}
	for id, generation := range s.controls.Restricted {
		next.Restricted[id] = generation
	}
	next.Restricted[messageID] = next.Generation
	if err := s.db.Update(func(tx store.Tx) error { return putJSON(tx, controlsCollection, legalRestrictionsKey, next) }); err != nil {
		return 0, err
	}
	s.controls = next
	return next.Generation, nil
}

// legalControlsReady returns the recovery error when the committed legal restrictions could not be loaded.
func (s *Service) legalControlsReady() error { return s.controlsErr }

// legalRestriction reports whether messageID is under a committed legal restriction and the generation that committed it.
func (s *Service) legalRestriction(messageID string) (uint64, bool, error) {
	if err := s.legalControlsReady(); err != nil {
		return 0, false, err
	}
	s.controlMu.RLock()
	defer s.controlMu.RUnlock()
	generation, restricted := s.controls.Restricted[messageID]
	return generation, restricted, nil
}

// legalGeneration returns the generation of the latest committed legal restriction.
func (s *Service) legalGeneration() (uint64, error) {
	if err := s.legalControlsReady(); err != nil {
		return 0, err
	}
	s.controlMu.RLock()
	defer s.controlMu.RUnlock()
	return s.controls.Generation, nil
}

// Close closes the journaled store; call it after Run has returned.
func (s *Service) Close() error { return s.db.Close() }

func validateReplay(saved *state, log *governance.Log) error {
	for _, entry := range saved.entries {
		object, ok := saved.objects[entry.ObjectHash]
		if !ok {
			return errors.New("foundation: log entry has no stored object")
		}
		canonical, err := canon.Canonicalize(object, canon.Limits{MaxDepth: 32})
		if err != nil || string(canonical) != string(object) {
			return errors.New("foundation: stored log object is not JCS")
		}
		digest := sha256.Sum256(object)
		if hex.EncodeToString(digest[:]) != entry.ObjectHash {
			return errors.New("foundation: stored log object hash mismatch")
		}
	}
	var previousSize int64
	for _, head := range saved.heads {
		if head.Size <= previousSize || head.Size > log.Len() {
			return errors.New("foundation: invalid persisted head size")
		}
		if err := governance.VerifyLogHead(head); err != nil {
			return err
		}
		root, err := log.Root(head.Size)
		if err != nil || root != head.Root {
			return errors.New("foundation: persisted head does not match log")
		}
		previousSize = head.Size
	}
	return nil
}

// Handler returns the Foundation HTTP handler.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	for _, register := range routeGroups {
		register(s, mux)
	}
	return apierr.Routed(mux)
}

// Run starts every registered background loop and waits for ctx cancellation.
func (s *Service) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("foundation: nil context")
	}
	var loops sync.WaitGroup
	for _, run := range backgroundLoops {
		loops.Add(1)
		go func(run func(context.Context, *Service)) {
			defer loops.Done()
			run(ctx, s)
		}(run)
	}
	<-ctx.Done()
	loops.Wait()
	if errors.Is(ctx.Err(), context.Canceled) {
		return nil
	}
	return ctx.Err()
}

// Clock returns the service time in UTC and whole seconds, as every protocol time is.
func (s *Service) Clock() time.Time { return s.cfg.Clock().UTC().Truncate(time.Second) }

// Trust returns the verified trust state for certificates issued by the current time.
func (s *Service) Trust() *governance.Trust {
	now := s.Clock()
	s.trustMu.Lock()
	defer s.trustMu.Unlock()
	certificates := certificatesAt(s.cfg, now)
	ids := certificateIDs(certificates)
	if equalStrings(ids, s.trustCertIDs) {
		return s.trust
	}
	trust, err := governance.NewTrust(s.cfg.Genesis, s.cfg.KeySetUpdates, certificates, s.cfg.Closures, nil)
	if err != nil {
		return s.trust
	}
	s.trust = trust
	s.trustCertIDs = ids
	return s.trust
}

func certificatesAt(cfg Config, now time.Time) []wire.FoundationKeyCertificate {
	if cfg.Certificates == nil {
		return nil
	}
	return cfg.Certificates(now)
}

func certificateIDs(certificates []wire.FoundationKeyCertificate) []string {
	ids := make([]string, len(certificates))
	for i, certificate := range certificates {
		ids[i] = certificate.CertificateID
	}
	return ids
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

// Calendar returns the configured statement calendar.
func (s *Service) Calendar() distribution.Calendar { return s.cfg.Calendar }

// Policies returns the verified distribution policy history.
func (s *Service) Policies() []wire.DistributionPolicy {
	result := append([]wire.DistributionPolicy(nil), s.cfg.Policies...)
	for i := range result {
		result[i] = clonePolicy(result[i])
	}
	return result
}

// PolicyInForce returns the latest approved policy effective at t.
func (s *Service) PolicyInForce(t time.Time) (wire.DistributionPolicy, bool) {
	policy, ok := distribution.InForce(s.cfg.Policies, t)
	return clonePolicy(policy), ok
}

// FeeSchedules returns every configured approved fee schedule.
func (s *Service) FeeSchedules() []wire.ApprovedFeeSchedule {
	result := append([]wire.ApprovedFeeSchedule(nil), s.cfg.FeeSchedules...)
	for i := range result {
		result[i].Approval.ReleaseSignatures = cloneSignatures(result[i].Approval.ReleaseSignatures)
	}
	return result
}

// Faux returns the configured faux chain client.
func (s *Service) Faux() faux.Chain { return s.cfg.Faux }

// Providers returns the configured provider base URLs.
func (s *Service) Providers() []string { return append([]string(nil), s.cfg.Providers...) }

// Monitors returns a copy of monitor base URLs keyed by monitor DID.
func (s *Service) Monitors() map[string]string {
	result := make(map[string]string, len(s.cfg.Monitors))
	for id, url := range s.cfg.Monitors {
		result[id] = url
	}
	return result
}

// MonitorPayouts returns a copy of payout addresses keyed by monitor DID and network.
func (s *Service) MonitorPayouts() map[string]map[string]string {
	result := make(map[string]map[string]string, len(s.cfg.MonitorPayouts))
	for id, payouts := range s.cfg.MonitorPayouts {
		result[id] = make(map[string]string, len(payouts))
		for network, address := range payouts {
			result[id][network] = address
		}
	}
	return result
}
