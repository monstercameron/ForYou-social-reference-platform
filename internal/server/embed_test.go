package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPostEmbeds(t *testing.T) {
	t.Setenv("FORYOU_PUBLIC_ORIGIN", "https://example.test")
	check := func(t *testing.T, path, want string, status int) {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.Host = "untrusted.invalid"
		w := httptest.NewRecorder()
		newHandler("client", nil).ServeHTTP(w, r)
		if w.Code != status || !strings.Contains(w.Body.String(), want) || strings.Contains(w.Body.String(), "untrusted.invalid") {
			t.Errorf("%s: status %d; wanted %d and %q without request host", path, w.Code, status, want)
		}
	}
	t.Run("post link", func(t *testing.T) { check(t, "/posts/maya", "community garden", http.StatusOK) })
	t.Run("embed content", func(t *testing.T) { check(t, "/embed/maya", "community garden", http.StatusOK) })
	t.Run("share code", func(t *testing.T) {
		check(t, "/share/fragment?id=maya", "https://example.test/embed/maya", http.StatusOK)
	})
	t.Run("second post", func(t *testing.T) { check(t, "/embed/elliot", "small things growing", http.StatusOK) })
	t.Run("technology video on post page", func(t *testing.T) { check(t, "/posts/tech-video", "demo-tech.mp4", http.StatusOK) })
	t.Run("technology video in embed", func(t *testing.T) { check(t, "/embed/tech-video", "demo-tech.mp4", http.StatusOK) })
	t.Run("unknown post", func(t *testing.T) { check(t, "/embed/unknown", "Post not found", http.StatusNotFound) })
}

func TestInvalidPublicOrigin(t *testing.T) {
	t.Setenv("FORYOU_PUBLIC_ORIGIN", "http://example.test")
	w := httptest.NewRecorder()
	newHandler("client", nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/share/fragment?id=maya", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d; want 503 for invalid public origin", w.Code)
	}
}

func TestVideoComponent(t *testing.T) {
	check := func(t *testing.T, path, asset string) {
		t.Helper()
		w := httptest.NewRecorder()
		newHandler("client", nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, asset) || !strings.Contains(body, `controls preload="none"`) {
			t.Errorf("%s: video asset or deferred loading missing (status %d)", path, w.Code)
		}
	}
	t.Run("feed video", func(t *testing.T) { check(t, "/?filter=video", "demo-tech.mp4") })
	t.Run("standalone video", func(t *testing.T) { check(t, "/posts/tech-video", "demo-tech.mp4") })
	t.Run("embedded video", func(t *testing.T) { check(t, "/embed/tech-video", "demo-tech.mp4") })
}

func TestStandalonePostWithoutTopic(t *testing.T) {
	w := httptest.NewRecorder()
	newHandler("client", nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/posts/journal", nil))
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `class="topic"`) || !strings.Contains(w.Body.String(), "rain.\n\nAfter a few weeks") {
		t.Errorf("standalone post lost paragraph text or showed an empty topic: status %d", w.Code)
	}
}

func TestShareFallbackReturnsToPost(t *testing.T) {
	w := httptest.NewRecorder()
	newHandler("client", nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/share?id=maya", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `action="/posts/maya"`) || strings.Contains(w.Body.String(), `get="/share/fragment"`) {
		t.Errorf("share page close does not return to its post as a plain form, status %d", w.Code)
	}
}

func TestDetailPagesUseTheAppShell(t *testing.T) {
	h := newHandler("client", nil)
	checkShell := func(t *testing.T, path string) {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `<nav`) || !strings.Contains(w.Body.String(), `aria-label="Primary"`) {
			t.Errorf("%s: status %d; app navigation landmark missing", path, w.Code)
		}
	}
	t.Run("post page", func(t *testing.T) { checkShell(t, "/posts/maya") })
	t.Run("thread page", func(t *testing.T) { checkShell(t, "/thread?id=maya") })
	t.Run("share page", func(t *testing.T) { checkShell(t, "/share?id=maya") })
	t.Run("gallery page", func(t *testing.T) { checkShell(t, "/gallery?id=sofia&slide=1") })
	t.Run("embed omits the shell", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/embed/maya", nil))
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), `aria-label="Primary"`) {
			t.Errorf("embed page unexpectedly includes navigation (status %d)", w.Code)
		}
	})
}

func TestDetailNotFoundPages(t *testing.T) {
	h := newHandler("client", nil)
	check := func(t *testing.T, path, want string, hasShell bool) {
		t.Helper()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		body := w.Body.String()
		if w.Code != http.StatusNotFound || !strings.Contains(body, want) || strings.Contains(body, `aria-label="Primary"`) != hasShell {
			t.Errorf("%s: status %d; want 404 with %q and shell=%t", path, w.Code, want, hasShell)
		}
	}
	t.Run("post", func(t *testing.T) { check(t, "/posts/unknown", "Page not found", true) })
	t.Run("thread", func(t *testing.T) { check(t, "/thread?id=unknown", "Page not found", true) })
	t.Run("share", func(t *testing.T) { check(t, "/share?id=unknown", "Page not found", true) })
	t.Run("embed", func(t *testing.T) { check(t, "/embed/unknown", "Post not found", false) })
	t.Run("unknown gallery post", func(t *testing.T) { check(t, "/gallery?id=unknown&slide=1", "Page not found", true) })
	t.Run("post without gallery", func(t *testing.T) { check(t, "/gallery?id=journal&slide=1", "Page not found", true) })
}

func TestPostPageActionsStayInsideCard(t *testing.T) {
	w := httptest.NewRecorder()
	newHandler("client", nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/posts/maya", nil))
	body := w.Body.String()
	card := strings.Index(body, `<article class="post detail-post-card">`)
	actions := strings.Index(body, `detail-post-actions"`)
	thread := strings.Index(body, `id="thread-maya"`)
	end := -1
	if card >= 0 {
		end = strings.Index(body[card:], `</article>`)
	}
	if w.Code != http.StatusOK || card < 0 || actions < card || end < 0 || actions > card+end || thread < card+end || !strings.Contains(body, `aria-label="Positive feedback"`) || !strings.Contains(body, `>Embed</span>`) || !strings.Contains(body, `>Replies</span>`) {
		t.Errorf("post actions are not inside the post card, or the thread is not below it (status %d)", w.Code)
	}
}

func TestShareCopyFieldsUseConfiguredOrigin(t *testing.T) {
	t.Setenv("FORYOU_PUBLIC_ORIGIN", "https://example.test")
	w := httptest.NewRecorder()
	newHandler("client", nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/share/fragment?id=maya", nil))
	body := w.Body.String()
	if w.Code != http.StatusOK || strings.Count(body, `data-copy-target="`) != 2 || !strings.Contains(body, `value="https://example.test/posts/maya"`) || !strings.Contains(body, "https://example.test/embed/maya") || !strings.Contains(body, "This URL works only where this server is reachable.") {
		t.Errorf("share panel copy controls or configured origin missing (status %d)", w.Code)
	}
}

func TestGalleryViewerControlsAndCounter(t *testing.T) {
	w := httptest.NewRecorder()
	newHandler("client", nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/gallery?id=sofia&slide=2", nil))
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, "2 / 3") || !strings.Contains(body, "Image 2 of 3") || !strings.Contains(body, `alt="Illustrated mountain landscape, image 2 of 3"`) || !strings.Contains(body, `aria-label="Previous image"`) || !strings.Contains(body, `aria-label="Next image"`) {
		t.Errorf("gallery viewer counter or image text missing (status %d)", w.Code)
	}
}

func TestPostPageIgnoresInvalidThreadPage(t *testing.T) {
	w := httptest.NewRecorder()
	newHandler("client", nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/posts/maya?page=3", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `id="thread-maya"`) || strings.Contains(w.Body.String(), `id="thread-"`) {
		t.Errorf("invalid embedded thread page was not reset (status %d)", w.Code)
	}
}
