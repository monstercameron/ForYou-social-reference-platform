package server

import "net/http"

func init() { routeGroups = append(routeGroups, registerExplore) }

func registerExplore(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /explore", a.exploreHandler)
	mux.HandleFunc("GET /explore/more", a.exploreMoreHandler)
}

type explorePage struct {
	feedPage
}

// exploreHandler renders a navigable view of the records available locally.
func (a *App) exploreHandler(w http.ResponseWriter, r *http.Request) {
	page, ok := a.readFeedRequest(r, false)
	if !ok {
		http.Error(w, "invalid explore filter or search", http.StatusBadRequest)
		return
	}
	rememberTheme(w, r)
	page.Layout = a.layout(r, "explore", "Explore")
	a.page(w, r, "explore-page", explorePage{feedPage: page})
}

// exploreMoreHandler appends the next five records without replacing earlier cards.
func (a *App) exploreMoreHandler(w http.ResponseWriter, r *http.Request) {
	page, ok := a.readFeedRequest(r, true)
	if !ok {
		http.Error(w, "invalid explore page", http.StatusBadRequest)
		return
	}
	a.fragment(w, r, "explore-more-response", page)
}
