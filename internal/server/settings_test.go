package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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
