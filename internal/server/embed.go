package server

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"
)

func init() { routeGroups = append(routeGroups, registerEmbed) }

func registerEmbed(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /posts/{id}", a.postPageHandler)
	mux.HandleFunc("GET /embed/{id}", a.embedPageHandler)
	mux.HandleFunc("GET /share", a.sharePageHandler)
	mux.HandleFunc("GET /share/fragment", a.shareFragmentHandler)
}

type shareDetails struct {
	Layout                  Layout
	Post                    post
	PostURL, EmbedURL, Code string
	Thread                  thread
	// Standalone renders the share panel on its own page, where Close is a plain link back to the post.
	Standalone bool
}

var staticPostsByID = func() map[string]post {
	byID := make(map[string]post, len(staticDemoPosts))
	for _, p := range staticDemoPosts {
		byID[p.ID] = p
	}
	for _, p := range adsOutsideFeed {
		byID[p.ID] = p
	}
	return byID
}()

func postByID(id string) (post, bool) {
	p, ok := staticPostsByID[id]
	return p, ok
}

// publicOrigin is configured by the operator rather than inferred from Host.
func (a *App) publicOrigin() (string, error) {
	devMode := a.config.Dev
	origin := a.config.PublicOrigin
	if origin == "" {
		origin = "https://127.0.0.1:8443"
		if devMode {
			origin = "http://localhost:8443"
		}
	}
	u, err := url.Parse(origin)
	if err != nil || !allowedOriginScheme(u, devMode) || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return "", fmt.Errorf("invalid public origin")
	}
	return strings.TrimSuffix(u.String(), "/"), nil
}

// allowedOriginScheme requires HTTPS, except for a loopback origin in dev mode.
func allowedOriginScheme(u *url.URL, devMode bool) bool {
	if u.Scheme == "https" {
		return true
	}
	host := u.Hostname()
	loopback := host == "localhost" || host == "127.0.0.1" || host == "::1"
	return devMode && u.Scheme == "http" && loopback
}

func (a *App) shareData(r *http.Request, id string) (shareDetails, int) {
	p, ok := a.requestPost(r, id)
	if !ok {
		return shareDetails{}, http.StatusNotFound
	}
	origin, err := a.publicOrigin()
	if err != nil {
		return shareDetails{}, http.StatusServiceUnavailable
	}
	postURL := origin + "/posts/" + url.PathEscape(p.ID)
	embedURL := origin + "/embed/" + url.PathEscape(p.ID)
	code := fmt.Sprintf(`<iframe src="%s" title="ForYou post %s" width="480" height="480" loading="lazy"></iframe>`, html.EscapeString(embedURL), html.EscapeString(p.Receipt))
	return shareDetails{Layout: a.layout(r, "", ""), Post: p, PostURL: postURL, EmbedURL: embedURL, Code: code}, http.StatusOK
}

// writeDetailNotFound answers an unknown post with the app's shared not-found page, inside the shell.
func (a *App) writeDetailNotFound(w http.ResponseWriter, r *http.Request) {
	a.renderStatus(w, r, "layout-error-page", notFoundPage{Layout: a.layout(r, "", "Page not found")}, http.StatusNotFound)
}

// postThread reads the first page of a post's replies for rendering inside its page.
func (a *App) postThread(r *http.Request, id string) (thread, int) {
	threadRequest := r.Clone(r.Context())
	threadRequest.URL = &url.URL{Path: "/thread", RawQuery: url.Values{"id": {id}}.Encode()}
	t, status := a.readThread(threadRequest)
	t.Static = true
	return t, status
}

func (a *App) postPageHandler(w http.ResponseWriter, r *http.Request) {
	details, status := a.shareData(r, r.PathValue("id"))
	if status == http.StatusOK {
		details.Thread, status = a.postThread(r, details.Post.ID)
	}
	if status != http.StatusOK {
		if status == http.StatusNotFound {
			a.writeDetailNotFound(w, r)
			return
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	details.Layout = a.layout(r, "post", details.Post.Receipt)
	a.page(w, r, "post-page", details)
}

// embedTheme honors only an explicit query choice, so an embed otherwise follows the host reader's color scheme.
func embedTheme(r *http.Request) string {
	if theme := r.URL.Query().Get("theme"); theme == "light" || theme == "dark" {
		return theme
	}
	return ""
}

func (a *App) embedPageHandler(w http.ResponseWriter, r *http.Request) {
	details, status := a.shareData(r, r.PathValue("id"))
	if status != http.StatusOK && status != http.StatusNotFound {
		http.Error(w, http.StatusText(status), status)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src 'self'; media-src 'self'; base-uri 'none'; form-action 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if status == http.StatusNotFound {
		a.renderOutput(w, "embed-error-page", struct{ Layout Layout }{Layout: Layout{Theme: embedTheme(r)}}, http.StatusNotFound, "private, no-store")
		return
	}
	details.Layout.Theme = embedTheme(r)
	a.renderOutput(w, "embed-page", details, http.StatusOK, "private, no-store")
}

func (a *App) sharePageHandler(w http.ResponseWriter, r *http.Request) {
	details, status := a.shareData(r, r.URL.Query().Get("id"))
	if status != http.StatusOK {
		if status == http.StatusNotFound {
			a.writeDetailNotFound(w, r)
			return
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	details.Standalone = true
	details.Layout = a.layout(r, "share", "Embed post")
	a.page(w, r, "share-page", details)
}

func (a *App) shareFragmentHandler(w http.ResponseWriter, r *http.Request) {
	details, status := a.shareData(r, r.URL.Query().Get("id"))
	if status != http.StatusOK {
		http.Error(w, http.StatusText(status), status)
		return
	}
	if r.URL.Query().Get("view") == "closed" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte("<span></span>"))
		return
	}
	a.fragment(w, r, "share-panel", details)
}
