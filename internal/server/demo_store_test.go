package server

import (
	"bytes"
	"errors"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestDemoPublish(t *testing.T) {
	t.Run("native form uses redirect and displays escaped content", func(t *testing.T) {
		h := testClientHandler(t)
		r := httptest.NewRequest(http.MethodPost, "/compose", strings.NewReader(url.Values{"text": {"Hello <script>demo</script>"}, "draft_token": {"11111111111111111111111111111111"}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusSeeOther || !strings.HasPrefix(w.Header().Get("Location"), "/posts/demo-") {
			t.Fatalf("post returned %d, location %q", w.Code, w.Header().Get("Location"))
		}
		post := httptest.NewRecorder()
		h.ServeHTTP(post, httptest.NewRequest(http.MethodGet, w.Header().Get("Location"), nil))
		if post.Code != http.StatusOK || !strings.Contains(post.Body.String(), "Hello &lt;script&gt;demo&lt;/script&gt;") {
			t.Errorf("post page: %d %s", post.Code, post.Body.String())
		}
		feed := httptest.NewRecorder()
		h.ServeHTTP(feed, httptest.NewRequest(http.MethodGet, "/", nil))
		if !strings.Contains(feed.Body.String(), "Hello &lt;script&gt;demo&lt;/script&gt;") {
			t.Error("post missing from feed")
		}
	})
	t.Run("htmlex multipart produces confirmation", func(t *testing.T) {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		if err := writer.WriteField("text", "Multipart demo"); err != nil {
			t.Fatal(err)
		}
		if err := writer.WriteField("draft_token", "22222222222222222222222222222222"); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/compose/fragment", &body)
		r.Header.Set("Content-Type", writer.FormDataContentType())
		w := httptest.NewRecorder()
		testClientHandler(t).ServeHTTP(w, r)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Post added") || !strings.Contains(w.Body.String(), "/posts/demo-") {
			t.Errorf("unexpected response: %d %s", w.Code, w.Body.String())
		}
	})
	check := func(t *testing.T, text, origin string, status int) {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/compose", strings.NewReader(url.Values{"text": {text}, "draft_token": {"33333333333333333333333333333333"}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		testClientHandler(t).ServeHTTP(w, r)
		if w.Code != status {
			t.Errorf("status = %d; want %d", w.Code, status)
		}
	}
	t.Run("empty rejected", func(t *testing.T) { check(t, "  ", "", http.StatusBadRequest) })
	t.Run("long draft rejected", func(t *testing.T) { check(t, strings.Repeat("a", 4001), "", http.StatusBadRequest) })
	t.Run("cross origin rejected", func(t *testing.T) { check(t, "hello", "https://evil.example", http.StatusForbidden) })
}

func TestPaginationRetainsOriginalBoundary(t *testing.T) {
	h := testClientHandler(t)
	r := httptest.NewRequest(http.MethodPost, "/compose", strings.NewReader(url.Values{"text": {"A new post"}, "draft_token": {"44444444444444444444444444444444"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	created := httptest.NewRecorder()
	h.ServeHTTP(created, r)
	if created.Code != http.StatusSeeOther {
		t.Fatalf("create: %d", created.Code)
	}
	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/feed/more?filter=all&limit=10&anchor=maya", nil))
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), "Adult content") || strings.Contains(page.Body.String(), "A new post") || strings.Contains(page.Body.String(), "Spent the afternoon") {
		t.Errorf("pagination shifted after insert: %d %s", page.Code, page.Body.String())
	}
}

func TestDemoPublishRetry(t *testing.T) {
	h := testClientHandler(t)
	submit := func(text string) *httptest.ResponseRecorder {
		data := url.Values{"text": {text}, "draft_token": {"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}
		r := httptest.NewRequest(http.MethodPost, "/compose", strings.NewReader(data.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	first := submit("uniquely purple capybara")
	retry := submit("uniquely purple capybara")
	conflict := submit("changed version")
	if first.Code != http.StatusSeeOther || retry.Code != http.StatusSeeOther || first.Header().Get("Location") != retry.Header().Get("Location") {
		t.Fatalf("retry returned different outcome: first %d %q, retry %d %q", first.Code, first.Header().Get("Location"), retry.Code, retry.Header().Get("Location"))
	}
	if conflict.Code != http.StatusConflict {
		t.Errorf("changed retry status = %d, want 409", conflict.Code)
	}
	feed := httptest.NewRecorder()
	h.ServeHTTP(feed, httptest.NewRequest(http.MethodGet, "/?q=uniquely+purple+capybara", nil))
	if got := strings.Count(feed.Body.String(), `<article class="post">`); got != 1 {
		t.Errorf("retry created %d posts; want 1", got)
	}
}

func TestConcurrentDemoRetry(t *testing.T) {
	store := &demoStore{}
	type outcome struct {
		post post
		err  error
	}
	results := make(chan outcome, 2)
	submit := func() { p, err := store.add("same draft", "same-token"); results <- outcome{p, err} }
	go submit()
	go submit()
	first, second := <-results, <-results
	if first.err != nil || second.err != nil || first.post.ID != second.post.ID || len(store.list()) != 1 {
		t.Errorf("concurrent retry did not converge: first=%v second=%v count=%d", first.err, second.err, len(store.list()))
	}
}

func TestConcurrentDemoReadAndWrite(t *testing.T) {
	store := &demoStore{}
	first, err := store.add("first post", "first")
	if err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	reads := make(chan []post, 1)
	writes := make(chan error, 1)
	go func() {
		<-start
		reads <- store.list()
	}()
	go func() {
		<-start
		_, err := store.add("second post", "second")
		writes <- err
	}()
	close(start)
	posts := <-reads
	if err := <-writes; err != nil {
		t.Fatalf("concurrent write: %v", err)
	}
	if len(posts) == 1 && posts[0].ID != first.ID {
		t.Error("single-post snapshot does not contain the original post")
	} else if len(posts) == 2 && (posts[0].Text != "second post" || posts[1].ID != first.ID) {
		t.Error("two-post snapshot is not in newest-first order")
	} else if len(posts) != 1 && len(posts) != 2 {
		t.Errorf("snapshot contains %d posts; want one or two", len(posts))
	}
}

func TestDemoSnapshotRemainsStable(t *testing.T) {
	store := &demoStore{}
	first, err := store.add("first", "first")
	if err != nil {
		t.Fatal(err)
	}
	snapshot := store.list()
	second, err := store.add("second", "second")
	if err != nil {
		t.Fatal(err)
	}
	current := store.list()
	if len(snapshot) != 1 || snapshot[0].ID != first.ID || len(current) != 2 || current[0].ID != second.ID || current[1].ID != first.ID {
		t.Error("snapshot changed after a new post or current feed is out of order")
	}
}

func TestFullDemoStore(t *testing.T) {
	stored := post{ID: "demo-existing", Text: "existing draft"}
	store := &demoStore{posts: make([]post, 1000), byToken: map[string]post{"original": stored}}
	if _, err := store.add("new draft", "new-token"); !errors.Is(err, errDemoStoreFull) {
		t.Errorf("new post at capacity: got %v; want storage full", err)
	}
	if got, err := store.add("existing draft", "original"); err != nil || got.ID != stored.ID {
		t.Errorf("retry at capacity: got %q, %v; want %q", got.ID, err, stored.ID)
	}
	if _, err := store.add("changed draft", "original"); !errors.Is(err, errDraftConflict) {
		t.Errorf("changed retry at capacity: got %v; want conflict", err)
	}
	form := url.Values{"text": {"new draft"}, "draft_token": {"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}
	r := httptest.NewRequest(http.MethodPost, "/compose", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := newTestClient(t, false)
	client.app.store = store
	w := httptest.NewRecorder()
	client.app.publishDemo(w, r)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("publish at capacity: status %d; want 503", w.Code)
	}
}

func TestComposerAndMobileActions(t *testing.T) {
	h := testClientHandler(t)
	check := func(path, want string) {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s: missing %q (status %d)", path, want, w.Code)
		}
	}
	check("/", `aria-label="New post"`)
	check("/compose", `Publishing is not ready`)
	check("/compose", `aria-label="Main navigation"`)
	check("/compose", `href="/compose" class="nav-compose is-current" aria-current="page"`)
	check("/static/tailwind.css", `min-height`)
}

func TestHomeHasOneSearchForm(t *testing.T) {
	w := httptest.NewRecorder()
	testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	body := w.Body.String()
	if w.Code != http.StatusOK || strings.Count(body, `type="search"`) != 1 || strings.Contains(body, "Search ForYou") {
		t.Errorf("home has %d search inputs; status %d", strings.Count(body, `type="search"`), w.Code)
	}
}

func TestNoScriptComposerFallback(t *testing.T) {
	h := testClientHandler(t)
	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest(http.MethodGet, "/compose", nil))
	body := page.Body.String()
	if page.Code != http.StatusOK || !strings.Contains(body, `<noscript><link rel="stylesheet" href="/static/no-js.css"></noscript>`) || !strings.Contains(body, `Publishing is not ready`) {
		t.Errorf("composer without a provider should say what is missing; status %d", page.Code)
	}
	quote := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/quote", strings.NewReader(url.Values{"text": {"café"}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	h.ServeHTTP(quote, r)
	if quote.Code != http.StatusOK || !strings.Contains(quote.Body.String(), "5 bytes") || !strings.Contains(quote.Body.String(), "café") {
		t.Errorf("native quote did not preserve the draft and byte count; status %d", quote.Code)
	}
}

func TestDraftBytesAndNativeUnicode(t *testing.T) {
	h := testClientHandler(t)
	text := "  café  "
	quote := httptest.NewRecorder()
	h.ServeHTTP(quote, httptest.NewRequest(http.MethodGet, "/quote?text="+url.QueryEscape(text), nil))
	if quote.Code != http.StatusOK || !strings.Contains(quote.Body.String(), "9 bytes") {
		t.Fatalf("unexpected quote: status %d", quote.Code)
	}
	form := url.Values{"text": {text}, "draft_token": {"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}
	r := httptest.NewRequest(http.MethodPost, "/compose", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	created := httptest.NewRecorder()
	h.ServeHTTP(created, r)
	if created.Code != http.StatusSeeOther {
		t.Fatalf("create status = %d", created.Code)
	}
	page := httptest.NewRecorder()
	h.ServeHTTP(page, httptest.NewRequest(http.MethodGet, created.Header().Get("Location"), nil))
	if !strings.Contains(page.Body.String(), ">  café  </p>") {
		t.Error("published draft lost its surrounding spaces")
	}
	large := url.Values{"text": {strings.Repeat("🙂", 4000)}, "draft_token": {"cccccccccccccccccccccccccccccccc"}}
	largeRequest := httptest.NewRequest(http.MethodPost, "/compose", strings.NewReader(large.Encode()))
	largeRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	largeResult := httptest.NewRecorder()
	h.ServeHTTP(largeResult, largeRequest)
	if largeResult.Code != http.StatusSeeOther {
		t.Errorf("valid 4,000-character Unicode draft status = %d; want 303", largeResult.Code)
	}
}

func TestDraftRejectsAttachmentsAndOversize(t *testing.T) {
	h := testClientHandler(t)
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("text", "hello"); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("draft_token", "dddddddddddddddddddddddddddddddd"); err != nil {
		t.Fatal(err)
	}
	file, err := writer.CreateFormFile("file", "test.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("not supported")); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/compose/fragment", &body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("attachment status = %d; want 400", w.Code)
	}
	quote := httptest.NewRecorder()
	h.ServeHTTP(quote, httptest.NewRequest(http.MethodGet, "/quote?text="+strings.Repeat("x", 4001), nil))
	if quote.Code != http.StatusBadRequest {
		t.Errorf("oversize quote status = %d; want 400", quote.Code)
	}
}

func TestQuoteFeedback(t *testing.T) {
	w := httptest.NewRecorder()
	testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/quote/fragment?text=caf%C3%A9", nil))
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `target="#quote-result(innerHTML)"`) || !strings.Contains(body, `target="#quote-status(innerHTML)"`) || !strings.Contains(body, "5 bytes") {
		t.Errorf("quote feedback missing expected fragments or bytes, status %d", w.Code)
	}
}

func TestQuoteDescribesLastCheck(t *testing.T) {
	w := httptest.NewRecorder()
	testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/quote/fragment?text=hello", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Size (UTF-8)") || !strings.Contains(w.Body.String(), "Check again after editing.") {
		t.Errorf("quote does not describe its snapshot; status %d", w.Code)
	}
}

func TestQuoteFragmentPost(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("text", strings.Repeat("🙂", 4000)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/quote/fragment", &body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	testClientHandler(t).ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "16000 bytes") || !strings.Contains(w.Body.String(), "Check again after editing.") {
		t.Errorf("large quote: status %d; expected 16000-byte response", w.Code)
	}
}

func TestQuoteFragmentPostReturnsUTF8ByteCount(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("text", "héllo"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/quote/fragment", &body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	testClientHandler(t).ServeHTTP(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `<fragment target="#byte-count(innerHTML)">6 bytes</fragment>`) {
		t.Errorf("quote fragment = %d %q; want byte-count fragment with 6 bytes", w.Code, w.Body.String())
	}
}

func TestQuoteFragmentRejectsAttachment(t *testing.T) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("text", "hello"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.CreateFormFile("file", "unsupported.txt"); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/quote/fragment", &body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	w := httptest.NewRecorder()
	testClientHandler(t).ServeHTTP(w, r)
	if w.Code != http.StatusBadRequest {
		t.Errorf("quote with attachment = %d; want 400", w.Code)
	}
}

func TestQuoteFragmentRejectsUnsupportedFormat(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/quote/fragment", strings.NewReader(`{"text":"hello"}`))
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	testClientHandler(t).ServeHTTP(w, r)
	if w.Code != http.StatusUnsupportedMediaType {
		t.Errorf("unsupported quote format = %d; want 415", w.Code)
	}
}

func TestPaginationAnchorSurvivesLaterPost(t *testing.T) {
	h := testClientHandler(t)
	create := func(text, token string) string {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, "/compose", strings.NewReader(url.Values{"text": {text}, "draft_token": {token}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusSeeOther {
			t.Fatalf("create post: status %d", w.Code)
		}
		return strings.TrimPrefix(w.Header().Get("Location"), "/posts/")
	}
	anchor := create("original snapshot head", "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee")
	create("newer post after snapshot", "ffffffffffffffffffffffffffffffff")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/feed/more?limit=10&anchor="+url.QueryEscape(anchor), nil))
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, "Spent the afternoon") || strings.Contains(body, "newer post after snapshot") || !strings.Contains(body, "5 more posts loaded, 10 total.") {
		t.Errorf("anchored continuation changed after insertion: status %d", w.Code)
	}
}

func TestQuoteRefusesAnEmptyDraft(t *testing.T) {
	check := func(t *testing.T, target string) {
		t.Helper()
		w := httptest.NewRecorder()
		testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, target, nil))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "Write the post first.") {
			t.Errorf("%s = %d %q; want 400 naming the missing text", target, w.Code, w.Body.String())
		}
	}
	t.Run("no text parameter", func(t *testing.T) { check(t, "/quote") })
	t.Run("empty text", func(t *testing.T) { check(t, "/quote?text=") })
	t.Run("only spaces", func(t *testing.T) { check(t, "/quote/fragment?text=%20%20") })
}
