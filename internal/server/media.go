package server

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"foryou.local/foryou/internal/content"
	"foryou.local/foryou/internal/wire"
)

func init() { routeGroups = append(routeGroups, registerMedia) }

func registerMedia(a *App, mux *http.ServeMux) {
	mux.HandleFunc("POST /media/load", a.medLoadHandler)
	mux.HandleFunc("POST /media/stop", a.medStopHandler)
	mux.HandleFunc("GET /media/{id}/{slot}", a.medObjectHandler)
	mux.HandleFunc("GET /viewer/{id}", a.medViewerHandler)
}

// Slot states as the figure templates show them.
const (
	medNotLoaded   = "not-loaded"
	medLoading     = "loading"
	medImage       = "image"
	medVideo       = "video"
	medMismatch    = "mismatch"
	medTooLarge    = "too-large"
	medUnsupported = "unsupported"
	medBlocked     = "blocked"
	medUnavailable = "unavailable"
	medRedirect    = "redirect"
	medInvalid     = "invalid"
)

const (
	// medClickWindow is how long a click on one item lets its object be shown.
	medClickWindow = 10 * time.Minute
	// medFailureWindow is how long a failed load is shown before the item is tried again.
	medFailureWindow = 10 * time.Minute
)

var medSlotIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// medRecord reads a record the reader can see. Tests replace it to supply records without a provider.
var medRecord = func(a *App, r *http.Request, id string) (post, bool, error) {
	p, _, ok, err := a.records().Get(r.Context(), r, id)
	return p, ok, err
}

// medState is the per-server state of media loading: verified objects, click and failure records.
type medState struct {
	cache    *medCache
	fetch    *medFetcher
	mu       sync.Mutex
	clicks   map[string]medClick
	failures map[string]medFailure
}

// medClick is a reader's click on one item: the time it was made and the origin the item loads from.
type medClick struct {
	at     time.Time
	origin string
}

// medFailure is the outcome of a load that did not produce a verified object.
type medFailure struct {
	state, text, moved string
	at                 time.Time
}

var medStates sync.Map

// medStateFor returns the media state of a server, created on first use.
func medStateFor(a *App) *medState {
	if state, ok := medStates.Load(a); ok {
		return state.(*medState)
	}
	cache := newMedCache()
	state, _ := medStates.LoadOrStore(a, &medState{cache: cache, fetch: newMedFetcher(cache), clicks: map[string]medClick{}, failures: map[string]medFailure{}})
	return state.(*medState)
}

func (s *medState) markClick(key, origin string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clicks[key] = medClick{at: now, origin: origin}
}

func (s *medState) clicked(key string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	click, ok := s.clicks[key]
	return ok && now.Sub(click.at) < medClickWindow
}

// revokeOrigin ends the click windows a profile opened on one origin and cancels the fetches that reach it.
func (s *medState) revokeOrigin(profile, origin string) {
	s.mu.Lock()
	for key, click := range s.clicks {
		if click.origin == origin && strings.HasPrefix(key, profile+"\x00") {
			delete(s.clicks, key)
		}
	}
	s.mu.Unlock()
	s.fetch.cancelOrigin(origin)
}

func (s *medState) setFailure(key string, failure medFailure) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures[key] = failure
}

func (s *medState) clearFailure(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.failures, key)
}

func (s *medState) failure(key string, now time.Time) (medFailure, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	failure, ok := s.failures[key]
	return failure, ok && now.Sub(failure.at) < medFailureWindow
}

// medClickKey names one item of one post for one profile.
func medClickKey(profile, postID, slotID string) string {
	return profile + "\x00" + postID + "\x00" + slotID
}

// medSlotView is one media slot as a reader sees it, ready for the figure templates.
type medSlotView struct {
	FigureID, PostID, SlotID string
	State, Text, Pill        string
	Kind, Label, Alt         string
	Host, Origin, Declared   string
	Src, Button, Approve     string
	Width, Height            int
	Index, Count             int
	Loaded, Lazy, Stop       bool
}

// medGrid is the set of slots of one post, with the viewer link when it has two or more verified images.
type medGrid struct {
	PostID    string
	Count     int
	Slots     []medSlotView
	ViewerURL string
}

// medViewerPage is the full viewer of one post's media, or its stage when HTMLeX swaps it.
type medViewerPage struct {
	Layout  Layout
	Post    post
	Grid    medGrid
	Current medSlotView
	Prev    string
	Next    string
}

func medFigureID(postID, slotID string) string {
	var b strings.Builder
	b.WriteString("media-")
	for _, c := range postID[4:16] + "-" + slotID {
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '-' {
			b.WriteRune(c)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}

func medAlt(slot wire.MediaReference) string {
	if slot.AltText != nil && *slot.AltText != "" {
		return *slot.AltText
	}
	return "No description provided by the author"
}

func medDeclaredSize(size int64) string {
	switch {
	case size <= 0:
		return "size not declared"
	case size < 1<<20:
		return fmt.Sprintf("%d KB", (size+1023)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(size)/(1<<20))
}

func medKindLabel(kind string) string {
	if kind == "video" {
		return "Video"
	}
	return "Image"
}

func medKindMatches(t content.MediaType, kind string) bool {
	switch kind {
	case "image":
		return strings.HasPrefix(string(t), "image/")
	case "video":
		return strings.HasPrefix(string(t), "video/")
	}
	return false
}

// medOriginOf returns the consent origin and host of a signed media URL.
func medOriginOf(raw string) (string, string, bool) {
	if content.ValidateExternalURL(raw) != nil {
		return "", "", false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", "", false
	}
	origin, err := content.Origin(u)
	if err != nil {
		return "", "", false
	}
	return origin, u.Hostname(), true
}

// medSlotViewOf works out what a reader is shown for one slot. Nothing is fetched here: a
// consented origin is loaded by the figure's own lazy request, so rendering a feed fetches nothing.
func medSlotViewOf(state *medState, profile, postID string, index, count int, slot wire.MediaReference, settings ProfileSettings, now time.Time) medSlotView {
	v := medSlotView{
		FigureID: medFigureID(postID, slot.ID), PostID: postID, SlotID: slot.ID,
		Kind: slot.Kind, Label: medKindLabel(slot.Kind), Alt: medAlt(slot), Declared: medDeclaredSize(slot.SizeBytes),
		Index: index, Count: count,
	}
	origin, host, ok := medOriginOf(slot.URL)
	if !ok || !medSlotIDPattern.MatchString(slot.ID) {
		v.State, v.Text, v.Pill = medInvalid, "This media address is not valid, so it is not loaded.", "pill-bad"
		return v
	}
	v.Host, v.Origin = host, origin
	v.Button = "Load from " + host
	consented := slices.Contains(settings.MediaOrigins, origin)
	key := medClickKey(profile, postID, slot.ID)
	clicked := state.clicked(key, now)
	if media, ok := state.cache.get(medKey{sha: slot.SHA256, size: slot.SizeBytes}); ok && (consented || clicked) && medKindMatches(media.Type, slot.Kind) {
		v.Loaded, v.Stop = true, consented
		v.Src = "/media/" + postID + "/" + slot.ID
		v.Pill, v.Text = "pill-ok", "Checked against the signed SHA-256"
		v.State = medVideo
		if slot.Kind == "image" {
			v.State, v.Width, v.Height = medImage, media.Width, media.Height
		}
		return v
	}
	if failure, ok := state.failure(key, now); ok {
		v.State, v.Text, v.Pill = failure.state, failure.text, medFailurePill(failure.state)
		v.Button = "Try again"
		if failure.moved != "" {
			v.Approve = failure.moved
			v.Button = "Load from " + failure.moved
		}
		return v
	}
	if consented {
		v.State, v.Text, v.Pill, v.Lazy = medLoading, "Loading and checking…", "pill-muted", true
		return v
	}
	v.State, v.Text, v.Pill = medNotLoaded, "Not loaded. Nothing is contacted until you load it.", "pill-muted"
	return v
}

func medFailurePill(state string) string {
	switch state {
	case medUnsupported, medRedirect:
		return "pill-warn"
	case medLoading, medNotLoaded:
		return "pill-muted"
	}
	return "pill-bad"
}

// medGridOf builds the slots of one post for one reader.
func medGridOf(state *medState, profile, postID string, slots []wire.MediaReference, settings ProfileSettings, now time.Time) medGrid {
	grid := medGrid{PostID: postID, Count: len(slots), Slots: make([]medSlotView, 0, len(slots))}
	verified, firstVerified := 0, ""
	for i, slot := range slots {
		v := medSlotViewOf(state, profile, postID, i+1, len(slots), slot, settings, now)
		if v.State == medImage {
			verified++
			if firstVerified == "" {
				firstVerified = slot.ID
			}
		}
		grid.Slots = append(grid.Slots, v)
	}
	if verified >= 2 {
		grid.ViewerURL = "/viewer/" + postID + "?slot=" + url.QueryEscape(firstVerified)
	}
	return grid
}

// renderPostMedia returns the media slots of a network post as this reader sees them.
func renderPostMedia(a *App, profile string, settings ProfileSettings, p post, now time.Time) template.HTML {
	if len(p.Slots) == 0 || !canonicalMessageID(p.ID) {
		return ""
	}
	var out bytes.Buffer
	if err := a.templates.ExecuteTemplate(&out, "media-slots", medGridOf(medStateFor(a), profile, p.ID, p.Slots, settings, now)); err != nil {
		a.logger().Printf("media slots: %v", err)
		return ""
	}
	return template.HTML(out.String())
}

// medFailureOf maps a failed load to the state a reader is shown, in words.
func medFailureOf(err error, moved string, slot wire.MediaReference, now time.Time) medFailure {
	failure := func(state, text string) medFailure {
		return medFailure{state: state, text: text, at: now}
	}
	var status *medStatusError
	switch {
	case moved != "" && errors.Is(err, content.ErrRedirectRefused):
		return medFailure{state: medRedirect, text: "This media moved to " + moved + ". Load from there?", moved: moved, at: now}
	case errors.Is(err, content.ErrBlockedDestination):
		return failure(medBlocked, "This address points to a private or local network and is never contacted.")
	case errors.Is(err, content.ErrDigestMismatch):
		return failure(medMismatch, "This file differs from the one the author signed. Not shown.")
	case errors.Is(err, content.ErrMediaTooLarge) && slot.SizeBytes > content.DefaultMediaBudget().MaxBytes:
		return failure(medTooLarge, "Larger than this client loads (32 MB). Not shown.")
	case errors.Is(err, content.ErrMediaSizeMismatch), errors.Is(err, content.ErrMediaTooLarge) && slot.SizeBytes > 0:
		return failure(medTooLarge, "Larger than the author declared. Not shown.")
	case errors.Is(err, content.ErrMediaTooLarge):
		return failure(medTooLarge, "Larger than this client loads (32 MB). Not shown.")
	case errors.Is(err, content.ErrMediaBudget):
		return failure(medTooLarge, "Larger than this client decodes. Not shown.")
	case errors.Is(err, content.ErrEncodedResponse):
		return failure(medUnsupported, "The server sent this file compressed, so it is not shown.")
	case errors.Is(err, content.ErrUnsupportedMedia), errors.Is(err, content.ErrKindMismatch):
		return failure(medUnsupported, "This file type is not shown by ForYou (for example SVG or HTML).")
	case errors.As(err, &status):
		return failure(medUnavailable, fmt.Sprintf("The server answered HTTP %d. Try again.", status.code))
	case errors.Is(err, context.DeadlineExceeded):
		return failure(medUnavailable, "The server did not answer in time. Try again.")
	}
	return failure(medUnavailable, "The server could not be reached. Try again.")
}

func medIsHTMLeX(r *http.Request) bool {
	return r.Header.Get("Sec-Fetch-Dest") == "empty"
}

// medValidOrigin reports whether s is exactly the canonical consent origin of an HTTPS URL.
func medValidOrigin(s string) bool {
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	origin, err := content.Origin(u)
	return err == nil && origin == s
}

// medLookup returns the post and the index of slotID when the reader can see the record and the slot is declared in it.
func (a *App) medLookup(r *http.Request, postID, slotID string) (post, int, bool, error) {
	if !canonicalMessageID(postID) || !medSlotIDPattern.MatchString(slotID) {
		return post{}, 0, false, nil
	}
	p, ok, err := medRecord(a, r, postID)
	if err != nil || !ok {
		return post{}, 0, false, err
	}
	for i, slot := range p.Slots {
		if slot.ID == slotID {
			return p, i, true, nil
		}
	}
	return post{}, 0, false, nil
}

// medLoadHandler loads one item for the reader. The item's own figure answers HTMLeX requests;
// a plain form post gets the post page back with the item loaded.
func (a *App) medLoadHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := readActionForm(w, r, "post", "slot")
	if !ok {
		return
	}
	profileID, err := a.profile(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The media could not be loaded.")
		return
	}
	postID, slotID, approve := form.Get("post"), form.Get("slot"), form.Get("approve")
	if approve != "" && !medValidOrigin(approve) {
		http.Error(w, "invalid origin", http.StatusBadRequest)
		return
	}
	p, index, found, err := a.medLookup(r, postID, slotID)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The media could not be loaded.")
		return
	}
	if !found {
		a.fail(w, r, http.StatusNotFound, "This media is not available.")
		return
	}
	slot := p.Slots[index]
	origin, _, _ := medOriginOf(slot.URL)
	settings, err := a.settings(profileID)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The media could not be loaded.")
		return
	}
	now := a.now()
	if form.Get("always") == "1" {
		target := origin
		if approve != "" {
			target = approve
		}
		if err := a.updateSettings(profileID, func(s *ProfileSettings) error {
			if !slices.Contains(s.MediaOrigins, target) {
				s.MediaOrigins = append(s.MediaOrigins, target)
			}
			return nil
		}); err != nil {
			a.fail(w, r, http.StatusInternalServerError, "The media could not be loaded.")
			return
		}
		if settings, err = a.settings(profileID); err != nil {
			a.fail(w, r, http.StatusInternalServerError, "The media could not be loaded.")
			return
		}
	}
	state := medStateFor(a)
	key := medClickKey(profileID, postID, slotID)
	auto := form.Get("auto") == "1"
	if !auto || slices.Contains(settings.MediaOrigins, origin) {
		if !auto {
			state.markClick(key, origin, now)
		}
		state.clearFailure(key)
		consent := slices.Clone(settings.MediaOrigins)
		_, moved, err := state.fetch.load(r.Context(), slot, func(target string) bool {
			return target == approve || slices.Contains(consent, target)
		})
		if err != nil && !errors.Is(err, context.Canceled) {
			state.setFailure(key, medFailureOf(err, moved, slot, now))
		}
	}
	view := medSlotViewOf(state, profileID, postID, index+1, len(p.Slots), slot, settings, now)
	w.Header().Set("Cache-Control", "no-store")
	if medIsHTMLeX(r) {
		a.fragment(w, r, "media-slot", view)
		return
	}
	http.Redirect(w, r, "/posts/"+url.PathEscape(postID)+"#"+view.FigureID, http.StatusSeeOther)
}

// medStopHandler removes consent for one origin, ends the click windows and cancels the loads that reach it.
// The item's figure answers HTMLeX requests; a plain form post gets the post page back.
func (a *App) medStopHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := readActionForm(w, r, "post", "slot", "origin")
	if !ok {
		return
	}
	profileID, err := a.profile(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The media could not be stopped.")
		return
	}
	postID, slotID, origin := form.Get("post"), form.Get("slot"), form.Get("origin")
	if !medValidOrigin(origin) {
		http.Error(w, "invalid origin", http.StatusBadRequest)
		return
	}
	p, index, found, err := a.medLookup(r, postID, slotID)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The media could not be stopped.")
		return
	}
	if !found {
		a.fail(w, r, http.StatusNotFound, "This media is not available.")
		return
	}
	if err := a.updateSettings(profileID, func(s *ProfileSettings) error {
		s.MediaOrigins = slices.DeleteFunc(s.MediaOrigins, func(allowed string) bool { return allowed == origin })
		return nil
	}); err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The media could not be stopped.")
		return
	}
	state := medStateFor(a)
	state.revokeOrigin(profileID, origin)
	settings, err := a.settings(profileID)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The media could not be stopped.")
		return
	}
	view := medSlotViewOf(state, profileID, postID, index+1, len(p.Slots), p.Slots[index], settings, a.now())
	w.Header().Set("Cache-Control", "no-store")
	if medIsHTMLeX(r) {
		a.fragment(w, r, "media-slot", view)
		return
	}
	http.Redirect(w, r, "/posts/"+url.PathEscape(postID)+"#"+view.FigureID, http.StatusSeeOther)
}

// medObjectHandler serves a verified object to a reader who consented to its origin or loaded it by click.
// Anything else, including an object another reader loaded, is not found.
func (a *App) medObjectHandler(w http.ResponseWriter, r *http.Request) {
	profileID := requestProfile(r)
	p, index, found, err := a.medLookup(r, r.PathValue("id"), r.PathValue("slot"))
	if err != nil || !found || profileID == "" {
		http.NotFound(w, r)
		return
	}
	slot := p.Slots[index]
	origin, _, valid := medOriginOf(slot.URL)
	if !valid {
		http.NotFound(w, r)
		return
	}
	settings, err := a.settings(profileID)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	state := medStateFor(a)
	if !slices.Contains(settings.MediaOrigins, origin) && !state.clicked(medClickKey(profileID, p.ID, slot.ID), a.now()) {
		http.NotFound(w, r)
		return
	}
	media, ok := state.cache.get(medKey{sha: slot.SHA256, size: slot.SizeBytes})
	if !ok || !medKindMatches(media.Type, slot.Kind) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", string(media.Type))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
	w.Header().Set("Cache-Control", "private, max-age=600")
	w.Header().Set("Content-Disposition", "inline")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(media.Bytes))
}

// medViewerHandler shows one post's media one slot at a time, with thumbnails and previous and next.
func (a *App) medViewerHandler(w http.ResponseWriter, r *http.Request) {
	postID := r.PathValue("id")
	p, ok, err := medRecord(a, r, postID)
	if err != nil || !ok || !canonicalMessageID(postID) || len(p.Slots) == 0 {
		http.NotFound(w, r)
		return
	}
	profileID := requestProfile(r)
	var settings ProfileSettings
	if profileID != "" {
		if settings, err = a.settings(profileID); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	grid := medGridOf(medStateFor(a), profileID, postID, p.Slots, settings, a.now())
	selected := 0
	if wanted := r.URL.Query().Get("slot"); wanted != "" {
		for i, slot := range grid.Slots {
			if slot.SlotID == wanted {
				selected = i
			}
		}
	} else {
		for i, slot := range grid.Slots {
			if slot.State == medImage {
				selected = i
				break
			}
		}
	}
	page := medViewerPage{Layout: a.layout(r, "media", "Media viewer"), Post: p, Grid: grid, Current: grid.Slots[selected]}
	if selected > 0 {
		page.Prev = grid.Slots[selected-1].SlotID
	}
	if selected+1 < len(grid.Slots) {
		page.Next = grid.Slots[selected+1].SlotID
	}
	w.Header().Set("Cache-Control", "private, no-store")
	if medIsHTMLeX(r) {
		a.fragment(w, r, "media-viewer-stage", page)
		return
	}
	a.page(w, r, "media-viewer-page", page)
}
