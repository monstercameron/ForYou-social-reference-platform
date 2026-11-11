package sdk

import (
	"context"
	"crypto/ed25519"
	"errors"
	"net/http"
	"strings"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/delegation"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

// ErrChallengeMismatch means a service challenge did not match the local request; it is never a harmless sign-in.
var ErrChallengeMismatch = errors.New("sdk: service challenge mismatch")

// acAuthenticate obtains one fresh challenge, checks it against the exact request, and signs it once.
// CheckBeforeSigning requires a `/v1/` resource, so clients configured with a base path prefix are rejected before signing.
func (c *Client) acAuthenticate(ctx context.Context, key ed25519.PrivateKey, method, resource string, body []byte) (http.Header, error) {
	author := keys.DIDOf(key)
	kind := delegation.BodyJSON
	if method == http.MethodGet {
		kind = delegation.BodyNone
		body = nil
	}
	bodyHash, err := delegation.BodyHash(kind, body)
	if err != nil {
		return nil, ErrChallengeMismatch
	}
	want := delegation.ChallengeRequest{AuthorID: author, Method: method, Resource: resource, BodyHash: bodyHash}
	challengeBody, err := canon.Encode(want)
	if err != nil {
		return nil, err
	}
	res, err := c.send(ctx, http.MethodPost, "/v1/service-challenges", nil, challengeBody, nil, rdObjectLimit)
	if err != nil {
		return nil, err
	}
	if res.status != http.StatusOK {
		return nil, rdProtocol("unexpected service challenge status")
	}
	var challenge wire.ServiceChallenge
	if wire.DecodeLimits(res.body, canon.Limits{MaxBytes: int(rdObjectLimit), MaxDepth: canon.MessageLimits.MaxDepth}, "ServiceChallenge", &challenge) != nil {
		return nil, ErrChallengeMismatch
	}
	if delegation.CheckBeforeSigning(challenge, want, c.ServiceID(), c.now(), 5*time.Second) != nil {
		return nil, ErrChallengeMismatch
	}
	encoded, signature, err := delegation.SignChallenge(key, challenge)
	if err != nil {
		return nil, err
	}
	headers := make(http.Header)
	headers.Set(delegation.HeaderChallenge, encoded)
	headers.Set(delegation.HeaderSignature, signature)
	return headers, nil
}

func acResource(c *Client, path string) string {
	return strings.TrimSuffix(c.base.EscapedPath(), "/") + "/" + strings.TrimPrefix(path, "/")
}

func acJSON(value any) ([]byte, error) { return canon.Encode(value) }

func acDecode(body []byte, definition string, out any) error {
	if err := wire.DecodeLimits(body, canon.Limits{MaxBytes: int(rdObjectLimit), MaxDepth: canon.MessageLimits.MaxDepth}, definition, out); err != nil {
		return rdProtocol("invalid " + definition + " response")
	}
	return nil
}

func acDecodeExactStatus(body []byte, want string) error {
	value, err := canon.Parse(body, canon.Limits{MaxBytes: 256, MaxDepth: 2})
	if err != nil {
		return rdProtocol("invalid status response")
	}
	object, ok := value.(map[string]any)
	if !ok || len(object) != 1 || object["status"] != want {
		return rdProtocol("unexpected status response")
	}
	return nil
}
