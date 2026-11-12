package server

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"foryou.local/foryou/internal/sdk"
)

func init() { routeGroups = append(routeGroups, registerFeed) }

func registerFeed(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", a.homeHandler)
	mux.HandleFunc("GET /feed", a.feedHandler)
	mux.HandleFunc("GET /feed/search", a.searchHandler)
	mux.HandleFunc("GET /feed/more", a.moreHandler)
}

const (
	// feedChunk is how many posts one page of Home shows.
	feedChunk = 5
	// feedScanCalls bounds the record reads one Home request may make while
	// it looks for enough posts that pass the filters.
	feedScanCalls = 64
	feedPosition  = 512
)

var feedErrInvalid = errors.New("invalid feed request")

type feedPage struct {
	Layout    Layout
	Posts     []post
	HasMore   bool
	NextLimit int
	Loaded    int
	Anchor    string
	Order     fyoView
}

// feedHomePage is the Home feed: one page of records from Recent, with the
// coverage of what the sources returned and the position of the next page.
type feedHomePage struct {
	Layout    Layout
	Posts     []post
	Next      string
	Continue  string
	Position  string
	Coverage  Coverage
	Hidden    int
	Untrusted bool
	Trust     string
	Failed    bool
	Retry     string
	Newer     string
	Toggle    string
	// Legacy marks a page paged by a count and an anchor, the window links
	// made before positions; HasMore, NextLimit, Anchor and Loaded belong to it.
	Legacy    bool
	HasMore   bool
	NextLimit int
	Anchor    string
	Loaded    int
	Order     fyoView
}

// feedFilter is the reader's choice of posts: a kind of post, or their own
// follows, likes or the ads section.
type feedFilter struct {
	name     string
	followed map[string]bool
	liked    map[string]bool
}

var feedFilterNames = []string{"all", "notes", "posts", "replies", "text", "images", "video", "following", "liked", "ads"}

func (f feedFilter) matches(p post) bool {
	switch f.name {
	case "all", "ads":
		return true
	case "notes", "posts":
		return !p.IsReply
	case "replies":
		return p.IsReply
	case "following":
		return f.followed[p.AuthorID]
	case "liked":
		return f.liked[p.ID]
	}
	return f.name == p.Kind
}

// keeps reports whether a post passes the filter and the search needle, which is lower case.
func (f feedFilter) keeps(p post, needle string) bool {
	return f.matches(p) && (needle == "" || strings.Contains(strings.ToLower(p.Receipt+" "+p.Text), needle))
}

// matchingPosts counts through the requested limit but retains only the window.
// One extra match determines whether another page exists. The lists are read
// in order and never joined.
func matchingPosts(f feedFilter, query, anchor string, offset, limit int, lists ...[]post) ([]post, string, int, bool) {
	capacity := 0
	for _, list := range lists {
		capacity += len(list)
	}
	if capacity > limit-offset {
		capacity = limit - offset
	}
	filtered := make([]post, 0, capacity)
	needle := strings.ToLower(query)
	found := anchor == ""
	first := ""
	count := 0
	for _, list := range lists {
		for _, p := range list {
			if !f.matches(p) {
				continue
			}
			if needle != "" && !strings.Contains(strings.ToLower(p.Receipt+" "+p.Text), needle) {
				continue
			}
			if !found {
				if p.ID != anchor {
					continue
				}
				found = true
			}
			if first == "" {
				first = p.ID
			}
			count++
			if count > limit {
				return filtered, first, count, found
			}
			if count > offset {
				filtered = append(filtered, p)
			}
		}
	}
	return filtered, first, count, found
}

// readFeedRequest serves Explore, which pages its list by a count and an anchor.
func (a *App) readFeedRequest(r *http.Request, more bool) (feedPage, bool) {
	return a.feedWindowOf(r, more, func() ([]post, error) { return a.records().Posts(r.Context(), r) })
}

// feedWindowOf pages the posts that read returns by a count and an anchor.
// read runs only once the request is valid.
func (a *App) feedWindowOf(r *http.Request, more bool, read func() ([]post, error)) (feedPage, bool) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return feedPage{}, false
	}
	filter := values.Get("filter")
	if filter == "" {
		filter = "all"
	}
	if !slices.Contains(feedFilterNames, filter) {
		return feedPage{}, false
	}
	query := strings.TrimSpace(values.Get("q"))
	if !utf8.ValidString(query) || utf8.RuneCountInString(query) > 120 {
		return feedPage{}, false
	}
	anchor := values.Get("anchor")
	if len(anchor) > 80 {
		return feedPage{}, false
	}
	requestedTheme := values.Get("theme")
	if requestedTheme != "" && requestedTheme != "dark" && requestedTheme != "light" {
		return feedPage{}, false
	}
	limit := 5
	if raw := values.Get("limit"); raw != "" {
		var err error
		limit, err = strconv.Atoi(raw)
		if err != nil || limit < 5 || limit > 10000 || limit%5 != 0 {
			return feedPage{}, false
		}
	}
	offset := 0
	if more {
		if limit < 10 {
			return feedPage{}, false
		}
		offset = limit - 5
	}
	available, err := read()
	if err != nil {
		return feedPage{}, false
	}
	lists := [][]post{available}
	chosen := feedFilter{name: filter}
	switch filter {
	case "following":
		chosen.followed = followSet(r)
	case "liked":
		chosen.liked = likedSet(r)
		lists = append(lists, adsOutsideFeed)
	case "ads":
		lists = [][]post{adsFor(r)}
	}
	posts, first, count, found := matchingPosts(chosen, query, anchor, offset, limit, lists...)
	if !found {
		return feedPage{}, false
	}
	a.forReader(r, posts)
	page := feedPage{Layout: a.layout(r, "home", "Home"), Posts: posts, Anchor: first}
	page.Layout.Filter = filter
	page.Layout.Query = query
	page.HasMore = count > limit
	page.NextLimit = limit + 5
	if count > limit {
		count = limit
	}
	page.Loaded = count
	page.Order = a.fyoOrder(r, page.Posts)
	return page, true
}

// feedRead builds the Home feed from the record source. Filters, search and
// the untrusted toggle are applied to what Recent returns; the position of a
// previous page resumes the scan. A request that names a limit or an anchor
// is a window of the older count-and-anchor paging, which Explore shares.
func (a *App) feedRead(r *http.Request, more bool) (feedHomePage, error) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return feedHomePage{}, feedErrInvalid
	}
	if values.Has("limit") || values.Has("anchor") {
		return a.feedWindow(r, more)
	}
	filter := values.Get("filter")
	if filter == "" {
		filter = "all"
	}
	if !slices.Contains(feedFilterNames, filter) {
		return feedHomePage{}, feedErrInvalid
	}
	query := strings.TrimSpace(values.Get("q"))
	if !utf8.ValidString(query) || utf8.RuneCountInString(query) > 120 {
		return feedHomePage{}, feedErrInvalid
	}
	requestedTheme := values.Get("theme")
	if requestedTheme != "" && requestedTheme != "dark" && requestedTheme != "light" {
		return feedHomePage{}, feedErrInvalid
	}
	untrusted := false
	switch values.Get("untrusted") {
	case "1":
		untrusted = true
	case "", "0":
	default:
		return feedHomePage{}, feedErrInvalid
	}
	position := values.Get("position")
	if len(position) > feedPosition {
		return feedHomePage{}, feedErrInvalid
	}

	page := feedHomePage{Layout: a.layout(r, "home", "Home"), Position: position, Untrusted: untrusted}
	page.Layout.Filter = filter
	page.Layout.Query = query
	theme := page.Layout.Theme
	page.Newer = feedHref(theme, filter, query, untrusted, "")
	page.Retry = page.Newer
	page.Toggle = feedHref(theme, filter, query, !untrusted, "")

	chosen := feedFilter{name: filter}
	switch filter {
	case "following":
		chosen.followed = followSet(r)
	case "liked":
		chosen.liked = likedSet(r)
	}
	needle := strings.ToLower(query)
	if filter == "ads" {
		for _, ad := range adsFor(r) {
			if chosen.keeps(ad, needle) {
				page.Posts = append(page.Posts, ad)
			}
		}
		a.forReader(r, page.Posts)
		return page, nil
	}
	if filter == "liked" && position == "" {
		for _, ad := range adsOutsideFeed {
			if chosen.keeps(ad, needle) {
				page.Posts = append(page.Posts, ad)
			}
		}
		a.forReader(r, page.Posts)
	}

	held, err := a.feedScan(r, &page, chosen, needle, position)
	if err != nil {
		return feedHomePage{}, err
	}
	if !held && position == "" {
		return a.feedLive(r, page, chosen, needle)
	}
	if page.Next != "" {
		page.Continue = feedHref(theme, filter, query, untrusted, page.Next)
	}
	page.Failed = feedAllFailed(page.Coverage)
	page.Trust = a.feedTrustNotice(page.Layout.NetworkConfigured)
	return page, nil
}

// feedScan reads Recent from position until a page of posts passes the
// filter, the search and the untrusted toggle, or the held records run out.
// It reports whether Recent held any record at all for this reader.
func (a *App) feedScan(r *http.Request, page *feedHomePage, chosen feedFilter, needle, position string) (bool, error) {
	held := false
	for call := 0; call < feedScanCalls; call++ {
		recent, err := a.records().Recent(r.Context(), r, position, max(feedChunk-len(page.Posts), 1))
		if errors.Is(err, errRecordPosition) {
			return false, feedErrInvalid
		}
		if err != nil {
			return false, err
		}
		if call == 0 {
			page.Coverage = recent.Coverage
		}
		held = held || len(recent.Posts) > 0 || recent.Next != ""
		a.forReader(r, recent.Posts)
		for _, p := range recent.Posts {
			if !chosen.keeps(p, needle) {
				continue
			}
			if p.Class == string(sdk.ClassValidUntrusted) && !page.Untrusted {
				page.Hidden++
				continue
			}
			page.Posts = append(page.Posts, p)
		}
		position = recent.Next
		page.Next = recent.Next
		if recent.Next == "" || len(page.Posts) >= feedChunk {
			break
		}
	}
	return held, nil
}

// feedLive answers the first page while this server holds no record for the
// reader yet, as before the sync has run. It reads the providers live, which
// keeps their valid records for Recent; in dev mode an empty provider gives
// marked demo data. Records the live read could not keep, such as ones not
// verifiable without a trust root, are paged by a count and an anchor.
func (a *App) feedLive(r *http.Request, page feedHomePage, chosen feedFilter, needle string) (feedHomePage, error) {
	live, err := a.records().Posts(r.Context(), r)
	if err != nil {
		return feedHomePage{}, err
	}
	filter, query := page.Layout.Filter, page.Layout.Query
	page.Layout = a.layout(r, "home", "Home")
	page.Layout.Filter, page.Layout.Query = filter, query
	held, err := a.feedScan(r, &page, chosen, needle, "")
	if err != nil {
		return feedHomePage{}, err
	}
	page.Coverage = a.feedLiveCoverage(page.Coverage)
	if held {
		if page.Next != "" {
			page.Continue = feedHref(page.Layout.Theme, filter, query, page.Untrusted, page.Next)
		}
	} else {
		shown := make([]post, 0, len(live))
		for _, p := range live {
			if p.Class == string(sdk.ClassValidUntrusted) && !page.Untrusted {
				page.Hidden++
				continue
			}
			shown = append(shown, p)
		}
		window, ok := a.feedWindowOf(r, false, func() ([]post, error) { return shown, nil })
		if !ok {
			return feedHomePage{}, feedErrInvalid
		}
		page.Posts = window.Posts
		page.Legacy, page.HasMore, page.NextLimit, page.Anchor, page.Loaded = true, window.HasMore, window.NextLimit, window.Anchor, window.Loaded
	}
	page.Failed = feedAllFailed(page.Coverage)
	page.Trust = a.feedTrustNotice(page.Layout.NetworkConfigured)
	return page, nil
}

// feedLiveCoverage replaces "not synced yet" for each provider with what it
// answered to the live read that just ran. Mirrors and the vault keep their status.
func (a *App) feedLiveCoverage(c Coverage) Coverage {
	c.Note = "Your sources were read just now; nothing from them was held on this server before"
	sources := slices.Clone(c.Sources)
	a.networkMu.RLock()
	defer a.networkMu.RUnlock()
	for i, source := range sources {
		state, ok := a.providerState[source.Endpoint]
		if source.Kind != sdk.SourceProvider || !ok {
			continue
		}
		sources[i].OK = state == "reachable"
		sources[i].Text = "read just now: " + state
	}
	c.Sources = sources
	return c
}

// feedWindow serves a count-and-anchor window of the reader's posts.
func (a *App) feedWindow(r *http.Request, more bool) (feedHomePage, error) {
	window, ok := a.readFeedRequest(r, more)
	if !ok {
		return feedHomePage{}, feedErrInvalid
	}
	return feedHomePage{Layout: window.Layout, Posts: window.Posts, Legacy: true, HasMore: window.HasMore, NextLimit: window.NextLimit, Anchor: window.Anchor, Loaded: window.Loaded}, nil
}

// feedAllFailed is true when every source the read consulted failed.
func feedAllFailed(c Coverage) bool {
	if len(c.Sources) == 0 {
		return false
	}
	for _, source := range c.Sources {
		if source.OK {
			return false
		}
	}
	return true
}

// feedTrustNotice says what a stale or missing trust root means for the badges.
// Demo data has no settlement to check, so the notice is for networked reads only.
func (a *App) feedTrustNotice(networked bool) string {
	if !networked {
		return ""
	}
	_, status := a.trustRoot()
	at := "an unknown time"
	if !status.FetchedAt.IsZero() {
		at = status.FetchedAt.UTC().Format("2006-01-02 15:04 UTC")
	}
	switch status.State {
	case TrustStale:
		return "Settlement cannot be checked right now. Badges show what was known at " + at + "."
	case TrustUnavailable:
		return "Settlement cannot be checked; records are shown as not verifiable."
	}
	return ""
}

// feedHref builds a link to Home that keeps the filter, search and toggle.
func feedHref(theme, filter, query string, untrusted bool, position string) string {
	values := url.Values{}
	values.Set("filter", filter)
	if theme != "" {
		values.Set("theme", theme)
	}
	if query != "" {
		values.Set("q", query)
	}
	if untrusted {
		values.Set("untrusted", "1")
	}
	if position != "" {
		values.Set("position", position)
	}
	return "/?" + values.Encode()
}

func (a *App) feedHandler(w http.ResponseWriter, r *http.Request) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err == nil && values.Has("play") {
		id := values.Get("play")
		p, ok := a.requestPost(r, id)
		if !ok || (p.Art != "video" && p.Art != "tech-video") {
			http.Error(w, "video post not found", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		a.fragment(w, r, "card-video-player-response", p)
		return
	}
	page, ok := a.feedServe(w, r, false)
	if !ok {
		return
	}
	a.fragment(w, r, "feed-main", page)
}

func (a *App) searchHandler(w http.ResponseWriter, r *http.Request) {
	page, ok := a.feedServe(w, r, false)
	if !ok {
		return
	}
	a.fragment(w, r, "feed-search-response", page)
}

// moreHandler returns only the next batch, leaving earlier posts in place.
func (a *App) moreHandler(w http.ResponseWriter, r *http.Request) {
	page, ok := a.feedServe(w, r, true)
	if !ok {
		return
	}
	a.fragment(w, r, "feed-more-response", page)
}

// feedServe reads one Home page and writes the error response when the request is not valid.
func (a *App) feedServe(w http.ResponseWriter, r *http.Request, more bool) (feedHomePage, bool) {
	page, err := a.feedRead(r, more)
	switch {
	case errors.Is(err, feedErrInvalid):
		http.Error(w, "invalid feed filter or search", http.StatusBadRequest)
		return feedHomePage{}, false
	case err != nil:
		a.logger().Printf("client feed: %v", err)
		http.Error(w, "the records of your sources could not be read", http.StatusInternalServerError)
		return feedHomePage{}, false
	}
	page.Order = a.fyoOrder(r, page.Posts)
	return page, true
}

func (a *App) homeHandler(w http.ResponseWriter, r *http.Request) {
	page, ok := a.feedServe(w, r, false)
	if !ok {
		return
	}
	rememberTheme(w, r)
	a.page(w, r, "feed-page", page)
}
