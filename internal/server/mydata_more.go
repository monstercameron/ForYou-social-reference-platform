package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
)

// The kinds in this file were added after the first data file format. The page
// lists and erases them and the download carries them; import shows them for
// reference and does not apply them.
var mydataMoreKinds = []string{"display", "order", "shortcuts", "start", "saved", "mutes", "petnames", "displayname", "reports", "manifests"}

func init() {
	mydataTitles["display"] = "Accent, text size and motion"
	mydataTitles["order"] = "Home feed order"
	mydataTitles["shortcuts"] = "Keyboard shortcuts"
	mydataTitles["start"] = "Getting started panel"
	mydataTitles["saved"] = "Saved posts"
	mydataTitles["mutes"] = "Muted keys and words"
	mydataTitles["petnames"] = "Names you gave to keys"
	mydataTitles["displayname"] = "Your display name"
	mydataTitles["reports"] = "Reports you sent"
	mydataTitles["manifests"] = "Provider manifests you have seen"
}

// mydataProfileFiles are every file this client keeps in a profile's folder.
var mydataProfileFiles = []string{"settings.json", followingFileName, "saved.json", muteFileName, namesPetnameFile, nameidFileName, "reports.json", "manifests.json"}

const mydataDamagedFile = "This file could not be read, so its contents are not shown or downloaded."

// mydataMore is what the reader's other cookies and profile files hold.
type mydataMore struct {
	Display                  aprPrefs
	DisplaySaved, DisplayBad bool
	OrderSaved, ForYou       bool
	ShortcutsSaved, NoKeys   bool
	StartSaved, StartHidden  bool
	Saved                    []bmEntry
	SavedBad                 bool
	Mutes                    muteList
	MutesBad                 bool
	Petnames                 map[string]string
	PetnamesBad              bool
	Name                     nameidState
	NameBad                  bool
	Reports                  []reportEntry
	ReportsBad               bool
	Pins                     int
	PinsBad                  bool
}

func mydataHasCookie(r *http.Request, name string) bool {
	_, err := r.Cookie(name)
	return err == nil
}

func (a *App) mydataReadMore(r *http.Request, profile string) mydataMore {
	var m mydataMore
	m.Display, m.DisplayBad = aprRead(r)
	m.DisplaySaved = mydataHasCookie(r, aprCookie)
	m.OrderSaved, m.ForYou = mydataHasCookie(r, fyoOrderCookie), fyoChosen(r) == fyoForYou
	m.ShortcutsSaved, m.NoKeys = mydataHasCookie(r, scCookie), scOff(r)
	m.StartSaved, m.StartHidden = mydataHasCookie(r, startCookie), startHidden(r)
	m.Mutes = muteEmpty()
	if profile == "" {
		return m
	}
	var err error
	if m.Saved, err = a.bmEntries(profile); err != nil {
		m.Saved, m.SavedBad = nil, true
	}
	if m.Mutes, err = a.muteRead(profile); err != nil {
		m.Mutes, m.MutesBad = muteEmpty(), true
	}
	if m.Petnames, err = a.petnames(profile); err != nil {
		m.Petnames, m.PetnamesBad = nil, true
	}
	if m.Name, err = a.nameidLoad(profile); err != nil {
		m.Name, m.NameBad = nameidState{}, true
	}
	m.Reports, m.ReportsBad = a.reportList(profile)
	m.Pins, m.PinsBad = a.mydataPins(profile)
	return m
}

// mydataPins counts the provider manifests pinned for a profile.
func (a *App) mydataPins(profile string) (count int, bad bool) {
	lock := a.profiles.lock(profile)
	lock.Lock()
	defer lock.Unlock()
	_, snapshot, err := a.netReadPins(profile)
	if err != nil {
		return 0, true
	}
	var stored struct {
		Pins map[string]json.RawMessage `json:"pins"`
	}
	if json.Unmarshal(snapshot, &stored) != nil {
		return 0, true
	}
	return len(stored.Pins), false
}

func mydataCookieRow(key, cookie, summary string, saved bool) mydataRow {
	return mydataRow{Key: key, Title: mydataTitles[key], Where: mydataInBrowser, Code: cookie, Suffix: " cookie", Summary: summary, Saved: saved}
}

func mydataFileRow(key, file string, damaged bool) mydataRow {
	row := mydataRow{Key: key, Title: mydataTitles[key], Where: mydataOnServer, Code: file, Suffix: " for this profile"}
	if damaged {
		row.Notice, row.Saved, row.Damaged = mydataDamagedFile, true, true
	}
	return row
}

func mydataCount(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}

func mydataMoreRows(profile string, m mydataMore) []mydataRow {
	return []mydataRow{
		mydataDisplayRow(m), mydataOrderRow(m), mydataShortcutsRow(m), mydataStartRow(m),
		mydataSavedRow(m), mydataMutesRow(m), mydataPetnamesRow(m), mydataNameRow(m), mydataReportsRow(m), mydataPinsRow(m),
	}
}

func mydataDisplayRow(m mydataMore) mydataRow {
	summary := "Defaults (no cookie)"
	switch {
	case m.DisplayBad:
		summary = mydataCouldNotRead
	case m.DisplaySaved:
		p := m.Display
		summary = strings.Join([]string{aprWords(aprAccentOptions(), p.Accent), aprWords(aprSizes, p.Size), aprWords(aprDensities, p.Density), aprWords(aprMotions, p.Motion)}, ", ")
	}
	return mydataCookieRow("display", aprCookie, summary, m.DisplaySaved)
}

func mydataOrderRow(m mydataMore) mydataRow {
	summary := "Newest first (no cookie)"
	switch {
	case m.ForYou:
		summary = "For you, ordered on this server from your likes and dislikes"
	case m.OrderSaved:
		summary = mydataCouldNotRead
	}
	return mydataCookieRow("order", fyoOrderCookie, summary, m.OrderSaved)
}

func mydataShortcutsRow(m mydataMore) mydataRow {
	summary := "On (no cookie)"
	switch {
	case m.NoKeys:
		summary = "Off"
	case m.ShortcutsSaved:
		summary = mydataCouldNotRead
	}
	return mydataCookieRow("shortcuts", scCookie, summary, m.ShortcutsSaved)
}

func mydataStartRow(m mydataMore) mydataRow {
	summary := "Shown (no cookie)"
	switch {
	case m.StartHidden:
		summary = "Hidden"
	case m.StartSaved:
		summary = mydataCouldNotRead
	}
	return mydataCookieRow("start", startCookie, summary, m.StartSaved)
}

func mydataSavedRow(m mydataMore) mydataRow {
	row := mydataFileRow("saved", "saved.json", m.SavedBad)
	if m.SavedBad {
		return row
	}
	row.Summary = "None saved."
	if n := len(m.Saved); n > 0 {
		row.Saved = true
		row.Summary = fmt.Sprintf("%s saved (up to %d).", mydataCount(n, "post", "posts"), bmLimit)
		row.Items = []mydataItem{{Text: "Open Saved", Href: "/saved"}}
	}
	return row
}

func mydataMutesRow(m mydataMore) mydataRow {
	row := mydataFileRow("mutes", muteFileName, m.MutesBad)
	if m.MutesBad {
		return row
	}
	row.Summary = "None saved."
	keys, words := len(m.Mutes.Keys), len(m.Mutes.Words)
	if keys+words == 0 {
		return row
	}
	row.Saved = true
	row.Summary = mydataCount(keys, "key", "keys") + " and " + mydataCount(words, "word or phrase", "words or phrases") + " muted."
	for _, k := range m.Mutes.Keys {
		row.Items = append(row.Items, mydataItem{Text: shortDID(k.AuthorID), Hidden: k.AuthorID, Note: k.Name, Mono: true})
	}
	for _, w := range m.Mutes.Words {
		row.Items = append(row.Items, mydataItem{Text: w.Text})
	}
	row.Details = "Show muted keys and words"
	return row
}

func mydataPetnamesRow(m mydataMore) mydataRow {
	row := mydataFileRow("petnames", namesPetnameFile, m.PetnamesBad)
	if m.PetnamesBad {
		return row
	}
	row.Summary = "None saved."
	if len(m.Petnames) == 0 {
		return row
	}
	row.Saved = true
	row.Summary = mydataCount(len(m.Petnames), "name", "names") + ". Only you see them; they are never published."
	authors := make([]string, 0, len(m.Petnames))
	for author := range m.Petnames {
		authors = append(authors, author)
	}
	sort.Strings(authors)
	for _, author := range authors {
		row.Items = append(row.Items, mydataItem{Text: shortDID(author), Hidden: author, Note: m.Petnames[author], Mono: true})
	}
	row.Details = "Show names"
	return row
}

func mydataNameRow(m mydataMore) mydataRow {
	row := mydataFileRow("displayname", nameidFileName, m.NameBad)
	if m.NameBad {
		return row
	}
	row.Summary = "None saved."
	if m.Name.Local != "" {
		row.Lines = append(row.Lines, "Name kept on this server only: "+m.Name.Local)
	}
	if record := m.Name.Record; record != nil {
		line := "Last name record you signed: a withdrawal"
		if record.DisplayName != nil {
			line = "Last name record you signed: " + *record.DisplayName
		}
		row.Lines = append(row.Lines, line, "Erasing here does not withdraw a name that providers already hold. Withdraw it on Display name first.")
	}
	if m.Name.Pending != nil {
		row.Lines = append(row.Lines, "A name record is waiting to be sent.")
	}
	if len(row.Lines) > 0 || len(m.Name.Outcomes) > 0 {
		row.Saved, row.Summary = true, ""
	}
	return row
}

func mydataReportsRow(m mydataMore) mydataRow {
	row := mydataFileRow("reports", "reports.json", m.ReportsBad)
	if m.ReportsBad {
		return row
	}
	row.Summary = "None saved."
	if len(m.Reports) == 0 {
		return row
	}
	row.Saved = true
	row.Summary = fmt.Sprintf("%s. Only the %d most recent receipts are kept; what you wrote is not kept.", mydataCount(len(m.Reports), "receipt", "receipts"), reportListLimit)
	row.Lines = []string{"Erasing the receipts does not withdraw the reports, and you can no longer check their status from this browser."}
	for _, entry := range m.Reports {
		row.Items = append(row.Items, mydataItem{Text: entry.RequestID, Note: entry.Kind + ", received " + entry.ReceivedAt, Mono: true})
	}
	row.Details = "Show receipts"
	return row
}

func mydataPinsRow(m mydataMore) mydataRow {
	row := mydataFileRow("manifests", "manifests.json", m.PinsBad)
	if m.PinsBad {
		return row
	}
	row.Summary = "None saved."
	if m.Pins == 0 {
		return row
	}
	row.Saved = true
	row.Summary = mydataCount(m.Pins, "provider", "providers") + ". The client remembers the newest manifest each one served, so an older one is refused."
	row.Lines = []string{"After erasing, the next manifest each provider serves is taken as the first one seen."}
	return row
}

// mydataMoreCookie names the cookie of a browser kind and the SameSite mode it is set with.
func mydataMoreCookie(kind string) (name string, sameSite http.SameSite, ok bool) {
	switch kind {
	case "display":
		return aprCookie, http.SameSiteLaxMode, true
	case "order":
		return fyoOrderCookie, http.SameSiteStrictMode, true
	case "shortcuts":
		return scCookie, http.SameSiteLaxMode, true
	case "start":
		return startCookie, http.SameSiteStrictMode, true
	}
	return "", 0, false
}

// mydataMoreFile names the profile file of a server kind.
func mydataMoreFile(kind string) (string, bool) {
	switch kind {
	case "saved":
		return "saved.json", true
	case "mutes":
		return muteFileName, true
	case "petnames":
		return namesPetnameFile, true
	case "displayname":
		return nameidFileName, true
	case "reports":
		return "reports.json", true
	case "manifests":
		return "manifests.json", true
	}
	return "", false
}

// mydataEraseMore erases one of the kinds in this file.
func (a *App) mydataEraseMore(w http.ResponseWriter, r *http.Request, kind, profile string) error {
	if name, sameSite, ok := mydataMoreCookie(kind); ok {
		mydataExpireCookie(w, r, name, sameSite)
		return nil
	}
	file, ok := mydataMoreFile(kind)
	if !ok || profile == "" {
		return nil
	}
	lock := a.profiles.lock(profile)
	lock.Lock()
	defer lock.Unlock()
	return mydataRemove(filepath.Join(a.profiles.root, profile, file))
}

func mydataRemove(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func mydataMoreCookieErased(kind, name string) bool {
	if kind == "everything" {
		return slices.Contains([]string{aprCookie, fyoOrderCookie, scCookie, startCookie}, name)
	}
	cookie, _, ok := mydataMoreCookie(kind)
	return ok && cookie == name
}

// mydataOther is the part of the data file that is carried for reference and never imported.
type mydataOther struct {
	Display      string            `json:"display"`
	FeedOrder    string            `json:"feed_order"`
	Shortcuts    string            `json:"shortcuts"`
	StartPanel   string            `json:"start_panel"`
	SavedPosts   []string          `json:"saved_posts"`
	MutedKeys    []string          `json:"muted_keys"`
	MutedWords   []string          `json:"muted_words"`
	Petnames     map[string]string `json:"petnames"`
	LocalName    string            `json:"local_name"`
	SignedName   string            `json:"signed_name"`
	Reports      []mydataReport    `json:"reports"`
	ManifestPins int               `json:"manifest_pins"`
	Unreadable   []string          `json:"unreadable_files"`
}

type mydataReport struct {
	RequestID  string `json:"request_id"`
	Kind       string `json:"kind"`
	Provider   string `json:"provider_endpoint"`
	ReceivedAt string `json:"received_at"`
}

func mydataOtherFor(m mydataMore) *mydataOther {
	other := &mydataOther{
		FeedOrder: fyoNewest, Shortcuts: "on", StartPanel: "shown",
		SavedPosts: []string{}, MutedKeys: []string{}, MutedWords: []string{}, Petnames: map[string]string{}, Reports: []mydataReport{}, Unreadable: []string{},
		LocalName: m.Name.Local, ManifestPins: m.Pins,
	}
	if m.DisplaySaved && !m.DisplayBad {
		other.Display = m.Display.canonical()
	}
	if m.ForYou {
		other.FeedOrder = fyoForYou
	}
	if m.NoKeys {
		other.Shortcuts = "off"
	}
	if m.StartHidden {
		other.StartPanel = "hidden"
	}
	for _, entry := range m.Saved {
		other.SavedPosts = append(other.SavedPosts, entry.ID)
	}
	for _, k := range m.Mutes.Keys {
		other.MutedKeys = append(other.MutedKeys, k.AuthorID)
	}
	for _, w := range m.Mutes.Words {
		other.MutedWords = append(other.MutedWords, w.Text)
	}
	for author, name := range m.Petnames {
		other.Petnames[author] = name
	}
	if record := m.Name.Record; record != nil && record.DisplayName != nil {
		other.SignedName = *record.DisplayName
	}
	for _, entry := range m.Reports {
		other.Reports = append(other.Reports, mydataReport{RequestID: entry.RequestID, Kind: entry.Kind, Provider: entry.ProviderEndpoint, ReceivedAt: entry.ReceivedAt})
	}
	damaged := []struct {
		bad  bool
		file string
	}{{m.SavedBad, "saved.json"}, {m.MutesBad, muteFileName}, {m.PetnamesBad, namesPetnameFile}, {m.NameBad, nameidFileName}, {m.ReportsBad, "reports.json"}, {m.PinsBad, "manifests.json"}}
	for _, d := range damaged {
		if d.bad {
			other.Unreadable = append(other.Unreadable, d.file)
		}
	}
	return other
}
