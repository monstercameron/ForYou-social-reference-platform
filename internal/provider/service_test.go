package provider

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/delegation"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/distribution"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/governance"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/retention"
	"foryou.local/foryou/internal/succession"
	"foryou.local/foryou/internal/wire"
)

func TestServiceRouteGroupsAndLoops(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	cfg, err := TestConfig(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	old := routeGroups
	t.Cleanup(func() { routeGroups = old })
	called := false
	routeGroups = append(routeGroups, func(s *Service, mux *http.ServeMux) {
		s.SetControls(testControls{readyErr: errors.New("control state uncertain"), restriction: LegalRestricted})
		mux.HandleFunc("GET /test", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
		s.Every("test", time.Second, func(context.Context, time.Time) error { called = true; return nil })
	})
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got := httptest.NewRecorder()
	s.Handler().ServeHTTP(got, httptest.NewRequest("GET", "/test", nil))
	if got.Code != http.StatusNoContent {
		t.Fatalf("/test status = %d", got.Code)
	}
	response := httptest.NewRecorder()
	s.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/healthz", nil))
	if response.Code != 200 || response.Body.String() != `{"role":"provider","status":"ok"}` {
		t.Fatalf("health = %d %s", response.Code, response.Body.String())
	}
	response = httptest.NewRecorder()
	s.Handler().ServeHTTP(response, httptest.NewRequest("GET", "/readyz", nil))
	if response.Code != 503 || response.Body.String() != `{"retry_after_seconds":5,"status":"unavailable"}` {
		t.Fatalf("unready = %d %s", response.Code, response.Body.String())
	}
	message := loadExample(t, 0)
	id := "fy1:4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f"
	if _, err := s.Records().Accept(Acceptance{MessageID: id, Bytes: message, IntentDigest: sha256.Sum256([]byte("x")), IssuedAt: now}); err != nil {
		t.Fatal(err)
	}
	visible, _, err := s.Visibility().Visible(id)
	if err != nil || visible {
		t.Fatalf("restricted visibility = %t, %v", visible, err)
	}
	if err := s.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("registered loop did not run")
	}
}

type testControls struct {
	readyErr    error
	restriction Restriction
}

func (c testControls) Restriction(string) (Restriction, error) { return c.restriction, nil }
func (c testControls) Ready() error                            { return c.readyErr }

func TestServiceClockAndReadiness(t *testing.T) {
	current := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	cfg, err := TestConfig(func() time.Time { return current })
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.RunOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	current = current.Add(-60 * time.Second)
	if err = s.CheckClock(); err != nil {
		t.Fatalf("60 seconds behind: %v", err)
	}
	current = current.Add(-time.Second)
	if err = s.CheckClock(); err != ErrClockUncertain {
		t.Fatalf("61 seconds behind: %v", err)
	}
}

func TestServiceSharedComponentsAndDeclaration(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	cfg, err := TestConfig(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if s.Chain == nil || s.Ledger == nil || s.ServiceAuth == nil || s.Authority == nil || s.Successions == nil || len(s.FeeSchedules()) == 0 {
		t.Fatal("shared components were not initialized")
	}
	ds := s.Declarations()
	if len(ds) != 1 || ds[0].RetentionSeconds != 31536000 {
		t.Fatalf("initial retention declaration = %#v", ds)
	}
	if err := retention.Verify(ds[0]); err != nil {
		t.Fatalf("initial declaration verify: %v", err)
	}
	if err := s.AddDeclaration(ds[0]); err != nil {
		t.Fatalf("idempotent declaration: %v", err)
	}
	past, err := retention.Declare(s.Key(), 31536000, wire.PayoutRecipients{}, now.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AddDeclaration(past); err == nil {
		t.Fatal("past declaration accepted")
	}
}

func TestServiceSuccessionPersists(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	dir := filepath.Join(t.TempDir(), "data", "provider")
	cfg, err := TestConfig(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = dir
	cfg.ContentStore = nil
	cfg.ControlStore = nil
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	item, err := succession.New(devnet.ProviderKey(0), devnet.ProviderKey(1), now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Successions.Add(item); err != nil {
		t.Fatal(err)
	}
	if err = s.PersistSuccessions(); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	cfg, err = TestConfig(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = dir
	cfg.ContentStore = nil
	cfg.ControlStore = nil
	s, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if got := s.Successions.ForOld(keys.DIDOf(devnet.ProviderKey(0))); len(got) != 1 || got[0].SuccessionID != item.SuccessionID {
		t.Fatalf("restored successions = %#v", got)
	}
}

func TestServiceRevocationPersists(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	dir := filepath.Join(t.TempDir(), "provider")
	cfg, err := TestConfig(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = dir
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	root, err := keys.NewKeyFromSeed(bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	signer, err := keys.NewKeyFromSeed(bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatal(err)
	}
	rootID := keys.DIDOf(root)
	enroll, err := delegation.SignEnroll(root, wire.AuthorizationEpochEnroll{ProviderID: s.ProviderID(), RequestNonce: keys.EncodeB64URL(bytes.Repeat([]byte{7}, 32)), NotAfter: wire.FormatTime(now.Add(time.Hour))})
	if err != nil {
		t.Fatal(err)
	}
	state, _, err := s.Authority.Enroll(rootID, enroll)
	if err != nil {
		t.Fatal(err)
	}
	d := wire.Delegation{AuthorID: rootID, SignerID: keys.DIDOf(signer), ProviderID: s.ProviderID(), Scope: []string{"post"}, Epoch: *state.Epoch, ValidFrom: wire.FormatTime(now), ValidUntil: wire.FormatTime(now.Add(time.Hour))}
	d, err = delegation.SignDelegation(root, d)
	if err != nil {
		t.Fatal(err)
	}
	req, err := delegation.SignRevocation(root, wire.RevocationRequest{ProviderID: s.ProviderID(), DelegationID: d.DelegationID, Delegation: d})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.Authority.Revoke(rootID, req); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	cfg, err = TestConfig(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	cfg.DataDir = dir
	s, err = New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	err = s.Authority.Admit(d, delegation.Use{AuthorID: rootID, SignerID: d.SignerID, MessageType: "post"}, func(time.Time) error { return nil })
	if !errors.Is(err, delegation.ErrRevoked) {
		t.Fatalf("Admit after restart = %v", err)
	}
}

func TestServiceAddSchedule(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	cfg, err := TestConfig(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	approved := cfg.FeeSchedules[0]
	approved.Schedule.Revision++
	approved.Schedule.EffectiveAt = "2027-07-01T00:00:00Z"
	approved.Schedule.ExpiresAt = "2028-01-01T00:00:00Z"
	approved.Schedule.ScheduleID, err = fee.ScheduleID(approved.Schedule)
	if err != nil {
		t.Fatal(err)
	}
	approved.Approval = wire.FeeScheduleApproval{ScheduleID: approved.Schedule.ScheduleID, ApprovedAt: "2027-04-01T00:00:00Z"}
	unsigned, err := canon.Parse(mustEncodeWithout(t, approved.Approval, "release_signatures"), canon.Limits{MaxDepth: 32})
	if err != nil {
		t.Fatal(err)
	}
	approved.Approval.ReleaseSignatures, err = governance.SignRelease("fee-schedule-approval", unsigned, devnet.ReleaseKeys())
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AddSchedule(approved); err != nil {
		t.Fatalf("approved successor: %v", err)
	}
	if len(s.FeeSchedules()) != len(cfg.FeeSchedules)+1 {
		t.Fatalf("schedule count = %d", len(s.FeeSchedules()))
	}
	if got, err := fee.InForce(s.FeeSchedules(), approved.Schedule.Network, mustTime(t, approved.Schedule.EffectiveAt)); err != nil || got.ScheduleID != approved.Schedule.ScheduleID {
		t.Fatal("adopted schedule is not in force at activation")
	}
	unapproved := approved
	unapproved.Approval.ReleaseSignatures, err = governance.SignRelease("fee-schedule-approval", unsigned, devnet.ReleaseKeys()[:2])
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AddSchedule(unapproved); !errors.Is(err, fee.ErrApproval) {
		t.Fatalf("insufficient schedule approval = %v", err)
	}
}

func TestServiceAddPolicy(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	cfg, err := TestConfig(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	p := cfg.Policies[0]
	p.ApprovedAt = "2026-11-11T00:00:00Z"
	p.EffectiveAt = "2026-11-25T00:00:00Z"
	p.PolicyID, err = distribution.PolicyID(p)
	if err != nil {
		t.Fatal(err)
	}
	unsigned, err := canon.Parse(mustEncodeWithout(t, p, "release_signatures"), canon.Limits{MaxDepth: 32})
	if err != nil {
		t.Fatal(err)
	}
	p.ReleaseSignatures, err = governance.SignRelease("distribution-policy", unsigned, devnet.ReleaseKeys())
	if err != nil {
		t.Fatal(err)
	}
	unapproved := p
	unapproved.ReleaseSignatures, err = governance.SignRelease("distribution-policy", unsigned, devnet.ReleaseKeys())
	if err != nil {
		t.Fatal(err)
	}
	unapproved.ReleaseSignatures[2].Signature = keys.EncodeB64URL(make([]byte, 64))
	if err = s.AddPolicy(unapproved); !errors.Is(err, distribution.ErrReleaseApproval) {
		t.Fatalf("insufficient policy approval = %v", err)
	}
	if err = s.AddPolicy(p); err != nil {
		t.Fatalf("approved successor policy: %v", err)
	}
	if len(s.Policies()) != 2 {
		t.Fatalf("policy count = %d", len(s.Policies()))
	}
}

func mustEncode(t *testing.T, v any) []byte {
	t.Helper()
	b, err := canon.Encode(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func mustEncodeWithout(t *testing.T, v any, omit string) []byte {
	t.Helper()
	b, err := canon.EncodeWithout(v, omit)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestProviderRespondHelpers(t *testing.T) {
	w := httptest.NewRecorder()
	writeError(w, http.StatusNotFound, "not_found")
	if w.Body.String() != `{"status":"not_found"}` || w.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("error response = %q %v", w.Body.String(), w.Header())
	}
	w = httptest.NewRecorder()
	writeRetry(w, http.StatusTooManyRequests, "rate_limited", 30)
	if w.Body.String() != `{"retry_after_seconds":30,"status":"rate_limited"}` || w.Header().Get("Retry-After") != "30" {
		t.Fatalf("retry response = %q %v", w.Body.String(), w.Header())
	}
	r := httptest.NewRequest("POST", "/", strings.NewReader(`{"a":1}`))
	var body struct {
		A int `json:"a"`
	}
	got, err := decodeJSON(r, 20, &body)
	if err != nil || string(got) != `{"a":1}` || body.A != 1 {
		t.Fatalf("decode = %q %#v %v", got, body, err)
	}
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := wire.ParseTime(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
