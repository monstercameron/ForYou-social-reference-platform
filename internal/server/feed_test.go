package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
		check(t, "/feed/search?filter=all&q=zzz", "No posts found", "community garden", http.StatusOK)
	})
	t.Run("full page fallback", func(t *testing.T) {
		check(t, "/?filter=replies", "most interesting spaces", "community garden", http.StatusOK)
	})
	t.Run("runtime", func(t *testing.T) { check(t, "/static/htmlex/htmlex.js", "initHTMLeX", "", http.StatusOK) })
	t.Run("role isolation", func(t *testing.T) {
		w := httptest.NewRecorder()
		newHandler("provider", nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/feed?filter=all", nil))
		if w.Code != http.StatusNotFound {
			t.Errorf("provider feed status = %d; want 404", w.Code)
		}
	})
}

func TestFilterPreservesSearchAndTheme(t *testing.T) {
	h := testClientHandler(t)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/?filter=text&theme=dark&q=plants", nil))
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `name="q" value="plants"`) || !strings.Contains(body, `aria-current="page"`) || !strings.Contains(body, `theme=light&amp;filter=text&amp;q=plants`) {
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
