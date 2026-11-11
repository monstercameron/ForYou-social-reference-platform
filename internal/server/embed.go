package server

import (
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"strings"
)

type shareDetails struct {
	Post                          post
	PostURL, EmbedURL, Code       string
	Theme, Section, Filter, Query string
	Thread                        thread
	// Standalone renders the share panel on its own page, where Close is a plain link back to the post.
	Standalone bool
}

var staticPostsByID = func() map[string]post {
	byID := make(map[string]post, len(staticDemoPosts))
	for _, p := range staticDemoPosts {
		byID[p.ID] = p
	}
	return byID
}()

func postByID(id string) (post, bool) {
	p, ok := staticPostsByID[id]
	return p, ok
}

// publicOrigin is configured by the operator rather than inferred from Host.
func publicOrigin() (string, error) {
	devMode := os.Getenv("FORYOU_DEV") == "1"
	origin := os.Getenv("FORYOU_PUBLIC_ORIGIN")
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

func shareData(r *http.Request, id string) (shareDetails, int) {
	p, ok := requestPost(r, id)
	if !ok {
		return shareDetails{}, http.StatusNotFound
	}
	origin, err := publicOrigin()
	if err != nil {
		return shareDetails{}, http.StatusServiceUnavailable
	}
	postURL := origin + "/posts/" + url.PathEscape(p.ID)
	embedURL := origin + "/embed/" + url.PathEscape(p.ID)
	code := fmt.Sprintf(`<iframe src="%s" title="ForYou post %s" width="480" height="480" loading="lazy"></iframe>`, html.EscapeString(embedURL), html.EscapeString(p.Receipt))
	return shareDetails{Post: p, PostURL: postURL, EmbedURL: embedURL, Code: code, Theme: requestTheme(r), Filter: "all"}, http.StatusOK
}

// writeDetailNotFound answers an unknown post with the app's shared not-found page, inside the shell.
func writeDetailNotFound(w http.ResponseWriter, theme string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusNotFound)
	_ = home.ExecuteTemplate(w, "not-found-page", notFoundPage{Theme: theme})
}

// postThread reads the first page of a post's replies for rendering inside its page.
func postThread(r *http.Request, id string) (thread, int) {
	threadRequest := r.Clone(r.Context())
	threadRequest.URL = &url.URL{Path: "/thread", RawQuery: url.Values{"id": {id}}.Encode()}
	t, status := readThread(threadRequest)
	t.Static = true
	return t, status
}

func postPageHandler(w http.ResponseWriter, r *http.Request) {
	details, status := shareData(r, r.PathValue("id"))
	if status == http.StatusOK {
		details.Thread, status = postThread(r, details.Post.ID)
	}
	if status != http.StatusOK {
		if status == http.StatusNotFound {
			writeDetailNotFound(w, requestTheme(r))
			return
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	_ = home.ExecuteTemplate(w, "post-page", details)
}

// embedTheme honors only an explicit query choice, so an embed otherwise follows the host reader's color scheme.
func embedTheme(r *http.Request) string {
	if theme := r.URL.Query().Get("theme"); theme == "light" || theme == "dark" {
		return theme
	}
	return ""
}

func embedPageHandler(w http.ResponseWriter, r *http.Request) {
	details, status := shareData(r, r.PathValue("id"))
	if status != http.StatusOK && status != http.StatusNotFound {
		http.Error(w, http.StatusText(status), status)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'self'; img-src 'self'; media-src 'self'; base-uri 'none'; form-action 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if status == http.StatusNotFound {
		w.WriteHeader(http.StatusNotFound)
		_ = home.ExecuteTemplate(w, "embed-error-page", struct{ Theme string }{Theme: embedTheme(r)})
		return
	}
	details.Theme = embedTheme(r)
	_ = home.ExecuteTemplate(w, "embed-page", details)
}

func sharePageHandler(w http.ResponseWriter, r *http.Request) {
	details, status := shareData(r, r.URL.Query().Get("id"))
	if status != http.StatusOK {
		if status == http.StatusNotFound {
			writeDetailNotFound(w, requestTheme(r))
			return
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	details.Standalone = true
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	_ = home.ExecuteTemplate(w, "share-page", details)
}

func shareFragmentHandler(w http.ResponseWriter, r *http.Request) {
	details, status := shareData(r, r.URL.Query().Get("id"))
	if status != http.StatusOK {
		http.Error(w, http.StatusText(status), status)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("view") == "closed" {
		_, _ = w.Write([]byte("<span></span>"))
		return
	}
	_ = home.ExecuteTemplate(w, "share-panel", details)
}
