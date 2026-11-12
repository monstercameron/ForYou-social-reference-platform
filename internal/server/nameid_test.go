package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/color"
	"image/gif"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"foryou.local/foryou/internal/names"
)

var nameidClock = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

// nameidPixelBase64 is a 68-byte, 1x1 PNG. Its digest and size are written out in the tests.
const nameidPixelBase64 = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII="

const nameidPixelSHA256 = "63ef318d96b5d0d0ceba6e04a4e622b1158335cdc67c49e27839132c6f655058"

// nameidFake answers the display name routes the way a provider does.
type nameidFake struct {
	mu       sync.Mutex
	requests int
	current  []byte
	answer   func(attempt int) (int, string)
	posts    [][]byte
}

func (f *nameidFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests++
	switch {
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/names/"):
		if f.current == nil {
			nameidReply(w, http.StatusNotFound, `{"status":"not_found"}`)
			return
		}
		nameidReply(w, http.StatusOK, string(f.current))
	case r.Method == http.MethodPost && r.URL.Path == "/v1/names":
		body, _ := io.ReadAll(r.Body)
		f.posts = append(f.posts, body)
		if f.answer != nil {
			status, reply := f.answer(len(f.posts))
			if status == 0 {
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					conn.Close()
				}
				return
			}
			if status >= http.StatusBadRequest {
				nameidReply(w, status, reply)
				return
			}
		}
		nameidReply(w, http.StatusCreated, string(body))
	default:
		http.NotFound(w, r)
	}
}

func (f *nameidFake) sent() [][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]byte(nil), f.posts...)
}

func nameidReply(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func nameidProvider(t *testing.T, label string, fake *nameidFake) Endpoint {
	t.Helper()
	server := httptest.NewServer(fake)
	t.Cleanup(server.Close)
	return Endpoint{URL: server.URL, ServiceID: identServiceDID(t), Label: label}
}

// nameidSetup returns a dev client with the given providers, a reader's cookie and the DID of the key it created.
func nameidSetup(t *testing.T, providers ...Endpoint) (*Client, *http.Cookie, string) {
	t.Helper()
	client, err := NewClient(Config{DataDir: t.TempDir(), Dev: true, Providers: providers, Now: func() time.Time { return nameidClock }})
	if err != nil {
		t.Fatal(err)
	}
	cookie := identReader(t, client.Handler())
	did := identCreateKey(t, client.Handler(), cookie)
	return client, cookie, did
}

// nameidHTMLeXGet fetches path the way HTMLeX does, with Sec-Fetch-Dest set.
func nameidHTMLeXGet(h http.Handler, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Header.Set("Sec-Fetch-Dest", "empty")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// nameidHTMLeX posts form the way HTMLeX does, with Sec-Fetch-Dest set.
func nameidHTMLeX(h http.Handler, path string, form url.Values, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Sec-Fetch-Dest", "empty")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

// nameidSigned returns a record the profile's key signed, for seeding a provider or the profile.
func nameidSigned(t *testing.T, client *Client, cookie *http.Cookie, name *string, issued time.Time, previous *string) names.NameRecord {
	t.Helper()
	key, err := client.app.keystore().PrivateKey(cookie.Value)
	if err != nil {
		t.Fatal(err)
	}
	record, err := names.Build(key, name, nil, issued, previous)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func nameidEncoded(t *testing.T, record names.NameRecord) []byte {
	t.Helper()
	encoded, err := names.Encode(record)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func nameidParsed(t *testing.T, body []byte) names.NameRecord {
	t.Helper()
	record, err := names.Parse(body)
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func nameidString(s string) *string { return &s }

// nameidAnimatedGIF returns a two-frame GIF, which the avatar check refuses as animated.
func nameidAnimatedGIF(t *testing.T) []byte {
	t.Helper()
	frame := image.NewPaletted(image.Rect(0, 0, 1, 1), color.Palette{color.Black})
	var buf bytes.Buffer
	if err := gif.EncodeAll(&buf, &gif.GIF{Image: []*image.Paletted{frame, frame}, Delay: []int{10, 10}}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestNameIDPage(t *testing.T) {
	t.Run("without a key the page shows the empty state and no form", func(t *testing.T) {
		client, err := NewClient(Config{DataDir: t.TempDir(), Dev: true, Now: func() time.Time { return nameidClock }})
		if err != nil {
			t.Fatal(err)
		}
		h := client.Handler()
		body := getWith(h, "/identity/name", identReader(t, h))
		if !strings.Contains(body, "You need a signing key before you can name it.") || strings.Contains(body, `id="nameid-name"`) {
			t.Error("a profile without a key should see the empty state and no name form")
		}
	})
	t.Run("publishing without a key is refused with 409", func(t *testing.T) {
		client, err := NewClient(Config{DataDir: t.TempDir(), Dev: true, Now: func() time.Time { return nameidClock }})
		if err != nil {
			t.Fatal(err)
		}
		h := client.Handler()
		cookie := identReader(t, h)
		w := postForm(h, "/identity/name/publish", url.Values{"name": {"Ada"}, "understood": {"yes"}}, cookie)
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "You need a signing key before you can name it.") {
			t.Errorf("publish without a key = %d; want 409 with the empty-state message", w.Code)
		}
	})
	t.Run("with a key the page shows the form and the fingerprint", func(t *testing.T) {
		client, cookie, did := nameidSetup(t)
		body := getWith(client.Handler(), "/identity/name", cookie)
		fingerprint := names.Fingerprint(did)
		if !strings.Contains(body, `id="nameid-name"`) || !strings.Contains(body, fingerprint) || !strings.Contains(body, "Your key fingerprint") {
			t.Error("a profile with a key should see the form and its fingerprint")
		}
		if !strings.Contains(body, "You have no display name. Readers see your key fingerprint.") {
			t.Error("a profile with no name should see the empty state for its display name")
		}
	})
	t.Run("the identity page links to the display name page", func(t *testing.T) {
		client, cookie, _ := nameidSetup(t)
		if !strings.Contains(getWith(client.Handler(), "/identity", cookie), `href="/identity/name"`) {
			t.Error("the identity page should link to /identity/name")
		}
	})
}

func TestNameIDCheck(t *testing.T) {
	client, cookie, _ := nameidSetup(t)
	h := client.Handler()
	t.Run("a combining mark previews the composed name and says it will be saved so", func(t *testing.T) {
		w := nameidHTMLeX(h, "/identity/name/check", url.Values{"name": {"Zoë"}}, cookie)
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, "Zoë") || !strings.Contains(body, "Will be saved as") {
			t.Error("the preview should show Zoë and say that it is saved as the composed form")
		}
	})
	t.Run("two spaces in a row are refused and the error is tied to the input", func(t *testing.T) {
		w := postForm(h, "/identity/name/check", url.Values{"name": {"Ada  B"}}, cookie)
		body := w.Body.String()
		if !strings.Contains(body, "Contains two spaces in a row") || !strings.Contains(body, `aria-describedby="nameid-name-help nameid-name-error"`) || !strings.Contains(body, `id="nameid-name-error"`) {
			t.Error("the two-spaces rule should be named and linked to the input with aria-describedby")
		}
	})
	t.Run("sixty-five characters show the length error", func(t *testing.T) {
		w := nameidHTMLeX(h, "/identity/name/check", url.Values{"name": {strings.Repeat("a", 65)}}, cookie)
		if !strings.Contains(w.Body.String(), "Too long: 65 of 64 characters") {
			t.Error("a 65 character name should report 65 of 64 characters")
		}
	})
	t.Run("markup in a name is escaped inside bdi", func(t *testing.T) {
		w := nameidHTMLeX(h, "/identity/name/check", url.Values{"name": {"<b>x</b>"}}, cookie)
		if !strings.Contains(w.Body.String(), `<bdi class="nameid-name">&lt;b&gt;x&lt;/b&gt;</bdi>`) {
			t.Error("markup in a name should be escaped inside bdi")
		}
	})
	t.Run("without HTMLeX the check answers with the full page", func(t *testing.T) {
		full := postForm(h, "/identity/name/check", url.Values{"name": {"Ada"}}, cookie)
		fragment := nameidHTMLeX(h, "/identity/name/check", url.Values{"name": {"Ada"}}, cookie)
		if !strings.Contains(full.Body.String(), "<!doctype html>") || strings.Contains(fragment.Body.String(), "<!doctype html>") {
			t.Error("the same route should return the full page without HTMLeX and only the fragment with it")
		}
	})
}

func TestNameIDLocal(t *testing.T) {
	fake := &nameidFake{}
	client, cookie, _ := nameidSetup(t, nameidProvider(t, "Alpha", fake))
	h := client.Handler()
	profileFile := filepath.Join(client.app.profiles.root, cookie.Value, nameidFileName)
	t.Run("keeping a name writes the profile file and sends nothing to a provider", func(t *testing.T) {
		w := postForm(h, "/identity/name/local", url.Values{"name": {"Ada"}}, cookie)
		data, err := os.ReadFile(profileFile)
		if err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK || !strings.Contains(string(data), `"local":"Ada"`) || fake.requests != 0 {
			t.Errorf("keep = %d with file %q and %d provider requests; want 200, the local name saved and no provider contacted", w.Code, data, fake.requests)
		}
	})
	t.Run("clearing removes the local name", func(t *testing.T) {
		postForm(h, "/identity/name/local/clear", url.Values{}, cookie)
		data, err := os.ReadFile(profileFile)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), `"local"`) || fake.requests != 0 {
			t.Errorf("after clearing the file is %q; want no local name", data)
		}
	})
	t.Run("a cross-site post is refused", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodPost, "/identity/name/local", strings.NewReader("name=Ada"))
		r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		r.Header.Set("Sec-Fetch-Site", "cross-site")
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusForbidden {
			t.Errorf("cross-site keep = %d; want 403", w.Code)
		}
	})
}

func TestNameIDPublish(t *testing.T) {
	t.Run("two providers answer: one publishes, one is not ready yet", func(t *testing.T) {
		accept := &nameidFake{}
		notReady := &nameidFake{answer: func(int) (int, string) { return http.StatusConflict, `{"status":"not_ready"}` }}
		client, cookie, did := nameidSetup(t, nameidProvider(t, "Alpha", accept), nameidProvider(t, "Beta", notReady))
		w := postForm(client.Handler(), "/identity/name/publish", url.Values{"name": {"Ada"}, "understood": {"yes"}}, cookie)
		body := w.Body.String()
		if w.Code != http.StatusOK || !strings.Contains(body, `<span>Published</span>`) || !strings.Contains(body, `<span class="nameid-provider">Beta</span> <span>Not accepted yet: this provider needs at least one of your posts first.</span>`) {
			t.Fatalf("publish = %d; want the Alpha row published and the Beta row not accepted yet", w.Code)
		}
		record := nameidParsed(t, accept.sent()[0])
		if err := names.Verify(record, nameidClock); err != nil {
			t.Fatalf("record does not verify: %v", err)
		}
		if record.AuthorID != did || record.PreviousNameID != nil || record.DisplayName == nil || *record.DisplayName != "Ada" {
			t.Error("the first record should be Ada, signed by the profile's key, with no previous name")
		}
	})
	t.Run("publishing without the checkbox is refused and the error is tied to it", func(t *testing.T) {
		fake := &nameidFake{}
		client, cookie, _ := nameidSetup(t, nameidProvider(t, "Alpha", fake))
		w := postForm(client.Handler(), "/identity/name/publish", url.Values{"name": {"Ada"}}, cookie)
		body := w.Body.String()
		if w.Code != http.StatusBadRequest || !strings.Contains(body, "Tick the box to confirm that a published name is public.") || !strings.Contains(body, `aria-describedby="nameid-understood-error"`) || len(fake.sent()) != 0 {
			t.Error("publish without the checkbox should be a 400 that names the checkbox and sends nothing")
		}
	})
	t.Run("a publish without the checkbox from HTMLeX ties the error to the checkbox in the form", func(t *testing.T) {
		client, cookie, _ := nameidSetup(t, nameidProvider(t, "Alpha", &nameidFake{}))
		w := nameidHTMLeX(client.Handler(), "/identity/name/publish", url.Values{"name": {"Ada"}}, cookie)
		if !strings.Contains(w.Body.String(), `<section id="nameid-form"`) || !strings.Contains(w.Body.String(), `aria-describedby="nameid-understood-error"`) {
			t.Error("the HTMLeX answer should refresh the form with the checkbox error tied to it")
		}
	})
	t.Run("after an HTMLeX publish the answer carries the withdraw form", func(t *testing.T) {
		client, cookie, _ := nameidSetup(t, nameidProvider(t, "Alpha", &nameidFake{}))
		w := nameidHTMLeX(client.Handler(), "/identity/name/publish", url.Values{"name": {"Ada"}, "understood": {"yes"}}, cookie)
		if !strings.Contains(w.Body.String(), `action="/identity/name/withdraw"`) {
			t.Error("a publish that sets a name should refresh the withdraw form in the same HTMLeX answer")
		}
	})
	t.Run("an unconfirmed HTMLeX withdrawal keeps the published name in the form", func(t *testing.T) {
		client, cookie, _ := nameidSetup(t, nameidProvider(t, "Alpha", &nameidFake{}))
		h := client.Handler()
		postForm(h, "/identity/name/publish", url.Values{"name": {"Ada"}, "understood": {"yes"}}, cookie)
		w := nameidHTMLeX(h, "/identity/name/withdraw", url.Values{}, cookie)
		if !strings.Contains(w.Body.String(), "Tick the box to confirm the withdrawal.") || !strings.Contains(w.Body.String(), `name="name" type="text" value="Ada"`) {
			t.Error("a refused withdrawal should name the checkbox and leave the form holding the published name")
		}
	})
	t.Run("a publish that no provider accepts leaves no change warning on the next page", func(t *testing.T) {
		refused := &nameidFake{answer: func(int) (int, string) { return http.StatusConflict, `{"status":"not_ready"}` }}
		client, cookie, _ := nameidSetup(t, nameidProvider(t, "Alpha", refused))
		h := client.Handler()
		postForm(h, "/identity/name/publish", url.Values{"name": {"Ada"}, "understood": {"yes"}}, cookie)
		if strings.Contains(getWith(h, "/identity/name", cookie), "You changed your name less than a day ago") {
			t.Error("a record no provider accepted should not warn that the name was changed")
		}
	})
	t.Run("a later record links to the record the provider holds and is issued after it", func(t *testing.T) {
		fake := &nameidFake{}
		client, cookie, _ := nameidSetup(t, nameidProvider(t, "Alpha", fake))
		previous := nameidSigned(t, client, cookie, nameidString("Ada"), nameidClock.Add(-time.Hour), nil)
		fake.current = nameidEncoded(t, previous)
		postForm(client.Handler(), "/identity/name/publish", url.Values{"name": {"Ada Lovelace"}, "understood": {"yes"}}, cookie)
		record := nameidParsed(t, fake.sent()[0])
		if record.PreviousNameID == nil || *record.PreviousNameID != previous.NameID || !names.Newer(record, previous) {
			t.Error("the new record should name the held record as its predecessor and be selected over it")
		}
	})
	t.Run("a rate limit is shown as the wait in hours and minutes", func(t *testing.T) {
		fake := &nameidFake{answer: func(int) (int, string) {
			return http.StatusTooManyRequests, `{"retry_after_seconds":5400,"status":"rate_limited"}`
		}}
		client, cookie, _ := nameidSetup(t, nameidProvider(t, "Alpha", fake))
		w := postForm(client.Handler(), "/identity/name/publish", url.Values{"name": {"Ada"}, "understood": {"yes"}}, cookie)
		if !strings.Contains(w.Body.String(), "Name changes are limited here. Try again in 1 h 30 min.") {
			t.Error("a 5400 second wait should read as 1 h 30 min")
		}
	})
	t.Run("an answer that never arrives is unknown, and retry resends the same bytes", func(t *testing.T) {
		fake := &nameidFake{answer: func(attempt int) (int, string) {
			if attempt == 1 {
				return 0, ""
			}
			return http.StatusCreated, ""
		}}
		client, cookie, _ := nameidSetup(t, nameidProvider(t, "Alpha", fake))
		h := client.Handler()
		first := postForm(h, "/identity/name/publish", url.Values{"name": {"Ada"}, "understood": {"yes"}}, cookie)
		if !strings.Contains(first.Body.String(), "Unknown result: the provider did not answer in time.") || !strings.Contains(first.Body.String(), `action="/identity/name/retry"`) {
			t.Fatal("an unanswered publish should be unknown and offer a retry")
		}
		retry := postForm(h, "/identity/name/retry", url.Values{}, cookie)
		sent := fake.sent()
		if len(sent) != 2 || !bytes.Equal(sent[0], sent[1]) || !strings.Contains(retry.Body.String(), "<span>Published</span>") {
			t.Error("retry should resend the byte-identical record and report the provider's answer")
		}
	})
	t.Run("a withdrawal sends a null display name and falls back to the stored record", func(t *testing.T) {
		fake := &nameidFake{}
		client, cookie, _ := nameidSetup(t, nameidProvider(t, "Alpha", fake))
		h := client.Handler()
		postForm(h, "/identity/name/publish", url.Values{"name": {"Ada"}, "understood": {"yes"}}, cookie)
		w := postForm(h, "/identity/name/withdraw", url.Values{"withdraw": {"yes"}}, cookie)
		sent := fake.sent()
		if w.Code != http.StatusOK || len(sent) != 2 {
			t.Fatalf("withdraw = %d with %d sent records; want 200 and a second record", w.Code, len(sent))
		}
		withdrawal := nameidParsed(t, sent[1])
		if err := names.Verify(withdrawal, nameidClock); err != nil {
			t.Fatalf("withdrawal does not verify: %v", err)
		}
		if withdrawal.DisplayName != nil || withdrawal.Avatar != nil || withdrawal.PreviousNameID == nil {
			t.Error("the withdrawal should carry a null name and avatar and name the record it replaces")
		}
	})
}

func TestNameIDStatus(t *testing.T) {
	older := &nameidFake{}
	withdrawn := &nameidFake{}
	client, cookie, _ := nameidSetup(t, nameidProvider(t, "Alpha", older), nameidProvider(t, "Beta", withdrawn))
	previous := nameidSigned(t, client, cookie, nameidString("Ada"), nameidClock.Add(-time.Hour), nil)
	withdrawal := nameidSigned(t, client, cookie, nil, nameidClock, nameidString(previous.NameID))
	older.current = nameidEncoded(t, previous)
	withdrawn.current = nameidEncoded(t, withdrawal)
	t.Run("the first provider is shown as holding an older name and the second as holding the withdrawal", func(t *testing.T) {
		w := nameidHTMLeXGet(client.Handler(), "/identity/name/status", cookie)
		body := w.Body.String()
		if !strings.Contains(body, `<span class="nameid-provider">Alpha</span> <span>Holds an older name</span>`) || !strings.Contains(body, `<span class="nameid-provider">Beta</span> <span>Holds your withdrawal</span>`) {
			t.Error("the status should compare each provider with the selected withdrawal")
		}
		if !strings.Contains(body, "Selected record: Withdrawn") {
			t.Error("the status should name the selected record as the withdrawal")
		}
	})
}

func TestNameIDAvatar(t *testing.T) {
	pixel, err := base64.StdEncoding.DecodeString(nameidPixelBase64)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/avatar.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(pixel)
	})
	mux.HandleFunc("/big", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, names.MaxAvatarBytes+1))
	})
	mux.HandleFunc("/text", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("hello"))
	})
	animated := nameidAnimatedGIF(t)
	mux.HandleFunc("/animated.gif", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/gif")
		_, _ = w.Write(animated)
	})
	host := httptest.NewTLSServer(mux)
	t.Cleanup(host.Close)
	transport := host.Client().Transport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		var dialer net.Dialer
		return dialer.DialContext(ctx, network, host.Listener.Addr().String())
	}
	client := &http.Client{Transport: transport}
	original := nameidAvatarClient
	nameidAvatarClient = func() *http.Client { return client }
	t.Cleanup(func() { nameidAvatarClient = original })

	fake := &nameidFake{}
	app, cookie, _ := nameidSetup(t, nameidProvider(t, "Alpha", fake))
	h := app.Handler()
	t.Run("a checked picture is the one the record commits to", func(t *testing.T) {
		check := postForm(h, "/identity/name/avatar", url.Values{"name": {"Ada"}, "avatar": {"https://example.com/avatar.png"}}, cookie)
		if !strings.Contains(check.Body.String(), `name="avatar_sha256" value="`+nameidPixelSHA256+`"`) || !strings.Contains(check.Body.String(), `name="avatar_size" value="68"`) {
			t.Fatal("the check should show the digest and size of the exact bytes")
		}
		postForm(h, "/identity/name/publish", url.Values{
			"name": {"Ada"}, "understood": {"yes"}, "avatar": {"https://example.com/avatar.png"},
			"avatar_checked": {"https://example.com/avatar.png"}, "avatar_sha256": {nameidPixelSHA256}, "avatar_size": {"68"},
		}, cookie)
		record := nameidParsed(t, fake.sent()[0])
		if record.Avatar == nil || record.Avatar.SHA256 != nameidPixelSHA256 || record.Avatar.SizeBytes != 68 || record.Avatar.URL != "https://example.com/avatar.png" {
			t.Errorf("the record should commit to the checked picture, got %+v", record.Avatar)
		}
	})
	t.Run("the page keeps the signed picture so the next publish does not drop it", func(t *testing.T) {
		body := getWith(h, "/identity/name", cookie)
		if !strings.Contains(body, `value="https://example.com/avatar.png"`) || !strings.Contains(body, `name="avatar_sha256" value="`+nameidPixelSHA256+`"`) || !strings.Contains(body, "Signed picture, 68 bytes.") {
			t.Error("after publishing with a picture the form should hold that picture's address and digest")
		}
	})
	t.Run("clearing the picture field publishes a null avatar", func(t *testing.T) {
		postForm(h, "/identity/name/publish", url.Values{"name": {"Ada"}, "understood": {"yes"}}, cookie)
		sent := fake.sent()
		if nameidParsed(t, sent[len(sent)-1]).Avatar != nil {
			t.Error("a publish without a picture address should carry a null avatar")
		}
	})
	t.Run("an oversize picture is refused", func(t *testing.T) {
		w := postForm(h, "/identity/name/avatar", url.Values{"name": {"Ada"}, "avatar": {"https://example.com/big"}}, cookie)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "The picture is larger than 1048576 bytes.") {
			t.Error("a picture over 1 MiB should be refused with its size limit")
		}
	})
	t.Run("a file that is not an image is refused", func(t *testing.T) {
		w := postForm(h, "/identity/name/avatar", url.Values{"name": {"Ada"}, "avatar": {"https://example.com/text"}}, cookie)
		if !strings.Contains(w.Body.String(), "This address does not serve a PNG, JPEG, GIF or WebP picture.") {
			t.Error("a text response should be refused as not a picture")
		}
	})
	t.Run("an animated picture is refused", func(t *testing.T) {
		w := postForm(h, "/identity/name/avatar", url.Values{"name": {"Ada"}, "avatar": {"https://example.com/animated.gif"}}, cookie)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "This picture is animated. Choose a still picture.") {
			t.Error("a two-frame GIF should be refused as animated with a 400")
		}
	})
	t.Run("an http address is refused", func(t *testing.T) {
		w := postForm(h, "/identity/name/avatar", url.Values{"name": {"Ada"}, "avatar": {"http://example.com/avatar.png"}}, cookie)
		if !strings.Contains(w.Body.String(), "The picture address must start with https://.") {
			t.Error("an http address should be refused")
		}
	})
}
