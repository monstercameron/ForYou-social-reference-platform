package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"maps"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"foryou.local/foryou/internal/transport"
)

// devnetOptions configures an in-process network of all six roles.
type devnetOptions struct {
	// Addrs maps a role to its loopback listen address; a missing role listens on 127.0.0.1:0.
	Addrs map[string]string
	// DataDir holds one data directory per role.
	DataDir string
	// Now supplies every service clock; nil means time.Now.
	Now func() time.Time
	// ClaimPeriod and LoopInterval are passed to every builder; zero keeps each service default.
	ClaimPeriod, LoopInterval time.Duration
	// Loops starts every role's Run loop once all six roles are ready.
	Loops bool
	// ReadyTimeout bounds the wait for each role's /healthz; zero means 10 seconds.
	ReadyTimeout time.Duration
}

// devnetRuntime is a running devnet: six listeners, their services and their loops.
type devnetRuntime struct {
	services    map[string]service
	listeners   map[string]*transport.Listener
	serveCancel map[string]context.CancelFunc
	serveDone   map[string]chan error
	loopCancel  context.CancelFunc
	runDone     map[string]chan error
	done        chan struct{}
	failureMu   sync.Mutex
	failure     error
	failOnce    sync.Once
	closeOnce   sync.Once
	closeErr    error
}

func init() { commands["devnet"] = runDevnet }

func runDevnet(ctx context.Context, getenv func(string) string) error {
	base := getenv("FORYOU_DATA_DIR")
	if base == "" {
		base = filepath.Join(".local", "data")
	}
	addrs := make(map[string]string, len(roleOrder))
	for _, role := range roleOrder {
		addr := getenv("FORYOU_" + strings.ToUpper(role) + "_ADDR")
		if addr == "" {
			addr = defaultAddr(role)
		}
		if err := validateDevnetAddr(role, addr); err != nil {
			return err
		}
		addrs[role] = addr
	}
	log.Print("devnet serves plain HTTP on loopback in dev mode; this is not for production")
	d, err := startDevnet(ctx, devnetOptions{Addrs: addrs, DataDir: base, ClaimPeriod: time.Minute, LoopInterval: 2 * time.Second, Loops: true})
	if err != nil {
		if ctx.Err() != nil && errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
	select {
	case <-ctx.Done():
	case <-d.Done():
	}
	return d.Close()
}

func startDevnet(ctx context.Context, opts devnetOptions) (*devnetRuntime, error) {
	if opts.DataDir == "" {
		opts.DataDir = filepath.Join(".local", "data")
	}
	if opts.ReadyTimeout == 0 {
		opts.ReadyTimeout = 10 * time.Second
	}
	addrs := make(map[string]string, len(roleOrder))
	for _, role := range roleOrder {
		addrs[role] = opts.Addrs[role]
		if addrs[role] == "" {
			addrs[role] = "127.0.0.1:0"
		}
		if err := validateDevnetAddr(role, addrs[role]); err != nil {
			return nil, err
		}
	}
	d := &devnetRuntime{
		services: make(map[string]service, len(roleOrder)), listeners: make(map[string]*transport.Listener, len(roleOrder)),
		serveCancel: make(map[string]context.CancelFunc, len(roleOrder)), serveDone: make(map[string]chan error, len(roleOrder)),
		runDone: make(map[string]chan error, len(roleOrder)), done: make(chan struct{}),
	}
	for _, role := range roleOrder {
		listener, err := transport.Listen(transport.Config{Addr: addrs[role], Dev: true})
		if err != nil {
			_ = d.Close()
			return nil, fmt.Errorf("listen %s: %w", role, err)
		}
		d.listeners[role] = listener
	}
	peers := make(map[string]string, len(roleOrder))
	for _, role := range roleOrder {
		peers[role] = d.listeners[role].URL()
	}
	for _, role := range roleOrder {
		dataDir := filepath.Join(opts.DataDir, role)
		if err := os.MkdirAll(dataDir, 0700); err != nil {
			_ = d.Close()
			return nil, fmt.Errorf("create %s data directory: %w", role, err)
		}
		build, ok := builders[role]
		if !ok {
			_ = d.Close()
			return nil, fmt.Errorf("build %s: no builder registered", role)
		}
		cfg := roleConfig{Role: role, Dev: true, Addr: addrs[role], URL: peers[role], Peers: maps.Clone(peers), DataDir: dataDir, Now: opts.Now, LoopInterval: opts.LoopInterval, ClaimPeriod: opts.ClaimPeriod}
		svc, err := build(cfg)
		if err != nil {
			_ = d.Close()
			return nil, fmt.Errorf("build %s: %w", role, err)
		}
		d.services[role] = svc
	}
	for _, role := range roleOrder {
		serveCtx, cancel := context.WithCancel(context.Background())
		d.serveCancel[role] = cancel
		d.serveDone[role] = make(chan error, 1)
		go func(role string) {
			err := d.listeners[role].Serve(serveCtx, d.services[role].Handler())
			d.serveDone[role] <- err
			if err != nil {
				d.fail(role, err)
			}
		}(role)
	}
	for _, role := range roleOrder {
		if err := waitForRole(ctx, role, d.listeners[role].URL(), opts.ReadyTimeout); err != nil {
			_ = d.Close()
			return nil, fmt.Errorf("%s readiness: %w", role, err)
		}
		log.Printf("%s ready at %s", role, d.listeners[role].URL())
	}
	if opts.Loops {
		loopCtx, cancel := context.WithCancel(context.Background())
		d.loopCancel = cancel
		for _, role := range roleOrder {
			d.runDone[role] = make(chan error, 1)
			go func(role string) {
				err := d.services[role].Run(loopCtx)
				d.runDone[role] <- err
				if err != nil && !errors.Is(err, context.Canceled) {
					d.fail(role, err)
				}
			}(role)
		}
	}
	return d, nil
}

func validateDevnetAddr(role, addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil || !transport.IsLoopbackHost(host) {
		return fmt.Errorf("FORYOU_%s_ADDR must use a loopback host: %q", strings.ToUpper(role), addr)
	}
	return nil
}

func waitForRole(ctx context.Context, role, baseURL string, timeout time.Duration) error {
	readyCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	client := &http.Client{Timeout: 500 * time.Millisecond}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, err := http.NewRequestWithContext(readyCtx, http.MethodGet, baseURL+"/healthz", nil)
		if err == nil {
			response, requestErr := client.Do(request)
			if requestErr == nil {
				_ = response.Body.Close()
				if response.StatusCode == http.StatusOK {
					return nil
				}
			}
		}
		select {
		case <-readyCtx.Done():
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return fmt.Errorf("%s did not answer health checks within %s", role, timeout)
		case <-ticker.C:
		}
	}
}

// URL returns the bound base URL for role, or an empty string for an unknown role.
func (d *devnetRuntime) URL(role string) string {
	if listener := d.listeners[role]; listener != nil {
		return listener.URL()
	}
	return ""
}

// Service returns the service for role, or nil for an unknown role.
func (d *devnetRuntime) Service(role string) service { return d.services[role] }

// Step runs one deterministic service pass. Foundation work runs only in its background loop.
func (d *devnetRuntime) Step(ctx context.Context) error {
	for _, role := range []string{"provider", "monitor", "provider", "replica"} {
		if err := d.services[role].RunOnce(ctx); err != nil {
			return fmt.Errorf("%s: %w", role, err)
		}
	}
	return nil
}

// Done closes when a role's serving or background work fails; Close then returns that failure.
func (d *devnetRuntime) Done() <-chan struct{} { return d.done }

// Close stops serving in reverse role order, stops every loop, then closes services and listeners in reverse order.
// It returns the first role failure joined with any close errors, and the same result on repeated calls.
func (d *devnetRuntime) Close() error {
	d.closeOnce.Do(func() {
		for i := len(roleOrder) - 1; i >= 0; i-- {
			role := roleOrder[i]
			if cancel := d.serveCancel[role]; cancel != nil {
				cancel()
				<-d.serveDone[role]
			}
		}
		if d.loopCancel != nil {
			d.loopCancel()
			for _, role := range roleOrder {
				<-d.runDone[role]
			}
		}
		errs := []error{d.failureError()}
		for i := len(roleOrder) - 1; i >= 0; i-- {
			role := roleOrder[i]
			if svc := d.services[role]; svc != nil {
				if err := svc.Close(); err != nil {
					errs = append(errs, fmt.Errorf("%s close: %w", role, err))
				}
			}
		}
		for i := len(roleOrder) - 1; i >= 0; i-- {
			role := roleOrder[i]
			if listener := d.listeners[role]; listener != nil {
				if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
					errs = append(errs, fmt.Errorf("%s listener: %w", role, err))
				}
			}
		}
		d.closeErr = errors.Join(errs...)
		log.Print("devnet shutdown complete")
	})
	return d.closeErr
}

func (d *devnetRuntime) fail(role string, err error) {
	d.failOnce.Do(func() {
		d.failureMu.Lock()
		d.failure = fmt.Errorf("%s: %w", role, err)
		d.failureMu.Unlock()
		close(d.done)
	})
}

func (d *devnetRuntime) failureError() error {
	d.failureMu.Lock()
	defer d.failureMu.Unlock()
	return d.failure
}
