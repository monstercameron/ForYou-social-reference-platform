package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"unicode/utf8"
)

// demoStore is local to one client handler. It is deliberately bounded and
// volatile: this demo does not publish to a provider or persist across restarts.
type demoStore struct {
	mu      sync.RWMutex
	posts   []post // Immutable snapshots, newest first.
	byToken map[string]post
	byID    map[string]post
}
type demoStoreKey struct{}

// list returns an immutable snapshot. Callers must not modify it.
func (s *demoStore) list() []post {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.posts
}

const demoPostLimit = 1000

var (
	errDraftConflict = errors.New("draft already submitted with different text")
	errDemoStoreFull = errors.New("demo storage full")
)

func (s *demoStore) add(text, token string) (post, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.byToken[token]; ok {
		if existing.Text != text {
			return post{}, errDraftConflict
		}
		return existing, nil
	}
	if len(s.posts) >= demoPostLimit {
		return post{}, errDemoStoreFull
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return post{}, err
	}
	p := post{ID: "demo-" + hex.EncodeToString(id[:]), Kind: "text", Color: "violet", Time: "Just now", Text: text}
	p.Receipt = mockReceipt(p.ID)
	p.ReplyCount = len(demoReplies(p.ID))
	posts := make([]post, len(s.posts)+1)
	posts[0] = p
	copy(posts[1:], s.posts)
	s.posts = posts
	if s.byToken == nil {
		s.byToken = make(map[string]post)
	}
	s.byToken[token] = p
	if s.byID == nil {
		s.byID = make(map[string]post)
	}
	s.byID[p.ID] = p
	return p, nil
}
func storeFor(r *http.Request) *demoStore {
	s, _ := r.Context().Value(demoStoreKey{}).(*demoStore)
	return s
}
func (s *demoStore) get(id string) (post, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.byID[id]
	return p, ok
}

func requestPost(r *http.Request, id string) (post, bool) {
	if s := storeFor(r); s != nil {
		if p, ok := s.get(id); ok {
			return p, true
		}
	}
	return postByID(id)
}

func publishDemo(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return
		}
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		http.Error(w, "invalid origin", http.StatusForbidden)
		return
	}
	// URL-encoded UTF-8 can expand to three bytes per input byte.
	r.Body = http.MaxBytesReader(w, r.Body, 50000)
	parse := r.ParseForm
	if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
		parse = func() error { return r.ParseMultipartForm(20000) }
	}
	if err := parse(); err != nil {
		http.Error(w, "invalid draft", http.StatusBadRequest)
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
		if len(r.MultipartForm.File) != 0 {
			http.Error(w, "attachments are not supported", http.StatusBadRequest)
			return
		}
	}
	text := r.PostForm.Get("text")
	if strings.TrimSpace(text) == "" || !utf8.ValidString(text) || len(text) > 16000 || utf8.RuneCountInString(text) > 4000 {
		http.Error(w, "Write a post of up to 4,000 characters.", http.StatusBadRequest)
		return
	}
	token := r.PostForm.Get("draft_token")
	decoded, tokenErr := hex.DecodeString(token)
	if tokenErr != nil || len(decoded) != 16 || hex.EncodeToString(decoded) != token {
		http.Error(w, "Invalid draft. Reload the composer and try again.", http.StatusBadRequest)
		return
	}
	p, err := storeFor(r).add(text, token)
	if errors.Is(err, errDraftConflict) {
		http.Error(w, "This draft was already submitted with different text.", http.StatusConflict)
		return
	}
	if err != nil {
		http.Error(w, "Demo storage is full or unavailable.", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/compose/fragment" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_ = home.ExecuteTemplate(w, "compose-success", p)
		return
	}
	http.Redirect(w, r, "/posts/"+p.ID, http.StatusSeeOther)
}
