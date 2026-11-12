package server

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/succession"
	"foryou.local/foryou/internal/wire"
)

var identNow = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

const identPassphrase = "correct horse battery"

// identFakeProvider answers succession reads and writes the way a provider does.
type identFakeProvider struct {
	mu          sync.Mutex
	unavailable bool
	held        []wire.KeySuccession
	published   []wire.KeySuccession
}

func (f *identFakeProvider) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unavailable {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"retry_after_seconds":30,"status":"unavailable"}`))
		return
	}
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/v1/successions":
		body, _ := io.ReadAll(r.Body)
		var s wire.KeySuccession
		_ = json.Unmarshal(body, &s)
		f.published = append(f.published, s)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write(body)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/successions/"):
		held := f.held
		if held == nil {
			held = []wire.KeySuccession{}
		}
		encoded, err := canon.Encode(struct {
			Successions []wire.KeySuccession `json:"successions"`
		}{held})
		if err != nil {
			http.Error(w, "encode", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(encoded)
	default:
		http.NotFound(w, r)
	}
}

func (f *identFakeProvider) publishedCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.published)
}

func identProvider(t *testing.T, label string, fake *identFakeProvider) Endpoint {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	return Endpoint{URL: server.URL, ServiceID: identServiceDID(t), Label: label}
}

func identServiceDID(t *testing.T) string {
	t.Helper()
	key, err := keys.NewKeyFromSeed(bytes.Repeat([]byte{9}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return keys.DIDOf(key)
}

func identApp(t *testing.T, dev bool, providers ...Endpoint) *Client {
	t.Helper()
	client, err := NewClient(Config{DataDir: t.TempDir(), Dev: dev, Providers: providers, Now: func() time.Time { return identNow }})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// identReader returns a new reader's profile cookie, which the first identity page visit sets.
func identReader(t *testing.T, h http.Handler) *http.Cookie {
	t.Helper()
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/identity", nil))
	for _, cookie := range w.Result().Cookies() {
		if cookie.Name == profileCookie {
			return cookie
		}
	}
	t.Fatal("identity page did not set a profile cookie")
	return nil
}

func identCreateKey(t *testing.T, h http.Handler, cookie *http.Cookie) string {
	t.Helper()
	if w := postForm(h, "/identity/create", url.Values{}, cookie); w.Code != http.StatusOK {
		t.Fatalf("create key status = %d", w.Code)
	}
	return identDIDOnPage(t, getWith(h, "/identity", cookie))
}

func identDIDOnPage(t *testing.T, body string) string {
	t.Helper()
	_, after, found := strings.Cut(body, `class="identity-did" type="text" value="`)
	did, _, closed := strings.Cut(after, `"`)
	if !found || !closed {
		t.Fatal("page does not show a did:key")
	}
	return did
}

func identDownload(t *testing.T, h http.Handler, cookie *http.Cookie, passphrase string) []byte {
	t.Helper()
	w := postForm(h, "/identity/backup", url.Values{"passphrase": {passphrase}, "confirm": {passphrase}}, cookie)
	if w.Code != http.StatusOK {
		t.Fatalf("backup status = %d", w.Code)
	}
	return w.Body.Bytes()
}

// identUpload posts a backup file and passphrase the way the file form does.
func identUpload(t *testing.T, h http.Handler, path string, cookie *http.Cookie, file []byte, passphrase string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	name := "backup.json"
	if file == nil {
		name = ""
	}
	part, err := writer.CreateFormFile("backup", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(file); err != nil {
		t.Fatal(err)
	}
	if err := writer.WriteField("passphrase", passphrase); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, path, &body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestIdentityCreate(t *testing.T) {
	t.Run("fresh profile sees the empty state", func(t *testing.T) {
		h := identApp(t, false).Handler()
		body := getWith(h, "/identity")
		if !strings.Contains(body, "<h1>Identity and keys</h1>") || !strings.Contains(body, "You do not have a signing key yet.") || !strings.Contains(body, `action="/identity/create"`) {
			t.Error("empty identity state missing its heading, line or create form")
		}
	})
	t.Run("create answers with a did:key page", func(t *testing.T) {
		h := identApp(t, false).Handler()
		cookie := identReader(t, h)
		w := postForm(h, "/identity/create", url.Values{}, cookie)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "did:key:z6Mk") || !strings.Contains(w.Body.String(), "<!doctype html>") {
			t.Errorf("create status = %d; want the page showing a did:key:z6Mk identity", w.Code)
		}
	})
	t.Run("create fragment swaps the identity panels", func(t *testing.T) {
		h := identApp(t, false).Handler()
		cookie := identReader(t, h)
		w := postForm(h, "/identity/create/fragment", url.Values{}, cookie)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `id="identity-main"`) || !strings.Contains(w.Body.String(), "Before you publish") || strings.Contains(w.Body.String(), "<!doctype html>") {
			t.Errorf("create fragment status = %d; want the identity panels without a page frame", w.Code)
		}
	})
	t.Run("cross-origin create is refused", func(t *testing.T) {
		h := identApp(t, false).Handler()
		r := httptest.NewRequest(http.MethodPost, "/identity/create", strings.NewReader(""))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Origin", "https://attacker.example")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("cross-origin create status = %d; want 403", w.Code)
		}
	})
}

func TestIdentityAcknowledge(t *testing.T) {
	t.Run("without the checkbox the answer is 400 tied to it", func(t *testing.T) {
		h := identApp(t, false).Handler()
		cookie := identReader(t, h)
		identCreateKey(t, h, cookie)
		w := postForm(h, "/identity/acknowledge", url.Values{}, cookie)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `aria-describedby="identity-ack-error"`) {
			t.Errorf("unchecked acknowledge status = %d; want 400 with the error tied to the checkbox", w.Code)
		}
	})
	t.Run("with the checkbox the warnings are acknowledged and stored", func(t *testing.T) {
		client := identApp(t, false)
		h := client.Handler()
		cookie := identReader(t, h)
		identCreateKey(t, h, cookie)
		w := postForm(h, "/identity/acknowledge/fragment", url.Values{"understood": {"yes"}}, cookie)
		settings, err := client.app.settings(cookie.Value)
		if err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Acknowledged on 11 November 2026") {
			t.Errorf("acknowledge status = %d; want the acknowledged pill", w.Code)
		}
		if _, ok := settings.Acknowledgements[ackKeyWarnings]; !ok {
			t.Error("acknowledgement was not stored in profile settings")
		}
	})
}

func TestIdentityBackup(t *testing.T) {
	t.Run("an eleven-character passphrase is rejected", func(t *testing.T) {
		h := identApp(t, false).Handler()
		cookie := identReader(t, h)
		identCreateKey(t, h, cookie)
		w := postForm(h, "/identity/backup", url.Values{"passphrase": {"elevenchars"}, "confirm": {"elevenchars"}}, cookie)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "at least 12 characters") {
			t.Errorf("short passphrase status = %d; want 400 naming the minimum", w.Code)
		}
	})
	t.Run("a valid passphrase downloads an encrypted backup without the seed", func(t *testing.T) {
		h := identApp(t, false).Handler()
		cookie := identReader(t, h)
		identCreateKey(t, h, cookie)
		w := postForm(h, "/identity/backup", url.Values{"passphrase": {identPassphrase}, "confirm": {identPassphrase}}, cookie)
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/json" || !strings.HasPrefix(w.Header().Get("Content-Disposition"), "attachment; filename=\"foryou-key-z6Mk") {
			t.Fatalf("backup status = %d headers %v; want a JSON attachment", w.Code, w.Header())
		}
		if !strings.Contains(w.Body.String(), `"format":"foryou-key-backup-1"`) || strings.Contains(w.Body.String(), "seed_b64url") {
			t.Error("backup must name its format and must not carry a plaintext seed")
		}
	})
	t.Run("checking the right passphrase marks the backup checked", func(t *testing.T) {
		h := identApp(t, false).Handler()
		cookie := identReader(t, h)
		identCreateKey(t, h, cookie)
		file := identDownload(t, h, cookie, identPassphrase)
		w := identUpload(t, h, "/identity/backup/check", cookie, file, identPassphrase)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Backup opens and matches") {
			t.Fatalf("check status = %d; want the backup to open", w.Code)
		}
		if !strings.Contains(getWith(h, "/identity", cookie), "Backup checked on ") {
			t.Error("the identity page does not show the backup as checked")
		}
	})
	t.Run("checking with the wrong passphrase shows that it cannot be opened", func(t *testing.T) {
		h := identApp(t, false).Handler()
		cookie := identReader(t, h)
		identCreateKey(t, h, cookie)
		file := identDownload(t, h, cookie, identPassphrase)
		w := identUpload(t, h, "/identity/backup/check/fragment", cookie, file, "a different passphrase")
		if !strings.Contains(w.Body.String(), "cannot be opened with that passphrase") {
			t.Errorf("wrong passphrase status = %d; want the cannot-open message", w.Code)
		}
	})
	t.Run("checking with no file chosen asks for one", func(t *testing.T) {
		h := identApp(t, false).Handler()
		cookie := identReader(t, h)
		identCreateKey(t, h, cookie)
		w := identUpload(t, h, "/identity/backup/check/fragment", cookie, nil, identPassphrase)
		if !strings.Contains(w.Body.String(), "Choose a backup file.") {
			t.Errorf("empty file status = %d; want the choose-a-file message", w.Code)
		}
	})
	t.Run("a file over the upload limit is named as too large", func(t *testing.T) {
		h := identApp(t, false).Handler()
		cookie := identReader(t, h)
		identCreateKey(t, h, cookie)
		w := identUpload(t, h, "/identity/backup/check/fragment", cookie, bytes.Repeat([]byte("a"), 2*identBackupLimit), identPassphrase)
		if !strings.Contains(w.Body.String(), "This file is too large to be a key backup.") {
			t.Errorf("oversized file status = %d; want the too-large message", w.Code)
		}
	})
}

func TestIdentityRotate(t *testing.T) {
	t.Run("rotation publishes one succession per provider and reports a failing one", func(t *testing.T) {
		first, second := &identFakeProvider{}, &identFakeProvider{unavailable: true}
		client := identApp(t, true, identProvider(t, "First provider", first), identProvider(t, "Second provider", second))
		h := client.Handler()
		cookie := identReader(t, h)
		oldDID := identCreateKey(t, h, cookie)
		w := postForm(h, "/identity/rotate/fragment", url.Values{"confirm": {"yes"}}, cookie)
		page := getWith(h, "/identity", cookie)
		newDID := identDIDOnPage(t, page)
		if w.Code != http.StatusOK || newDID == oldDID || !strings.Contains(w.Body.String(), `First provider</span><span class="pill pill-ok">Published</span>`) || !strings.Contains(w.Body.String(), `Second provider</span><span class="pill pill-warn">Not published: unavailable</span>`) {
			t.Fatalf("rotate status = %d; want a new key and per-provider results", w.Code)
		}
		first.mu.Lock()
		published := first.published
		first.mu.Unlock()
		if len(published) != 1 || published[0].OldID != oldDID || published[0].NewID != newDID || succession.Verify(published[0]) != nil {
			t.Errorf("first provider received %+v; want one valid succession from %s to %s", published, oldDID, newDID)
		}
		if !strings.Contains(page, "Earlier keys") || !strings.Contains(page, `<span class="id-chip">`+oldDID+`</span>`) {
			t.Error("the rotated page does not list the old key under earlier keys")
		}
		if second.publishedCount() != 0 {
			t.Error("the unavailable provider should have stored nothing")
		}
	})
	t.Run("rotation without the checkbox is refused and publishes nothing", func(t *testing.T) {
		fake := &identFakeProvider{}
		h := identApp(t, true, identProvider(t, "Provider", fake)).Handler()
		cookie := identReader(t, h)
		identCreateKey(t, h, cookie)
		w := postForm(h, "/identity/rotate", url.Values{}, cookie)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `aria-describedby="identity-rotate-error"`) || fake.publishedCount() != 0 {
			t.Errorf("unchecked rotate status = %d; want 400 and no publication", w.Code)
		}
	})
}

func TestIdentitySuccessions(t *testing.T) {
	t.Run("two successions to different keys show a conflict", func(t *testing.T) {
		fake := &identFakeProvider{}
		client := identApp(t, true, identProvider(t, "Provider", fake))
		h := client.Handler()
		cookie := identReader(t, h)
		identCreateKey(t, h, cookie)
		oldKey, err := client.app.keystore().PrivateKey(cookie.Value)
		if err != nil {
			t.Fatal(err)
		}
		fake.mu.Lock()
		fake.held = []wire.KeySuccession{identSuccessionTo(t, oldKey, 1), identSuccessionTo(t, oldKey, 2)}
		fake.mu.Unlock()
		body := getWith(h, "/identity?check=1", cookie)
		if !strings.Contains(body, "Conflict:") || !strings.Contains(body, "Readers will not follow either.") {
			t.Error("the page should report the conflicting successions")
		}
	})
	t.Run("one successor shows the link", func(t *testing.T) {
		fake := &identFakeProvider{}
		client := identApp(t, true, identProvider(t, "Provider", fake))
		h := client.Handler()
		cookie := identReader(t, h)
		identCreateKey(t, h, cookie)
		oldKey, err := client.app.keystore().PrivateKey(cookie.Value)
		if err != nil {
			t.Fatal(err)
		}
		fake.mu.Lock()
		fake.held = []wire.KeySuccession{identSuccessionTo(t, oldKey, 3)}
		fake.mu.Unlock()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, identWithCookie(httptest.NewRequest(http.MethodGet, "/identity/successions", nil), cookie))
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Linked: ") || !strings.Contains(w.Body.String(), "asserted by the old key") || strings.Contains(w.Body.String(), "<!doctype html>") {
			t.Errorf("successions fragment status = %d; want a linked result without a page frame", w.Code)
		}
	})
	t.Run("no selected provider says that nothing was checked", func(t *testing.T) {
		h := identApp(t, true).Handler()
		cookie := identReader(t, h)
		identCreateKey(t, h, cookie)
		body := getWith(h, "/identity?check=1", cookie)
		if !strings.Contains(body, "No provider is selected, so no successions were checked.") || strings.Contains(body, "No succession published for") {
			t.Error("with no provider selected the page must not claim that successions were checked")
		}
	})
	t.Run("a provider that does not answer is not counted as holding nothing", func(t *testing.T) {
		h := identApp(t, true, identProvider(t, "Provider", &identFakeProvider{unavailable: true})).Handler()
		cookie := identReader(t, h)
		did := identCreateKey(t, h, cookie)
		body := getWith(h, "/identity?check=1", cookie)
		if !strings.Contains(body, "No selected provider answered for "+did+", so nothing is known about its successions.") || !strings.Contains(body, "Provider: unavailable") || strings.Contains(body, "No succession published for") {
			t.Error("an unavailable provider must be reported as not answering, not as holding no succession")
		}
	})
	t.Run("checking successions without a key is refused", func(t *testing.T) {
		h := identApp(t, false).Handler()
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/identity/successions", nil))
		if w.Code != http.StatusConflict {
			t.Errorf("successions without a key status = %d; want 409", w.Code)
		}
	})
}

func TestIdentityRestore(t *testing.T) {
	t.Run("a backup restores into a profile with no key", func(t *testing.T) {
		h := identApp(t, false).Handler()
		source := identReader(t, h)
		did := identCreateKey(t, h, source)
		file := identDownload(t, h, source, identPassphrase)
		target := identReader(t, h)
		w := identUpload(t, h, "/identity/restore/fragment", target, file, identPassphrase)
		if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "Restored key") || !strings.Contains(w.Body.String(), did) {
			t.Errorf("restore status = %d; want the restored DID", w.Code)
		}
	})
	t.Run("a wrong passphrase is refused", func(t *testing.T) {
		h := identApp(t, false).Handler()
		source := identReader(t, h)
		identCreateKey(t, h, source)
		file := identDownload(t, h, source, identPassphrase)
		target := identReader(t, h)
		w := identUpload(t, h, "/identity/restore", target, file, "a different passphrase")
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "cannot be opened with that passphrase") {
			t.Errorf("wrong passphrase restore status = %d; want 400 with the cannot-open message", w.Code)
		}
	})
	t.Run("restore into a profile that already has a key is refused", func(t *testing.T) {
		h := identApp(t, false).Handler()
		source := identReader(t, h)
		identCreateKey(t, h, source)
		file := identDownload(t, h, source, identPassphrase)
		w := identUpload(t, h, "/identity/restore", source, file, identPassphrase)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "already has a key") {
			t.Errorf("restore over a key status = %d; want 400", w.Code)
		}
	})
}

func TestIdentityDevCustody(t *testing.T) {
	t.Run("dev mode states the custody on the page", func(t *testing.T) {
		if !strings.Contains(getWith(identApp(t, true).Handler(), "/identity"), "Never use it for anything real.") {
			t.Error("dev identity page is missing the custody warning")
		}
	})
	t.Run("non-dev page omits the custody warning", func(t *testing.T) {
		if strings.Contains(getWith(identApp(t, false).Handler(), "/identity"), "Never use it for anything real.") {
			t.Error("non-dev identity page showed the dev custody warning")
		}
	})
}

// identSuccessionTo is a succession from oldKey to the key generated from seed.
func identSuccessionTo(t *testing.T, oldKey keys.PrivateKey, seed byte) wire.KeySuccession {
	t.Helper()
	next, err := keys.NewKeyFromSeed(bytes.Repeat([]byte{seed}, 32))
	if err != nil {
		t.Fatal(err)
	}
	s, err := succession.New(oldKey, next, identNow)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func identWithCookie(r *http.Request, cookie *http.Cookie) *http.Request {
	r.AddCookie(cookie)
	return r
}
