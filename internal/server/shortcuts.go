package server

import (
	"net/http"
	"net/url"
)

func init() { routeGroups = append(routeGroups, scRegister) }

// scCookie holds the shortcut choice. It carries no secret, so the script reads it.
const scCookie = "foryou_shortcuts"

const scMaxAge = 31536000

func scRegister(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /shortcuts", a.scPageHandler)
	mux.HandleFunc("GET /shortcuts/dialog", a.scDialogHandler)
	mux.HandleFunc("POST /shortcuts", a.scToggleHandler)
	mux.HandleFunc("POST /shortcuts/fragment", a.scFragmentHandler)
}

type scPage struct {
	Layout Layout
	State  scStateView
}

type scStateView struct {
	On     bool
	Status string
}

func scOff(r *http.Request) bool {
	c, err := r.Cookie(scCookie)
	return err == nil && c.Value == "off"
}

func scStatus(on bool) string {
	if on {
		return "Shortcuts are on on this browser."
	}
	return "Shortcuts are off on this browser."
}

func scSetCookie(w http.ResponseWriter, r *http.Request, on bool) {
	cookie := &http.Cookie{Name: scCookie, Value: "off", Path: "/", MaxAge: scMaxAge, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode}
	if on {
		cookie.Value, cookie.MaxAge = "", -1
	}
	http.SetCookie(w, cookie)
}

func scChoice(w http.ResponseWriter, r *http.Request) (bool, url.Values, bool) {
	values, ok := readActionForm(w, r, "state")
	if !ok {
		return false, nil, false
	}
	switch values.Get("state") {
	case "on":
		return true, values, true
	case "off":
		return false, values, true
	}
	http.Error(w, "invalid shortcuts setting", http.StatusBadRequest)
	return false, nil, false
}

func (a *App) scPageHandler(w http.ResponseWriter, r *http.Request) {
	a.page(w, r, "shortcuts-page", scPage{Layout: a.layout(r, "settings", "Keyboard shortcuts"), State: scStateView{On: !scOff(r)}})
}

func (a *App) scDialogHandler(w http.ResponseWriter, r *http.Request) {
	a.fragment(w, r, "shortcuts-dialog", nil)
}

func (a *App) scToggleHandler(w http.ResponseWriter, r *http.Request) {
	on, values, ok := scChoice(w, r)
	if !ok {
		return
	}
	scSetCookie(w, r, on)
	target := "/shortcuts#shortcuts-state"
	if values.Get("from") == "settings" {
		target = "/settings#sc-settings"
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

func (a *App) scFragmentHandler(w http.ResponseWriter, r *http.Request) {
	on, _, ok := scChoice(w, r)
	if !ok {
		return
	}
	scSetCookie(w, r, on)
	a.fragment(w, r, "shortcuts-state", scStateView{On: on, Status: scStatus(on)})
}
