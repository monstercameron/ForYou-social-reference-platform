package server

import "net/http"

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
