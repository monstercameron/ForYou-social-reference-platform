package server

import "net/http"

// Layout is the shared shell data passed to layout templates. Each template
// folder belongs to one feature. Definitions in feature folders start with
// feed-, explore-, post-, thread-, embed-, share-, compose-, settings-,
// identity-, network-, wallet-, versions-, markdown-, or media-. The layout-
// and card- prefixes are reserved for shared core markup.
type Layout struct {
	Title, Section, Theme string
	Dev                   bool
	Identity              string
	Network               []NetworkLine
	Query, Filter         string
	Rightbar              bool
}

// NetworkLine describes one line in the local network summary.
type NetworkLine struct {
	Label, Value string
}

type plannedPage struct {
	Path, Section, Title, Description string
}

var plannedPages = []plannedPage{
	{Path: "/identity", Section: "identity", Title: "Identity and keys", Description: "Create and back up the key that signs your posts."},
	{Path: "/network", Section: "network", Title: "Network", Description: "Choose providers and see what they declare."},
	{Path: "/wallet", Section: "wallet", Title: "Wallet", Description: "Fund a provider balance that pays the per-post fee."},
}

func registerLandingPages(a *App, mux *http.ServeMux) {
	for _, page := range plannedPages {
		request, _ := http.NewRequest(http.MethodGet, page.Path, nil)
		_, pattern := mux.Handler(request)
		if pattern != "GET "+page.Path {
			current := page
			mux.HandleFunc("GET "+current.Path, func(w http.ResponseWriter, r *http.Request) {
				data := struct {
					Layout
					Description string
				}{Layout: a.layout(r, current.Section, current.Title), Description: current.Description}
				a.page(w, r, "layout-landing", data)
			})
		}
	}
}
