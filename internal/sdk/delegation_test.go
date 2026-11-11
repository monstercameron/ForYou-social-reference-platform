package sdk

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/delegation"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/succession"
	"foryou.local/foryou/internal/wire"
)

// acEnrolled returns a fake provider with the author's first epoch enrolled through EnrollEpoch.
func acEnrolled(t *testing.T) (*acProvider, wire.AuthorizationEpochState) {
	t.Helper()
	p := acNewProvider(t, nil)
	state, err := p.client.EnrollEpoch(context.Background(), acAuthorKey(), wire.AuthorizationEpochEnroll{RequestNonce: keys.EncodeB64URL([]byte("ac-enroll-nonce-0000000000000001")), NotAfter: wire.FormatTime(acNow.Add(time.Minute))})
	if err != nil {
		t.Fatal(err)
	}
	return p, state
}

func acControl(p *acProvider, state wire.AuthorizationEpochState, revision int64, action string) (wire.AuthorizationEpochState, error) {
	return p.client.ControlEpoch(context.Background(), acAuthorKey(), wire.AuthorizationEpochControl{ExpectedEpochID: state.Epoch.EpochID, ExpectedStateRevision: revision, Action: action, Nonce: keys.EncodeB64URL([]byte("ac-control-nonce-000000000000001"))})
}

func TestACAuthorizationEpochs(t *testing.T) {
	t.Run("enrollment returns a verified active first epoch", func(t *testing.T) {
		p, state := acEnrolled(t)
		if state.Status != "active" || state.StateRevision != 1 || state.Epoch.EpochSequence != 1 || state.AuthorID != keys.DIDOf(acAuthorKey()) {
			t.Fatalf("EnrollEpoch = %+v; want active revision 1 with epoch 1", state)
		}
		if p.issued() != 1 || p.count("POST /v1/authorization-epochs") != 1 {
			t.Fatalf("challenges/enrollments = %d/%d; want 1/1", p.issued(), p.count("POST /v1/authorization-epochs"))
		}
	})
	t.Run("freeze advances the revision once", func(t *testing.T) {
		p, enrolled := acEnrolled(t)
		state, err := acControl(p, enrolled, 1, "freeze")
		if err != nil || state.Status != "frozen" || state.StateRevision != 2 {
			t.Fatalf("freeze = %+v, %v; want frozen revision 2", state, err)
		}
	})
	t.Run("rotate activates the next epoch", func(t *testing.T) {
		p, enrolled := acEnrolled(t)
		state, err := acControl(p, enrolled, 1, "rotate")
		if err != nil || state.Status != "active" || state.Epoch.EpochSequence != 2 || state.Epoch.EpochID == enrolled.Epoch.EpochID {
			t.Fatalf("rotate = %+v, %v; want active epoch 2", state, err)
		}
	})
	t.Run("stale expected revision is a 409 stale_state", func(t *testing.T) {
		p, enrolled := acEnrolled(t)
		_, err := acControl(p, enrolled, 5, "freeze")
		var public *Error
		if !errors.As(err, &public) || public.HTTPStatus != 409 || public.Status != "stale_state" {
			t.Fatalf("ControlEpoch error = %v; want 409 stale_state", err)
		}
	})
	t.Run("unknown action is refused before any request", func(t *testing.T) {
		p, enrolled := acEnrolled(t)
		if _, err := acControl(p, enrolled, 1, "thaw"); err == nil {
			t.Fatal("ControlEpoch accepted action thaw")
		}
		if p.issued() != 1 {
			t.Fatalf("challenges = %d; want only the enrollment's", p.issued())
		}
	})
	t.Run("state response is signed and bound to the author", func(t *testing.T) {
		p, enrolled := acEnrolled(t)
		response, err := p.client.EpochState(context.Background(), acAuthorKey())
		if err != nil || response.State.Epoch == nil || response.State.Epoch.EpochID != enrolled.Epoch.EpochID {
			t.Fatalf("EpochState = %+v, %v; want the enrolled epoch", response, err)
		}
	})
}

// acRevocation returns a fake provider with an enrolled author and a root-signed delegation under that epoch.
func acRevocation(t *testing.T) (*acProvider, wire.RevocationRequest) {
	t.Helper()
	p, state := acEnrolled(t)
	d, err := delegation.SignDelegation(acAuthorKey(), wire.Delegation{AuthorID: keys.DIDOf(acAuthorKey()), SignerID: keys.DIDOf(acOtherKey()), ProviderID: keys.DIDOf(acProviderKey()), Scope: []string{"post"}, Epoch: *state.Epoch, ValidFrom: wire.FormatTime(acNow), ValidUntil: wire.FormatTime(acNow.Add(time.Hour))})
	if err != nil {
		t.Fatal(err)
	}
	return p, wire.RevocationRequest{DelegationID: d.DelegationID, Delegation: d}
}

func TestACRevoke(t *testing.T) {
	t.Run("revocation is acknowledged and a repeat is idempotent", func(t *testing.T) {
		p, request := acRevocation(t)
		if err := p.client.Revoke(context.Background(), acAuthorKey(), request); err != nil {
			t.Fatal(err)
		}
		if err := p.client.Revoke(context.Background(), acAuthorKey(), request); err != nil {
			t.Fatalf("repeated Revoke: %v", err)
		}
		if p.count("POST /v1/revocations") != 2 {
			t.Fatalf("revocations = %d; want 2", p.count("POST /v1/revocations"))
		}
	})
	t.Run("delegation_id that does not match the delegation is refused locally", func(t *testing.T) {
		p, request := acRevocation(t)
		request.DelegationID = request.DelegationID[:len(request.DelegationID)-64] + strings.Repeat("0", 64)
		if err := p.client.Revoke(context.Background(), acAuthorKey(), request); err == nil {
			t.Fatal("Revoke accepted a mismatched delegation_id")
		}
		if p.count("POST /v1/revocations") != 0 {
			t.Fatalf("revocations = %d; want 0", p.count("POST /v1/revocations"))
		}
	})
}

func TestACPublishSuccession(t *testing.T) {
	effective := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	t.Run("valid succession posts once without service-auth", func(t *testing.T) {
		value, err := succession.New(acAuthorKey(), acOtherKey(), effective)
		if err != nil {
			t.Fatal(err)
		}
		p := acNewProvider(t, nil)
		if err := p.client.PublishSuccession(context.Background(), value); err != nil {
			t.Fatal(err)
		}
		if p.count("POST /v1/successions") != 1 || p.issued() != 0 {
			t.Fatalf("publishes/challenges = %d/%d; want 1/0", p.count("POST /v1/successions"), p.issued())
		}
	})
	t.Run("invalid succession is rejected without a request", func(t *testing.T) {
		value, err := succession.New(acAuthorKey(), acOtherKey(), effective)
		if err != nil {
			t.Fatal(err)
		}
		value.NewSignature = value.OldSignature
		p := acNewProvider(t, nil)
		if err := p.client.PublishSuccession(context.Background(), value); err == nil {
			t.Fatal("PublishSuccession accepted a succession with a wrong new_signature")
		}
		if p.count("POST /v1/successions") != 0 {
			t.Fatalf("publishes = %d; want 0", p.count("POST /v1/successions"))
		}
	})
}
