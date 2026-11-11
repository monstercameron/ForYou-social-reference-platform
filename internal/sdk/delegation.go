package sdk

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/http"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/delegation"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/succession"
	"foryou.local/foryou/internal/wire"
)

// EnrollEpoch signs, authenticates, and verifies an authorization epoch enrollment.
func (c *Client) EnrollEpoch(ctx context.Context, key ed25519.PrivateKey, e wire.AuthorizationEpochEnroll) (wire.AuthorizationEpochState, error) {
	if e.ProviderID != "" && e.ProviderID != c.ServiceID() {
		return wire.AuthorizationEpochState{}, errors.New("sdk: epoch provider mismatch")
	}
	e.ProviderID = c.ServiceID()
	e, err := delegation.SignEnroll(key, e)
	if err != nil {
		return wire.AuthorizationEpochState{}, err
	}
	body, err := canon.Encode(e)
	if err != nil {
		return wire.AuthorizationEpochState{}, err
	}
	path := "/v1/authorization-epochs"
	state, status, err := c.acEpochPost(ctx, key, path, body)
	if err != nil {
		return wire.AuthorizationEpochState{}, err
	}
	if status != http.StatusCreated && status != http.StatusOK {
		return wire.AuthorizationEpochState{}, rdProtocol("unexpected epoch enrollment status")
	}
	if err = acVerifyEpochState(state, c.ServiceID(), keys.DIDOf(key)); err != nil || state.Status != "active" {
		return wire.AuthorizationEpochState{}, rdProtocol("invalid enrolled epoch state")
	}
	return state, nil
}

// ControlEpoch signs, authenticates and verifies a freeze or rotation. A 409 stale_state is returned as *Error.
// A rotation must yield a different active epoch with sequence at least 2; the previous sequence is not part of the control request.
// A freeze of an already frozen state commits no change, so its revision may equal the expected one.
func (c *Client) ControlEpoch(ctx context.Context, key ed25519.PrivateKey, ctl wire.AuthorizationEpochControl) (wire.AuthorizationEpochState, error) {
	if ctl.Action != "freeze" && ctl.Action != "rotate" {
		return wire.AuthorizationEpochState{}, errors.New("sdk: invalid epoch control action")
	}
	if ctl.ProviderID != "" && ctl.ProviderID != c.ServiceID() {
		return wire.AuthorizationEpochState{}, errors.New("sdk: epoch provider mismatch")
	}
	ctl.ProviderID = c.ServiceID()
	ctl, err := delegation.SignControl(key, ctl)
	if err != nil {
		return wire.AuthorizationEpochState{}, err
	}
	body, err := canon.Encode(ctl)
	if err != nil {
		return wire.AuthorizationEpochState{}, err
	}
	path := "/v1/authorization-epochs/control"
	state, status, err := c.acEpochPost(ctx, key, path, body)
	if err != nil {
		return wire.AuthorizationEpochState{}, err
	}
	if status != http.StatusOK {
		return wire.AuthorizationEpochState{}, rdProtocol("unexpected epoch control status")
	}
	if err = acVerifyEpochState(state, c.ServiceID(), keys.DIDOf(key)); err != nil {
		return wire.AuthorizationEpochState{}, rdProtocol("invalid controlled epoch state")
	}
	advanced := state.StateRevision == ctl.ExpectedStateRevision+1
	if ctl.Action == "freeze" {
		if state.Status != "frozen" || !advanced && state.StateRevision != ctl.ExpectedStateRevision {
			return wire.AuthorizationEpochState{}, rdProtocol("freeze did not freeze the epoch")
		}
		return state, nil
	}
	if !advanced || state.Status != "active" || state.Epoch.EpochID == ctl.ExpectedEpochID || state.Epoch.EpochSequence < 2 {
		return wire.AuthorizationEpochState{}, rdProtocol("rotation did not activate the next epoch")
	}
	return state, nil
}

// EpochState fetches a provider-signed epoch state bound to a fresh client challenge.
func (c *Client) EpochState(ctx context.Context, key ed25519.PrivateKey) (wire.AuthorizationEpochStateResponse, error) {
	challengeBytes := make([]byte, 32)
	if _, err := rand.Read(challengeBytes); err != nil {
		return wire.AuthorizationEpochStateResponse{}, err
	}
	challenge := keys.EncodeB64URL(challengeBytes)
	body, err := canon.Encode(struct {
		AuthorID  string `json:"author_id"`
		Challenge string `json:"challenge"`
	}{keys.DIDOf(key), challenge})
	if err != nil {
		return wire.AuthorizationEpochStateResponse{}, err
	}
	path := "/v1/authorization-epochs/state"
	headers, err := c.acAuthenticate(ctx, key, http.MethodPost, acResource(c, path), body)
	if err != nil {
		return wire.AuthorizationEpochStateResponse{}, err
	}
	res, err := c.send(ctx, http.MethodPost, path, nil, body, headers, rdObjectLimit)
	if err != nil {
		return wire.AuthorizationEpochStateResponse{}, err
	}
	if res.status != http.StatusOK {
		return wire.AuthorizationEpochStateResponse{}, rdProtocol("unexpected epoch state status")
	}
	var response wire.AuthorizationEpochStateResponse
	if err = acDecode(res.body, "AuthorizationEpochStateResponse", &response); err != nil {
		return response, err
	}
	if err = delegation.VerifyStateResponse(response, c.ServiceID(), challenge); err != nil || response.State.AuthorID != keys.DIDOf(key) {
		return wire.AuthorizationEpochStateResponse{}, rdProtocol("invalid epoch state response")
	}
	return response, nil
}

// Revoke submits an idempotent root-signed revocation and verifies the exact acknowledgement.
func (c *Client) Revoke(ctx context.Context, key ed25519.PrivateKey, req wire.RevocationRequest) error {
	if req.ProviderID != "" && req.ProviderID != c.ServiceID() {
		return errors.New("sdk: revocation provider mismatch")
	}
	if req.Delegation.AuthorID != keys.DIDOf(key) {
		return errors.New("sdk: revocation author mismatch")
	}
	delegationID, err := delegation.DelegationID(req.Delegation)
	if err != nil {
		return err
	}
	if req.DelegationID != delegationID {
		return errors.New("sdk: revocation delegation ID mismatch")
	}
	req.ProviderID, req.DelegationID = c.ServiceID(), delegationID
	req, err = delegation.SignRevocation(key, req)
	if err != nil {
		return err
	}
	body, err := canon.Encode(req)
	if err != nil {
		return err
	}
	path := "/v1/revocations"
	headers, err := c.acAuthenticate(ctx, key, http.MethodPost, acResource(c, path), body)
	if err != nil {
		return err
	}
	res, err := c.send(ctx, http.MethodPost, path, nil, body, headers, rdObjectLimit)
	if err != nil {
		return err
	}
	if res.status != http.StatusOK {
		return rdProtocol("unexpected revocation status")
	}
	if err = acDecodeRevocation(res.body, delegationID); err != nil {
		return err
	}
	return nil
}

// PublishSuccession verifies and publishes a self-authenticating key succession without service-auth.
func (c *Client) PublishSuccession(ctx context.Context, s wire.KeySuccession) error {
	if err := succession.Verify(s); err != nil {
		return err
	}
	body, err := canon.Encode(s)
	if err != nil {
		return err
	}
	res, err := c.send(ctx, http.MethodPost, "/v1/successions", nil, body, nil, rdObjectLimit)
	if err != nil {
		return err
	}
	if res.status != http.StatusCreated && res.status != http.StatusOK {
		return rdProtocol("unexpected succession status")
	}
	return nil
}

func (c *Client) acEpochPost(ctx context.Context, key ed25519.PrivateKey, path string, body []byte) (wire.AuthorizationEpochState, int, error) {
	headers, err := c.acAuthenticate(ctx, key, http.MethodPost, acResource(c, path), body)
	if err != nil {
		return wire.AuthorizationEpochState{}, 0, err
	}
	res, err := c.send(ctx, http.MethodPost, path, nil, body, headers, rdObjectLimit)
	if err != nil {
		return wire.AuthorizationEpochState{}, 0, err
	}
	var state wire.AuthorizationEpochState
	if err = acDecode(res.body, "AuthorizationEpochState", &state); err != nil {
		return state, 0, err
	}
	return state, res.status, nil
}

func acVerifyEpochState(state wire.AuthorizationEpochState, providerID, authorID string) error {
	if state.ProviderID != providerID || state.AuthorID != authorID {
		return errors.New("sdk: epoch state identity mismatch")
	}
	if state.Status == "unregistered" {
		if state.Epoch != nil || state.StateRevision != 0 {
			return errors.New("sdk: invalid unregistered state")
		}
	} else if state.Epoch == nil || state.StateRevision < 1 {
		return errors.New("sdk: invalid registered state")
	}
	if state.Epoch != nil && (delegation.VerifyEpoch(*state.Epoch) != nil || state.Epoch.ProviderID != providerID || state.Epoch.AuthorID != authorID) {
		return errors.New("sdk: invalid epoch certificate")
	}
	body, err := canon.EncodeWithout(state, "provider_signature")
	if err != nil {
		return err
	}
	return keys.Verify(providerID, "authorization-epoch-state", body, state.ProviderSignature)
}

func acDecodeRevocation(body []byte, wantID string) error {
	value, err := canon.Parse(body, canon.Limits{MaxBytes: 256, MaxDepth: 2})
	if err != nil {
		return rdProtocol("invalid revocation response")
	}
	object, ok := value.(map[string]any)
	if !ok || len(object) != 2 || object["delegation_id"] != wantID || object["status"] != "revoked" {
		return rdProtocol("unexpected revocation response")
	}
	return nil
}
