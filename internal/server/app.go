package server

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/sdk"
)

// Config contains the client service's local configuration.
type Config struct {
	Dev          bool
	DataDir      string
	PublicOrigin string
	Now          func() time.Time
	LoopInterval time.Duration
	Providers    []Endpoint
	DefaultList  ListDisclosure
	Mirrors      []Endpoint
	Foundation   Endpoint
	Monitors     []Endpoint
	Faux         string
	Pin          sdk.Pin
	HTTPClient   *http.Client
}

// Endpoint identifies one configured ForYou service.
type Endpoint struct{ URL, ServiceID, Label string }

// ListDisclosure describes the source and policy of the default endpoint list.
type ListDisclosure struct{ Controller, Funding, Version, Criteria, Monitors, Conflicts string }

// App holds the state shared by one client service.
type App struct {
	config        Config
	templates     *template.Template
	profiles      *profileStore
	store         *demoStore
	clients       map[string]*sdk.Client
	clientMu      sync.Mutex
	trustMu       sync.RWMutex
	root          *sdk.TrustRoot
	trustStatus   TrustStatus
	ledgerValue   *sdk.Ledger
	faux          *faux.Client
	recordSource  recordSource
	keystoreValue *sdk.Keystore
	networkMu     sync.RWMutex
	providerState map[string]string
	sourceNote    string
	fallback      bool
}

// routeGroups lets feature files register routes without central route edits.
var routeGroups []func(*App, *http.ServeMux)

func (a *App) page(w http.ResponseWriter, r *http.Request, name string, data any) {
	a.render(w, r, name, data, true)
}

func (a *App) fragment(w http.ResponseWriter, r *http.Request, name string, data any) {
	a.render(w, r, name, data, false)
}

func (a *App) render(w http.ResponseWriter, r *http.Request, name string, data any, page bool) {
	cache := "no-store"
	if page {
		cache = "private, no-store"
	}
	a.renderOutput(w, name, data, http.StatusOK, cache)
}

func (a *App) renderStatus(w http.ResponseWriter, r *http.Request, name string, data any, status int) {
	a.renderOutput(w, name, data, status, "no-store")
}

func (a *App) renderOutput(w http.ResponseWriter, name string, data any, status int, cache string) {
	var output bytes.Buffer
	if err := a.templates.ExecuteTemplate(&output, name, data); err != nil {
		a.logger().Printf("client: render %s: %v", name, err)
		http.Error(w, "unable to render page", http.StatusInternalServerError)
		return
	}
	if w.Header().Get("Content-Type") == "" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	if cache != "" && w.Header().Get("Cache-Control") == "" {
		w.Header().Set("Cache-Control", cache)
	}
	w.WriteHeader(status)
	_, _ = w.Write(output.Bytes())
}

func (a *App) fail(w http.ResponseWriter, r *http.Request, status int, message string) {
	name := "layout-error-message"
	// HTMLeX fetch requests have Sec-Fetch-Dest: empty and no custom request header.
	if r.Header.Get("Sec-Fetch-Dest") == "empty" {
		name = "layout-error-fragment"
	}
	data := struct {
		Layout
		Message string
	}{Layout: a.layout(r, "", "Request failed"), Message: message}
	var output bytes.Buffer
	if err := a.templates.ExecuteTemplate(&output, name, data); err != nil {
		http.Error(w, "request failed", status)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_, _ = w.Write(output.Bytes())
}

func (a *App) layout(r *http.Request, section, title string) Layout {
	filter := r.URL.Query().Get("filter")
	if filter == "" {
		filter = "all"
	}
	shell := Layout{Title: title, Section: section, Theme: requestTheme(r), Dev: a.config.Dev, Query: r.URL.Query().Get("q"), Filter: filter, Rightbar: section != "compose" && section != "settings" && section != "gallery"}
	shell.Identity = "No identity yet"
	if cookie, err := r.Cookie(profileCookie); err == nil && validProfileID(cookie.Value) {
		if identity, found, identityErr := a.keystore().Identity(cookie.Value); identityErr == nil && found {
			shell.Identity = shortDID(identity.DID)
		}
	}
	if a.config.Dev {
		shell.Custody = "Dev custody"
	}
	if shell.Rightbar {
		shell.Ads = adsFor(r)
		if len(shell.Ads) > 2 {
			shell.Ads = shell.Ads[:2]
		}
	}
	if len(a.config.Providers) > 0 {
		shell.NetworkConfigured = true
		shell.Source, shell.Network = a.networkLines(r)
		if d := a.config.DefaultList; d.Controller != "" {
			shell.DefaultList = "Controlled by " + d.Controller + ", funded by " + d.Funding + ". Version " + d.Version + ". Criteria: " + d.Criteria + ". Monitors: " + d.Monitors + ". Conflicts: " + d.Conflicts + "."
		}
	}
	return shell
}

// networkLines describes the reader's network from cached state only; no
// service is contacted while a page renders.
func (a *App) networkLines(r *http.Request) (string, []NetworkLine) {
	a.networkMu.RLock()
	source := "Network"
	if a.sourceNote != "" {
		source = a.sourceNote
	}
	var lines []NetworkLine
	for _, provider := range a.selection(requestProfile(r)).Providers {
		state, ok := a.providerState[provider.Endpoint]
		if !ok {
			state = "not contacted yet"
		}
		trust := "not trusted"
		if provider.Trusted {
			trust = "trusted"
		}
		host := provider.Endpoint
		if u, err := url.Parse(provider.Endpoint); err == nil && u.Host != "" {
			host = u.Host
		}
		lines = append(lines, NetworkLine{Label: provider.Label, Value: host + " · " + shortDID(provider.OperatorKey) + " · " + state + " · " + trust})
	}
	a.networkMu.RUnlock()
	root, status := a.trustRoot()
	trust := "Trust root unavailable: " + status.Err
	switch status.State {
	case TrustReady:
		trust = "Trust root verified " + relativeTime(status.FetchedAt, a.now())
	case TrustStale:
		trust = "Trust root stale since " + relativeTime(status.FetchedAt, a.now()) + ": " + status.Err
	}
	lines = append(lines, NetworkLine{Label: "Trust root", Value: trust})
	if root != nil {
		summary := root.Summary()
		lines = append(lines, NetworkLine{Label: "Trust summary", Value: fmt.Sprintf("%d of %d release keys · %d Foundation keys · %d approved fee schedules · %d rejected objects", summary.Threshold, len(summary.ReleaseKeyIDs), len(summary.Foundations), len(summary.Schedules), len(summary.Rejected))})
	}
	if len(a.config.Mirrors) > 0 {
		lines = append(lines, NetworkLine{Label: "Mirrors", Value: endpointLabels(a.config.Mirrors)})
	}
	if len(a.config.Monitors) > 0 {
		lines = append(lines, NetworkLine{Label: "Monitors", Value: endpointLabels(a.config.Monitors)})
	}
	lines = append(lines, NetworkLine{Label: "Media", Value: "External, loaded only on request"})
	return source, lines
}

func endpointLabels(endpoints []Endpoint) string {
	labels := make([]string, 0, len(endpoints))
	for _, endpoint := range endpoints {
		if endpoint.Label != "" {
			labels = append(labels, endpoint.Label)
		} else {
			labels = append(labels, endpoint.URL)
		}
	}
	return strings.Join(labels, ", ")
}
