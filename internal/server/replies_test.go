package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

func TestReplyThreads(t *testing.T) {
	check := func(t *testing.T, path, present, absent string, status int) {
		t.Helper()
		w := httptest.NewRecorder()
		testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
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
	h := testClientHandler(t)
	feed := httptest.NewRecorder()
	h.ServeHTTP(feed, httptest.NewRequest(http.MethodGet, "/?limit=10000", nil))
	body := feed.Body.String()
	if feed.Code != http.StatusOK || strings.Contains(body, "No replies") || strings.Count(body, `data-thread-control="open"`) != len(staticDemoPosts) {
		t.Errorf("feed has missing reply controls: status %d, controls %d, posts %d", feed.Code, strings.Count(body, `data-thread-control="open"`), len(staticDemoPosts))
	}
	check := func(t *testing.T, id, open, want string) {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/thread?id="+id+"&page=2&open="+open, nil))
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
	h := testClientHandler(t)
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
	testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/thread/fragment?id=maya", nil))
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

// quoteReplyForTest writes a reply and returns the quote the reader would confirm.
func quoteReplyForTest(t *testing.T, h http.Handler, id, parent, text string) string {
	t.Helper()
	w := postForm(h, "/reply/quote/fragment", url.Values{"id": {id}, "parent": {parent}, "text": {text}})
	_, after, found := strings.Cut(w.Body.String(), `name="quote" value="`)
	token, _, closed := strings.Cut(after, `"`)
	if w.Code != http.StatusOK || !found || !closed || len(token) != 32 {
		t.Fatalf("quote for %q returned %d without a quote token", text, w.Code)
	}
	return token
}

func TestReplyCostIsShownBeforePosting(t *testing.T) {
	h := testClientHandler(t)
	t.Run("one started KiB", func(t *testing.T) {
		w := postForm(h, "/reply/quote/fragment", url.Values{"id": {"maya"}, "parent": {""}, "text": {"A short reply <b>"}})
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "A short reply &lt;b&gt;") || !strings.Contains(body, "17 bytes, billed as 1 KiB") || !strings.Contains(body, "<dt>Fee paid in Bitcoin</dt><dd>136 msat</dd>") || !strings.Contains(body, "<dt>Fee paid in Ethereum</dt><dd>48,900,000,000 wei</dd>") || !strings.Contains(body, "Confirm and post") {
			t.Errorf("status %d; the confirmation should show the reply, its size and the schedule 1 fee in both assets: %s", w.Code, body)
		}
	})
	t.Run("nothing is posted by asking for the cost", func(t *testing.T) {
		thread := httptest.NewRecorder()
		h.ServeHTTP(thread, httptest.NewRequest(http.MethodGet, "/thread/fragment?id=maya&page=2", nil))
		if strings.Contains(thread.Body.String(), "A short reply") || !strings.Contains(thread.Body.String(), `<span class="reply-count">6</span>`) {
			t.Error("a quoted reply must not appear in the thread before it is confirmed")
		}
	})
	t.Run("two started KiB", func(t *testing.T) {
		cost, err := costOf(strings.Repeat("a", 1025))
		if err != nil || cost.Units != 2 || cost.Fees[0] != (networkFee{Asset: "Bitcoin", Amount: "272", Unit: "msat"}) || cost.Fees[1] != (networkFee{Asset: "Ethereum", Amount: "97,800,000,000", Unit: "wei"}) {
			t.Errorf("cost of 1025 bytes = %+v, %v; want 2 KiB, 272 msat and 97,800,000,000 wei", cost, err)
		}
	})
	t.Run("edit returns the text to the form", func(t *testing.T) {
		w := postForm(h, "/reply/edit/fragment", url.Values{"id": {"maya"}, "parent": {"0"}, "text": {"Still writing"}})
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, ">Still writing</textarea>") || !strings.Contains(body, `name="parent" value="0"`) || !strings.Contains(body, "Review cost") {
			t.Errorf("status %d; editing should give back the form with the text: %s", w.Code, body)
		}
	})
	t.Run("without scripts", func(t *testing.T) {
		w := postForm(h, "/reply/quote", url.Values{"id": {"noor"}, "parent": {""}, "text": {"Plain form"}})
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "<h1>Confirm your reply</h1>") || !strings.Contains(body, `action="/reply" method="post"`) || !strings.Contains(body, `href="/thread?id=noor">Cancel</a>`) {
			t.Errorf("status %d; a plain form post should get a confirmation page", w.Code)
		}
	})
	t.Run("compose shows the same fee", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/quote/fragment?text=hello", nil))
		if !strings.Contains(w.Body.String(), "5 bytes, billed as 1 KiB") || !strings.Contains(w.Body.String(), "136 msat") {
			t.Errorf("the post quote should show the fee: %s", w.Body.String())
		}
	})
}

func TestQuoteLifetime(t *testing.T) {
	issued := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	store := &demoStore{}
	inTime, _ := store.quoteReply(replyDraft{PostID: "maya", Text: "in time"}, issued)
	late, _ := store.quoteReply(replyDraft{PostID: "maya", Text: "late"}, issued)
	if d, ok := store.takeQuote(inTime, issued.Add(replyQuoteLifetime-time.Second)); !ok || d.Text != "in time" {
		t.Error("a quote should be usable one second before it expires")
	}
	if _, ok := store.takeQuote(inTime, issued.Add(time.Minute)); ok {
		t.Error("a quote must not be usable twice")
	}
	if _, ok := store.takeQuote(late, issued.Add(replyQuoteLifetime)); ok {
		t.Error("a quote must not be usable at its expiry time")
	}
}

func TestAddReply(t *testing.T) {
	h := testClientHandler(t)
	confirm := func(path, id, quote string) *httptest.ResponseRecorder {
		return postForm(h, path, url.Values{"id": {id}, "quote": {quote}})
	}
	t.Run("to the post", func(t *testing.T) {
		w := confirm("/reply/fragment", "poetry", quoteReplyForTest(t, h, "poetry", "", "A first reply <b>"))
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "A first reply &lt;b&gt;") || !strings.Contains(body, `<span class="reply-count">1</span>`) || !strings.Contains(body, `>you</span>`) {
			t.Errorf("status %d; the thread should show the escaped reply, marked as the reader's own, and count it", w.Code)
		}
	})
	t.Run("to a nested reply", func(t *testing.T) {
		w := confirm("/reply/fragment", "maya", quoteReplyForTest(t, h, "maya", "0.0", "Answering the second level"))
		body := w.Body.String()
		at := strings.Index(body, "Answering the second level")
		if w.Code != http.StatusOK || at < 0 || at > strings.Index(body, "And room for things to grow") || !strings.Contains(body, `<span class="reply-count">7</span>`) {
			t.Errorf("status %d; the answer should sit inside the first reply's branch and raise the count to 7", w.Code)
		}
	})
	t.Run("without scripts", func(t *testing.T) {
		w := confirm("/reply", "noor", quoteReplyForTest(t, h, "noor", "", "Plain form"))
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/thread?id=noor&page=2" {
			t.Errorf("status %d, location %q; want a redirect to the whole thread", w.Code, w.Header().Get("Location"))
		}
	})
	t.Run("a quote posts once", func(t *testing.T) {
		quote := quoteReplyForTest(t, h, "sofia", "", "Only once")
		first, second := confirm("/reply/fragment", "sofia", quote), confirm("/reply/fragment", "sofia", quote)
		if first.Code != http.StatusOK || second.Code != http.StatusConflict {
			t.Errorf("statuses %d then %d; the second confirmation of one quote should be refused", first.Code, second.Code)
		}
	})
	t.Run("a quote belongs to its post", func(t *testing.T) {
		if w := confirm("/reply/fragment", "elliot", quoteReplyForTest(t, h, "sofia", "", "Wrong post")); w.Code != http.StatusConflict {
			t.Errorf("status %d; a quote for another post should be refused", w.Code)
		}
	})
	t.Run("text without a quote is not posted", func(t *testing.T) {
		if w := postForm(h, "/reply/fragment", url.Values{"id": {"maya"}, "parent": {""}, "text": {"Unpriced"}}); w.Code != http.StatusBadRequest {
			t.Errorf("status %d; a reply that was never quoted must be refused", w.Code)
		}
	})
	refused := func(t *testing.T, form url.Values, want int) {
		t.Helper()
		if w := postForm(h, "/reply/quote/fragment", form); w.Code != want {
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
		r := httptest.NewRequest(http.MethodPost, "/reply/quote/fragment", strings.NewReader(url.Values{"id": {"maya"}, "parent": {""}, "text": {"x"}}.Encode()))
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
		testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
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

func TestThreadReplyList(t *testing.T) {
	h := testClientHandler(t)
	t.Run("replies are an ordered list with items", func(t *testing.T) {
		body := getWith(h, "/thread/fragment?id=maya")
		if !strings.Contains(body, `<ol class="replies" aria-label="Replies">`) || !strings.Contains(body, `<li class="reply">`) {
			t.Error("the replies should be an ordered list named Replies, one item per reply")
		}
	})
	t.Run("nested replies sit in their parent item", func(t *testing.T) {
		body := getWith(h, "/thread/branch?id=maya&branch=0")
		if !strings.Contains(body, `<ol class="replies nested-replies" aria-label="Replies">`) {
			t.Error("an opened branch should be a nested ordered list")
		}
	})
	t.Run("the cost note says the test network charges nothing", func(t *testing.T) {
		w := postForm(h, "/reply/quote/fragment", url.Values{"id": {"maya"}, "parent": {""}, "text": {"Wording"}})
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "A reply is a post: it pays the same fee and stays public. This is a test network, so nothing is really charged. This price holds for 10 minutes.") {
			t.Errorf("status %d; the confirm note should use the agreed wording: %s", w.Code, body)
		}
	})
}

// thrSource serves threads from fixtures and counts the reply lookups the handlers make.
type thrSource struct {
	recordSource
	replies  []reply
	coverage Coverage
	lookups  int
}

func (s *thrSource) Thread(_ context.Context, _ *http.Request, postID string) ([]reply, error) {
	s.lookups++
	return prepareReplies(s.replies, postID, "", replyDepthLimit), nil
}

func (s *thrSource) Replies(context.Context, *http.Request, string) (recordPage, error) {
	s.lookups++
	return recordPage{Coverage: s.coverage}, nil
}

func thrHandler(t *testing.T, source *thrSource) http.Handler {
	t.Helper()
	client, err := NewClient(Config{DataDir: t.TempDir(), PublicOrigin: "https://example.test"})
	if err != nil {
		t.Fatal(err)
	}
	source.recordSource = client.app.recordSource
	client.app.recordSource = source
	return client.Handler()
}

func TestThreadInlineDepthAndHeading(t *testing.T) {
	h := testClientHandler(t)
	t.Run("inline replies stop at the third level with a link on", func(t *testing.T) {
		body := getWith(h, "/thread/fragment?id=gaming&open=0.0")
		if !strings.Contains(body, `<a class="thread-continue" href="/thread?id=gaming&amp;open=0.0.0">Continue this thread</a>`) || strings.Contains(body, "best thread on my feed today") {
			t.Error("a deep thread should be cut at the third level with Continue this thread")
		}
	})
	t.Run("phones stop at the second level", func(t *testing.T) {
		body := getWith(h, "/thread/fragment?id=gaming&open=0.0")
		if !strings.Contains(body, `<a class="thread-continue thread-continue-phone" href="/thread?id=gaming&amp;open=0.0">Continue this thread</a>`) {
			t.Error("the second level should link on for phones")
		}
	})
	t.Run("phones hide the branch below the second level", func(t *testing.T) {
		body := getWith(h, "/thread/fragment?id=gaming&open=0.0")
		if !strings.Contains(body, `class="reply-branch reply-branch-wide"`) {
			t.Error("the branch under a phone Continue link should be marked for wide screens only")
		}
	})
	t.Run("opening the thread moves focus to its heading", func(t *testing.T) {
		body := getWith(h, "/thread/fragment?id=maya&view=closed")
		if !strings.Contains(body, `data-focus-after-swap=".thread-heading"`) {
			t.Error("the View replies control should move focus to the Replies found heading")
		}
	})
	t.Run("the full page shows the deep reply without a cut", func(t *testing.T) {
		body := getWith(h, "/thread?id=gaming&open=0.0.0.0.0.0.0")
		if !strings.Contains(body, "best thread on my feed today") || strings.Contains(body, "Continue this thread") {
			t.Error("the full page should show every level it holds")
		}
	})
	t.Run("the heading counts every reply found, folded ones too, and the sources that answered", func(t *testing.T) {
		body := getWith(h, "/thread/fragment?id=maya")
		if !strings.Contains(body, `<h2 id="thread-heading-maya" class="thread-heading" tabindex="-1">Replies found: 6 on 1 of 1 sources</h2>`) {
			t.Error("the thread should say how many replies it found and from how many sources")
		}
	})
	t.Run("the full page has the same heading and the coverage line", func(t *testing.T) {
		body := getWith(h, "/thread?id=maya")
		if !strings.Contains(body, `<h2 id="thread-heading-maya" class="thread-heading" tabindex="-1">Replies found: 6 on 1 of 1 sources</h2>`) || !strings.Contains(body, `<p class="thread-coverage">`) {
			t.Error("the full page should show the heading and the coverage line")
		}
	})
	t.Run("an empty result says others may exist elsewhere", func(t *testing.T) {
		body := getWith(h, "/thread/fragment?id=weather")
		if !strings.Contains(body, "Your sources returned no replies. Others may exist elsewhere.") || strings.Contains(body, "Nothing here yet") {
			t.Error("an empty thread should not claim that no replies exist")
		}
	})
	t.Run("the expanded list continues the numbering", func(t *testing.T) {
		body := getWith(h, "/thread/fragment?id=maya&page=2")
		if !strings.Contains(body, `<ol class="replies" aria-label="Replies" start="3">`) {
			t.Error("the expanded list should start after the replies above it")
		}
	})
}

func TestThreadFromRecordSource(t *testing.T) {
	coverage := Coverage{Sources: []SourceStatus{{Label: "Provider", OK: true}, {Label: "Mirror", OK: false}}}
	t.Run("a reply the source returns twice is shown once", func(t *testing.T) {
		h := thrHandler(t, &thrSource{replies: []reply{{ID: "dup", Text: "Said twice"}, {ID: "dup", Text: "Said twice"}}, coverage: coverage})
		body := getWith(h, "/thread/fragment?id=maya")
		if strings.Count(body, "Said twice") != 1 || !strings.Contains(body, "Replies found: 1 on 1 of 2 sources") {
			t.Error("a duplicate reply should be shown once and counted once")
		}
	})
	t.Run("a reply to an earlier version links both versions", func(t *testing.T) {
		h := thrHandler(t, &thrSource{replies: []reply{{ID: "answer", Text: "Newer", RepliedTo: "maya-v1", Selected: "maya-v2", ContextChanged: true}}})
		body := getWith(h, "/thread/fragment?id=maya")
		if !strings.Contains(body, `<a href="/posts/maya-v1">Replied to an earlier version</a>`) || !strings.Contains(body, `<a href="/posts/maya-v2">Current version</a>`) {
			t.Error("a reply to an earlier version should link that version and the current one")
		}
	})
	t.Run("the feed does not look up replies", func(t *testing.T) {
		source := &thrSource{}
		getWith(thrHandler(t, source), "/")
		if source.lookups != 0 {
			t.Errorf("the feed made %d reply lookups; want none", source.lookups)
		}
	})
}
