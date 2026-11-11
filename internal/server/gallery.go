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
	ID, Reply, Alt, Caption       string
	Slide, Count, Image           int
	Prev, Next                    int
	Standalone                    bool
	Theme, Section, Filter, Query string
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

func readGallery(r *http.Request) (galleryPage, bool) {
	query := r.URL.Query()
	slide, err := strconv.Atoi(query.Get("slide"))
	p, found := requestPost(r, query.Get("id"))
	if err != nil || !found {
		return galleryPage{}, false
	}
	images, caption := demoImages[p.ID], p.Text
	replyPath := query.Get("reply")
	if replyPath != "" {
		chain := replyChain(threadReplies(r, p.ID), replyPath)
		if chain == nil {
			return galleryPage{}, false
		}
		images, caption = chain[len(chain)-1].pictures, ""
	}
	page, ok := slideOf(p.ID, replyPath, images, slide)
	page.Caption = caption
	return page, ok
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

// galleryViewerHandler fills or empties the viewer that opens over the page
// when a reader clicks a picture.
func galleryViewerHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Query().Get("view") == "closed" {
		_, _ = w.Write([]byte("<span hidden></span>"))
		return
	}
	page, ok := readGallery(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	_ = home.ExecuteTemplate(w, "gallery-viewer", page)
}
