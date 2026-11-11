package server

import (
	"net/http"
	"strconv"
)

// demoImages lists the bundled images each image post shows, in slide order.
// A post with one image opens in the same viewer as a slideshow.
var demoImages = map[string][]int{
	"sofia":       {1, 2, 3},
	"travel":      {3},
	"photography": {2},
}

type galleryPage struct {
	ID, Caption                   string
	Slide, Count, Image           int
	Prev, Next                    int
	Standalone                    bool
	Theme, Section, Filter, Query string
}

func gallerySlide(id string, slide int) (galleryPage, bool) {
	images := demoImages[id]
	if slide < 1 || slide > len(images) {
		return galleryPage{}, false
	}
	prev, next := slide-1, slide+1
	if prev < 1 {
		prev = len(images)
	}
	if next > len(images) {
		next = 1
	}
	return galleryPage{ID: id, Slide: slide, Count: len(images), Image: images[slide-1], Prev: prev, Next: next}, true
}

// Gallery returns the first slide of a post's images for the feed.
func (p post) Gallery() galleryPage {
	page, _ := gallerySlide(p.ID, 1)
	return page
}

func readGallery(r *http.Request) (galleryPage, bool) {
	slide, err := strconv.Atoi(r.URL.Query().Get("slide"))
	if err != nil {
		return galleryPage{}, false
	}
	page, ok := gallerySlide(r.URL.Query().Get("id"), slide)
	if !ok {
		return galleryPage{}, false
	}
	if p, found := postByID(page.ID); found {
		page.Caption = p.Text
	}
	return page, true
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
