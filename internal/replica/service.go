// Package replica implements an independently operated mirror with its own key that copies
// verified records from explicitly pinned providers and assigns its own local sequences. Copies
// are evidence, not new admission, and the replica never claims an admitting provider's acceptance
// or finalization.
package replica

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/discovery"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/store"
)

// Source pins one provider endpoint and identity as an explicitly trusted sync source.
type Source struct {
	// Origin is an absolute https origin without path, query, fragment or userinfo; plain http is
	// allowed only for a loopback host in dev mode.
	Origin string
	// ProviderID is the pinned provider DID; trust is the explicit source list, never discovery.
	ProviderID string
}

// OperatorConfig holds the operator contacts disclosed by the replica manifest.
type OperatorConfig struct {
	// LegalContact is the operator's legal contact URI or address.
	LegalContact string
	// AbuseContact is the operator's abuse contact URI or address.
	AbuseContact string
	// PrivacyContact is the operator's privacy contact URI or address.
	PrivacyContact string
	// MarketsAssessed lists markets the operator has assessed for its disclosure.
	MarketsAssessed []string
}

// ReadLimits bounds anonymous direct and listing requests.
type ReadLimits struct {
	// DirectPerMinute defaults to 600 and cannot be below the portability floor of 600.
	DirectPerMinute int
	// DirectPerSourcePerMinute defaults to 30 and cannot be below the floor of 30.
	DirectPerSourcePerMinute int
	// ListingPerMinute defaults to 60 and cannot be below the floor of 60.
	ListingPerMinute int
	// ListingPerSourcePerMinute defaults to 6 and cannot be below the floor of 6.
	ListingPerSourcePerMinute int
	// MaxConcurrent defaults to 64 and must be positive.
	MaxConcurrent int
	// MaxSources defaults to 4096 and must be positive.
	MaxSources int
}

// Config configures the replica's identity, storage, sync sources and public limits.
type Config struct {
	// DataDir is the journal directory; empty selects an in-memory store.
	DataDir string
	// Key is the replica's required private signing key.
	Key keys.PrivateKey
	// Sources is the explicit allowlist of pinned providers.
	Sources []Source
	// Now supplies the service clock; nil uses the system clock.
	Now func() time.Time
	// PublicOrigin is the replica's optional public https origin, without path or credentials.
	PublicOrigin string
	// Dev enables loopback plain-http source origins.
	Dev bool
	// PollInterval defaults to 30 seconds and must be positive.
	PollInterval time.Duration
	// Reads contains anonymous read limits; zero fields use defaults and configured rates must
	// meet the portability floors documented on ReadLimits.
	Reads ReadLimits
	// Snapshots configures bounded discovery snapshots; zero values use discovery defaults and
	// configured values must satisfy discovery.SnapshotLimits validation.
	Snapshots discovery.SnapshotLimits
	// DirectCacheMaxAge is the max-age of a direct-read 200; zero selects the default of one hour,
	// and a longer lifetime is rejected (docs/05-portability.md "Read capacity and caching").
	DirectCacheMaxAge time.Duration
	// Operator supplies contacts and assessed markets for the replica manifest.
	Operator OperatorConfig
}

type loop struct {
	name     string
	interval time.Duration
	fn       func(context.Context, time.Time) error
}

// Service owns one replica store, its local index, signing identity and HTTP routes.
type Service struct {
	cfg         Config
	db          store.Store
	records     *Records
	index       *discovery.Index
	snapshots   *discovery.Snapshots
	mux         *http.ServeMux
	loops       []loop
	registering bool
	mu          sync.Mutex
	closed      bool
	running     bool
	stop        context.CancelFunc
	loopsWG     sync.WaitGroup
	limiter     *readLimiter
}

// routeGroups are populated by independently owned wave-4 route files.
var routeGroups []func(*Service, *http.ServeMux)

// New validates configuration, opens storage, restores the local index and builds routes once.
func New(cfg Config) (*Service, error) {
	cfg = effectiveConfig(cfg)
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	var db store.Store
	var err error
	if cfg.DataDir == "" {
		db = store.NewMemory()
	} else {
		db, err = store.Open(cfg.DataDir)
		if err != nil {
			return nil, fmt.Errorf("replica: open store: %w", err)
		}
	}
	index := discovery.NewIndex()
	records := newRecords(db, index, cfg.Now)
	if err := records.restore(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("replica: restore records: %w", err)
	}
	snapshots, err := discovery.NewSnapshots(cfg.Snapshots, cfg.Now, nil)
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	s := &Service{cfg: cfg, db: db, records: records, index: index, snapshots: snapshots, mux: http.NewServeMux(), limiter: newReadLimiter(cfg.Reads, cfg.Now)}
	s.addHealthRoutes(s.mux)
	s.registering = true
	for _, group := range routeGroups {
		group(s, s.mux)
	}
	s.registering = false
	return s, nil
}

// DevConfig returns the deterministic local replica configuration.
func DevConfig(root string) (Config, error) {
	return Config{
		DataDir: store.DataDir(root, "replica"), Key: devnet.ReplicaKey(),
		Sources:      []Source{{Origin: "http://127.0.0.1:8445", ProviderID: keys.DIDOf(devnet.ProviderKey(0))}},
		PublicOrigin: "https://127.0.0.1:8446", Dev: true,
		Operator: OperatorConfig{LegalContact: "legal@example.invalid", AbuseContact: "abuse@example.invalid", PrivacyContact: "privacy@example.invalid"},
	}, nil
}

func effectiveConfig(cfg Config) Config {
	cfg.Key = append(keys.PrivateKey(nil), cfg.Key...)
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 30 * time.Second
	}
	cfg.Reads = effectiveReadLimits(cfg.Reads)
	if cfg.DirectCacheMaxAge == 0 {
		cfg.DirectCacheMaxAge = time.Hour
	}
	cfg.Sources = append([]Source(nil), cfg.Sources...)
	cfg.Operator.MarketsAssessed = append([]string(nil), cfg.Operator.MarketsAssessed...)
	return cfg
}

func effectiveReadLimits(l ReadLimits) ReadLimits {
	if l.DirectPerMinute == 0 {
		l.DirectPerMinute = 600
	}
	if l.DirectPerSourcePerMinute == 0 {
		l.DirectPerSourcePerMinute = 30
	}
	if l.ListingPerMinute == 0 {
		l.ListingPerMinute = 60
	}
	if l.ListingPerSourcePerMinute == 0 {
		l.ListingPerSourcePerMinute = 6
	}
	if l.MaxConcurrent == 0 {
		l.MaxConcurrent = 64
	}
	if l.MaxSources == 0 {
		l.MaxSources = 4096
	}
	return l
}

func validateConfig(cfg Config) error {
	if len(cfg.Key) != ed25519.PrivateKeySize {
		return errors.New("replica: invalid signing key")
	}
	derived, err := keys.NewKeyFromSeed(cfg.Key.Seed())
	if err != nil || !bytes.Equal(derived, cfg.Key) {
		return errors.New("replica: inconsistent signing key")
	}
	if cfg.PollInterval <= 0 {
		return errors.New("replica: poll interval must be positive")
	}
	if cfg.PublicOrigin != "" && !validHTTPSOrigin(cfg.PublicOrigin) {
		return errors.New("replica: public origin must be an https origin")
	}
	if cfg.DirectCacheMaxAge < 0 || cfg.DirectCacheMaxAge > time.Hour || cfg.DirectCacheMaxAge%time.Second != 0 {
		return errors.New("replica: direct cache age must be whole seconds up to one hour")
	}
	limits := cfg.Reads
	if limits.DirectPerMinute < 600 || limits.DirectPerSourcePerMinute < 30 || limits.ListingPerMinute < 60 || limits.ListingPerSourcePerMinute < 6 || limits.MaxConcurrent < 1 || limits.MaxSources < 1 {
		return errors.New("replica: read limits are below required floors")
	}
	for _, source := range cfg.Sources {
		if !validSourceOrigin(source.Origin, cfg.Dev) {
			return fmt.Errorf("replica: invalid source origin %q", source.Origin)
		}
		if _, err := keys.ParseDID(source.ProviderID); err != nil {
			return fmt.Errorf("replica: invalid source provider ID: %w", err)
		}
	}
	return nil
}

func validHTTPSOrigin(value string) bool {
	u, err := url.Parse(value)
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.Path == "" && u.RawQuery == "" && u.Fragment == ""
}

func validSourceOrigin(value string, dev bool) bool {
	u, err := url.Parse(value)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return false
	}
	if u.Scheme == "https" {
		return true
	}
	return dev && u.Scheme == "http" && isLoopbackHost(u.Hostname())
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Handler returns the mux built during construction.
func (s *Service) Handler() http.Handler { return s.mux }

// Every registers a periodic service loop during route-group construction.
func (s *Service) Every(name string, interval time.Duration, fn func(context.Context, time.Time) error) {
	if !s.registering {
		panic("replica: Every called outside route registration")
	}
	if name == "" || interval <= 0 || fn == nil {
		panic("replica: invalid loop registration")
	}
	s.loops = append(s.loops, loop{name: name, interval: interval, fn: fn})
}

// Run executes registered loops until ctx ends; failed iterations are logged and retried.
func (s *Service) Run(ctx context.Context) error {
	if len(s.loops) == 0 {
		return nil
	}
	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		cancel()
		return nil
	}
	if s.running {
		s.mu.Unlock()
		cancel()
		return errors.New("replica: loops are already running")
	}
	s.running = true
	s.stop = cancel
	for _, item := range s.loops {
		s.loopsWG.Add(1)
		go func(item loop) {
			defer s.loopsWG.Done()
			ticker := time.NewTicker(item.interval)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					if err := item.fn(ctx, s.Now()); err != nil {
						slog.Error("replica loop failed", "name", item.name, "error", err)
					}
				}
			}
		}(item)
	}
	s.mu.Unlock()
	<-ctx.Done()
	s.loopsWG.Wait()
	s.mu.Lock()
	s.running = false
	s.stop = nil
	s.mu.Unlock()
	cancel()
	return nil
}

// RunOnce executes registered loops synchronously in registration order.
func (s *Service) RunOnce(ctx context.Context) error {
	for _, item := range s.loops {
		if err := item.fn(ctx, s.Now()); err != nil {
			return fmt.Errorf("replica loop %s: %w", item.name, err)
		}
	}
	return nil
}

// Close stops service loops and closes the underlying store.
func (s *Service) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return store.ErrClosed
	}
	s.closed = true
	if s.stop != nil {
		s.stop()
	}
	s.mu.Unlock()
	s.loopsWG.Wait()
	return s.db.Close()
}

// Records returns the durable replica record collection.
func (s *Service) Records() *Records { return s.records }

// Store returns the service store.
func (s *Service) Store() store.Store { return s.db }

// Config returns the effective configuration with independent slices.
func (s *Service) Config() Config {
	cfg := s.cfg
	cfg.Key = append(keys.PrivateKey(nil), cfg.Key...)
	cfg.Sources = append([]Source(nil), cfg.Sources...)
	cfg.Operator.MarketsAssessed = append([]string(nil), cfg.Operator.MarketsAssessed...)
	return cfg
}

// DataDir returns the configured durable storage directory.
func (s *Service) DataDir() string { return s.cfg.DataDir }

// Dev reports whether development mode is enabled.
func (s *Service) Dev() bool { return s.cfg.Dev }

// Now returns the configured clock in UTC, truncated to seconds.
func (s *Service) Now() time.Time { return s.cfg.Now().UTC().Truncate(time.Second) }

// ReplicaID returns the replica's own signing identity.
func (s *Service) ReplicaID() string { return keys.DIDOf(s.cfg.Key) }

// Snapshots returns the replica's bounded discovery snapshot manager.
func (s *Service) Snapshots() *discovery.Snapshots { return s.snapshots }

// Sources returns a copy of the explicitly configured sync sources.
func (s *Service) Sources() []Source { return append([]Source(nil), s.cfg.Sources...) }

// DirectCacheMaxAge returns the configured direct-read cache lifetime.
func (s *Service) DirectCacheMaxAge() time.Duration { return s.cfg.DirectCacheMaxAge }

// PublicOrigin returns the configured public HTTPS origin.
func (s *Service) PublicOrigin() string { return s.cfg.PublicOrigin }

// Operator returns the configured operator disclosure contacts.
func (s *Service) Operator() OperatorConfig {
	o := s.cfg.Operator
	o.MarketsAssessed = append([]string(nil), o.MarketsAssessed...)
	return o
}

// Sign signs canonical object bytes under the supplied ForYou domain label.
func (s *Service) Sign(label string, obj any) (string, error) {
	encoded, err := canon.Encode(obj)
	if err != nil {
		return "", err
	}
	return keys.Sign(s.cfg.Key, label, encoded)
}
