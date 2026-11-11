package server

import (
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

// followedAuthors reads the author keys this browser follows, oldest first.
// Follows stay in the browser: no provider learns them.
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

func followSet(r *http.Request) map[string]bool {
	set := make(map[string]bool)
	for _, id := range followedAuthors(r) {
		set[id] = true
	}
	return set
}

func setFollowCookie(w http.ResponseWriter, r *http.Request, ids []string) {
	c := &http.Cookie{Name: followCookie, Value: strings.Join(ids, "."), Path: "/", MaxAge: 31536000, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode}
	if len(ids) == 0 {
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
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
	ids := slices.DeleteFunc(followedAuthors(r), func(id string) bool { return id == p.AuthorID })
	if choice == "on" {
		if len(ids) == followLimit {
			ids = ids[1:]
		}
		ids = append(ids, p.AuthorID)
	}
	setFollowCookie(w, r, ids)
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
