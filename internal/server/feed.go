package server

import (
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

func init() { routeGroups = append(routeGroups, registerFeed) }

func registerFeed(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /{$}", a.homeHandler)
	mux.HandleFunc("GET /feed", a.feedHandler)
	mux.HandleFunc("GET /feed/search", a.searchHandler)
	mux.HandleFunc("GET /feed/more", a.moreHandler)
}

type feedPage struct {
	Layout    Layout
	Posts     []post
	HasMore   bool
	NextLimit int
	Loaded    int
	Anchor    string
	Order     fyoView
}

// feedFilter is the reader's choice of posts: a kind of post, or their own
// follows, likes or the ads section.
type feedFilter struct {
	name     string
	followed map[string]bool
	liked    map[string]bool
}

var feedFilterNames = []string{"all", "notes", "replies", "text", "images", "video", "following", "liked", "ads"}

func (f feedFilter) matches(p post) bool {
	switch f.name {
	case "all", "ads":
		return true
	case "notes":
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

func (a *App) readFeedRequest(r *http.Request, more bool) (feedPage, bool) {
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
	available, err := a.records().Posts(r.Context(), r)
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
	page, ok := a.readFeedRequest(r, false)
	if !ok {
		http.Error(w, "invalid feed filter or search", http.StatusBadRequest)
		return
	}
	a.fragment(w, r, "feed-main", page)
}

func (a *App) searchHandler(w http.ResponseWriter, r *http.Request) {
	page, ok := a.readFeedRequest(r, false)
	if !ok {
		http.Error(w, "invalid feed filter or search", http.StatusBadRequest)
		return
	}
	a.fragment(w, r, "feed-search-response", page)
}

// moreHandler returns only the next batch, leaving earlier posts in place.
func (a *App) moreHandler(w http.ResponseWriter, r *http.Request) {
	page, ok := a.readFeedRequest(r, true)
	if !ok {
		http.Error(w, "invalid feed page", http.StatusBadRequest)
		return
	}
	a.fragment(w, r, "feed-more-response", page)
}

func (a *App) homeHandler(w http.ResponseWriter, r *http.Request) {
	page, ok := a.readFeedRequest(r, false)
	if !ok {
		http.Error(w, "invalid feed filter or search", http.StatusBadRequest)
		return
	}
	page.Layout = a.layout(r, "home", "Home")
	rememberTheme(w, r)
	a.page(w, r, "feed-page", page)
}
