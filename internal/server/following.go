package server

import (
	"errors"
	"net/http"
	"net/url"
	"slices"

	"foryou.local/foryou/internal/names"
)

func init() { routeGroups = append(routeGroups, registerFollowing) }

func registerFollowing(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /following", a.followingFeedHandler)
	mux.HandleFunc("GET /following/more", a.followingMoreHandler)
	mux.HandleFunc("GET /following/keys", a.followingKeysHandler)
	mux.HandleFunc("GET /following/control", a.followingControlHandler)
	mux.HandleFunc("POST /following/follow", a.followingFollowHandler)
	mux.HandleFunc("POST /following/unfollow", a.followingUnfollowHandler)
	mux.HandleFunc("GET /following/successions", a.followingSuccessionsHandler)
	mux.HandleFunc("POST /following/successions/dismiss", a.followingDismissHandler)
}

// followingPage is the Following feed or the keys list, with its tab selected.
type followingPage struct {
	Layout Layout
	Tab    string
	Feed   followingFeed
	Keys   []followingKey
}

// followingKey is one row of the keys list.
type followingKey struct {
	DID, Fingerprint, FingerprintID string
	FollowedAt, LabelURL, AuthorURL string
}

// followingControl is the follow toggle for one key, as a fragment or a full page.
type followingControl struct {
	Layout                          Layout
	DID, Fingerprint, FingerprintID string
	Via                             string
	Followed                        bool
	Notice, Alert                   string
}

// followingIsFragment reports whether an HTMLeX request asks for a fragment.
// HTMLeX fetch requests have Sec-Fetch-Dest: empty.
func followingIsFragment(r *http.Request) bool {
	return r.Header.Get("Sec-Fetch-Dest") == "empty"
}

// followingPostForm parses the small same-origin form behind a follow control.
func followingPostForm(w http.ResponseWriter, r *http.Request) (url.Values, bool) {
	if !requireSameOrigin(w, r) || !parseSmallForm(w, r, 4096) {
		return nil, false
	}
	return r.PostForm, true
}

func followingStateFor(a *App, profile string) (followingState, error) {
	if profile == "" {
		return followingState{}, nil
	}
	return a.followingRead(profile)
}

func followingControlFor(did string, followed bool, via string) followingControl {
	fingerprint := names.Fingerprint(did)
	return followingControl{DID: did, Fingerprint: fingerprint, FingerprintID: followingFingerprintID(fingerprint), Via: via, Followed: followed}
}

// followingKeyRows lists the followed keys, newest follow first.
func followingKeyRows(state followingState) []followingKey {
	entries := slices.Clone(state.Follows)
	slices.SortStableFunc(entries, func(x, y followingEntry) int { return y.FollowedAt.Compare(x.FollowedAt) })
	rows := make([]followingKey, 0, len(entries))
	for _, entry := range entries {
		fingerprint := names.Fingerprint(entry.AuthorID)
		label, author := followingKeyURLs(entry.AuthorID)
		rows = append(rows, followingKey{DID: entry.AuthorID, Fingerprint: fingerprint, FingerprintID: followingFingerprintID(fingerprint), FollowedAt: entry.FollowedAt.Format("2 Jan 2006"), LabelURL: label, AuthorURL: author})
	}
	return rows
}

func (a *App) followingFeedHandler(w http.ResponseWriter, r *http.Request) {
	profile, err := a.followingProfile(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "Following could not be read.")
		return
	}
	state, err := followingStateFor(a, profile)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "Following could not be read.")
		return
	}
	var feed followingFeed
	if token := r.URL.Query().Get("cursor"); token != "" {
		feed = a.followingNextFeed(r, profile, state, token)
	} else {
		feed = a.followingFirstFeed(r, profile, state)
	}
	a.page(w, r, "following-page", followingPage{Layout: a.layout(r, "following", "Following"), Tab: "posts", Feed: feed})
}

// followingMoreHandler appends the next batch behind a cursor to the Following feed.
func (a *App) followingMoreHandler(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("cursor")
	if token == "" {
		http.Error(w, "invalid feed page", http.StatusBadRequest)
		return
	}
	profile, err := a.followingProfile(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "Following could not be read.")
		return
	}
	state, err := followingStateFor(a, profile)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "Following could not be read.")
		return
	}
	a.fragment(w, r, "following-more-response", followingPage{Feed: a.followingNextFeed(r, profile, state, token)})
}

func (a *App) followingKeysHandler(w http.ResponseWriter, r *http.Request) {
	profile, err := a.followingProfile(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "Following could not be read.")
		return
	}
	state, err := followingStateFor(a, profile)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "Following could not be read.")
		return
	}
	a.page(w, r, "following-keys-page", followingPage{Layout: a.layout(r, "following", "Following"), Tab: "keys", Keys: followingKeyRows(state)})
}

// followingControlHandler renders the follow toggle for one key. Without HTMLeX
// the same URL returns the full page.
func (a *App) followingControlHandler(w http.ResponseWriter, r *http.Request) {
	did := r.URL.Query().Get("author")
	if !followingCanonical(did) {
		http.Error(w, "invalid key", http.StatusBadRequest)
		return
	}
	control := followingControlFor(did, a.followSet(r)[did], followingViaAuthor)
	a.followingRenderControl(w, r, control)
}

func (a *App) followingFollowHandler(w http.ResponseWriter, r *http.Request) {
	a.followingChange(w, r, true)
}

func (a *App) followingUnfollowHandler(w http.ResponseWriter, r *http.Request) {
	a.followingChange(w, r, false)
}

// followingChange follows or unfollows a key from a control, from a key row
// (view=row) or from an offer (view=offers). Without HTMLeX it returns the
// full control page.
func (a *App) followingChange(w http.ResponseWriter, r *http.Request, follow bool) {
	form, ok := followingPostForm(w, r)
	if !ok {
		return
	}
	did, via := form.Get("author"), form.Get("via")
	if via == "" {
		via = followingViaAuthor
	}
	if !followingCanonical(did) || !followingValidVia(via) {
		http.Error(w, "invalid key", http.StatusBadRequest)
		return
	}
	profile, err := a.profile(w, r)
	if err == nil {
		err = a.followingMigrate(w, r, profile)
	}
	if err == nil {
		err = a.followingUpdate(profile, func(state *followingState) error {
			if follow {
				return followingAdd(state, did, via, a.now())
			}
			followingUnfollow(state, did)
			return nil
		})
	}
	alert := ""
	if errors.Is(err, followingErrLimit) {
		alert, err = followingLimitMessage, nil
	}
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "Follows could not be saved.")
		return
	}
	switch form.Get("view") {
	case "offers":
		if !followingIsFragment(r) {
			http.Redirect(w, r, "/following", http.StatusSeeOther)
			return
		}
		a.followingSuccessionsFragment(w, r, profile, true)
		return
	case "row":
		if !followingIsFragment(r) {
			http.Redirect(w, r, "/following/keys", http.StatusSeeOther)
			return
		}
		if !follow {
			a.followingRenderRemoved(w, r, did)
			return
		}
	}
	state, err := followingStateFor(a, profile)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "Follows could not be read.")
		return
	}
	control := followingControlFor(did, followingFollowed(state, did), via)
	if alert != "" {
		control.Alert = alert
	} else if follow {
		control.Notice = "Following this key."
	} else {
		control.Notice = "No longer following this key."
	}
	a.followingRenderControl(w, r, control)
}

func (a *App) followingRenderControl(w http.ResponseWriter, r *http.Request, control followingControl) {
	if followingIsFragment(r) {
		a.fragment(w, r, "following-control", control)
		return
	}
	control.Layout = a.layout(r, "following", "Follow a key")
	a.page(w, r, "following-control-page", control)
}

// followingRenderRemoved answers an unfollow from a key row with the row's replacement.
func (a *App) followingRenderRemoved(w http.ResponseWriter, r *http.Request, did string) {
	fingerprint := names.Fingerprint(did)
	a.fragment(w, r, "following-removed", followingControl{DID: did, Fingerprint: fingerprint, FingerprintID: followingFingerprintID(fingerprint), Notice: "No longer following this key."})
}
