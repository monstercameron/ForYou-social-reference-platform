package server

import (
	"context"
	"net/http"
	"strings"
	"time"

	"foryou.local/foryou/internal/names"
)

// namesView is what one response may show of names. The public cache is always
// used. Petnames are the reader's own and apply only outside embeds, so an
// embedded post looks the same for everyone.
type namesView struct {
	cache    *nameCache
	now      time.Time
	petnames map[string]string
}

// namesViewFor reads the reader's petnames for one request. The profile comes from
// the profile cookie only, and an invalid or missing cookie means no petnames.
func (a *App) namesViewFor(r *http.Request) namesView {
	view := namesView{cache: a.nameCache(), now: a.now(), petnames: map[string]string{}}
	if strings.HasPrefix(r.URL.Path, "/embed/") {
		return view
	}
	if profile, ok := namesProfileID(r); ok {
		if petnames, err := a.petnames(profile); err == nil {
			view.petnames = petnames
		}
	}
	return view
}

// namesProfileID returns the profile the cookie names when it is a valid profile ID.
// Unlike a.profile it never creates a profile, so rendering a card writes nothing.
func namesProfileID(r *http.Request) (string, bool) {
	cookie, err := r.Cookie(profileCookie)
	if err != nil || !validProfileID(cookie.Value) {
		return "", false
	}
	return cookie.Value, true
}

// AuthorPage is the author name page of the post's key, where the full key is shown.
func (p post) AuthorPage() string {
	return namesAuthorPath(p.AuthorID)
}

// sharersOf lists the other keys whose name matches name: another cached display
// name, or a petname the reader gave another key. Both count because a petname is
// shown instead of the display name.
//
// This is the profile's collision rule ("two different keys on screen, or a visible
// key and a followed key"): only cached names are ever shown, so two keys on one
// screen with matching names are both in the cache and both marked, and a followed
// key enters the cache whenever the reader sees its posts, its label or its author
// page. Names the cache does not hold, such as demo names, are compared per
// response by namesMarkOnScreen. Look-alike detection is not done.
func (v namesView) sharersOf(name, author string) []string {
	others := v.cache.sharers(name, author)
	seen := map[string]bool{}
	for _, other := range others {
		seen[other] = true
	}
	for other, petname := range v.petnames {
		if other != author && !seen[other] && names.SameName(petname, name) {
			others = append(others, other)
		}
	}
	return others
}

// fill sets the author label fields of p: the fingerprint, the reader's petname,
// the cached verified name when p has no signed name yet, and the collision mark.
// It reads the cache and marks the key as wanted; it never contacts a provider.
func (v namesView) fill(p post) post {
	p.Fingerprint = names.Fingerprint(p.AuthorID)
	if p.SignedName == "" {
		cached, _ := v.cache.want(p.AuthorID, v.now)
		p.SignedName, _ = currentName(cached)
	}
	p.Petname = v.petnames[p.AuthorID]
	if shown := p.shownName(); shown != "" {
		p.NameShared = len(v.sharersOf(shown, p.AuthorID)) > 0
	}
	return p
}

// author returns the label of one key as a post that carries only its author fields.
func (v namesView) author(author string) post {
	return v.fill(post{AuthorID: author})
}

// shownName is the name the label shows: the reader's petname, else the signed name.
func (p post) shownName() string {
	if p.Petname != "" {
		return p.Petname
	}
	return p.SignedName
}

// labelPosts fills the author label of every post by a key.
func (a *App) labelPosts(r *http.Request, posts []post) []post {
	view := a.namesViewFor(r)
	for i := range posts {
		if posts[i].AuthorID != "" {
			posts[i] = view.fill(posts[i])
		}
	}
	namesMarkOnScreen(posts)
	return posts
}

// namesMarkOnScreen marks posts by different keys in one response whose shown names
// match, which covers names the cache does not hold.
func namesMarkOnScreen(posts []post) {
	for i := range posts {
		for j := range posts {
			if posts[i].NameShared {
				break
			}
			first, second := posts[i].shownName(), posts[j].shownName()
			posts[i].NameShared = posts[i].AuthorID != posts[j].AuthorID && first != "" && second != "" && names.SameName(first, second)
		}
	}
}

// namesRecords decorates a record source so every post it returns carries its author label.
type namesRecords struct {
	recordSource
	app *App
}

func (s namesRecords) Posts(ctx context.Context, r *http.Request) ([]post, error) {
	posts, err := s.recordSource.Posts(ctx, r)
	return s.app.labelPosts(r, posts), err
}

func (s namesRecords) Post(ctx context.Context, r *http.Request, id string) (post, bool, error) {
	p, ok, err := s.recordSource.Post(ctx, r, id)
	if ok {
		p = s.app.labelPosts(r, []post{p})[0]
	}
	return p, ok, err
}

func (s namesRecords) Get(ctx context.Context, r *http.Request, id string) (post, Coverage, bool, error) {
	p, coverage, ok, err := s.recordSource.Get(ctx, r, id)
	if ok {
		p = s.app.labelPosts(r, []post{p})[0]
	}
	return p, coverage, ok, err
}

func (s namesRecords) Recent(ctx context.Context, r *http.Request, position string, limit int) (recordPage, error) {
	page, err := s.recordSource.Recent(ctx, r, position, limit)
	page.Posts = s.app.labelPosts(r, page.Posts)
	return page, err
}

func (s namesRecords) Replies(ctx context.Context, r *http.Request, id string) (recordPage, error) {
	page, err := s.recordSource.Replies(ctx, r, id)
	page.Posts = s.app.labelPosts(r, page.Posts)
	return page, err
}

func (s namesRecords) Successors(ctx context.Context, r *http.Request, id string) (recordPage, error) {
	page, err := s.recordSource.Successors(ctx, r, id)
	page.Posts = s.app.labelPosts(r, page.Posts)
	return page, err
}

func (s namesRecords) ByAuthor(ctx context.Context, r *http.Request, authorID string) (recordPage, error) {
	page, err := s.recordSource.ByAuthor(ctx, r, authorID)
	page.Posts = s.app.labelPosts(r, page.Posts)
	return page, err
}

func (s namesRecords) Search(ctx context.Context, r *http.Request, query string, limit int) (recordPage, error) {
	page, err := s.recordSource.Search(ctx, r, query, limit)
	page.Posts = s.app.labelPosts(r, page.Posts)
	return page, err
}
