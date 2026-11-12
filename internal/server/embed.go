package server

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"strings"

	"foryou.local/foryou/internal/names"
)

func init() { routeGroups = append(routeGroups, registerEmbed) }

func registerEmbed(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /embed/{id}", a.embedPageHandler)
	mux.HandleFunc("GET /share", a.sharePageHandler)
	mux.HandleFunc("GET /share/fragment", a.shareFragmentHandler)
}

// embedContentSecurityPolicy keeps an embed inert: it runs no script, loads no media and submits no form.
const embedContentSecurityPolicy = "default-src 'none'; style-src 'self'; img-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors *"

type shareDetails struct {
	Layout                  Layout
	Post                    post
	PostURL, EmbedURL, Code string
	// Author is the short author label of the shared post: its signed name, else its key fingerprint.
	Author string
	// NoOrigin is set when the client has no valid public address to put in a share link.
	NoOrigin bool
	// Theme is the color scheme the embed code asks for: light, dark, or empty to match the reader.
	Theme  string
	Thread thread
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

// publicOrigin is configured by the operator rather than inferred from Host, and sharing has no fallback.
func (a *App) publicOrigin() (string, error) {
	devMode := a.config.Dev
	origin := a.config.PublicOrigin
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

// shareData reads a post for sharing. An ID that no source holds, including one that is not a
// canonical message ID, is not found. A client without a valid public origin still renders the panel, without links or code.
func (a *App) shareData(r *http.Request, id string) (shareDetails, int) {
	p, ok := a.requestPost(r, id)
	if !ok {
		return shareDetails{}, http.StatusNotFound
	}
	details := shareDetails{Layout: a.layout(r, "", ""), Post: p, Author: embAuthor(p), Theme: embedTheme(r)}
	origin, err := a.publicOrigin()
	if err != nil {
		details.NoOrigin = true
		return details, http.StatusOK
	}
	details.PostURL = origin + "/posts/" + url.PathEscape(p.ID)
	details.EmbedURL = origin + "/embed/" + url.PathEscape(p.ID)
	if details.Theme != "" {
		details.EmbedURL += "?theme=" + details.Theme
	}
	details.Code = fmt.Sprintf(`<iframe src="%s" title="ForYou post by %s" width="480" height="480" loading="lazy" referrerpolicy="no-referrer" sandbox="allow-popups allow-popups-to-escape-sandbox"></iframe>`, html.EscapeString(details.EmbedURL), html.EscapeString(details.Author))
	return details, http.StatusOK
}

// embAuthor names the author of a post briefly: the signed name, a demo author's name, or the
// fingerprint of the author key. The receipt identifies the submission, not the author, and is the last resort.
func embAuthor(p post) string {
	switch {
	case p.SignedName != "":
		return p.SignedName
	case p.Name != "":
		return p.Name
	case p.AuthorID != "":
		return names.Fingerprint(p.AuthorID)
	}
	return p.Receipt
}

// writeDetailNotFound answers an unknown post with the app's shared not-found page, inside the shell.
func (a *App) writeDetailNotFound(w http.ResponseWriter, r *http.Request) {
	a.renderStatus(w, r, "layout-error-page", notFoundPage{Layout: a.layout(r, "", "Page not found")}, http.StatusNotFound)
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
	w.Header().Set("Content-Security-Policy", embedContentSecurityPolicy)
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
