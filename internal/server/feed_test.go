package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"testing"

	"foryou.local/foryou/internal/sdk"
)

func TestFeedFilters(t *testing.T) {
	check := func(t *testing.T, path, present, absent string, status int) {
		t.Helper()
		w := httptest.NewRecorder()
		testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		body := w.Body.String()
		if w.Code != status || !strings.Contains(body, present) || (absent != "" && strings.Contains(body, absent)) {
			t.Errorf("%s = status %d, body %q; want status %d, present %q and absent %q", path, w.Code, body, status, present, absent)
		}
	}
	t.Run("initial page", func(t *testing.T) { check(t, "/", "community garden", "", http.StatusOK) })
	t.Run("notes", func(t *testing.T) {
		check(t, "/feed?filter=notes", "community garden", "most interesting spaces", http.StatusOK)
	})
	t.Run("replies", func(t *testing.T) {
		check(t, "/feed?filter=replies", "most interesting spaces", "community garden", http.StatusOK)
	})
	t.Run("unknown filter", func(t *testing.T) {
		check(t, "/feed?filter=unknown", "invalid feed filter", "community garden", http.StatusBadRequest)
	})
	t.Run("search", func(t *testing.T) {
		check(t, "/feed/search?filter=all&q=growing", "small things growing", "community garden", http.StatusOK)
	})
	t.Run("no matches", func(t *testing.T) {
		check(t, "/feed/search?filter=all&q=zzz", "No posts match \"zzz\"", "community garden", http.StatusOK)
	})
	t.Run("full page fallback", func(t *testing.T) {
		check(t, "/?filter=replies", "most interesting spaces", "community garden", http.StatusOK)
	})
	t.Run("runtime", func(t *testing.T) { check(t, "/static/htmlex/htmlex.js", "initHTMLeX", "", http.StatusOK) })
}

func TestFilterPreservesSearchAndTheme(t *testing.T) {
	h := testClientHandler(t)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/?filter=text&theme=dark&q=plants", nil))
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `name="q" value="plants"`) || !strings.Contains(body, `aria-current="page"`) || !strings.Contains(body, `href="/theme?choice=light&amp;back=%2f%3ffilter%3dtext%26q%3dplants"`) {
		t.Errorf("filter context missing in status %d: %s", w.Code, body)
	}
}

func TestNavigationComponents(t *testing.T) {
	h := testClientHandler(t)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/?filter=video&q=growing&theme=dark", nil))
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `aria-label="Primary"`) || !strings.Contains(body, `aria-current="page"`) || !strings.Contains(body, `name="filter" value="video"`) || !strings.Contains(body, `id="about-demo"`) {
		t.Errorf("navigation components or search state missing, status %d", w.Code)
	}
}

func TestFeedVideoFacadesAndIcons(t *testing.T) {
	w := httptest.NewRecorder()
	testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	body := w.Body.String()
	if w.Code != http.StatusOK {
		t.Fatalf("feed status = %d; want 200", w.Code)
	}
	if strings.Contains(body, "👍") || strings.Contains(body, "👎") {
		t.Errorf("feed uses emoji for feedback icons")
	}
	if strings.Count(body, `video-facade`) != 2 || strings.Count(body, `<noscript><video`) != 2 || !strings.Contains(body, `aria-label="Play Flower video"`) || !strings.Contains(body, `aria-label="Play Home server monitor video"`) || strings.Count(body, `get="/feed"`) != 2 || !strings.Contains(body, `target="#video-elliot(outerHTML)" timeout="10000" onerror="#thread-error(innerHTML)"><input type="hidden" name="play" value="elliot">`) || !strings.Contains(body, `target="#video-tech-video(outerHTML)" timeout="10000" onerror="#thread-error(innerHTML)"><input type="hidden" name="play" value="tech-video">`) {
		t.Errorf("video facade or accessible play control missing")
	}
	if !strings.Contains(body, `poster="/static/demo-flower-poster.svg"`) || !strings.Contains(body, `poster="/static/demo-tech-poster.svg"`) {
		t.Errorf("video poster missing")
	}
}

func TestFeedHasOneAccessibleActionOfEachKindPerPost(t *testing.T) {
	w := httptest.NewRecorder()
	testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("feed status = %d; want 200", w.Code)
	}
	check := func(t *testing.T, id string) {
		t.Helper()
		marker := `id="thread-` + id + `"`
		markerAt := strings.Index(w.Body.String(), marker)
		if markerAt < 0 {
			t.Fatalf("post %q is missing its replies slot", id)
		}
		start := strings.LastIndex(w.Body.String()[:markerAt], `<article class="post">`)
		end := strings.Index(w.Body.String()[markerAt:], `</article>`)
		if start < 0 || end < 0 {
			t.Fatalf("post %q article boundaries are missing", id)
		}
		article := w.Body.String()[start : markerAt+end+len(`</article>`)]
		if strings.Count(article, `aria-label="Like"`) != 1 || strings.Count(article, `aria-label="Dislike"`) != 1 || strings.Count(article, `>Embed</span>`) != 1 || strings.Count(article, `>Open post</span>`) != 1 || strings.Count(article, `class="thread-toggle post-action"`) != 1 {
			t.Errorf("post %q does not have one accessible action in each slot", id)
		}
		if got := strings.Count(article, `<span class="reply-label">replies</span>`) + strings.Count(article, `<span>No replies</span>`); got != 1 {
			t.Errorf("post %q replies label count = %d; want 1", id, got)
		}
	}
	t.Run("maya", func(t *testing.T) { check(t, "maya") })
	t.Run("elliot", func(t *testing.T) { check(t, "elliot") })
	t.Run("tech video", func(t *testing.T) { check(t, "tech-video") })
	t.Run("sponsored post", func(t *testing.T) { check(t, "ad-coffee") })
	t.Run("art post", func(t *testing.T) { check(t, "art-studio") })
}

func TestFeedVideoPlayerFragments(t *testing.T) {
	check := func(t *testing.T, id, present string, wantStatus int) {
		t.Helper()
		w := httptest.NewRecorder()
		testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/feed?play="+id, nil))
		if w.Code != wantStatus || (present != "" && !strings.Contains(w.Body.String(), present)) {
			t.Errorf("play %q: status %d, expected %d and %q", id, w.Code, wantStatus, present)
		}
	}
	t.Run("flower player", func(t *testing.T) {
		check(t, "elliot", `<video class="post-video" controls autoplay playsinline preload="auto" poster="/static/demo-flower-poster.svg"`, http.StatusOK)
	})
	t.Run("tech player", func(t *testing.T) {
		check(t, "tech-video", `<video class="post-video" controls autoplay playsinline preload="auto" poster="/static/demo-tech-poster.svg"`, http.StatusOK)
	})
	t.Run("text post rejected", func(t *testing.T) { check(t, "maya", "", http.StatusBadRequest) })
	t.Run("unknown post rejected", func(t *testing.T) { check(t, "missing", "", http.StatusBadRequest) })
}

func TestFeedRejectsInvalidQueryEncoding(t *testing.T) {
	t.Run("malformed percent escape", func(t *testing.T) {
		w := httptest.NewRecorder()
		testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/?q=%GG", nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("malformed escape status = %d; want 400", w.Code)
		}
	})
	w := httptest.NewRecorder()
	testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/?q=%FF", nil))
	if w.Code != http.StatusBadRequest {
		t.Errorf("invalid UTF-8 status = %d; want 400", w.Code)
	}
}

// feedFakeRecords serves a fixed list of records through Recent, with the
// position as a plain offset, answers a live read with live, and leaves every
// other method to the real source.
type feedFakeRecords struct {
	recordSource
	posts    []post
	live     []post
	coverage Coverage
}

func (f feedFakeRecords) Posts(context.Context, *http.Request) ([]post, error) {
	return slices.Clone(f.live), nil
}

func (f feedFakeRecords) Recent(_ context.Context, _ *http.Request, position string, limit int) (recordPage, error) {
	start := 0
	if position != "" {
		offset, err := strconv.Atoi(position)
		if err != nil {
			return recordPage{}, errRecordPosition
		}
		start = offset
	}
	end := min(start+limit, len(f.posts))
	page := recordPage{Posts: slices.Clone(f.posts[start:end]), Coverage: f.coverage}
	if end < len(f.posts) {
		page.Next = strconv.Itoa(end)
	}
	return page, nil
}

// feedClientWith returns a client whose record source is replaced by fake.
func feedClientWith(t *testing.T, fake feedFakeRecords) *Client {
	t.Helper()
	client, err := NewClient(Config{DataDir: t.TempDir(), PublicOrigin: "https://example.test"})
	if err != nil {
		t.Fatal(err)
	}
	fake.recordSource = client.app.recordSource
	client.app.recordSource = fake
	return client
}

func feedBody(t *testing.T, handler http.Handler, path string) (int, string) {
	t.Helper()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w.Code, w.Body.String()
}

func feedSettled(id, text string) post {
	return post{ID: id, Text: text, Receipt: "fy1-" + id, Kind: "text", Color: "blue", Time: "1 hour ago", Settlement: "settled", Class: "locally_admitted"}
}

func TestHomeFeedFromRecords(t *testing.T) {
	records := []post{
		feedSettled("one", "Settled note"),
		{ID: "two", Text: "Pending note", Receipt: "fy1-two", Kind: "text", Color: "mint", Time: "2 hours ago", Settlement: "pending_settlement", RecheckBy: "2026-12-01", Class: "locally_admitted"},
		{ID: "three", Text: "Unsettled note", Receipt: "fy1-three", Kind: "text", Color: "peach", Time: "3 hours ago", Settlement: "unsettled", Class: "locally_admitted"},
		{ID: "four", Text: "Untrusted note", Receipt: "fy1-four", Kind: "text", Color: "lilac", Time: "4 hours ago", Settlement: "unsettled", Class: "cryptographically_valid_untrusted"},
		{ID: "five", Text: "Reply note", Receipt: "fy1-five", Kind: "text", Color: "violet", Time: "5 hours ago", IsReply: true, ReplyTo: "one", Class: "locally_admitted"},
	}
	handler := feedClientWith(t, feedFakeRecords{posts: records}).Handler()
	t.Run("settled, pending and unsettled records show their badges", func(t *testing.T) {
		code, body := feedBody(t, handler, "/")
		if code != http.StatusOK || !strings.Contains(body, "Settled note") || !strings.Contains(body, "Pending note") || !strings.Contains(body, "Unsettled note") {
			t.Fatalf("status %d; posts missing", code)
		}
		if !strings.Contains(body, "<span>Settled</span>") || !strings.Contains(body, "<span>Pending</span>") || !strings.Contains(body, "<span>Unsettled</span>") {
			t.Errorf("badge text missing")
		}
	})
	t.Run("untrusted records are hidden with a count and a link", func(t *testing.T) {
		_, body := feedBody(t, handler, "/")
		if strings.Contains(body, "Untrusted note") {
			t.Errorf("untrusted record shown by default")
		}
		if !strings.Contains(body, "1 post from a provider you do not trust is hidden") || !strings.Contains(body, "untrusted=1") {
			t.Errorf("hidden count or toggle link missing")
		}
	})
	t.Run("untrusted records appear with the toggle on", func(t *testing.T) {
		_, body := feedBody(t, handler, "/?untrusted=1")
		if !strings.Contains(body, "Untrusted note") || strings.Contains(body, "is hidden") {
			t.Errorf("untrusted record missing with untrusted=1")
		}
	})
	t.Run("replies filter returns only replies", func(t *testing.T) {
		_, body := feedBody(t, handler, "/?filter=replies")
		if !strings.Contains(body, "Reply note") || strings.Contains(body, "Settled note") {
			t.Errorf("replies filter did not keep only replies")
		}
	})
	t.Run("unknown filter is rejected", func(t *testing.T) {
		if code, _ := feedBody(t, handler, "/?filter=sideways"); code != http.StatusBadRequest {
			t.Errorf("status = %d; want 400", code)
		}
	})
	t.Run("malformed position is rejected", func(t *testing.T) {
		if code, _ := feedBody(t, handler, "/?position=abc"); code != http.StatusBadRequest {
			t.Errorf("status = %d; want 400", code)
		}
	})
}

func TestHomeLoadMoreAndCoverage(t *testing.T) {
	records := []post{
		feedSettled("p0", "Alpha post"),
		feedSettled("p1", "Bravo post"),
		feedSettled("p2", "Charlie post"),
		feedSettled("p3", "Delta post"),
		feedSettled("p4", "Echo post"),
		feedSettled("p5", "Foxtrot post"),
		feedSettled("p6", "Golf post"),
	}
	handler := feedClientWith(t, feedFakeRecords{posts: records}).Handler()
	t.Run("first page shows five posts and the next position", func(t *testing.T) {
		_, body := feedBody(t, handler, "/")
		if !strings.Contains(body, "Echo post") || strings.Contains(body, "Foxtrot post") || !strings.Contains(body, `name="position" value="5"`) {
			t.Errorf("first page wrong: next form or posts missing")
		}
	})
	t.Run("more fragment returns only the next cards and the end marker", func(t *testing.T) {
		code, body := feedBody(t, handler, "/feed/more?position=5&filter=all")
		if code != http.StatusOK || !strings.Contains(body, "Foxtrot post") || !strings.Contains(body, "Golf post") || strings.Contains(body, "Echo post") {
			t.Errorf("status %d; next cards wrong", code)
		}
		if !strings.Contains(body, "You have reached the end of what your sources returned.") || strings.Contains(body, "everything") {
			t.Errorf("end marker missing or overclaims")
		}
		if !strings.Contains(body, "2 more posts loaded.") {
			t.Errorf("status announcement missing")
		}
	})
	t.Run("full page with a position renders the next page and a newer link", func(t *testing.T) {
		_, body := feedBody(t, handler, "/?position=5")
		if !strings.Contains(body, "Foxtrot post") || strings.Contains(body, "Alpha post") || !strings.Contains(body, "Newer posts") {
			t.Errorf("no-script next page wrong")
		}
	})
	t.Run("coverage names each source and its own failure", func(t *testing.T) {
		handler := feedClientWith(t, feedFakeRecords{posts: records, coverage: Coverage{Note: "Records this server has fetched", Sources: []SourceStatus{
			{Label: "Provider A", OK: false, Signal: sdk.SignalRateLimited, Text: "rate limited here; retry after 30 s"},
			{Label: "Provider B", OK: true, Text: "synced 1 hour ago: 4 records kept"},
		}}}).Handler()
		_, body := feedBody(t, handler, "/")
		if !strings.Contains(body, "rate limited here; retry after 30 s") || !strings.Contains(body, "Results may be incomplete.") || !strings.Contains(body, "Alpha post") {
			t.Errorf("per-source failure or incomplete line missing")
		}
		if strings.Contains(body, "Try again") {
			t.Errorf("try again shown although one source answered")
		}
	})
	t.Run("every source failing shows try again", func(t *testing.T) {
		handler := feedClientWith(t, feedFakeRecords{coverage: Coverage{Sources: []SourceStatus{{Label: "Provider A", Text: "unavailable now"}}}}).Handler()
		_, body := feedBody(t, handler, "/")
		if !strings.Contains(body, "Try again") {
			t.Errorf("try again link missing")
		}
	})
	t.Run("unavailable trust root shows the notice", func(t *testing.T) {
		client := feedClientWith(t, feedFakeRecords{posts: records})
		client.app.config.Providers = []Endpoint{{URL: "http://127.0.0.1:1", Label: "Provider A"}}
		_, body := feedBody(t, client.Handler(), "/")
		if !strings.Contains(body, "Settlement cannot be checked; records are shown as not verifiable.") {
			t.Errorf("unavailable trust notice missing")
		}
	})
	t.Run("empty network shows links to network and compose", func(t *testing.T) {
		_, body := feedBody(t, feedClientWith(t, feedFakeRecords{}).Handler(), "/")
		if !strings.Contains(body, "No posts from your sources yet") || !strings.Contains(body, `href="/network"`) || !strings.Contains(body, `href="/compose"`) {
			t.Errorf("empty state links missing")
		}
	})
	t.Run("nothing held reads the sources live and pages what they returned", func(t *testing.T) {
		handler := feedClientWith(t, feedFakeRecords{live: []post{
			{ID: "l0", Text: "Live one", Receipt: "fy1-l0", Kind: "text", Color: "blue", Time: "1 min ago", Unverifiable: "The trust root is unavailable"},
			{ID: "l1", Text: "Live two", Receipt: "fy1-l1", Kind: "text", Color: "blue", Time: "2 min ago"},
			{ID: "l2", Text: "Live three", Receipt: "fy1-l2", Kind: "text", Color: "blue", Time: "3 min ago"},
			{ID: "l3", Text: "Live four", Receipt: "fy1-l3", Kind: "text", Color: "blue", Time: "4 min ago"},
			{ID: "l4", Text: "Live five", Receipt: "fy1-l4", Kind: "text", Color: "blue", Time: "5 min ago"},
			{ID: "l5", Text: "Live six", Receipt: "fy1-l5", Kind: "text", Color: "blue", Time: "6 min ago"},
		}}).Handler()
		_, body := feedBody(t, handler, "/")
		if !strings.Contains(body, "Live one") || !strings.Contains(body, "Not verifiable now") || strings.Contains(body, "Live six") || !strings.Contains(body, `name="limit" value="10"`) {
			t.Errorf("live first page wrong: posts, badge or next window missing")
		}
	})
	t.Run("empty filter offers browse all instead of clear search", func(t *testing.T) {
		_, body := feedBody(t, handler, "/?filter=video")
		if !strings.Contains(body, "No posts in this filter yet") || !strings.Contains(body, "Browse all posts") || strings.Contains(body, "Clear search") {
			t.Errorf("empty filter state wrong")
		}
	})
	t.Run("search without results names the query", func(t *testing.T) {
		_, body := feedBody(t, handler, "/?q=zebra")
		if !strings.Contains(body, `No posts match "zebra"`) {
			t.Errorf("search empty state missing the query")
		}
	})
}
