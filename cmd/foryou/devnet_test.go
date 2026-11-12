package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"foryou.local/foryou/internal/faux"
)

type devnetRecorder struct {
	mu       sync.Mutex
	events   []string
	configs  map[string]roleConfig
	started  map[string]chan struct{}
	returned map[string]chan struct{}
	runErr   map[string]error
	health   map[string]int
}

func newDevnetRecorder() *devnetRecorder {
	return &devnetRecorder{configs: make(map[string]roleConfig), started: make(map[string]chan struct{}), returned: make(map[string]chan struct{}), runErr: make(map[string]error), health: make(map[string]int)}
}

func (r *devnetRecorder) add(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *devnetRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func installDevnetStubs(t *testing.T, recorder *devnetRecorder, failRole string, failErr error) {
	t.Helper()
	previous := make(map[string]builder, len(roleOrder))
	install := func(role string) {
		previous[role] = builders[role]
		builders[role] = func(config roleConfig) (service, error) {
			recorder.add("build:" + role)
			if role == failRole {
				return nil, failErr
			}
			recorder.configs[role] = config
			stub := &recordingDevnetService{role: role, recorder: recorder}
			if recorder.health[role] == 0 {
				recorder.health[role] = http.StatusOK
			}
			return stub, nil
		}
	}
	install("faux")
	install("foundation")
	install("monitor")
	install("provider")
	install("replica")
	install("client")
	t.Cleanup(func() {
		restore := func(role string) {
			if previous[role] == nil {
				delete(builders, role)
				return
			}
			builders[role] = previous[role]
		}
		restore("faux")
		restore("foundation")
		restore("monitor")
		restore("provider")
		restore("replica")
		restore("client")
	})
}

type recordingDevnetService struct {
	role     string
	recorder *devnetRecorder
}

func (s *recordingDevnetService) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(s.recorder.health[s.role])
	})
}

func (s *recordingDevnetService) Run(ctx context.Context) error {
	s.recorder.add("run-start:" + s.role)
	if started := s.recorder.started[s.role]; started != nil {
		close(started)
	}
	if err := s.recorder.runErr[s.role]; err != nil {
		s.recorder.add("run-return:" + s.role)
		if returned := s.recorder.returned[s.role]; returned != nil {
			close(returned)
		}
		return err
	}
	<-ctx.Done()
	s.recorder.add("run-return:" + s.role)
	if returned := s.recorder.returned[s.role]; returned != nil {
		close(returned)
	}
	return ctx.Err()
}

func (s *recordingDevnetService) RunOnce(context.Context) error {
	s.recorder.add("once:" + s.role)
	return nil
}

func (s *recordingDevnetService) Close() error {
	s.recorder.add("close:" + s.role)
	return nil
}

func TestStartDevnetBuildsAndClosesInRoleOrder(t *testing.T) {
	recorder := newDevnetRecorder()
	setRunChannels(recorder)
	installDevnetStubs(t, recorder, "", nil)
	d, err := startDevnet(context.Background(), devnetOptions{DataDir: t.TempDir(), Loops: true})
	if err != nil {
		t.Fatal(err)
	}
	awaitRun(t, recorder, "faux")
	awaitRun(t, recorder, "foundation")
	awaitRun(t, recorder, "monitor")
	awaitRun(t, recorder, "provider")
	awaitRun(t, recorder, "replica")
	awaitRun(t, recorder, "client")
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	assertBuildAndReverseClose(t, recorder.snapshot())
}

func TestStartDevnetUsesEphemeralPeerURLs(t *testing.T) {
	recorder := newDevnetRecorder()
	installDevnetStubs(t, recorder, "", nil)
	d, err := startDevnet(context.Background(), devnetOptions{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	peers := make(map[string]string, len(roleOrder))
	peers["faux"] = checkDevnetURL(t, d.URL("faux"))
	peers["foundation"] = checkDevnetURL(t, d.URL("foundation"))
	peers["monitor"] = checkDevnetURL(t, d.URL("monitor"))
	peers["provider"] = checkDevnetURL(t, d.URL("provider"))
	peers["replica"] = checkDevnetURL(t, d.URL("replica"))
	peers["client"] = checkDevnetURL(t, d.URL("client"))
	distinct := map[string]bool{peers["faux"]: true, peers["foundation"]: true, peers["monitor"]: true, peers["provider"]: true, peers["replica"]: true, peers["client"]: true}
	if len(distinct) != 6 {
		t.Fatalf("role URLs = %v; want six distinct values", peers)
	}
	assertPeers := func(role string) {
		if got := recorder.configs[role].Peers; !reflect.DeepEqual(got, peers) {
			t.Errorf("%s peers = %v; want %v", role, got, peers)
		}
	}
	assertPeers("faux")
	assertPeers("foundation")
	assertPeers("monitor")
	assertPeers("provider")
	assertPeers("replica")
	assertPeers("client")
}

func checkDevnetURL(t *testing.T, base string) string {
	t.Helper()
	parsed, err := url.Parse(base)
	if err != nil {
		t.Fatalf("URL(%q) parse: %v", base, err)
	}
	host, port, err := net.SplitHostPort(parsed.Host)
	if err != nil || parsed.Scheme != "http" || host != "127.0.0.1" || port == "0" {
		t.Errorf("URL = %q; want http://127.0.0.1:<nonzero port>", base)
	}
	return base
}

func TestStartDevnetBuildFailureClosesBuiltRolesInReverse(t *testing.T) {
	recorder := newDevnetRecorder()
	failure := errors.New("provider build failed")
	installDevnetStubs(t, recorder, "provider", failure)
	_, err := startDevnet(context.Background(), devnetOptions{DataDir: t.TempDir()})
	if !errors.Is(err, failure) || !strings.Contains(fmt.Sprint(err), "provider") {
		t.Fatalf("startDevnet error = %v; want provider build error", err)
	}
	got := recorder.snapshot()
	if !reflect.DeepEqual(got, []string{"build:faux", "build:foundation", "build:monitor", "build:provider", "close:monitor", "close:foundation", "close:faux"}) {
		t.Errorf("events = %v", got)
	}
}

func TestStartDevnetReadinessFailureClosesBuiltRoles(t *testing.T) {
	recorder := newDevnetRecorder()
	recorder.health["monitor"] = http.StatusServiceUnavailable
	installDevnetStubs(t, recorder, "", nil)
	_, err := startDevnet(context.Background(), devnetOptions{DataDir: t.TempDir(), ReadyTimeout: 60 * time.Millisecond, Loops: true})
	if err == nil || !strings.Contains(err.Error(), "monitor") {
		t.Fatalf("startDevnet error = %v; want monitor readiness error", err)
	}
	got := recorder.snapshot()
	if strings.Contains(strings.Join(got, ","), "run-start:") {
		t.Errorf("Run started before readiness completed: %v", got)
	}
	want := []string{"build:faux", "build:foundation", "build:monitor", "build:provider", "build:replica", "build:client", "close:client", "close:replica", "close:provider", "close:monitor", "close:foundation", "close:faux"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("events = %v; want %v", got, want)
	}
}

func TestRunDevnetRejectsNonLoopbackBeforeBuild(t *testing.T) {
	recorder := newDevnetRecorder()
	installDevnetStubs(t, recorder, "", nil)
	err := runDevnet(context.Background(), mapEnv(map[string]string{"FORYOU_PROVIDER_ADDR": "0.0.0.0:0"}))
	if err == nil || !strings.Contains(err.Error(), "FORYOU_PROVIDER_ADDR") {
		t.Fatalf("runDevnet error = %v; want FORYOU_PROVIDER_ADDR", err)
	}
	if got := recorder.snapshot(); len(got) != 0 {
		t.Errorf("builder events = %v; want none", got)
	}
}

func TestDevnetStepOrder(t *testing.T) {
	recorder := newDevnetRecorder()
	installDevnetStubs(t, recorder, "", nil)
	d, err := startDevnet(context.Background(), devnetOptions{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := recorder.snapshot()
	if !reflect.DeepEqual(got, []string{"build:faux", "build:foundation", "build:monitor", "build:provider", "build:replica", "build:client", "once:provider", "once:monitor", "once:provider", "once:replica"}) {
		t.Errorf("events = %v", got)
	}
}

func TestRunDevnetStopsOnRoleFailure(t *testing.T) {
	recorder := newDevnetRecorder()
	failure := errors.New("run failed")
	setRunChannels(recorder)
	recorder.runErr["monitor"] = failure
	installDevnetStubs(t, recorder, "", nil)
	result := make(chan error, 1)
	go func() {
		result <- runDevnet(context.Background(), devnetTestEnv(t.TempDir()))
	}()
	select {
	case err := <-result:
		if !errors.Is(err, failure) || !strings.Contains(fmt.Sprint(err), "monitor") {
			t.Fatalf("runDevnet error = %v; want monitor run failure", err)
		}
	case <-time.After(time.Minute):
		t.Fatal("runDevnet did not return after role failure")
	}
	closed := strings.Join(recorder.snapshot(), ",")
	if !strings.Contains(closed, "close:faux") || !strings.Contains(closed, "close:foundation") || !strings.Contains(closed, "close:monitor") || !strings.Contains(closed, "close:provider") || !strings.Contains(closed, "close:replica") || !strings.Contains(closed, "close:client") {
		t.Errorf("not all roles were closed: %s", closed)
	}
	assertReturned(t, recorder, "faux")
	assertReturned(t, recorder, "foundation")
	assertReturned(t, recorder, "monitor")
	assertReturned(t, recorder, "provider")
	assertReturned(t, recorder, "replica")
	assertReturned(t, recorder, "client")
}

func TestRunDevnetGracefulShutdown(t *testing.T) {
	recorder := newDevnetRecorder()
	setRunChannels(recorder)
	installDevnetStubs(t, recorder, "", nil)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- runDevnet(ctx, devnetTestEnv(t.TempDir())) }()
	waitForEvents(t, recorder, "run-start:client")
	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Errorf("runDevnet after cancellation = %v; want nil", err)
		}
	case <-time.After(time.Minute):
		t.Fatal("runDevnet did not stop after cancellation")
	}
	assertReturned(t, recorder, "faux")
	assertReturned(t, recorder, "foundation")
	assertReturned(t, recorder, "monitor")
	assertReturned(t, recorder, "provider")
	assertReturned(t, recorder, "replica")
	assertReturned(t, recorder, "client")
}

func TestDevnetRestartsWithJournaledFauxTransfer(t *testing.T) {
	dir := t.TempDir()
	first, err := startDevnet(context.Background(), devnetOptions{DataDir: dir, Loops: true, LoopInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	client := faux.NewClient(first.URL("faux"), nil)
	transfer, err := client.Submit(context.Background(), faux.SubmitRequest{Network: faux.BitcoinMainnet, Recipient: faux.DevAddress(faux.BitcoinMainnet, "restart-recipient"), AmountChainUnits: "12345", ClientRef: "restart-test"})
	if err != nil {
		_ = first.Close()
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := startDevnet(context.Background(), devnetOptions{DataDir: dir, Loops: true, LoopInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	got, err := faux.NewClient(second.URL("faux"), nil).Transfer(context.Background(), transfer.Network, transfer.TxID)
	closeErr := second.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got.TxID != transfer.TxID || got.AmountChainUnits != transfer.AmountChainUnits || got.Recipient != transfer.Recipient {
		t.Errorf("reopened transfer = %+v; want %+v", got, transfer)
	}
	if closeErr != nil {
		t.Errorf("second Close = %v; want nil", closeErr)
	}
}

func devnetTestEnv(dataDir string) func(string) string {
	values := map[string]string{"FORYOU_DATA_DIR": filepath.Clean(dataDir)}
	values["FORYOU_FAUX_ADDR"] = "127.0.0.1:0"
	values["FORYOU_FOUNDATION_ADDR"] = "127.0.0.1:0"
	values["FORYOU_MONITOR_ADDR"] = "127.0.0.1:0"
	values["FORYOU_PROVIDER_ADDR"] = "127.0.0.1:0"
	values["FORYOU_REPLICA_ADDR"] = "127.0.0.1:0"
	values["FORYOU_CLIENT_ADDR"] = "127.0.0.1:0"
	return mapEnv(values)
}

func waitForEvents(t *testing.T, recorder *devnetRecorder, event string) {
	t.Helper()
	select {
	case <-recorder.started[strings.TrimPrefix(event, "run-start:")]:
	case <-time.After(time.Minute):
		t.Fatalf("did not observe %q", event)
	}
}

func setRunChannels(recorder *devnetRecorder) {
	setReturnedChannels(recorder)
	recorder.started["faux"] = make(chan struct{})
	recorder.started["foundation"] = make(chan struct{})
	recorder.started["monitor"] = make(chan struct{})
	recorder.started["provider"] = make(chan struct{})
	recorder.started["replica"] = make(chan struct{})
	recorder.started["client"] = make(chan struct{})
}

func setReturnedChannels(recorder *devnetRecorder) {
	recorder.returned["faux"] = make(chan struct{})
	recorder.returned["foundation"] = make(chan struct{})
	recorder.returned["monitor"] = make(chan struct{})
	recorder.returned["provider"] = make(chan struct{})
	recorder.returned["replica"] = make(chan struct{})
	recorder.returned["client"] = make(chan struct{})
}

func awaitRun(t *testing.T, recorder *devnetRecorder, role string) {
	t.Helper()
	select {
	case <-recorder.started[role]:
	case <-time.After(time.Minute):
		t.Fatalf("%s Run did not start", role)
	}
}

func assertReturned(t *testing.T, recorder *devnetRecorder, role string) {
	t.Helper()
	select {
	case <-recorder.returned[role]:
	default:
		t.Errorf("%s Run has not returned", role)
	}
}

func assertBuildAndReverseClose(t *testing.T, got []string) {
	t.Helper()
	if len(got) < 6 {
		t.Fatalf("events = %v; want all six builds", got)
	}
	if !reflect.DeepEqual(got[:6], []string{"build:faux", "build:foundation", "build:monitor", "build:provider", "build:replica", "build:client"}) {
		t.Errorf("build events = %v", got[:6])
	}
	joined := strings.Join(got, ",")
	lastReturn := strings.LastIndex(joined, "run-return:")
	firstClose := strings.Index(joined, "close:")
	if lastReturn < 0 || firstClose < lastReturn {
		t.Errorf("service close began before every Run returned: %v", got)
	}
	if !strings.HasSuffix(joined, "close:client,close:replica,close:provider,close:monitor,close:foundation,close:faux") {
		t.Errorf("roles did not close in reverse order: %v", got)
	}
}
