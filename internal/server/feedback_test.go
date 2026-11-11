package server

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestFeedbackSelection(t *testing.T) {
	submit := func(vote string, cookie *http.Cookie) *httptest.ResponseRecorder {
		t.Helper()
		form := url.Values{"id": {"maya"}, "vote": {vote}}
		r := httptest.NewRequest(http.MethodPost, "/feedback/fragment", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		feedbackHandler(w, r)
		return w
	}
	up := submit("up", nil)
	if up.Code != http.StatusOK || !strings.Contains(up.Body.String(), `aria-label="Like" aria-pressed="true"`) || strings.Count(up.Body.String(), `aria-pressed="true"`) != 1 || strings.Count(up.Body.String(), `aria-pressed="false"`) != 1 || strings.Count(up.Body.String(), `onerror="this(after)"`) != 2 {
		t.Fatalf("positive feedback: status %d", up.Code)
	}
	cookies := up.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatal("missing browser-local feedback cookie protections")
	}
	other := httptest.NewRequest(http.MethodGet, "/", nil)
	if len(feedbackSelections(other)) != 0 {
		t.Fatal("feedback leaked to another browser")
	}
	down := submit("down", cookies[0])
	if down.Code != http.StatusOK || !strings.Contains(down.Body.String(), `aria-label="Dislike" aria-pressed="true"`) || strings.Count(down.Body.String(), `aria-pressed="true"`) != 1 || strings.Count(down.Body.String(), `aria-pressed="false"`) != 1 {
		t.Fatalf("negative feedback: status %d", down.Code)
	}
	cleared := submit("clear", down.Result().Cookies()[0])
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.AddCookie(cleared.Result().Cookies()[0])
	if cleared.Code != http.StatusOK || len(feedbackSelections(r)) != 0 {
		t.Errorf("cleared feedback status %d; selections %v", cleared.Code, feedbackSelections(r))
	}
}

const legacyFeedbackFixture = `[{"i":"a0","v":"up"},{"i":"a1","v":"up"},{"i":"a2","v":"up"},{"i":"a3","v":"up"},{"i":"a4","v":"up"},{"i":"a5","v":"up"},{"i":"a6","v":"up"},{"i":"a7","v":"up"},{"i":"a8","v":"up"},{"i":"a9","v":"up"},{"i":"a10","v":"up"},{"i":"a11","v":"up"},{"i":"a12","v":"up"},{"i":"a13","v":"up"},{"i":"a14","v":"up"},{"i":"a15","v":"up"},{"i":"a16","v":"up"},{"i":"a17","v":"up"},{"i":"a18","v":"up"},{"i":"a19","v":"up"},{"i":"a20","v":"up"},{"i":"a21","v":"up"},{"i":"a22","v":"up"},{"i":"a23","v":"up"}]`

func TestFeedbackEvictsOldest(t *testing.T) {
	form := url.Values{"id": {"maya"}, "vote": {"down"}}
	r := httptest.NewRequest(http.MethodPost, "/feedback/fragment", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: feedbackCookie, Value: base64.RawURLEncoding.EncodeToString([]byte(legacyFeedbackFixture))})
	w := httptest.NewRecorder()
	feedbackHandler(w, r)
	if w.Code != http.StatusOK || len(w.Result().Cookies()) != 2 {
		t.Fatalf("feedback status %d, cookies %d", w.Code, len(w.Result().Cookies()))
	}
	check := httptest.NewRequest(http.MethodGet, "/", nil)
	check.AddCookie(&http.Cookie{Name: feedbackCookie, Value: base64.RawURLEncoding.EncodeToString([]byte(legacyFeedbackFixture))})
	check.AddCookie(w.Result().Cookies()[0])
	check.AddCookie(w.Result().Cookies()[1])
	selections := feedbackSelections(check)
	if len(selections) != 24 || selections["a0"] != "" || selections["a1"] != "up" || selections["maya"] != "down" {
		t.Errorf("incorrect feedback after eviction: %v", selections)
	}
}

func TestFeedbackRejectsInvalidRequests(t *testing.T) {
	check := func(t *testing.T, id, vote, origin string, want int) {
		t.Helper()
		form := url.Values{"id": {id}, "vote": {vote}}
		r := httptest.NewRequest(http.MethodPost, "/feedback/fragment", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if origin != "" {
			r.Header.Set("Origin", origin)
		}
		w := httptest.NewRecorder()
		feedbackHandler(w, r)
		if w.Code != want || len(w.Result().Cookies()) != 0 {
			t.Errorf("status %d, cookie count %d; want status %d and no cookies", w.Code, len(w.Result().Cookies()), want)
		}
	}
	t.Run("cross origin", func(t *testing.T) { check(t, "maya", "up", "https://evil.example", http.StatusForbidden) })
	t.Run("unknown post", func(t *testing.T) { check(t, "missing", "up", "", http.StatusNotFound) })
	t.Run("unknown vote", func(t *testing.T) { check(t, "maya", "maybe", "", http.StatusBadRequest) })
	t.Run("cross-site fetch metadata", func(t *testing.T) {
		form := url.Values{"id": {"maya"}, "vote": {"up"}}
		r := httptest.NewRequest(http.MethodPost, "/feedback/fragment", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		w := httptest.NewRecorder()
		feedbackHandler(w, r)
		if w.Code != http.StatusForbidden || len(w.Result().Cookies()) != 0 {
			t.Errorf("cross-site request: status %d, cookies %d", w.Code, len(w.Result().Cookies()))
		}
	})
	t.Run("duplicate vote", func(t *testing.T) {
		form := url.Values{"id": {"maya"}, "vote": {"up", "down"}}
		r := httptest.NewRequest(http.MethodPost, "/feedback/fragment", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		feedbackHandler(w, r)
		if w.Code != http.StatusBadRequest || len(w.Result().Cookies()) != 0 {
			t.Errorf("ambiguous vote: status %d, cookies %d", w.Code, len(w.Result().Cookies()))
		}
	})
	t.Run("oversized request", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/feedback/fragment", strings.NewReader("id=maya&vote=up&padding="+strings.Repeat("x", actionFormLimit)))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		feedbackHandler(w, r)
		if w.Code != http.StatusBadRequest || len(w.Result().Cookies()) != 0 {
			t.Errorf("oversized request: status %d, cookies %d", w.Code, len(w.Result().Cookies()))
		}
	})
}

func TestFeedbackNativeFallback(t *testing.T) {
	form := url.Values{"id": {"maya"}, "vote": {"up"}}
	r := httptest.NewRequest(http.MethodPost, "https://example.com/feedback", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	feedbackHandler(w, r)
	cookies := w.Result().Cookies()
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/posts/maya" || len(cookies) != 1 || !cookies[0].Secure {
		t.Errorf("native feedback: status %d, location %q, cookies %d", w.Code, w.Header().Get("Location"), len(cookies))
	}
}

func TestFeedbackClear(t *testing.T) {
	t.Run("expires cookie", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "https://example.com/feedback/clear", nil)
		w := httptest.NewRecorder()
		feedbackClearHandler(w, r)
		cookies := w.Result().Cookies()
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/settings" || len(cookies) != 1 || cookies[0].Name != feedbackCookie || cookies[0].MaxAge >= 0 || !cookies[0].Secure {
			t.Errorf("clear: status %d, location %q, cookies %v", w.Code, w.Header().Get("Location"), cookies)
		}
	})
	t.Run("rejects cross origin", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/feedback/clear", nil)
		r.Header.Set("Origin", "https://evil.example")
		w := httptest.NewRecorder()
		feedbackClearHandler(w, r)
		if w.Code != http.StatusForbidden || len(w.Result().Cookies()) != 0 {
			t.Errorf("cross-origin clear: status %d, cookies %v", w.Code, w.Result().Cookies())
		}
	})
}
