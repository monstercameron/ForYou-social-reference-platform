package server

import (
	"net/http"
	"net/url"
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
}

// matchingPosts counts through the requested limit but retains only the window.
// One extra match determines whether another page exists.
func matchingPosts(filter, query, anchor string, offset, limit int, added []post, followed map[string]bool) ([]post, string, int, bool) {
	capacity := len(added) + len(staticDemoPosts)
	if capacity > limit-offset {
		capacity = limit - offset
	}
	filtered := make([]post, 0, capacity)
	needle := strings.ToLower(query)
	found := anchor == ""
	first := ""
	count := 0
	appendMatches := func(posts []post) bool {
		for _, p := range posts {
			matchesFilter := filter == "all" || filter == p.Kind || (filter == "notes" && !p.IsReply) || (filter == "replies" && p.IsReply) || (filter == "following" && followed[p.AuthorID])
			if !matchesFilter {
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
				return true
			}
			if count > offset {
				filtered = append(filtered, p)
			}
		}
		return false
	}
	if !appendMatches(added) {
		appendMatches(staticDemoPosts)
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
	if filter != "all" && filter != "notes" && filter != "replies" && filter != "text" && filter != "images" && filter != "video" && filter != "following" {
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
	var added []post
	if store := a.store; store != nil {
		added = store.list()
	}
	posts, first, count, found := matchingPosts(filter, query, anchor, offset, limit, added, followSet(r))
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
