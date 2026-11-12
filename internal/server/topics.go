package server

import (
	"fmt"
	"net/http"
	"slices"
	"strings"
)

func init() { routeGroups = append(routeGroups, registerTopics) }

func registerTopics(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /topics", a.topicsIndexHandler)
	mux.HandleFunc("GET /topics/rail", a.topicsRailHandler)
	mux.HandleFunc("GET /topics/{topic}", a.topicsPageHandler)
}

const (
	topicsListLimit = 50
	topicsRailLimit = 8
	topicsSlugMax   = 32
)

// topicsLink is one rubric topic of a post, linked to its topic page.
type topicsLink struct{ Slug, Label, Href string }

// topicsCount is how many of the fetched posts carry one topic.
type topicsCount struct {
	Slug, Label, Href string
	Posts             int
}

// TopicLinks lists the rubric topics of a post, in rubric order, as links to their topic pages.
func (r rating) TopicLinks() []topicsLink {
	var links []topicsLink
	for _, slug := range strings.Split(r.Topic, ", ") {
		if topicsSlugValid(slug) {
			links = append(links, topicsLink{Slug: slug, Label: topicsLabel(slug), Href: "/topics/" + slug})
		}
	}
	return links
}

func topicsSlugValid(s string) bool {
	if len(s) == 0 || len(s) > topicsSlugMax {
		return false
	}
	for i := range len(s) {
		if s[i] < 'a' || s[i] > 'z' {
			return false
		}
	}
	return true
}

func topicsLabel(slug string) string {
	return strings.ToUpper(slug[:1]) + slug[1:]
}

// topicsCounts counts the posts that carry each topic, most posts first. A post counts once per topic.
func topicsCounts(posts []post) []topicsCount {
	byTopic := map[string]*topicsCount{}
	for _, p := range posts {
		seen := map[string]bool{}
		for _, link := range p.Rating.TopicLinks() {
			if seen[link.Slug] {
				continue
			}
			seen[link.Slug] = true
			count := byTopic[link.Slug]
			if count == nil {
				count = &topicsCount{Slug: link.Slug, Label: link.Label, Href: link.Href}
				byTopic[link.Slug] = count
			}
			count.Posts++
		}
	}
	counts := make([]topicsCount, 0, len(byTopic))
	for _, count := range byTopic {
		counts = append(counts, *count)
	}
	slices.SortFunc(counts, func(a, b topicsCount) int {
		if a.Posts != b.Posts {
			return b.Posts - a.Posts
		}
		return strings.Compare(a.Slug, b.Slug)
	})
	return counts
}

// topicsMatching returns the posts carrying the topic, in the order the records were read.
func topicsMatching(posts []post, slug string) []post {
	var matches []post
	for _, p := range posts {
		if slices.ContainsFunc(p.Rating.TopicLinks(), func(link topicsLink) bool { return link.Slug == slug }) {
			matches = append(matches, p)
		}
	}
	return matches
}

func topicsScope(fetched int) string {
	if fetched == 1 {
		return "the one post this client fetched"
	}
	return fmt.Sprintf("the %d posts this client fetched", fetched)
}

func topicsPageSummary(matches, fetched int, failed bool) string {
	switch {
	case failed || fetched == 0 || matches == 0:
		return "Posts this client fetched with this topic."
	case fetched == 1 && matches == 1:
		return "The one post this client fetched has this topic."
	case matches == 1:
		return fmt.Sprintf("1 of %s has this topic.", topicsScope(fetched))
	}
	return fmt.Sprintf("%d of %s have this topic.", matches, topicsScope(fetched))
}

func topicsIndexSummary(fetched int, failed bool) string {
	scope := "the posts this client fetched"
	if !failed && fetched > 0 {
		scope = topicsScope(fetched)
	}
	return "Topics the rubric found in " + scope + ". Counts are of those posts only, not of the network."
}

func topicsStatus(err error) int {
	if err != nil {
		return http.StatusBadGateway
	}
	return http.StatusOK
}

type topicsPage struct {
	Layout         Layout
	Label, Slug    string
	Summary, Scope string
	Overflow       string
	Posts          []post
	Fetched        int
	Failed         bool
}

type topicsIndex struct {
	Layout  Layout
	Summary string
	Scope   string
	Counts  []topicsCount
	Fetched int
	Failed  bool
}

// topicsPosts reads the posts this client fetched, with this reader's filters applied.
func (a *App) topicsPosts(r *http.Request) ([]post, error) {
	posts, err := a.records().Posts(r.Context(), r)
	if err != nil {
		return nil, err
	}
	a.forReader(r, posts)
	return posts, nil
}

func (a *App) topicsIndexHandler(w http.ResponseWriter, r *http.Request) {
	rememberTheme(w, r)
	posts, err := a.topicsPosts(r)
	page := topicsIndex{
		Layout:  a.layout(r, "topics", "Topics"),
		Summary: topicsIndexSummary(len(posts), err != nil),
		Scope:   topicsScope(len(posts)),
		Counts:  topicsCounts(posts),
		Fetched: len(posts),
		Failed:  err != nil,
	}
	w.Header().Set("Cache-Control", "private, no-store")
	a.renderStatus(w, r, "topics-index-page", page, topicsStatus(err))
}

func (a *App) topicsPageHandler(w http.ResponseWriter, r *http.Request) {
	slug := r.PathValue("topic")
	if !topicsSlugValid(slug) {
		a.writeDetailNotFound(w, r)
		return
	}
	rememberTheme(w, r)
	posts, err := a.topicsPosts(r)
	label := topicsLabel(slug)
	matches := topicsMatching(posts, slug)
	page := topicsPage{
		Layout:  a.layout(r, "topics", label+" · Topics"),
		Label:   label,
		Slug:    slug,
		Summary: topicsPageSummary(len(matches), len(posts), err != nil),
		Scope:   topicsScope(len(posts)),
		Posts:   matches,
		Fetched: len(posts),
		Failed:  err != nil,
	}
	if len(matches) > topicsListLimit {
		page.Posts = matches[:topicsListLimit]
		page.Overflow = fmt.Sprintf("Showing the first %d of %d fetched posts with this topic.", topicsListLimit, len(matches))
	}
	w.Header().Set("Cache-Control", "private, no-store")
	a.renderStatus(w, r, "topics-page", page, topicsStatus(err))
}

func (a *App) topicsRailHandler(w http.ResponseWriter, r *http.Request) {
	posts, err := a.topicsPosts(r)
	counts := topicsCounts(posts)
	page := topicsIndex{
		Scope:   topicsScope(len(posts)),
		Counts:  counts[:min(len(counts), topicsRailLimit)],
		Fetched: len(posts),
		Failed:  err != nil,
	}
	a.fragment(w, r, "topics-rail-list", page)
}
