package server

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode"
	"unicode/utf8"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

func init() { routeGroups = append(routeGroups, reportRegister) }

func reportRegister(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /report", a.reportPageHandler)
	mux.HandleFunc("GET /report/fragment", a.reportFragmentHandler)
	mux.HandleFunc("POST /report", a.reportSubmitHandler)
	mux.HandleFunc("POST /report/fragment", a.reportSubmitHandler)
	mux.HandleFunc("POST /report/resend", a.reportResendHandler)
	mux.HandleFunc("POST /report/resend/fragment", a.reportResendHandler)
	mux.HandleFunc("GET /report/check", a.reportCheckPage)
	mux.HandleFunc("POST /report/check", a.reportCheckSubmit)
	mux.HandleFunc("POST /report/check/fragment", a.reportCheckSubmit)
}

// reportOption is one choice in a select or a radio group.
type reportOption struct {
	Value, Label string
	Selected     bool
}

// reportProviderView names the provider a report goes to.
type reportProviderView struct {
	Label, Host, ShortKey, FullKey string
}

// reportKindOption is one choice in step 1, with what the provider says about it.
type reportKindOption struct {
	Kind, Name, Summary, Reason string
	Offered, Unavailable        bool
}

type reportContactRow struct {
	Name, Value string
}

// reportContactsView is the contacts fallback, from the verified manifest only.
type reportContactsView struct {
	Label, PostID, Reason, TryURL, Expires string
	Heading, Verified                      bool
	Rows                                   []reportContactRow
	Version                                int64
}

// reportFieldView is one regional field of an intake form.
type reportFieldView struct {
	Name, Help, Value, Err string
	Required               bool
	Max                    int64
}

// reportFormView is step 2: the form the reader fills in.
type reportFormView struct {
	Kind, Heading, PostID, FormID, FormVersion, Digest, Instructions  string
	StatementLabel, StatementHelp, Label, Notice, Locators, Statement string
	Contact, Right, Decision                                          string
	LongInstructions, IsRights, IsAppeal, Understood, Summary         bool
	Rights                                                            []reportOption
	Fields                                                            []reportFieldView
	Errors                                                            map[string]string
}

// reportReceiptView is what the provider answered, with the secret when the reader needs it.
type reportReceiptView struct {
	Success, ShowSecret, ShowResend, ShowStart, ShowBack bool
	PostID, Kind, KindName, Label, ReceivedAt, RequestID string
	RequestDigest, ProviderKey, ProviderShort, Secret    string
	Notice, NoticeClass, Note, ResendRequest, ResendKey  string
}

// reportFlow is the step the reader is on. Stage is one of demo, notfound,
// unverified, notselected, kinds, form or receipt.
type reportFlow struct {
	ID, Kind, Stage string
	Provider        *reportProviderView
	Admitted        *reportProviderView
	Kinds           []reportKindOption
	HasKinds        bool
	Contacts        *reportContactsView
	Error, Warn     string
	Form            *reportFormView
	Receipt         *reportReceiptView
}

type reportPageData struct {
	Layout  Layout
	ID      string
	Post    post
	HasPost bool
	Flow    reportFlow
}

type reportEntryView struct {
	Kind, Provider, RequestID, Received string
}

type reportProviderOption struct {
	Key, Label string
	Selected   bool
}

// reportCheckResult is the outcome of checking one request with its secret.
type reportCheckResult struct {
	FieldError, Title, Body, Notice string
	Verified                        bool
	Received, Kind, Statement       string
	Locators                        string
}

type reportCheckData struct {
	Layout    Layout
	Entries   []reportEntryView
	Damaged   bool
	Providers []reportProviderOption
	KeyError  string
	Result    reportCheckResult
}

// reportHost returns the host of a provider endpoint.
func reportHost(endpoint string) string {
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		return u.Host
	}
	return endpoint
}

// reportProviderFor describes a selected provider for the report pages.
func reportProviderFor(choice ProviderChoice) *reportProviderView {
	host := reportHost(choice.Endpoint)
	label := choice.Label
	if label == "" {
		label = host
	}
	return &reportProviderView{Label: label, Host: host, ShortKey: shortDID(choice.OperatorKey), FullKey: choice.OperatorKey}
}

// reportFormatTime shows a provider timestamp in UTC for the reader.
func reportFormatTime(value string) string {
	when, err := wire.ParseTime(value)
	if err != nil {
		return value
	}
	return when.UTC().Format("2 Jan 2006, 15:04 UTC")
}

// reportSentence capitalises the first letter of a reason and ends it with a full stop.
func reportSentence(text string) string {
	if text == "" {
		return ""
	}
	first, size := utf8.DecodeRuneInString(text)
	text = string(unicode.ToUpper(first)) + text[size:]
	if !strings.HasSuffix(text, ".") {
		text += "."
	}
	return text
}

func (a *App) reportValidRequest(w http.ResponseWriter, r *http.Request, id, kind string) bool {
	if !canonicalMessageID(id) {
		a.fail(w, r, http.StatusBadRequest, "That is not a post ID.")
		return false
	}
	if kind != "" {
		if _, ok := reportKindByName(kind); !ok {
			a.fail(w, r, http.StatusBadRequest, "Unknown report kind.")
			return false
		}
	}
	return true
}

// reportPostForm checks a same-origin form and that each field appears once.
func reportPostForm(w http.ResponseWriter, r *http.Request) (url.Values, bool) {
	if !requireSameOrigin(w, r) || !parseSmallForm(w, r, reportPostLimit) {
		return nil, false
	}
	for _, values := range r.PostForm {
		if len(values) != 1 {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return nil, false
		}
	}
	return r.PostForm, true
}

func (a *App) reportPageHandler(w http.ResponseWriter, r *http.Request) {
	a.reportGet(w, r, false)
}

func (a *App) reportFragmentHandler(w http.ResponseWriter, r *http.Request) {
	a.reportGet(w, r, true)
}

func (a *App) reportGet(w http.ResponseWriter, r *http.Request, fragment bool) {
	id, kind := r.URL.Query().Get("id"), r.URL.Query().Get("kind")
	if !a.reportValidRequest(w, r, id, kind) {
		return
	}
	flow := a.reportFlowFor(r, id, kind, r.URL.Query().Get("step") == "choose")
	if fragment {
		a.fragment(w, r, "report-flow", flow)
		return
	}
	a.page(w, r, "report-page", a.reportPage(r, id, flow))
}

func (a *App) reportPage(r *http.Request, id string, flow reportFlow) reportPageData {
	data := reportPageData{Layout: a.layout(r, "report", "Report a post"), ID: id, Flow: flow}
	if p, ok := a.requestPost(r, id); ok {
		data.Post, data.HasPost = p, true
	}
	return data
}

// reportRespond answers a POST with the flow, as a fragment or a full page.
func (a *App) reportRespond(w http.ResponseWriter, r *http.Request, id string, flow reportFlow, status int) {
	if strings.HasSuffix(r.URL.Path, "/fragment") {
		a.renderStatus(w, r, "report-flow", flow, status)
		return
	}
	a.renderStatus(w, r, "report-page", a.reportPage(r, id, flow), status)
}

// reportSetStage puts the resolution into the flow. It returns true when the
// provider is known and the reader can go on.
func reportSetStage(flow *reportFlow, resolution reportResolution) bool {
	switch resolution.Outcome {
	case reportOutcomeProvider:
		flow.Provider = reportProviderFor(resolution.Choice)
		return true
	case reportOutcomeNotSelected:
		flow.Stage = "notselected"
		flow.Admitted = &reportProviderView{ShortKey: shortDID(resolution.Admitted), FullKey: resolution.Admitted}
	default:
		flow.Stage = resolution.Outcome
	}
	return false
}

// reportFlowFor builds the flow for a GET: the choice of kind, or the form when a kind is given and offered.
func (a *App) reportFlowFor(r *http.Request, id, kind string, choose bool) reportFlow {
	ctx := r.Context()
	flow := reportFlow{ID: id, Kind: kind}
	resolution := a.reportResolve(ctx, requestProfile(r), id)
	if !reportSetStage(&flow, resolution) {
		return flow
	}
	choice := resolution.Choice
	if k, ok := reportKindByName(kind); ok {
		if state := a.reportFormFor(ctx, choice, k); state.State == "offered" {
			flow.Stage = "form"
			flow.Form = reportFormViewFor(state, choice, id, nil, nil, "")
			return flow
		}
	}
	a.reportKindsStage(ctx, &flow, choice)
	if choose && kind == "" {
		flow.Error = "Choose what to send."
	}
	return flow
}

// reportFormFor fetches one form within the five-second budget.
func (a *App) reportFormFor(ctx context.Context, choice ProviderChoice, kind reportKind) reportFormState {
	ctx, cancel := context.WithTimeout(ctx, reportResolveBudget)
	defer cancel()
	return a.reportFetchForm(ctx, choice, kind)
}

// reportKindsStage fills step 1: the kinds the provider offers, and the contacts.
func (a *App) reportKindsStage(ctx context.Context, flow *reportFlow, choice ProviderChoice) {
	states := a.reportOffers(ctx, choice)
	manifest := a.reportManifest(ctx, choice)
	flow.Stage = "kinds"
	flow.Provider = reportProviderFor(choice)
	flow.Kinds, flow.HasKinds = nil, false
	for _, state := range states {
		option := reportKindOption{Kind: state.Kind.Kind, Name: state.Kind.Name, Summary: state.Kind.Summary}
		switch state.State {
		case "offered":
			option.Offered = true
			flow.HasKinds = true
		case "notoffered":
			option.Reason = "This provider does not take this kind of report."
		default:
			option.Unavailable = true
			option.Reason = reportSentence(state.Reason)
		}
		flow.Kinds = append(flow.Kinds, option)
	}
	flow.Contacts = reportContactsFor(choice, manifest, flow.ID, !flow.HasKinds)
}

// reportContactsFor builds the contacts fallback from a verified manifest.
func reportContactsFor(choice ProviderChoice, manifest reportManifestState, id string, main bool) *reportContactsView {
	view := &reportContactsView{Label: reportProviderFor(choice).Label, PostID: id, Heading: main, TryURL: "/report?id=" + id, Verified: manifest.Verified}
	if !manifest.Verified {
		view.Reason = manifest.Reason
		return view
	}
	m := manifest.Manifest
	view.Rows = []reportContactRow{
		{Name: "Abuse", Value: reportOr(m.AbuseContact)},
		{Name: "Legal", Value: reportOr(m.LegalContact)},
		{Name: "Privacy", Value: reportOr(m.PrivacyContact)},
		{Name: "Appeals and claims", Value: reportOr(m.AppealURL)},
	}
	view.Version = m.ManifestVersion
	if expires, err := wire.ParseTime(m.ExpiresAt); err == nil {
		view.Expires = expires.UTC().Format("2 Jan 2006")
	}
	return view
}

func reportOr(value string) string {
	if value == "" {
		return "Not published"
	}
	return value
}

// reportFormViewFor prepares step 2 from a form the provider offers and the reader's answers.
func reportFormViewFor(state reportFormState, choice ProviderChoice, id string, values url.Values, errs map[string]string, notice string) *reportFormView {
	view := &reportFormView{
		Kind: state.Kind.Kind, Heading: state.Kind.Heading, PostID: id, Label: reportProviderFor(choice).Label,
		FormID: state.Form.FormID, FormVersion: state.Form.FormVersion, Digest: state.Digest,
		Instructions: state.Form.Instructions, LongInstructions: len(state.Form.Instructions) > reportLongInstruction,
		StatementLabel: state.Kind.StatementLabel, StatementHelp: state.Kind.StatementHelp,
		Notice: notice, Locators: values.Get("locators"), Statement: values.Get("statement"),
		Contact: values.Get("contact"), Right: values.Get("requested_right"), Decision: values.Get("decision_reference"),
		Understood: values.Get("understood") == "1", Errors: errs,
		IsRights: state.Kind.Kind == "rights_request", IsAppeal: state.Kind.Kind == "appeal",
	}
	for _, option := range reportRights {
		option.Selected = option.Value == view.Right
		view.Rights = append(view.Rights, option)
	}
	for _, field := range state.Form.Fields {
		view.Fields = append(view.Fields, reportFieldView{
			Name: field.Name, Help: field.Description, Value: values.Get("field_" + field.Name),
			Err: errs["field_"+field.Name], Required: field.Required, Max: field.MaxUTF8Bytes,
		})
	}
	return view
}

// reportSend resolves the post, checks the posted answers against a freshly
// fetched form, sends the report and returns the flow with its status.
func (a *App) reportSend(r *http.Request, profile, id, kind string, values url.Values) (reportFlow, int) {
	ctx := r.Context()
	flow := reportFlow{ID: id, Kind: kind}
	resolution := a.reportResolve(ctx, profile, id)
	if !reportSetStage(&flow, resolution) {
		return flow, http.StatusOK
	}
	choice := resolution.Choice
	k, _ := reportKindByName(kind)
	state := a.reportFormFor(ctx, choice, k)
	if state.State != "offered" {
		a.reportKindsStage(ctx, &flow, choice)
		flow.Warn = "This kind of report is not offered by " + reportProviderFor(choice).Label + " right now."
		return flow, http.StatusOK
	}
	if state.Digest != values.Get("form_digest") {
		flow.Stage = "form"
		flow.Form = reportFormViewFor(state, choice, id, values, nil, "The provider changed this form while you were writing. Check it and send again.")
		return flow, http.StatusOK
	}
	input, errs := reportReadInput(values, state.Form, k, id)
	if len(errs) > 0 {
		return a.reportFormStage(flow, state, choice, values, errs), http.StatusUnprocessableEntity
	}
	request, secret, err := reportNewRequest(a.now(), choice.OperatorKey, state, input)
	if err != nil {
		return a.reportFormStage(flow, state, choice, values, map[string]string{"statement": "This report could not be prepared. Try again."}), http.StatusUnprocessableEntity
	}
	requestBytes, err := wire.Encode("IntakeRequest", request)
	if err != nil {
		return a.reportFormStage(flow, state, choice, values, map[string]string{"statement": "This report could not be prepared. Try again."}), http.StatusUnprocessableEntity
	}
	if len(requestBytes) > reportPostLimit {
		return a.reportFormStage(flow, state, choice, values, map[string]string{"statement": "This report is larger than the provider's 256 KiB limit."}), http.StatusUnprocessableEntity
	}
	answer := a.reportSubmitRequest(ctx, choice, k, requestBytes, secret, request.RequestID)
	flow.Stage = "receipt"
	view := a.reportReceiptFor(answer, choice, profile, id, request, requestBytes, secret)
	flow.Receipt = &view
	return flow, http.StatusOK
}

// reportFormStage returns the form with the reader's answers and their errors.
func (a *App) reportFormStage(flow reportFlow, state reportFormState, choice ProviderChoice, values url.Values, errs map[string]string) reportFlow {
	flow.Stage = "form"
	flow.Form = reportFormViewFor(state, choice, flow.ID, values, errs, "")
	flow.Form.Summary = true
	return flow
}

// reportReceiptFor says what the provider answered. The recovery secret is shown
// wherever the provider may have stored the request, and is never stored here.
func (a *App) reportReceiptFor(answer reportAnswer, choice ProviderChoice, profile, id string, request wire.IntakeRequest, requestBytes, secret []byte) reportReceiptView {
	kind, _ := reportKindByName(request.Kind)
	view := reportReceiptView{
		PostID: id, Kind: request.Kind, KindName: kind.Name, Label: reportProviderFor(choice).Label,
		RequestID: request.RequestID, ProviderKey: choice.OperatorKey, ProviderShort: shortDID(choice.OperatorKey),
	}
	showResend := func() {
		view.Secret = keys.EncodeB64URL(secret)
		view.ShowSecret, view.ShowResend = true, true
		view.ResendRequest, view.ResendKey = reportEncodeResend(requestBytes, secret)
	}
	switch answer.Outcome {
	case reportAnswerReceived:
		view.Success = true
		view.ReceivedAt = reportFormatTime(answer.Receipt.ReceivedAt)
		view.RequestDigest = answer.Receipt.RequestDigest
		view.Secret, view.ShowSecret = keys.EncodeB64URL(secret), true
		entry := reportEntry{
			MessageID: id, Kind: request.Kind, ProviderEndpoint: choice.Endpoint, OperatorID: choice.OperatorKey,
			RequestID: request.RequestID, FormID: request.FormID, FormVersion: request.FormVersion,
			FormDigest: request.FormDigest, RequestDigest: answer.Receipt.RequestDigest, ReceivedAt: answer.Receipt.ReceivedAt,
		}
		if err := a.reportAdd(profile, entry); err != nil {
			view.Note = "This report could not be added to your list on this server."
		}
	case reportAnswerUnverified:
		view.NoticeClass = "notice-bad"
		view.Notice = "The provider's answer did not verify, so it is not a receipt. Your report may or may not have arrived. Keep the secret below and check later."
		view.ShowSecret = true
		view.Secret = keys.EncodeB64URL(secret)
	case reportAnswerUnclear:
		view.NoticeClass = "notice-warn"
		view.Notice = "It is not known whether the provider received this. Send the same report again, or keep the secret and check later."
		if reportIsRedirect(answer.Status) {
			view.Notice = "The provider answered with a redirect, which intake does not allow. " + view.Notice
		}
		showResend()
	case reportAnswerBusy:
		view.NoticeClass = "notice-warn"
		retry := "after a short wait"
		if answer.RetryAfter > 0 {
			retry = fmt.Sprintf("after %d seconds", answer.RetryAfter)
		}
		view.Notice = "The provider is busy. Send the same report again " + retry + "."
		showResend()
	case reportAnswerExpired:
		view.NoticeClass = "notice-bad"
		view.Notice = "The time to send this report ran out before the provider stored it. Nothing was received. Start again."
		view.ShowStart = true
	case reportAnswerConflict:
		view.NoticeClass = "notice-bad"
		view.Notice = "The provider already holds a different request under this ID. Nothing was changed. Start again."
		view.ShowStart = true
	default:
		view.NoticeClass = "notice-bad"
		view.Notice = fmt.Sprintf("The provider refused this report (HTTP %d).", answer.Status)
		view.ShowBack = true
	}
	return view
}

func (a *App) reportResendHandler(w http.ResponseWriter, r *http.Request) {
	values, ok := reportPostForm(w, r)
	if !ok {
		return
	}
	requestBytes, err := base64.RawURLEncoding.DecodeString(values.Get("request"))
	if err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	secret, err := keys.DecodeB64URL(values.Get("key"), 32)
	if err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	var request wire.IntakeRequest
	if err := wire.DecodeLimits(requestBytes, canon.Limits{MaxBytes: reportPostLimit, MaxDepth: 8}, "IntakeRequest", &request); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	if derived, err := reportDeriveRequestID(request.OperatorID, secret); err != nil || derived != request.RequestID {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	profile, err := a.profile(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The report could not be sent.")
		return
	}
	choice, ok := a.reportProviderChoice(profile, request.OperatorID)
	if !ok {
		a.fail(w, r, http.StatusBadRequest, "That provider is not one of your providers.")
		return
	}
	kind, ok := reportKindByName(request.Kind)
	if !ok || len(request.Locators) == 0 || !canonicalMessageID(request.Locators[0]) {
		a.fail(w, r, http.StatusBadRequest, "That is not a post ID.")
		return
	}
	id := request.Locators[0]
	flow := reportFlow{ID: id, Kind: kind.Kind, Stage: "receipt", Provider: reportProviderFor(choice)}
	answer := reportAnswer{Outcome: reportAnswerExpired}
	if acceptUntil, err := wire.ParseTime(request.AcceptUntil); err == nil && a.now().Before(acceptUntil) {
		answer = a.reportSubmitRequest(r.Context(), choice, kind, requestBytes, secret, request.RequestID)
	}
	view := a.reportReceiptFor(answer, choice, profile, id, request, requestBytes, secret)
	flow.Receipt = &view
	a.reportRespond(w, r, id, flow, http.StatusOK)
}

func (a *App) reportSubmitHandler(w http.ResponseWriter, r *http.Request) {
	values, ok := reportPostForm(w, r)
	if !ok {
		return
	}
	id, kind := values.Get("id"), values.Get("kind")
	if _, known := reportKindByName(kind); !known {
		a.fail(w, r, http.StatusBadRequest, "Unknown report kind.")
		return
	}
	if !a.reportValidRequest(w, r, id, kind) {
		return
	}
	profile, err := a.profile(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The report could not be sent.")
		return
	}
	flow, status := a.reportSend(r, profile, id, kind, values)
	a.reportRespond(w, r, id, flow, status)
}

func (a *App) reportCheckPage(w http.ResponseWriter, r *http.Request) {
	a.page(w, r, "report-check-page", a.reportCheckPageData(r, requestProfile(r), r.URL.Query().Get("request"), "", reportCheckResult{}))
}

// reportCheckPageData lists the profile's reports and prepares the check form.
func (a *App) reportCheckPageData(r *http.Request, profile, requestID, providerKey string, result reportCheckResult) reportCheckData {
	entries, damaged := a.reportList(profile)
	selection := a.selection(profile).Providers
	data := reportCheckData{Layout: a.layout(r, "report", "Check a report"), Damaged: damaged, Result: result}
	now := a.now()
	for _, entry := range entries {
		if providerKey == "" && entry.RequestID == requestID {
			providerKey = entry.OperatorID
		}
		kind, _ := reportKindByName(entry.Kind)
		provider := reportHost(entry.ProviderEndpoint)
		for _, choice := range selection {
			if choice.OperatorKey == entry.OperatorID {
				provider = reportProviderFor(choice).Label
			}
		}
		received, _ := wire.ParseTime(entry.ReceivedAt)
		data.Entries = append(data.Entries, reportEntryView{Kind: kind.Name, Provider: provider, RequestID: entry.RequestID, Received: relativeTime(received, now)})
	}
	for _, choice := range selection {
		data.Providers = append(data.Providers, reportProviderOption{Key: choice.OperatorKey, Label: reportProviderFor(choice).Label, Selected: choice.OperatorKey == providerKey})
	}
	return data
}

func (a *App) reportCheckSubmit(w http.ResponseWriter, r *http.Request) {
	values, ok := reportPostForm(w, r)
	if !ok {
		return
	}
	profile := requestProfile(r)
	choice, found := a.reportProviderChoice(profile, values.Get("provider"))
	if !found {
		a.fail(w, r, http.StatusBadRequest, "That provider is not one of your providers.")
		return
	}
	var result reportCheckResult
	if secret, err := keys.DecodeB64URL(values.Get("key"), 32); err != nil {
		result.FieldError = "A recovery secret is 43 characters of letters, digits, - and _."
	} else {
		result = a.reportCheckOutcome(r.Context(), profile, choice, secret)
	}
	if strings.HasSuffix(r.URL.Path, "/fragment") {
		a.fragment(w, r, "report-check-result", result)
		return
	}
	page := a.reportCheckPageData(r, profile, "", choice.OperatorKey, result)
	page.KeyError, page.Result.FieldError = result.FieldError, ""
	a.page(w, r, "report-check-page", page)
}

// reportCheckOutcome asks the provider for a request and says what it verified.
func (a *App) reportCheckOutcome(ctx context.Context, profile string, choice ProviderChoice, secret []byte) reportCheckResult {
	recovery := a.reportRecover(ctx, choice, secret)
	label := reportProviderFor(choice).Label
	switch recovery.Outcome {
	case "verified":
		entries, _ := a.reportList(profile)
		if reportDigestMatches(entries, recovery.Request.RequestID, recovery.Receipt.RequestDigest) {
			kind, _ := reportKindByName(recovery.Request.Kind)
			return reportCheckResult{
				Title: label + " still holds this request", Verified: true,
				Received: reportFormatTime(recovery.Receipt.ReceivedAt), Kind: kind.Name,
				Statement: recovery.Request.Statement, Locators: strings.Join(recovery.Request.Locators, "\n"),
			}
		}
		return reportCheckResult{Title: "The provider's answer did not verify", Notice: "Treat this as no answer."}
	case "unverified":
		return reportCheckResult{Title: "The provider's answer did not verify", Notice: "Treat this as no answer."}
	case "notfound":
		return reportCheckResult{Title: "No request to show", Body: "The provider has no request it will show for this secret. The secret may be wrong, the provider may no longer keep the request, or it may not be allowed to show it. These look the same on purpose."}
	default:
		return reportCheckResult{Title: "Could not reach the provider", Body: "Nothing was checked. Try again later."}
	}
}

// reportDigestMatches reports whether a local entry for the request, if any, names the same receipt digest.
func reportDigestMatches(entries []reportEntry, requestID, digest string) bool {
	for _, entry := range entries {
		if entry.RequestID == requestID && entry.RequestDigest != digest {
			return false
		}
	}
	return true
}
