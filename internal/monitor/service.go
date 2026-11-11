// Package monitor hosts the development settlement committee and its scheduler.
package monitor

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/distribution"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/governance"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

const (
	trustRefreshOrder         = 5
	peerSyncOrder             = 10
	auditOrder                = 20
	qualityOrder              = 30
	vaultDeliveryOrder        = 40
	monitorSnapshotCollection = "monitor.snapshot"
)

// Config supplies the genesis trust, peers, storage and scheduler settings.
type Config struct {
	Genesis      wire.GenesisManifest  // Genesis pins the release trust root and initial policy objects.
	Members      []keys.PrivateKey     // Members overrides the default five dev monitor identities.
	Peers        map[string]string     // Peers maps service names to their base URLs.
	Providers    map[string]string     // Providers maps provider did:key identities to base URLs.
	DataDir      string                // DataDir is the monitor role's journal directory.
	Now          func() time.Time      // Now supplies the service clock.
	LoopInterval time.Duration         // LoopInterval sets the scheduler interval.
	Calendar     distribution.Calendar // Calendar is shared with distribution verification.
	Faux         faux.Chain            // Faux overrides the HTTP faux-chain client.
	HTTPClient   *http.Client          // HTTPClient is shared by outgoing clients.
}

type loopStep struct {
	order int
	name  string
	run   func(*Service, context.Context) error
}

// Loop-step files append from their own init(); orders 5, 10, 20, 30 and 40
// are reserved for trust refresh, peer sync, audit, quality and vault delivery.
var loopSteps []loopStep

// Route-group files append from their own init(); routes are relative to the
// member prefix and read the current member with MemberFrom.
var routeGroups []func(*Service, *http.ServeMux)

// Service hosts committee members and their scheduled work.
type Service struct {
	config     Config
	members    []*Member
	byIndex    map[int]*Member
	shared     *store.DB
	view       *View
	httpClient *http.Client
	memberMux  *http.ServeMux
}

// Provider identifies a configured provider and its base URL.
type Provider struct {
	ID  string
	URL string
}

// New opens journals and restores the configured monitor members.
func New(cfg Config) (*Service, error) {
	if cfg.DataDir == "" {
		return nil, fmt.Errorf("monitor: DataDir is required")
	}
	if cfg.Genesis.Release == "" {
		cfg.Genesis = devnet.Genesis()
	}
	if err := configureProviders(&cfg); err != nil {
		return nil, err
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.LoopInterval <= 0 {
		cfg.LoopInterval = time.Minute
	}
	if cfg.Calendar == nil {
		calendar, err := devnet.Calendar(0)
		if err != nil {
			return nil, err
		}
		cfg.Calendar = calendar
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if cfg.Faux == nil {
		base := cfg.Peers["faux"]
		if base != "" {
			cfg.Faux = faux.NewClient(base, cfg.HTTPClient)
		}
	}
	if cfg.Members == nil {
		cfg.Members = []keys.PrivateKey{devnet.MonitorKey(0), devnet.MonitorKey(1), devnet.MonitorKey(2), devnet.MonitorKey(3), devnet.MonitorKey(4)}
	}
	certificates := devnet.Certificates(cfg.Now().UTC().Truncate(time.Second))
	trust, err := governance.NewTrust(cfg.Genesis, nil, certificates[1:], nil, nil)
	if err != nil {
		return nil, fmt.Errorf("monitor: build trust: %w", err)
	}
	view, err := openView(cfg.DataDir, cfg.Genesis, trust)
	if err != nil {
		return nil, err
	}
	shared, err := store.Open(storePath(cfg.DataDir, "shared"))
	if err != nil {
		_ = view.Close()
		return nil, fmt.Errorf("monitor: open shared journal: %w", err)
	}
	s := &Service{config: cfg, shared: shared, view: view, httpClient: cfg.HTTPClient, byIndex: make(map[int]*Member)}
	if cfg.Faux == nil {
		_ = shared.Close()
		_ = view.Close()
		return nil, fmt.Errorf("monitor: faux chain client is required")
	}
	for i, key := range cfg.Members {
		member, openErr := openMember(cfg.DataDir, i, key, view.Schedules(), &depositSource{chain: cfg.Faux, view: view})
		if openErr != nil {
			_ = s.Close()
			return nil, openErr
		}
		if _, exists := s.byIndex[i]; exists {
			_ = member.Close()
			_ = s.Close()
			return nil, fmt.Errorf("monitor: duplicate member index")
		}
		s.members = append(s.members, member)
		s.byIndex[i] = member
	}
	s.memberMux = http.NewServeMux()
	for _, register := range routeGroups {
		register(s, s.memberMux)
	}
	return s, nil
}

func configureProviders(cfg *Config) error {
	if cfg.Providers == nil {
		cfg.Providers = make(map[string]string)
		for name, baseURL := range cfg.Peers {
			if !strings.HasPrefix(name, "provider") {
				continue
			}
			suffix := strings.TrimPrefix(name, "provider")
			index := 0
			if suffix != "" {
				parsed, err := strconv.Atoi(suffix)
				if err != nil || parsed < 0 || strconv.Itoa(parsed) != suffix {
					return fmt.Errorf("monitor: cannot map provider peer %q to a dev identity", name)
				}
				index = parsed
			}
			id := keys.DIDOf(devnet.ProviderKey(index))
			if _, exists := cfg.Providers[id]; exists {
				return fmt.Errorf("monitor: provider peers map to duplicate dev identity %q", id)
			}
			cfg.Providers[id] = baseURL
		}
	}
	providers := make(map[string]string, len(cfg.Providers))
	urls := make(map[string]string, len(cfg.Providers))
	for id, baseURL := range cfg.Providers {
		if _, err := keys.ParseDID(id); err != nil {
			return fmt.Errorf("monitor: invalid provider identity %q: %w", id, err)
		}
		if err := validProviderURL(baseURL); err != nil {
			return fmt.Errorf("monitor: invalid provider URL for %q: %w", id, err)
		}
		if previous, exists := urls[baseURL]; exists {
			return fmt.Errorf("monitor: providers %q and %q share URL %q", previous, id, baseURL)
		}
		urls[baseURL] = id
		providers[id] = baseURL
	}
	cfg.Providers = providers
	return nil
}

func validProviderURL(baseURL string) error {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Opaque != "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return errors.New("URL must be absolute http or https with a host and no userinfo")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || strings.Contains(baseURL, "?") {
		return errors.New("URL must not contain a query")
	}
	if parsed.Fragment != "" || strings.Contains(baseURL, "#") {
		return errors.New("URL must not contain a fragment")
	}
	return nil
}

func storePath(root, child string) string { return filepath.Join(root, child) }

// Handler returns the root health handler and the per-member route dispatcher.
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.HandleFunc("/monitors/", s.dispatchMember)
	return mux
}

func (s *Service) dispatchMember(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/monitors/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) != 2 || parts[0] == "" {
		apierr.Write(w, apierr.PublicReadError{Status: apierr.NotFound})
		return
	}
	n, err := strconv.Atoi(parts[0])
	if err != nil || strconv.Itoa(n) != parts[0] {
		apierr.Write(w, apierr.PublicReadError{Status: apierr.NotFound})
		return
	}
	member, ok := s.byIndex[n]
	if !ok {
		apierr.Write(w, apierr.PublicReadError{Status: apierr.NotFound})
		return
	}
	clone := r.Clone(context.WithValue(r.Context(), memberContextKey{}, member))
	clone.URL.Path = "/" + parts[1]
	clone.URL.RawPath = ""
	s.memberMux.ServeHTTP(w, clone)
}

type memberContextKey struct{}

// MemberFrom returns the committee member attached to a routed request.
func MemberFrom(r *http.Request) *Member {
	member, _ := r.Context().Value(memberContextKey{}).(*Member)
	return member
}

// Members returns the hosted committee members in route-index order.
func (s *Service) Members() []*Member { return append([]*Member(nil), s.members...) }

// DB returns the process-wide journal for state not owned by one member.
func (s *Service) DB() *store.DB { return s.shared }

// View returns the verified trust and committee view.
func (s *Service) View() *View { return s.view }

// Now returns the service time in UTC and whole seconds, as every protocol time is.
func (s *Service) Now() time.Time { return s.config.Now().UTC().Truncate(time.Second) }

// Peers returns a copy of configured peer base URLs.
func (s *Service) Peers() map[string]string {
	peers := make(map[string]string, len(s.config.Peers))
	for name, url := range s.config.Peers {
		peers[name] = url
	}
	return peers
}

// Calendar returns the shared distribution calendar.
func (s *Service) Calendar() distribution.Calendar { return s.config.Calendar }

// VaultURL returns the Foundation base URL used for vault delivery.
func (s *Service) VaultURL() string { return s.config.Peers["foundation"] }

// ProviderList returns providers sorted by identity.
func (s *Service) ProviderList() []Provider {
	providers := make([]Provider, 0, len(s.config.Providers))
	for id, baseURL := range s.config.Providers {
		providers = append(providers, Provider{ID: id, URL: baseURL})
	}
	sort.Slice(providers, func(i, j int) bool { return providers[i].ID < providers[j].ID })
	return providers
}

// ProviderURL returns the configured URL for id.
func (s *Service) ProviderURL(id string) (string, bool) {
	baseURL, ok := s.config.Providers[id]
	return baseURL, ok
}

// Providers returns a sorted copy of provider base URLs configured for audit.
func (s *Service) Providers() []string {
	var providers []string
	for _, baseURL := range s.config.Providers {
		providers = append(providers, baseURL)
	}
	sort.Strings(providers)
	return providers
}

// HTTPClient returns the client used for outgoing service calls.
func (s *Service) HTTPClient() *http.Client { return s.httpClient }

// MemberURL returns the development base URL for member n.
func (s *Service) MemberURL(n int) string {
	return strings.TrimRight(s.config.Peers["monitor"], "/") + "/monitors/" + strconv.Itoa(n)
}

// Run executes scheduled work at LoopInterval until ctx ends.
func (s *Service) Run(ctx context.Context) error {
	ticker := time.NewTicker(s.config.LoopInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := s.RunOnce(ctx); err != nil {
				log.Printf("monitor: scheduled work failed: %v", err)
			}
		}
	}
}

// RunOnce runs each registered loop step in deterministic order and stops on error.
func (s *Service) RunOnce(ctx context.Context) error {
	steps := append([]loopStep(nil), loopSteps...)
	sort.SliceStable(steps, func(i, j int) bool {
		if steps[i].order == steps[j].order {
			return steps[i].name < steps[j].name
		}
		return steps[i].order < steps[j].order
	})
	for _, step := range steps {
		if err := step.run(s, ctx); err != nil {
			return fmt.Errorf("monitor: %s: %w", step.name, err)
		}
	}
	return nil
}

// Close flushes and closes every member, view and shared journal.
func (s *Service) Close() error {
	var errs []error
	for _, member := range s.members {
		if err := member.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if s.view != nil {
		if err := s.view.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	if s.shared != nil {
		if err := s.shared.Close(); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
