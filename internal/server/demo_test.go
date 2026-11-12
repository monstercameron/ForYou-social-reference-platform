package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDemoViews(t *testing.T) {
	check := func(t *testing.T, path, want, absent string, status int) {
		t.Helper()
		w := httptest.NewRecorder()
		testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != status || !strings.Contains(w.Body.String(), want) || (absent != "" && strings.Contains(w.Body.String(), absent)) {
			t.Errorf("%s: got status %d; wanted %d and %q without %q", path, w.Code, status, want, absent)
		}
	}
	t.Run("video filter", func(t *testing.T) {
		check(t, "/feed?filter=video", "demo-flower.mp4", "community garden", http.StatusOK)
	})
	t.Run("image filter", func(t *testing.T) {
		check(t, "/feed?filter=images", "Slideshow", "demo-flower.mp4", http.StatusOK)
	})
	t.Run("gallery second slide", func(t *testing.T) { check(t, "/gallery/fragment?id=sofia&slide=2", "2 / 3", "1 / 3", http.StatusOK) })
	t.Run("invalid gallery", func(t *testing.T) {
		check(t, "/gallery/fragment?id=sofia&slide=8", "404", "landscape", http.StatusNotFound)
	})
	t.Run("dark appearance", func(t *testing.T) { check(t, "/?theme=dark", `data-theme="dark"`, "maya.c", http.StatusOK) })
	t.Run("mock quote counts UTF-8", func(t *testing.T) { check(t, "/quote/fragment?text=caf%C3%A9", "5 bytes", "6 bytes", http.StatusOK) })
}

func TestFeedPagination(t *testing.T) {
	check := func(t *testing.T, path string, count int, present, absent string, status int) {
		t.Helper()
		w := httptest.NewRecorder()
		testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		body := w.Body.String()
		if w.Code != status || strings.Count(body, `<article class="post">`) != count || !strings.Contains(body, present) || (absent != "" && strings.Contains(body, absent)) {
			t.Errorf("%s: status %d, posts %d; expected %d posts and %q without %q", path, w.Code, strings.Count(body, `<article class="post">`), count, present, absent)
		}
	}
	t.Run("initial five", func(t *testing.T) { check(t, "/", 5, `auto="lazy"`, "Adult content", http.StatusOK) })
	t.Run("next five", func(t *testing.T) {
		check(t, "/feed/more?limit=10", 5, "Adult content", "community garden", http.StatusOK)
	})
	t.Run("later window preserves anchor", func(t *testing.T) {
		check(t, "/feed/more?limit=15&anchor=maya", 5, `name="anchor" value="maya"`, "Tonight's sky", http.StatusOK)
	})
	t.Run("native fallback", func(t *testing.T) { check(t, "/?limit=10", 10, "Adult content", "LIMITED OFFER!", http.StatusOK) })
	t.Run("filtered final page", func(t *testing.T) {
		check(t, "/feed/more?filter=text&q=content&limit=10", 0, "You have reached the end of what your sources returned.", `id="feed-more"`, http.StatusOK)
	})
	t.Run("invalid page", func(t *testing.T) { check(t, "/feed/more?limit=6", 0, "invalid", "", http.StatusBadRequest) })
	t.Run("anchor outside filtered results", func(t *testing.T) {
		check(t, "/feed/more?filter=video&anchor=maya&limit=10", 0, "invalid", "community garden", http.StatusBadRequest)
	})
	t.Run("missing anchor", func(t *testing.T) {
		check(t, "/feed/more?anchor=missing&limit=10", 0, "invalid", "community garden", http.StatusBadRequest)
	})
}

func TestGalleryNavigation(t *testing.T) {
	check := func(t *testing.T, path, present, absent string) {
		t.Helper()
		w := httptest.NewRecorder()
		testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, present) || (absent != "" && strings.Contains(body, absent)) {
			t.Errorf("%s: status %d; want %q and not %q", path, w.Code, present, absent)
		}
	}
	t.Run("feed opens first slide", func(t *testing.T) {
		check(t, "/?filter=images", `class="gallery-open" action="/gallery" method="get" get="/gallery/viewer"`, `style="`)
	})
	t.Run("fragment preserves lazy navigation", func(t *testing.T) {
		check(t, "/gallery/fragment?id=sofia&slide=2", `get="/gallery/fragment"`, `style="`)
	})
	t.Run("full page uses addressable navigation", func(t *testing.T) {
		check(t, "/gallery?id=sofia&slide=2", `name="slide" value="3"`, `get="/gallery/fragment"`)
	})
	t.Run("last slide wraps to first", func(t *testing.T) { check(t, "/gallery?id=sofia&slide=3", `name="slide" value="1"`, `style="`) })
}

func TestGalleryFocusTargets(t *testing.T) {
	w := httptest.NewRecorder()
	testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/gallery/fragment?id=sofia&slide=2", nil))
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `data-gallery-direction="previous"`) || !strings.Contains(body, `data-gallery-direction="next"`) {
		t.Errorf("gallery fragment is missing keyboard focus targets; status %d", w.Code)
	}
}

func TestFeedLoadAnnouncements(t *testing.T) {
	check := func(t *testing.T, path, want string) {
		t.Helper()
		w := httptest.NewRecorder()
		testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), want) {
			t.Errorf("%s: status %d; missing %q", path, w.Code, want)
		}
	}
	t.Run("five new posts", func(t *testing.T) { check(t, "/feed/more?limit=10", `5 more posts loaded, 10 total.`) })
	t.Run("partial final page", func(t *testing.T) { check(t, "/feed/more?limit=45", `4 more posts loaded, 44 total.`) })
	t.Run("empty filtered page", func(t *testing.T) {
		check(t, "/feed/more?filter=text&q=content&limit=10", `0 more posts loaded, 0 total.`)
	})
}

func TestClientPagesUseCompiledStyles(t *testing.T) {
	h := testClientHandler(t)
	check := func(t *testing.T, path string) {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, `href="/static/tailwind.css"`) || strings.Contains(body, `href="/static/app.css"`) || strings.Contains(body, ` style="`) || strings.Contains(body, `<style`) {
			t.Errorf("%s: expected compiled stylesheet without inline or legacy CSS; status %d", path, w.Code)
		}
	}
	t.Run("feed", func(t *testing.T) { check(t, "/") })
	t.Run("composer", func(t *testing.T) { check(t, "/compose") })
	t.Run("post", func(t *testing.T) { check(t, "/posts/maya") })
	t.Run("embed", func(t *testing.T) { check(t, "/embed/maya") })
	t.Run("thread", func(t *testing.T) { check(t, "/thread?id=maya") })
	t.Run("gallery", func(t *testing.T) { check(t, "/gallery?id=sofia&slide=1") })
}
