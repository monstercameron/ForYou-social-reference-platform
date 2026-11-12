package server

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"

	"foryou.local/foryou/internal/content"
	"foryou.local/foryou/internal/names"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/wire"
)

func init() { routeGroups = append(routeGroups, registerNameID) }

const (
	nameidFormLimit   = 4096
	nameidTimeout     = 10 * time.Second
	nameidConcurrency = 4
	nameidWindow      = 24 * time.Hour
	nameidNoKey       = "You need a signing key before you can name it."
)

// nameidAvatarClient returns the isolated client that fetches an avatar picture the author chose.
var nameidAvatarClient = func() *http.Client {
	return content.NewFetchClient(content.FetchConfig{Timeout: nameidTimeout})
}

func registerNameID(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /identity/name", a.nameidPageHandler)
	mux.HandleFunc("GET /identity/name/status", a.nameidStatusHandler)
	mux.HandleFunc("POST /identity/name/check", a.nameidCheckHandler)
	mux.HandleFunc("POST /identity/name/avatar", a.nameidAvatarHandler)
	mux.HandleFunc("POST /identity/name/local", a.nameidLocalHandler)
	mux.HandleFunc("POST /identity/name/local/clear", a.nameidLocalClearHandler)
	mux.HandleFunc("POST /identity/name/publish", a.nameidPublishHandler)
	mux.HandleFunc("POST /identity/name/withdraw", a.nameidWithdrawHandler)
	mux.HandleFunc("POST /identity/name/retry", a.nameidRetryHandler)
}

// nameidView is the data every display name page and fragment renders.
type nameidView struct {
	Layout       Layout
	HasIdentity  bool
	DID          string
	Fingerprint  string
	Form         nameidForm
	Preview      nameidPreview
	Picture      nameidPicture
	Local        string
	Notice       string
	Warn         string
	Published    *nameidPublished
	CanWithdraw  bool
	CanRetry     bool
	LastOutcomes []nameidResult
	ResultsShown bool
	ResultError  string
	Results      []nameidResult
	Checked      bool
	CheckNote    string
	Lines        []nameidLine
	Current      *nameidPublished
}

// nameidForm holds what the reader typed, so a re-rendered form keeps it.
type nameidForm struct {
	Name            string
	Avatar          string
	UnderstoodError string
}

// nameidPreview shows a name as readers will see it, or the rule it breaks.
type nameidPreview struct {
	Empty       bool
	Problem     string
	Name        string
	Changed     bool
	Fingerprint string
}

// nameidPicture is an avatar the reader checked, with the bytes it committed to.
type nameidPicture struct {
	Error  string
	URL    string
	SHA256 string
	Size   int64
	Data   template.URL
}

// nameidPublished describes one record: a name, or a withdrawal.
type nameidPublished struct {
	Name      string
	NameID    string
	Withdrawn bool
	IssuedAt  time.Time
}

// nameidResult is one provider's answer to a publication, in words.
type nameidResult struct {
	Provider string
	Code     string
	Outcome  string
}

// nameidLine is one provider's answer to a check of the published name.
type nameidLine struct {
	Provider string
	Outcome  string
}

func (a *App) nameidPageHandler(w http.ResponseWriter, r *http.Request) {
	profile, did, found, err := a.nameidReader(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The identity could not be read.")
		return
	}
	view, err := a.nameidView(r, profile, did, found)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The display name could not be read.")
		return
	}
	if found {
		view.Preview = nameidPreviewOf(view.Form.Name, view.Fingerprint)
		if r.URL.Query().Get("check") == "1" {
			a.nameidCheck(r.Context(), profile, did, &view)
		}
	}
	w.Header().Set("Cache-Control", "private, no-store")
	a.page(w, r, "nameid-page", view)
}

func (a *App) nameidStatusHandler(w http.ResponseWriter, r *http.Request) {
	profile, did, ok := a.nameidKeyed(w, r)
	if !ok {
		return
	}
	view, err := a.nameidView(r, profile, did, true)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The display name could not be read.")
		return
	}
	a.nameidCheck(r.Context(), profile, did, &view)
	a.nameidAnswer(w, r, "nameid-status", view, http.StatusOK)
}

func (a *App) nameidCheckHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := nameidPost(w, r)
	if !ok {
		return
	}
	profile, did, ok := a.nameidKeyed(w, r)
	if !ok {
		return
	}
	view, err := a.nameidView(r, profile, did, true)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The display name could not be read.")
		return
	}
	view.Form = nameidForm{Name: form.Get("name"), Avatar: form.Get("avatar")}
	view.Preview = nameidPreviewOf(view.Form.Name, view.Fingerprint)
	a.nameidAnswer(w, r, "nameid-preview-update", view, http.StatusOK)
}

// nameidAvatarHandler is the one fetch of an avatar: it shows the picture and its size, and the
// publish step signs the digest shown here.
func (a *App) nameidAvatarHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := nameidPost(w, r)
	if !ok {
		return
	}
	profile, did, ok := a.nameidKeyed(w, r)
	if !ok {
		return
	}
	view, err := a.nameidView(r, profile, did, true)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The display name could not be read.")
		return
	}
	view.Form = nameidForm{Name: form.Get("name"), Avatar: form.Get("avatar")}
	view.Preview = nameidPreviewOf(view.Form.Name, view.Fingerprint)
	status := http.StatusOK
	raw := strings.TrimSpace(form.Get("avatar"))
	if raw == "" {
		view.Picture.Error = "Enter an https:// address for the picture."
		status = http.StatusBadRequest
	} else if picture, problem := nameidFetchPicture(r.Context(), raw); problem != "" {
		view.Picture.Error = problem
		status = http.StatusBadRequest
	} else {
		view.Picture = picture
	}
	a.nameidAnswer(w, r, "nameid-avatar-update", view, status)
}

func (a *App) nameidLocalHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := nameidPost(w, r)
	if !ok {
		return
	}
	profile, did, ok := a.nameidKeyed(w, r)
	if !ok {
		return
	}
	raw := form.Get("name")
	name, problem := nameidCheckName(raw)
	view, err := a.nameidView(r, profile, did, true)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The display name could not be read.")
		return
	}
	view.Form = nameidForm{Name: raw, Avatar: form.Get("avatar")}
	view.Preview = nameidPreviewOf(raw, view.Fingerprint)
	if problem != "" {
		view.Preview = nameidPreview{Problem: problem, Fingerprint: view.Fingerprint}
		a.nameidAnswer(w, r, "nameid-body", view, http.StatusBadRequest)
		return
	}
	if err := a.nameidUpdate(profile, func(s *nameidState) { s.Local = name }); err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The name could not be saved.")
		return
	}
	if view, err = a.nameidView(r, profile, did, true); err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The display name could not be read.")
		return
	}
	view.Notice = "Saved on this client only. Nobody else sees it."
	a.nameidAnswer(w, r, "nameid-body", view, http.StatusOK)
}

func (a *App) nameidLocalClearHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := nameidPost(w, r); !ok {
		return
	}
	profile, did, ok := a.nameidKeyed(w, r)
	if !ok {
		return
	}
	if err := a.nameidUpdate(profile, func(s *nameidState) { s.Local = "" }); err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The name could not be removed.")
		return
	}
	view, err := a.nameidView(r, profile, did, true)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The display name could not be read.")
		return
	}
	view.Notice = "Removed the name kept on this device."
	a.nameidAnswer(w, r, "nameid-body", view, http.StatusOK)
}

func (a *App) nameidPublishHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := nameidPost(w, r)
	if !ok {
		return
	}
	profile, did, ok := a.nameidKeyed(w, r)
	if !ok {
		return
	}
	if form.Get("understood") != "yes" {
		a.nameidFailResults(w, r, profile, did, form, "Tick the box to confirm that a published name is public.", true)
		return
	}
	name, problem := nameidCheckName(form.Get("name"))
	if problem != "" {
		a.nameidFailResults(w, r, profile, did, form, "Fix the name before you publish it: "+problem+".", false)
		return
	}
	avatar, problem := nameidAvatarOf(form)
	if problem != "" {
		a.nameidFailResults(w, r, profile, did, form, problem, false)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), nameidTimeout)
	defer cancel()
	outcomes, problem, err := a.nameidPublish(ctx, profile, did, &name, avatar)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The name could not be published.")
		return
	}
	a.nameidResultsAnswer(w, r, profile, did, outcomes, problem)
}

func (a *App) nameidWithdrawHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := nameidPost(w, r)
	if !ok {
		return
	}
	profile, did, ok := a.nameidKeyed(w, r)
	if !ok {
		return
	}
	if form.Get("withdraw") != "yes" {
		a.nameidFailResults(w, r, profile, did, form, "Tick the box to confirm the withdrawal.", false)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), nameidTimeout)
	defer cancel()
	outcomes, problem, err := a.nameidPublish(ctx, profile, did, nil, nil)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The withdrawal could not be sent.")
		return
	}
	a.nameidResultsAnswer(w, r, profile, did, outcomes, problem)
}

func (a *App) nameidRetryHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := nameidPost(w, r); !ok {
		return
	}
	profile, did, ok := a.nameidKeyed(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), nameidTimeout)
	defer cancel()
	outcomes, problem, err := a.nameidRetry(ctx, profile)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The publication could not be retried.")
		return
	}
	a.nameidResultsAnswer(w, r, profile, did, outcomes, problem)
}

// nameidReader returns the profile and its key's DID; found is false when the profile has no key yet.
func (a *App) nameidReader(w http.ResponseWriter, r *http.Request) (string, string, bool, error) {
	profile, err := a.profile(w, r)
	if err != nil {
		return "", "", false, err
	}
	identity, found, err := a.keystore().Identity(profile)
	if err != nil {
		return "", "", false, err
	}
	return profile, identity.DID, found, nil
}

// nameidKeyed answers 409 with the empty-state message when the profile has no key.
func (a *App) nameidKeyed(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	profile, did, found, err := a.nameidReader(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The identity could not be read.")
		return "", "", false
	}
	if !found {
		a.fail(w, r, http.StatusConflict, nameidNoKey)
		return "", "", false
	}
	return profile, did, true
}

// nameidPost checks the same-origin request and parses its small form.
func nameidPost(w http.ResponseWriter, r *http.Request) (url.Values, bool) {
	if !requireSameOrigin(w, r) || !parseSmallForm(w, r, nameidFormLimit) {
		return nil, false
	}
	return r.PostForm, true
}

// nameidFragment reports whether the request came from HTMLeX, which sends Sec-Fetch-Dest: empty.
func nameidFragment(r *http.Request) bool { return r.Header.Get("Sec-Fetch-Dest") == "empty" }

// nameidAnswer sends the named fragment to HTMLeX, or the full page otherwise. Fragments answer 200 because HTMLeX drops the body of an error response.
func (a *App) nameidAnswer(w http.ResponseWriter, r *http.Request, fragment string, view nameidView, status int) {
	if nameidFragment(r) {
		a.renderStatus(w, r, fragment, view, http.StatusOK)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	a.renderStatus(w, r, "nameid-page", view, status)
}

func (a *App) nameidResultsAnswer(w http.ResponseWriter, r *http.Request, profile, did string, outcomes []nameidOutcome, problem string) {
	view, err := a.nameidView(r, profile, did, true)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The display name could not be read.")
		return
	}
	view.Preview = nameidPreviewOf(view.Form.Name, view.Fingerprint)
	view.ResultsShown = true
	view.ResultError = problem
	view.Results = nameidResults(outcomes)
	status := http.StatusOK
	if problem != "" {
		status = http.StatusBadRequest
	}
	a.nameidAnswer(w, r, "nameid-results-update", view, status)
}

func (a *App) nameidFailResults(w http.ResponseWriter, r *http.Request, profile, did string, form url.Values, message string, understood bool) {
	view, err := a.nameidView(r, profile, did, true)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The display name could not be read.")
		return
	}
	if form.Has("name") {
		view.Form = nameidForm{Name: form.Get("name"), Avatar: form.Get("avatar")}
	}
	view.Preview = nameidPreviewOf(view.Form.Name, view.Fingerprint)
	if understood {
		view.Form.UnderstoodError = message
	}
	view.ResultsShown = true
	view.ResultError = message
	a.nameidAnswer(w, r, "nameid-results-update", view, http.StatusBadRequest)
}

// nameidView builds the page data from the profile's stored state.
func (a *App) nameidView(r *http.Request, profile, did string, found bool) (nameidView, error) {
	view := nameidView{Layout: a.layout(r, "identity", "Display name"), HasIdentity: found, DID: did, Fingerprint: names.Fingerprint(did)}
	view.Layout.Rightbar = false
	if !found {
		return view, nil
	}
	state, err := a.nameidLoad(profile)
	if err != nil {
		return nameidView{}, err
	}
	view.Local = state.Local
	view.Form.Name = state.Local
	if state.Record != nil {
		published := nameidPublishedOf(*state.Record)
		view.Published = &published
		if state.Record.DisplayName != nil {
			view.CanWithdraw = true
			if view.Form.Name == "" {
				view.Form.Name = *state.Record.DisplayName
			}
		}
		if avatar := state.Record.Avatar; avatar != nil {
			view.Form.Avatar = avatar.URL
			view.Picture = nameidPicture{URL: avatar.URL, SHA256: avatar.SHA256, Size: avatar.SizeBytes}
		}
		if issued := nameidAt(*state.Record); !issued.IsZero() && a.now().Sub(issued) < nameidWindow && nameidAccepted(state.Outcomes) {
			view.Warn = "You changed your name less than a day ago; providers will refuse another change until " + issued.Add(nameidWindow).Format("2 January 2006 15:04 UTC") + "."
		}
	}
	view.CanRetry = state.Pending != nil
	view.LastOutcomes = nameidResults(state.Outcomes)
	return view, nil
}

// nameidCheck reads the selected providers' current records. It runs only when the reader asks for it.
func (a *App) nameidCheck(ctx context.Context, profile, did string, view *nameidView) {
	ctx, cancel := context.WithTimeout(ctx, nameidTimeout)
	defer cancel()
	choices := a.selection(profile).Providers
	view.Checked = true
	view.Lines, view.Current, view.CheckNote = nil, nil, ""
	if len(choices) == 0 {
		view.CheckNote = "No provider is selected, so nothing was checked."
		return
	}
	result := sdk.ResolveName(ctx, a.nameidClients(choices), did, sdk.ResolveNameOptions{Parallel: nameidConcurrency})
	view.Lines = make([]nameidLine, 0, len(choices))
	for i, choice := range choices {
		view.Lines = append(view.Lines, nameidLine{Provider: identProviderName(choice), Outcome: nameidHolding(result, result.Sources[i])})
	}
	if result.Found {
		current := nameidPublishedOf(result.Record)
		view.Current = &current
	}
}

// nameidPublish signs a record for the profile's key and sends it to every selected provider. A nil name withdraws the published name. A non-empty problem is a reader-facing reason that nothing was sent.
func (a *App) nameidPublish(ctx context.Context, profile, did string, name *string, avatar *names.Avatar) ([]nameidOutcome, string, error) {
	choices := a.selection(profile).Providers
	if len(choices) == 0 {
		return nil, "No provider is selected, so nothing was published.", nil
	}
	clients := a.nameidClients(choices)
	view := sdk.ResolveName(ctx, clients, did, sdk.ResolveNameOptions{Parallel: nameidConcurrency})
	state, err := a.nameidLoad(profile)
	if err != nil {
		return nil, "", err
	}
	previous := state.Record
	if view.Found {
		current := view.Record
		previous = &current
	}
	if name == nil && (previous == nil || previous.DisplayName == nil) {
		return nil, "There is no published name to withdraw.", nil
	}
	issued := a.now().UTC().Truncate(time.Second)
	var previousID *string
	if previous != nil {
		id := previous.NameID
		previousID = &id
		if before := nameidAt(*previous); !before.IsZero() && !issued.After(before) {
			issued = before.Add(time.Second)
		}
	}
	key, err := a.keystore().PrivateKey(profile)
	if err != nil {
		return nil, "", err
	}
	record, err := names.Build(key, name, avatar, issued, previousID)
	if err != nil {
		return nil, "The name could not be signed. Check the picture and try again.", nil
	}
	if err := a.nameidUpdate(profile, func(s *nameidState) { s.Record, s.Pending, s.Outcomes = &record, &record, nil }); err != nil {
		return nil, "", err
	}
	outcomes := nameidSend(ctx, choices, clients, record, nameidAll(len(choices)))
	if err := a.nameidUpdate(profile, func(s *nameidState) { s.Outcomes, s.Pending = outcomes, nameidPendingFor(outcomes, &record) }); err != nil {
		return nil, "", err
	}
	return outcomes, "", nil
}

// nameidRetry resends the same signed record to the providers whose last answer was ambiguous.
func (a *App) nameidRetry(ctx context.Context, profile string) ([]nameidOutcome, string, error) {
	state, err := a.nameidLoad(profile)
	if err != nil {
		return nil, "", err
	}
	nothing := "Nothing is waiting to be retried."
	if state.Pending == nil {
		return nil, nothing, nil
	}
	choices := a.selection(profile).Providers
	var indices []int
	for i, choice := range choices {
		if outcome, found := nameidFind(state.Outcomes, choice.Endpoint); found && outcome.Ambiguous {
			indices = append(indices, i)
		}
	}
	if len(indices) == 0 {
		return nil, nothing, nil
	}
	outcomes := nameidSend(ctx, choices, a.nameidClients(choices), *state.Pending, indices)
	if err := a.nameidUpdate(profile, func(s *nameidState) {
		for _, outcome := range outcomes {
			s.Outcomes = nameidReplace(s.Outcomes, outcome)
		}
		s.Pending = nameidPendingFor(s.Outcomes, s.Pending)
	}); err != nil {
		return nil, "", err
	}
	return outcomes, "", nil
}

func (a *App) nameidClients(choices []ProviderChoice) []*sdk.Client {
	clients := make([]*sdk.Client, len(choices))
	for i, choice := range choices {
		if client, err := a.providerClient(identEndpoint(choice)); err == nil {
			clients[i] = client
		}
	}
	return clients
}

// nameidSend publishes record to the providers at indices, at most four at a time. Outcomes follow indices.
func nameidSend(ctx context.Context, choices []ProviderChoice, clients []*sdk.Client, record names.NameRecord, indices []int) []nameidOutcome {
	outcomes := make([]nameidOutcome, len(indices))
	slots := make(chan struct{}, nameidConcurrency)
	var wait sync.WaitGroup
	for k, i := range indices {
		wait.Add(1)
		go func(k, i int) {
			defer wait.Done()
			slots <- struct{}{}
			defer func() { <-slots }()
			outcomes[k] = nameidAttempt(ctx, choices[i], clients[i], record)
		}(k, i)
	}
	wait.Wait()
	return outcomes
}

func nameidAttempt(ctx context.Context, choice ProviderChoice, client *sdk.Client, record names.NameRecord) nameidOutcome {
	var outcome nameidOutcome
	if client == nil {
		outcome = nameidOutcome{Code: "other", Message: "Not published: could not be reached"}
	} else {
		result, err := client.PublishName(ctx, record)
		outcome = nameidClassify(result, err)
	}
	outcome.Endpoint, outcome.Label = choice.Endpoint, identProviderName(choice)
	return outcome
}

// nameidClassify maps one publish answer to the code the reader sees. An ambiguous answer is one the provider may still act on.
func nameidClassify(result sdk.NamePublishResult, err error) nameidOutcome {
	if err == nil {
		if result == sdk.NamePublished {
			return nameidOutcome{Code: "published"}
		}
		return nameidOutcome{Code: "already_held"}
	}
	if sdk.IsAmbiguous(err) {
		return nameidOutcome{Code: "unknown", Ambiguous: true}
	}
	var providerErr *sdk.Error
	if errors.As(err, &providerErr) {
		switch providerErr.Status {
		case "not_ready":
			return nameidOutcome{Code: "not_ready"}
		case "stale_state":
			return nameidOutcome{Code: "stale_state"}
		case "rate_limited":
			return nameidOutcome{Code: "rate_limited", RetryAfter: int(providerErr.RetryAfter / time.Second)}
		case "invalid_message":
			return nameidOutcome{Code: "invalid"}
		}
	}
	return nameidOutcome{Code: "other", Message: "Not published: " + identSignalWording(sdk.Classify(err))}
}

// nameidAccepted reports whether a provider holds the stored record, by publishing it or already holding it.
func nameidAccepted(outcomes []nameidOutcome) bool {
	for _, outcome := range outcomes {
		if outcome.Code == "published" || outcome.Code == "already_held" {
			return true
		}
	}
	return false
}

func nameidResults(outcomes []nameidOutcome) []nameidResult {
	results := make([]nameidResult, 0, len(outcomes))
	for _, outcome := range outcomes {
		results = append(results, nameidResult{Provider: outcome.Label, Code: outcome.Code, Outcome: nameidWording(outcome)})
	}
	return results
}

func nameidWording(outcome nameidOutcome) string {
	switch outcome.Code {
	case "published":
		return "Published"
	case "already_held":
		return "Already held"
	case "not_ready":
		return "Not accepted yet: this provider needs at least one of your posts first."
	case "stale_state":
		return "This provider holds a newer name record for your key."
	case "rate_limited":
		return "Name changes are limited here. Try again in " + nameidWait(outcome.RetryAfter) + "."
	case "invalid":
		return "Rejected as invalid."
	case "unknown":
		return "Unknown result: the provider did not answer in time."
	default:
		return outcome.Message
	}
}

// nameidWait formats a wait in whole hours and minutes, rounding up to the minute.
func nameidWait(seconds int) string {
	minutes := (seconds + 59) / 60
	return fmt.Sprintf("%d h %d min", minutes/60, minutes%60)
}

func nameidPendingFor(outcomes []nameidOutcome, record *names.NameRecord) *names.NameRecord {
	for _, outcome := range outcomes {
		if outcome.Ambiguous {
			return record
		}
	}
	return nil
}

func nameidFind(outcomes []nameidOutcome, endpoint string) (nameidOutcome, bool) {
	for _, outcome := range outcomes {
		if outcome.Endpoint == endpoint {
			return outcome, true
		}
	}
	return nameidOutcome{}, false
}

func nameidReplace(outcomes []nameidOutcome, next nameidOutcome) []nameidOutcome {
	for i := range outcomes {
		if outcomes[i].Endpoint == next.Endpoint {
			outcomes[i] = next
			return outcomes
		}
	}
	return append(outcomes, next)
}

func nameidAll(n int) []int {
	indices := make([]int, n)
	for i := range indices {
		indices[i] = i
	}
	return indices
}

// nameidHolding says what one provider holds, relative to the selected record.
func nameidHolding(view sdk.NameView, source sdk.NameSource) string {
	switch {
	case source.NameID != "" && source.Behind:
		return "Holds an older name"
	case source.NameID != "" && view.Withdrawn:
		return "Holds your withdrawal"
	case source.NameID != "":
		return "Holds your current name"
	case source.Signal == sdk.SignalNotFound:
		return "No name here"
	default:
		return identSignalWording(source.Signal)
	}
}

// nameidAvatarOf reads the avatar the reader checked. The publish step signs only the digest shown at that check.
func nameidAvatarOf(form url.Values) (*names.Avatar, string) {
	raw := strings.TrimSpace(form.Get("avatar"))
	if raw == "" {
		return nil, ""
	}
	if strings.TrimSpace(form.Get("avatar_checked")) != raw {
		return nil, "Check the picture before you publish it."
	}
	size, err := strconv.ParseInt(form.Get("avatar_size"), 10, 64)
	if err != nil {
		return nil, "Check the picture again: its size is not valid."
	}
	return &names.Avatar{URL: raw, SHA256: form.Get("avatar_sha256"), SizeBytes: size}, ""
}

// nameidFetchPicture fetches an avatar once, in isolation, and checks that it is an image within the avatar size limit.
func nameidFetchPicture(ctx context.Context, raw string) (nameidPicture, string) {
	if !strings.HasPrefix(raw, "https://") {
		return nameidPicture{}, "The picture address must start with https://."
	}
	if len(raw) > names.MaxAvatarURLLength || content.ValidateExternalURL(raw) != nil {
		return nameidPicture{}, "This address cannot be used for a picture."
	}
	request, err := content.NewFetchRequest(ctx, raw)
	if err != nil {
		return nameidPicture{}, "This address cannot be used for a picture."
	}
	response, err := nameidAvatarClient().Do(request)
	if err != nil {
		return nameidPicture{}, "The picture could not be fetched."
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nameidPicture{}, fmt.Sprintf("The picture could not be fetched: the host answered HTTP %d.", response.StatusCode)
	}
	data, err := content.ReadLimited(response.Body, names.MaxAvatarBytes)
	switch {
	case errors.Is(err, content.ErrResponseTooLarge):
		return nameidPicture{}, fmt.Sprintf("The picture is larger than %d bytes.", names.MaxAvatarBytes)
	case err != nil:
		return nameidPicture{}, "The picture could not be read."
	case len(data) == 0:
		return nameidPicture{}, "The picture is empty."
	}
	kind, err := content.SniffMediaType(data)
	if err != nil || !strings.HasPrefix(string(kind), "image/") {
		return nameidPicture{}, "This address does not serve a PNG, JPEG, GIF or WebP picture."
	}
	_, _, frames, err := content.CheckImageBudget(data, kind, content.DefaultMediaBudget())
	if err != nil {
		return nameidPicture{}, "The picture is too large to show."
	}
	if frames > 1 {
		return nameidPicture{}, "This picture is animated. Choose a still picture."
	}
	sum := sha256.Sum256(data)
	return nameidPicture{
		URL:    raw,
		SHA256: hex.EncodeToString(sum[:]),
		Size:   int64(len(data)),
		Data:   template.URL("data:" + string(kind) + ";base64," + base64.StdEncoding.EncodeToString(data)),
	}, ""
}

// nameidCheckName normalizes a typed name to NFC and names the first rule it breaks. The problem is empty for a valid name.
func nameidCheckName(raw string) (string, string) {
	if !utf8.ValidString(raw) {
		return "", "Contains text that is not valid"
	}
	normalized := norm.NFC.String(raw)
	scalars := utf8.RuneCountInString(normalized)
	switch {
	case normalized == "":
		return "", "Enter 1 to 64 characters"
	case scalars > names.MaxNameScalars:
		return "", fmt.Sprintf("Too long: %d of %d characters", scalars, names.MaxNameScalars)
	case len(normalized) > names.MaxNameBytes:
		return "", fmt.Sprintf("Too long: %d of %d bytes", len(normalized), names.MaxNameBytes)
	}
	for _, r := range normalized {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp) {
			return "", "Contains a control or invisible character"
		}
	}
	first, _ := utf8.DecodeRuneInString(normalized)
	last, _ := utf8.DecodeLastRuneInString(normalized)
	if unicode.Is(unicode.White_Space, first) || unicode.Is(unicode.White_Space, last) {
		return "", "Starts or ends with a space"
	}
	if strings.Contains(normalized, "  ") {
		return "", "Contains two spaces in a row"
	}
	if names.ValidateName(normalized) != nil {
		return "", "Contains a character that is not allowed"
	}
	return normalized, ""
}

func nameidPreviewOf(raw, fingerprint string) nameidPreview {
	if raw == "" {
		return nameidPreview{Empty: true, Fingerprint: fingerprint}
	}
	name, problem := nameidCheckName(raw)
	if problem != "" {
		return nameidPreview{Problem: problem, Fingerprint: fingerprint}
	}
	return nameidPreview{Name: name, Changed: name != raw, Fingerprint: fingerprint}
}

func nameidPublishedOf(record names.NameRecord) nameidPublished {
	published := nameidPublished{NameID: record.NameID, Withdrawn: record.DisplayName == nil, IssuedAt: nameidAt(record).UTC()}
	if record.DisplayName != nil {
		published.Name = *record.DisplayName
	}
	return published
}

// nameidAt is the signing time of a stored record, or the zero time when it cannot be read.
func nameidAt(record names.NameRecord) time.Time {
	issued, err := wire.ParseTime(record.IssuedAt)
	if err != nil {
		return time.Time{}
	}
	return issued
}
