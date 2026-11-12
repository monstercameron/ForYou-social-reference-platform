package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

const startTestProfile = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

func TestStartPage(t *testing.T) {
	t.Run("a new reader sees nothing done", func(t *testing.T) {
		w := httptest.NewRecorder()
		testClientHandler(t).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/start", nil))
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "<h1>Getting started</h1>") || !strings.Contains(body, "0 of 3 checked steps done. Funding") {
			t.Errorf("status %d; want the page with 0 of 3 checked steps done. Funding", w.Code)
		}
		if strings.Count(body, `pill-warn">To do</span>`) != 3 || strings.Count(body, `pill-muted">Not checked here</span>`) != 2 {
			t.Error("a new reader should have three To do steps and two steps not checked here")
		}
		for _, c := range w.Result().Cookies() {
			if c.Name == profileCookie {
				t.Error("the guide must not create a profile")
			}
		}
	})
	t.Run("an existing key marks the key step done", func(t *testing.T) {
		client := newTestClient(t, true)
		identity, err := client.app.keystore().Create(startTestProfile)
		if err != nil {
			t.Fatal(err)
		}
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodGet, "/start", nil)
		r.AddCookie(&http.Cookie{Name: profileCookie, Value: startTestProfile})
		client.Handler().ServeHTTP(w, r)
		body := w.Body.String()
		if !strings.Contains(body, "Your key "+shortDID(identity.DID)+" signs everything you publish.") || !strings.Contains(body, "dev custody") {
			t.Error("the key step should name the key and say it is held in dev custody")
		}
		if !strings.Contains(body, "1 of 3 checked steps done.") {
			t.Error("the progress line should count the key step as done")
		}
	})
	t.Run("a like marks the follow step done", func(t *testing.T) {
		if !strings.Contains(getWith(testClientHandler(t), "/start", voteCookie("maya", "up")), "liking 1 post") {
			t.Error("a liked post should mark the follow step done and count it")
		}
	})
	t.Run("a saved filter marks the filters step done", func(t *testing.T) {
		h := testClientHandler(t)
		if !strings.Contains(getWith(h, "/start", &http.Cookie{Name: filterCookie, Value: "32333311"}), "Change filters") {
			t.Error("a valid filter cookie should mark the filters step done")
		}
		if !strings.Contains(getWith(h, "/start", &http.Cookie{Name: filterCookie, Value: "99"}), "Set filters") {
			t.Error("a damaged filter cookie should leave the filters step to do")
		}
	})
	t.Run("the fee is never called a provider price", func(t *testing.T) {
		body := getWith(testClientHandler(t), "/start")
		if !strings.Contains(body, "passed to the ForYou Foundation") || strings.Contains(body, "provider price") || strings.Contains(body, "provider fee") {
			t.Error("the fee must be described as passed to the Foundation, never as a provider price or fee")
		}
	})
}

func TestStartPanel(t *testing.T) {
	h := testClientHandler(t)
	t.Run("rail panel lists five steps", func(t *testing.T) {
		body := getWith(h, "/start/panel?place=rail")
		if !strings.Contains(body, `id="start-rail"`) || strings.Count(body, `<li class="start-step `) != 5 || !strings.Contains(body, `post="/start/hide/fragment"`) {
			t.Error("the rail panel should list five steps and post its hide control to the fragment route")
		}
	})
	t.Run("unknown place is refused", func(t *testing.T) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/start/panel?place=side", nil))
		if w.Code != http.StatusBadRequest {
			t.Errorf("status %d; want 400", w.Code)
		}
	})
	t.Run("hidden cookie collapses the rail", func(t *testing.T) {
		body := getWith(h, "/start/panel?place=rail", &http.Cookie{Name: startCookie, Value: "hidden"})
		if !strings.Contains(body, `id="start-rail-show"`) || strings.Contains(body, `<li class="start-step `) {
			t.Error("a hidden guide should answer with the collapsed rail and no steps")
		}
	})
	t.Run("damaged cookie means shown", func(t *testing.T) {
		body := getWith(h, "/start/panel?place=rail", &http.Cookie{Name: startCookie, Value: "maybe"})
		if strings.Count(body, `<li class="start-step `) != 5 {
			t.Error("an unknown guide cookie should show the steps")
		}
	})
}

func TestStartHideAndShow(t *testing.T) {
	h := testClientHandler(t)
	t.Run("hide without scripts redirects home", func(t *testing.T) {
		w := postForm(h, "/start/hide", url.Values{"place": {"rail"}})
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/" {
			t.Fatalf("status %d to %q; want 303 to /", w.Code, w.Header().Get("Location"))
		}
		cookies := w.Result().Cookies()
		if len(cookies) != 1 || cookies[0].Name != startCookie || cookies[0].Value != "hidden" || cookies[0].MaxAge != 31536000 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
			t.Error("hiding should set the hidden cookie for a year, HttpOnly and SameSite Strict")
		}
	})
	t.Run("hide from the page returns to the page", func(t *testing.T) {
		w := postForm(h, "/start/hide", url.Values{"place": {"page"}})
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/start#start-home" {
			t.Errorf("status %d to %q; want 303 to /start#start-home", w.Code, w.Header().Get("Location"))
		}
	})
	t.Run("show fragment expires the cookie and returns the panel", func(t *testing.T) {
		w := postForm(h, "/start/show/fragment", url.Values{"place": {"rail"}}, &http.Cookie{Name: startCookie, Value: "hidden"})
		cookies := w.Result().Cookies()
		if w.Code != http.StatusOK || len(cookies) != 1 || cookies[0].MaxAge >= 0 || !strings.Contains(w.Body.String(), `id="start-rail-title"`) {
			t.Errorf("status %d; want the rail panel and an expired cookie", w.Code)
		}
	})
	t.Run("cross-origin hide is refused", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/start/hide", strings.NewReader(url.Values{"place": {"rail"}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://evil.example")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden || w.Header().Get("Set-Cookie") != "" {
			t.Errorf("status %d with cookie %q; want 403 and no cookie", w.Code, w.Header().Get("Set-Cookie"))
		}
	})
}

func TestStartHooks(t *testing.T) {
	h := testClientHandler(t)
	t.Run("home has the rail and feed placeholders", func(t *testing.T) {
		body := getWith(h, "/")
		if strings.Count(body, `get="/start/panel"`) != 2 || !strings.Contains(body, `value="rail"`) || !strings.Contains(body, `value="feed"`) {
			t.Error("Home should carry one rail and one feed placeholder that load the guide")
		}
	})
	t.Run("other pages do not", func(t *testing.T) {
		if strings.Contains(getWith(h, "/explore"), `get="/start/panel"`) {
			t.Error("the guide belongs on Home only")
		}
	})
}
