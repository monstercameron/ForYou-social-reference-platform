package server

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAppearancePreference(t *testing.T) {
	h := testClientHandler(t)
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

func TestThemeSwitch(t *testing.T) {
	get := func(t *testing.T, target string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodGet, target, nil)
		for _, c := range cookies {
			r.AddCookie(c)
		}
		w := httptest.NewRecorder()
		testClientHandler(t).ServeHTTP(w, r)
		return w
	}
	t.Run("dark is saved and the reader returns to the page", func(t *testing.T) {
		w := get(t, "/theme?choice=dark&back=%2Fwallet%2Fhistory%3Fnetwork%3Dbitcoin-mainnet")
		cookies := w.Result().Cookies()
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/wallet/history?network=bitcoin-mainnet" || len(cookies) != 1 || cookies[0].Name != themeCookie || cookies[0].Value != "dark" {
			t.Errorf("status %d, location %q, cookies %v; want 303 to the wallet history with the dark theme cookie", w.Code, w.Header().Get("Location"), cookies)
		}
	})
	t.Run("system forgets the saved choice", func(t *testing.T) {
		w := get(t, "/theme?choice=system&back=%2Fledger", &http.Cookie{Name: themeCookie, Value: "dark"})
		cookies := w.Result().Cookies()
		if w.Header().Get("Location") != "/ledger" || len(cookies) != 1 || cookies[0].Name != themeCookie || cookies[0].MaxAge >= 0 {
			t.Errorf("location %q, cookies %v; want /ledger and the theme cookie expired", w.Header().Get("Location"), cookies)
		}
	})
	t.Run("an address on another site goes Home", func(t *testing.T) {
		if got := get(t, "/theme?choice=dark&back=%2F%2Fevil.example%2F").Header().Get("Location"); got != "/" {
			t.Errorf("location %q; want /", got)
		}
	})
	t.Run("an absolute address goes Home", func(t *testing.T) {
		if got := get(t, "/theme?choice=dark&back=https%3A%2F%2Fevil.example%2F").Header().Get("Location"); got != "/" {
			t.Errorf("location %q; want /", got)
		}
	})
	t.Run("a one-time theme parameter is dropped from the return address", func(t *testing.T) {
		if got := get(t, "/theme?choice=light&back=%2F%3Ftheme%3Ddark%26filter%3Dtext").Header().Get("Location"); got != "/?filter=text" {
			t.Errorf("location %q; want /?filter=text", got)
		}
	})
	t.Run("wallet pages leave the feed panels out of the right bar", func(t *testing.T) {
		body := get(t, "/wallet").Body.String()
		if strings.Contains(body, "About this feed") || strings.Contains(body, "Ads for you") || !strings.Contains(body, "Your network") {
			t.Error("the wallet right bar should show the network panel without the feed and ads panels")
		}
	})
}
