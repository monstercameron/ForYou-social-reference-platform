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
		check(t, "/thread/fragment?id=maya&open=0.0", "come back whenever you feel like it", "More community gardens, online and off.", http.StatusOK)
	})
	t.Run("answers stay folded until asked for", func(t *testing.T) {
		check(t, "/thread/fragment?id=maya", `get="/thread/branch"`, "It should feel welcoming to linger", http.StatusOK)
	})
	t.Run("one branch on demand", func(t *testing.T) {
		check(t, "/thread/branch?id=maya&branch=0", "It should feel welcoming to linger", "come back whenever you feel like it", http.StatusOK)
	})
	t.Run("fold a branch", func(t *testing.T) {
		check(t, "/thread/branch?id=maya&branch=0&view=closed", `aria-expanded="false"`, "It should feel welcoming to linger", http.StatusOK)
	})
	t.Run("branch without answers", func(t *testing.T) {
		check(t, "/thread/branch?id=maya&branch=1", "404", "replies", http.StatusNotFound)
	})
	t.Run("unknown branch", func(t *testing.T) {
		check(t, "/thread/fragment?id=maya&open=7", "Bad Request", "replies", http.StatusBadRequest)
	})
	t.Run("show more", func(t *testing.T) {
		check(t, "/thread/fragment?id=maya&page=2", "More community gardens, online and off.", "Show more replies", http.StatusOK)
	})
	t.Run("collapse", func(t *testing.T) {
		check(t, "/thread/fragment?id=maya&view=closed", `<span class="reply-count">6</span>`, "more tending", http.StatusOK)
	})
	t.Run("closing expanded thread", func(t *testing.T) {
		check(t, "/thread/fragment?id=maya&page=2&view=closing", "More community gardens, online and off.", "Show more replies", http.StatusOK)
	})
	t.Run("closing shows the closed control at once and needs no second request", func(t *testing.T) {
		check(t, "/thread/fragment?id=maya&view=closing", `data-thread-control="open" aria-expanded="false"`, `auto="`, http.StatusOK)
	})
	t.Run("closing panel removes itself", func(t *testing.T) {
		check(t, "/thread/fragment?id=maya&view=closing", `class="thread-panel thread-collapsing" timer="280"`, `data-thread-control="hide"`, http.StatusOK)
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
	check := func(t *testing.T, id, open, want string) {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/thread/fragment?id="+id+"&page=2&open="+open, nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), want) {
			t.Errorf("thread %s: status %d; missing %q", id, w.Code, want)
		}
	}
	t.Run("adult", func(t *testing.T) { check(t, "adult-1", "", "seductive") })
	t.Run("scam and spam", func(t *testing.T) { check(t, "scam", "", "FREE DEAL") })
	t.Run("long unbroken spam", func(t *testing.T) { check(t, "spam-2", "", "FREEFREEFREEFREEFREEFREEFREEFREEFREEFREE") })
	t.Run("insults", func(t *testing.T) { check(t, "harassment", "", "assholes") })
	t.Run("four levels", func(t *testing.T) { check(t, "tech-video", "0.0.0", "At the wall, with a plug-in meter") })
	t.Run("eight levels", func(t *testing.T) { check(t, "gaming", "0.0.0.0.0.0.0", "best thread on my feed today") })
	t.Run("threats", func(t *testing.T) { check(t, "threat", "", "waiting outside with a bat") })
	t.Run("hateful remarks", func(t *testing.T) { check(t, "news", "", "Send them back where they came from") })
	t.Run("code", func(t *testing.T) { check(t, "open-source", "0.0.0.0", "filepath.Join(filepath.Dir(file), href)") })
	t.Run("credential-shaped text", func(t *testing.T) { check(t, "tech-video", "", "AWS_SECRET_ACCESS_KEY=") })
	t.Run("topic-specific replies", func(t *testing.T) { check(t, "garden", "", "Tomatoes and basil") })
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
	if thread.Code != http.StatusOK || !strings.Contains(thread.Body.String(), `data-thread-control="hide"`) || !strings.Contains(thread.Body.String(), "Show more replies") {
		t.Errorf("new post thread returned %d without reply controls", thread.Code)
	}
}

func TestPrepareRepliesLimitsDepthAndCopies(t *testing.T) {
	input := []reply{{Text: "first", Children: []reply{{Text: "second", Children: []reply{{Text: "third", Children: []reply{{Text: "fourth"}}}}}}}}
	got := prepareReplies(input, "p", "", 3)
	third := got[0].Children[0].Children[0]
	if len(third.Children) != 0 || third.Open || third.Path != "0.0.0" || !got[0].Open {
		t.Fatalf("third level = %+v; want the last answerable level cut off at depth three", third)
	}
	got[0].Children[0].Text = "changed"
	if input[0].Receipt != "" || input[0].Children[0].Text != "second" {
		t.Fatal("rendering changed the shared reply fixture")
	}
}

func TestDemoThreadFixtures(t *testing.T) {
	var depth func([]reply) int
	depth = func(replies []reply) int {
		deepest := 0
		for _, r := range replies {
			deepest = max(deepest, depth(r.Children))
		}
		if len(replies) == 0 {
			return 0
		}
		return deepest + 1
	}
	silent, deep := 0, 0
	for _, p := range staticDemoPosts {
		replies := demoReplies(p.ID)
		if len(replies) == 0 {
			silent++
		}
		if d := depth(replies); d > replyDepthLimit {
			t.Errorf("post %q thread is %d levels deep; the client shows %d, so its reply count would be wrong", p.ID, d, replyDepthLimit)
		} else if d >= 5 {
			deep++
		}
		if p.ReplyCount != countReplies(replies) {
			t.Errorf("post %q counts %d replies; its thread holds %d", p.ID, p.ReplyCount, countReplies(replies))
		}
	}
	if silent != 2 || deep < 5 {
		t.Errorf("fixtures have %d posts without replies and %d deep threads; want exactly 2 and at least 5", silent, deep)
	}
}

func TestReplyCountIncludesNestedReplies(t *testing.T) {
	w := httptest.NewRecorder()
	newHandler("client", nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/thread/fragment?id=maya", nil))
	if !strings.Contains(w.Body.String(), `<span class="reply-count">6</span>`) {
		t.Error("maya has four replies and two nested ones; the thread control should count 6")
	}
}

func postForm(h http.Handler, path string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestAddReply(t *testing.T) {
	h := newHandler("client", nil)
	t.Run("to the post", func(t *testing.T) {
		w := postForm(h, "/reply/fragment", url.Values{"id": {"poetry"}, "parent": {""}, "text": {"A first reply <b>"}})
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "A first reply &lt;b&gt;") || !strings.Contains(body, `<span class="reply-count">1</span>`) || !strings.Contains(body, `>you</span>`) {
			t.Errorf("status %d; the thread should show the escaped reply, marked as the reader's own, and count it", w.Code)
		}
	})
	t.Run("to a nested reply", func(t *testing.T) {
		w := postForm(h, "/reply/fragment", url.Values{"id": {"maya"}, "parent": {"0.0"}, "text": {"Answering the second level"}})
		body := w.Body.String()
		at := strings.Index(body, "Answering the second level")
		if w.Code != http.StatusOK || at < 0 || at > strings.Index(body, "And room for things to grow") || !strings.Contains(body, `<span class="reply-count">7</span>`) {
			t.Errorf("status %d; the answer should sit inside the first reply's branch and raise the count to 7", w.Code)
		}
	})
	t.Run("without scripts", func(t *testing.T) {
		w := postForm(h, "/reply", url.Values{"id": {"noor"}, "parent": {""}, "text": {"Plain form"}})
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/thread?id=noor&page=2" {
			t.Errorf("status %d, location %q; want a redirect to the whole thread", w.Code, w.Header().Get("Location"))
		}
	})
	refused := func(t *testing.T, form url.Values, want int) {
		t.Helper()
		if w := postForm(h, "/reply/fragment", form); w.Code != want {
			t.Errorf("status %d; want %d", w.Code, want)
		}
	}
	t.Run("empty text", func(t *testing.T) {
		refused(t, url.Values{"id": {"maya"}, "parent": {""}, "text": {"  "}}, http.StatusBadRequest)
	})
	t.Run("too long", func(t *testing.T) {
		refused(t, url.Values{"id": {"maya"}, "parent": {""}, "text": {strings.Repeat("é", replyRuneLimit+1)}}, http.StatusBadRequest)
	})
	t.Run("unknown parent", func(t *testing.T) {
		refused(t, url.Values{"id": {"maya"}, "parent": {"9.9"}, "text": {"x"}}, http.StatusBadRequest)
	})
	t.Run("deepest level takes no answer", func(t *testing.T) {
		refused(t, url.Values{"id": {"gaming"}, "parent": {"0.0.0.0.0.0.0.0"}, "text": {"x"}}, http.StatusBadRequest)
	})
	t.Run("unknown post", func(t *testing.T) {
		refused(t, url.Values{"id": {"nope"}, "parent": {""}, "text": {"x"}}, http.StatusNotFound)
	})
	t.Run("other origin", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/reply/fragment", strings.NewReader(url.Values{"id": {"maya"}, "parent": {""}, "text": {"x"}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://elsewhere.example")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("status %d; want 403", w.Code)
		}
	})
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
