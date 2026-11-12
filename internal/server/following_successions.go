package server

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"foryou.local/foryou/internal/names"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/succession"
	"foryou.local/foryou/internal/wire"
)

const (
	followingSuccessionBatch = 50
	followingSuccessionTTL   = time.Hour
)

// followingCheck is what the providers said about one followed key's successions.
type followingCheck struct {
	at   time.Time
	held []wire.KeySuccession
}

// followingChecks keeps each followed key's last check for one profile. The
// results are local reader state and are never sent to a provider.
var followingChecks = struct {
	sync.Mutex
	byKey map[string]followingCheck
}{byKey: make(map[string]followingCheck)}

// followingOffer offers to follow the key an old followed key linked to.
type followingOffer struct {
	OldID, NewID                       string
	OldFingerprint, NewFingerprint     string
	OldFingerprintID, NewFingerprintID string
	OldLabelURL, NewLabelURL           string
	NewAuthorURL                       string
	NewFollowed                        bool
}

// followingCandidate is one new key that a conflicting old key names.
type followingCandidate struct {
	DID, Fingerprint, AuthorURL, LabelURL string
}

// followingConflict says that a followed key names more than one new key.
type followingConflict struct {
	OldID, OldFingerprint, OldFingerprintID, OldLabelURL string
	Candidates                                           []followingCandidate
}

// followingSuccessions is the offers region of the Following views.
type followingSuccessions struct {
	Layout    Layout
	Offers    []followingOffer
	Conflicts []followingConflict
	Answered  bool
}

func followingCheckKey(profile, did string) string { return profile + "\x00" + did }

// followingRefreshChecks asks the selected providers about the successions of
// up to followingSuccessionBatch followed keys whose last check is an hour old
// or older, least recently checked first.
func (a *App) followingRefreshChecks(r *http.Request, profile string, state followingState) {
	now := a.now()
	type due struct {
		did string
		at  time.Time
	}
	var stale []due
	followingChecks.Lock()
	for _, entry := range state.Follows {
		check, checked := followingChecks.byKey[followingCheckKey(profile, entry.AuthorID)]
		if !checked || now.Sub(check.at) >= followingSuccessionTTL {
			stale = append(stale, due{did: entry.AuthorID, at: check.at})
		}
	}
	followingChecks.Unlock()
	slices.SortStableFunc(stale, func(x, y due) int { return x.at.Compare(y.at) })
	stale = stale[:min(followingSuccessionBatch, len(stale))]
	if len(stale) == 0 {
		return
	}
	providers := a.selection(profile).Providers
	ctx, cancel := context.WithTimeout(r.Context(), followingPageBudget)
	defer cancel()
	found := make([][]wire.KeySuccession, len(stale))
	answered := make([]bool, len(stale))
	followingParallel(len(stale), func(i int) {
		for _, provider := range providers {
			client, err := a.providerClient(Endpoint{URL: provider.Endpoint, ServiceID: provider.OperatorKey, Label: provider.Label})
			if err != nil {
				continue
			}
			held, _, _, err := client.Successions(ctx, stale[i].did)
			if err != nil {
				continue
			}
			answered[i] = true
			found[i] = append(found[i], held...)
		}
	})
	followingChecks.Lock()
	defer followingChecks.Unlock()
	for i, item := range stale {
		if !answered[i] {
			continue
		}
		followingChecks.byKey[followingCheckKey(profile, item.did)] = followingCheck{at: now, held: found[i]}
	}
}

// followingSuccessionView builds the offers for the followed keys. A link
// offers the new key unless the reader dismissed it; conflicting links offer
// neither key and show every candidate.
func (a *App) followingSuccessionView(r *http.Request, profile string, state followingState, layout Layout) followingSuccessions {
	a.followingRefreshChecks(r, profile, state)
	view := followingSuccessions{Layout: layout}
	followingChecks.Lock()
	defer followingChecks.Unlock()
	for _, entry := range state.Follows {
		check, checked := followingChecks.byKey[followingCheckKey(profile, entry.AuthorID)]
		if !checked {
			continue
		}
		resolved := sdk.ResolveAuthorSuccession(entry.AuthorID, check.held)
		switch resolved.Resolution {
		case succession.Linked:
			if followingDismissed(state, entry.AuthorID, resolved.NewID) {
				continue
			}
			view.Offers = append(view.Offers, followingOfferFor(entry.AuthorID, resolved.NewID, followingFollowed(state, resolved.NewID)))
		case succession.Unresolved:
			view.Conflicts = append(view.Conflicts, followingConflictFor(entry.AuthorID, resolved.Valid))
		}
	}
	return view
}

func followingOfferFor(oldID, newID string, newFollowed bool) followingOffer {
	oldFingerprint, newFingerprint := names.Fingerprint(oldID), names.Fingerprint(newID)
	oldLabel, _ := followingKeyURLs(oldID)
	newLabel, newAuthor := followingKeyURLs(newID)
	return followingOffer{
		OldID: oldID, NewID: newID,
		OldFingerprint: oldFingerprint, NewFingerprint: newFingerprint,
		OldFingerprintID: followingFingerprintID(oldFingerprint), NewFingerprintID: followingFingerprintID(newFingerprint),
		OldLabelURL: oldLabel, NewLabelURL: newLabel, NewAuthorURL: newAuthor,
		NewFollowed: newFollowed,
	}
}

func followingConflictFor(oldID string, valid []wire.KeySuccession) followingConflict {
	fingerprint := names.Fingerprint(oldID)
	label, _ := followingKeyURLs(oldID)
	conflict := followingConflict{OldID: oldID, OldFingerprint: fingerprint, OldFingerprintID: followingFingerprintID(fingerprint), OldLabelURL: label}
	for _, s := range valid {
		if slices.ContainsFunc(conflict.Candidates, func(c followingCandidate) bool { return c.DID == s.NewID }) {
			continue
		}
		candidateLabel, candidateAuthor := followingKeyURLs(s.NewID)
		conflict.Candidates = append(conflict.Candidates, followingCandidate{DID: s.NewID, Fingerprint: names.Fingerprint(s.NewID), AuthorURL: candidateAuthor, LabelURL: candidateLabel})
	}
	return conflict
}

// followingKeyURLs returns the name label region and the author view of a key.
func followingKeyURLs(did string) (label, author string) {
	return "/names/label?author=" + url.QueryEscape(did), "/explore?author=" + url.QueryEscape(did)
}

func followingFingerprintID(fingerprint string) string {
	return strings.ReplaceAll(fingerprint, " ", "")
}

// followingSuccessionsFragment answers with the offers region body for a
// profile's current follows. answered is set when the region answers a reader's
// action, so that an empty region still gives focus a place to land.
func (a *App) followingSuccessionsFragment(w http.ResponseWriter, r *http.Request, profile string, answered bool) {
	state, err := followingStateFor(a, profile)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "Successions could not be read.")
		return
	}
	view := a.followingSuccessionView(r, profile, state, Layout{})
	view.Answered = answered
	a.fragment(w, r, "following-successions", view)
}

func (a *App) followingSuccessionsHandler(w http.ResponseWriter, r *http.Request) {
	profile, err := a.followingProfile(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "Successions could not be read.")
		return
	}
	if !followingIsFragment(r) {
		state, err := a.followingRead(profile)
		if profile == "" || err != nil {
			state = followingState{}
		}
		view := a.followingSuccessionView(r, profile, state, a.layout(r, "following", "Following"))
		a.page(w, r, "following-successions-page", view)
		return
	}
	a.followingSuccessionsFragment(w, r, profile, false)
}

func (a *App) followingDismissHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := followingPostForm(w, r)
	if !ok {
		return
	}
	oldID, newID := form.Get("old"), form.Get("new")
	if !followingCanonical(oldID) || !followingCanonical(newID) {
		http.Error(w, "invalid key", http.StatusBadRequest)
		return
	}
	profile, err := a.profile(w, r)
	if err == nil {
		err = a.followingMigrate(w, r, profile)
	}
	if err == nil {
		err = a.followingUpdate(profile, func(state *followingState) error {
			if followingDismissed(*state, oldID, newID) {
				return nil
			}
			if len(state.Dismissed) >= followingDismissLimit {
				state.Dismissed = state.Dismissed[1:]
			}
			state.Dismissed = append(state.Dismissed, followingDismissal{OldID: oldID, NewID: newID})
			return nil
		})
	}
	if err != nil {
		http.Error(w, "the offer could not be saved", http.StatusInternalServerError)
		return
	}
	if !followingIsFragment(r) {
		http.Redirect(w, r, "/following", http.StatusSeeOther)
		return
	}
	a.followingSuccessionsFragment(w, r, profile, true)
}
