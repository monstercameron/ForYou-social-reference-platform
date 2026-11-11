package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestReplyThreads(t *testing.T) {
	check := func(t *testing.T, path, present, absent string, status int) {
		t.Helper()
		w := httptest.NewRecorder()
		newHandler("client", nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		body := w.Body.String()
		if w.Code != status || !strings.Contains(body, present) || (absent != "" && strings.Contains(body, absent)) {
			t.Errorf("%s: status %d, expected %d; missing %q or unexpectedly present %q", path, w.Code, status, present, absent)
		}
	}
	t.Run("initial replies", func(t *testing.T) {
		check(t, "/thread/fragment?id=maya", "more tending", "More community gardens, online and off.", http.StatusOK)
	})
	t.Run("nested replies", func(t *testing.T) {
		check(t, "/thread/fragment?id=maya", "come back whenever you feel like it", "More community gardens, online and off.", http.StatusOK)
	})
	t.Run("show more", func(t *testing.T) {
		check(t, "/thread/fragment?id=maya&page=2", "More community gardens, online and off.", "Show more replies", http.StatusOK)
	})
	t.Run("collapse", func(t *testing.T) {
		check(t, "/thread/fragment?id=maya&view=closed", `<span class="reply-count">4</span>`, "more tending", http.StatusOK)
	})
	t.Run("closing expanded thread", func(t *testing.T) {
		check(t, "/thread/fragment?id=maya&page=2&view=closing", "More community gardens, online and off.", "Show more replies", http.StatusOK)
	})
	t.Run("closing failure has a recovery target", func(t *testing.T) {
		check(t, "/thread/fragment?id=maya&view=closing", `onerror="this(prepend)"`, "", http.StatusOK)
	})
	t.Run("unknown post", func(t *testing.T) {
		check(t, "/thread/fragment?id=unknown", "Not Found", "more tending", http.StatusNotFound)
	})
	t.Run("fallback page", func(t *testing.T) {
		check(t, "/thread?id=maya&page=2", "More community gardens, online and off.", "Show more replies", http.StatusOK)
	})
}

func TestRepliesAcrossDemoFeed(t *testing.T) {
	h := newHandler("client", nil)
	feed := httptest.NewRecorder()
	h.ServeHTTP(feed, httptest.NewRequest(http.MethodGet, "/?limit=10000", nil))
	body := feed.Body.String()
	if feed.Code != http.StatusOK || strings.Contains(body, "No replies") || strings.Count(body, `data-thread-control="open"`) != len(staticDemoPosts) {
		t.Errorf("feed has missing reply controls: status %d, controls %d, posts %d", feed.Code, strings.Count(body, `data-thread-control="open"`), len(staticDemoPosts))
	}
	check := func(t *testing.T, id, want string) {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/thread/fragment?id="+id+"&page=2", nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), want) {
			t.Errorf("thread %s: status %d; missing %q", id, w.Code, want)
		}
	}
	t.Run("adult", func(t *testing.T) { check(t, "adult-1", "seductive") })
	t.Run("scam and spam", func(t *testing.T) { check(t, "scam", "FREE DEAL") })
	t.Run("long unbroken spam", func(t *testing.T) { check(t, "spam-2", "FREEFREEFREEFREEFREEFREEFREEFREEFREEFREE") })
	t.Run("insults", func(t *testing.T) { check(t, "harassment", "asshole") })
	t.Run("three levels", func(t *testing.T) { check(t, "tech-video", "Following along for the details") })
	t.Run("topic-specific replies", func(t *testing.T) { check(t, "garden", "Tomatoes and basil") })
}

func TestNewDemoPostHasReplies(t *testing.T) {
	h := newHandler("client", nil)
	form := url.Values{"text": {"A new demo post"}, "draft_token": {"abcdabcdabcdabcdabcdabcdabcdabcd"}}
	r := httptest.NewRequest(http.MethodPost, "/compose", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("create returned %d", w.Code)
	}
	id := strings.TrimPrefix(w.Header().Get("Location"), "/posts/")
	thread := httptest.NewRecorder()
	h.ServeHTTP(thread, httptest.NewRequest(http.MethodGet, "/thread/fragment?id="+url.QueryEscape(id), nil))
	if thread.Code != http.StatusOK || !strings.Contains(thread.Body.String(), "Hide replies") || !strings.Contains(thread.Body.String(), "Show more replies") {
		t.Errorf("new post thread returned %d without reply controls", thread.Code)
	}
}

func TestReplyDepthLimit(t *testing.T) {
	input := []reply{{Text: "first", Children: []reply{{Text: "second", Children: []reply{{Text: "third", Children: []reply{{Text: "fourth"}}}}}}}}
	got := limitReplyDepth(input, 3)
	if len(got) != 1 || len(got[0].Children) != 1 || len(got[0].Children[0].Children) != 1 || len(got[0].Children[0].Children[0].Children) != 0 {
		t.Fatal("expected three reply levels and no fourth level")
	}
	got[0].Children[0].Text = "changed"
	if input[0].Receipt != "" || input[0].Children[0].Text != "second" {
		t.Fatal("rendering changed the shared reply fixture")
	}
}

func TestThreadFocusTargets(t *testing.T) {
	check := func(t *testing.T, path, present string) {
		t.Helper()
		w := httptest.NewRecorder()
		newHandler("client", nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), present) {
			t.Errorf("%s: missing %q, status %d", path, present, w.Code)
		}
	}
	t.Run("opening targets hide", func(t *testing.T) {
		check(t, "/thread/fragment?id=maya", `data-thread-control="hide" aria-expanded="true"`)
	})
	t.Run("closing targets open", func(t *testing.T) {
		check(t, "/thread/fragment?id=maya&view=closed", `data-thread-control="open" aria-expanded="false"`)
	})
	t.Run("show more targets hide", func(t *testing.T) { check(t, "/thread/fragment?id=maya", `data-thread-focus="hide"`) })
}
