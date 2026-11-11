package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExplore(t *testing.T) {
	check := func(t *testing.T, path, present, absent string, status int) {
		t.Helper()
		w := httptest.NewRecorder()
		exploreHandler(w, httptest.NewRequest(http.MethodGet, path, nil))
		body := w.Body.String()
		if w.Code != status || !strings.Contains(body, present) || (absent != "" && strings.Contains(body, absent)) {
			t.Errorf("%s: status %d; expected %d, present %q, absent %q", path, w.Code, status, present, absent)
		}
	}
	t.Run("initial", func(t *testing.T) { check(t, "/explore", "Explore", "No posts found", http.StatusOK) })
	t.Run("filter", func(t *testing.T) {
		check(t, "/explore?filter=images", "Illustrated mountain slideshow", "demo-tech.mp4", http.StatusOK)
	})
	t.Run("search", func(t *testing.T) {
		check(t, "/explore?q=tomatoes", "The tomatoes finally ripened", "community garden", http.StatusOK)
	})
	t.Run("invalid filter", func(t *testing.T) {
		check(t, "/explore?filter=bad", "invalid explore", "community garden", http.StatusBadRequest)
	})
}

func TestExploreMore(t *testing.T) {
	w := httptest.NewRecorder()
	exploreMoreHandler(w, httptest.NewRequest(http.MethodGet, "/explore/more?limit=10&anchor=maya", nil))
	body := w.Body.String()
	if w.Code != http.StatusOK || strings.Count(body, `<article class="post">`) != 5 || !strings.Contains(body, `get="/explore/more"`) || !strings.Contains(body, `name="anchor" value="maya"`) || !strings.Contains(body, "5 more posts loaded, 10 total.") {
		t.Errorf("unexpected Explore continuation, status %d", w.Code)
	}
}

func TestExploreNavigationKeepsContext(t *testing.T) {
	w := httptest.NewRecorder()
	newHandler("client", nil).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/explore?filter=video&q=server", nil))
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, `href="/explore" aria-current="page"`) || !strings.Contains(body, `href="/explore?theme=light&amp;filter=video&amp;q=server"`) || !strings.Contains(body, `action="/explore"`) {
		t.Errorf("Explore navigation or theme context missing, status %d", w.Code)
	}
}

func TestExploreIncludesNewPosts(t *testing.T) {
	store := &demoStore{}
	if _, err := store.add("newly created explore record", "explore-token"); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodGet, "/explore?q=newly+created", nil)
	r = r.WithContext(context.WithValue(r.Context(), demoStoreKey{}, store))
	w := httptest.NewRecorder()
	exploreHandler(w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "newly created explore record") {
		t.Errorf("new post missing from Explore: status %d", w.Code)
	}
}
