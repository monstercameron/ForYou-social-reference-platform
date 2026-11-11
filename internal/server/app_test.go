package server

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestClient(t *testing.T, dev bool) *Client {
	t.Helper()
	client, err := NewClient(Config{DataDir: t.TempDir(), Dev: dev})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func testClientHandler(t *testing.T) http.Handler {
	t.Helper()
	client, err := NewClient(Config{DataDir: t.TempDir(), PublicOrigin: "https://example.test"})
	if err != nil {
		t.Fatal(err)
	}
	return client.Handler()
}

func TestClientRouteGroups(t *testing.T) {
	previous := routeGroups
	routeGroups = append(routeGroups, func(a *App, mux *http.ServeMux) {
		mux.HandleFunc("GET /test-route-group", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("registered")) })
	})
	t.Cleanup(func() { routeGroups = previous })
	client := newTestClient(t, false)
	w := httptest.NewRecorder()
	client.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/test-route-group", nil))
	if w.Code != http.StatusOK || w.Body.String() != "registered" {
		t.Fatalf("group route response = %d %q; want 200 registered", w.Code, w.Body.String())
	}
}

func TestClientNavigationAndLandingPages(t *testing.T) {
	t.Run("all planned destinations are linked", func(t *testing.T) {
		client := newTestClient(t, false)
		w := httptest.NewRecorder()
		client.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
		if !strings.Contains(w.Body.String(), `href="/"`) || !strings.Contains(w.Body.String(), `href="/explore"`) || !strings.Contains(w.Body.String(), `href="/wallet"`) || !strings.Contains(w.Body.String(), `href="/identity"`) || !strings.Contains(w.Body.String(), `href="/network"`) || !strings.Contains(w.Body.String(), `href="/settings"`) || !strings.Contains(w.Body.String(), `href="/compose"`) {
			t.Error("home is missing a planned navigation destination")
		}
	})
	t.Run("dev custody notice follows configuration", func(t *testing.T) {
		client := newTestClient(t, true)
		w := httptest.NewRecorder()
		client.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
		if !strings.Contains(w.Body.String(), "Dev custody: keys and payments are not real") {
			t.Error("dev custody text missing")
		}
	})
	t.Run("non-dev client omits custody notice", func(t *testing.T) {
		client := newTestClient(t, false)
		w := httptest.NewRecorder()
		client.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/", nil))
		if strings.Contains(w.Body.String(), "Dev custody: keys and payments are not real") {
			t.Error("non-dev client showed dev custody text")
		}
	})
	t.Run("identity landing renders active navigation", func(t *testing.T) {
		checkLanding(t, "/identity", "Identity and keys")
	})
	t.Run("network landing renders active navigation", func(t *testing.T) {
		checkLanding(t, "/network", "Network")
	})
	t.Run("wallet landing renders active navigation", func(t *testing.T) {
		checkLanding(t, "/wallet", "Wallet")
	})
}

func TestStateChangingRouteGuards(t *testing.T) {
	t.Run("cross origin feedback is refused", func(t *testing.T) {
		client := newTestClient(t, false)
		r := httptest.NewRequest(http.MethodPost, "/feedback", strings.NewReader("id=maya&vote=up"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://attacker.example")
		w := httptest.NewRecorder()
		client.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("cross-origin feedback status = %d; want 403", w.Code)
		}
	})
	t.Run("multipart file parts are refused", func(t *testing.T) {
		client := newTestClient(t, false)
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("attachment", "draft.txt")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write([]byte("not accepted")); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		r := httptest.NewRequest(http.MethodPost, "/feedback", &body)
		r.Header.Set("Content-Type", writer.FormDataContentType())
		w := httptest.NewRecorder()
		client.Handler().ServeHTTP(w, r)
		if w.Code != http.StatusBadRequest {
			t.Errorf("multipart file response = %d; want 400", w.Code)
		}
	})
}

func TestFailRendersForFetchAndNavigation(t *testing.T) {
	client := newTestClient(t, false)
	t.Run("HTMLeX fetch fragment", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.Header.Set("Sec-Fetch-Dest", "empty")
		w := httptest.NewRecorder()
		client.app.fail(w, r, http.StatusBadRequest, "invalid request")
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "<fragment>") || strings.Contains(w.Body.String(), "<!doctype html>") {
			t.Errorf("fetch failure = %d %q; want an error fragment", w.Code, w.Body.String())
		}
	})
	t.Run("document page", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()
		client.app.fail(w, r, http.StatusBadRequest, "invalid request")
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "<!doctype html>") || !strings.Contains(w.Body.String(), `class="layout`) || !strings.Contains(w.Body.String(), "invalid request") {
			t.Errorf("document failure = %d %q; want a shell error page", w.Code, w.Body.String())
		}
	})
}

func checkLanding(t *testing.T, path, title string) {
	t.Helper()
	client := newTestClient(t, false)
	w := httptest.NewRecorder()
	client.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "<h1>"+title+"</h1>") || !strings.Contains(w.Body.String(), "This page is not available yet.") || !strings.Contains(w.Body.String(), `href="`+path+`" aria-current="page"`) {
		t.Errorf("%s landing = %d %q", path, w.Code, w.Body.String())
	}
}
