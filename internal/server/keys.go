package server

import (
	"crypto/ed25519"
	"net/http"

	"foryou.local/foryou/internal/sdk"
)

// shortDID shows the first 12 and last 6 characters of a did:key.
func shortDID(did string) string {
	if len(did) <= 18 {
		return did
	}
	return did[:12] + "…" + did[len(did)-6:]
}

func (a *App) keystore() *sdk.Keystore { return a.keystoreValue }

// identity returns the reader profile's author identity, if one was created.
func (a *App) identity(w http.ResponseWriter, r *http.Request) (sdk.Identity, bool, error) {
	profile, err := a.profile(w, r)
	if err != nil {
		return sdk.Identity{}, false, err
	}
	return a.keystore().Identity(profile)
}

// authorKey returns the profile's author key, creating it in dev custody on first use.
func (a *App) authorKey(w http.ResponseWriter, r *http.Request) (ed25519.PrivateKey, error) {
	profile, err := a.profile(w, r)
	if err != nil {
		return nil, err
	}
	key, err := a.keystore().PrivateKey(profile)
	if err == nil {
		return key, nil
	}
	if err != sdk.ErrNoIdentity || !a.config.Dev {
		return nil, err
	}
	if _, err := a.keystore().Create(profile); err != nil {
		return nil, err
	}
	return a.keystore().PrivateKey(profile)
}
