package server

import (
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

func aprServe(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func aprCookieNamed(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != aprCookie {
		t.Fatalf("cookies = %#v; want one %s cookie", cookies, aprCookie)
	}
	return cookies[0]
}

func aprStyle(h http.Handler, cookie *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, "/appearance.css", nil)
	if cookie != nil {
		r.AddCookie(cookie)
	}
	return aprServe(h, r)
}

func TestAprSave(t *testing.T) {
	h := testClientHandler(t)
	teal := url.Values{"accent": {"teal"}, "size": {"large"}, "density": {"compact"}, "motion": {"reduce"}}
	t.Run("save writes the canonical cookie", func(t *testing.T) {
		w := postForm(h, "/appearance", teal)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/appearance?done=saved" {
			t.Fatalf("status %d to %q; want 303 to /appearance?done=saved", w.Code, w.Header().Get("Location"))
		}
		c := aprCookieNamed(t, w)
		if c.Value != "1.teal.large.compact.reduce" || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
			t.Errorf("cookie = %q httponly=%v samesite=%v; want 1.teal.large.compact.reduce, HttpOnly, SameSite=Lax", c.Value, c.HttpOnly, c.SameSite)
		}
	})
	t.Run("saving the defaults removes the cookie", func(t *testing.T) {
		defaults := url.Values{"accent": {"indigo"}, "size": {"standard"}, "density": {"comfortable"}, "motion": {"system"}}
		w := postForm(h, "/appearance", defaults)
		if c := aprCookieNamed(t, w); c.MaxAge >= 0 {
			t.Errorf("MaxAge = %d; want a negative value that deletes the cookie", c.MaxAge)
		}
	})
	t.Run("reset removes the cookie", func(t *testing.T) {
		w := aprServe(h, httptest.NewRequest(http.MethodPost, "/appearance/reset", nil))
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/appearance?done=reset" {
			t.Fatalf("status %d to %q; want 303 to /appearance?done=reset", w.Code, w.Header().Get("Location"))
		}
		if c := aprCookieNamed(t, w); c.MaxAge >= 0 {
			t.Errorf("MaxAge = %d; want a negative value that deletes the cookie", c.MaxAge)
		}
	})
	t.Run("cross-origin post is refused", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/appearance", strings.NewReader(teal.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://evil.example")
		w := aprServe(h, r)
		if w.Code != http.StatusForbidden || len(w.Result().Cookies()) != 0 {
			t.Errorf("status %d with cookies %d; want 403 and no cookie", w.Code, len(w.Result().Cookies()))
		}
	})
	t.Run("unknown accent is refused", func(t *testing.T) {
		w := postForm(h, "/appearance", url.Values{"accent": {"hotpink"}, "size": {"standard"}, "density": {"comfortable"}, "motion": {"system"}})
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "That choice is not offered. Pick one of the options below.") || w.Header().Get("Set-Cookie") != "" {
			t.Errorf("status %d; want 400 with the refusal message and no Set-Cookie", w.Code)
		}
	})
	t.Run("no-script preview saves nothing", func(t *testing.T) {
		preview := url.Values{"accent": {"teal"}, "size": {"large"}, "density": {"compact"}, "motion": {"reduce"}, "preview": {"1"}}
		w := postForm(h, "/appearance", preview)
		if w.Code != http.StatusOK || w.Header().Get("Set-Cookie") != "" {
			t.Fatalf("status %d with Set-Cookie %q; want 200 and no cookie", w.Code, w.Header().Get("Set-Cookie"))
		}
		if !strings.Contains(w.Body.String(), "Preview only. Nothing is saved until you choose Save appearance.") || !strings.Contains(w.Body.String(), `data-apr-accent="teal"`) {
			t.Error("the no-script preview should say nothing is saved and show the posted accent")
		}
		if !strings.Contains(w.Body.String(), `tabindex="-1" autofocus`) {
			t.Error("the no-script preview notice should take focus")
		}
	})
}

func TestAprStylesheet(t *testing.T) {
	h := testClientHandler(t)
	header := "/* ForYou appearance for this browser */\n"
	t.Run("no cookie gives only the header line", func(t *testing.T) {
		w := aprStyle(h, nil)
		if w.Body.String() != header {
			t.Errorf("body = %q; want only the header line", w.Body.String())
		}
		if w.Header().Get("ETag") != `"apr-default"` || w.Header().Get("Cache-Control") != "private, no-cache" || w.Header().Get("Vary") != "Cookie" || w.Header().Get("Content-Type") != "text/css; charset=utf-8" {
			t.Errorf("headers ETag=%q Cache-Control=%q Vary=%q Content-Type=%q", w.Header().Get("ETag"), w.Header().Get("Cache-Control"), w.Header().Get("Vary"), w.Header().Get("Content-Type"))
		}
	})
	t.Run("teal large compact reduce", func(t *testing.T) {
		want := header + strings.Join([]string{
			"body{--accent:#0f766e;--accent-soft:#e2f3f1}",
			"body[data-theme=dark]{--accent:#3cc9b6;--accent-soft:#143331}",
			"@media(prefers-color-scheme:dark){body:not([data-theme=light]){--accent:#3cc9b6;--accent-soft:#143331}}",
			"main.center>:not(.mobile-nav){zoom:1.125}",
			".posts{gap:8px}.posts>.post{padding:12px}.component-card{padding:16px}.settings-card+.settings-card{margin-top:12px}.posts>.post .post-text{line-height:1.5}",
			"*,*::before,*::after{animation:none!important;transition:none!important}html{scroll-behavior:auto!important}",
		}, "\n") + "\n"
		w := aprStyle(h, &http.Cookie{Name: aprCookie, Value: "1.teal.large.compact.reduce"})
		if w.Body.String() != want {
			t.Errorf("body = %q; want %q", w.Body.String(), want)
		}
	})
	t.Run("larger plum only", func(t *testing.T) {
		body := aprStyle(h, &http.Cookie{Name: aprCookie, Value: "1.plum.larger.comfortable.system"}).Body.String()
		if !strings.Contains(body, "body{--accent:#9333ea;--accent-soft:#f4eafd}") || !strings.Contains(body, "zoom:1.25") {
			t.Error("plum and larger text should set the plum tokens and 125% zoom")
		}
		if strings.Contains(body, ".posts{gap:8px}") || strings.Contains(body, "animation:none") {
			t.Error("comfortable density and system motion should add no density or motion rules")
		}
	})
	t.Run("damaged cookie falls back to defaults", func(t *testing.T) {
		if body := aprStyle(h, &http.Cookie{Name: aprCookie, Value: "1.hotpink.large.compact.reduce"}).Body.String(); body != header {
			t.Errorf("body = %q; want only the header line", body)
		}
	})
	t.Run("unchanged stylesheet is not resent", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/appearance.css", nil)
		r.AddCookie(&http.Cookie{Name: aprCookie, Value: "1.teal.large.compact.reduce"})
		r.Header.Set("If-None-Match", `"apr-1.teal.large.compact.reduce"`)
		w := aprServe(h, r)
		if w.Code != http.StatusNotModified || w.Body.Len() != 0 {
			t.Errorf("status %d with %d bytes; want 304 and no body", w.Code, w.Body.Len())
		}
	})
}

func TestAprPage(t *testing.T) {
	h := testClientHandler(t)
	t.Run("page shows the saved choices", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/appearance", nil)
		r.AddCookie(&http.Cookie{Name: aprCookie, Value: "1.teal.large.compact.reduce"})
		w := aprServe(h, r)
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "<h1>Appearance</h1>") {
			t.Fatalf("status %d; want the Appearance page", w.Code)
		}
		if !strings.Contains(body, `name="accent" value="teal" checked>`) {
			t.Error(`missing checked choice name="accent" value="teal"`)
		}
		if !strings.Contains(body, `name="size" value="large" checked>`) {
			t.Error(`missing checked choice name="size" value="large"`)
		}
		if !strings.Contains(body, `name="density" value="compact" checked>`) {
			t.Error(`missing checked choice name="density" value="compact"`)
		}
		if !strings.Contains(body, `name="motion" value="reduce" checked>`) {
			t.Error(`missing checked choice name="motion" value="reduce"`)
		}
		if !strings.Contains(body, `aria-current="page"`) {
			t.Error("the Settings section should be marked as the current page")
		}
	})
	t.Run("damaged cookie is explained", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/appearance", nil)
		r.AddCookie(&http.Cookie{Name: aprCookie, Value: "1.hotpink.large.compact.reduce"})
		if !strings.Contains(aprServe(h, r).Body.String(), "The appearance saved in this browser could not be read, so the defaults are shown. Saving replaces it.") {
			t.Error("a damaged cookie should be explained on the page")
		}
	})
	t.Run("saved status", func(t *testing.T) {
		if !strings.Contains(aprServe(h, httptest.NewRequest(http.MethodGet, "/appearance?done=saved", nil)).Body.String(), "Saved. Every page in this browser now uses these choices.") {
			t.Error("the saved message is missing")
		}
	})
	t.Run("preview fragment shows unsaved choices", func(t *testing.T) {
		w := postForm(h, "/appearance/preview", url.Values{"accent": {"plum"}, "size": {"larger"}, "density": {"compact"}, "motion": {"system"}})
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, `data-apr-accent="plum"`) || !strings.Contains(body, "Plum accent · Larger text · Compact · Motion follows device") {
			t.Errorf("status %d; want the plum preview fragment", w.Code)
		}
		if strings.Contains(body, "<h1>") || w.Header().Get("Set-Cookie") != "" {
			t.Error("the preview should be a fragment and never set a cookie")
		}
	})
	t.Run("every page links the reader stylesheet", func(t *testing.T) {
		if !strings.Contains(getWith(h, "/"), `<link rel="stylesheet" href="/appearance.css">`) {
			t.Error("the shell should link the reader stylesheet")
		}
	})
	t.Run("settings links here", func(t *testing.T) {
		if !strings.Contains(getWith(h, "/settings"), `href="/appearance"`) {
			t.Error("Settings should link to the Appearance page")
		}
	})
}

func aprLuminance(hex string) float64 {
	channel := func(part string) float64 {
		v, _ := strconv.ParseInt(part, 16, 64)
		c := float64(v) / 255
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*channel(hex[1:3]) + 0.7152*channel(hex[3:5]) + 0.0722*channel(hex[5:7])
}

func aprContrast(foreground, background string) float64 {
	light, dark := aprLuminance(foreground), aprLuminance(background)
	if light < dark {
		light, dark = dark, light
	}
	return (light + 0.05) / (dark + 0.05)
}

func aprAtLeastAA(t *testing.T, pair, foreground, background string) {
	t.Helper()
	if ratio := aprContrast(foreground, background); ratio < 4.5 {
		t.Errorf("%s: contrast %.2f for %s on %s; want at least 4.5", pair, ratio, foreground, background)
	}
}

func TestAprAccentContrast(t *testing.T) {
	t.Run("indigo", func(t *testing.T) {
		aprAtLeastAA(t, "light accent on panel", "#4746df", "#ffffff")
		aprAtLeastAA(t, "light accent on soft accent", "#4746df", "#eeedff")
		aprAtLeastAA(t, "dark accent on panel", "#8589ff", "#191e2c")
		aprAtLeastAA(t, "dark accent on soft accent", "#8589ff", "#272b4b")
		aprAtLeastAA(t, "dark accent on on-accent", "#8589ff", "#111521")
	})
	t.Run("blue", func(t *testing.T) {
		aprAtLeastAA(t, "light accent on panel", "#1d4ed8", "#ffffff")
		aprAtLeastAA(t, "light accent on soft accent", "#1d4ed8", "#e6edfd")
		aprAtLeastAA(t, "dark accent on panel", "#7aa7ff", "#191e2c")
		aprAtLeastAA(t, "dark accent on soft accent", "#7aa7ff", "#1c2a48")
		aprAtLeastAA(t, "dark accent on on-accent", "#7aa7ff", "#111521")
	})
	t.Run("teal", func(t *testing.T) {
		aprAtLeastAA(t, "light accent on panel", "#0f766e", "#ffffff")
		aprAtLeastAA(t, "light accent on soft accent", "#0f766e", "#e2f3f1")
		aprAtLeastAA(t, "dark accent on panel", "#3cc9b6", "#191e2c")
		aprAtLeastAA(t, "dark accent on soft accent", "#3cc9b6", "#143331")
		aprAtLeastAA(t, "dark accent on on-accent", "#3cc9b6", "#111521")
	})
	t.Run("plum", func(t *testing.T) {
		aprAtLeastAA(t, "light accent on panel", "#9333ea", "#ffffff")
		aprAtLeastAA(t, "light accent on soft accent", "#9333ea", "#f4eafd")
		aprAtLeastAA(t, "dark accent on panel", "#c99bff", "#191e2c")
		aprAtLeastAA(t, "dark accent on soft accent", "#c99bff", "#33224a")
		aprAtLeastAA(t, "dark accent on on-accent", "#c99bff", "#111521")
	})
}
