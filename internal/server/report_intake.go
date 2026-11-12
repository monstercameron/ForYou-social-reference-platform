package server

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/wire"
)

const (
	reportResolveBudget   = 5 * time.Second
	reportCallTimeout     = 10 * time.Second
	reportFormLimit       = 1 << 20
	reportReplyLimit      = 65536
	reportPostLimit       = 262144
	reportAcceptWindow    = 300 * time.Second
	reportMaxFields       = 32
	reportMaxLocators     = 15
	reportMaxLocator      = 4096
	reportMaxStatement    = 65536
	reportMaxDecision     = 4096
	reportMaxContact      = 320
	reportLongInstruction = 600
	reportIntakeKeyHeader = "ForYou-Intake-Key"
)

const (
	reportOutcomeDemo        = "demo"
	reportOutcomeNotFound    = "notfound"
	reportOutcomeUnverified  = "unverified"
	reportOutcomeNotSelected = "notselected"
	reportOutcomeProvider    = "provider"
)

const (
	reportAnswerReceived   = "received"
	reportAnswerUnverified = "unverified"
	reportAnswerUnclear    = "unclear"
	reportAnswerBusy       = "busy"
	reportAnswerExpired    = "expired"
	reportAnswerConflict   = "conflict"
	reportAnswerRefused    = "refused"
)

var (
	reportErrReceipt = errors.New("the receipt does not describe this request")
	reportErrForm    = errors.New("the form is not the one this report needs")
)

// reportKind describes one kind of report. The table's FormID is the form ID the
// reference provider publishes. The specification has operators publish their
// form IDs; replace FormID with the operator's published list once the provider
// discloses one (#114).
type reportKind struct {
	Kind, Name, Summary, Heading, FormID, Route string
	StatementLabel, StatementHelp               string
}

var reportKinds = []reportKind{
	{Kind: "notice", Name: "Notice", Summary: "Tell the provider this post may break the law or its rules.", Heading: "Send a notice", FormID: "notice", Route: "notices", StatementLabel: "What is wrong", StatementHelp: "Plain text, up to 64 KiB. Only the provider reads this; it is not published."},
	{Kind: "rights_request", Name: "Data-rights request", Summary: "Ask about personal data about you in this post: access, a copy, correction, erasure or restriction.", Heading: "Send a data-rights request", FormID: "rights", Route: "rights-requests", StatementLabel: "What you are asking for", StatementHelp: "Plain text, up to 64 KiB. Only the provider reads this; it is not published."},
	{Kind: "appeal", Name: "Appeal", Summary: "Ask the provider to review a decision it made about this post or your account.", Heading: "Send an appeal", FormID: "appeal", Route: "appeals", StatementLabel: "Why the decision should change", StatementHelp: "Plain text, up to 64 KiB. Only the provider reads this; it is not published."},
}

// reportRights lists the rights a data-rights request can ask for.
var reportRights = []reportOption{
	{Value: "access", Label: "Access to the data"},
	{Value: "export", Label: "A copy to take elsewhere"},
	{Value: "correction", Label: "Correction"},
	{Value: "erasure", Label: "Erasure"},
	{Value: "restriction", Label: "Restriction of use"},
}

func reportKindByName(name string) (reportKind, bool) {
	for _, kind := range reportKinds {
		if kind.Kind == name {
			return kind, true
		}
	}
	return reportKind{}, false
}

// reportHTTPClient returns the client intake calls use: a shallow copy of the
// configured client that never follows redirects and waits at most 10 seconds.
func (a *App) reportHTTPClient() *http.Client {
	base := a.config.HTTPClient
	if base == nil {
		base = http.DefaultClient
	}
	client := *base
	client.Timeout = reportCallTimeout
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &client
}

type reportReply struct {
	status      int
	contentType string
	body        []byte
}

// reportCall sends one intake request to a provider. A recovery key travels only
// in the ForYou-Intake-Key header, and no cookie or referrer is sent.
func (a *App) reportCall(ctx context.Context, endpoint, method string, elements []string, body, key []byte, limit int) (reportReply, error) {
	target, err := url.JoinPath(endpoint, elements...)
	if err != nil {
		return reportReply{}, err
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return reportReply{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Accept-Encoding", "identity")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if key != nil {
		request.Header.Set(reportIntakeKeyHeader, keys.EncodeB64URL(key))
	}
	response, err := a.reportHTTPClient().Do(request)
	if err != nil {
		return reportReply{}, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, int64(limit)+1))
	if err != nil {
		return reportReply{}, err
	}
	if len(data) > limit {
		return reportReply{}, reportErrTooLarge
	}
	return reportReply{status: response.StatusCode, contentType: response.Header.Get("Content-Type"), body: data}, nil
}

var reportErrTooLarge = errors.New("the provider's answer is larger than intake allows")

func reportIsRedirect(status int) bool { return status >= 300 && status < 400 }

func reportMediaType(value string) string {
	mediaType, _, _ := mime.ParseMediaType(value)
	return mediaType
}

// reportProviderChoice returns the selected provider with the given key.
func (a *App) reportProviderChoice(profile, operatorKey string) (ProviderChoice, bool) {
	for _, choice := range a.selection(profile).Providers {
		if choice.OperatorKey == operatorKey {
			return choice, true
		}
	}
	return ProviderChoice{}, false
}

// reportResolution says which selected provider admitted a post.
type reportResolution struct {
	Outcome  string
	Choice   ProviderChoice
	Admitted string
}

// reportResolve finds the post on the reader's selected providers and checks
// which provider admitted it. The whole lookup has a five-second budget.
func (a *App) reportResolve(ctx context.Context, profile, id string) reportResolution {
	if _, network := a.networkSource(); !network || !canonicalMessageID(id) {
		return reportResolution{Outcome: reportOutcomeDemo}
	}
	ctx, cancel := context.WithTimeout(ctx, reportResolveBudget)
	defer cancel()
	selected := a.selection(profile).Providers
	for _, choice := range selected {
		client, err := a.providerClient(Endpoint{URL: choice.Endpoint, ServiceID: choice.OperatorKey, Label: choice.Label})
		if err != nil {
			continue
		}
		fetched, err := client.GetMessage(ctx, id)
		if err != nil {
			continue
		}
		return a.reportVerifyFetched(profile, selected, fetched)
	}
	return reportResolution{Outcome: reportOutcomeNotFound}
}

// reportVerifyFetched verifies a fetched post the way the feed does. A post that
// fails only at the fee step, while no trust root is loaded, still names its provider.
func (a *App) reportVerifyFetched(profile string, selected []ProviderChoice, fetched sdk.Fetched) reportResolution {
	record := a.verifier(profile).Verify(fetched.Raw, fetched.ID, sdk.Source{Endpoint: fetched.Endpoint, ServiceID: fetched.ServiceID, Kind: sdk.SourceProvider})
	if record.Class == sdk.ClassInvalid {
		root, _ := a.trustRoot()
		if root != nil || record.Failure == nil || record.Failure.Step != 5 {
			return reportResolution{Outcome: reportOutcomeUnverified}
		}
	}
	admitted := fetched.Message.SubmissionReceipt.ProviderID
	if admitted == "" {
		return reportResolution{Outcome: reportOutcomeUnverified}
	}
	for _, choice := range selected {
		if choice.OperatorKey == admitted {
			return reportResolution{Outcome: reportOutcomeProvider, Choice: choice}
		}
	}
	return reportResolution{Outcome: reportOutcomeNotSelected, Admitted: admitted}
}

// reportManifestState is a provider's signed contact disclosure and whether it verified.
type reportManifestState struct {
	Manifest wire.OperatorManifest
	Verified bool
	Reason   string
}

// reportManifest fetches the operator manifest and checks it against a fresh pin.
// Only a current manifest counts as verified.
func (a *App) reportManifest(ctx context.Context, choice ProviderChoice) reportManifestState {
	ctx, cancel := context.WithTimeout(ctx, reportResolveBudget)
	defer cancel()
	client, err := a.providerClient(Endpoint{URL: choice.Endpoint, ServiceID: choice.OperatorKey, Label: choice.Label})
	if err != nil {
		return reportManifestState{Reason: "the provider address is not usable"}
	}
	manifest, raw, err := client.OperatorManifest(ctx)
	if err != nil {
		return reportManifestState{Reason: "the manifest could not be fetched"}
	}
	switch sdk.NewManifestPins().Check(choice.OperatorKey, raw, manifest, a.now()) {
	case sdk.ManifestCurrent:
		return reportManifestState{Manifest: manifest, Verified: true}
	case sdk.ManifestExpired:
		return reportManifestState{Manifest: manifest, Reason: "the manifest is outside its validity window"}
	case sdk.ManifestBadSignature:
		return reportManifestState{Manifest: manifest, Reason: "its signature does not check out"}
	default:
		return reportManifestState{Manifest: manifest, Reason: "the manifest could not be checked"}
	}
}

// reportFormState is what a provider says about one kind of report.
type reportFormState struct {
	Kind   reportKind
	State  string
	Reason string
	Form   wire.IntakeForm
	Digest string
}

// reportFetchForm fetches the form for one kind. A 404 means the provider does
// not offer that kind; any other failure leaves it unavailable, with a reason.
func (a *App) reportFetchForm(ctx context.Context, choice ProviderChoice, kind reportKind) reportFormState {
	state := reportFormState{Kind: kind, State: "unavailable"}
	reply, err := a.reportCall(ctx, choice.Endpoint, http.MethodGet, []string{"v1", "intake-forms", kind.FormID, "1"}, nil, nil, reportFormLimit)
	switch {
	case err != nil && errors.Is(err, reportErrTooLarge):
		state.Reason = "the form is larger than 1 MiB"
		return state
	case err != nil:
		state.Reason = "the provider could not be reached"
		return state
	case reply.status == http.StatusNotFound:
		state.State = "notoffered"
		return state
	case reportIsRedirect(reply.status):
		state.Reason = "the provider answered with a redirect, which intake does not allow"
		return state
	case reply.status != http.StatusOK:
		state.Reason = fmt.Sprintf("the provider answered HTTP %d", reply.status)
		return state
	case reportMediaType(reply.contentType) != "application/json":
		state.Reason = "the form is not JSON"
		return state
	}
	form, digest, err := reportCheckForm(reply.body, choice, kind)
	if err != nil {
		state.Reason = err.Error()
		return state
	}
	state.State, state.Form, state.Digest = "offered", form, digest
	return state
}

// reportCheckForm accepts a form only when it is exact canonical JSON, decodes
// as an IntakeForm and names this operator, form ID, version 1 and kind.
func reportCheckForm(raw []byte, choice ProviderChoice, kind reportKind) (wire.IntakeForm, string, error) {
	canonical, err := canon.Canonicalize(raw, canon.MessageLimits)
	if err != nil || !bytes.Equal(canonical, raw) {
		return wire.IntakeForm{}, "", errors.New("the form is not exact canonical JSON")
	}
	var form wire.IntakeForm
	if err := wire.Decode(raw, "IntakeForm", &form); err != nil {
		return wire.IntakeForm{}, "", errors.New("the form does not match the specification")
	}
	if form.OperatorID != choice.OperatorKey || form.FormID != kind.FormID || form.FormVersion != "1" || form.Kind != kind.Kind {
		return wire.IntakeForm{}, "", reportErrForm
	}
	if len(form.Fields) > reportMaxFields {
		return wire.IntakeForm{}, "", errors.New("the form asks for more than 32 fields")
	}
	names := make(map[string]bool, len(form.Fields))
	for _, field := range form.Fields {
		if field.Name == "" || names[field.Name] {
			return wire.IntakeForm{}, "", errors.New("the form has an empty or repeated field name")
		}
		names[field.Name] = true
	}
	digest := sha256.Sum256(raw)
	return form, hex.EncodeToString(digest[:]), nil
}

// reportOffers asks the provider for every kind's form within the five-second budget.
func (a *App) reportOffers(ctx context.Context, choice ProviderChoice) []reportFormState {
	ctx, cancel := context.WithTimeout(ctx, reportResolveBudget)
	defer cancel()
	states := make([]reportFormState, 0, len(reportKinds))
	for _, kind := range reportKinds {
		states = append(states, a.reportFetchForm(ctx, choice, kind))
	}
	return states
}

// reportDeriveRequestID returns the request ID a recovery secret gives for an operator.
func reportDeriveRequestID(operatorID string, secret []byte) (string, error) {
	canonical, err := canon.Encode(map[string]string{"operator_id": operatorID, "recovery_key": keys.EncodeB64URL(secret)})
	if err != nil {
		return "", err
	}
	input, err := keys.SigningInput("intake-id", canonical)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(input)
	return keys.EncodeB64URL(digest[:]), nil
}

// reportVerifyReceipt accepts a receipt only when it is signed by the operator
// and binds exactly the bytes that were submitted.
func reportVerifyReceipt(receipt wire.IntakeReceipt, operatorID, requestID string, requestBytes []byte) error {
	digest := sha256.Sum256(requestBytes)
	if receipt.OperatorID != operatorID || receipt.RequestID != requestID || receipt.Status != "received" || receipt.RequestDigest != hex.EncodeToString(digest[:]) {
		return reportErrReceipt
	}
	if _, err := wire.ParseTime(receipt.ReceivedAt); err != nil {
		return err
	}
	signed, err := canon.EncodeWithout(receipt, "signature")
	if err != nil {
		return err
	}
	return keys.Verify(operatorID, "intake-receipt", signed, receipt.Signature)
}

// reportInput is the reader's answer to one form, checked and ready to send.
type reportInput struct {
	Locators  []string
	Statement string
	Right     string
	Decision  string
	Contact   *wire.IntakeContact
	Regional  map[string]string
}

// reportReadInput checks the posted fields against the form. Errors are keyed by
// field name; a regional field's key is its name with the field_ prefix.
func reportReadInput(values url.Values, form wire.IntakeForm, kind reportKind, postID string) (reportInput, map[string]string) {
	errs := map[string]string{}
	input := reportInput{Locators: []string{postID}, Regional: map[string]string{}}
	var others []string
	for _, line := range strings.Split(values.Get("locators"), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case len(line) > reportMaxLocator:
			errs["locators"] = "Each location can be at most 4096 bytes."
		default:
			others = append(others, line)
		}
	}
	if len(others) > reportMaxLocators {
		errs["locators"] = "Give up to 15 other locations."
	}
	input.Locators = append(input.Locators, others...)

	input.Statement = strings.TrimSpace(values.Get("statement"))
	switch {
	case input.Statement == "":
		errs["statement"] = "Write what the provider should read."
	case len(input.Statement) > reportMaxStatement:
		errs["statement"] = "This is over the 64 KiB limit."
	}

	if kind.Kind == "rights_request" {
		input.Right = values.Get("requested_right")
		if !slices.ContainsFunc(reportRights, func(option reportOption) bool { return option.Value == input.Right }) {
			errs["right"] = "Choose the right you are asking for."
		}
	}
	if kind.Kind == "appeal" {
		input.Decision = strings.TrimSpace(values.Get("decision_reference"))
		if input.Decision == "" || len(input.Decision) > reportMaxDecision {
			errs["decision"] = "Give the case or decision ID the provider gave you, up to 4096 bytes."
		}
	}

	if contact := strings.TrimSpace(values.Get("contact")); contact != "" {
		if reportValidEmail(contact) {
			input.Contact = &wire.IntakeContact{Kind: "email", Address: contact}
		} else {
			errs["contact"] = "Enter an address with one @ and no spaces, or leave this empty."
		}
	}

	for _, field := range form.Fields {
		value := strings.TrimSpace(values.Get("field_" + field.Name))
		key := "field_" + field.Name
		switch {
		case value == "" && field.Required:
			errs[key] = "This field is required."
		case int64(len(value)) > field.MaxUTF8Bytes:
			errs[key] = fmt.Sprintf("Use at most %d bytes.", field.MaxUTF8Bytes)
		case value != "":
			input.Regional[field.Name] = value
		}
	}

	if values.Get("understood") != "1" {
		errs["understood"] = "Tick the box to confirm what a receipt means."
	}
	return input, errs
}

// reportValidEmail accepts 1 to 320 visible ASCII bytes with exactly one @.
func reportValidEmail(address string) bool {
	if len(address) < 1 || len(address) > reportMaxContact {
		return false
	}
	at := 0
	for i := 0; i < len(address); i++ {
		c := address[i]
		if c < 0x21 || c > 0x7e {
			return false
		}
		if c == '@' {
			at++
		}
	}
	return at == 1
}

// reportNewRequest builds an IntakeRequest with a fresh recovery secret. The
// caller keeps the secret: it is the only way to check the request later.
func reportNewRequest(now time.Time, operatorID string, state reportFormState, input reportInput) (wire.IntakeRequest, []byte, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return wire.IntakeRequest{}, nil, err
	}
	requestID, err := reportDeriveRequestID(operatorID, secret)
	if err != nil {
		return wire.IntakeRequest{}, nil, err
	}
	request := wire.IntakeRequest{
		OperatorID:        operatorID,
		RequestID:         requestID,
		AcceptUntil:       wire.FormatTime(now.Add(reportAcceptWindow)),
		Kind:              state.Kind.Kind,
		FormID:            state.Form.FormID,
		FormVersion:       state.Form.FormVersion,
		FormDigest:        state.Digest,
		Locators:          input.Locators,
		Statement:         input.Statement,
		EvidenceRefs:      []wire.IntakeEvidenceRef{},
		RegionalFields:    input.Regional,
		Contact:           input.Contact,
		RequestedRight:    input.Right,
		DecisionReference: input.Decision,
	}
	return request, secret, nil
}

// reportAnswer is what a provider answered to one submission.
type reportAnswer struct {
	Outcome    string
	Receipt    wire.IntakeReceipt
	Status     int
	RetryAfter int
}

// reportSubmitRequest sends the exact request bytes to the route for their kind.
func (a *App) reportSubmitRequest(ctx context.Context, choice ProviderChoice, kind reportKind, requestBytes, secret []byte, requestID string) reportAnswer {
	reply, err := a.reportCall(ctx, choice.Endpoint, http.MethodPost, []string{"v1", kind.Route}, requestBytes, secret, reportReplyLimit)
	if err != nil {
		if errors.Is(err, reportErrTooLarge) {
			return reportAnswer{Outcome: reportAnswerUnverified}
		}
		return reportAnswer{Outcome: reportAnswerUnclear}
	}
	answer := reportAnswer{Status: reply.status}
	switch {
	case reply.status == http.StatusOK || reply.status == http.StatusCreated:
		var receipt wire.IntakeReceipt
		if wire.Decode(reply.body, "IntakeReceipt", &receipt) != nil || reportVerifyReceipt(receipt, choice.OperatorKey, requestID, requestBytes) != nil {
			answer.Outcome = reportAnswerUnverified
			return answer
		}
		answer.Outcome, answer.Receipt = reportAnswerReceived, receipt
	case reportIsRedirect(reply.status), reply.status == http.StatusServiceUnavailable:
		answer.Outcome = reportAnswerUnclear
	case reply.status == http.StatusTooManyRequests:
		answer.Outcome = reportAnswerBusy
		if parsed, parseErr := apierr.Parse(reply.body); parseErr == nil {
			answer.RetryAfter = parsed.RetryAfterSeconds
		}
	case reply.status == http.StatusGone:
		answer.Outcome = reportAnswerExpired
	case reply.status == http.StatusConflict:
		answer.Outcome = reportAnswerConflict
	default:
		answer.Outcome = reportAnswerRefused
	}
	return answer
}

// reportRecovery is a request the provider still holds for a recovery secret.
type reportRecovery struct {
	Outcome string
	Request wire.IntakeRequest
	Receipt wire.IntakeReceipt
}

// reportRecover asks the provider for the request behind a recovery secret and
// verifies its receipt against the recovered bytes.
func (a *App) reportRecover(ctx context.Context, choice ProviderChoice, secret []byte) reportRecovery {
	requestID, err := reportDeriveRequestID(choice.OperatorKey, secret)
	if err != nil {
		return reportRecovery{Outcome: "unreachable"}
	}
	reply, err := a.reportCall(ctx, choice.Endpoint, http.MethodGet, []string{"v1", "intake", requestID}, nil, secret, reportReplyLimit)
	if err != nil {
		return reportRecovery{Outcome: "unreachable"}
	}
	switch reply.status {
	case http.StatusNotFound:
		return reportRecovery{Outcome: "notfound"}
	case http.StatusOK:
	default:
		return reportRecovery{Outcome: "unreachable"}
	}
	var recovery wire.IntakeRecovery
	if err := wire.Decode(reply.body, "IntakeRecovery", &recovery); err != nil {
		return reportRecovery{Outcome: "unverified"}
	}
	requestBytes, err := wire.Encode("IntakeRequest", recovery.Request)
	if err != nil || recovery.Request.RequestID != requestID || recovery.Request.OperatorID != choice.OperatorKey {
		return reportRecovery{Outcome: "unverified"}
	}
	if reportVerifyReceipt(recovery.Receipt, choice.OperatorKey, requestID, requestBytes) != nil {
		return reportRecovery{Outcome: "unverified"}
	}
	return reportRecovery{Outcome: "verified", Request: recovery.Request, Receipt: recovery.Receipt}
}

// reportEncodeResend returns the base64url forms of a request and its secret for the resend form.
func reportEncodeResend(requestBytes, secret []byte) (string, string) {
	return base64.RawURLEncoding.EncodeToString(requestBytes), keys.EncodeB64URL(secret)
}
