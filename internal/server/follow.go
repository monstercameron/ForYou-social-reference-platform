package server

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"foryou.local/foryou/internal/keys"
)

const followCookie = "foryou_follows"
const followLimit = 32

func init() { routeGroups = append(routeGroups, registerFollow) }

func registerFollow(a *App, mux *http.ServeMux) {
	mux.HandleFunc("POST /follow", a.followHandler)
	mux.HandleFunc("POST /follow/fragment", a.followHandler)
}

// followedAuthors reads the author keys the legacy follow cookie holds, oldest
// first. Follows now live in the profile's file; this only feeds the migration.
func followedAuthors(r *http.Request) []string {
	c, err := r.Cookie(followCookie)
	if err != nil || len(c.Value) > 4000 {
		return nil
	}
	var ids []string
	for _, id := range strings.Split(c.Value, ".") {
		if _, err := keys.ParseDID(id); err == nil && len(ids) < followLimit && !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	return ids
}

// followHandler follows or unfollows the key that signed a post's display name.
// The fragment answer updates the control on every post by that key.
func (a *App) followHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := readActionForm(w, r, "id", "follow")
	if !ok {
		return
	}
	p, found := a.requestPost(r, form.Get("id"))
	if !found || p.AuthorID == "" {
		http.NotFound(w, r)
		return
	}
	choice := form.Get("follow")
	if choice != "on" && choice != "off" {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	profile, err := a.profile(w, r)
	if err == nil {
		err = a.followingMigrate(w, r, profile)
	}
	if err == nil {
		err = a.followingUpdate(profile, func(state *followingState) error {
			if choice == "on" {
				return followingAdd(state, p.AuthorID, followingViaPost, a.now())
			}
			followingUnfollow(state, p.AuthorID)
			return nil
		})
	}
	if errors.Is(err, followingErrLimit) {
		http.Error(w, followingLimitMessage, http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "follows could not be saved", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path != "/follow/fragment" {
		http.Redirect(w, r, "/posts/"+url.PathEscape(p.ID), http.StatusSeeOther)
		return
	}
	var signed []post
	for _, other := range staticDemoPosts {
		if other.AuthorID == p.AuthorID {
			other.Following = choice == "on"
			signed = append(signed, other)
		}
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	a.fragment(w, r, "card-follow-response", signed)
}
