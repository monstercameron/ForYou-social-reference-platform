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

const demoImageAlt = "Illustrated mountain landscape"

// galleryPage is one image of a post, or of a reply when Reply holds its path.
type galleryPage struct {
	Layout                  Layout
	ID, Reply, Alt, Caption string
	Slide, Count, Image     int
	Prev, Next              int
	Standalone              bool
}

func init() { routeGroups = append(routeGroups, registerGallery) }

func registerGallery(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /gallery", a.galleryPageHandler)
	mux.HandleFunc("GET /gallery/fragment", a.galleryFragmentHandler)
	mux.HandleFunc("GET /gallery/viewer", a.galleryViewerHandler)
}

func slideOf(id, replyPath string, images []int, slide int) (galleryPage, bool) {
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
	alt := demoImageAlt
	if len(images) > 1 {
		alt += ", image " + strconv.Itoa(slide) + " of " + strconv.Itoa(len(images))
	}
	return galleryPage{ID: id, Reply: replyPath, Alt: alt, Slide: slide, Count: len(images), Image: images[slide-1], Prev: prev, Next: next}, true
}

// Gallery returns the first slide of a post's images for the feed.
func (p post) Gallery() galleryPage {
	page, _ := slideOf(p.ID, "", demoImages[p.ID], 1)
	return page
}

func (a *App) readGallery(r *http.Request) (galleryPage, bool) {
	query := r.URL.Query()
	slide, err := strconv.Atoi(query.Get("slide"))
	p, found := a.requestPost(r, query.Get("id"))
	if err != nil || !found {
		return galleryPage{}, false
	}
	replyPath := query.Get("reply")
	images, caption, hasPictures, err := a.records().Pictures(r.Context(), r, p.ID, replyPath)
	if err != nil || !hasPictures {
		return galleryPage{}, false
	}
	page, ok := slideOf(p.ID, replyPath, images, slide)
	page.Caption = caption
	return page, ok
}

func (a *App) galleryPageHandler(w http.ResponseWriter, r *http.Request) {
	page, ok := a.readGallery(r)
	if !ok {
		a.writeDetailNotFound(w, r)
		return
	}
	page.Standalone = true
	page.Layout = a.layout(r, "gallery", "Slideshow")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	a.renderOutput(w, "card-gallery-page", page, http.StatusOK, "")
}

func (a *App) galleryFragmentHandler(w http.ResponseWriter, r *http.Request) {
	page, ok := a.readGallery(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	a.fragment(w, r, "card-gallery-content", page)
}

// galleryViewerHandler fills or empties the viewer that opens over the page
// when a reader clicks a picture.
func (a *App) galleryViewerHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("view") == "closed" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write([]byte("<span hidden></span>"))
		return
	}
	page, ok := a.readGallery(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	a.fragment(w, r, "card-gallery-viewer", page)
}
