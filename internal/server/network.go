package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/sdk"
)

// TrustState reports whether the pinned Foundation publications are current.
type TrustState string

const (
	// TrustReady means the latest publications verified against the local pin.
	TrustReady TrustState = "ready"
	// TrustStale means a prior trust root is retained after refresh failure.
	TrustStale TrustState = "stale"
	// TrustUnavailable means no pinned trust root has been verified.
	TrustUnavailable TrustState = "unavailable"
)

// TrustStatus describes the current pinned trust-root refresh state.
type TrustStatus struct {
	State     TrustState
	FetchedAt time.Time
	Err       string
	Endpoint  string
}

// Selection is the profile's local choice of read and publish sources.
type Selection struct {
	Providers []ProviderChoice
	Mirrors   []Endpoint
	Vault     bool
}

// backgroundJobs are run once per client cycle in registration order.
var backgroundJobs []func(context.Context, *App) error

func init() { backgroundJobs = append(backgroundJobs, refreshTrustRoot) }

func (a *App) providerClient(endpoint Endpoint) (*sdk.Client, error) {
	return a.clientFor(endpoint)
}

func (a *App) clientFor(endpoint Endpoint) (*sdk.Client, error) {
	a.clientMu.Lock()
	defer a.clientMu.Unlock()
	key := endpoint.URL + "\x00" + endpoint.ServiceID
	if client := a.clients[key]; client != nil {
		return client, nil
	}
	client, err := sdk.New(endpoint.URL, sdk.Options{ServiceID: endpoint.ServiceID, AllowLoopbackHTTP: a.config.Dev, HTTPClient: a.config.HTTPClient, Now: a.config.Now})
	if err != nil {
		return nil, err
	}
	if a.clients == nil {
		a.clients = make(map[string]*sdk.Client)
	}
	a.clients[key] = client
	return client, nil
}

func (a *App) selection(profile string) Selection {
	settings, err := a.settings(profile)
	if err != nil {
		settings = ProfileSettings{}
	}
	providers := make([]ProviderChoice, 0, len(a.config.Providers)+len(settings.Providers))
	for _, endpoint := range a.config.Providers {
		if containsString(settings.RemovedDefaults, endpoint.URL) {
			continue
		}
		choice := ProviderChoice{Endpoint: endpoint.URL, OperatorKey: endpoint.ServiceID, Label: endpoint.Label, Source: "default", Trusted: true}
		for _, override := range settings.Providers {
			if override.Endpoint == endpoint.URL {
				choice.Trusted = override.Trusted
				choice.Label = override.Label
			}
		}
		providers = append(providers, choice)
	}
	for _, choice := range settings.Providers {
		if choice.Source == "manual" {
			providers = append(providers, choice)
		}
	}
	return Selection{Providers: netPrimaryFirst(providers, settings.PrimaryProvider), Mirrors: append([]Endpoint(nil), a.config.Mirrors...), Vault: a.config.Foundation.URL != ""}
}

func (a *App) publishingProvider(profile string) (ProviderChoice, bool) {
	for _, provider := range a.selection(profile).Providers {
		if provider.Trusted {
			return provider, true
		}
	}
	return ProviderChoice{}, false
}

func (a *App) verifier(profile string) *sdk.Verifier {
	selection := a.selection(profile)
	trusted := make(map[string]bool, len(selection.Providers))
	for _, provider := range selection.Providers {
		if provider.Trusted {
			trusted[provider.OperatorKey] = true
		}
	}
	root, _ := a.trustRoot()
	return sdk.NewVerifier(sdk.VerifierConfig{Root: root, Ledger: a.ledger(), Now: a.config.Now, Trusts: func(id string) bool { return trusted[id] }, ProviderSuccessions: a.providerSuccessions()})
}

func containsString(values []string, match string) bool {
	for _, value := range values {
		if value == match {
			return true
		}
	}
	return false
}

func (a *App) foundationClient() (*sdk.Client, error) {
	if a.config.Foundation.URL == "" {
		return nil, errors.New("Foundation endpoint is not configured")
	}
	return a.clientFor(a.config.Foundation)
}

func (a *App) monitorClients() []*sdk.Client { return a.clientsFor(a.config.Monitors) }
func (a *App) mirrorClients() []*sdk.Client  { return a.clientsFor(a.config.Mirrors) }

func (a *App) clientsFor(endpoints []Endpoint) []*sdk.Client {
	clients := make([]*sdk.Client, 0, len(endpoints))
	for _, endpoint := range endpoints {
		client, err := a.clientFor(endpoint)
		if err == nil {
			clients = append(clients, client)
		}
	}
	return clients
}

func (a *App) fauxClient() *faux.Client { return a.faux }

func (a *App) logger() *log.Logger { return log.Default() }

func (a *App) trustRoot() (*sdk.TrustRoot, TrustStatus) {
	a.trustMu.RLock()
	defer a.trustMu.RUnlock()
	return a.root, a.trustStatus
}

func (a *App) setProviderState(endpoint Endpoint, err error) {
	a.networkMu.Lock()
	defer a.networkMu.Unlock()
	if a.providerState == nil {
		a.providerState = make(map[string]string)
	}
	value := "reachable"
	var answered *sdk.Error
	if err != nil && !errors.As(err, &answered) {
		value = "unreachable: " + err.Error()
	}
	a.providerState[endpoint.URL] = value
}

// setFallback records whether the dev demo fallback is showing, and why.
func (a *App) setFallback(on bool, note string) {
	a.networkMu.Lock()
	a.fallback, a.sourceNote = on, note
	a.networkMu.Unlock()
}

func (a *App) fallbackActive() bool {
	a.networkMu.RLock()
	defer a.networkMu.RUnlock()
	return a.fallback
}

func (a *App) ledger() *sdk.Ledger { return a.ledgerValue }

func (a *App) now() time.Time { return a.config.Now() }

// RunOnce executes every registered background job once.
func (c *Client) RunOnce(ctx context.Context) error {
	var failures []error
	for _, job := range backgroundJobs {
		before, _ := c.app.ledgerValue.Snapshot()
		if err := job(ctx, c.app); err != nil {
			failures = append(failures, err)
		}
		after, snapErr := c.app.ledgerValue.Snapshot()
		if snapErr != nil {
			failures = append(failures, snapErr)
			continue
		}
		if !bytes.Equal(before, after) {
			if err := saveLedger(filepath.Join(c.app.config.DataDir, "ledger.json"), after); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}

// Run repeats background jobs until the context is cancelled.
func (c *Client) Run(ctx context.Context) error {
	interval := c.app.config.LoopInterval
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := c.RunOnce(ctx); err != nil {
			c.app.logger().Printf("client background job: %v", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func refreshTrustRoot(ctx context.Context, a *App) error {
	if a.config.Foundation.URL == "" {
		a.trustMu.Lock()
		a.trustStatus = TrustStatus{State: TrustUnavailable, Err: "No Foundation is configured"}
		a.trustMu.Unlock()
		return nil
	}
	client, err := a.foundationClient()
	if err != nil {
		return a.failTrustRefresh(err)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	genesis, genesisRaw, err := client.Genesis(ctx)
	if err != nil {
		return a.failTrustRefresh(err)
	}
	certificates, certificatesRaw, err := client.Certificates(ctx)
	if err != nil {
		return a.failTrustRefresh(err)
	}
	schedules, schedulesRaw, err := client.FeeSchedules(ctx)
	if err != nil {
		return a.failTrustRefresh(err)
	}
	distribution, distributionRaw, err := client.Distribution(ctx)
	if err != nil {
		return a.failTrustRefresh(err)
	}
	pubs := sdk.Publications{Genesis: genesis, GenesisRaw: genesisRaw, Certificates: certificates, CertificatesRaw: certificatesRaw, FeeSchedules: schedules, FeeSchedulesRaw: schedulesRaw, Distribution: distribution, DistributionRaw: distributionRaw, Endpoint: client.BaseURL(), FetchedAt: a.config.Now()}
	applied, err := a.netAppliedUpdates()
	if err != nil {
		return a.failTrustRefresh(err)
	}
	if err := sdk.CheckAppliedUpdates(applied, certificates.KeySetUpdates); err != nil {
		return a.failTrustRefresh(err)
	}
	root, err := sdk.BuildTrustRoot(a.config.Pin, pubs)
	if err != nil {
		return a.failTrustRefresh(err)
	}
	if now := root.AppliedUpdates(); len(now) > len(applied) {
		if err := a.netSaveAppliedUpdates(now); err != nil {
			return a.failTrustRefresh(err)
		}
	}
	a.trustMu.Lock()
	a.root = root
	a.trustStatus = TrustStatus{State: TrustReady, FetchedAt: pubs.FetchedAt, Endpoint: client.BaseURL()}
	a.trustMu.Unlock()
	return nil
}

// netAppliedPath is where the client keeps the release key-set updates it has applied.
func (a *App) netAppliedPath() string {
	return filepath.Join(a.config.DataDir, "keyset-updates.json")
}

// netAppliedUpdates reads the applied updates. A missing file means none; a file that cannot
// be read is an error, so a damaged file never lets an applied update be forgotten.
func (a *App) netAppliedUpdates() ([]sdk.AppliedUpdate, error) {
	data, err := os.ReadFile(a.netAppliedPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("applied key-set updates: %w", err)
	}
	var applied []sdk.AppliedUpdate
	if err := json.Unmarshal(data, &applied); err != nil {
		return nil, fmt.Errorf("applied key-set updates cannot be read: %w", err)
	}
	return applied, nil
}

func (a *App) netSaveAppliedUpdates(applied []sdk.AppliedUpdate) error {
	data, err := json.Marshal(applied)
	if err != nil {
		return err
	}
	return saveLedger(a.netAppliedPath(), data)
}

func (a *App) failTrustRefresh(err error) error {
	a.trustMu.Lock()
	state := TrustUnavailable
	fetchedAt := time.Time{}
	if a.root != nil {
		state = TrustStale
		fetchedAt = a.trustStatus.FetchedAt
	}
	a.trustStatus = TrustStatus{State: state, FetchedAt: fetchedAt, Err: err.Error(), Endpoint: a.config.Foundation.URL}
	a.trustMu.Unlock()
	return fmt.Errorf("refresh trust root: %w", err)
}

func saveLedger(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	temp, err := os.CreateTemp(dir, ".ledger-*")
	if err != nil {
		return err
	}
	name := temp.Name()
	defer os.Remove(name)
	if err := temp.Chmod(0600); err != nil {
		temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
