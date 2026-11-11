package server

import (
	"net/http"
	"net/url"
)

func init() { routeGroups = append(routeGroups, registerSettings) }

func registerSettings(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /settings", a.settingsHandler)
}

type settingsPage struct {
	Layout        Layout
	FeedbackCount int
}

// settingsHandler uses navigable links so appearance changes work without
// JavaScript and the selected view remains meaningful in browser history.
func (a *App) settingsHandler(w http.ResponseWriter, r *http.Request) {
	values, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		http.Error(w, "invalid settings", http.StatusBadRequest)
		return
	}
	theme := requestTheme(r)
	if choice := values.Get("theme"); choice != "" {
		switch choice {
		case "light", "dark":
			rememberTheme(w, r)
			theme = choice
		case "system":
			http.SetCookie(w, &http.Cookie{Name: themeCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode})
			theme = ""
		default:
			http.Error(w, "invalid appearance", http.StatusBadRequest)
			return
		}
	}
	layout := a.layout(r, "settings", "Settings")
	layout.Theme = theme
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	a.page(w, r, "settings-page", settingsPage{Layout: layout, FeedbackCount: len(feedbackEntries(r))})
}
