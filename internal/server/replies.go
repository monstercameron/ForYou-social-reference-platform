package server

import (
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// replyDepthLimit is the deepest reply a thread shows or accepts.
const replyDepthLimit = 8

const replyRuneLimit = 1000

func init() { routeGroups = append(routeGroups, registerThread) }

func registerThread(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /thread", a.threadPageHandler)
	mux.HandleFunc("GET /thread/fragment", a.threadFragmentHandler)
	mux.HandleFunc("GET /thread/branch", a.branchFragmentHandler)
	mux.HandleFunc("POST /reply", a.replyHandler)
	mux.HandleFunc("POST /reply/fragment", a.replyHandler)
}

type reply struct {
	Color, Time, Text string
	Receipt           string
	// HTML is Text rendered as Markdown; Images are the pictures shown under it.
	HTML     template.HTML
	Images   []galleryPage
	pictures []int
	// PostID and Path address a reply inside its thread; Path is the index at each level, joined by dots.
	PostID, Path string
	// Mine marks a reply written in this session; Open means the reply can still be answered.
	Mine, Open bool
	// Descendants counts every reply below this one. The answers stay folded
	// until the reader asks for them, unless Unfolded is set.
	Descendants int
	Unfolded    bool
	Children    []reply
}

// BranchID names the element that holds a reply's answers.
func (r reply) BranchID() string {
	return "branch-" + r.PostID + "-" + strings.ReplaceAll(r.Path, ".", "-")
}

// Draft starts an answer to this reply.
func (r reply) Draft() replyDraft {
	return replyDraft{PostID: r.PostID, Path: r.Path}
}

func (r reply) ParentPath() string {
	cut := strings.LastIndex(r.Path, ".")
	if cut < 0 {
		return ""
	}
	return r.Path[:cut]
}

type thread struct {
	Layout   Layout
	Parent   post
	Replies  []reply
	Extra    []reply
	HasMore  bool
	Expanded bool
	Static   bool
}

// Draft starts a reply to the post itself.
func (t thread) Draft() replyDraft {
	return replyDraft{PostID: t.Parent.ID}
}

func childPath(parent string, index int) string {
	if parent == "" {
		return strconv.Itoa(index)
	}
	return parent + "." + strconv.Itoa(index)
}

// prepareReplies copies a fixture thread down to depth levels and fills in what
// the templates need. The fixtures themselves are shared and never changed.
func prepareReplies(replies []reply, postID, parent string, depth int) []reply {
	if depth <= 0 || len(replies) == 0 {
		return nil
	}
	result := make([]reply, len(replies))
	for i, r := range replies {
		r.PostID, r.Path = postID, childPath(parent, i)
		r.Receipt = mockReceipt(r.Text)
		r.Open = depth > 1
		r.render()
		r.Children = prepareReplies(r.Children, postID, r.Path, depth-1)
		r.Descendants = countReplies(r.Children)
		result[i] = r
	}
	return result
}

// replyChain returns the replies from the top of a thread down to the one at
// path, or nil when no reply has that path.
func replyChain(replies []reply, path string) []*reply {
	var chain []*reply
	for _, part := range strings.Split(path, ".") {
		i, err := strconv.Atoi(part)
		if err != nil || i < 0 || i >= len(replies) || strconv.Itoa(i) != part {
			return nil
		}
		chain = append(chain, &replies[i])
		replies = replies[i].Children
	}
	return chain
}

// threadReplies returns a post's fixture replies with the replies added in this
// session attached where they were written.
func (a *App) threadReplies(r *http.Request, postID string) []reply {
	replies := prepareReplies(demoReplies(postID), postID, "", replyDepthLimit)
	store := a.store
	if store == nil {
		return replies
	}
	for _, added := range store.repliesTo(postID) {
		siblings := &replies
		depth := 1
		if added.parent != "" {
			chain := replyChain(replies, added.parent)
			if chain == nil {
				continue
			}
			for _, ancestor := range chain {
				ancestor.Descendants++
			}
			siblings = &chain[len(chain)-1].Children
			depth = len(chain) + 1
		}
		path := childPath(added.parent, len(*siblings))
		written := reply{Color: "violet", Time: "Just now", Text: added.text, Receipt: mockReceipt(postID + "/" + path + "/" + added.text), PostID: postID, Path: path, Mine: true, Open: depth < replyDepthLimit}
		written.render()
		*siblings = append(*siblings, written)
	}
	return replies
}

func (a *App) readThread(r *http.Request) (thread, int) {
	id := r.URL.Query().Get("id")
	parent, ok := a.requestPost(r, id)
	if !ok {
		return thread{}, http.StatusNotFound
	}
	replies := a.threadReplies(r, id)
	page := r.URL.Query().Get("page")
	if page != "" && page != "1" && page != "2" {
		return thread{}, http.StatusBadRequest
	}
	if open := r.URL.Query().Get("open"); open != "" {
		chain := replyChain(replies, open)
		if chain == nil {
			return thread{}, http.StatusBadRequest
		}
		for _, unfolded := range chain {
			unfolded.Unfolded = true
		}
	}
	expanded := page == "2" && len(replies) > 2
	more := !expanded && len(replies) > 2
	var extra []reply
	if expanded {
		extra = replies[2:]
		replies = replies[:2]
	} else if more {
		replies = replies[:2]
	}
	return thread{Parent: parent, Replies: replies, Extra: extra, HasMore: more, Expanded: expanded}, http.StatusOK
}

func (a *App) threadPageHandler(w http.ResponseWriter, r *http.Request) {
	t, status := a.readThread(r)
	if status != http.StatusOK {
		if status == http.StatusNotFound {
			a.writeDetailNotFound(w, r)
			return
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	t.Static = true
	t.Layout = a.layout(r, "thread", "Replies")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	a.page(w, r, "thread-page", t)
}

func (a *App) threadFragmentHandler(w http.ResponseWriter, r *http.Request) {
	t, status := a.readThread(r)
	if status != http.StatusOK {
		http.Error(w, http.StatusText(status), status)
		return
	}
	view := r.URL.Query().Get("view")
	if view != "" && view != "closed" && view != "closing" {
		http.Error(w, "invalid view", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if view == "closed" {
		a.fragment(w, r, "thread-closed", t.Parent)
		return
	}
	if view == "closing" {
		a.fragment(w, r, "thread-closing", t)
		return
	}
	a.fragment(w, r, "thread-open", t)
}

// branchFragmentHandler answers a reader who opens or folds the answers to one reply.
func (a *App) branchFragmentHandler(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	id, view := query.Get("id"), query.Get("view")
	if view != "" && view != "closed" {
		http.Error(w, "invalid view", http.StatusBadRequest)
		return
	}
	if _, found := a.requestPost(r, id); !found {
		http.NotFound(w, r)
		return
	}
	chain := replyChain(a.threadReplies(r, id), query.Get("branch"))
	if chain == nil || len(chain[len(chain)-1].Children) == 0 {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	name := "thread-branch-open"
	if view == "closed" {
		name = "thread-branch-closed"
	}
	a.fragment(w, r, name, *chain[len(chain)-1])
}

// replyHandler posts a reply the reader has confirmed. It takes the quote the
// reader was shown, never the text itself, so nothing is posted unpriced. The
// fragment answer is the whole thread, opened far enough to show the new reply.
func (a *App) replyHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := readActionForm(w, r, "id", "quote")
	if !ok {
		return
	}
	store := a.store
	if store == nil {
		http.Error(w, "Demo storage is full or unavailable.", http.StatusServiceUnavailable)
		return
	}
	d, quoted := store.takeQuote(form.Get("quote"), a.config.Now())
	if !quoted || d.PostID != form.Get("id") {
		http.Error(w, "This quote has expired or was already used. Review the cost again.", http.StatusConflict)
		return
	}
	if store.addReply(d.PostID, d.Path, d.Text) != nil {
		http.Error(w, "Demo storage is full or unavailable.", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	shown := url.Values{"id": {d.PostID}, "page": {"2"}}
	if d.Path != "" {
		shown.Set("open", d.Path)
	}
	whole := shown.Encode()
	if r.URL.Path != "/reply/fragment" {
		http.Redirect(w, r, "/thread?"+whole, http.StatusSeeOther)
		return
	}
	threadRequest := r.Clone(r.Context())
	threadRequest.URL = &url.URL{Path: "/thread/fragment", RawQuery: whole}
	t, status := a.readThread(threadRequest)
	if status != http.StatusOK {
		http.Error(w, http.StatusText(status), status)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	a.fragment(w, r, "thread-open", t)
}
