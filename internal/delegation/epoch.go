package delegation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

// EpochID derives the content ID of e without its ID or provider signature.
func EpochID(e wire.AuthorizationEpoch) (string, error) {
	b, err := canon.EncodeWithout(e, "epoch_id", "provider_signature")
	if err != nil {
		return "", err
	}
	return keys.DigestID("fye1:", "authorization-epoch-id", b)
}

// VerifyEpoch validates an authorization epoch certificate.
func VerifyEpoch(e wire.AuthorizationEpoch) error {
	if err := checkWire("AuthorizationEpoch", e); err != nil {
		return err
	}
	if e.EpochSequence < 1 || e.EpochSequence > 9007199254740991 {
		return errors.New("invalid epoch sequence")
	}
	if _, err := wire.ParseTime(e.ActivatedAt); err != nil {
		return err
	}
	if _, err := keys.DecodeB64URL(e.Nonce, 32); err != nil {
		return err
	}
	id, err := EpochID(e)
	if err != nil || id != e.EpochID {
		return errors.New("invalid epoch ID")
	}
	b, err := canon.EncodeWithout(e, "provider_signature")
	if err != nil {
		return err
	}
	return keys.Verify(e.ProviderID, "authorization-epoch", b, e.ProviderSignature)
}

// VerifyStateResponse validates a provider-signed response bound to challenge.
func VerifyStateResponse(r wire.AuthorizationEpochStateResponse, providerID, challenge string) error {
	if _, err := keys.DecodeB64URL(challenge, 32); err != nil || r.Challenge != challenge {
		return errors.New("invalid state challenge")
	}
	if err := checkWire("AuthorizationEpochStateResponse", r); err != nil {
		return err
	}
	if r.State.ProviderID != providerID {
		return errors.New("state provider mismatch")
	}
	if err := checkWire("AuthorizationEpochState", r.State); err != nil {
		return err
	}
	if r.State.Status == "unregistered" {
		if r.State.Epoch != nil || r.State.StateRevision != 0 {
			return errors.New("invalid unregistered state")
		}
	} else if r.State.Epoch == nil || r.State.StateRevision < 1 {
		return errors.New("invalid registered state")
	}
	if r.State.Epoch != nil && VerifyEpoch(*r.State.Epoch) != nil {
		return errors.New("invalid state epoch")
	}
	if r.State.Epoch != nil && (r.State.Epoch.ProviderID != r.State.ProviderID || r.State.Epoch.AuthorID != r.State.AuthorID) {
		return errors.New("state epoch binding mismatch")
	}
	sb, err := canon.EncodeWithout(r.State, "provider_signature")
	if err != nil {
		return err
	}
	if err = keys.Verify(providerID, "authorization-epoch-state", sb, r.State.ProviderSignature); err != nil {
		return err
	}
	rb, err := canon.EncodeWithout(r, "provider_signature")
	if err != nil {
		return err
	}
	return keys.Verify(providerID, "authorization-epoch-state-response", rb, r.ProviderSignature)
}

// SignEnroll signs an authorization epoch enrollment request.
func SignEnroll(root keys.PrivateKey, b wire.AuthorizationEpochEnroll) (wire.AuthorizationEpochEnroll, error) {
	b.AuthorID = keys.DIDOf(root)
	encoded, err := canon.EncodeWithout(b, "author_signature")
	if err != nil {
		return b, err
	}
	b.AuthorSignature, err = keys.Sign(root, "authorization-epoch-enroll", encoded)
	return b, err
}

// SignControl signs an authorization epoch control request.
func SignControl(root keys.PrivateKey, b wire.AuthorizationEpochControl) (wire.AuthorizationEpochControl, error) {
	b.AuthorID = keys.DIDOf(root)
	encoded, err := canon.EncodeWithout(b, "author_signature")
	if err != nil {
		return b, err
	}
	b.AuthorSignature, err = keys.Sign(root, "authorization-epoch-control", encoded)
	return b, err
}

// SignRevocation signs a delegation revocation request.
func SignRevocation(root keys.PrivateKey, b wire.RevocationRequest) (wire.RevocationRequest, error) {
	b.AuthorID = keys.DIDOf(root)
	encoded, err := canon.EncodeWithout(b, "signature")
	if err != nil {
		return b, err
	}
	b.Signature, err = keys.Sign(root, "revoke", encoded)
	return b, err
}

func checkWire(def string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	if err = decoder.Decode(&decoded); err != nil {
		return err
	}
	return wire.Check(def, decoded)
}

func signEpoch(key keys.PrivateKey, e wire.AuthorizationEpoch) (wire.AuthorizationEpoch, error) {
	e.ProviderID = keys.DIDOf(key)
	id, err := EpochID(e)
	if err != nil {
		return e, err
	}
	e.EpochID = id
	b, err := canon.EncodeWithout(e, "provider_signature")
	if err != nil {
		return e, err
	}
	e.ProviderSignature, err = keys.Sign(key, "authorization-epoch", b)
	return e, err
}

func signState(key keys.PrivateKey, s wire.AuthorizationEpochState) (wire.AuthorizationEpochState, error) {
	s.ProviderID = keys.DIDOf(key)
	b, err := canon.EncodeWithout(s, "provider_signature")
	if err != nil {
		return s, err
	}
	s.ProviderSignature, err = keys.Sign(key, "authorization-epoch-state", b)
	return s, err
}

func signResponse(key keys.PrivateKey, r wire.AuthorizationEpochStateResponse) (wire.AuthorizationEpochStateResponse, error) {
	b, err := canon.EncodeWithout(r, "provider_signature")
	if err != nil {
		return r, err
	}
	r.ProviderSignature, err = keys.Sign(key, "authorization-epoch-state-response", b)
	return r, err
}

func second(t time.Time) time.Time { return t.UTC().Truncate(time.Second) }

func rootSigned(authorID, label string, value any, signature string, omit string) error {
	b, err := canon.EncodeWithout(value, omit)
	if err != nil {
		return err
	}
	if err = keys.Verify(authorID, label, b, signature); err != nil {
		return fmt.Errorf("%w: %v", ErrBadRootSignature, err)
	}
	return nil
}
