package server

import (
	"net/http"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"foryou.local/foryou/internal/content"
	"foryou.local/foryou/internal/rank"
	"foryou.local/foryou/internal/wire"
)

func init() { routeGroups = append(routeGroups, registerFyoOrder) }

func registerFyoOrder(a *App, mux *http.ServeMux) {
	mux.HandleFunc("POST /feed/order", a.fyoOrderHandler)
	mux.HandleFunc("POST /feed/order/fragment", a.fyoOrderFragmentHandler)
}

const (
	fyoOrderCookie = "foryou_order"
	fyoForYou      = "foryou"
	fyoNewest      = "newest"
	fyoQueryLimit  = 120
)

// fyoView is the sort bar above the Home feed and what it says about the order.
type fyoView struct {
	Show, Active         bool
	Choice               string
	Likes, Dislikes      int
	Filter, Query, Theme string
	Note                 string
}

type fyoChoice struct {
	order, filter, query, theme string
}

func fyoField(form url.Values, name string) (string, bool) {
	switch values := form[name]; len(values) {
	case 0:
		return "", true
	case 1:
		return values[0], true
	}
	return "", false
}

func fyoReadChoice(form url.Values) (fyoChoice, bool) {
	orders := form["order"]
	if len(orders) != 1 || (orders[0] != fyoNewest && orders[0] != fyoForYou) {
		return fyoChoice{}, false
	}
	filter, ok := fyoField(form, "filter")
	if !ok || (filter != "" && !slices.Contains(feedFilterNames, filter)) {
		return fyoChoice{}, false
	}
	query, ok := fyoField(form, "q")
	query = strings.TrimSpace(query)
	if !ok || !utf8.ValidString(query) || utf8.RuneCountInString(query) > fyoQueryLimit {
		return fyoChoice{}, false
	}
	theme, ok := fyoField(form, "theme")
	if !ok || (theme != "" && theme != "light" && theme != "dark") {
		return fyoChoice{}, false
	}
	return fyoChoice{order: orders[0], filter: filter, query: query, theme: theme}, true
}

// fyoParams keeps only the feed parameters that differ from the defaults.
func fyoParams(c fyoChoice) url.Values {
	values := url.Values{}
	if c.filter != "" && c.filter != "all" {
		values.Set("filter", c.filter)
	}
	if c.query != "" {
		values.Set("q", c.query)
	}
	if c.theme != "" {
		values.Set("theme", c.theme)
	}
	return values
}

func fyoSetOrder(w http.ResponseWriter, r *http.Request, order string) {
	cookie := &http.Cookie{Name: fyoOrderCookie, Value: fyoForYou, Path: "/", MaxAge: 31536000, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode}
	if order != fyoForYou {
		cookie.MaxAge = -1
	}
	http.SetCookie(w, cookie)
}

// fyoChosen reads the reader's order; any value but the exact one means Newest.
func fyoChosen(r *http.Request) string {
	if c, err := r.Cookie(fyoOrderCookie); err == nil && c.Value == fyoForYou {
		return fyoForYou
	}
	return fyoNewest
}

func fyoLocation(c fyoChoice) string {
	values := fyoParams(c)
	if len(values) == 0 {
		return "/"
	}
	return "/?" + values.Encode()
}

// fyoFeedRequest is a GET of Home with the chosen order, sent back as if the reader had asked for it.
func fyoFeedRequest(r *http.Request, c fyoChoice) *http.Request {
	r2 := r.Clone(r.Context())
	r2.Method = http.MethodGet
	r2.URL = &url.URL{Path: "/", RawQuery: fyoParams(c).Encode()}
	r2.Header.Del("Cookie")
	for _, cookie := range r.Cookies() {
		if cookie.Name != fyoOrderCookie {
			r2.AddCookie(cookie)
		}
	}
	if c.order == fyoForYou {
		r2.AddCookie(&http.Cookie{Name: fyoOrderCookie, Value: fyoForYou})
	}
	return r2
}

func (a *App) fyoOrderHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := readActionForm(w, r)
	if !ok {
		return
	}
	choice, ok := fyoReadChoice(form)
	if !ok {
		http.Error(w, "Choose Newest or For you.", http.StatusBadRequest)
		return
	}
	fyoSetOrder(w, r, choice.order)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, fyoLocation(choice), http.StatusSeeOther)
}

func (a *App) fyoOrderFragmentHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := readActionForm(w, r)
	if !ok {
		return
	}
	choice, ok := fyoReadChoice(form)
	if !ok {
		http.Error(w, "Choose Newest or For you.", http.StatusBadRequest)
		return
	}
	fyoSetOrder(w, r, choice.order)
	r2 := fyoFeedRequest(r, choice)
	page, ok := a.feedServe(w, r2, false)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	a.fragment(w, r2, "feed-main", page)
}

// fyoOrder reorders the posts of one Home page, or of one batch, in place and
// says what the sort bar shows. Other pages, and the ads section, keep their order.
func (a *App) fyoOrder(r *http.Request, posts []post) fyoView {
	switch r.URL.Path {
	case "/", "/feed", "/feed/search", "/feed/more":
	default:
		return fyoView{}
	}
	query := r.URL.Query()
	filter := query.Get("filter")
	if filter == "" {
		filter = "all"
	}
	if filter == "ads" {
		return fyoView{}
	}
	view := fyoView{Show: true, Choice: fyoChosen(r), Filter: filter, Query: strings.TrimSpace(query.Get("q")), Theme: query.Get("theme")}
	if view.Choice == fyoNewest {
		return view
	}
	now := time.Now()
	votes, likes, dislikes := a.fyoVotes(r, now)
	model := rank.Train(votes, now, nil)
	view.Likes, view.Dislikes = likes, dislikes
	if !model.Active() {
		view.Note = "For you needs at least 10 likes and dislikes, with at least 2 of each, on posts this client can read. You have " + fyoCount(likes, "like") + " and " + fyoCount(dislikes, "dislike") + ". Showing newest first until then."
		return view
	}
	sets := make([][]string, len(posts))
	for i := range posts {
		sets[i] = fyoIndicators(posts[i])
	}
	fyoArrange(posts, model, sets)
	view.Active = true
	view.Note = "Ordered by this client from your " + fyoCount(likes, "like") + " and " + fyoCount(dislikes, "dislike") + " saved in this browser. Each batch is reordered as it loads; no post is hidden, and nothing is sent to providers or advertisers."
	return view
}

// fyoVotes turns the browser's recent likes and dislikes into votes at one moment, since the browser keeps no reliable vote times.
func (a *App) fyoVotes(r *http.Request, now time.Time) ([]rank.Vote, int, int) {
	var votes []rank.Vote
	var likes, dislikes int
	for _, entry := range feedbackEntries(r) {
		p, ok, err := a.records().Post(r.Context(), r, entry.ID)
		if err != nil || !ok {
			continue
		}
		like := entry.Vote == "up"
		votes = append(votes, rank.Vote{PostID: p.ID, AuthorID: p.AuthorID, Like: like, At: now, Indicators: fyoIndicators(p)})
		if like {
			likes++
		} else {
			dislikes++
		}
	}
	return votes, likes, dislikes
}

func fyoCount(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

var fyoIndicatorSets sync.Map

// fyoIndicators returns the taste indicators of a post, as the on-device model reads them.
func fyoIndicators(p post) []string {
	kind := "p"
	if p.IsReply {
		kind = "r"
	}
	key := p.format() + "\x00" + p.AuthorID + "\x00" + kind + "\x00" + p.Text
	if kept, ok := fyoIndicatorSets.Load(key); ok {
		return kept.([]string)
	}
	segments, err := content.Segment(wire.PostPayload{ContentFormat: p.format(), Text: p.Text})
	if err != nil {
		return nil
	}
	features := rank.Extract(rank.Post{ID: p.ID, AuthorID: p.AuthorID, Segments: segments, BillableBytes: len(p.Text), IsReply: p.IsReply}, time.Time{})
	indicators := make([]string, 0, len(features.Indicators()))
	for _, indicator := range features.Indicators() {
		if indicator != "key:" {
			indicators = append(indicators, indicator)
		}
	}
	fyoIndicatorSets.Store(key, indicators)
	return indicators
}

// fyoArrange sorts posts by the model's score, most likely first. Equal scores keep their order.
func fyoArrange(posts []post, model rank.Model, sets [][]string) {
	type ranked struct {
		post  post
		score rank.Score
	}
	items := make([]ranked, len(posts))
	for i, p := range posts {
		items[i] = ranked{post: p, score: model.Score(sets[i])}
	}
	sort.SliceStable(items, func(i, j int) bool { return items[i].score.Z > items[j].score.Z })
	for i, item := range items {
		if item.post.Why == "" {
			item.post.Why = fyoWhy(item.score)
		}
		posts[i] = item.post
	}
}

func fyoWhy(score rank.Score) string {
	if len(score.Top) == 0 {
		return "Why this post: nothing you liked or disliked matches it yet."
	}
	var likes, dislikes []string
	for _, weight := range score.Top {
		if weight.W > 0 {
			likes = append(likes, fyoPhrase(weight.Indicator))
		} else {
			dislikes = append(dislikes, fyoPhrase(weight.Indicator))
		}
	}
	var parts []string
	if len(likes) > 0 {
		parts = append(parts, "you tend to like "+fyoJoin(likes))
	}
	if len(dislikes) > 0 {
		parts = append(parts, "you tend to dislike "+fyoJoin(dislikes))
	}
	return "Why this post: " + strings.Join(parts, "; ") + "."
}

func fyoJoin(items []string) string {
	if len(items) == 1 {
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

var fyoPhrases = map[string]string{
	"len:tiny":        "very short posts",
	"len:short":       "short posts",
	"len:medium":      "medium-length posts",
	"len:long":        "long posts",
	"struct:rich":     "posts with lists or headings",
	"read:dense":      "dense writing",
	"media:image":     "posts with images",
	"media:video":     "posts with video",
	"media:noalt":     "images without descriptions",
	"media:none":      "posts without media",
	"link:any":        "posts with links",
	"link:many":       "posts with many links",
	"code:block":      "posts with code",
	"code:inline":     "posts with inline code",
	"prof:mild":       "mild swearing",
	"prof:strong":     "strong swearing",
	"harass":          "insulting posts",
	"spam:caps":       "posts in capitals",
	"spam:repeat":     "repetitive posts",
	"spam:punct":      "posts with runs of punctuation",
	"spam:emoji":      "emoji-heavy posts",
	"promo":           "promotional posts",
	"adult":           "posts labelled adult",
	"author:named":    "posts from named authors",
	"author:delegate": "posts signed by a delegate key",
	"thread:reply":    "replies",
	"thread:deep":     "replies deep in a thread",
	"thread:busy":     "posts with many replies",
	"thread:self":     "authors replying to themselves",
}

// fyoPhrase says an indicator in plain words for the Why line.
func fyoPhrase(indicator string) string {
	if phrase, ok := fyoPhrases[indicator]; ok {
		return phrase
	}
	switch {
	case strings.HasPrefix(indicator, "topic:"):
		return "posts about " + strings.TrimPrefix(indicator, "topic:")
	case strings.HasPrefix(indicator, "lang:"):
		return "posts in language " + strings.TrimPrefix(indicator, "lang:")
	case strings.HasPrefix(indicator, "script:"):
		return "posts in " + strings.TrimPrefix(indicator, "script:") + " script"
	case strings.HasPrefix(indicator, "key:"):
		return "posts by this author"
	}
	return "posts tagged " + indicator
}
