package server

import (
	"net/http"
	"net/url"
)

type settingsPage struct {
	Theme         string
	Choice        string
	FeedbackCount int
	Section       string
}

// settingsHandler uses navigable links so appearance changes work without
// JavaScript and the selected view remains meaningful in browser history.
func settingsHandler(w http.ResponseWriter, r *http.Request) {
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
	choice := theme
	if choice == "" {
		choice = "system"
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	_ = home.ExecuteTemplate(w, "settings-page", settingsPage{Theme: theme, Choice: choice, FeedbackCount: len(feedbackEntries(r)), Section: "settings"})
}
