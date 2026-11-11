package server

import (
	"net/http"
	"strconv"
)

type galleryPage struct {
	ID                            string
	Slide, Prev, Next             int
	Standalone                    bool
	Theme, Section, Filter, Query string
}

func readGallery(r *http.Request) (galleryPage, bool) {
	if r.URL.Query().Get("id") != "sofia" {
		return galleryPage{}, false
	}
	slide, err := strconv.Atoi(r.URL.Query().Get("slide"))
	if err != nil || slide < 1 || slide > 3 {
		return galleryPage{}, false
	}
	prev, next := slide-1, slide+1
	if prev < 1 {
		prev = 3
	}
	if next > 3 {
		next = 1
	}
	return galleryPage{ID: "sofia", Slide: slide, Prev: prev, Next: next}, true
}

func galleryPageHandler(w http.ResponseWriter, r *http.Request) {
	page, ok := readGallery(r)
	if !ok {
		writeDetailNotFound(w, requestTheme(r))
		return
	}
	page.Standalone = true
	page.Theme = requestTheme(r)
	page.Filter = "all"
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_ = home.ExecuteTemplate(w, "gallery-page", page)
}

func galleryFragmentHandler(w http.ResponseWriter, r *http.Request) {
	page, ok := readGallery(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = home.ExecuteTemplate(w, "gallery-content", page)
}
