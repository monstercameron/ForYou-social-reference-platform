package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/wire"
)

func init() { routeGroups = append(routeGroups, registerExplore) }

func registerExplore(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /explore", a.exploreHandler)
	mux.HandleFunc("GET /explore/more", a.exploreMoreHandler)
}

const (
	expPageSize   = 5
	expMaxQuery   = 120
	expMaxShown   = 100000
	expScanRounds = 10
	// expCountPages bounds the walk that counts held records for the coverage line.
	expCountPages = 50
	expLookupWait = 5 * time.Second
)

var expKinds = []string{"all", "text", "images", "video", "posts", "replies"}

var errExplorePosition = errors.New("explore: list position is not valid")

// explorePage is the Explore view. Search and author lookups hold the complete
// matching list and page through it by offset; browsing pages through the
// record source's own opaque positions.
type explorePage struct {
	Layout      Layout
	Query, Kind string
	From, Until string
	Author      string
	Posts       []post
	HasMore     bool
	Next        string
	Loaded      int
	Coverage    []string
	Bounds      string
	// Error is the message of a rejected request; ErrorField names the field
	// it belongs to ("q", "author", "dates" or "position").
	Error, ErrorField string
	AuthorHeader      *expAuthor
}

// expAuthor is the header of an author lookup.
type expAuthor struct {
	DID, Short, Successor string
	Avatar                post
}

// exploreRequest is the validated reader request for one Explore page.
type exploreRequest struct {
	query, kind, author string
	from, until         string
	fromDay, untilDay   time.Time
	position            string
	shown               int
}

// parseExplore validates the query string. It returns the field and message of
// the first error, both empty when the request is valid.
func parseExplore(rawQuery string) (exploreRequest, string, string) {
	req := exploreRequest{kind: "all"}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return req, "q", "The address is not a valid search."
	}
	req.query = strings.TrimSpace(values.Get("q"))
	if !utf8.ValidString(req.query) || utf8.RuneCountInString(req.query) > expMaxQuery {
		return req, "q", "Search text can be at most 120 characters."
	}
	if kind := values.Get("kind"); kind != "" {
		req.kind = kind
	}
	if !slices.Contains(expKinds, req.kind) {
		return req, "q", "Choose a kind from the list."
	}
	req.author = strings.TrimSpace(values.Get("author"))
	if req.author != "" {
		if _, err := keys.ParseDID(req.author); err != nil {
			return req, "author", "Enter a full did:key; names and @handles are not identities."
		}
	}
	req.from, req.until = values.Get("from"), values.Get("until")
	if req.from != "" {
		if req.fromDay, err = time.Parse(time.DateOnly, req.from); err != nil {
			return req, "dates", "Enter From as a calendar date."
		}
	}
	if req.until != "" {
		if req.untilDay, err = time.Parse(time.DateOnly, req.until); err != nil {
			return req, "dates", "Enter Until as a calendar date."
		}
	}
	if !req.fromDay.IsZero() && !req.untilDay.IsZero() && req.fromDay.After(req.untilDay) {
		return req, "dates", "From must be on or before Until."
	}
	req.position = values.Get("position")
	if raw := values.Get("shown"); raw != "" {
		shown, err := strconv.Atoi(raw)
		if err != nil || shown < 0 || shown > expMaxShown {
			return req, "position", "This list position is not valid."
		}
		req.shown = shown
	}
	return req, "", ""
}

// keeps reports whether a post belongs to the chosen kind and date range.
func (req exploreRequest) keeps(p post) bool {
	switch req.kind {
	case "text", "images", "video":
		if p.Kind != req.kind {
			return false
		}
	case "posts":
		if p.IsReply {
			return false
		}
	case "replies":
		if !p.IsReply {
			return false
		}
	}
	return req.inDates(p)
}

// inDates reports whether a post's signed timestamp falls within the requested
// UTC calendar days. A post without a signed timestamp never matches a date range.
func (req exploreRequest) inDates(p post) bool {
	if req.fromDay.IsZero() && req.untilDay.IsZero() {
		return true
	}
	when, err := wire.ParseTime(p.Timestamp)
	if err != nil {
		return false
	}
	if !req.fromDay.IsZero() && when.Before(req.fromDay) {
		return false
	}
	return req.untilDay.IsZero() || when.Before(req.untilDay.AddDate(0, 0, 1))
}

func (req exploreRequest) selected(posts []post) []post {
	kept := make([]post, 0, len(posts))
	for _, p := range posts {
		if req.keeps(p) {
			kept = append(kept, p)
		}
	}
	return kept
}

// window returns one page of a complete list, starting at the offset the position names.
func (req exploreRequest) window(all []post) ([]post, string, error) {
	offset := 0
	if req.position != "" {
		parsed, err := strconv.Atoi(req.position)
		if err != nil || parsed < 0 || parsed > len(all) {
			return nil, "", errExplorePosition
		}
		offset = parsed
	}
	end := min(offset+expPageSize, len(all))
	next := ""
	if end < len(all) {
		next = strconv.Itoa(end)
	}
	return all[offset:end], next, nil
}

// expBounds states the date range as the explicit UTC days the server applies.
func expBounds(req exploreRequest) string {
	if req.from == "" && req.until == "" {
		return ""
	}
	from, until := req.from, req.until
	if from == "" {
		from = "the first record"
	}
	if until == "" {
		until = "the latest record"
	}
	return fmt.Sprintf("UTC %s to %s", from, until)
}

// exploreRead builds the page for one request and the HTTP status it deserves.
// The author header and its key succession are looked up on the first page
// only, so incremental loading never repeats them.
func (a *App) exploreRead(r *http.Request, more bool) (explorePage, int) {
	page := explorePage{Layout: a.layout(r, "explore", "Explore")}
	req, field, message := parseExplore(r.URL.RawQuery)
	page.Query, page.Kind, page.Author = req.query, req.kind, req.author
	page.From, page.Until = req.from, req.until
	page.Bounds = expBounds(req)
	page.Loaded = req.shown
	if message != "" {
		page.Error, page.ErrorField = message, field
		return page, http.StatusBadRequest
	}
	var posts []post
	var next string
	var err error
	switch {
	case req.author != "":
		found, lookupErr := a.records().ByAuthor(r.Context(), r, req.author)
		if lookupErr != nil {
			page.Error = "Records for this key could not be read."
			return page, http.StatusInternalServerError
		}
		page.Coverage = found.Coverage.Lines()
		posts, next, err = req.window(req.selected(found.Posts))
		if !more {
			page.AuthorHeader = a.expAuthorHeader(r, req.author)
		}
	case req.query != "":
		posts, next, page.Coverage, err = a.expSearch(r, req)
	default:
		var coverage Coverage
		posts, next, coverage, err = a.expBrowse(r, req)
		page.Coverage = coverage.Lines()
	}
	if err != nil {
		if req.position != "" {
			page.Error, page.ErrorField = "This list position is not valid.", "position"
			return page, http.StatusBadRequest
		}
		page.Error = "Fetched records could not be read."
		return page, http.StatusInternalServerError
	}
	a.forReader(r, posts)
	page.Posts = posts
	page.Next = next
	page.HasMore = next != ""
	page.Loaded += len(posts)
	return page, http.StatusOK
}

// expSearch matches the query over the records this server holds and states
// how many held records were searched and from which sources.
func (a *App) expSearch(r *http.Request, req exploreRequest) ([]post, string, []string, error) {
	found, err := a.records().Search(r.Context(), r, req.query, nrMaxLimit)
	if err != nil {
		return nil, "", nil, err
	}
	held, more, coverage, err := a.expHeld(r)
	if err != nil {
		return nil, "", nil, err
	}
	labels := make([]string, 0, len(coverage.Sources))
	for _, source := range coverage.Sources {
		labels = append(labels, source.Label)
	}
	from := "your sources"
	if len(labels) > 0 {
		from = strings.Join(labels, ", ")
	}
	count := strconv.Itoa(held)
	if more {
		count = "more than " + count
	}
	lines := []string{fmt.Sprintf("Searching %s records this server has fetched from %s.", count, from)}
	if len(found.Posts) == nrMaxLimit {
		lines = append(lines, fmt.Sprintf("Showing the newest %d matches only.", nrMaxLimit))
	}
	coverage.Note = ""
	lines = append(lines, coverage.Lines()...)
	posts, next, err := req.window(req.selected(found.Posts))
	return posts, next, lines, err
}

// expHeld counts the records this server holds for the reader, at most
// expCountPages pages of them; more is true when the walk stopped early.
func (a *App) expHeld(r *http.Request) (int, bool, Coverage, error) {
	position := ""
	held := 0
	var coverage Coverage
	for round := range expCountPages {
		found, err := a.records().Recent(r.Context(), r, position, nrMaxLimit)
		if err != nil {
			return 0, false, Coverage{}, err
		}
		if round == 0 {
			coverage = found.Coverage
		}
		held += len(found.Posts)
		if found.Next == "" {
			return held, false, coverage, nil
		}
		position = found.Next
	}
	return held, true, coverage, nil
}

// expBrowse walks the newest held records one source page at a time until a
// page holds a match or the scan budget runs out. Positions stay the source's own.
func (a *App) expBrowse(r *http.Request, req exploreRequest) ([]post, string, Coverage, error) {
	position := req.position
	var posts []post
	var next string
	var coverage Coverage
	for range expScanRounds {
		found, err := a.records().Recent(r.Context(), r, position, nrDefaultLimit)
		if err != nil {
			return nil, "", Coverage{}, err
		}
		coverage = found.Coverage
		posts = req.selected(found.Posts)
		next = found.Next
		position = found.Next
		if len(posts) > 0 || next == "" {
			break
		}
	}
	return posts, next, coverage, nil
}

// expAuthorHeader names the key and, when the first selected provider holds a
// succession the old key signed, says so. A provider that fails or does not
// answer within the deadline only leaves that line out.
func (a *App) expAuthorHeader(r *http.Request, did string) *expAuthor {
	header := &expAuthor{DID: did, Short: shortDID(did), Avatar: post{AuthorID: did, Color: authorColor(did)}}
	sources := a.nrSourcesFor(requestProfile(r))
	first := slices.IndexFunc(sources, func(src nrSource) bool { return src.kind == sdk.SourceProvider })
	if first < 0 {
		return header
	}
	client, err := a.providerClient(sources[first].endpoint)
	if err != nil {
		return header
	}
	ctx, cancel := context.WithTimeout(r.Context(), expLookupWait)
	defer cancel()
	items, _, _, err := client.Successions(ctx, did)
	if err != nil {
		return header
	}
	if view := sdk.ResolveAuthorSuccession(did, items); view.AssertedByOld && view.NewID != "" {
		header.Successor = "This key names a successor: " + view.NewID + " (asserted by the old key)"
	}
	return header
}

// exploreHandler renders Explore with search, author lookup and the first page of results.
func (a *App) exploreHandler(w http.ResponseWriter, r *http.Request) {
	page, status := a.exploreRead(r, false)
	rememberTheme(w, r)
	w.Header().Set("Cache-Control", "private, no-store")
	if status != http.StatusOK {
		a.renderStatus(w, r, "explore-page", page, status)
		return
	}
	a.page(w, r, "explore-page", page)
}

// exploreMoreHandler returns the next page of results and leaves earlier cards in place.
func (a *App) exploreMoreHandler(w http.ResponseWriter, r *http.Request) {
	page, status := a.exploreRead(r, true)
	if status != http.StatusOK {
		a.renderStatus(w, r, "explore-more-error", page, status)
		return
	}
	a.fragment(w, r, "explore-more-response", page)
}
