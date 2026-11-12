package server

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

func init() { routeGroups = append(routeGroups, registerAppearance) }

func registerAppearance(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /appearance", a.appearancePageHandler)
	mux.HandleFunc("POST /appearance", a.appearanceSaveHandler)
	mux.HandleFunc("POST /appearance/preview", a.appearancePreviewHandler)
	mux.HandleFunc("POST /appearance/reset", a.appearanceResetHandler)
	mux.HandleFunc("GET /appearance.css", a.appearanceStylesheetHandler)
	mux.HandleFunc("GET /appearance/swatches.css", a.appearanceSwatchesHandler)
}

const aprCookie = "foryou_appearance"

// aprOption is one choice of a preference. Words is how the choice reads in a sentence.
type aprOption struct{ Value, Label, Words string }

// aprAccent is an accent colour as it is set in each theme. Only these tokens change;
// the text on accent stays as base.css sets it.
type aprAccent struct {
	aprOption
	Light, LightSoft, Dark, DarkSoft string
}

var aprAccents = []aprAccent{
	{aprOption{"indigo", "Indigo (default)", "Indigo accent"}, "#4746df", "#eeedff", "#8589ff", "#272b4b"},
	{aprOption{"blue", "Blue", "Blue accent"}, "#1d4ed8", "#e6edfd", "#7aa7ff", "#1c2a48"},
	{aprOption{"teal", "Teal", "Teal accent"}, "#0f766e", "#e2f3f1", "#3cc9b6", "#143331"},
	{aprOption{"plum", "Plum", "Plum accent"}, "#9333ea", "#f4eafd", "#c99bff", "#33224a"},
}

var aprSizes = []aprOption{
	{"standard", "Standard (default)", "Standard text"},
	{"large", "Large", "Large text"},
	{"larger", "Larger", "Larger text"},
}

var aprDensities = []aprOption{
	{"comfortable", "Comfortable (default)", "Comfortable"},
	{"compact", "Compact", "Compact"},
}

var aprMotions = []aprOption{
	{"system", "Follow device (default)", "Motion follows device"},
	{"reduce", "Always reduce", "Motion always reduced"},
}

// aprPrefs is one reader's choices. The zero value is not used: aprDefaults gives the defaults.
type aprPrefs struct{ Accent, Size, Density, Motion string }

func aprDefaults() aprPrefs {
	return aprPrefs{Accent: "indigo", Size: "standard", Density: "comfortable", Motion: "system"}
}

func aprHas(options []aprOption, value string) bool {
	for _, o := range options {
		if o.Value == value {
			return true
		}
	}
	return false
}

func aprAccentFor(key string) (aprAccent, bool) {
	for _, a := range aprAccents {
		if a.Value == key {
			return a, true
		}
	}
	return aprAccent{}, false
}

func aprAccentOptions() []aprOption {
	options := make([]aprOption, 0, len(aprAccents))
	for _, a := range aprAccents {
		options = append(options, a.aprOption)
	}
	return options
}

func (p aprPrefs) valid() bool {
	_, accent := aprAccentFor(p.Accent)
	return accent && aprHas(aprSizes, p.Size) && aprHas(aprDensities, p.Density) && aprHas(aprMotions, p.Motion)
}

// canonical is the cookie value for these choices.
func (p aprPrefs) canonical() string {
	return strings.Join([]string{"1", p.Accent, p.Size, p.Density, p.Motion}, ".")
}

// aprParse reads a cookie value. Anything outside the fixed sets is refused whole.
func aprParse(value string) (aprPrefs, bool) {
	if len(value) > 64 {
		return aprPrefs{}, false
	}
	parts := strings.Split(value, ".")
	if len(parts) != 5 || parts[0] != "1" {
		return aprPrefs{}, false
	}
	prefs := aprPrefs{Accent: parts[1], Size: parts[2], Density: parts[3], Motion: parts[4]}
	return prefs, prefs.valid()
}

// aprRead returns the reader's saved choices. A damaged cookie gives the defaults and reports damaged.
func aprRead(r *http.Request) (prefs aprPrefs, damaged bool) {
	cookie, err := r.Cookie(aprCookie)
	if err != nil {
		return aprDefaults(), false
	}
	prefs, ok := aprParse(cookie.Value)
	if !ok {
		return aprDefaults(), true
	}
	return prefs, false
}

func aprFromForm(form url.Values) (aprPrefs, bool) {
	prefs := aprPrefs{Accent: form.Get("accent"), Size: form.Get("size"), Density: form.Get("density"), Motion: form.Get("motion")}
	return prefs, prefs.valid()
}

func aprSetCookie(w http.ResponseWriter, r *http.Request, prefs aprPrefs) {
	cookie := &http.Cookie{Name: aprCookie, Path: "/", MaxAge: 31536000, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode}
	if prefs == aprDefaults() {
		cookie.MaxAge = -1
	} else {
		cookie.Value = prefs.canonical()
	}
	http.SetCookie(w, cookie)
}

func aprETag(prefs aprPrefs) string {
	if prefs == aprDefaults() {
		return `"apr-default"`
	}
	return `"apr-` + prefs.canonical() + `"`
}

// aprStylesheet builds the reader's rules from the fixed tables. Only non-default choices get a rule.
func aprStylesheet(prefs aprPrefs) string {
	var b strings.Builder
	b.WriteString("/* ForYou appearance for this browser */\n")
	if accent, ok := aprAccentFor(prefs.Accent); ok && prefs.Accent != "indigo" {
		fmt.Fprintf(&b, "body{--accent:%s;--accent-soft:%s}\n", accent.Light, accent.LightSoft)
		fmt.Fprintf(&b, "body[data-theme=dark]{--accent:%s;--accent-soft:%s}\n", accent.Dark, accent.DarkSoft)
		fmt.Fprintf(&b, "@media(prefers-color-scheme:dark){body:not([data-theme=light]){--accent:%s;--accent-soft:%s}}\n", accent.Dark, accent.DarkSoft)
	}
	switch prefs.Size {
	case "large":
		b.WriteString("main.center>:not(.mobile-nav){zoom:1.125}\n")
	case "larger":
		b.WriteString("main.center>:not(.mobile-nav){zoom:1.25}\n")
	}
	if prefs.Density == "compact" {
		b.WriteString(".posts{gap:8px}.posts>.post{padding:12px}.component-card{padding:16px}.settings-card+.settings-card{margin-top:12px}.posts>.post .post-text{line-height:1.5}\n")
	}
	if prefs.Motion == "reduce" {
		b.WriteString("*,*::before,*::after{animation:none!important;transition:none!important}html{scroll-behavior:auto!important}\n")
	}
	return b.String()
}

// aprSwatches gives each accent's swatch and preview the same colours the page uses.
func aprSwatches() string {
	var b strings.Builder
	for _, a := range aprAccents {
		light := fmt.Sprintf(".apr-swatch-%[1]s,.apr-preview[data-apr-accent=%[1]s]", a.Value)
		dark := fmt.Sprintf(":is(.apr-swatch-%[1]s,.apr-preview[data-apr-accent=%[1]s])", a.Value)
		fmt.Fprintf(&b, "%s{--accent:%s;--accent-soft:%s}\n", light, a.Light, a.LightSoft)
		fmt.Fprintf(&b, "body[data-theme=dark] %s{--accent:%s;--accent-soft:%s}\n", dark, a.Dark, a.DarkSoft)
		fmt.Fprintf(&b, "@media(prefers-color-scheme:dark){body:not([data-theme=light]) %s{--accent:%s;--accent-soft:%s}}\n", dark, a.Dark, a.DarkSoft)
	}
	return b.String()
}

func (a *App) appearanceStylesheetHandler(w http.ResponseWriter, r *http.Request) {
	prefs, _ := aprRead(r)
	etag := aprETag(prefs)
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("Vary", "Cookie")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = io.WriteString(w, aprStylesheet(prefs))
}

func (a *App) appearanceSwatchesHandler(w http.ResponseWriter, r *http.Request) {
	const etag = `"apr-swatches-1"`
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	_, _ = io.WriteString(w, aprSwatches())
}

// aprNotice is the message shown above the choices. Role is "status" for news and "alert" for a refused choice.
type aprNotice struct {
	Text, Role, Class string
	Focus             bool
}

type aprChoice struct {
	aprOption
	Checked bool
}

type aprRow struct {
	Key, Name, About string
	Swatches         bool
	Choices          []aprChoice
}

type aprPreview struct {
	Accent, Size, Density, Motion, Summary string
}

type aprPage struct {
	Layout  Layout
	Rows    []aprRow
	Preview aprPreview
	Notice  aprNotice
}

func aprRows(prefs aprPrefs) []aprRow {
	row := func(key, name, about string, swatches bool, options []aprOption, current string) aprRow {
		choices := make([]aprChoice, 0, len(options))
		for _, o := range options {
			choices = append(choices, aprChoice{aprOption: o, Checked: o.Value == current})
		}
		return aprRow{Key: key, Name: name, About: about, Swatches: swatches, Choices: choices}
	}
	return []aprRow{
		row("accent", "Accent color", "Buttons, links, the current page in the menu and focus outlines. Every color keeps text readable in light and dark.", true, aprAccentOptions(), prefs.Accent),
		row("size", "Text size", "Scales posts, replies and panels in the main column to 100%, 112.5% or 125%. The menu keeps its size.", false, aprSizes, prefs.Size),
		row("density", "Density", "Compact tightens the space around posts and panels. Buttons stay at least 44 pixels tall.", false, aprDensities, prefs.Density),
		row("motion", "Motion", "Always reduce turns off entrance animations, hover movement, loading shimmer and smooth scrolling on every page, whatever your device asks for. Follow device does this only when your device asks for reduced motion.", false, aprMotions, prefs.Motion),
	}
}

func aprPreviewFor(prefs aprPrefs) aprPreview {
	accent, _ := aprAccentFor(prefs.Accent)
	words := []string{accent.Words, aprWords(aprSizes, prefs.Size), aprWords(aprDensities, prefs.Density), aprWords(aprMotions, prefs.Motion)}
	return aprPreview{Accent: prefs.Accent, Size: prefs.Size, Density: prefs.Density, Motion: prefs.Motion, Summary: strings.Join(words, " · ")}
}

func aprWords(options []aprOption, value string) string {
	for _, o := range options {
		if o.Value == value {
			return o.Words
		}
	}
	return ""
}

func (a *App) appearanceView(r *http.Request, prefs aprPrefs, notice aprNotice) aprPage {
	return aprPage{Layout: a.layout(r, "settings", "Appearance"), Rows: aprRows(prefs), Preview: aprPreviewFor(prefs), Notice: notice}
}

func (a *App) appearancePageHandler(w http.ResponseWriter, r *http.Request) {
	prefs, damaged := aprRead(r)
	notice := aprNotice{Role: "status"}
	switch r.URL.Query().Get("done") {
	case "saved":
		notice = aprNotice{Text: "Saved. Every page in this browser now uses these choices.", Role: "status", Focus: true}
	case "reset":
		notice = aprNotice{Text: "Back to the defaults. The appearance cookie was removed.", Role: "status", Focus: true}
	default:
		if damaged {
			notice = aprNotice{Text: "The appearance saved in this browser could not be read, so the defaults are shown. Saving replaces it.", Role: "status", Class: "notice notice-warn"}
		}
	}
	a.page(w, r, "appearance-page", a.appearanceView(r, prefs, notice))
}

// appearanceSaveHandler is a plain form post, so the stylesheet is fetched again after a save.
func (a *App) appearanceSaveHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := readActionForm(w, r, "accent", "size", "density", "motion")
	if !ok {
		return
	}
	prefs, valid := aprFromForm(form)
	if !valid {
		saved, _ := aprRead(r)
		notice := aprNotice{Text: "That choice is not offered. Pick one of the options below.", Role: "alert"}
		a.renderStatus(w, r, "appearance-page", a.appearanceView(r, saved, notice), http.StatusBadRequest)
		return
	}
	if form.Get("preview") == "1" {
		notice := aprNotice{Text: "Preview only. Nothing is saved until you choose Save appearance.", Role: "status", Focus: true}
		a.page(w, r, "appearance-page", a.appearanceView(r, prefs, notice))
		return
	}
	aprSetCookie(w, r, prefs)
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/appearance?done=saved", http.StatusSeeOther)
}

// appearancePreviewHandler answers the live preview. It never stores anything.
func (a *App) appearancePreviewHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := readActionForm(w, r, "accent", "size", "density", "motion")
	if !ok {
		return
	}
	prefs, valid := aprFromForm(form)
	if !valid {
		http.Error(w, "That choice is not offered.", http.StatusBadRequest)
		return
	}
	a.fragment(w, r, "appearance-preview", aprPreviewFor(prefs))
}

func (a *App) appearanceResetHandler(w http.ResponseWriter, r *http.Request) {
	if !requireSameOrigin(w, r) {
		return
	}
	aprSetCookie(w, r, aprDefaults())
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/appearance?done=reset", http.StatusSeeOther)
}
