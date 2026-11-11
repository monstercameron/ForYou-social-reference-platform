package server

import "net/http"

type explorePage struct {
	feedPage
	Section string
}

// exploreHandler renders a navigable view of the records available locally.
func exploreHandler(w http.ResponseWriter, r *http.Request) {
	page, ok := readFeedRequest(r, false)
	if !ok {
		http.Error(w, "invalid explore filter or search", http.StatusBadRequest)
		return
	}
	rememberTheme(w, r)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "private, no-store")
	_ = home.ExecuteTemplate(w, "explore-page", explorePage{feedPage: page, Section: "explore"})
}

// exploreMoreHandler appends the next five records without replacing earlier cards.
func exploreMoreHandler(w http.ResponseWriter, r *http.Request) {
	page, ok := readFeedRequest(r, true)
	if !ok {
		http.Error(w, "invalid explore page", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = home.ExecuteTemplate(w, "explore-more-response", page)
}
