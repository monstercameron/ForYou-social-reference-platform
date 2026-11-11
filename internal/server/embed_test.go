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
	t.Run("unknown post", func(t *testing.T) { check(t, "/embed/unknown", "Not Found", http.StatusNotFound) })
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
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `action="/posts/maya"`) {
		t.Errorf("share fallback does not return to its post, status %d", w.Code)
	}
}
