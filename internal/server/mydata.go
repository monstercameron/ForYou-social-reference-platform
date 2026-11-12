package server

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"
)

func init() { routeGroups = append(routeGroups, registerMydata) }

func registerMydata(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /mydata", a.mydataPageHandler)
	mux.HandleFunc("GET /mydata/export", a.mydataExportHandler)
	mux.HandleFunc("POST /mydata/import", a.mydataImportHandler)
	mux.HandleFunc("POST /mydata/import/apply", a.mydataApplyHandler)
	mux.HandleFunc("POST /mydata/erase", a.mydataEraseHandler)
	mux.HandleFunc("POST /mydata/erase/fragment", a.mydataEraseHandler)
}

// mydataKinds are the stored kinds the page lists, in display order.
var mydataKinds = []string{"appearance", "filters", "feedback", "follows", "media", "network", "acknowledgements"}

var mydataTitles = map[string]string{
	"appearance":       "Appearance",
	"filters":          "Content filters",
	"feedback":         "Likes and dislikes",
	"follows":          "Follows",
	"media":            "Media origins you allowed",
	"network":          "Network choices",
	"acknowledgements": "Warnings you confirmed",
}

const (
	mydataInBrowser       = "In this browser"
	mydataOnServer        = "On this server"
	mydataCouldNotRead    = "A saved value could not be read and is ignored."
	mydataNoProfile       = "Nothing saved: this browser has no profile ID yet."
	mydataEraseFailed     = "Could not erase. Nothing changed; try again."
	mydataConfirmNeeded   = "Tick the box to confirm, then erase again."
	mydataSettingsDamaged = "The settings file for this profile could not be read, so its contents are not shown or downloaded. Network, media and warning choices fall back to the defaults until it is removed."
	mydataJournalCap      = 1000
)

// mydataState is what the page shows about the reader, read from the request
// cookies and from this profile's files on this server.
type mydataState struct {
	Profile       string
	Theme         string
	ThemeSaved    bool
	ThemeBad      bool
	Levels        []int
	FiltersSaved  bool
	FiltersBad    bool
	FeedbackSaved bool
	Feedback      []feedbackEntry
	FollowsSaved  bool
	FollowsBad    bool
	Follows       []string
	SettingsBad   bool
	Settings      ProfileSettings
	DID           string
	Journal       int
	More          mydataMore
}

func (a *App) mydataReadState(r *http.Request) mydataState {
	s := mydataState{Profile: mydataProfile(r), Levels: mydataDefaultLevels()}
	s.Theme, s.ThemeSaved, s.ThemeBad = mydataThemeFrom(r)
	s.Levels, s.FiltersSaved, s.FiltersBad = mydataFiltersFrom(r)
	s.FeedbackSaved = mydataFeedbackSaved(r)
	s.Feedback = feedbackEntries(r)
	s.Follows, s.FollowsSaved, s.FollowsBad = a.mydataFollowsFrom(s.Profile)
	s.More = a.mydataReadMore(r, s.Profile)
	if s.Profile == "" {
		return s
	}
	settings, err := a.settings(s.Profile)
	s.SettingsBad = err != nil
	s.Settings = settings
	if identity, found, err := a.keystore().Identity(s.Profile); err == nil && found {
		s.DID = identity.DID
	}
	s.Journal = a.mydataJournal(s.Profile)
	return s
}

// mydataProfile reads the profile cookie without creating a profile, so a GET never sets a cookie.
func mydataProfile(r *http.Request) string {
	cookie, err := r.Cookie(profileCookie)
	if err != nil || !validProfileID(cookie.Value) {
		return ""
	}
	return cookie.Value
}

func mydataDefaultLevels() []int {
	levels := make([]int, len(dimensions))
	for i, d := range dimensions {
		levels[i] = d.Shown
	}
	return levels
}

func mydataThemeFrom(r *http.Request) (theme string, saved, bad bool) {
	cookie, err := r.Cookie(themeCookie)
	if err != nil {
		return "system", false, false
	}
	if cookie.Value == "light" || cookie.Value == "dark" {
		return cookie.Value, true, false
	}
	return "system", true, true
}

func mydataThemeLabel(theme string) string {
	switch theme {
	case "light":
		return "Light"
	case "dark":
		return "Dark"
	}
	return "System"
}

func mydataFiltersFrom(r *http.Request) (levels []int, saved, bad bool) {
	cookie, err := r.Cookie(filterCookie)
	if err != nil {
		return mydataDefaultLevels(), false, false
	}
	levels, ok := mydataFilterDigits(cookie.Value)
	if !ok {
		return mydataDefaultLevels(), true, true
	}
	return levels, true, false
}

// mydataFilterDigits reads the filter cookie: one digit per dimension, each a valid level.
func mydataFilterDigits(value string) ([]int, bool) {
	if len(value) != len(dimensions) {
		return nil, false
	}
	levels := make([]int, len(dimensions))
	for i, d := range dimensions {
		if value[i] < '0' || int(value[i]-'0') >= len(d.Levels) {
			return nil, false
		}
		levels[i] = int(value[i] - '0')
	}
	return levels, true
}

func mydataFeedbackSaved(r *http.Request) bool {
	for _, c := range r.Cookies() {
		if c.Name == feedbackCookie || strings.HasPrefix(c.Name, feedbackPrefix) {
			return true
		}
	}
	return false
}

// mydataFollowsFrom reads the keys the profile follows from its follow list file.
func (a *App) mydataFollowsFrom(profile string) (follows []string, saved, bad bool) {
	if profile == "" {
		return nil, false, false
	}
	state, err := a.followingRead(profile)
	if err != nil {
		return nil, true, true
	}
	for _, entry := range state.Follows {
		follows = append(follows, entry.AuthorID)
	}
	return follows, len(follows) > 0, false
}

// mydataJournal counts the publish journal entries of a profile, stopping just past the cap.
func (a *App) mydataJournal(profile string) int {
	entries, err := os.ReadDir(filepath.Join(a.config.DataDir, "publish", profile))
	if err != nil {
		return 0
	}
	count := 0
	for _, entry := range entries {
		if entry.Type().IsRegular() && strings.HasSuffix(entry.Name(), ".json") {
			count++
			if count > mydataJournalCap {
				break
			}
		}
	}
	return count
}

// mydataItem is one line in a row's list. Mono names a stored identifier.
type mydataItem struct {
	Text, Hidden, Href, Note string
	Mono                     bool
}

// mydataRow is one stored kind on the page.
type mydataRow struct {
	Key, Title, Where, Code, Suffix string
	Summary, Notice                 string
	Lines                           []string
	Items                           []mydataItem
	Details                         string
	Saved, Damaged                  bool
}

func mydataRows(s mydataState) []mydataRow {
	rows := []mydataRow{mydataAppearanceRow(s), mydataFiltersRow(s), mydataFeedbackRow(s), mydataFollowsRow(s)}
	if s.SettingsBad {
		rows = append(rows, mydataRow{Key: "media", Title: "Settings file", Where: mydataOnServer, Code: "settings.json", Suffix: " for this profile", Notice: mydataSettingsDamaged, Saved: true, Damaged: true})
	} else {
		rows = append(rows, mydataMediaRow(s), mydataNetworkRow(s), mydataAcknowledgementsRow(s))
	}
	return append(rows, mydataMoreRows(s.Profile, s.More)...)
}

func mydataAppearanceRow(s mydataState) mydataRow {
	row := mydataRow{Key: "appearance", Title: mydataTitles["appearance"], Where: mydataInBrowser, Code: "foryou_theme", Suffix: " cookie", Saved: s.ThemeSaved}
	switch {
	case s.ThemeBad:
		row.Summary = mydataCouldNotRead
	case !s.ThemeSaved:
		row.Summary = "System (no cookie)"
	default:
		row.Summary = mydataThemeLabel(s.Theme)
	}
	return row
}

func mydataFiltersRow(s mydataState) mydataRow {
	row := mydataRow{Key: "filters", Title: mydataTitles["filters"], Where: mydataInBrowser, Code: "foryou_filters", Suffix: " cookie", Saved: s.FiltersSaved}
	switch {
	case !s.FiltersSaved:
		row.Summary = "Defaults (no cookie)"
		return row
	case s.FiltersBad:
		row.Summary = mydataCouldNotRead
		return row
	}
	labels := filterRows(s.Levels)
	changed := 0
	for i, d := range dimensions {
		if s.Levels[i] == d.Shown {
			continue
		}
		changed++
		row.Items = append(row.Items, mydataItem{Text: d.Name + ": " + labels[i].Options[s.Levels[i]].Label})
	}
	row.Summary = fmt.Sprintf("%d of %d kinds changed from the defaults", changed, len(dimensions))
	return row
}

func mydataFeedbackRow(s mydataState) mydataRow {
	row := mydataRow{Key: "feedback", Title: mydataTitles["feedback"], Where: mydataInBrowser, Code: "foryou_vote_…", Suffix: " cookies, one per post, and the older foryou_feedback cookie if present", Saved: s.FeedbackSaved}
	if len(s.Feedback) == 0 {
		row.Summary = "None saved."
		return row
	}
	up := 0
	for _, e := range s.Feedback {
		if e.Vote == "up" {
			up++
		}
	}
	row.Summary = fmt.Sprintf("%d saved: %d up, %d down. Only the %d most recent are kept.", len(s.Feedback), up, len(s.Feedback)-up, feedbackLimit)
	for _, e := range s.Feedback {
		note := "liked"
		if e.Vote == "down" {
			note = "disliked"
		}
		row.Items = append(row.Items, mydataItem{Text: e.ID, Href: "/posts/" + url.PathEscape(e.ID), Note: note, Mono: true})
	}
	row.Details = "Show posts"
	return row
}

func mydataFollowsRow(s mydataState) mydataRow {
	row := mydataRow{Key: "follows", Title: mydataTitles["follows"], Where: mydataOnServer, Code: "following.json", Suffix: " for this profile", Saved: s.FollowsSaved}
	switch len(s.Follows) {
	case 0:
		row.Summary = "None saved."
	case 1:
		row.Summary = fmt.Sprintf("1 key followed (up to %d).", followingKeyLimit)
	default:
		row.Summary = fmt.Sprintf("%d keys followed (up to %d).", len(s.Follows), followingKeyLimit)
	}
	if s.FollowsBad {
		row.Lines = []string{"The follow list file could not be read."}
	}
	for _, did := range s.Follows {
		row.Items = append(row.Items, mydataItem{Text: shortDID(did), Hidden: did, Mono: true})
	}
	if len(s.Follows) > 0 {
		row.Details = "Show keys"
	}
	return row
}

func mydataMediaRow(s mydataState) mydataRow {
	row := mydataRow{Key: "media", Title: mydataTitles["media"], Where: mydataOnServer, Code: "settings.json", Suffix: ", field media_origins"}
	origins := s.Settings.MediaOrigins
	switch {
	case s.Profile == "":
		row.Summary = mydataNoProfile
	case len(origins) == 0:
		row.Summary = "None. Media loads only when you click it."
	default:
		row.Saved = true
		row.Summary = fmt.Sprintf("%d origins:", len(origins))
		if len(origins) == 1 {
			row.Summary = "1 origin:"
		}
		for _, origin := range origins {
			row.Items = append(row.Items, mydataItem{Text: origin, Mono: true})
		}
	}
	return row
}

func mydataNetworkRow(s mydataState) mydataRow {
	row := mydataRow{Key: "network", Title: mydataTitles["network"], Where: mydataOnServer, Code: "settings.json", Suffix: ", fields providers, removed_defaults, monitors, publish_network"}
	choices := s.Settings
	switch {
	case s.Profile == "":
		row.Summary = mydataNoProfile
	case len(choices.Providers) == 0 && len(choices.RemovedDefaults) == 0 && len(choices.Monitors) == 0 && choices.PublishNetwork == "":
		row.Summary = "Defaults from this server."
	default:
		row.Saved = true
		if len(choices.Providers) > 0 {
			row.Lines = append(row.Lines, fmt.Sprintf("Providers you added or changed: %d", len(choices.Providers)))
		}
		if len(choices.RemovedDefaults) > 0 {
			row.Lines = append(row.Lines, fmt.Sprintf("Default providers removed: %d", len(choices.RemovedDefaults)))
		}
		if len(choices.Monitors) > 0 {
			row.Lines = append(row.Lines, fmt.Sprintf("Monitors you added: %d", len(choices.Monitors)))
		}
		if choices.PublishNetwork != "" {
			row.Lines = append(row.Lines, "Publishing network: "+choices.PublishNetwork)
		}
	}
	return row
}

func mydataAcknowledgementsRow(s mydataState) mydataRow {
	row := mydataRow{Key: "acknowledgements", Title: mydataTitles["acknowledgements"], Where: mydataOnServer, Code: "settings.json", Suffix: ", field acknowledgements"}
	acks := s.Settings.Acknowledgements
	switch {
	case s.Profile == "":
		row.Summary = mydataNoProfile
	case len(acks) == 0:
		row.Summary = "None."
	default:
		row.Saved = true
		names := make([]string, 0, len(acks))
		for name := range acks {
			names = append(names, name)
		}
		sort.Strings(names)
		row.Summary = fmt.Sprintf("%d confirmed. Erasing these means the warnings are shown again.", len(names))
		for _, name := range names {
			row.Items = append(row.Items, mydataItem{Text: mydataAckLabel(name) + ", confirmed " + acks[name].Format("2006-01-02")})
		}
	}
	return row
}

func mydataAckLabel(name string) string {
	switch {
	case name == ackKeyWarnings:
		return "Key warnings"
	case name == ackPermanence:
		return "Posts are permanent"
	case name == ackLinkability:
		return "Funding can be linked to you"
	case strings.HasPrefix(name, ackCustodyPrefix):
		return "Custody disclosure for " + strings.TrimPrefix(name, ackCustodyPrefix)
	}
	return name
}

// mydataIdentity is the Identity card: the profile ID prefix, the public key and the publish journal.
type mydataIdentity struct {
	HasProfile  bool
	Prefix      string
	HasKey      bool
	Short, DID  string
	HasJournal  bool
	JournalText string
}

func mydataIdentityFor(s mydataState) mydataIdentity {
	var identity mydataIdentity
	if s.Profile != "" {
		identity.HasProfile = true
		identity.Prefix = s.Profile[:8]
	}
	if s.DID != "" {
		identity.HasKey = true
		identity.DID = s.DID
		identity.Short = shortDID(s.DID)
	}
	if s.Journal > 0 {
		identity.HasJournal = true
		identity.JournalText = fmt.Sprintf("%d entries", s.Journal)
		switch {
		case s.Journal > mydataJournalCap:
			identity.JournalText = "1000 or more entries"
		case s.Journal == 1:
			identity.JournalText = "1 entry"
		}
	}
	return identity
}

// mydataCookieErased says whether an erase of a kind removes a request cookie, so the answer can render the state after it.
func mydataCookieErased(kind, name string) bool {
	switch kind {
	case "appearance":
		return name == themeCookie
	case "filters":
		return name == filterCookie
	case "feedback":
		return name == feedbackCookie || strings.HasPrefix(name, feedbackPrefix)
	case "follows":
		return name == followCookie
	case "everything":
		return name == themeCookie || name == filterCookie || name == followCookie || name == feedbackCookie || strings.HasPrefix(name, feedbackPrefix) || mydataMoreCookieErased(kind, name)
	}
	return mydataMoreCookieErased(kind, name)
}

// mydataWithout copies a request with the cookies that drop selects removed.
func mydataWithout(r *http.Request, drop func(name string) bool) *http.Request {
	clone := r.Clone(r.Context())
	clone.Header.Del("Cookie")
	for _, c := range r.Cookies() {
		if !drop(c.Name) {
			clone.AddCookie(c)
		}
	}
	return clone
}

// mydataStatusFor turns the page's query parameters into the status line. Unknown values are ignored.
func mydataStatusFor(erased, imported string) string {
	switch erased {
	case "everything":
		return "Everything erased. This browser has a new start; a new profile ID is made when one is needed."
	case "everything-kept":
		return "Everything erased. Your profile ID stays because your identity key or publishing journal is kept with it."
	}
	if title, ok := mydataTitles[erased]; ok {
		return title + " erased."
	}
	if n, err := strconv.Atoi(imported); err == nil && n >= 0 && n <= 8 {
		switch n {
		case 0:
			return "Nothing was imported."
		case 1:
			return "Imported 1 part."
		}
		return fmt.Sprintf("Imported %d parts.", n)
	}
	return ""
}

// mydataKindsView is the What is saved section: its status line and the rows.
type mydataKindsView struct {
	Status string
	Rows   []mydataRow
}

// mydataPageView is everything the Your data page shows.
type mydataPageView struct {
	Layout      Layout
	Kinds       mydataKindsView
	Identity    mydataIdentity
	ImportError string
}

func (a *App) mydataPageView(r *http.Request, s mydataState, status, importError string) mydataPageView {
	return mydataPageView{
		Layout:      a.layout(r, "settings", "Your data"),
		Kinds:       mydataKindsView{Status: status, Rows: mydataRows(s)},
		Identity:    mydataIdentityFor(s),
		ImportError: importError,
	}
}

func (a *App) mydataPageHandler(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	status := mydataStatusFor(query.Get("erased"), query.Get("imported"))
	w.Header().Set("Cache-Control", "private, no-store")
	a.page(w, r, "mydata-page", a.mydataPageView(r, a.mydataReadState(r), status, ""))
}

func (a *App) mydataEraseHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := readActionForm(w, r, "kind")
	if !ok {
		return
	}
	kind := form.Get("kind")
	fragment := strings.HasSuffix(r.URL.Path, "/fragment")
	profile := mydataProfile(r)
	w.Header().Set("Cache-Control", "no-store")
	if kind == "everything" {
		a.mydataEraseEverything(w, r, form.Get("confirm") == "yes", fragment, profile)
		return
	}
	erase := a.mydataEraseKind
	switch {
	case slices.Contains(mydataMoreKinds, kind):
		erase = a.mydataEraseMore
	case !slices.Contains(mydataKinds, kind):
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if err := erase(w, r, kind, profile); err != nil {
		a.fail(w, r, http.StatusInternalServerError, mydataEraseFailed)
		return
	}
	if !fragment {
		http.Redirect(w, r, "/mydata?erased="+kind+"#mydata-kinds-title", http.StatusSeeOther)
		return
	}
	after := mydataWithout(r, func(name string) bool { return mydataCookieErased(kind, name) })
	a.fragment(w, r, "mydata-kinds", mydataKindsView{Status: mydataStatusFor(kind, ""), Rows: mydataRows(a.mydataReadState(after))})
}

// mydataEraseKind expires the cookies of a browser kind, or rewrites the server settings of a server kind.
func (a *App) mydataEraseKind(w http.ResponseWriter, r *http.Request, kind, profile string) error {
	switch kind {
	case "appearance":
		mydataExpireCookie(w, r, themeCookie, http.SameSiteLaxMode)
	case "filters":
		mydataExpireCookie(w, r, filterCookie, http.SameSiteStrictMode)
	case "feedback":
		mydataExpireFeedback(w, r)
	case "follows":
		mydataExpireCookie(w, r, followCookie, http.SameSiteStrictMode)
		if profile == "" {
			return nil
		}
		return a.followingUpdate(profile, func(state *followingState) error {
			state.Follows, state.Dismissed = nil, nil
			return nil
		})
	default:
		return a.mydataEraseSettings(kind, profile)
	}
	return nil
}

// mydataExpireFeedback removes the legacy feedback cookie and every per-post vote cookie.
func mydataExpireFeedback(w http.ResponseWriter, r *http.Request) {
	expireFeedbackCookie(w, r, feedbackCookie)
	for _, c := range r.Cookies() {
		if strings.HasPrefix(c.Name, feedbackPrefix) && len(c.Name) <= 128 {
			expireFeedbackCookie(w, r, c.Name)
		}
	}
}

// mydataEraseSettings clears one server kind. A damaged settings file cannot be rewritten, so it is removed.
func (a *App) mydataEraseSettings(kind, profile string) error {
	if profile == "" {
		return nil
	}
	if _, err := a.settings(profile); err != nil {
		return a.mydataRemoveProfileFiles(profile, "settings.json")
	}
	return a.updateSettings(profile, func(settings *ProfileSettings) error {
		switch kind {
		case "media":
			settings.MediaOrigins = nil
		case "network":
			settings.Providers, settings.RemovedDefaults, settings.Monitors, settings.PublishNetwork = nil, nil, nil, ""
		case "acknowledgements":
			settings.Acknowledgements = nil
		}
		return nil
	})
}

// mydataRemoveProfileFiles deletes the named files of a profile and its folder when nothing else is in it.
func (a *App) mydataRemoveProfileFiles(profile string, files ...string) error {
	lock := a.profiles.lock(profile)
	lock.Lock()
	defer lock.Unlock()
	dir := filepath.Join(a.profiles.root, profile)
	for _, file := range files {
		if err := mydataRemove(filepath.Join(dir, file)); err != nil {
			return err
		}
	}
	_ = os.Remove(dir)
	return nil
}

// mydataKept reports whether a profile must keep its cookie: it has an identity key or publish journal entries.
func (a *App) mydataKept(profile string) bool {
	if profile == "" {
		return false
	}
	if _, found, err := a.keystore().Identity(profile); err != nil || found {
		return true
	}
	return a.mydataJournal(profile) > 0
}

func (a *App) mydataEraseEverything(w http.ResponseWriter, r *http.Request, confirmed, fragment bool, profile string) {
	if !confirmed {
		state := a.mydataReadState(r)
		if fragment {
			a.renderStatus(w, r, "mydata-kinds", mydataKindsView{Status: mydataConfirmNeeded, Rows: mydataRows(state)}, http.StatusBadRequest)
			return
		}
		a.renderStatus(w, r, "mydata-page", a.mydataPageView(r, state, mydataConfirmNeeded, ""), http.StatusBadRequest)
		return
	}
	mydataExpireCookie(w, r, themeCookie, http.SameSiteLaxMode)
	mydataExpireCookie(w, r, filterCookie, http.SameSiteStrictMode)
	mydataExpireCookie(w, r, followCookie, http.SameSiteStrictMode)
	mydataExpireFeedback(w, r)
	for _, kind := range mydataMoreKinds {
		if name, sameSite, ok := mydataMoreCookie(kind); ok {
			mydataExpireCookie(w, r, name, sameSite)
		}
	}
	if profile != "" {
		if err := a.mydataRemoveProfileFiles(profile, mydataProfileFiles...); err != nil {
			a.fail(w, r, http.StatusInternalServerError, mydataEraseFailed)
			return
		}
	}
	kept := a.mydataKept(profile)
	status := "everything"
	if kept {
		status = "everything-kept"
	} else {
		http.SetCookie(w, &http.Cookie{Name: profileCookie, Path: "/", MaxAge: -1, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode})
	}
	if !fragment {
		http.Redirect(w, r, "/mydata?erased="+status+"#mydata-kinds-title", http.StatusSeeOther)
		return
	}
	after := mydataWithout(r, func(name string) bool {
		return mydataCookieErased("everything", name) || (!kept && name == profileCookie)
	})
	a.fragment(w, r, "mydata-kinds", mydataKindsView{Status: mydataStatusFor(status, ""), Rows: mydataRows(a.mydataReadState(after))})
}

// mydataExpireCookie removes a cookie with the attributes the client sets it with.
func mydataExpireCookie(w http.ResponseWriter, r *http.Request, name string, sameSite http.SameSite) {
	http.SetCookie(w, &http.Cookie{Name: name, Path: "/", MaxAge: -1, HttpOnly: true, Secure: r.TLS != nil, SameSite: sameSite})
}

func (a *App) mydataExportHandler(w http.ResponseWriter, r *http.Request) {
	now := a.now().UTC()
	file := mydataExportFile(a.mydataReadState(r), now.Format(time.RFC3339))
	body, err := mydataMarshal(file)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "Your data could not be exported.")
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="foryou-data-`+now.Format("2006-01-02")+`.json"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(body)
}
