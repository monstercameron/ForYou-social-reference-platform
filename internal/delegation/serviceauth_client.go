package delegation

import (
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

// CheckBeforeSigning confirms a challenge matches the locally approved request and pinned service.
func CheckBeforeSigning(challenge wire.ServiceChallenge, want ChallengeRequest, pinnedServiceID string, now time.Time, tolerance time.Duration) error {
	if tolerance < 0 || challengeValidateWire(challenge) != nil || challengeValidateRequest(want) != nil {
		return ErrChallengeMismatch
	}
	if challenge.AuthorID != want.AuthorID || challenge.Method != want.Method || challenge.Resource != want.Resource || challenge.BodyHash != want.BodyHash || challenge.ServiceID != pinnedServiceID {
		return ErrChallengeMismatch
	}
	expires, err := wire.ParseTime(challenge.ExpiresAt)
	if err != nil || !expires.After(now.Add(-tolerance)) || expires.After(now.Add(ChallengeLifetime+tolerance)) {
		return ErrChallengeMismatch
	}
	return nil
}

// SignChallenge returns the canonical challenge and service-auth signature headers.
func SignChallenge(root keys.PrivateKey, challenge wire.ServiceChallenge) (string, string, error) {
	if challengeValidateWire(challenge) != nil {
		return "", "", ErrChallengeMismatch
	}
	canonical, err := canon.Encode(challenge)
	if err != nil {
		return "", "", ErrChallengeMismatch
	}
	signature, err := keys.Sign(root, "service-auth", canonical)
	if err != nil {
		return "", "", err
	}
	return keys.EncodeB64URL(canonical), signature, nil
}
