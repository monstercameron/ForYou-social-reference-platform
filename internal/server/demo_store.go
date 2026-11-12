package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"
)

func init() { routeGroups = append(routeGroups, registerDemoStore) }

func registerDemoStore(a *App, mux *http.ServeMux) {
	mux.HandleFunc("POST /compose", a.publishDemo)
	mux.HandleFunc("POST /compose/fragment", a.publishDemo)
}

// demoStore is local to one client handler. It is deliberately bounded and
// volatile: this demo does not publish to a provider or persist across restarts.
type demoStore struct {
	mu      sync.RWMutex
	posts   []post // Immutable snapshots, newest first.
	byToken map[string]post
	byID    map[string]post
	replies map[string][]addedReply
	replied int
	quotes  map[string]quotedReply
}

// addedReply is a reply written in this session; parent is the path of the reply it answers, or empty for the post.
type addedReply struct{ parent, text string }

// list returns an immutable snapshot. Callers must not modify it.
func (s *demoStore) list() []post {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.posts
}

const demoPostLimit = 1000
const demoReplyLimit = 5000

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
	p.ReplyCount = countReplies(demoReplies(p.ID))
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
func (s *demoStore) get(id string) (post, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.byID[id]
	return p, ok
}

func (s *demoStore) addReply(postID, parent, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.replied >= demoReplyLimit {
		return errDemoStoreFull
	}
	if s.replies == nil {
		s.replies = make(map[string][]addedReply)
	}
	s.replies[postID] = append(s.replies[postID], addedReply{parent: parent, text: text})
	s.replied++
	return nil
}

// repliesTo returns a snapshot in the order the replies were written.
func (s *demoStore) repliesTo(postID string) []addedReply {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return slices.Clip(s.replies[postID])
}

func (s *demoStore) replyCount(postID string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.replies[postID])
}

// forReader adds what only this request knows: the reader's own choices and
// the replies written in this session.
func (a *App) forReader(r *http.Request, posts []post) {
	feedback, followed, store, shown := feedbackSelections(r), followSet(r), a.store, readerFilters(r)
	for i := range posts {
		posts[i].Rating = rate(posts[i].format(), posts[i].Text, shown)
		posts[i].Feedback = feedback[posts[i].ID]
		posts[i].Following = followed[posts[i].AuthorID]
		if store != nil {
			posts[i].ReplyCount += store.replyCount(posts[i].ID)
		}
	}
}

func (a *App) publishDemo(w http.ResponseWriter, r *http.Request) {
	if !requireSameOrigin(w, r) {
		return
	}
	// URL-encoded UTF-8 can expand to three bytes per input byte.
	if !parseSmallForm(w, r, 50000) {
		return
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
	p, err := a.records().PublishPost(r.Context(), text, token)
	if errors.Is(err, errDraftConflict) {
		http.Error(w, "This draft was already submitted with different text.", http.StatusConflict)
		return
	}
	if errors.Is(err, errNetworkPublish) {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	if err != nil {
		http.Error(w, "Demo storage is full or unavailable.", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/compose/fragment" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		a.fragment(w, r, "compose-success", p)
		return
	}
	http.Redirect(w, r, "/posts/"+p.ID, http.StatusSeeOther)
}
