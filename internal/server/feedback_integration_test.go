package server

import (
	"encoding/base64"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestFeedbackAcrossPages(t *testing.T) {
	h := newHandler("client", nil)
	form := url.Values{"id": {"maya"}, "vote": {"up"}}
	r := httptest.NewRequest(http.MethodPost, "/feedback/fragment", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK || len(w.Result().Cookies()) != 1 {
		t.Fatalf("feedback submission status %d, cookies %d", w.Code, len(w.Result().Cookies()))
	}
	cookie := w.Result().Cookies()[0]
	check := func(t *testing.T, path, want string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.AddCookie(cookie)
		response := httptest.NewRecorder()
		h.ServeHTTP(response, req)
		if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), want) {
			t.Errorf("%s status %d; missing %q", path, response.Code, want)
		}
	}
	t.Run("home", func(t *testing.T) { check(t, "/", `aria-label="Like" aria-pressed="true"`) })
	t.Run("explore", func(t *testing.T) { check(t, "/explore", `aria-label="Like" aria-pressed="true"`) })
	t.Run("post page", func(t *testing.T) { check(t, "/posts/maya", `aria-label="Like" aria-pressed="true"`) })
	t.Run("settings", func(t *testing.T) { check(t, "/settings", "Saved choices: 1") })
	clearRequest := httptest.NewRequest(http.MethodPost, "/feedback/clear", nil)
	clearRequest.AddCookie(cookie)
	cleared := httptest.NewRecorder()
	h.ServeHTTP(cleared, clearRequest)
	if cleared.Code != http.StatusSeeOther || cleared.Header().Get("Location") != "/settings" || len(cleared.Result().Cookies()) != 2 || cleared.Result().Cookies()[0].Name != feedbackCookie || cleared.Result().Cookies()[1].Name != cookie.Name || cleared.Result().Cookies()[0].MaxAge >= 0 || cleared.Result().Cookies()[1].MaxAge >= 0 {
		t.Fatalf("clear feedback status %d; location %q", cleared.Code, cleared.Header().Get("Location"))
	}
	settings := httptest.NewRecorder()
	h.ServeHTTP(settings, httptest.NewRequest(http.MethodGet, "/settings", nil))
	if settings.Code != http.StatusOK || !strings.Contains(settings.Body.String(), "Saved choices: 0") {
		t.Errorf("cleared settings status %d; expected zero saved choices", settings.Code)
	}
}

func TestConcurrentFeedbackForDifferentPosts(t *testing.T) {
	h := newHandler("client", nil)
	submit := func(id, vote string, result chan<- *httptest.ResponseRecorder) {
		form := url.Values{"id": {id}, "vote": {vote}}
		r := httptest.NewRequest(http.MethodPost, "https://example.com/feedback/fragment", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		result <- w
	}
	results := make(chan *httptest.ResponseRecorder, 2)
	go submit("maya", "up", results)
	go submit("elliot", "down", results)
	first, second := <-results, <-results
	if first.Code != http.StatusOK || second.Code != http.StatusOK || len(first.Result().Cookies()) != 1 || len(second.Result().Cookies()) != 1 {
		t.Fatalf("feedback responses: %d and %d", first.Code, second.Code)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	origin, err := url.Parse("https://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(origin, first.Result().Cookies())
	jar.SetCookies(origin, second.Result().Cookies())
	cookies := jar.Cookies(origin)
	if len(cookies) != 2 {
		t.Fatalf("cookie jar retained %d choices; want 2", len(cookies))
	}
	insecure, err := url.Parse("http://example.com/")
	if err != nil {
		t.Fatal(err)
	}
	if got := jar.Cookies(insecure); len(got) != 0 {
		t.Errorf("secure feedback cookies sent over HTTP: %d", len(got))
	}
	r := httptest.NewRequest(http.MethodGet, "https://example.com/", nil)
	r.AddCookie(cookies[0])
	r.AddCookie(cookies[1])
	got := feedbackSelections(r)
	if got["maya"] != "up" || got["elliot"] != "down" {
		t.Errorf("concurrent feedback lost a choice: %v", got)
	}
}

func TestLegacyFeedbackCanBeClearedIndividually(t *testing.T) {
	legacy := base64.RawURLEncoding.EncodeToString([]byte(`[{"i":"maya","v":"up"},{"i":"elliot","v":"down"}]`))
	form := url.Values{"id": {"maya"}, "vote": {"clear"}}
	r := httptest.NewRequest(http.MethodPost, "/feedback/fragment", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(&http.Cookie{Name: feedbackCookie, Value: legacy})
	w := httptest.NewRecorder()
	newHandler("client", nil).ServeHTTP(w, r)
	if w.Code != http.StatusOK || len(w.Result().Cookies()) != 1 {
		t.Fatalf("clear legacy feedback: status %d, cookies %d", w.Code, len(w.Result().Cookies()))
	}
	check := httptest.NewRequest(http.MethodGet, "/", nil)
	check.AddCookie(&http.Cookie{Name: feedbackCookie, Value: legacy})
	check.AddCookie(w.Result().Cookies()[0])
	got := feedbackSelections(check)
	if got["maya"] != "" || got["elliot"] != "down" {
		t.Errorf("legacy override changed the wrong choices: %v", got)
	}
}

func TestFeedbackOverlappingUpdatesAtCapacity(t *testing.T) {
	h := newHandler("client", nil)
	legacy := &http.Cookie{Name: feedbackCookie, Value: base64.RawURLEncoding.EncodeToString([]byte(legacyFeedbackFixture))}
	submit := func(id, vote string) *httptest.ResponseRecorder {
		t.Helper()
		form := url.Values{"id": {id}, "vote": {vote}}
		r := httptest.NewRequest(http.MethodPost, "/feedback/fragment", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.AddCookie(legacy)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w
	}
	first := submit("maya", "up")
	second := submit("elliot", "down")
	if first.Code != http.StatusOK || second.Code != http.StatusOK || len(first.Result().Cookies()) != 2 || len(second.Result().Cookies()) != 2 {
		t.Fatalf("overlapping responses: %d and %d", first.Code, second.Code)
	}
	form := url.Values{"id": {"maya"}, "vote": {"clear"}}
	r := httptest.NewRequest(http.MethodPost, "/feedback/fragment", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.AddCookie(legacy)
	r.AddCookie(first.Result().Cookies()[0])
	r.AddCookie(first.Result().Cookies()[1])
	r.AddCookie(second.Result().Cookies()[1])
	if got := feedbackSelections(r); len(got) != 24 || got["maya"] != "up" || got["elliot"] != "down" {
		t.Fatalf("overlapping feedback: %v", got)
	}
	cleared := httptest.NewRecorder()
	h.ServeHTTP(cleared, r)
	if cleared.Code != http.StatusOK || len(cleared.Result().Cookies()) != 2 || cleared.Result().Cookies()[0].Name != feedbackCookieName("a1") || cleared.Result().Cookies()[1].Name != feedbackCookieName("maya") {
		t.Fatalf("overflow cleanup: status %d, cookies %v", cleared.Code, cleared.Result().Cookies())
	}
	check := httptest.NewRequest(http.MethodGet, "/", nil)
	check.AddCookie(legacy)
	check.AddCookie(first.Result().Cookies()[0])
	check.AddCookie(cleared.Result().Cookies()[0])
	check.AddCookie(second.Result().Cookies()[1])
	if got := feedbackSelections(check); len(got) != 23 || got["a1"] != "" || got["maya"] != "" || got["elliot"] != "down" {
		t.Errorf("overflow cleanup restored an evicted choice: %v", got)
	}
}

func TestNativeFeedbackReturnsToEditablePost(t *testing.T) {
	h := newHandler("client", nil)
	form := url.Values{"id": {"maya"}, "vote": {"down"}}
	r := httptest.NewRequest(http.MethodPost, "/feedback", strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/posts/maya" || len(w.Result().Cookies()) != 1 {
		t.Fatalf("native feedback: status %d, location %q", w.Code, w.Header().Get("Location"))
	}
	postRequest := httptest.NewRequest(http.MethodGet, w.Header().Get("Location"), nil)
	postRequest.AddCookie(w.Result().Cookies()[0])
	page := httptest.NewRecorder()
	h.ServeHTTP(page, postRequest)
	if page.Code != http.StatusOK || !strings.Contains(page.Body.String(), `aria-label="Dislike" aria-pressed="true"`) || !strings.Contains(page.Body.String(), `value="clear"`) {
		t.Errorf("post page does not offer the selected feedback for editing: status %d", page.Code)
	}
}
