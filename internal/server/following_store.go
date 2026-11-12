package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	"foryou.local/foryou/internal/keys"
)

const (
	followingFileName     = "following.json"
	followingFileLimit    = 256 << 10
	followingKeyLimit     = 500
	followingDismissLimit = 500
	followingLimitMessage = "You can follow up to 500 keys."
)

// Values of followingEntry.Via say where a follow started.
const (
	followingViaPost       = "post"
	followingViaAuthor     = "author"
	followingViaSuccession = "succession"
	followingViaManual     = "manual"
)

var followingErrLimit = errors.New(followingLimitMessage)

// followingEntry is one key the reader follows. The key is the only identity
// stored: a follow is never tied to a display name.
type followingEntry struct {
	AuthorID   string    `json:"author_id"`
	FollowedAt time.Time `json:"followed_at"`
	Via        string    `json:"via"`
}

// followingDismissal records that the reader declined the offer to follow a new key.
type followingDismissal struct {
	OldID string `json:"old_id"`
	NewID string `json:"new_id"`
}

// followingState is the follow list of one profile. It is local application
// state of the reader, not protocol state, and nothing in it is sent to a provider.
// Revision changes with every write and bounds the feed cursors built from it.
type followingState struct {
	Revision  int                  `json:"revision"`
	Follows   []followingEntry     `json:"follows,omitempty"`
	Dismissed []followingDismissal `json:"dismissed,omitempty"`
}

// followingValidVia reports whether via is a source the follow list records.
func followingValidVia(via string) bool {
	switch via {
	case followingViaPost, followingViaAuthor, followingViaSuccession, followingViaManual:
		return true
	}
	return false
}

// followingCanonical reports whether did is a canonical did:key identifier.
func followingCanonical(did string) bool {
	_, err := keys.ParseDID(did)
	return err == nil
}

func (a *App) followingRead(profile string) (followingState, error) {
	if !validProfileID(profile) {
		return followingState{}, errors.New("invalid profile id")
	}
	lock := a.profiles.lock(profile)
	lock.Lock()
	defer lock.Unlock()
	return a.followingLoad(profile)
}

func (a *App) followingLoad(profile string) (followingState, error) {
	file, err := os.Open(filepath.Join(a.profiles.root, profile, followingFileName))
	if errors.Is(err, os.ErrNotExist) {
		return followingState{}, nil
	}
	if err != nil {
		return followingState{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(io.LimitReader(file, followingFileLimit))
	decoder.DisallowUnknownFields()
	var state followingState
	if err := decoder.Decode(&state); err != nil {
		return followingState{}, err
	}
	return state, nil
}

// followingUpdate applies change to the profile's follow list under its lock
// and writes the result atomically.
func (a *App) followingUpdate(profile string, change func(*followingState) error) error {
	if !validProfileID(profile) {
		return errors.New("invalid profile id")
	}
	lock := a.profiles.lock(profile)
	lock.Lock()
	defer lock.Unlock()
	state, err := a.followingLoad(profile)
	if err != nil {
		return err
	}
	if err := change(&state); err != nil {
		return err
	}
	state.Revision++
	return a.followingSave(profile, state)
}

func (a *App) followingSave(profile string, state followingState) error {
	dir := filepath.Join(a.profiles.root, profile)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".following-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return err
	}
	if err := json.NewEncoder(temporary).Encode(state); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, filepath.Join(dir, followingFileName))
}

// followingAdd follows did unless it is already followed, and refuses a new key past the limit.
func followingAdd(state *followingState, did, via string, now time.Time) error {
	if slices.ContainsFunc(state.Follows, func(e followingEntry) bool { return e.AuthorID == did }) {
		return nil
	}
	if len(state.Follows) >= followingKeyLimit {
		return followingErrLimit
	}
	state.Follows = append(state.Follows, followingEntry{AuthorID: did, FollowedAt: now, Via: via})
	return nil
}

func followingUnfollow(state *followingState, did string) {
	state.Follows = slices.DeleteFunc(state.Follows, func(e followingEntry) bool { return e.AuthorID == did })
}

func followingDismissed(state followingState, oldID, newID string) bool {
	return slices.Contains(state.Dismissed, followingDismissal{OldID: oldID, NewID: newID})
}

func followingFollowed(state followingState, did string) bool {
	return slices.ContainsFunc(state.Follows, func(e followingEntry) bool { return e.AuthorID == did })
}

// followingLegacy reports whether the browser still carries the cookie that
// held follows before they moved into the profile's file.
func followingLegacy(r *http.Request) bool {
	_, err := r.Cookie(followCookie)
	return err == nil
}

// followingMigrate moves follows from the legacy browser cookie into the
// profile's file, then expires the cookie. Entries past the limit are dropped.
func (a *App) followingMigrate(w http.ResponseWriter, r *http.Request, profile string) error {
	if !followingLegacy(r) {
		return nil
	}
	legacy := followedAuthors(r)
	now := a.now()
	err := a.followingUpdate(profile, func(state *followingState) error {
		for _, did := range legacy {
			if err := followingAdd(state, did, followingViaPost, now); errors.Is(err, followingErrLimit) {
				break
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{Name: followCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
	return nil
}

// followingProfile returns the profile whose follows a page reads. It creates
// no profile unless legacy follows must be moved into one.
func (a *App) followingProfile(w http.ResponseWriter, r *http.Request) (string, error) {
	profile := requestProfile(r)
	if profile == "" && !followingLegacy(r) {
		return "", nil
	}
	if profile == "" {
		id, err := a.profile(w, r)
		if err != nil {
			return "", err
		}
		profile = id
	}
	if err := a.followingMigrate(w, r, profile); err != nil {
		return "", err
	}
	return profile, nil
}

// followSet returns the keys this browser's reader follows, for marking posts
// and the Following filter. Legacy cookie follows count until they migrate.
func (a *App) followSet(r *http.Request) map[string]bool {
	set := make(map[string]bool)
	if profile := requestProfile(r); profile != "" {
		if state, err := a.followingRead(profile); err == nil {
			for _, entry := range state.Follows {
				set[entry.AuthorID] = true
			}
		}
	}
	for _, id := range followedAuthors(r) {
		set[id] = true
	}
	return set
}
