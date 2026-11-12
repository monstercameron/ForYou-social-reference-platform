package server

import (
	"net/http"
	"net/url"
	"strings"
)

func init() { routeGroups = append(routeGroups, registerTheme) }

func registerTheme(_ *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /theme", themeHandler)
}

// themeBack returns the page a theme choice was made on, or Home when back is not a path on this client.
func themeBack(back string) string {
	if !strings.HasPrefix(back, "/") || strings.HasPrefix(back, "//") || strings.ContainsAny(back, "\\\r\n") {
		return "/"
	}
	target, err := url.Parse(back)
	if err != nil || target.Host != "" || target.Scheme != "" || target.Path == "/theme" {
		return "/"
	}
	query := target.Query()
	query.Del("theme")
	target.RawQuery = query.Encode()
	return target.RequestURI()
}

// themeHandler saves light or dark, or forgets the choice for system, and returns to the page.
func themeHandler(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	switch choice := query.Get("choice"); choice {
	case "light", "dark":
		http.SetCookie(w, &http.Cookie{Name: themeCookie, Value: choice, Path: "/", MaxAge: 31536000, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode})
	case "system":
		http.SetCookie(w, &http.Cookie{Name: themeCookie, Path: "/", MaxAge: -1, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode})
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, themeBack(query.Get("back")), http.StatusSeeOther)
}

const themeCookie = "foryou_theme"

// requestTheme resolves an explicit choice before the stored browser preference.
func requestTheme(r *http.Request) string {
	if theme := r.URL.Query().Get("theme"); theme == "light" || theme == "dark" {
		return theme
	}
	if c, err := r.Cookie(themeCookie); err == nil && (c.Value == "light" || c.Value == "dark") {
		return c.Value
	}
	return ""
}

func rememberTheme(w http.ResponseWriter, r *http.Request) {
	theme := r.URL.Query().Get("theme")
	if theme != "light" && theme != "dark" {
		return
	}
	http.SetCookie(w, &http.Cookie{Name: themeCookie, Value: theme, Path: "/", MaxAge: 31536000, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode})
}
