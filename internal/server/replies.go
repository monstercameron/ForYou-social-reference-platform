package server

import (
	"net/http"
)

type reply struct {
	Name, Initials, Color, Time, Text string
	Receipt                           string
	Children                          []reply
}

type thread struct {
	Parent                 post
	Theme                  string
	Section, Filter, Query string
	Replies                []reply
	Extra                  []reply
	HasMore                bool
	Expanded               bool
	Static                 bool
}

func readThread(r *http.Request) (thread, int) {
	id := r.URL.Query().Get("id")
	parent, ok := requestPost(r, id)
	if !ok {
		return thread{}, http.StatusNotFound
	}
	replies := demoReplies(id)
	page := r.URL.Query().Get("page")
	if page != "" && page != "1" && page != "2" {
		return thread{}, http.StatusBadRequest
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
	return thread{Theme: requestTheme(r), Filter: "all", Parent: parent, Replies: limitReplyDepth(replies, 3), Extra: limitReplyDepth(extra, 3), HasMore: more, Expanded: expanded}, http.StatusOK
}

func limitReplyDepth(replies []reply, depth int) []reply {
	if depth <= 0 {
		return nil
	}
	result := make([]reply, len(replies))
	for i, r := range replies {
		result[i] = r
		result[i].Receipt = mockReceipt(r.Text)
		result[i].Children = limitReplyDepth(r.Children, depth-1)
	}
	return result
}

func threadPageHandler(w http.ResponseWriter, r *http.Request) {
	t, status := readThread(r)
	if status != http.StatusOK {
		if status == http.StatusNotFound {
			writeDetailNotFound(w, requestTheme(r))
			return
		}
		http.Error(w, http.StatusText(status), status)
		return
	}
	t.Parent.Feedback = feedbackSelections(r)[t.Parent.ID]
	t.Static = true
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = home.ExecuteTemplate(w, "thread-page", t)
}

func threadFragmentHandler(w http.ResponseWriter, r *http.Request) {
	t, status := readThread(r)
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
		_ = home.ExecuteTemplate(w, "thread-closed", t.Parent)
		return
	}
	if view == "closing" {
		_ = home.ExecuteTemplate(w, "thread-closing", t)
		return
	}
	_ = home.ExecuteTemplate(w, "thread-open", t)
}
