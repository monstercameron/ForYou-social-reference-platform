package server

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const feedbackCookie = "foryou_feedback"
const feedbackPrefix = "foryou_vote_"
const feedbackLimit = 24

type feedbackEntry struct {
	ID   string `json:"i"`
	Vote string `json:"v"`
}

// legacyFeedbackEntries reads choices saved before the per-post cookie format.
func legacyFeedbackEntries(r *http.Request) []feedbackEntry {
	c, err := r.Cookie(feedbackCookie)
	if err != nil || len(c.Value) > 3600 {
		return nil
	}
	data, err := base64.RawURLEncoding.DecodeString(c.Value)
	if err != nil {
		return nil
	}
	var entries []feedbackEntry
	if json.Unmarshal(data, &entries) != nil || len(entries) > feedbackLimit {
		return nil
	}
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		if len(e.ID) == 0 || len(e.ID) > 80 || (e.Vote != "up" && e.Vote != "down") || seen[e.ID] {
			return nil
		}
		seen[e.ID] = true
	}
	return entries
}

type timedFeedback struct {
	feedbackEntry
	at int64
}

func feedbackCookieName(id string) string {
	return feedbackPrefix + base64.RawURLEncoding.EncodeToString([]byte(id))
}

// allFeedbackEntries combines legacy and per-post browser-local preferences.
func allFeedbackEntries(r *http.Request) []feedbackEntry {
	legacy := legacyFeedbackEntries(r)
	byID := make(map[string]timedFeedback, len(legacy))
	for i, e := range legacy {
		byID[e.ID] = timedFeedback{feedbackEntry: e, at: int64(i - len(legacy))}
	}
	for _, c := range r.Cookies() {
		if !strings.HasPrefix(c.Name, feedbackPrefix) || len(c.Name) > 128 || len(c.Value) > 32 {
			continue
		}
		encodedID := strings.TrimPrefix(c.Name, feedbackPrefix)
		idBytes, err := base64.RawURLEncoding.DecodeString(encodedID)
		if err != nil || len(idBytes) == 0 || len(idBytes) > 80 || base64.RawURLEncoding.EncodeToString(idBytes) != encodedID {
			continue
		}
		parts := strings.Split(c.Value, ".")
		if len(parts) != 2 || (parts[0] != "up" && parts[0] != "down" && parts[0] != "clear") {
			continue
		}
		at, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || at <= 0 {
			continue
		}
		id := string(idBytes)
		if previous, ok := byID[id]; ok && previous.at >= at {
			continue
		}
		byID[id] = timedFeedback{feedbackEntry: feedbackEntry{ID: id, Vote: parts[0]}, at: at}
	}
	ordered := make([]timedFeedback, 0, len(byID))
	for _, e := range byID {
		if e.Vote != "clear" {
			ordered = append(ordered, e)
		}
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].at == ordered[j].at {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].at < ordered[j].at
	})
	entries := make([]feedbackEntry, len(ordered))
	for i, e := range ordered {
		entries[i] = e.feedbackEntry
	}
	return entries
}

// feedbackEntries returns the most recent choices, not a trained model.
func feedbackEntries(r *http.Request) []feedbackEntry {
	entries := allFeedbackEntries(r)
	if len(entries) > feedbackLimit {
		entries = entries[len(entries)-feedbackLimit:]
	}
	return entries
}

func setFeedbackCookie(w http.ResponseWriter, r *http.Request, id, vote string) {
	value := vote + "." + strconv.FormatInt(time.Now().UnixNano(), 10)
	http.SetCookie(w, &http.Cookie{Name: feedbackCookieName(id), Value: value, Path: "/", MaxAge: 31536000, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
}

func expireFeedbackCookie(w http.ResponseWriter, r *http.Request, name string) {
	http.SetCookie(w, &http.Cookie{Name: name, Path: "/", MaxAge: -1, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
}

func hasLegacyFeedback(entries []feedbackEntry, id string) bool {
	for _, e := range entries {
		if e.ID == id {
			return true
		}
	}
	return false
}

func clearFeedbackChoice(w http.ResponseWriter, r *http.Request, legacy []feedbackEntry, id string) {
	if hasLegacyFeedback(legacy, id) {
		setFeedbackCookie(w, r, id, "clear")
	} else {
		expireFeedbackCookie(w, r, feedbackCookieName(id))
	}
}

func feedbackSelections(r *http.Request) map[string]string {
	selections := make(map[string]string)
	for _, e := range feedbackEntries(r) {
		selections[e.ID] = e.Vote
	}
	return selections
}

func sameOriginFeedback(r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
			return false
		}
	}
	return r.Header.Get("Sec-Fetch-Site") != "cross-site"
}

func feedbackHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := readActionForm(w, r, "id", "vote")
	if !ok {
		return
	}
	id, vote := form.Get("id"), form.Get("vote")
	if _, ok := requestPost(r, id); !ok {
		http.NotFound(w, r)
		return
	}
	if vote != "up" && vote != "down" && vote != "clear" {
		http.Error(w, "invalid feedback", http.StatusBadRequest)
		return
	}
	all := allFeedbackEntries(r)
	legacy := legacyFeedbackEntries(r)
	if len(all) > feedbackLimit {
		for _, e := range all[:len(all)-feedbackLimit] {
			clearFeedbackChoice(w, r, legacy, e.ID)
		}
		all = all[len(all)-feedbackLimit:]
	}
	entries := all
	updated := make([]feedbackEntry, 0, feedbackLimit)
	for _, e := range entries {
		if e.ID != id {
			updated = append(updated, e)
		}
	}
	if vote == "clear" {
		clearFeedbackChoice(w, r, legacy, id)
	} else {
		if len(updated) == feedbackLimit {
			clearFeedbackChoice(w, r, legacy, updated[0].ID)
		}
		setFeedbackCookie(w, r, id, vote)
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/feedback/fragment" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		selected := vote
		if selected == "clear" {
			selected = ""
		}
		_ = home.ExecuteTemplate(w, "feedback-controls", struct{ ID, Feedback string }{id, selected})
		return
	}
	http.Redirect(w, r, "/posts/"+url.PathEscape(id), http.StatusSeeOther)
}

func feedbackClearHandler(w http.ResponseWriter, r *http.Request) {
	if !sameOriginFeedback(r) {
		http.Error(w, "invalid origin", http.StatusForbidden)
		return
	}
	expireFeedbackCookie(w, r, feedbackCookie)
	for _, c := range r.Cookies() {
		if strings.HasPrefix(c.Name, feedbackPrefix) && len(c.Name) <= 128 {
			expireFeedbackCookie(w, r, c.Name)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/settings", http.StatusSeeOther)
}
