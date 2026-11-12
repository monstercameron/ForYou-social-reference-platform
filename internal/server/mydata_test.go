package server

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/keys"
)

func mydataTestDID(t *testing.T) string {
	t.Helper()
	priv, err := keys.NewKeyFromSeed(make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return keys.DIDOf(priv)
}

func mydataUploadRequest(t *testing.T, content []byte, cookies ...*http.Cookie) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "foryou-data.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/mydata/import", &body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	for _, c := range cookies {
		r.AddCookie(c)
	}
	return r
}

// mydataShowsSeed reports whether a body carries a seed in hex, standard base64 or raw URL base64.
func mydataShowsSeed(body string, seed []byte) bool {
	return strings.Contains(body, hex.EncodeToString(seed)) ||
		strings.Contains(body, base64.StdEncoding.EncodeToString(seed)) ||
		strings.Contains(body, base64.RawURLEncoding.EncodeToString(seed))
}

// mydataTestFollow puts one key on the test profile's follow list.
func mydataTestFollow(t *testing.T, client *Client, did string) {
	t.Helper()
	err := client.app.followingUpdate(setTestProfile, func(state *followingState) error {
		return followingAdd(state, did, followingViaManual, time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC))
	})
	if err != nil {
		t.Fatal(err)
	}
}

func mydataServe(h http.Handler, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestMydataPage(t *testing.T) {
	t.Run("lists each kind with where it lives", func(t *testing.T) {
		client := newTestClient(t, false)
		mydataTestFollow(t, client, mydataTestDID(t))
		body := getWith(client.Handler(), "/mydata", &http.Cookie{Name: themeCookie, Value: "dark"}, &http.Cookie{Name: profileCookie, Value: setTestProfile})
		if !strings.Contains(body, "foryou_theme") {
			t.Error("Your data page does not name the foryou_theme cookie")
		}
		if !strings.Contains(body, "Dark") {
			t.Error("Your data page does not show the dark appearance")
		}
		if !strings.Contains(body, "following.json") {
			t.Error("Your data page does not name the follow list file on this server")
		}
		if !strings.Contains(body, "1 key followed (up to 500).") {
			t.Error("Your data page does not count the followed key")
		}
		if !strings.Contains(body, "settings.json") {
			t.Error("Your data page does not name settings.json on this server")
		}
		if !strings.Contains(body, `href="/settings" aria-current="page"`) {
			t.Error("Your data page does not link to Settings as the current section")
		}
	})
	t.Run("a GET sets no cookie", func(t *testing.T) {
		client := newTestClient(t, false)
		w := mydataServe(client.Handler(), httptest.NewRequest(http.MethodGet, "/mydata", nil))
		if w.Code != http.StatusOK || w.Header().Get("Set-Cookie") != "" {
			t.Errorf("GET /mydata status %d; want 200 without a Set-Cookie header", w.Code)
		}
	})
	t.Run("never shows a private key", func(t *testing.T) {
		client := newTestClient(t, false)
		identity, err := client.app.keystore().Create(setTestProfile)
		if err != nil {
			t.Fatal(err)
		}
		priv, err := client.app.keystore().PrivateKey(setTestProfile)
		if err != nil {
			t.Fatal(err)
		}
		seed := priv.Seed()
		cookie := &http.Cookie{Name: profileCookie, Value: setTestProfile}
		page := getWith(client.Handler(), "/mydata", cookie)
		export := getWith(client.Handler(), "/mydata/export", cookie)
		if !strings.Contains(page, identity.DID) || !strings.Contains(export, identity.DID) {
			t.Error("the page or the export does not carry the public did:key")
		}
		if mydataShowsSeed(page, seed) || mydataShowsSeed(export, seed) {
			t.Error("the page or the export shows the private key seed")
		}
	})
	t.Run("a damaged settings file is reported, not shown", func(t *testing.T) {
		client := newTestClient(t, false)
		dir := filepath.Join(client.app.profiles.root, setTestProfile)
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte("{not json"), 0600); err != nil {
			t.Fatal(err)
		}
		cookie := &http.Cookie{Name: profileCookie, Value: setTestProfile}
		if page := getWith(client.Handler(), "/mydata", cookie); !strings.Contains(page, "could not be read") {
			t.Error("a damaged settings file is not reported on the page")
		}
		if export := getWith(client.Handler(), "/mydata/export", cookie); !strings.Contains(export, `"settings_file_error"`) {
			t.Error("a damaged settings file is not marked in the export")
		}
	})
}

func TestMydataExport(t *testing.T) {
	t.Run("holds the reader's choices", func(t *testing.T) {
		client := newTestClient(t, false)
		r := httptest.NewRequest(http.MethodGet, "/mydata/export", nil)
		r.AddCookie(&http.Cookie{Name: themeCookie, Value: "dark"})
		r.AddCookie(voteCookie("garden", "up"))
		w := mydataServe(client.Handler(), r)
		if disposition := w.Header().Get("Content-Disposition"); !strings.HasPrefix(disposition, `attachment; filename="foryou-data-`) {
			t.Errorf("export disposition %q", disposition)
		}
		var file mydataFile
		if err := json.Unmarshal(w.Body.Bytes(), &file); err != nil {
			t.Fatal(err)
		}
		if file.Appearance == nil || *file.Appearance != "dark" || file.Feedback == nil || len(*file.Feedback) != 1 || (*file.Feedback)[0] != (mydataVote{Post: "garden", Vote: "up"}) {
			t.Errorf("export does not hold the theme and the garden vote: %s", w.Body.String())
		}
	})
}

func TestMydataImport(t *testing.T) {
	t.Run("preview shows the change and saves nothing", func(t *testing.T) {
		client := newTestClient(t, false)
		w := mydataServe(client.Handler(), mydataUploadRequest(t, []byte(`{"format":"foryou-client-data","version":1,"appearance":"dark"}`)))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Appearance: System → Dark") || w.Header().Get("Set-Cookie") != "" {
			t.Errorf("preview status %d; want the appearance change with no cookie set", w.Code)
		}
	})
	t.Run("apply sets only the chosen parts", func(t *testing.T) {
		client := newTestClient(t, false)
		did := mydataTestDID(t)
		content := []byte(`{"format":"foryou-client-data","version":1,"appearance":"dark","follows":["` + did + `"]}`)
		preview := mydataServe(client.Handler(), mydataUploadRequest(t, content))
		match := regexp.MustCompile(`name="data" value="([^"]+)"`).FindStringSubmatch(preview.Body.String())
		if match == nil {
			t.Fatal("preview has no data field")
		}
		form := url.Values{"data": {match[1]}, "kind": {"appearance"}}
		w := postForm(client.Handler(), "/mydata/import/apply", form)
		var theme, follows bool
		for _, c := range w.Result().Cookies() {
			theme = theme || (c.Name == themeCookie && c.Value == "dark")
			follows = follows || c.Name == followCookie
		}
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/mydata?imported=1#mydata-kinds-title" || !theme || follows {
			t.Errorf("apply status %d location %q; want only the theme cookie", w.Code, w.Header().Get("Location"))
		}
	})
	t.Run("refuses unknown fields", func(t *testing.T) {
		client := newTestClient(t, false)
		w := mydataServe(client.Handler(), mydataUploadRequest(t, []byte(`{"format":"foryou-client-data","version":1,"extra":1}`)))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "The file has fields this client does not know.") {
			t.Errorf("unknown field status %d", w.Code)
		}
	})
	t.Run("refuses a file over 64 KiB", func(t *testing.T) {
		client := newTestClient(t, false)
		w := mydataServe(client.Handler(), mydataUploadRequest(t, bytes.Repeat([]byte(" "), 65537)))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "The file is larger than 64 KiB.") {
			t.Errorf("large file status %d", w.Code)
		}
	})
	t.Run("refuses another version", func(t *testing.T) {
		client := newTestClient(t, false)
		w := mydataServe(client.Handler(), mydataUploadRequest(t, []byte(`{"format":"foryou-client-data","version":2}`)))
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "format version 2") {
			t.Errorf("version status %d", w.Code)
		}
	})
	t.Run("refuses a cross-origin upload", func(t *testing.T) {
		client := newTestClient(t, false)
		r := mydataUploadRequest(t, []byte(`{"format":"foryou-client-data","version":1}`))
		r.Header.Set("Origin", "https://evil.example")
		if w := mydataServe(client.Handler(), r); w.Code != http.StatusForbidden {
			t.Errorf("cross-origin upload status %d; want 403", w.Code)
		}
	})
}

func TestMydataErase(t *testing.T) {
	t.Run("erasing follows empties the profile's follow list", func(t *testing.T) {
		client := newTestClient(t, false)
		mydataTestFollow(t, client, mydataTestDID(t))
		cookie := &http.Cookie{Name: profileCookie, Value: setTestProfile}
		plain := postForm(client.Handler(), "/mydata/erase", url.Values{"kind": {"follows"}}, cookie)
		state, err := client.app.followingRead(setTestProfile)
		if plain.Code != http.StatusSeeOther || plain.Header().Get("Location") != "/mydata?erased=follows#mydata-kinds-title" || err != nil || len(state.Follows) != 0 {
			t.Errorf("plain erase status %d, follows left %d, error %v; want 303 and an empty follow list", plain.Code, len(state.Follows), err)
		}
		fragment := postForm(client.Handler(), "/mydata/erase/fragment", url.Values{"kind": {"follows"}}, cookie)
		if fragment.Code != http.StatusOK || !strings.Contains(fragment.Body.String(), `id="mydata-follows"`) || !strings.Contains(fragment.Body.String(), "Nothing saved") {
			t.Errorf("fragment erase status %d; the follows row should read Nothing saved", fragment.Code)
		}
	})
	t.Run("erase everything needs the box", func(t *testing.T) {
		client := newTestClient(t, false)
		w := postForm(client.Handler(), "/mydata/erase", url.Values{"kind": {"everything"}})
		if w.Code != http.StatusBadRequest || w.Header().Get("Set-Cookie") != "" || !strings.Contains(w.Body.String(), "Tick the box to confirm") {
			t.Errorf("unconfirmed erase status %d; want 400 without cookies", w.Code)
		}
	})
	t.Run("erase everything keeps a profile that has a key", func(t *testing.T) {
		client := newTestClient(t, false)
		if _, err := client.app.keystore().Create(setTestProfile); err != nil {
			t.Fatal(err)
		}
		cookie := setProfileWithOrigins(t, client, "https://media.example:443")
		w := postForm(client.Handler(), "/mydata/erase", url.Values{"kind": {"everything"}, "confirm": {"yes"}}, cookie)
		if _, err := os.Stat(filepath.Join(client.app.profiles.root, setTestProfile, "settings.json")); !os.IsNotExist(err) {
			t.Error("erase everything left the settings file in place")
		}
		for _, c := range w.Result().Cookies() {
			if c.Name == profileCookie {
				t.Error("erase everything expired the profile cookie of a profile with a key")
			}
		}
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/mydata?erased=everything-kept#mydata-kinds-title" {
			t.Errorf("erase everything status %d location %q", w.Code, w.Header().Get("Location"))
		}
	})
	t.Run("erase everything forgets a profile with no key", func(t *testing.T) {
		client := newTestClient(t, false)
		cookie := &http.Cookie{Name: profileCookie, Value: setTestProfile}
		w := postForm(client.Handler(), "/mydata/erase", url.Values{"kind": {"everything"}, "confirm": {"yes"}}, cookie)
		forgotten := false
		for _, c := range w.Result().Cookies() {
			forgotten = forgotten || (c.Name == profileCookie && c.MaxAge < 0)
		}
		if w.Code != http.StatusSeeOther || w.Header().Get("Location") != "/mydata?erased=everything#mydata-kinds-title" || !forgotten {
			t.Errorf("erase everything status %d location %q; want the profile cookie expired", w.Code, w.Header().Get("Location"))
		}
	})
}
