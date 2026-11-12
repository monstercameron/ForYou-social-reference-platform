package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func bmProfileCookie(t *testing.T, w *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	for _, c := range w.Result().Cookies() {
		if c.Name == profileCookie {
			return c
		}
	}
	t.Fatal("no profile cookie was set")
	return nil
}

func bmGet(h http.Handler, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// bmProfileDir returns the profile directory the client chose for the cookie.
func bmProfileDir(root string, cookie *http.Cookie) string {
	return filepath.Join(root, "profiles", cookie.Value)
}

func TestBookmarksControl(t *testing.T) {
	h := testClientHandler(t)
	t.Run("feed cards carry the lazy slot", func(t *testing.T) {
		body := getWith(h, "/")
		if !strings.Contains(body, `id="bookmarks-slot-maya"`) || !strings.Contains(body, `get="/saved/control"`) {
			t.Error("feed card has no lazy save slot")
		}
		if !strings.Contains(body, `href="/saved"`) {
			t.Error("feed filters have no Saved pill")
		}
	})
	t.Run("unsaved control is not pressed", func(t *testing.T) {
		body := getWith(h, "/saved/control?id=science")
		if !strings.Contains(body, `aria-pressed="false"`) || !strings.Contains(body, `name="want" value="save"`) {
			t.Error("unsaved control is not rendered as unpressed save")
		}
	})
	t.Run("saving presses the control", func(t *testing.T) {
		w := postForm(h, "/saved/toggle/fragment", url.Values{"id": {"science"}, "want": {"save"}})
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, `aria-pressed="true"`) || !strings.Contains(body, "Saved on this server for this browser.") || !strings.Contains(body, `value="remove"`) {
			t.Errorf("status %d, body %q", w.Code, body)
		}
	})
	t.Run("control fragment targets its slot by ID", func(t *testing.T) {
		body := postForm(h, "/saved/toggle/fragment", url.Values{"id": {"science"}, "want": {"remove"}}).Body.String()
		if !strings.Contains(body, `target="#bookmarks-slot-science(innerHTML)"`) || !strings.Contains(body, `data-focus-after-swap="#bookmarks-slot-science button"`) {
			t.Errorf("control fragment does not target its slot: %q", body)
		}
	})
	t.Run("a reply can be saved", func(t *testing.T) {
		w := postForm(h, "/saved/toggle/fragment", url.Values{"id": {"noor"}, "want": {"save"}})
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `aria-pressed="true"`) {
			t.Errorf("status %d; reply control not pressed", w.Code)
		}
	})
	t.Run("plain form redirects to the item page", func(t *testing.T) {
		w := postForm(h, "/saved/toggle", url.Values{"id": {"science"}, "want": {"save"}})
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/saved/item?id=science&done=saved" {
			t.Errorf("status %d, location %q", w.Code, w.Header().Get("Location"))
		}
	})
	t.Run("unknown post is not saved", func(t *testing.T) {
		w := postForm(h, "/saved/toggle", url.Values{"id": {"no-such-post"}, "want": {"save"}})
		if w.Code != http.StatusNotFound {
			t.Errorf("status %d; want 404", w.Code)
		}
	})
	t.Run("cross-origin save is refused", func(t *testing.T) {
		form := url.Values{"id": {"science"}, "want": {"save"}}
		r := httptest.NewRequest(http.MethodPost, "/saved/toggle", strings.NewReader(form.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://evil.example")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("status %d; want 403", w.Code)
		}
	})
	t.Run("bad want is refused", func(t *testing.T) {
		w := postForm(h, "/saved/toggle", url.Values{"id": {"science"}, "want": {"maybe"}})
		if w.Code != http.StatusBadRequest {
			t.Errorf("status %d; want 400", w.Code)
		}
	})
}

func TestBookmarksPage(t *testing.T) {
	t.Run("empty state", func(t *testing.T) {
		h := testClientHandler(t)
		body := getWith(h, "/saved")
		if !strings.Contains(body, "Nothing saved yet") || strings.Contains(body, "bookmarks-count") {
			t.Error("empty Saved page does not show the empty state alone")
		}
	})
	t.Run("newest first with an honest count", func(t *testing.T) {
		h := testClientHandler(t)
		first := postForm(h, "/saved/toggle/fragment", url.Values{"id": {"science"}, "want": {"save"}})
		cookie := bmProfileCookie(t, first)
		postForm(h, "/saved/toggle/fragment", url.Values{"id": {"noor"}, "want": {"save"}}, cookie)
		body := getWith(h, "/saved", cookie)
		if !strings.Contains(body, "2 saved posts") {
			t.Error("count line does not read 2 saved posts")
		}
		if !strings.Contains(body, "bookmarks-slot-noor") || !strings.Contains(body, "bookmarks-slot-science") {
			t.Fatal("both saved posts should be listed")
		}
		if strings.Index(body, "bookmarks-slot-noor") > strings.Index(body, "bookmarks-slot-science") {
			t.Error("newest save is not listed first")
		}
	})
	t.Run("remove updates the list", func(t *testing.T) {
		h := testClientHandler(t)
		first := postForm(h, "/saved/toggle/fragment", url.Values{"id": {"science"}, "want": {"save"}})
		cookie := bmProfileCookie(t, first)
		postForm(h, "/saved/toggle/fragment", url.Values{"id": {"noor"}, "want": {"save"}}, cookie)
		w := postForm(h, "/saved/remove/fragment", url.Values{"id": {"noor"}, "page": {"1"}}, cookie)
		body := w.Body.String()
		if !strings.Contains(body, "Removed. 1 saved post left.") || strings.Contains(body, "bookmarks-slot-noor") {
			t.Errorf("remove did not update the list: %q", body)
		}
	})
	t.Run("clear empties the list", func(t *testing.T) {
		h := testClientHandler(t)
		first := postForm(h, "/saved/toggle/fragment", url.Values{"id": {"science"}, "want": {"save"}})
		cookie := bmProfileCookie(t, first)
		w := postForm(h, "/saved/clear/fragment", url.Values{}, cookie)
		if !strings.Contains(w.Body.String(), "Cleared. Nothing saved.") || !strings.Contains(w.Body.String(), "Nothing saved yet") {
			t.Errorf("clear did not empty the list: %q", w.Body.String())
		}
	})
	t.Run("missing post stays removable", func(t *testing.T) {
		dataDir := t.TempDir()
		client, err := NewClient(Config{DataDir: dataDir, PublicOrigin: "https://example.test"})
		if err != nil {
			t.Fatal(err)
		}
		h := client.Handler()
		first := postForm(h, "/saved/toggle/fragment", url.Values{"id": {"science"}, "want": {"save"}})
		cookie := bmProfileCookie(t, first)
		dir := bmProfileDir(dataDir, cookie)
		if err := os.WriteFile(filepath.Join(dir, "saved.json"), []byte(`{"version":1,"entries":[{"id":"gone-post","saved_at":"2026-01-02T03:04:05Z"}]}`), 0600); err != nil {
			t.Fatal(err)
		}
		body := getWith(h, "/saved", cookie)
		if !strings.Contains(body, "This post is not in your current sources.") || !strings.Contains(body, `value="gone-post"`) {
			t.Error("missing post is not shown with a Remove form")
		}
	})
	t.Run("damaged file is reported, not overwritten", func(t *testing.T) {
		dataDir := t.TempDir()
		client, err := NewClient(Config{DataDir: dataDir, PublicOrigin: "https://example.test"})
		if err != nil {
			t.Fatal(err)
		}
		h := client.Handler()
		first := postForm(h, "/saved/toggle/fragment", url.Values{"id": {"science"}, "want": {"save"}})
		cookie := bmProfileCookie(t, first)
		path := filepath.Join(bmProfileDir(dataDir, cookie), "saved.json")
		damaged := []byte(`{"version":2,"entries":[]}`)
		if err := os.WriteFile(path, damaged, 0600); err != nil {
			t.Fatal(err)
		}
		if body := getWith(h, "/saved", cookie); !strings.Contains(body, "could not be read") {
			t.Error("damaged list is not reported")
		}
		w := postForm(h, "/saved/toggle/fragment", url.Values{"id": {"noor"}, "want": {"save"}}, cookie)
		if w.Code != http.StatusConflict {
			t.Errorf("save status %d; want 409", w.Code)
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != string(damaged) {
			t.Errorf("damaged file was changed: %q, %v", got, err)
		}
		postForm(h, "/saved/clear", url.Values{}, cookie)
		if body := getWith(h, "/saved", cookie); !strings.Contains(body, "Nothing saved yet") {
			t.Error("clear did not replace the damaged list")
		}
	})
	t.Run("page out of range is refused", func(t *testing.T) {
		h := testClientHandler(t)
		if w := bmGet(h, "/saved?page=26"); w.Code != http.StatusBadRequest {
			t.Errorf("status %d; want 400", w.Code)
		}
	})
}

// bmFullList writes a saved list at the limit, with ids p000 to p499.
func bmFullList() string {
	var b strings.Builder
	b.WriteString(`{"version":1,"entries":[`)
	for i := range bmLimit {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, `{"id":"p%03d","saved_at":"2026-01-02T03:04:05Z"}`, i)
	}
	b.WriteString(`]}`)
	return b.String()
}

func TestBookmarksLimit(t *testing.T) {
	t.Run("the 501st save is refused", func(t *testing.T) {
		dataDir := t.TempDir()
		client, err := NewClient(Config{DataDir: dataDir, PublicOrigin: "https://example.test"})
		if err != nil {
			t.Fatal(err)
		}
		h := client.Handler()
		first := postForm(h, "/saved/toggle/fragment", url.Values{"id": {"noor"}, "want": {"save"}})
		cookie := bmProfileCookie(t, first)
		if err := os.WriteFile(filepath.Join(bmProfileDir(dataDir, cookie), "saved.json"), []byte(bmFullList()), 0600); err != nil {
			t.Fatal(err)
		}
		w := postForm(h, "/saved/toggle/fragment", url.Values{"id": {"science"}, "want": {"save"}}, cookie)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "the most this server keeps") {
			t.Errorf("status %d; body %q", w.Code, w.Body.String())
		}
	})
}
