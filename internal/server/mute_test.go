package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const muteTestProfile = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"

// muteTestDID is the author key of the demo post "elliot", which carries the signed name "Elliot Brooks".
const muteTestDID = "did:key:z6Mkon3Necd6NkkyfoGoHxid2znGc59LU3K7mubaRcFbLfLX"

func muteTestCookie() *http.Cookie {
	return &http.Cookie{Name: profileCookie, Value: muteTestProfile}
}

func TestMuteMatch(t *testing.T) {
	t.Run("whole word in a sentence", func(t *testing.T) {
		if !muteMatch("Art show", "art") {
			t.Error(`"art" should match the whole word in "Art show"`)
		}
	})
	t.Run("inside a longer word", func(t *testing.T) {
		if muteMatch("party", "art") {
			t.Error(`"art" should not match inside "party"`)
		}
	})
	t.Run("before punctuation", func(t *testing.T) {
		if !muteMatch("ART.", "art") {
			t.Error(`"art" should match "ART." ignoring case and before a full stop`)
		}
	})
	t.Run("phrase across a line break", func(t *testing.T) {
		if !muteMatch("good\nmorning all", "good morning") {
			t.Error(`"good morning" should match across a line break`)
		}
	})
}

func TestMuteWordNormalize(t *testing.T) {
	t.Run("spaces collapse and case folds", func(t *testing.T) {
		word, err := muteNormalize("  Good   Morning ")
		if err != nil || word != "good morning" {
			t.Errorf("muteNormalize = %q, %v; want %q and no error", word, err, "good morning")
		}
	})
	t.Run("one character is refused", func(t *testing.T) {
		_, err := muteNormalize("a")
		if err == nil || err.Error() != "Use 2 to 40 characters." {
			t.Errorf("muteNormalize(\"a\") error = %v; want Use 2 to 40 characters.", err)
		}
	})
}

func TestMuteKey(t *testing.T) {
	t.Run("muting a post's key folds that author's posts on Home", func(t *testing.T) {
		h := testClientHandler(t)
		w := postForm(h, "/mute/key", url.Values{"id": {"elliot"}, "mute": {"on"}}, muteTestCookie())
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/muted?saved=key-added" {
			t.Fatalf("mute answered %d to %q; want 303 to /muted?saved=key-added", w.Code, w.Header().Get("Location"))
		}
		home := getWith(h, "/?limit=10000", muteTestCookie())
		if !strings.Contains(home, "Folded by your filters: Muted key (Elliot Brooks, 256c aa67 64a5 28a2)") {
			t.Error("Home should fold posts by the muted key with its name and fingerprint")
		}
		if !strings.Contains(home, `id="thread-maya"`) || strings.Contains(home, "Muted key (Maya") {
			t.Error("an unsigned post should stay in the feed without a fold")
		}
	})
	t.Run("fragment answers with undo and a status", func(t *testing.T) {
		h := testClientHandler(t)
		w := postForm(h, "/mute/key/fragment", url.Values{"id": {"elliot"}, "mute": {"on"}}, muteTestCookie())
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "Muted. Posts by this key fold the next time a page loads.") || !strings.Contains(body, `name="mute" value="off"`) || !strings.Contains(body, "data-mute-focus") {
			t.Errorf("fragment answered %d without the status and undo control", w.Code)
		}
	})
	t.Run("unmute from the Muted page", func(t *testing.T) {
		h := testClientHandler(t)
		postForm(h, "/mute/key", url.Values{"id": {"elliot"}, "mute": {"on"}}, muteTestCookie())
		w := postForm(h, "/muted/keys/remove", url.Values{"key": {muteTestDID}}, muteTestCookie())
		if w.Code != http.StatusSeeOther {
			t.Fatalf("unmute answered %d; want 303", w.Code)
		}
		if home := getWith(h, "/?limit=10000", muteTestCookie()); strings.Contains(home, "Muted key (") {
			t.Error("Home still folds the key after it was unmuted")
		}
	})
	t.Run("cross-origin post is refused", func(t *testing.T) {
		h := testClientHandler(t)
		r := httptest.NewRequest(http.MethodPost, "/mute/key", strings.NewReader(url.Values{"id": {"elliot"}, "mute": {"on"}}.Encode()))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://evil.example")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("cross-origin mute answered %d; want 403", w.Code)
		}
	})
}

func TestMuteWords(t *testing.T) {
	t.Run("a muted word folds a matching post", func(t *testing.T) {
		h := testClientHandler(t)
		w := postForm(h, "/muted/words", url.Values{"word": {"garden"}}, muteTestCookie())
		if w.Code != http.StatusSeeOther {
			t.Fatalf("add word answered %d; want 303", w.Code)
		}
		home := getWith(h, "/?limit=10000", muteTestCookie())
		if strings.Count(home, "Folded by your filters: Muted word (“garden”)") != 1 {
			t.Error("Home should fold exactly one post that contains the muted word")
		}
	})
	t.Run("a muted word folds a matching reply", func(t *testing.T) {
		h := testClientHandler(t)
		postForm(h, "/muted/words", url.Values{"word": {"basil"}}, muteTestCookie())
		thread := getWith(h, "/thread/fragment?id=garden", muteTestCookie())
		if !strings.Contains(thread, "Muted word (“basil”)") {
			t.Error("the thread should fold the reply that holds the muted word")
		}
		if !strings.Contains(thread, `<span class="reply-count">3</span>`) {
			t.Error("folding a reply must not change the reply count")
		}
	})
	t.Run("an invalid word keeps the input and explains", func(t *testing.T) {
		h := testClientHandler(t)
		w := postForm(h, "/muted/words/fragment", url.Values{"word": {"a"}}, muteTestCookie())
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, `aria-invalid="true"`) || !strings.Contains(body, "Use 2 to 40 characters.") || !strings.Contains(body, `value="a"`) {
			t.Errorf("fragment answered %d; want 200 with the field error and the typed value kept", w.Code)
		}
		full := postForm(h, "/muted/words", url.Values{"word": {"a"}}, muteTestCookie())
		if full.Code != http.StatusUnprocessableEntity {
			t.Errorf("full-page invalid word answered %d; want 422", full.Code)
		}
	})
}

func TestMuteFile(t *testing.T) {
	t.Run("empty Muted page explains how to mute", func(t *testing.T) {
		body := getWith(testClientHandler(t), "/muted")
		if !strings.Contains(body, "<h1>Muted</h1>") || !strings.Contains(body, "No muted keys.") || !strings.Contains(body, "No muted words or phrases.") {
			t.Error("the empty Muted page should explain how to mute keys and words")
		}
	})
	t.Run("a damaged file folds nothing and offers to clear it", func(t *testing.T) {
		dir := t.TempDir()
		client, err := NewClient(Config{DataDir: dir, PublicOrigin: "https://example.test"})
		if err != nil {
			t.Fatal(err)
		}
		h := client.Handler()
		profileDir := filepath.Join(dir, "profiles", muteTestProfile)
		if err := os.MkdirAll(profileDir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(profileDir, muteFileName), []byte(`{"version":2}`), 0600); err != nil {
			t.Fatal(err)
		}
		if home := getWith(h, "/?limit=10000", muteTestCookie()); strings.Contains(home, "Muted key (") {
			t.Error("a damaged mute list must fold nothing")
		}
		if body := getWith(h, "/muted", muteTestCookie()); !strings.Contains(body, "could not be read") {
			t.Error("the Muted page should explain that the list could not be read")
		}
		if w := postForm(h, "/muted/words", url.Values{"word": {"garden"}}, muteTestCookie()); w.Code != http.StatusConflict {
			t.Errorf("write to a damaged list answered %d; want 409", w.Code)
		}
		if w := postForm(h, "/muted/clear", url.Values{}, muteTestCookie()); w.Code != http.StatusSeeOther {
			t.Errorf("clear answered %d; want 303", w.Code)
		}
		if _, err := os.Stat(filepath.Join(profileDir, muteFileName)); !os.IsNotExist(err) {
			t.Error("clearing should remove the damaged file")
		}
	})
	t.Run("the file is written private", func(t *testing.T) {
		dir := t.TempDir()
		client, err := NewClient(Config{DataDir: dir, PublicOrigin: "https://example.test"})
		if err != nil {
			t.Fatal(err)
		}
		postForm(client.Handler(), "/mute/key", url.Values{"id": {"elliot"}, "mute": {"on"}}, muteTestCookie())
		info, err := os.Stat(filepath.Join(dir, "profiles", muteTestProfile, muteFileName))
		if err != nil {
			t.Fatalf("mute list file missing: %v", err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Errorf("mute list mode = %v; want 0600", info.Mode().Perm())
		}
	})
}
