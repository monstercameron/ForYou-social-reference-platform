package server

import (
	"bytes"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"math"
	"net/http"
	"sync"
)

func init() { routeGroups = append(routeGroups, instRoutes) }

func instRoutes(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /manifest.webmanifest", instManifestHandler)
	mux.HandleFunc("GET /install/icon.svg", instSVGHandler)
	mux.HandleFunc("GET /install/icon-192.png", instIconHandler(instIcon192))
	mux.HandleFunc("GET /install/icon-512.png", instIconHandler(instIcon512))
	mux.HandleFunc("GET /install/icon-maskable-512.png", instIconHandler(instIconMaskable512))
	mux.HandleFunc("GET /install/apple-touch-icon.png", instIconHandler(instIconApple180))
	mux.HandleFunc("GET /sw.js", instServiceWorkerHandler)
	mux.HandleFunc("GET /offline", a.instOfflineHandler)
}

type instManifestIcon struct {
	Src     string `json:"src"`
	Sizes   string `json:"sizes"`
	Type    string `json:"type"`
	Purpose string `json:"purpose"`
}

type instManifest struct {
	ID              string             `json:"id"`
	Name            string             `json:"name"`
	ShortName       string             `json:"short_name"`
	Description     string             `json:"description"`
	Lang            string             `json:"lang"`
	Dir             string             `json:"dir"`
	StartURL        string             `json:"start_url"`
	Scope           string             `json:"scope"`
	Display         string             `json:"display"`
	BackgroundColor string             `json:"background_color"`
	ThemeColor      string             `json:"theme_color"`
	Icons           []instManifestIcon `json:"icons"`
}

var instManifestDocument = instManifest{
	ID:              "/",
	Name:            "ForYou",
	ShortName:       "ForYou",
	Description:     "Read posts from the ForYou network through this client server.",
	Lang:            "en",
	Dir:             "ltr",
	StartURL:        "/",
	Scope:           "/",
	Display:         "standalone",
	BackgroundColor: "#ffffff",
	ThemeColor:      "#ffffff",
	Icons: []instManifestIcon{
		{Src: "/install/icon.svg", Sizes: "any", Type: "image/svg+xml", Purpose: "any"},
		{Src: "/install/icon-192.png", Sizes: "192x192", Type: "image/png", Purpose: "any"},
		{Src: "/install/icon-512.png", Sizes: "512x512", Type: "image/png", Purpose: "any"},
		{Src: "/install/icon-maskable-512.png", Sizes: "512x512", Type: "image/png", Purpose: "maskable"},
	},
}

func instManifestHandler(w http.ResponseWriter, r *http.Request) {
	body, err := json.Marshal(instManifestDocument)
	if err != nil {
		http.Error(w, "manifest unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/manifest+json")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(body)
}

const instSVG = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512"><rect width="512" height="512" rx="144" fill="#4746df"/><g transform="translate(96 96) scale(13.3333)"><path d="M5 19V5l14 14z" fill="#ffffff"/></g></svg>`

func instSVGHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write([]byte(instSVG))
}

// instIcon holds one rendered PNG. It is drawn on first request and kept in memory.
type instIcon struct {
	size int
	full bool
	once sync.Once
	data []byte
}

var (
	instIcon192         = &instIcon{size: 192}
	instIcon512         = &instIcon{size: 512}
	instIconMaskable512 = &instIcon{size: 512, full: true}
	instIconApple180    = &instIcon{size: 180, full: true}
)

func (icon *instIcon) bytes() []byte {
	icon.once.Do(func() {
		var buf bytes.Buffer
		if err := png.Encode(&buf, instRasterize(icon.size, icon.full)); err == nil {
			icon.data = buf.Bytes()
		}
	})
	return icon.data
}

func instIconHandler(icon *instIcon) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(icon.bytes())
	}
}

var (
	instAccent = [3]float64{0x47, 0x46, 0xdf}
	instWhite  = [3]float64{255, 255, 255}
)

// instRasterize draws the brand mark. The full shape is an opaque square for platforms that apply their own mask.
func instRasterize(size int, full bool) *image.NRGBA {
	s := float64(size)
	radius, box, offset := 0.0, s/2, s/4
	if !full {
		radius, box, offset = 9*s/32, 20*s/32, 6*s/32
	}
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			var sum [3]float64
			covered := 0
			for sy := 0; sy < 4; sy++ {
				for sx := 0; sx < 4; sx++ {
					px := float64(x) + (float64(sx)+0.5)/4
					py := float64(y) + (float64(sy)+0.5)/4
					if !instInRoundedSquare(px, py, s, radius) {
						continue
					}
					covered++
					rgb := instAccent
					if instGlyphCovers((px-offset)*24/box, (py-offset)*24/box) {
						rgb = instWhite
					}
					for i := range sum {
						sum[i] += rgb[i]
					}
				}
			}
			if covered == 0 {
				continue
			}
			n := float64(covered)
			img.SetNRGBA(x, y, color.NRGBA{
				R: uint8(math.Round(sum[0] / n)),
				G: uint8(math.Round(sum[1] / n)),
				B: uint8(math.Round(sum[2] / n)),
				A: uint8(covered * 255 / 16),
			})
		}
	}
	return img
}

func instInRoundedSquare(x, y, size, radius float64) bool {
	dx := math.Max(math.Max(radius-x, x-(size-radius)), 0)
	dy := math.Max(math.Max(radius-y, y-(size-radius)), 0)
	return dx*dx+dy*dy <= radius*radius
}

// instGlyphCovers reports whether a point in the 24-unit mark space lies inside the triangle M5 19V5l14 14z.
func instGlyphCovers(u, v float64) bool {
	return u >= 5 && v <= 19 && v >= u
}

// instServiceWorkerHandler serves the worker at the root so its default scope is the whole client.
func instServiceWorkerHandler(w http.ResponseWriter, r *http.Request) {
	body, err := templates.ReadFile("static/install-sw.js")
	if err != nil {
		http.Error(w, "service worker unavailable", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(body)
}

// instOfflineHandler does not use the layout: the page must not depend on cookies, profile, identity or ads.
// The body has no data-theme, so an explicit Light or Dark choice is not applied offline; the page follows prefers-color-scheme.
func (a *App) instOfflineHandler(w http.ResponseWriter, r *http.Request) {
	a.renderOutput(w, "install-offline", nil, http.StatusOK, "no-cache")
}
