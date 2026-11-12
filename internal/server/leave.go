package server

import (
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"foryou.local/foryou/internal/content"
)

func init() { routeGroups = append(routeGroups, registerLeave) }

func registerLeave(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /leave", a.leaveHandler)
	mux.HandleFunc("GET /ref", a.refHandler)
	mux.HandleFunc("GET /ref/fragment", a.refFragmentHandler)
}

// mdRecord returns a post by ID from the record source, where the reader's
// local copy and providers answer.
func (a *App) mdRecord(r *http.Request, id string) (post, bool) {
	if id == "" {
		return post{}, false
	}
	p, _, ok, err := a.records().Get(r.Context(), r, id)
	return p, err == nil && ok
}

type mdLeavePage struct {
	Layout   Layout
	Back     string
	Label    string
	URL      string
	Host     string
	Port     string
	Mismatch bool
}

// leaveHandler shows where an external link goes before the browser leaves.
// It never fetches, previews or resolves the destination.
func (a *App) leaveHandler(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	p, ok := a.mdRecord(r, query.Get("post"))
	if !ok {
		a.fail(w, r, http.StatusNotFound, "That post is not available from your sources.")
		return
	}
	rendered, err := mdRender(p)
	external := mdExternals(rendered.Links)
	n, convErr := strconv.Atoi(query.Get("link"))
	if err != nil || convErr != nil || n < 0 || n >= len(external) {
		a.fail(w, r, http.StatusNotFound, "That link is not in this post.")
		return
	}
	link := external[n]
	destination, err := url.Parse(link.URL)
	if err != nil {
		a.fail(w, r, http.StatusNotFound, "That link is not in this post.")
		return
	}
	port := destination.Port()
	if port == "443" {
		port = ""
	}
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'")
	a.page(w, r, "markdown-leave", mdLeavePage{
		Layout:   a.layout(r, "home", "Leaving ForYou"),
		Back:     "/posts/" + url.PathEscape(p.ID),
		Label:    link.Label,
		URL:      link.URL,
		Host:     destination.Hostname(),
		Port:     port,
		Mismatch: link.LabelMismatch,
	})
}

type mdRefPage struct {
	Layout     Layout
	PostID     string
	Label      string
	Target     string
	Author     bool
	Found      bool
	Posts      []post
	Coverage   []string
	ExploreURL string
	Back       string
	CloseURL   string
	RegionID   string
}

// mdRefView resolves a reference the reader asked for. A key target is only
// shown; a message target is looked up now, because the reader clicked.
func (a *App) mdRefView(w http.ResponseWriter, r *http.Request) (mdRefPage, bool) {
	query := r.URL.Query()
	p, ok := a.mdRecord(r, query.Get("post"))
	if !ok {
		a.fail(w, r, http.StatusNotFound, "That post is not available from your sources.")
		return mdRefPage{}, false
	}
	ref, declared := mdDeclaredRef(p, query.Get("ref"))
	if !declared {
		a.fail(w, r, http.StatusNotFound, "That reference is not in this post.")
		return mdRefPage{}, false
	}
	rendered, err := mdRender(p)
	if err != nil {
		a.fail(w, r, http.StatusNotFound, "That reference is not in this post.")
		return mdRefPage{}, false
	}
	page := mdRefPage{
		Layout:   a.layout(r, "home", "Reference"),
		PostID:   p.ID,
		Target:   ref.Target,
		Back:     "/posts/" + url.PathEscape(p.ID),
		RegionID: mdRegionID(p.ID, ref.ID),
	}
	for _, link := range rendered.Links {
		if link.Kind == content.LinkRef && link.RefID == ref.ID {
			page.Label = link.Label
		}
	}
	if strings.HasPrefix(ref.Target, "did:key:") {
		page.Author = true
		page.ExploreURL = "/explore?author=" + url.QueryEscape(ref.Target)
	} else if target, found, cov := a.mdLookup(r, ref.Target); found {
		page.Found = true
		page.Posts = target
	} else {
		page.Coverage = cov
	}
	return page, true
}

// mdLookup fetches a message target through the record source and reports what each source said.
func (a *App) mdLookup(r *http.Request, id string) ([]post, bool, []string) {
	p, cov, ok, err := a.records().Get(r.Context(), r, id)
	if err != nil || !ok {
		return nil, false, cov.Lines()
	}
	found := []post{p}
	a.forReader(r, found)
	return found, true, nil
}

func (a *App) refHandler(w http.ResponseWriter, r *http.Request) {
	page, ok := a.mdRefView(w, r)
	if !ok {
		return
	}
	a.page(w, r, "markdown-ref", page)
}

// refFragmentHandler fills the popover region of a reference on a card. The
// close control puts the open button back without looking the target up;
// without scripts the reference page is the fallback.
func (a *App) refFragmentHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("closed") == "1" {
		p, ok := a.mdRecord(r, r.URL.Query().Get("post"))
		ref, declared := mdDeclaredRef(p, r.URL.Query().Get("ref"))
		if !ok || !declared {
			a.fail(w, r, http.StatusNotFound, "That reference is not in this post.")
			return
		}
		a.fragment(w, r, "markdown-ref-closed", template.HTML(mdRefOpen(p.ID, ref)))
		return
	}
	page, ok := a.mdRefView(w, r)
	if !ok {
		return
	}
	query := "post=" + url.QueryEscape(page.PostID) + "&ref=" + url.QueryEscape(r.URL.Query().Get("ref"))
	page.CloseURL = "/ref/fragment?" + query + "&closed=1"
	a.fragment(w, r, "markdown-ref-fragment", page)
}
