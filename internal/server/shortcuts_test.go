package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func scRequest(h http.Handler, form url.Values, origin string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, "/shortcuts", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", origin)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestShortcutsPage(t *testing.T) {
	h := testClientHandler(t)
	t.Run("on by default", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/shortcuts", nil))
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, `<h1>Keyboard shortcuts</h1>`) || !strings.Contains(body, `pill pill-ok`) || !strings.Contains(body, `Turn off shortcuts`) || !strings.Contains(body, `<kbd class="sc-kbd">j</kbd>`) || !strings.Contains(body, `href="/settings" aria-current="page"`) {
			t.Errorf("shortcuts page status %d; want the on state, the key list and Settings as the current section", w.Code)
		}
	})
	t.Run("off cookie shows off", func(t *testing.T) {
		body := getWith(h, "/shortcuts", &http.Cookie{Name: scCookie, Value: "off"})
		if !strings.Contains(body, `Turn on shortcuts`) || !strings.Contains(body, `pill pill-muted`) {
			t.Error("with the off cookie the page should offer to turn shortcuts on and show Off")
		}
	})
	t.Run("damaged cookie reads as on", func(t *testing.T) {
		body := getWith(h, "/shortcuts", &http.Cookie{Name: scCookie, Value: "OFF%00x"})
		if !strings.Contains(body, `Turn off shortcuts`) {
			t.Error("a damaged shortcuts cookie should read as on")
		}
	})
}

func TestShortcutsToggle(t *testing.T) {
	h := testClientHandler(t)
	t.Run("turning off sets the cookie and redirects", func(t *testing.T) {
		w := postForm(h, "/shortcuts", url.Values{"state": {"off"}})
		cookies := w.Result().Cookies()
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/shortcuts#shortcuts-state" || len(cookies) != 1 || cookies[0].Name != scCookie || cookies[0].Value != "off" || cookies[0].MaxAge != 31536000 || cookies[0].HttpOnly {
			t.Errorf("turning off returned %d to %q with cookies %v; want 303 to the shortcuts state with a readable off cookie for a year", w.Code, w.Header().Get("Location"), cookies)
		}
	})
	t.Run("turning on clears the cookie", func(t *testing.T) {
		w := postForm(h, "/shortcuts", url.Values{"state": {"on"}}, &http.Cookie{Name: scCookie, Value: "off"})
		cookies := w.Result().Cookies()
		if len(cookies) != 1 || cookies[0].Name != scCookie || cookies[0].MaxAge >= 0 {
			t.Errorf("turning on left cookies %v; want the shortcuts cookie expired", cookies)
		}
	})
	t.Run("from settings returns to settings", func(t *testing.T) {
		w := postForm(h, "/shortcuts", url.Values{"state": {"off"}, "from": {"settings"}})
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/settings#sc-settings" {
			t.Errorf("from settings returned %d to %q; want 303 to /settings#sc-settings", w.Code, w.Header().Get("Location"))
		}
	})
	t.Run("fragment returns the state card", func(t *testing.T) {
		w := postForm(h, "/shortcuts/fragment", url.Values{"state": {"off"}})
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, `Shortcuts are off on this browser.`) || !strings.Contains(body, `id="shortcuts-state"`) || strings.Contains(body, `<html`) {
			t.Errorf("fragment status %d; want the state card alone with the off sentence", w.Code)
		}
	})
	t.Run("unknown state is refused", func(t *testing.T) {
		w := postForm(h, "/shortcuts", url.Values{"state": {"maybe"}})
		if w.Code != http.StatusBadRequest || w.Header().Get("Set-Cookie") != "" {
			t.Errorf("unknown state status %d; want 400 without a cookie", w.Code)
		}
	})
	t.Run("cross-origin post is refused", func(t *testing.T) {
		w := scRequest(h, url.Values{"state": {"off"}}, "https://evil.test")
		if w.Code != http.StatusForbidden || w.Header().Get("Set-Cookie") != "" {
			t.Errorf("cross-origin status %d; want 403 without a cookie", w.Code)
		}
	})
}

func TestShortcutsDialog(t *testing.T) {
	t.Run("dialog fragment", func(t *testing.T) {
		w := httptest.NewRecorder()
		testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/shortcuts/dialog", nil))
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, `id="sc-help-title"`) || !strings.Contains(body, `data-sc-close`) || strings.Contains(body, `<html`) {
			t.Errorf("dialog fragment status %d; want the help heading and close button without a page shell", w.Code)
		}
	})
}

func TestShortcutsWiring(t *testing.T) {
	h := testClientHandler(t)
	t.Run("script is served", func(t *testing.T) {
		if body := getWith(h, "/static/shortcuts.js"); !strings.Contains(body, `foryou_shortcuts=off`) {
			t.Error("the shortcuts script should be served and read the off cookie")
		}
	})
	t.Run("app imports the script", func(t *testing.T) {
		if body := getWith(h, "/static/app.js"); !strings.Contains(body, `import './shortcuts.js';`) {
			t.Error("app.js should import the shortcuts script")
		}
	})
	t.Run("settings links to shortcuts", func(t *testing.T) {
		if body := getWith(h, "/settings"); !strings.Contains(body, `id="sc-settings"`) || !strings.Contains(body, `href="/shortcuts"`) {
			t.Error("settings should carry the keyboard shortcuts card linking to /shortcuts")
		}
	})
}
