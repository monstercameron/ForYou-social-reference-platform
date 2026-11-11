package delegation

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

func TestAuthorityEnrollAndControl(t *testing.T) {
	root, err := keys.NewKeyFromSeed(bytes.Repeat([]byte{3}, 32))
	if err != nil {
		t.Fatal(err)
	}
	provider, err := keys.NewKeyFromSeed(bytes.Repeat([]byte{4}, 32))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	a, err := NewAuthority(AuthorityConfig{ProviderKey: provider, Store: NewMemoryEpochStore(), Now: func() time.Time { return now }, Random: bytes.NewReader(bytes.Repeat([]byte{5}, 128)), MaxRevocationsPerAuthor: 1})
	if err != nil {
		t.Fatal(err)
	}
	rootID := keys.DIDOf(root)
	providerID := keys.DIDOf(provider)
	req, err := SignEnroll(root, wire.AuthorizationEpochEnroll{ProviderID: providerID, RequestNonce: keys.EncodeB64URL(bytes.Repeat([]byte{7}, 32)), NotAfter: wire.FormatTime(now.Add(time.Hour))})
	if err != nil {
		t.Fatal(err)
	}
	state, created, err := a.Enroll(rootID, req)
	if err != nil || !created {
		t.Fatalf("Enroll = (%+v, %t, %v)", state, created, err)
	}
	t.Run("first enrollment creates epoch one", func(t *testing.T) {
		if state.StateRevision != 1 || state.Epoch == nil || state.Epoch.EpochSequence != 1 || state.Status != "active" {
			t.Fatalf("unexpected state: %+v", state)
		}
	})
	t.Run("exact enrollment retry returns existing state", func(t *testing.T) {
		got, created, err := a.Enroll(rootID, req)
		if err != nil || created || got.Epoch.EpochID != state.Epoch.EpochID {
			t.Fatalf("retry = (%+v, %t, %v)", got, created, err)
		}
	})
	freeze, err := SignControl(root, wire.AuthorizationEpochControl{ProviderID: providerID, ExpectedEpochID: state.Epoch.EpochID, ExpectedStateRevision: 1, Action: "freeze", Nonce: keys.EncodeB64URL(bytes.Repeat([]byte{8}, 32))})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := a.Control(rootID, freeze)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("freeze increments revision", func(t *testing.T) {
		if frozen.StateRevision != 2 || frozen.Status != "frozen" {
			t.Fatalf("unexpected frozen state: %+v", frozen)
		}
	})
	rotate, err := SignControl(root, wire.AuthorizationEpochControl{ProviderID: providerID, ExpectedEpochID: state.Epoch.EpochID, ExpectedStateRevision: 2, Action: "rotate", Nonce: keys.EncodeB64URL(bytes.Repeat([]byte{9}, 32))})
	if err != nil {
		t.Fatal(err)
	}
	active, err := a.Control(rootID, rotate)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("rotation advances the epoch", func(t *testing.T) {
		if active.StateRevision != 3 || active.Status != "active" || active.Epoch.EpochSequence != 2 {
			t.Fatalf("unexpected active state: %+v", active)
		}
	})
	t.Run("stale revision rejected", func(t *testing.T) {
		stale, err := SignControl(root, wire.AuthorizationEpochControl{ProviderID: providerID, ExpectedEpochID: state.Epoch.EpochID, ExpectedStateRevision: 1, Action: "freeze", Nonce: keys.EncodeB64URL(bytes.Repeat([]byte{10}, 32))})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = a.Control(rootID, stale); !errors.Is(err, ErrStale) {
			t.Fatalf("Control error = %v; want ErrStale", err)
		}
	})
}

func TestAuthorityErrorStatus(t *testing.T) {
	t.Run("stale state is conflict", func(t *testing.T) {
		status, name := ErrorStatus(ErrStale)
		if status != 409 || name != "stale_state" {
			t.Fatalf("ErrorStatus = (%d,%q)", status, name)
		}
	})
	t.Run("revoked admission is forbidden", func(t *testing.T) {
		status, name := ErrorStatus(ErrRevoked)
		if status != 403 || name != "delegation_revoked" {
			t.Fatalf("ErrorStatus = (%d,%q)", status, name)
		}
	})
}

func TestAuthorityRevocationGateAndRestore(t *testing.T) {
	root, err := keys.NewKeyFromSeed(bytes.Repeat([]byte{11}, 32))
	if err != nil {
		t.Fatal(err)
	}
	provider, err := keys.NewKeyFromSeed(bytes.Repeat([]byte{12}, 32))
	if err != nil {
		t.Fatal(err)
	}
	signer, err := keys.NewKeyFromSeed(bytes.Repeat([]byte{13}, 32))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	store := NewMemoryEpochStore()
	a, err := NewAuthority(AuthorityConfig{ProviderKey: provider, Store: store, Now: func() time.Time { return now }, Random: bytes.NewReader(bytes.Repeat([]byte{14}, 128)), MaxRevocationsPerAuthor: 1})
	if err != nil {
		t.Fatal(err)
	}
	rootID := keys.DIDOf(root)
	providerID := keys.DIDOf(provider)
	enroll, err := SignEnroll(root, wire.AuthorizationEpochEnroll{ProviderID: providerID, RequestNonce: keys.EncodeB64URL(bytes.Repeat([]byte{15}, 32)), NotAfter: wire.FormatTime(now.Add(time.Hour))})
	if err != nil {
		t.Fatal(err)
	}
	state, _, err := a.Enroll(rootID, enroll)
	if err != nil {
		t.Fatal(err)
	}
	d := wire.Delegation{AuthorID: rootID, SignerID: keys.DIDOf(signer), ProviderID: providerID, Scope: []string{"post"}, Epoch: *state.Epoch, ValidFrom: wire.FormatTime(now), ValidUntil: wire.FormatTime(now.Add(time.Hour))}
	d, err = SignDelegation(root, d)
	if err != nil {
		t.Fatal(err)
	}
	committed := false
	err = a.Admit(d, Use{AuthorID: rootID, SignerID: d.SignerID, MessageType: "post"}, func(at time.Time) error {
		committed = at.Equal(now)
		return nil
	})
	if err != nil || !committed {
		t.Fatalf("Admit before revocation = %v, committed %t", err, committed)
	}
	revoke, err := SignRevocation(root, wire.RevocationRequest{ProviderID: providerID, DelegationID: d.DelegationID, Delegation: d})
	if err != nil {
		t.Fatal(err)
	}
	if id, err := a.Revoke(rootID, revoke); err != nil || id != d.DelegationID {
		t.Fatalf("Revoke = (%q, %v)", id, err)
	}
	t.Run("revoked delegation cannot commit", func(t *testing.T) {
		called := false
		err := a.Admit(d, Use{AuthorID: rootID, SignerID: d.SignerID, MessageType: "post"}, func(time.Time) error { called = true; return nil })
		if !errors.Is(err, ErrRevoked) || called {
			t.Fatalf("Admit = %v, callback called %t", err, called)
		}
	})
	t.Run("revocation is idempotent", func(t *testing.T) {
		if id, err := a.Revoke(rootID, revoke); err != nil || id != d.DelegationID {
			t.Fatalf("Revoke retry = (%q, %v)", id, err)
		}
	})
	t.Run("capacity leaves control available", func(t *testing.T) {
		otherSigner, err := keys.NewKeyFromSeed(bytes.Repeat([]byte{16}, 32))
		if err != nil {
			t.Fatal(err)
		}
		other := d
		other.SignerID = keys.DIDOf(otherSigner)
		other, err = SignDelegation(root, other)
		if err != nil {
			t.Fatal(err)
		}
		request, err := SignRevocation(root, wire.RevocationRequest{ProviderID: providerID, DelegationID: other.DelegationID, Delegation: other})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = a.Revoke(rootID, request); !errors.Is(err, ErrCapacity) {
			t.Fatalf("Revoke beyond capacity = %v; want ErrCapacity", err)
		}
		control, err := SignControl(root, wire.AuthorizationEpochControl{ProviderID: providerID, ExpectedEpochID: state.Epoch.EpochID, ExpectedStateRevision: 1, Action: "freeze", Nonce: keys.EncodeB64URL(bytes.Repeat([]byte{17}, 32))})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = a.Control(rootID, control); err != nil {
			t.Fatalf("Control at exhausted capacity = %v", err)
		}
	})
}

func TestAuthorityRequiresPositiveRevocationCapacity(t *testing.T) {
	provider, err := keys.NewKeyFromSeed(bytes.Repeat([]byte{61}, 32))
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewAuthority(AuthorityConfig{ProviderKey: provider, Store: NewMemoryEpochStore()})
	if err == nil || errors.Is(err, ErrInvalidDelegation) {
		t.Fatalf("NewAuthority error = %v; want a distinct configuration error", err)
	}
}

func TestAuthorityPrunesExpiredRevocationCapacity(t *testing.T) {
	root, err := keys.NewKeyFromSeed(bytes.Repeat([]byte{62}, 32))
	if err != nil {
		t.Fatal(err)
	}
	provider, err := keys.NewKeyFromSeed(bytes.Repeat([]byte{63}, 32))
	if err != nil {
		t.Fatal(err)
	}
	signer, err := keys.NewKeyFromSeed(bytes.Repeat([]byte{64}, 32))
	if err != nil {
		t.Fatal(err)
	}
	clockNow := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	rootID, providerID := keys.DIDOf(root), keys.DIDOf(provider)
	a, err := NewAuthority(AuthorityConfig{ProviderKey: provider, Store: NewMemoryEpochStore(), Now: func() time.Time { return clockNow }, Random: bytes.NewReader(bytes.Repeat([]byte{65}, 256)), MaxRevocationsPerAuthor: 1})
	if err != nil {
		t.Fatal(err)
	}
	enroll, err := SignEnroll(root, wire.AuthorizationEpochEnroll{ProviderID: providerID, RequestNonce: keys.EncodeB64URL(bytes.Repeat([]byte{66}, 32)), NotAfter: wire.FormatTime(clockNow.Add(time.Hour))})
	if err != nil {
		t.Fatal(err)
	}
	state, _, err := a.Enroll(rootID, enroll)
	if err != nil {
		t.Fatal(err)
	}
	first := delegationFor(t, root, signer, rootID, providerID, *state.Epoch, clockNow)
	firstRequest, err := SignRevocation(root, wire.RevocationRequest{ProviderID: providerID, DelegationID: first.DelegationID, Delegation: first})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Revoke(rootID, firstRequest); err != nil {
		t.Fatal(err)
	}
	second := first
	second.ValidUntil = wire.FormatTime(clockNow.Add(3 * time.Hour))
	second, err = SignDelegation(root, second)
	if err != nil {
		t.Fatal(err)
	}
	secondRequest, err := SignRevocation(root, wire.RevocationRequest{ProviderID: providerID, DelegationID: second.DelegationID, Delegation: second})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.Revoke(rootID, secondRequest); !errors.Is(err, ErrCapacity) {
		t.Fatalf("Revoke before expiry = %v; want ErrCapacity", err)
	}
	clockNow = clockNow.Add(2 * time.Hour)
	if _, err = a.Revoke(rootID, secondRequest); err != nil {
		t.Fatalf("Revoke after expired entry = %v", err)
	}
}

func TestAuthorityEnrollRequestChecks(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	root, err := keys.NewKeyFromSeed(bytes.Repeat([]byte{51}, 32))
	if err != nil {
		t.Fatal(err)
	}
	provider, err := keys.NewKeyFromSeed(bytes.Repeat([]byte{52}, 32))
	if err != nil {
		t.Fatal(err)
	}
	providerID := keys.DIDOf(provider)
	rootID := keys.DIDOf(root)
	a, err := NewAuthority(AuthorityConfig{ProviderKey: provider, Store: NewMemoryEpochStore(), Now: func() time.Time { return now }, MaxRevocationsPerAuthor: 2})
	if err != nil {
		t.Fatal(err)
	}
	request, err := SignEnroll(root, wire.AuthorizationEpochEnroll{ProviderID: providerID, RequestNonce: keys.EncodeB64URL(bytes.Repeat([]byte{21}, 32)), NotAfter: wire.FormatTime(now.Add(time.Hour))})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("nonce with changed content conflicts", func(t *testing.T) {
		changed := request
		changed.NotAfter = wire.FormatTime(now.Add(2 * time.Hour))
		changed, err = SignEnroll(root, changed)
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = a.Enroll(rootID, request); err != nil {
			t.Fatal(err)
		}
		if _, _, err = a.Enroll(rootID, changed); !errors.Is(err, ErrRequestConflict) {
			t.Fatalf("Enroll error = %v; want ErrRequestConflict", err)
		}
	})
	t.Run("expired request rejected", func(t *testing.T) {
		past := wire.FormatTime(now.Add(-time.Second))
		expired, err := SignEnroll(root, wire.AuthorizationEpochEnroll{ProviderID: providerID, RequestNonce: keys.EncodeB64URL(bytes.Repeat([]byte{22}, 32)), NotAfter: past})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = a.Enroll(rootID, expired); !errors.Is(err, ErrRequestExpired) {
			t.Fatalf("Enroll error = %v; want ErrRequestExpired", err)
		}
	})
	t.Run("request author must match authenticated root", func(t *testing.T) {
		otherRoot, err := keys.NewKeyFromSeed(bytes.Repeat([]byte{23}, 32))
		if err != nil {
			t.Fatal(err)
		}
		wrong, err := SignEnroll(otherRoot, wire.AuthorizationEpochEnroll{ProviderID: providerID, RequestNonce: keys.EncodeB64URL(bytes.Repeat([]byte{24}, 32)), NotAfter: wire.FormatTime(now.Add(time.Hour))})
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err = a.Enroll(rootID, wrong); !errors.Is(err, ErrWrongAuthor) {
			t.Fatalf("Enroll error = %v; want ErrWrongAuthor", err)
		}
	})
}

func TestAuthorityRevokesDelegationValidAfterEpochActivation(t *testing.T) {
	now := time.Date(2026, 11, 11, 10, 30, 0, 0, time.UTC)
	f := newEpochFixture(t, now, 2)
	d := wire.Delegation{AuthorID: f.rootID, SignerID: keys.DIDOf(f.signer), ProviderID: f.providerID, Scope: []string{"post"}, Epoch: *f.state.Epoch, ValidFrom: "2026-11-11T10:00:00Z", ValidUntil: "2026-11-11T11:00:00Z"}
	d, err := SignDelegation(f.root, d)
	if err != nil {
		t.Fatal(err)
	}
	req, err := SignRevocation(f.root, wire.RevocationRequest{ProviderID: f.providerID, DelegationID: d.DelegationID, Delegation: d})
	if err != nil {
		t.Fatal(err)
	}
	if id, err := f.authority.Revoke(f.rootID, req); err != nil || id != d.DelegationID {
		t.Fatalf("Revoke = (%q, %v); want delegation revocation", id, err)
	}
}

func TestAuthorityFreezeRotateAndRestoredEpochs(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	f := newEpochFixture(t, now, 3)
	admit := func(a *Authority, d wire.Delegation) (called bool, err error) {
		err = a.Admit(d, Use{AuthorID: f.rootID, SignerID: d.SignerID, MessageType: "post"}, func(time.Time) error { called = true; return nil })
		return called, err
	}
	oldDelegation := delegationFor(t, f.root, f.signer, f.rootID, f.providerID, *f.state.Epoch, now)
	freeze, err := SignControl(f.root, wire.AuthorizationEpochControl{ProviderID: f.providerID, ExpectedEpochID: f.state.Epoch.EpochID, ExpectedStateRevision: 1, Action: "freeze", Nonce: keys.EncodeB64URL(bytes.Repeat([]byte{32}, 32))})
	if err != nil {
		t.Fatal(err)
	}
	frozen, err := f.authority.Control(f.rootID, freeze)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("frozen author rejects admission without commit", func(t *testing.T) {
		if called, err := admit(f.authority, oldDelegation); !errors.Is(err, ErrRevoked) || called {
			t.Fatalf("Admit = %v, commit called %t", err, called)
		}
	})
	rotate, err := SignControl(f.root, wire.AuthorizationEpochControl{ProviderID: f.providerID, ExpectedEpochID: frozen.Epoch.EpochID, ExpectedStateRevision: 2, Action: "rotate", Nonce: keys.EncodeB64URL(bytes.Repeat([]byte{33}, 32))})
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := f.authority.Control(f.rootID, rotate)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("retired epoch rejects admission without commit", func(t *testing.T) {
		if called, err := admit(f.authority, oldDelegation); !errors.Is(err, ErrRevoked) || called {
			t.Fatalf("Admit = %v, commit called %t", err, called)
		}
	})
	newDelegation := delegationFor(t, f.root, f.signer, f.rootID, f.providerID, *rotated.Epoch, now)
	revoke, err := SignRevocation(f.root, wire.RevocationRequest{ProviderID: f.providerID, DelegationID: newDelegation.DelegationID, Delegation: newDelegation})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.authority.Revoke(f.rootID, revoke); err != nil {
		t.Fatal(err)
	}
	snapshot, err := f.store.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	restored, err := RestoreMemoryEpochStore(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	recovered, err := NewAuthority(AuthorityConfig{ProviderKey: f.provider, Store: restored, Now: func() time.Time { return now }, MaxRevocationsPerAuthor: 3})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("restored revocation remains effective", func(t *testing.T) {
		if called, err := admit(recovered, newDelegation); !errors.Is(err, ErrRevoked) || called {
			t.Fatalf("Admit = %v, commit called %t", err, called)
		}
	})
	t.Run("restored retired epoch remains ineffective", func(t *testing.T) {
		if called, err := admit(recovered, oldDelegation); !errors.Is(err, ErrRevoked) || called {
			t.Fatalf("Admit = %v, commit called %t", err, called)
		}
	})
}

type orderingStore struct {
	EpochStore
	events chan<- string
}

func (s orderingStore) Commit(authorID string, prevVersion uint64, next AuthorRecord) error {
	s.events <- "store commit"
	return s.EpochStore.Commit(authorID, prevVersion, next)
}

func TestAuthoritySerializesRevokeWithAdmission(t *testing.T) {
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	f := newEpochFixture(t, now, 2)
	events := make(chan string, 2)
	a, err := NewAuthority(AuthorityConfig{ProviderKey: f.provider, Store: orderingStore{EpochStore: f.store, events: events}, Now: func() time.Time { return now }, MaxRevocationsPerAuthor: 2})
	if err != nil {
		t.Fatal(err)
	}
	d := delegationFor(t, f.root, f.signer, f.rootID, f.providerID, *f.state.Epoch, now)
	req, err := SignRevocation(f.root, wire.RevocationRequest{ProviderID: f.providerID, DelegationID: d.DelegationID, Delegation: d})
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	admitResult := make(chan error, 1)
	go func() {
		admitResult <- a.Admit(d, Use{AuthorID: f.rootID, SignerID: d.SignerID, MessageType: "post"}, func(time.Time) error {
			close(entered)
			<-release
			events <- "admission commit"
			return nil
		})
	}()
	<-entered
	revokeStarted := make(chan struct{})
	revokeResult := make(chan error, 1)
	go func() {
		close(revokeStarted)
		_, err := a.Revoke(f.rootID, req)
		revokeResult <- err
	}()
	<-revokeStarted
	close(release)
	if err := <-admitResult; err != nil {
		t.Fatal(err)
	}
	if err := <-revokeResult; err != nil {
		t.Fatal(err)
	}
	if first, second := <-events, <-events; first != "admission commit" || second != "store commit" {
		t.Fatalf("commit order = %q, %q; want the admission commit before the revocation is stored", first, second)
	}
	called := false
	err = a.Admit(d, Use{AuthorID: f.rootID, SignerID: d.SignerID, MessageType: "post"}, func(time.Time) error { called = true; return nil })
	if !errors.Is(err, ErrRevoked) || called {
		t.Fatalf("following Admit = %v, commit called %t", err, called)
	}
}

type epochFixture struct {
	root, provider, signer keys.PrivateKey
	rootID, providerID     string
	store                  *MemoryEpochStore
	authority              *Authority
	state                  wire.AuthorizationEpochState
}

func newEpochFixture(t *testing.T, now time.Time, capacity int) epochFixture {
	t.Helper()
	root, err := keys.NewKeyFromSeed(bytes.Repeat([]byte{41}, 32))
	if err != nil {
		t.Fatal(err)
	}
	provider, err := keys.NewKeyFromSeed(bytes.Repeat([]byte{42}, 32))
	if err != nil {
		t.Fatal(err)
	}
	signer, err := keys.NewKeyFromSeed(bytes.Repeat([]byte{43}, 32))
	if err != nil {
		t.Fatal(err)
	}
	f := epochFixture{root: root, provider: provider, signer: signer, rootID: keys.DIDOf(root), providerID: keys.DIDOf(provider), store: NewMemoryEpochStore()}
	f.authority, err = NewAuthority(AuthorityConfig{ProviderKey: provider, Store: f.store, Now: func() time.Time { return now }, Random: bytes.NewReader(bytes.Repeat([]byte{44}, 1024)), MaxRevocationsPerAuthor: capacity})
	if err != nil {
		t.Fatal(err)
	}
	request, err := SignEnroll(root, wire.AuthorizationEpochEnroll{ProviderID: f.providerID, RequestNonce: keys.EncodeB64URL(bytes.Repeat([]byte{45}, 32)), NotAfter: wire.FormatTime(now.Add(time.Hour))})
	if err != nil {
		t.Fatal(err)
	}
	if f.state, _, err = f.authority.Enroll(f.rootID, request); err != nil {
		t.Fatal(err)
	}
	return f
}

func delegationFor(t *testing.T, root, signer keys.PrivateKey, rootID, providerID string, epoch wire.AuthorizationEpoch, now time.Time) wire.Delegation {
	t.Helper()
	d := wire.Delegation{AuthorID: rootID, SignerID: keys.DIDOf(signer), ProviderID: providerID, Scope: []string{"post"}, Epoch: epoch, ValidFrom: wire.FormatTime(now.Add(-time.Minute)), ValidUntil: wire.FormatTime(now.Add(time.Hour))}
	result, err := SignDelegation(root, d)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
