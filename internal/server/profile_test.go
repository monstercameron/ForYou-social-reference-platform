package server

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBrowserProfileCookie(t *testing.T) {
	client := newTestClient(t, false)
	t.Run("new profile has a canonical cookie", func(t *testing.T) {
		w := httptest.NewRecorder()
		id, err := client.app.profile(w, httptest.NewRequest(http.MethodGet, "/", nil))
		if err != nil {
			t.Fatal(err)
		}
		cookie := w.Result().Cookies()[0]
		if len(id) != 43 || !validProfileID(id) || cookie.Name != "foryou_profile" || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" || cookie.MaxAge != 31536000 {
			t.Errorf("profile cookie = %#v, id length %d", cookie, len(id))
		}
	})
	t.Run("valid cookie is retained", func(t *testing.T) {
		id := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("x", 32)))
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(&http.Cookie{Name: profileCookie, Value: id})
		w := httptest.NewRecorder()
		got, err := client.app.profile(w, r)
		if err != nil || got != id || len(w.Result().Cookies()) != 0 {
			t.Errorf("profile = %q, error %v, cookies %v", got, err, w.Result().Cookies())
		}
	})
	t.Run("malformed cookie is replaced", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.AddCookie(&http.Cookie{Name: profileCookie, Value: "../../x"})
		w := httptest.NewRecorder()
		got, err := client.app.profile(w, r)
		if err != nil || got == "../../x" || !validProfileID(got) {
			t.Errorf("malformed profile = %q, error %v", got, err)
		}
	})
}

func TestProfileSettingsAreIsolatedAndStrict(t *testing.T) {
	client := newTestClient(t, false)
	profileA := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("a", 32)))
	profileB := base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("b", 32)))
	t.Run("settings are per profile", func(t *testing.T) {
		if err := client.app.updateSettings(profileA, func(settings *ProfileSettings) error {
			settings.PublishNetwork = "bitcoin"
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		gotA, err := client.app.settings(profileA)
		if err != nil || gotA.PublishNetwork != "bitcoin" {
			t.Errorf("profile A = %#v, error %v", gotA, err)
		}
		gotB, err := client.app.settings(profileB)
		if err != nil || gotB.PublishNetwork != "" {
			t.Errorf("profile B = %#v, error %v", gotB, err)
		}
	})
	t.Run("unknown fields fail decoding", func(t *testing.T) {
		dir := filepath.Join(client.app.profiles.root, profileB)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(`{"unknown":true}`), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := client.app.settings(profileB); err == nil {
			t.Error("unknown settings field was accepted")
		}
	})
}
