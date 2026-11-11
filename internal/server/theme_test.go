package server

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAppearancePreference(t *testing.T) {
	h := newHandler("client", nil)
	r := httptest.NewRequest(http.MethodGet, "/?theme=light", nil)
	r.TLS = &tls.ConnectionState{}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	cookies := w.Result().Cookies()
	if w.Code != http.StatusOK || len(cookies) != 1 || cookies[0].Name != themeCookie || cookies[0].Value != "light" || !cookies[0].HttpOnly || !cookies[0].Secure || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatalf("invalid appearance cookie: %#v", cookies)
	}
	check := func(t *testing.T, path, want string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(cookies[0])
		out := httptest.NewRecorder()
		h.ServeHTTP(out, req)
		if out.Code != http.StatusOK || !strings.Contains(out.Body.String(), want) || !strings.Contains(out.Header().Get("Cache-Control"), "no-store") {
			t.Errorf("%s: got status %d, cache %q; missing %q", path, out.Code, out.Header().Get("Cache-Control"), want)
		}
	}
	t.Run("feed remembers preference", func(t *testing.T) { check(t, "/", `data-theme="light"`) })
	t.Run("composer remembers preference", func(t *testing.T) { check(t, "/compose", `data-theme="light"`) })
	t.Run("post remembers preference", func(t *testing.T) { check(t, "/posts/maya", `data-theme="light"`) })
	t.Run("thread remembers preference", func(t *testing.T) { check(t, "/thread?id=maya", `data-theme="light"`) })
	t.Run("explicit choice wins", func(t *testing.T) { check(t, "/?theme=dark", `data-theme="dark"`) })
}
