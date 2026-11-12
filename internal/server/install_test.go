package server

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func instGet(t *testing.T, path string, cookies ...*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, path, nil)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	w := httptest.NewRecorder()
	testClientHandler(t).ServeHTTP(w, r)
	return w
}

func instDecodePNG(t *testing.T, w *httptest.ResponseRecorder) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(w.Body.Bytes()))
	if err != nil {
		t.Fatalf("decode png: %v", err)
	}
	return img
}

func TestInstallManifest(t *testing.T) {
	t.Run("headers", func(t *testing.T) {
		w := instGet(t, "/manifest.webmanifest")
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "application/manifest+json" || w.Header().Get("Cache-Control") != "no-cache" || w.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("manifest status %d or headers %v", w.Code, w.Header())
		}
	})
	t.Run("fields", func(t *testing.T) {
		var m struct {
			StartURL string `json:"start_url"`
			Scope    string `json:"scope"`
			Display  string `json:"display"`
			Icons    []struct {
				Src     string `json:"src"`
				Purpose string `json:"purpose"`
			} `json:"icons"`
		}
		if err := json.Unmarshal(instGet(t, "/manifest.webmanifest").Body.Bytes(), &m); err != nil {
			t.Fatalf("manifest JSON: %v", err)
		}
		if m.StartURL != "/" || m.Scope != "/" || m.Display != "standalone" || len(m.Icons) != 4 {
			t.Fatalf("manifest start %q scope %q display %q with %d icons", m.StartURL, m.Scope, m.Display, len(m.Icons))
		}
		if m.Icons[3].Src != "/install/icon-maskable-512.png" || m.Icons[3].Purpose != "maskable" {
			t.Errorf("maskable icon %+v", m.Icons[3])
		}
	})
}

func TestInstallIcons(t *testing.T) {
	accent := color.NRGBA{0x47, 0x46, 0xdf, 0xff}
	white := color.NRGBA{0xff, 0xff, 0xff, 0xff}
	t.Run("any 192", func(t *testing.T) {
		w := instGet(t, "/install/icon-192.png")
		if w.Header().Get("Content-Type") != "image/png" {
			t.Fatalf("content type %q", w.Header().Get("Content-Type"))
		}
		img := instDecodePNG(t, w)
		if img.Bounds() != image.Rect(0, 0, 192, 192) {
			t.Fatalf("bounds %v", img.Bounds())
		}
		if got := color.NRGBAModel.Convert(img.At(0, 0)).(color.NRGBA); got.A != 0 {
			t.Errorf("corner alpha %d; want 0", got.A)
		}
		if got := color.NRGBAModel.Convert(img.At(71, 121)).(color.NRGBA); got != white {
			t.Errorf("glyph pixel %v; want white", got)
		}
		if got := color.NRGBAModel.Convert(img.At(121, 71)).(color.NRGBA); got != accent {
			t.Errorf("background pixel %v; want accent", got)
		}
	})
	t.Run("maskable 512", func(t *testing.T) {
		img := instDecodePNG(t, instGet(t, "/install/icon-maskable-512.png"))
		if img.Bounds() != image.Rect(0, 0, 512, 512) {
			t.Fatalf("bounds %v", img.Bounds())
		}
		if got := color.NRGBAModel.Convert(img.At(0, 0)).(color.NRGBA); got != accent {
			t.Errorf("corner pixel %v; want opaque accent", got)
		}
		if got := color.NRGBAModel.Convert(img.At(202, 309)).(color.NRGBA); got != white {
			t.Errorf("glyph pixel %v; want white", got)
		}
		if got := color.NRGBAModel.Convert(img.At(309, 202)).(color.NRGBA); got != accent {
			t.Errorf("background pixel %v; want accent", got)
		}
	})
	t.Run("apple touch 180", func(t *testing.T) {
		img := instDecodePNG(t, instGet(t, "/install/apple-touch-icon.png"))
		if img.Bounds() != image.Rect(0, 0, 180, 180) {
			t.Fatalf("bounds %v", img.Bounds())
		}
		if got := color.NRGBAModel.Convert(img.At(0, 0)).(color.NRGBA); got != accent {
			t.Errorf("corner pixel %v; want opaque accent", got)
		}
	})
	t.Run("svg", func(t *testing.T) {
		w := instGet(t, "/install/icon.svg")
		body := w.Body.String()
		if w.Header().Get("Content-Type") != "image/svg+xml" || !strings.Contains(body, `fill="#4746df"`) || strings.Contains(body, "<script") {
			t.Errorf("svg content type %q or body %q", w.Header().Get("Content-Type"), body)
		}
	})
}

func TestInstallServiceWorker(t *testing.T) {
	t.Run("served at root", func(t *testing.T) {
		w := instGet(t, "/sw.js")
		if w.Code != http.StatusOK || w.Header().Get("Content-Type") != "text/javascript; charset=utf-8" || w.Header().Get("Cache-Control") != "no-cache" {
			t.Errorf("service worker status %d or headers %v", w.Code, w.Header())
		}
	})
	t.Run("precaches only public assets", func(t *testing.T) {
		body := instGet(t, "/sw.js").Body.String()
		if !strings.Contains(body, "const instPrecache = ['/offline', '/static/tailwind.css', '/install/icon.svg'];") {
			t.Errorf("precache list missing or changed")
		}
	})
}

func TestInstallOfflinePage(t *testing.T) {
	t.Run("renders", func(t *testing.T) {
		w := instGet(t, "/offline")
		body := w.Body.String()
		if w.Code != http.StatusOK || w.Header().Get("Cache-Control") != "no-cache" || !strings.Contains(body, "<h1>You are offline</h1>") || !strings.Contains(body, `href="/">Try again</a>`) || strings.Contains(body, "<script") {
			t.Errorf("offline status %d; heading, retry link or script check failed", w.Code)
		}
	})
	t.Run("same for every reader", func(t *testing.T) {
		plain := instGet(t, "/offline").Body.String()
		withCookies := instGet(t, "/offline",
			&http.Cookie{Name: profileCookie, Value: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"},
			&http.Cookie{Name: themeCookie, Value: "dark"},
		).Body.String()
		if plain != withCookies || strings.Contains(plain, "Dev custody") {
			t.Errorf("offline page differs with reader cookies")
		}
	})
}

func TestInstallHead(t *testing.T) {
	t.Run("system theme", func(t *testing.T) {
		body := instGet(t, "/").Body.String()
		if !strings.Contains(body, `<link rel="manifest" href="/manifest.webmanifest">`) || !strings.Contains(body, `content="#111521" media="(prefers-color-scheme: dark)"`) {
			t.Errorf("system theme head missing manifest or dark theme color")
		}
	})
	t.Run("dark choice", func(t *testing.T) {
		body := instGet(t, "/?theme=dark").Body.String()
		if !strings.Contains(body, `<meta name="theme-color" content="#111521">`) || strings.Contains(body, `media="(prefers-color-scheme: light)"`) {
			t.Errorf("dark choice theme color wrong")
		}
	})
}

func TestInstallSettings(t *testing.T) {
	t.Run("explains installing", func(t *testing.T) {
		body := instGet(t, "/settings").Body.String()
		if !strings.Contains(body, `id="install-title"`) || !strings.Contains(body, "Install ForYou") || !strings.Contains(body, "It does not keep posts") {
			t.Errorf("install section missing from settings")
		}
	})
}
