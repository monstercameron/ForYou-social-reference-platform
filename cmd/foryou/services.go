package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/foundation"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/monitor"
	"foryou.local/foryou/internal/provider"
	"foryou.local/foryou/internal/replica"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/server"
)

var errDevOnly = errors.New("only dev mode is supported")
var serviceHTTPClient = &http.Client{Timeout: 30 * time.Second}

func init() {
	registerRole("faux", buildFaux)
	registerRole("foundation", buildFoundation)
	registerRole("monitor", buildMonitor)
	registerRole("provider", buildProvider)
	registerRole("replica", buildReplica)
	registerRole("client", buildClient)
}

func requireDev(config roleConfig) error {
	if !config.Dev {
		return errDevOnly
	}
	return nil
}

func buildFaux(config roleConfig) (service, error) {
	if err := requireDev(config); err != nil {
		return nil, err
	}
	ledger, err := faux.Open(faux.Config{Now: config.Now, JournalPath: filepath.Join(config.DataDir, "journal.jsonl")})
	if err != nil {
		return nil, err
	}
	return &fauxService{ledger: ledger, handler: healthHandler(faux.NewHandler(ledger))}, nil
}

type fauxService struct {
	ledger  *faux.Ledger
	handler http.Handler
}

func (s *fauxService) Handler() http.Handler         { return s.handler }
func (s *fauxService) Run(context.Context) error     { return nil }
func (s *fauxService) RunOnce(context.Context) error { return nil }
func (s *fauxService) Close() error                  { return nil }

func healthHandler(next http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.Handle("/", next)
	return mux
}

func buildFoundation(config roleConfig) (service, error) {
	if err := requireDev(config); err != nil {
		return nil, err
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	calendar, err := devnet.Calendar(0)
	if err != nil {
		return nil, err
	}
	monitors := make(map[string]string, 5)
	payouts := make(map[string]map[string]string, 5)
	monitorLabels := [...]string{devnet.Monitor0Label, devnet.Monitor1Label, devnet.Monitor2Label, devnet.Monitor3Label, devnet.Monitor4Label}
	for n, label := range monitorLabels {
		id := keys.DIDOf(devnet.MonitorKey(n))
		monitors[id] = monitorMemberURL(config.Peers["monitor"], n)
		payouts[id] = map[string]string{
			faux.BitcoinMainnet:  devnet.Recipient(label, faux.BitcoinMainnet),
			faux.EthereumMainnet: devnet.Recipient(label, faux.EthereumMainnet),
		}
	}
	core, err := foundation.New(foundation.Config{
		Dev: true, Key: ed25519.PrivateKey(devnet.FoundationKey()), Genesis: devnet.Genesis(),
		Certificates: devnet.Certificates, FeeSchedules: devnet.FeeSchedules(), Policies: devnet.Policies(),
		Calendar: calendar, DataDir: config.DataDir, Clock: now,
		Faux:      faux.NewClient(config.Peers["faux"], serviceHTTPClient),
		Providers: []string{config.Peers["provider"]}, Monitors: monitors, MonitorPayouts: payouts,
	})
	if err != nil {
		return nil, err
	}
	return &foundationService{Service: core}, nil
}

type foundationService struct {
	*foundation.Service
}

func (s *foundationService) RunOnce(context.Context) error {
	return errors.New("foundation: core has no single-cycle run")
}

func monitorMemberURL(base string, n int) string {
	return strings.TrimRight(base, "/") + "/monitors/" + fmt.Sprint(n)
}

func buildMonitor(config roleConfig) (service, error) {
	if err := requireDev(config); err != nil {
		return nil, err
	}
	peers := map[string]string{
		"provider": config.Peers["provider"], "foundation": config.Peers["foundation"],
		"monitor": config.Peers["monitor"], "faux": config.Peers["faux"],
	}
	calendar, err := devnet.Calendar(0)
	if err != nil {
		return nil, err
	}
	core, err := monitor.New(monitor.Config{
		Genesis: devnet.Genesis(), Peers: peers, DataDir: config.DataDir,
		Now: config.Now, LoopInterval: config.LoopInterval, Calendar: calendar,
		Faux: faux.NewClient(config.Peers["faux"], serviceHTTPClient), HTTPClient: serviceHTTPClient,
	})
	if err != nil {
		return nil, err
	}
	return core, nil
}

// Every local role reaches the provider from the same loopback address, so one
// source is allowed the whole capacity. The limits themselves stay in force.
var sharedSourceReads = provider.ReadLimits{DirectPerMinute: 6000, DirectPerSourcePerMinute: 6000, ListingPerMinute: 600, ListingPerSourcePerMinute: 600}

func buildProvider(config roleConfig) (service, error) {
	if err := requireDev(config); err != nil {
		return nil, err
	}
	if config.ClaimPeriod > 24*time.Hour {
		return nil, fmt.Errorf("provider: claim period cannot exceed 24 hours")
	}
	coreConfig, err := provider.DevConfig(config.DataDir)
	if err != nil {
		return nil, err
	}
	coreConfig.DataDir = config.DataDir
	coreConfig.Faux = faux.NewClient(config.Peers["faux"], serviceHTTPClient)
	coreConfig.Reads = sharedSourceReads
	coreConfig.PublicOrigin, err = httpsOrigin(config.URL)
	if err != nil {
		return nil, err
	}
	if config.Now != nil {
		coreConfig.Now = config.Now
		coreConfig.Trust, err = devnet.Trust(config.Now())
		if err != nil {
			return nil, err
		}
	}
	if config.ClaimPeriod != 0 {
		coreConfig.ClaimPeriod = config.ClaimPeriod
	}
	core, err := provider.New(coreConfig)
	if err != nil {
		return nil, err
	}
	if foundation := config.Peers["foundation"]; foundation != "" {
		vault, err := sdk.New(foundation, sdk.Options{ServiceID: keys.DIDOf(devnet.FoundationKey()), AllowLoopbackHTTP: true, HTTPClient: serviceHTTPClient, Now: coreConfig.Now})
		if err != nil {
			_ = core.Close()
			return nil, fmt.Errorf("provider: Foundation vault: %w", err)
		}
		core.SetVault(vault)
	}
	return core, nil
}

func buildReplica(config roleConfig) (service, error) {
	if err := requireDev(config); err != nil {
		return nil, err
	}
	coreConfig, err := replica.DevConfig(config.DataDir)
	if err != nil {
		return nil, err
	}
	coreConfig.DataDir = config.DataDir
	coreConfig.Sources = []replica.Source{{Origin: config.Peers["provider"], ProviderID: keys.DIDOf(devnet.ProviderKey(0))}}
	coreConfig.PublicOrigin, err = httpsOrigin(config.URL)
	if err != nil {
		return nil, err
	}
	coreConfig.Now = config.Now
	if config.LoopInterval != 0 {
		coreConfig.PollInterval = config.LoopInterval
	}
	return replica.New(coreConfig)
}

func buildClient(config roleConfig) (service, error) {
	if err := requireDev(config); err != nil {
		return nil, err
	}
	return server.NewClient(server.Config{
		Dev: true, DataDir: config.DataDir, PublicOrigin: config.URL,
		Now: config.Now, LoopInterval: config.LoopInterval,
	})
}

func httpsOrigin(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" || parsed.User != nil {
		return "", fmt.Errorf("invalid role URL %q", raw)
	}
	return "https://" + parsed.Host, nil
}
