package server

import (
	"net/http"
	"net/url"
	"slices"
	"strings"
)

func init() { routeGroups = append(routeGroups, registerSettings) }

func registerSettings(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /settings", a.settingsHandler)
	mux.HandleFunc("POST /settings/filters", a.filtersHandler)
	mux.HandleFunc("POST /settings/filters/fragment", a.filtersHandler)
	mux.HandleFunc("POST /settings/media-origins/remove", a.settingsMediaRemove)
	mux.HandleFunc("POST /settings/media-origins/remove/fragment", a.settingsMediaRemove)
	mux.HandleFunc("POST /settings/media-origins/clear", a.settingsMediaClear)
	mux.HandleFunc("POST /settings/media-origins/clear/fragment", a.settingsMediaClear)
}

type settingsPage struct {
	Layout        Layout
	FeedbackCount int
	Filters       filterSettings
	Media         settingsMedia
	Profile       settingsProfile
}

// settingsMedia is the media section: the origins this profile allows and the outcome of the last change.
type settingsMedia struct {
	Origins []string
	Notice  string
}

// settingsProfile names the browser profile by a prefix of its identifier.
type settingsProfile struct {
	Prefix string
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
	profileID, stored, err := a.settingsCurrent(r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "Settings could not be read.")
		return
	}
	var profile settingsProfile
	if profileID != "" {
		profile.Prefix = profileID[:8]
	}
	layout := a.layout(r, "settings", "Settings")
	layout.Theme = theme
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	a.page(w, r, "settings-page", settingsPage{
		Layout:        layout,
		FeedbackCount: len(feedbackEntries(r)),
		Filters:       filterSettingsFor(readerFilters(r), false),
		Media:         settingsMedia{Origins: stored.MediaOrigins},
		Profile:       profile,
	})
}

// settingsCurrent reads the saved settings of the profile this browser identifies with. A browser without a profile has none.
func (a *App) settingsCurrent(r *http.Request) (string, ProfileSettings, error) {
	cookie, err := r.Cookie(profileCookie)
	if err != nil || !validProfileID(cookie.Value) {
		return "", ProfileSettings{}, nil
	}
	stored, err := a.settings(cookie.Value)
	return cookie.Value, stored, err
}

// settingsMediaRemove stops media loading from one origin. The fragment route answers with the media section; the plain form redirects back to it.
func (a *App) settingsMediaRemove(w http.ResponseWriter, r *http.Request) {
	form, ok := readActionForm(w, r, "origin")
	if !ok {
		return
	}
	origin := form.Get("origin")
	a.settingsMediaSave(w, r, func(settings *ProfileSettings) string {
		if !slices.Contains(settings.MediaOrigins, origin) {
			return ""
		}
		medStateFor(a).fetch.cancelOrigin(origin)
		settings.MediaOrigins = slices.DeleteFunc(settings.MediaOrigins, func(allowed string) bool { return allowed == origin })
		return "Stopped loading media from " + origin + "."
	})
}

// settingsMediaClear stops media loading from every origin.
func (a *App) settingsMediaClear(w http.ResponseWriter, r *http.Request) {
	if _, ok := readActionForm(w, r); !ok {
		return
	}
	a.settingsMediaSave(w, r, func(settings *ProfileSettings) string {
		for _, origin := range settings.MediaOrigins {
			medStateFor(a).fetch.cancelOrigin(origin)
		}
		settings.MediaOrigins = nil
		return "Stopped loading media from every origin."
	})
}

func (a *App) settingsMediaSave(w http.ResponseWriter, r *http.Request, change func(*ProfileSettings) string) {
	id, err := a.profile(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "Settings could not be saved.")
		return
	}
	var notice string
	if err := a.updateSettings(id, func(settings *ProfileSettings) error {
		notice = change(settings)
		return nil
	}); err != nil {
		a.fail(w, r, http.StatusInternalServerError, "Settings could not be saved.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if !strings.HasSuffix(r.URL.Path, "/fragment") {
		http.Redirect(w, r, "/settings#media", http.StatusSeeOther)
		return
	}
	stored, err := a.settings(id)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "Settings could not be read.")
		return
	}
	a.fragment(w, r, "settings-media", settingsMedia{Origins: stored.MediaOrigins, Notice: notice})
}
