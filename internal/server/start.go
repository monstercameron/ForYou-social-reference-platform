package server

import (
	"fmt"
	"net/http"
	"strconv"
)

func init() { routeGroups = append(routeGroups, registerStart) }

const startCookie = "foryou_start"

const (
	startDone      = "done"
	startTodo      = "todo"
	startUnchecked = "unchecked"
)

func registerStart(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /start", a.startPageHandler)
	mux.HandleFunc("GET /start/panel", a.startPanelHandler)
	mux.HandleFunc("POST /start/hide", a.startChangeHandler(true, false))
	mux.HandleFunc("POST /start/hide/fragment", a.startChangeHandler(true, true))
	mux.HandleFunc("POST /start/show", a.startChangeHandler(false, false))
	mux.HandleFunc("POST /start/show/fragment", a.startChangeHandler(false, true))
}

// startStep is one thing the guide can read from this server and this browser.
// State is done, todo, or unchecked when this page has no way to see the step.
type startStep struct {
	Key, Title, Detail, Href, Action, State string
}

// startProgress counts only the steps this page can check: a step is checked
// unless its state is unchecked.
type startProgress struct {
	Line, Note    string
	Done, Checked int
	Ready         bool
	Bar           string
}

// startPanel is the Home panel for one place, the rail or the feed strip.
type startPanel struct {
	Place, ID, TitleID, ShowID string
	Hidden                     bool
	Steps                      []startStep
	Progress                   startProgress
}

type startPageData struct {
	Layout     Layout
	Steps      []startStep
	Progress   startProgress
	Dev        bool
	HomeHidden bool
	Saved      bool
}

func (a *App) startPageHandler(w http.ResponseWriter, r *http.Request) {
	rememberTheme(w, r)
	w.Header().Set("Cache-Control", "private, no-store")
	steps := startStepsFor(a, r)
	a.page(w, r, "start-page", startPageData{
		Layout:     a.layout(r, "start", "Getting started"),
		Steps:      steps,
		Progress:   startProgressFor(steps),
		Dev:        a.config.Dev,
		HomeHidden: startHidden(r),
	})
}

func (a *App) startPanelHandler(w http.ResponseWriter, r *http.Request) {
	place := r.URL.Query().Get("place")
	if place != "rail" && place != "feed" {
		http.Error(w, "invalid place", http.StatusBadRequest)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	a.fragment(w, r, startPanelTemplate(place), a.startPanelFor(r, place, startHidden(r)))
}

// startChangeHandler hides or shows the guide. Without scripts the answer is a
// redirect; the fragment answer re-renders the place the reader acted on.
func (a *App) startChangeHandler(hidden, fragment bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		form, ok := readActionForm(w, r, "place")
		if !ok {
			return
		}
		place := form.Get("place")
		if place != "rail" && place != "feed" && place != "page" {
			http.Error(w, "invalid place", http.StatusBadRequest)
			return
		}
		startSetHidden(w, r, hidden)
		if !fragment {
			target := "/"
			if place == "page" {
				target = "/start#start-home"
			}
			http.Redirect(w, r, target, http.StatusSeeOther)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if place == "page" {
			a.fragment(w, r, "start-home", startPageData{HomeHidden: hidden, Saved: true})
			return
		}
		a.fragment(w, r, startPanelTemplate(place), a.startPanelFor(r, place, hidden))
	}
}

func (a *App) startPanelFor(r *http.Request, place string, hidden bool) startPanel {
	steps := startStepsFor(a, r)
	return startPanel{
		Place:    place,
		ID:       "start-" + place,
		TitleID:  "start-" + place + "-title",
		ShowID:   "start-" + place + "-show",
		Hidden:   hidden,
		Steps:    steps,
		Progress: startProgressFor(steps),
	}
}

func startPanelTemplate(place string) string {
	if place == "feed" {
		return "start-feed-panel"
	}
	return "start-rail-panel"
}

func startHidden(r *http.Request) bool {
	c, err := r.Cookie(startCookie)
	return err == nil && c.Value == "hidden"
}

func startSetHidden(w http.ResponseWriter, r *http.Request, hidden bool) {
	c := &http.Cookie{Name: startCookie, Value: "hidden", Path: "/", MaxAge: 31536000, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode}
	if !hidden {
		c.Value = ""
		c.MaxAge = -1
	}
	http.SetCookie(w, c)
}

// startStepsFor reads the five steps from this request alone. It never creates a
// profile, a key or a cookie, and never contacts a provider or the faux chain.
func startStepsFor(a *App, r *http.Request) []startStep {
	return []startStep{
		startKeyStep(a, r),
		startFundStep(a.config.Dev),
		startPostStep(),
		startFollowStep(r),
		startFiltersStep(r),
	}
}

func startKeyStep(a *App, r *http.Request) startStep {
	step := startStep{Key: "key", Title: "Create your signing key", Href: "/identity", Action: "Create key", State: startTodo,
		Detail: "Your key is your identity. There is no account and no password reset: whoever holds the key is you, and a lost key cannot be recovered."}
	profile := requestProfile(r)
	if profile == "" {
		return step
	}
	identity, found, err := a.keystore().Identity(profile)
	switch {
	case err != nil:
		step.State = startUnchecked
		step.Detail = "Could not read the key store on this server."
		step.Action = "Open identity"
	case found:
		step.State = startDone
		step.Action = "View key"
		step.Detail = "Your key " + shortDID(identity.DID) + " signs everything you publish."
		if a.config.Dev {
			step.Detail += " It is held in dev custody on this server."
		}
	}
	return step
}

func startFundStep(dev bool) startStep {
	step := startStep{Key: "fund", Title: "Add funds to a provider balance", Href: "/wallet", Action: "Open wallet", State: startUnchecked,
		Detail: "Each post pays a small fee for every started KiB, set by the specification and passed in full to the ForYou Foundation. It keeps spam expensive."}
	if dev {
		step.Title = "Add funds on the dev faux chain"
		step.Detail += " On this dev network the faux chain pays it and nothing has value."
	}
	step.Detail += " This page does not check your balance."
	return step
}

func startPostStep() startStep {
	return startStep{Key: "post", Title: "Publish your first post", Href: "/compose", Action: "Write a post", State: startUnchecked,
		Detail: "Every post, reply and edit is signed with your key and pays the fee. Nothing can be deleted: an edit is a new version that names the old one. This page does not check what you have published."}
}

func startFollowStep(r *http.Request) startStep {
	following := len(followedAuthors(r))
	liked := 0
	for _, entry := range feedbackEntries(r) {
		if entry.Vote == "up" {
			liked++
		}
	}
	step := startStep{Key: "follow", Title: "Follow a key or like a post", Href: "/?filter=all", Action: "Browse posts", State: startTodo,
		Detail: "Open a post and follow the key that signed it, or like it. Follows and likes stay in this browser and are never sent to the network."}
	if following == 0 && liked == 0 {
		return step
	}
	step.State = startDone
	step.Detail = fmt.Sprintf("Following %s and liking %s, saved in this browser.", startCount(following, "key", "keys"), startCount(liked, "post", "posts"))
	step.Href, step.Action = "/?filter=liked", "See them"
	if following > 0 {
		step.Href = "/?filter=following"
	}
	return step
}

func startFiltersStep(r *http.Request) startStep {
	step := startStep{Key: "filters", Title: "Choose content filters", Href: "/settings#content-filters", Action: "Set filters", State: startTodo,
		Detail: "Every post is rated on this server by a fixed rubric. Choose what gets folded; the choice is saved in this browser."}
	if startFiltersSaved(r) {
		step.State = startDone
		step.Action = "Change filters"
		step.Detail = "Your filters are saved in this browser."
	}
	return step
}

// startFiltersSaved accepts the filter cookie only when it has the shape
// readerFilters reads: one valid digit per dimension.
func startFiltersSaved(r *http.Request) bool {
	c, err := r.Cookie(filterCookie)
	if err != nil || len(c.Value) != len(dimensions) {
		return false
	}
	for i, d := range dimensions {
		if c.Value[i] < '0' || int(c.Value[i]-'0') >= len(d.Levels) {
			return false
		}
	}
	return true
}

func startCount(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

func startProgressFor(steps []startStep) startProgress {
	progress := startProgress{Note: "Funding and publishing are not checked here.", Bar: "start-bar-0"}
	for _, step := range steps {
		switch step.State {
		case startDone:
			progress.Done++
			progress.Checked++
		case startTodo:
			progress.Checked++
		}
	}
	switch {
	case progress.Checked == 0:
		progress.Line = "Progress could not be read on this server."
	case progress.Done == progress.Checked:
		progress.Line = "All checked steps are done."
		progress.Ready = true
	default:
		progress.Line = fmt.Sprintf("%d of %d checked steps done.", progress.Done, progress.Checked)
	}
	if progress.Checked > 0 {
		progress.Bar = "start-bar-" + strconv.Itoa(progress.Done*3/progress.Checked)
	}
	return progress
}
