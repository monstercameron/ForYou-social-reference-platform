package server

import (
	"context"
	"net/http"
	"strings"

	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/names"
	"foryou.local/foryou/internal/sdk"
)

func init() { routeGroups = append(routeGroups, registerNamesAuthor) }

func registerNamesAuthor(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /names/label", a.namesLabelHandler)
	mux.HandleFunc("GET /authors/{did}", a.namesPageHandler)
	mux.HandleFunc("GET /authors/{did}/panel", a.namesPanelHandler)
	mux.HandleFunc("GET /authors/{did}/name", a.namesCheckHandler)
	mux.HandleFunc("GET /authors/{did}/same-name", a.namesSameHandler)
}

// namesCheck is what one of the reader's providers answered in a name check.
type namesCheck struct {
	Source, Text string
}

// namesPanel is the author name panel: the key, its cached name, the reader's
// petname form and the other keys that share the name.
type namesPanel struct {
	AuthorID, Encoded, Fingerprint string
	State, Name, IssuedAt, NameID  string
	Checked                        bool
	Checks                         []namesCheck
	Petname                        namesPetnameForm
	Collision                      bool
	Others                         []post
	Search                         *namesSearch
}

// namesPetnameForm is the petname form and its status line.
type namesPetnameForm struct {
	Encoded, Value, Status, Error string
}

// namesCandidate is a key another of the reader's providers returned for the same name.
type namesCandidate struct {
	Name, Encoded, Fingerprint string
}

// namesSearch is the result of a search for other keys with the same name.
type namesSearch struct {
	Encoded    string
	Name       string
	Candidates []namesCandidate
	Notes      []string
}

// namesPage is the full author page.
type namesPage struct {
	Layout Layout
	Label  post
	Panel  namesPanel
}

// namesRequestDID returns the author key of the request path, or false when it is not a canonical did:key.
func namesRequestDID(r *http.Request) (string, bool) {
	did := r.PathValue("did")
	if _, err := keys.ParseDID(did); err != nil {
		return "", false
	}
	return did, true
}

// namesLabelHandler returns the label of one key. It marks the key as wanted, so the
// refresh job fetches its name later; rendering never fetches.
func (a *App) namesLabelHandler(w http.ResponseWriter, r *http.Request) {
	author := r.URL.Query().Get("author")
	if _, err := keys.ParseDID(author); err != nil {
		http.Error(w, "invalid author", http.StatusBadRequest)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	a.fragment(w, r, "names-label-fragment", a.namesViewFor(r).author(author))
}

// namesPageHandler is the full author page, and the twin of every author fragment.
func (a *App) namesPageHandler(w http.ResponseWriter, r *http.Request) {
	author, ok := namesRequestDID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	profile, err := a.profile(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "Could not open your local names.")
		return
	}
	view := a.namesViewFor(r)
	checked := r.URL.Query().Get("check") == "1"
	var checks []namesCheck
	if checked {
		checks = a.namesCheckAuthor(r.Context(), profile, author)
	}
	panel := a.namesPanelFor(view, author)
	panel.Petname = a.namesPetnameData(r, profile, author, r.URL.Query().Get("petname"))
	if checked {
		panel.Checked, panel.Checks = true, checks
		panel = a.namesRefreshPanel(view, panel, author)
	}
	if r.URL.Query().Get("same") == "1" {
		search := a.namesSearchAuthor(r.Context(), profile, author, view)
		panel.Search = &search
	}
	w.Header().Set("Cache-Control", "private, no-store")
	a.page(w, r, "names-author-page", namesPage{Layout: a.layout(r, "explore", "Author name"), Label: view.author(author), Panel: panel})
}

// namesPanelHandler returns the author name panel fragment. It reads the cache only.
func (a *App) namesPanelHandler(w http.ResponseWriter, r *http.Request) {
	author, ok := namesRequestDID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	profile, err := a.profile(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "Could not open your local names.")
		return
	}
	panel := a.namesPanelFor(a.namesViewFor(r), author)
	panel.Petname = a.namesPetnameData(r, profile, author, "")
	w.Header().Set("Cache-Control", "no-store")
	a.fragment(w, r, "names-author-panel", panel)
}

// namesCheckHandler asks the reader's providers for the name of a key, on the reader's action only.
func (a *App) namesCheckHandler(w http.ResponseWriter, r *http.Request) {
	author, ok := namesRequestDID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	profile, err := a.profile(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "Could not open your local names.")
		return
	}
	checks := a.namesCheckAuthor(r.Context(), profile, author)
	panel := a.namesPanelFor(a.namesViewFor(r), author)
	panel.Checked, panel.Checks = true, checks
	w.Header().Set("Cache-Control", "no-store")
	a.fragment(w, r, "names-author-name", panel)
}

// namesSameHandler searches the reader's providers for other keys with the cached name of this key.
func (a *App) namesSameHandler(w http.ResponseWriter, r *http.Request) {
	author, ok := namesRequestDID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	profile, err := a.profile(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "Could not open your local names.")
		return
	}
	search := a.namesSearchAuthor(r.Context(), profile, author, a.namesViewFor(r))
	w.Header().Set("Cache-Control", "no-store")
	a.fragment(w, r, "names-same-results", search)
}

// namesPanelFor builds the panel from the cache. It never contacts a provider.
func (a *App) namesPanelFor(view namesView, author string) namesPanel {
	cached, known := view.cache.want(author, view.now)
	panel := namesPanel{AuthorID: author, Encoded: namesPathID(author), Fingerprint: names.Fingerprint(author), State: "unchecked"}
	if known {
		panel.State = "none"
	}
	if known && cached.Found {
		panel.State = "named"
		if cached.Withdrawn {
			panel.State = "withdrawn"
		}
		panel.NameID, panel.IssuedAt = cached.Record.NameID, cached.Record.IssuedAt
		if name, ok := currentName(cached); ok {
			panel.Name = name
		}
	}
	panel.Petname = namesPetnameForm{Encoded: panel.Encoded, Value: view.petnames[author]}
	if name := view.author(author).shownName(); name != "" {
		for _, other := range view.sharersOf(name, author) {
			panel.Others = append(panel.Others, view.author(other))
		}
		panel.Collision = len(panel.Others) > 0
	}
	return panel
}

// namesRefreshPanel rebuilds the cache-derived parts of a panel after a check changed the cache.
func (a *App) namesRefreshPanel(view namesView, panel namesPanel, author string) namesPanel {
	fresh := a.namesPanelFor(view, author)
	fresh.Petname, fresh.Checked, fresh.Checks, fresh.Search = panel.Petname, panel.Checked, panel.Checks, panel.Search
	return fresh
}

// namesPetnameData returns the petname form for a key with its status message.
func (a *App) namesPetnameData(r *http.Request, profile, author, status string) namesPetnameForm {
	petnames, err := a.petnames(profile)
	if err != nil {
		petnames = map[string]string{}
	}
	form := namesPetnameForm{Encoded: namesPathID(author), Value: petnames[author]}
	switch status {
	case "saved":
		form.Status = "Saved. Only you see this name."
	case "cleared":
		form.Status = "Cleared."
	case "full":
		form.Error = "You have reached the limit of petnames on this client."
	case "invalid":
		form.Error = "Use 1 to 64 characters. Control and bidirectional marks are not allowed."
	case "failed":
		form.Error = "Could not save. Try again."
	}
	return form
}

// namesCheckAuthor asks the profile's selected providers for the name of a key and
// stores the verified result. It returns what each provider answered, in order.
func (a *App) namesCheckAuthor(ctx context.Context, profile, author string) []namesCheck {
	providers := a.selection(profile).Providers
	clients := make([]*sdk.Client, len(providers))
	for i, provider := range providers {
		if client, err := a.providerClient(namesEndpoint(provider)); err == nil {
			clients[i] = client
		}
	}
	view := sdk.ResolveName(ctx, clients, author, sdk.ResolveNameOptions{})
	if namesAnswered(view) {
		a.nameCache().store(author, view, a.now())
	}
	checks := make([]namesCheck, len(providers))
	for i, provider := range providers {
		checks[i] = namesCheck{Source: namesProviderLabel(provider), Text: namesSourceText(view.Sources[i])}
	}
	return checks
}

// namesSearchAuthor looks up other keys with the cached display name of author, first page per provider.
func (a *App) namesSearchAuthor(ctx context.Context, profile, author string, view namesView) namesSearch {
	search := namesSearch{Encoded: namesPathID(author)}
	name, ok := currentName(view.cache.current(author))
	if !ok {
		search.Notes = []string{"No published name to search for."}
		return search
	}
	search.Name = name
	seen := map[string]bool{}
	for _, provider := range a.selection(profile).Providers {
		client, err := a.providerClient(namesEndpoint(provider))
		if err != nil {
			search.Notes = append(search.Notes, namesProviderLabel(provider)+": could not connect")
			continue
		}
		lookup, err := sdk.NewNameLookup(client, name)
		if err != nil {
			search.Notes = append(search.Notes, namesProviderLabel(provider)+": could not search")
			continue
		}
		page, err := lookup.Next(ctx)
		if err != nil {
			search.Notes = append(search.Notes, namesProviderLabel(provider)+": "+namesErrorText(err))
			continue
		}
		for _, record := range page.Records {
			if record.AuthorID == author || seen[record.AuthorID] || record.DisplayName == nil {
				continue
			}
			seen[record.AuthorID] = true
			search.Candidates = append(search.Candidates, namesCandidate{Name: *record.DisplayName, Encoded: namesPathID(record.AuthorID), Fingerprint: names.Fingerprint(record.AuthorID)})
		}
	}
	return search
}

func namesEndpoint(provider ProviderChoice) Endpoint {
	return Endpoint{URL: provider.Endpoint, ServiceID: provider.OperatorKey, Label: provider.Label}
}

func namesProviderLabel(provider ProviderChoice) string {
	if provider.Label != "" {
		return provider.Label
	}
	return provider.Endpoint
}

// namesSourceText says in words what one provider answered about a key's name.
func namesSourceText(source sdk.NameSource) string {
	switch {
	case source.Signal == sdk.SignalNotFound:
		return "No name here"
	case source.NameID == "":
		return "Could not answer: " + namesSignalWords(source.Signal)
	case source.Behind:
		return "Older name"
	default:
		return "Current name"
	}
}

func namesErrorText(err error) string {
	return "Could not answer: " + namesSignalWords(sdk.Classify(err))
}

func namesSignalWords(signal sdk.Signal) string {
	return strings.ReplaceAll(signal.String(), "_", " ")
}
