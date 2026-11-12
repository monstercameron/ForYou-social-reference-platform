package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"time"
	"unicode"
	"unicode/utf8"
)

func init() { routeGroups = append(routeGroups, bmRegister) }

func bmRegister(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /saved", a.bmPageHandler)
	mux.HandleFunc("GET /saved/item", a.bmItemHandler)
	mux.HandleFunc("GET /saved/control", a.bmControlHandler)
	mux.HandleFunc("POST /saved/toggle", a.bmToggleHandler)
	mux.HandleFunc("POST /saved/toggle/fragment", a.bmToggleHandler)
	mux.HandleFunc("POST /saved/remove", a.bmRemoveHandler)
	mux.HandleFunc("POST /saved/remove/fragment", a.bmRemoveHandler)
	mux.HandleFunc("POST /saved/clear", a.bmClearHandler)
	mux.HandleFunc("POST /saved/clear/fragment", a.bmClearHandler)
}

const (
	bmVersion      = 1
	bmLimit        = 500
	bmPageSize     = 20
	bmMaxPage      = 25
	bmMaxIDBytes   = 128
	bmMaxFileBytes = 128 << 10
)

var (
	bmErrDamaged = errors.New("saved list is damaged")
	bmErrFull    = errors.New("saved list is full")
)

// bmFile is the saved list stored as saved.json. It is local application state for one reader, not protocol state.
type bmFile struct {
	Version int       `json:"version"`
	Entries []bmEntry `json:"entries"`
}

type bmEntry struct {
	ID      string    `json:"id"`
	SavedAt time.Time `json:"saved_at"`
}

// bmControl is the save control for one post and the outcome of the last toggle.
type bmControl struct {
	ID      string
	Saved   bool
	Status  string
	Problem string
}

type bmItem struct {
	ID, Short, Label string
	SavedAt          time.Time
	Found            bool
	Posts            []post
	Page             int
}

type bmList struct {
	Entries                           []bmItem
	Total                             int
	CountText, Status                 string
	Page, Prev, Next                  int
	Older, Newer, OutOfRange, Damaged bool
}

type bmPage struct {
	Layout Layout
	List   bmList
}

type bmItemPage struct {
	Layout  Layout
	Posts   []post
	Control bmControl
}

func (a *App) bmPath(profile string) string {
	return filepath.Join(a.profiles.root, profile, "saved.json")
}

func bmValidID(id string) bool {
	if len(id) == 0 || len(id) > bmMaxIDBytes || !utf8.ValidString(id) {
		return false
	}
	for _, r := range id {
		if unicode.IsControl(r) || unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

func bmParsePage(value string) (int, bool) {
	page, err := strconv.Atoi(value)
	return page, err == nil && page >= 1 && page <= bmMaxPage
}

func bmFind(entries []bmEntry, id string) int {
	return slices.IndexFunc(entries, func(e bmEntry) bool { return e.ID == id })
}

func bmRemove(list *bmFile, id string) bool {
	i := bmFind(list.Entries, id)
	if i < 0 {
		return false
	}
	list.Entries = slices.Delete(list.Entries, i, i+1)
	return true
}

func bmAdd(list *bmFile, id string, now time.Time) (bool, error) {
	if bmFind(list.Entries, id) >= 0 {
		return false, nil
	}
	if len(list.Entries) >= bmLimit {
		return false, bmErrFull
	}
	list.Entries = append(list.Entries, bmEntry{ID: id, SavedAt: now})
	return true, nil
}

// bmDecode rejects anything that is not a version 1 list with valid, unique ids and dated entries.
func bmDecode(data []byte) (bmFile, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var list bmFile
	if err := decoder.Decode(&list); err != nil {
		return bmFile{}, bmErrDamaged
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return bmFile{}, bmErrDamaged
	}
	if list.Version != bmVersion || len(list.Entries) > bmLimit {
		return bmFile{}, bmErrDamaged
	}
	seen := make(map[string]bool, len(list.Entries))
	for _, e := range list.Entries {
		if !bmValidID(e.ID) || seen[e.ID] || e.SavedAt.IsZero() {
			return bmFile{}, bmErrDamaged
		}
		seen[e.ID] = true
	}
	return list, nil
}

// bmRead returns the stored list; a missing file is an empty list.
func bmRead(path string) (bmFile, error) {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return bmFile{Version: bmVersion}, nil
	}
	if err != nil {
		return bmFile{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, bmMaxFileBytes+1))
	if err != nil {
		return bmFile{}, err
	}
	if len(data) > bmMaxFileBytes {
		return bmFile{}, bmErrDamaged
	}
	return bmDecode(data)
}

// bmWrite replaces the stored list atomically, as the profile settings are replaced.
func bmWrite(dir, path string, list bmFile) error {
	list.Version = bmVersion
	if list.Entries == nil {
		list.Entries = []bmEntry{}
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".saved-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return err
	}
	if err := json.NewEncoder(temporary).Encode(list); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, path)
}

// bmMutate applies change under the profile lock and writes only when it reports a change.
func (a *App) bmMutate(profile string, change func(*bmFile) (bool, error)) error {
	lock := a.profiles.lock(profile)
	lock.Lock()
	defer lock.Unlock()
	path := a.bmPath(profile)
	list, err := bmRead(path)
	if err != nil {
		return err
	}
	changed, err := change(&list)
	if err != nil || !changed {
		return err
	}
	return bmWrite(filepath.Join(a.profiles.root, profile), path, list)
}

func (a *App) bmEntries(profile string) ([]bmEntry, error) {
	lock := a.profiles.lock(profile)
	lock.Lock()
	defer lock.Unlock()
	list, err := bmRead(a.bmPath(profile))
	return list.Entries, err
}

func (a *App) bmClear(profile string) error {
	lock := a.profiles.lock(profile)
	lock.Lock()
	defer lock.Unlock()
	err := os.Remove(a.bmPath(profile))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// bmState returns the reader's saved entries and whether the saved list cannot be read.
func (a *App) bmState(r *http.Request) ([]bmEntry, bool) {
	profile := requestProfile(r)
	if profile == "" {
		return nil, false
	}
	entries, err := a.bmEntries(profile)
	return entries, err != nil
}

func (a *App) bmApply(profile, id, want string) (bmControl, int) {
	err := a.bmMutate(profile, func(list *bmFile) (bool, error) {
		if want == "remove" {
			return bmRemove(list, id), nil
		}
		return bmAdd(list, id, a.now().UTC().Truncate(time.Second))
	})
	control := bmControl{ID: id}
	switch {
	case errors.Is(err, bmErrDamaged):
		control.Problem = "damaged"
		return control, http.StatusConflict
	case errors.Is(err, bmErrFull):
		control.Problem = "full"
		return control, http.StatusConflict
	case err != nil:
		return control, http.StatusInternalServerError
	}
	control.Saved = want == "save"
	control.Status = bmDoneText("removed")
	if control.Saved {
		control.Status = bmDoneText("saved")
	}
	return control, http.StatusOK
}

func (a *App) bmControlFor(r *http.Request, id string) bmControl {
	entries, damaged := a.bmState(r)
	return bmControl{ID: id, Saved: !damaged && bmFind(entries, id) >= 0}
}

func bmDoneText(done string) string {
	switch done {
	case "saved":
		return "Saved on this server for this browser."
	case "removed":
		return "Removed from Saved."
	case "cleared":
		return "Cleared. Nothing saved."
	}
	return ""
}

func bmLeftText(left int) string {
	switch left {
	case 0:
		return "Removed. Nothing saved."
	case 1:
		return "Removed. 1 saved post left."
	}
	return fmt.Sprintf("Removed. %d saved posts left.", left)
}

// bmAdjustPage moves back a page when removals empty the page being shown.
func bmAdjustPage(page, total int) int {
	if page > 1 && (page-1)*bmPageSize >= total {
		return page - 1
	}
	return page
}

// bmShort shortens an id for display; the full id stays in the element's title.
func bmShort(id string) string {
	runes := []rune(id)
	if len(runes) <= 12 {
		return id
	}
	return string(runes[:6]) + "…" + string(runes[len(runes)-4:])
}

func bmNewestFirst(entries []bmEntry) []bmEntry {
	newest := slices.Clone(entries)
	slices.Reverse(newest)
	slices.SortStableFunc(newest, func(a, b bmEntry) int { return b.SavedAt.Compare(a.SavedAt) })
	return newest
}

// bmBuildList lays out one page of the saved list. Posts that the sources no longer return stay listed as missing.
func (a *App) bmBuildList(r *http.Request, entries []bmEntry, damaged bool, page int, status string) bmList {
	list := bmList{Page: page, Prev: page - 1, Next: page + 1, Newer: page > 1, Status: status, Damaged: damaged}
	if damaged {
		return list
	}
	newest := bmNewestFirst(entries)
	list.Total = len(newest)
	switch {
	case list.Total == 1:
		list.CountText = "1 saved post"
	case list.Total > 1:
		list.CountText = fmt.Sprintf("%d saved posts", list.Total)
	}
	start := min((page-1)*bmPageSize, len(newest))
	end := min(start+bmPageSize, len(newest))
	list.OutOfRange = start == len(newest) && len(newest) > 0
	list.Older = end < len(newest)
	for _, e := range newest[start:end] {
		item := bmItem{ID: e.ID, Short: bmShort(e.ID), SavedAt: e.SavedAt, Page: page}
		item.Label = item.Short
		if p, found := a.requestPost(r, e.ID); found {
			item.Found = true
			item.Posts = []post{p}
			item.Label = p.Receipt
		}
		list.Entries = append(list.Entries, item)
	}
	return list
}

func (a *App) bmPageHandler(w http.ResponseWriter, r *http.Request) {
	values := r.URL.Query()
	page := 1
	if values.Has("page") {
		parsed, ok := bmParsePage(values.Get("page"))
		if !ok {
			a.fail(w, r, http.StatusBadRequest, "Choose a page from 1 to 25.")
			return
		}
		page = parsed
	}
	status := ""
	if done := values.Get("done"); done == "removed" || done == "cleared" {
		status = bmDoneText(done)
	}
	entries, damaged := a.bmState(r)
	list := a.bmBuildList(r, entries, damaged, page, status)
	w.Header().Set("Cache-Control", "private, no-store")
	a.page(w, r, "bookmarks-page", bmPage{Layout: a.layout(r, "saved", "Saved"), List: list})
}

func (a *App) bmItemHandler(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if !bmValidID(id) {
		a.fail(w, r, http.StatusBadRequest, "This saved post link is not valid.")
		return
	}
	p, found := a.requestPost(r, id)
	if !found {
		a.fail(w, r, http.StatusNotFound, "This post is not available from your sources.")
		return
	}
	control := a.bmControlFor(r, id)
	if done := r.URL.Query().Get("done"); done == "saved" && control.Saved || done == "removed" && !control.Saved {
		control.Status = bmDoneText(done)
	}
	w.Header().Set("Cache-Control", "private, no-store")
	a.page(w, r, "bookmarks-item-page", bmItemPage{Layout: a.layout(r, "saved", "Save post"), Posts: []post{p}, Control: control})
}

func (a *App) bmControlHandler(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if !bmValidID(id) {
		a.fail(w, r, http.StatusBadRequest, "This saved post link is not valid.")
		return
	}
	if _, found := a.requestPost(r, id); !found {
		a.fail(w, r, http.StatusNotFound, "This post is not available from your sources.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	a.fragment(w, r, "bookmarks-control", a.bmControlFor(r, id))
}

func (a *App) bmToggleHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := readActionForm(w, r, "id", "want")
	if !ok {
		return
	}
	id, want := form.Get("id"), form.Get("want")
	if !bmValidID(id) || (want != "save" && want != "remove") {
		http.Error(w, "invalid saved post", http.StatusBadRequest)
		return
	}
	var posts []post
	if p, found := a.requestPost(r, id); found {
		posts = []post{p}
	} else if want == "save" {
		a.fail(w, r, http.StatusNotFound, "This post is not available from your sources.")
		return
	}
	profile, err := a.profile(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "Could not update your saved list. Try again.")
		return
	}
	control, status := a.bmApply(profile, id, want)
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/saved/toggle/fragment" {
		a.renderStatus(w, r, "bookmarks-control", control, status)
		return
	}
	if status != http.StatusOK {
		a.renderStatus(w, r, "bookmarks-item-page", bmItemPage{Layout: a.layout(r, "saved", "Save post"), Posts: posts, Control: control}, status)
		return
	}
	done := "removed"
	if want == "save" {
		done = "saved"
	}
	http.Redirect(w, r, "/saved/item?id="+url.QueryEscape(id)+"&done="+done, http.StatusSeeOther)
}

func (a *App) bmRemoveHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := readActionForm(w, r, "id", "page")
	if !ok {
		return
	}
	id := form.Get("id")
	page, pageOK := bmParsePage(form.Get("page"))
	if !bmValidID(id) || !pageOK {
		http.Error(w, "invalid saved post", http.StatusBadRequest)
		return
	}
	if profile := requestProfile(r); profile != "" {
		err := a.bmMutate(profile, func(list *bmFile) (bool, error) { return bmRemove(list, id), nil })
		if err != nil && !errors.Is(err, bmErrDamaged) {
			a.fail(w, r, http.StatusInternalServerError, "Could not update your saved list. Try again.")
			return
		}
	}
	entries, damaged := a.bmState(r)
	status := ""
	if !damaged {
		page = bmAdjustPage(page, len(entries))
		status = bmLeftText(len(entries))
	}
	list := a.bmBuildList(r, entries, damaged, page, status)
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/saved/remove/fragment" {
		code := http.StatusOK
		if damaged {
			code = http.StatusConflict
		}
		a.renderStatus(w, r, "bookmarks-list", list, code)
		return
	}
	if damaged {
		a.renderStatus(w, r, "bookmarks-page", bmPage{Layout: a.layout(r, "saved", "Saved"), List: list}, http.StatusConflict)
		return
	}
	http.Redirect(w, r, fmt.Sprintf("/saved?page=%d&done=removed", page), http.StatusSeeOther)
}

func (a *App) bmClearHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := readActionForm(w, r); !ok {
		return
	}
	if profile := requestProfile(r); profile != "" {
		if err := a.bmClear(profile); err != nil {
			a.fail(w, r, http.StatusInternalServerError, "Could not clear your saved list. Try again.")
			return
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/saved/clear/fragment" {
		a.fragment(w, r, "bookmarks-list", a.bmBuildList(r, nil, false, 1, bmDoneText("cleared")))
		return
	}
	http.Redirect(w, r, "/saved?done=cleared", http.StatusSeeOther)
}
