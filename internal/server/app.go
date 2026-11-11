package server

import (
	"bytes"
	"context"
	"html/template"
	"net/http"
	"time"
)

// Config contains the client service's local configuration.
type Config struct {
	Dev          bool
	DataDir      string
	PublicOrigin string
	Now          func() time.Time
	LoopInterval time.Duration
}

// App holds the state shared by one client service.
type App struct {
	config    Config
	templates *template.Template
	profiles  *profileStore
	store     *demoStore
	readiness func(context.Context) error
}

// routeGroups lets feature files register routes without central route edits.
var routeGroups []func(*App, *http.ServeMux)

func (a *App) page(w http.ResponseWriter, r *http.Request, name string, data any) {
	a.render(w, r, name, data, true)
}

func (a *App) fragment(w http.ResponseWriter, r *http.Request, name string, data any) {
	a.render(w, r, name, data, false)
}

func (a *App) render(w http.ResponseWriter, r *http.Request, name string, data any, page bool) {
	cache := "no-store"
	if page {
		cache = "private, no-store"
	}
	a.renderOutput(w, name, data, http.StatusOK, cache)
}

func (a *App) renderStatus(w http.ResponseWriter, r *http.Request, name string, data any, status int) {
	a.renderOutput(w, name, data, status, "no-store")
}

func (a *App) renderOutput(w http.ResponseWriter, name string, data any, status int, cache string) {
	var output bytes.Buffer
	if err := a.templates.ExecuteTemplate(&output, name, data); err != nil {
		http.Error(w, "unable to render page", http.StatusInternalServerError)
		return
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	if cache != "" && w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", cache)
	}
	w.WriteHeader(status)
	_, _ = w.Write(output.Bytes())
}

func (a *App) fail(w http.ResponseWriter, r *http.Request, status int, message string) {
	name := "layout-error-message"
	// HTMLeX fetch requests have Sec-Fetch-Dest: empty and no custom request header.
	if r.Header.Get("Sec-Fetch-Dest") == "empty" {
		name = "layout-error-fragment"
	}
	data := struct {
		Layout
		Message string
	}{Layout: a.layout(r, "", "Request failed"), Message: message}
	var output bytes.Buffer
	if err := a.templates.ExecuteTemplate(&output, name, data); err != nil {
		http.Error(w, "request failed", status)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(output.Bytes())
}

func (a *App) layout(r *http.Request, section, title string) Layout {
	filter := r.URL.Query().Get("filter")
	if filter == "" {
		filter = "all"
	}
	return Layout{Title: title, Section: section, Theme: requestTheme(r), Dev: a.config.Dev, Query: r.URL.Query().Get("q"), Filter: filter, Rightbar: section != "compose" && section != "settings" && section != "gallery"}
}
