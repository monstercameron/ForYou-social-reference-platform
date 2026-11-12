package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/names"
)

func init() { routeGroups = append(routeGroups, registerMute) }

func registerMute(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /muted", a.mutedPageHandler)
	mux.HandleFunc("POST /mute/key", a.muteKeyHandler)
	mux.HandleFunc("POST /mute/key/fragment", a.muteKeyHandler)
	mux.HandleFunc("POST /muted/keys/remove", a.muteKeyRemoveHandler)
	mux.HandleFunc("POST /muted/keys/remove/fragment", a.muteKeyRemoveHandler)
	mux.HandleFunc("POST /muted/words", a.muteWordHandler)
	mux.HandleFunc("POST /muted/words/fragment", a.muteWordHandler)
	mux.HandleFunc("POST /muted/words/remove", a.muteWordRemoveHandler)
	mux.HandleFunc("POST /muted/words/remove/fragment", a.muteWordRemoveHandler)
	mux.HandleFunc("POST /muted/clear", a.muteClearHandler)
}

const (
	muteFileName     = "mutes.json"
	muteFileLimit    = 64 << 10
	muteKeyLimit     = 64
	muteWordLimit    = 32
	muteNameRunes    = 64
	muteWordMinRunes = 2
	muteWordMaxRunes = 40
	muteDamagedText  = "Your mute list on this server could not be read. Clear it on the Muted page to start over."
	muteDamageNotice = "Your mute list on this server could not be read, so nothing is folded by it. Clear it to start over."
	muteKeyFull      = "You can mute up to 64 keys. Unmute one on the Muted page first."
	muteWordFull     = "You can mute up to 32 words or phrases. Unmute one first."
)

var errMuteDamaged = errors.New("mute list is damaged")

// muteInputError is a refusal the reader can correct, and its text is shown to them.
type muteInputError string

func (e muteInputError) Error() string { return string(e) }

// muteList is the per-profile mute list, kept on this server only.
type muteList struct {
	Version int        `json:"version"`
	Keys    []muteKey  `json:"keys"`
	Words   []muteWord `json:"words"`
}

type muteKey struct {
	AuthorID string    `json:"author_id"`
	Name     string    `json:"name,omitempty"`
	MutedAt  time.Time `json:"muted_at"`
}

type muteWord struct {
	Text    string    `json:"text"`
	MutedAt time.Time `json:"muted_at"`
}

func muteEmpty() muteList {
	return muteList{Version: 1, Keys: []muteKey{}, Words: []muteWord{}}
}

// muteRead returns the profile's mute list. A missing file is an empty list;
// a file that breaks the stored format is errMuteDamaged and folds nothing.
func (a *App) muteRead(id string) (muteList, error) {
	if !validProfileID(id) {
		return muteEmpty(), nil
	}
	file, err := os.Open(filepath.Join(a.profiles.root, id, muteFileName))
	if errors.Is(err, os.ErrNotExist) {
		return muteEmpty(), nil
	}
	if err != nil {
		return muteList{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, muteFileLimit+1))
	if err != nil {
		return muteList{}, err
	}
	if len(data) > muteFileLimit {
		return muteList{}, errMuteDamaged
	}
	return muteDecode(data)
}

func muteDecode(data []byte) (muteList, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var list muteList
	if err := decoder.Decode(&list); err != nil {
		return muteList{}, errMuteDamaged
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return muteList{}, errMuteDamaged
	}
	if !list.valid() {
		return muteList{}, errMuteDamaged
	}
	if list.Keys == nil {
		list.Keys = []muteKey{}
	}
	if list.Words == nil {
		list.Words = []muteWord{}
	}
	return list, nil
}

func (l muteList) valid() bool {
	if l.Version != 1 || len(l.Keys) > muteKeyLimit || len(l.Words) > muteWordLimit {
		return false
	}
	seen := make(map[string]bool, len(l.Keys))
	for _, k := range l.Keys {
		if _, err := keys.ParseDID(k.AuthorID); err != nil || seen[k.AuthorID] || k.MutedAt.IsZero() {
			return false
		}
		if k.Name != "" && (!utf8.ValidString(k.Name) || utf8.RuneCountInString(k.Name) > muteNameRunes) {
			return false
		}
		seen[k.AuthorID] = true
	}
	words := make(map[string]bool, len(l.Words))
	for _, w := range l.Words {
		normalized, err := muteNormalize(w.Text)
		if err != nil || normalized != w.Text || words[w.Text] || w.MutedAt.IsZero() {
			return false
		}
		words[w.Text] = true
	}
	return true
}

// muteWrite changes the profile's mute list under its lock and replaces the file atomically.
func (a *App) muteWrite(id string, change func(*muteList) error) error {
	lock := a.profiles.lock(id)
	lock.Lock()
	defer lock.Unlock()
	list, err := a.muteRead(id)
	if err != nil {
		return err
	}
	if err := change(&list); err != nil {
		return err
	}
	return a.muteSave(id, list)
}

func (a *App) muteSave(id string, list muteList) error {
	if list.Keys == nil {
		list.Keys = []muteKey{}
	}
	if list.Words == nil {
		list.Words = []muteWord{}
	}
	dir := filepath.Join(a.profiles.root, id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".mutes-*")
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
	if err := os.Rename(temporaryName, filepath.Join(dir, muteFileName)); err != nil {
		return err
	}
	return nil
}

func (a *App) muteClear(id string) error {
	lock := a.profiles.lock(id)
	lock.Lock()
	defer lock.Unlock()
	err := os.Remove(filepath.Join(a.profiles.root, id, muteFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (l *muteList) addKey(p post, at time.Time) error {
	if slices.ContainsFunc(l.Keys, func(k muteKey) bool { return k.AuthorID == p.AuthorID }) {
		return nil
	}
	if len(l.Keys) >= muteKeyLimit {
		return muteInputError(muteKeyFull)
	}
	l.Keys = append(l.Keys, muteKey{AuthorID: p.AuthorID, Name: muteTruncate(p.SignedName, muteNameRunes), MutedAt: at})
	return nil
}

func (l *muteList) removeKey(authorID string) {
	l.Keys = slices.DeleteFunc(l.Keys, func(k muteKey) bool { return k.AuthorID == authorID })
}

func (l *muteList) addWord(word string, at time.Time) error {
	if slices.ContainsFunc(l.Words, func(w muteWord) bool { return w.Text == word }) {
		return muteInputError("That word is already muted.")
	}
	if len(l.Words) >= muteWordLimit {
		return muteInputError(muteWordFull)
	}
	l.Words = append(l.Words, muteWord{Text: word, MutedAt: at})
	return nil
}

func (l *muteList) removeWord(word string) {
	l.Words = slices.DeleteFunc(l.Words, func(w muteWord) bool { return w.Text == word })
}

// muteNormalize trims a word, collapses its whitespace and lowercases it, and
// refuses what cannot be a word of two to forty characters.
func muteNormalize(text string) (string, error) {
	if !utf8.ValidString(text) {
		return "", muteInputError("Use plain text without control characters.")
	}
	word := strings.ToLower(strings.Join(strings.Fields(text), " "))
	for _, r := range word {
		if unicode.IsControl(r) {
			return "", muteInputError("Use plain text without control characters.")
		}
	}
	if length := utf8.RuneCountInString(word); length < muteWordMinRunes || length > muteWordMaxRunes {
		return "", muteInputError("Use 2 to 40 characters.")
	}
	return word, nil
}

// muteMatch reports whether word occurs in text as whole words, ignoring case and whitespace runs.
func muteMatch(text, word string) bool {
	if word == "" {
		return false
	}
	text = strings.Join(strings.Fields(strings.ToLower(text)), " ")
	for start := 0; ; {
		i := strings.Index(text[start:], word)
		if i < 0 {
			return false
		}
		at := start + i
		before, _ := utf8.DecodeLastRuneInString(text[:at])
		after, _ := utf8.DecodeRuneInString(text[at+len(word):])
		if !muteWordRune(before) && !muteWordRune(after) {
			return true
		}
		_, size := utf8.DecodeRuneInString(text[at:])
		start = at + size
	}
}

func muteWordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r)
}

func muteTruncate(s string, runes int) string {
	if utf8.RuneCountInString(s) <= runes {
		return s
	}
	return string([]rune(s)[:runes])
}

func muteFirstWord(list muteList, text string) (string, bool) {
	for _, w := range list.Words {
		if muteMatch(text, w.Text) {
			return w.Text, true
		}
	}
	return "", false
}

func muteWordLine(word string) ratingLine {
	return ratingLine{Name: "Muted word", Level: "“" + word + "”"}
}

func muteKeyLabel(p post) string {
	fingerprint := names.Fingerprint(p.AuthorID)
	if p.SignedName == "" {
		return fingerprint
	}
	return p.SignedName + ", " + fingerprint
}

// MuteFingerprint is the short fingerprint of the post's author key, or empty when it has none.
func (p post) MuteFingerprint() string {
	if p.AuthorID == "" {
		return ""
	}
	return names.Fingerprint(p.AuthorID)
}

// MuteKeyMuted reports whether this reader's mute list folds the post's author key.
func (p post) MuteKeyMuted() bool {
	return slices.ContainsFunc(p.Rating.Over, func(line ratingLine) bool { return line.Name == "Muted key" })
}

// muteReader reads the mute list of the reader's browser profile. A damaged
// list folds nothing, and a request without a profile has no list.
func (a *App) muteReader(r *http.Request) muteList {
	list, err := a.muteRead(requestProfile(r))
	if err != nil {
		return muteEmpty()
	}
	return list
}

// muteFold folds posts whose author key or words the reader muted. Folded
// posts stay in the feed and in every count.
func (a *App) muteFold(r *http.Request, posts []post) {
	list := a.muteReader(r)
	if len(list.Keys) == 0 && len(list.Words) == 0 {
		return
	}
	for i := range posts {
		p := &posts[i]
		if p.AuthorID != "" && slices.ContainsFunc(list.Keys, func(k muteKey) bool { return k.AuthorID == p.AuthorID }) {
			p.Rating.Over = append(p.Rating.Over, ratingLine{Name: "Muted key", Level: muteKeyLabel(*p)})
		}
		if word, ok := muteFirstWord(list, p.Text); ok {
			p.Rating.Over = append(p.Rating.Over, muteWordLine(word))
		}
	}
}

// muteFoldReplies folds replies that contain a muted word, at every depth.
// Reply counts and paths are not changed.
func (a *App) muteFoldReplies(r *http.Request, replies []reply) {
	list := a.muteReader(r)
	if len(list.Words) == 0 {
		return
	}
	muteFoldReplyList(list, replies)
}

func muteFoldReplyList(list muteList, replies []reply) {
	for i := range replies {
		if word, ok := muteFirstWord(list, replies[i].Text); ok {
			replies[i].Rating.Over = append(replies[i].Rating.Over, muteWordLine(word))
		}
		muteFoldReplyList(list, replies[i].Children)
	}
}

// muteResponse is the post menu after a change, answered as a fragment.
type muteResponse struct {
	Post   post
	Status string
}

type mutedKeyRow struct {
	AuthorID, Name, Fingerprint, MutedOn, Label string
}

// mutedView is what the Muted page and its sections show.
type mutedView struct {
	Layout                  Layout
	Keys                    []mutedKeyRow
	Words                   []string
	KeyLimit, WordLimit     int
	WordsFull, Damaged      bool
	Clearable               bool
	DamageText              string
	KeysStatus, WordsStatus string
	WordError, WordValue    string
}

func (a *App) muteView(r *http.Request, id string) mutedView {
	layout := a.layout(r, "muted", "Muted")
	layout.Rightbar = false
	view := mutedView{Layout: layout, KeyLimit: muteKeyLimit, WordLimit: muteWordLimit, DamageText: muteDamageNotice}
	list, err := a.muteRead(id)
	if err != nil {
		view.Damaged = true
		return view
	}
	for _, k := range list.Keys {
		name := k.Name
		label := "Unmute key " + names.Fingerprint(k.AuthorID)
		if name != "" {
			label = "Unmute " + name + " " + names.Fingerprint(k.AuthorID)
		}
		view.Keys = append(view.Keys, mutedKeyRow{AuthorID: k.AuthorID, Name: name, Fingerprint: names.Fingerprint(k.AuthorID), MutedOn: k.MutedAt.UTC().Format("2 Jan 2006"), Label: label})
	}
	for _, w := range list.Words {
		view.Words = append(view.Words, w.Text)
	}
	view.WordsFull = len(list.Words) >= muteWordLimit
	view.Clearable = len(list.Keys)+len(list.Words) > 0
	return view
}

func (v *mutedView) markSaved(saved string) {
	switch saved {
	case "key-added":
		v.KeysStatus = "Key muted."
	case "key-removed":
		v.KeysStatus = "Key unmuted."
	case "cleared":
		v.KeysStatus = "Mute list cleared."
	case "word-added":
		v.WordsStatus = "Word muted."
	case "word-removed":
		v.WordsStatus = "Word unmuted."
	}
}

func muteIsFragment(r *http.Request) bool {
	return strings.HasSuffix(r.URL.Path, "/fragment")
}

// muteDamaged refuses a write to a damaged list: plain text for a fragment,
// and the Muted page with the reason for a full page.
func (a *App) muteDamaged(w http.ResponseWriter, r *http.Request, id string) {
	if muteIsFragment(r) {
		http.Error(w, muteDamagedText, http.StatusConflict)
		return
	}
	view := a.muteView(r, id)
	view.DamageText = muteDamagedText
	a.renderStatus(w, r, "mute-page", view, http.StatusConflict)
}

func (a *App) mutedPageHandler(w http.ResponseWriter, r *http.Request) {
	view := a.muteView(r, requestProfile(r))
	view.markSaved(r.URL.Query().Get("saved"))
	w.Header().Set("Cache-Control", "private, no-store")
	a.page(w, r, "mute-page", view)
}

func (a *App) muteKeyHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := readActionForm(w, r, "id", "mute")
	if !ok {
		return
	}
	choice := form.Get("mute")
	if (choice != "on" && choice != "off") || len(form.Get("id")) > 80 {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	p, found := a.requestPost(r, form.Get("id"))
	if !found || p.AuthorID == "" {
		http.NotFound(w, r)
		return
	}
	id, err := a.profile(w, r)
	if err != nil {
		http.Error(w, "unable to save", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	at := a.now().UTC().Truncate(time.Second)
	err = a.muteWrite(id, func(list *muteList) error {
		if choice == "off" {
			list.removeKey(p.AuthorID)
			return nil
		}
		return list.addKey(p, at)
	})
	var refused muteInputError
	switch {
	case errors.Is(err, errMuteDamaged):
		a.muteDamaged(w, r, id)
		return
	case errors.As(err, &refused):
		http.Error(w, string(refused), http.StatusConflict)
		return
	case err != nil:
		http.Error(w, "unable to save", http.StatusInternalServerError)
		return
	}
	if !muteIsFragment(r) {
		saved := "key-removed"
		if choice == "on" {
			saved = "key-added"
		}
		http.Redirect(w, r, "/muted?saved="+saved, http.StatusSeeOther)
		return
	}
	status := "Unmuted. Posts by this key show normally the next time a page loads."
	if choice == "on" {
		status = "Muted. Posts by this key fold the next time a page loads."
	}
	if refreshed, found := a.requestPost(r, p.ID); found {
		p = refreshed
	}
	a.fragment(w, r, "mute-card-response", muteResponse{Post: p, Status: status})
}

func (a *App) muteKeyRemoveHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := readActionForm(w, r, "key")
	if !ok {
		return
	}
	if _, err := keys.ParseDID(form.Get("key")); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	id, err := a.profile(w, r)
	if err != nil {
		http.Error(w, "unable to save", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	err = a.muteWrite(id, func(list *muteList) error {
		list.removeKey(form.Get("key"))
		return nil
	})
	if errors.Is(err, errMuteDamaged) {
		a.muteDamaged(w, r, id)
		return
	}
	if err != nil {
		http.Error(w, "unable to save", http.StatusInternalServerError)
		return
	}
	if !muteIsFragment(r) {
		http.Redirect(w, r, "/muted?saved=key-removed#muted-keys", http.StatusSeeOther)
		return
	}
	view := a.muteView(r, id)
	view.KeysStatus = "Key unmuted."
	a.fragment(w, r, "mute-keys", view)
}

func (a *App) muteWordHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := readActionForm(w, r, "word")
	if !ok {
		return
	}
	id, err := a.profile(w, r)
	if err != nil {
		http.Error(w, "unable to save", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	at := a.now().UTC().Truncate(time.Second)
	word, err := muteNormalize(form.Get("word"))
	if err == nil {
		err = a.muteWrite(id, func(list *muteList) error { return list.addWord(word, at) })
	}
	var refused muteInputError
	switch {
	case errors.Is(err, errMuteDamaged):
		a.muteDamaged(w, r, id)
		return
	case errors.As(err, &refused):
		view := a.muteView(r, id)
		view.WordError, view.WordValue = string(refused), form.Get("word")
		if !muteIsFragment(r) {
			a.renderStatus(w, r, "mute-page", view, http.StatusUnprocessableEntity)
			return
		}
		a.fragment(w, r, "mute-words", view)
		return
	case err != nil:
		http.Error(w, "unable to save", http.StatusInternalServerError)
		return
	}
	if !muteIsFragment(r) {
		http.Redirect(w, r, "/muted?saved=word-added#muted-words", http.StatusSeeOther)
		return
	}
	view := a.muteView(r, id)
	view.WordsStatus = "Word muted."
	a.fragment(w, r, "mute-words", view)
}

func (a *App) muteWordRemoveHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := readActionForm(w, r, "word")
	if !ok {
		return
	}
	id, err := a.profile(w, r)
	if err != nil {
		http.Error(w, "unable to save", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	word, _ := muteNormalize(form.Get("word"))
	err = a.muteWrite(id, func(list *muteList) error {
		list.removeWord(word)
		return nil
	})
	if errors.Is(err, errMuteDamaged) {
		a.muteDamaged(w, r, id)
		return
	}
	if err != nil {
		http.Error(w, "unable to save", http.StatusInternalServerError)
		return
	}
	if !muteIsFragment(r) {
		http.Redirect(w, r, "/muted?saved=word-removed#muted-words", http.StatusSeeOther)
		return
	}
	view := a.muteView(r, id)
	view.WordsStatus = "Word unmuted."
	a.fragment(w, r, "mute-words", view)
}

func (a *App) muteClearHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := readActionForm(w, r); !ok {
		return
	}
	id, err := a.profile(w, r)
	if err != nil {
		http.Error(w, "unable to clear", http.StatusInternalServerError)
		return
	}
	if err := a.muteClear(id); err != nil {
		http.Error(w, "unable to clear", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/muted?saved=cleared", http.StatusSeeOther)
}
