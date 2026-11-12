package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const setTestProfile = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func setProfileWithOrigins(t *testing.T, client *Client, origins ...string) *http.Cookie {
	t.Helper()
	if err := client.app.updateSettings(setTestProfile, func(settings *ProfileSettings) error {
		settings.MediaOrigins = origins
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: profileCookie, Value: setTestProfile}
}

func TestSettingsAppearance(t *testing.T) {
	t.Run("default follows system", func(t *testing.T) {
		w := httptest.NewRecorder()
		testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/settings", nil))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `aria-label="Main navigation"`) || !strings.Contains(w.Body.String(), `href="/settings" aria-current="page"`) || !strings.Contains(w.Body.String(), `href="/settings?theme=system" aria-current="true"`) {
			t.Errorf("default settings status %d; shell navigation or system selection missing", w.Code)
		}
	})
	t.Run("dark selection persists", func(t *testing.T) {
		w := httptest.NewRecorder()
		testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/settings?theme=dark", nil))
		cookies := w.Result().Cookies()
		if w.Code != http.StatusOK || len(cookies) != 1 || cookies[0].Name != themeCookie || cookies[0].Value != "dark" || !strings.Contains(w.Body.String(), `data-theme="dark"`) {
			t.Errorf("dark selection status %d; cookie or theme missing", w.Code)
		}
	})
	t.Run("system clears saved selection", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/settings?theme=system", nil)
		r.AddCookie(&http.Cookie{Name: themeCookie, Value: "dark"})
		w := httptest.NewRecorder()
		testClientHandler(t).ServeHTTP(w, r)
		cookies := w.Result().Cookies()
		if w.Code != http.StatusOK || len(cookies) != 1 || cookies[0].Name != themeCookie || cookies[0].MaxAge >= 0 || !strings.Contains(w.Body.String(), `data-theme=""`) {
			t.Errorf("system selection status %d; cookie not cleared or system theme missing", w.Code)
		}
	})
	t.Run("invalid selection rejected", func(t *testing.T) {
		w := httptest.NewRecorder()
		testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/settings?theme=unknown", nil))
		if w.Code != http.StatusBadRequest || w.Header().Get("Set-Cookie") != "" {
			t.Errorf("invalid selection status %d; want 400 without cookie", w.Code)
		}
	})
}

func TestSettingsMedia(t *testing.T) {
	const origin = "https://media.example:443"
	t.Run("allowed origin renders a stop form", func(t *testing.T) {
		client := newTestClient(t, false)
		cookie := setProfileWithOrigins(t, client, origin)
		body := getWith(client.Handler(), "/settings", cookie)
		if !strings.Contains(body, `<span class="settings-media-host">https://media.example:443</span>`) || !strings.Contains(body, `action="/settings/media-origins/remove"`) {
			t.Errorf("allowed origin not listed with its stop form")
		}
	})
	t.Run("empty list explains that media waits for a click", func(t *testing.T) {
		client := newTestClient(t, false)
		body := getWith(client.Handler(), "/settings", setProfileWithOrigins(t, client))
		if !strings.Contains(body, "No origins allowed. Media loads only when you click it.") {
			t.Errorf("empty media list has no explanation")
		}
	})
	t.Run("stopping from the fragment empties the list", func(t *testing.T) {
		client := newTestClient(t, false)
		cookie := setProfileWithOrigins(t, client, origin)
		w := postForm(client.Handler(), "/settings/media-origins/remove/fragment", url.Values{"origin": {origin}}, cookie)
		body := getWith(client.Handler(), "/settings", cookie)
		if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "/settings/media-origins/remove") || !strings.Contains(w.Body.String(), "No origins allowed.") || !strings.Contains(body, "No origins allowed.") {
			t.Errorf("fragment stop status %d; list not emptied", w.Code)
		}
	})
	t.Run("stopping from a form without scripts redirects to the section", func(t *testing.T) {
		client := newTestClient(t, false)
		cookie := setProfileWithOrigins(t, client, origin)
		w := postForm(client.Handler(), "/settings/media-origins/remove", url.Values{"origin": {origin}}, cookie)
		body := getWith(client.Handler(), "/settings", cookie)
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/settings#media" || strings.Contains(body, `<span class="settings-media-host">https://media.example:443</span>`) || !strings.Contains(body, "No origins allowed. Media loads only when you click it.") {
			t.Errorf("plain stop status %d location %q; want 303 to /settings#media with the list emptied", w.Code, w.Header().Get("Location"))
		}
	})
	t.Run("stop all clears every origin", func(t *testing.T) {
		client := newTestClient(t, false)
		cookie := setProfileWithOrigins(t, client, origin, "https://other.example:443")
		w := postForm(client.Handler(), "/settings/media-origins/clear/fragment", url.Values{}, cookie)
		body := getWith(client.Handler(), "/settings", cookie)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Stopped loading media from every origin.") || !strings.Contains(body, "No origins allowed.") {
			t.Errorf("stop all status %d; origins not cleared", w.Code)
		}
	})
	t.Run("cross-origin stop is refused", func(t *testing.T) {
		client := newTestClient(t, false)
		cookie := setProfileWithOrigins(t, client, origin)
		r := httptest.NewRequest(http.MethodPost, "/settings/media-origins/remove", strings.NewReader(url.Values{"origin": {origin}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://attacker.example")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		client.Handler().ServeHTTP(w, r)
		body := getWith(client.Handler(), "/settings", cookie)
		if w.Code != http.StatusForbidden || !strings.Contains(body, `<span class="settings-media-host">https://media.example:443</span>`) {
			t.Errorf("cross-origin stop status %d; want 403 with the origin kept", w.Code)
		}
	})
}

func TestSettingsProfile(t *testing.T) {
	t.Run("names the profile and says it is not an account", func(t *testing.T) {
		client := newTestClient(t, true)
		body := getWith(client.Handler(), "/settings", setProfileWithOrigins(t, client))
		if !strings.Contains(body, `This browser is profile <span class="id-chip">AAAAAAAA…</span>, identified by a cookie on this server. It is not an account.`) || !strings.Contains(body, "Dev custody: keys and payments are not real.") {
			t.Errorf("profile section missing the profile prefix or dev custody sentence")
		}
	})
	t.Run("browser without a profile says so", func(t *testing.T) {
		body := getWith(testClientHandler(t), "/settings")
		if !strings.Contains(body, "This browser has no profile yet.") {
			t.Errorf("profile section does not say the browser has no profile")
		}
	})
	t.Run("content filters and connection stay on the page", func(t *testing.T) {
		body := getWith(testClientHandler(t), "/settings")
		if !strings.Contains(body, `id="content-filters"`) || !strings.Contains(body, `id="connection"`) {
			t.Errorf("content filters or connection card missing")
		}
	})
}
