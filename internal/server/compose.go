package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"foryou.local/foryou/internal/content"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/wire"
)

func init() { routeGroups = append(routeGroups, registerCompose) }

func registerCompose(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /compose", a.composeHandler)
	mux.HandleFunc("GET /quote", a.quoteHandler)
	mux.HandleFunc("POST /quote", a.quoteHandler)
	mux.HandleFunc("GET /quote/fragment", a.quoteHandler)
	mux.HandleFunc("POST /quote/fragment", a.quoteHandler)
	mux.HandleFunc("POST /compose/check", a.cmpMeasureHandler)
	mux.HandleFunc("POST /compose/measure/fragment", a.cmpMeasureHandler)
	mux.HandleFunc("POST /compose/quote", a.cmpQuoteHandler)
	mux.HandleFunc("POST /compose/quote/fragment", a.cmpQuoteHandler)
	mux.HandleFunc("POST /compose/publish", a.cmpPublishHandler)
	mux.HandleFunc("POST /compose/publish/fragment", a.cmpPublishHandler)
	mux.HandleFunc("POST /compose/resume", a.cmpResumeHandler)
	mux.HandleFunc("POST /compose/resume/fragment", a.cmpResumeHandler)
	mux.HandleFunc("POST /compose/rows", a.cmpRowsHandler)
	mux.HandleFunc("POST /compose/rows/fragment", a.cmpRowsHandler)
}

const cmpFormLimit = 262144

var (
	errCmpBlocked    = errors.New("compose: publishing is not ready")
	errCmpUnverified = errors.New("compose: the post could not be verified")
)

// cmpFlow is the part of sdk.Publisher the composer uses. Tests replace it with a fake.
type cmpFlow interface {
	Prepare(sdk.Draft, ed25519.PrivateKey, string, *sdk.TargetEvidence) (sdk.Prepared, error)
	QuoteDraft(context.Context, sdk.Draft, ed25519.PrivateKey, string, *sdk.TargetEvidence) (sdk.Quoted, error)
	Publish(context.Context, ed25519.PrivateKey, sdk.Quoted, sdk.Approval) (sdk.Published, error)
	Resume(context.Context, ed25519.PrivateKey, string) (sdk.JournalEntry, error)
	Entry(string) (sdk.JournalEntry, bool, error)
	Entries() ([]sdk.JournalEntry, error)
}

type cmpPublisherFlow struct {
	*sdk.Publisher
	journal sdk.Journal
}

func (f cmpPublisherFlow) Entry(id string) (sdk.JournalEntry, bool, error) {
	return f.journal.Get(id)
}

func (f cmpPublisherFlow) Entries() ([]sdk.JournalEntry, error) {
	return f.journal.List()
}

var cmpNewFlow = func(client *sdk.Client, root *sdk.TrustRoot, journal sdk.Journal, now func() time.Time) cmpFlow {
	return cmpPublisherFlow{Publisher: sdk.NewPublisher(client, root, journal, now), journal: journal}
}

// The provider reads below are variables so that tests can answer them without a network.
var (
	cmpBalance = func(ctx context.Context, client *sdk.Client, key ed25519.PrivateKey, network string) (wire.BalanceStatus, error) {
		return client.Balance(ctx, key, network)
	}
	cmpRetention = func(ctx context.Context, client *sdk.Client) (wire.RetentionDeclarationsPage, error) {
		page, _, err := client.Retention(ctx)
		return page, err
	}
	cmpFetchTarget = func(ctx context.Context, client *sdk.Client, verifier *sdk.Verifier, id string) (sdk.Record, error) {
		fetched, err := client.GetMessage(ctx, id)
		if err != nil {
			return sdk.Record{}, err
		}
		record := verifier.Verify(fetched.Raw, fetched.ID, sdk.Source{Endpoint: fetched.Endpoint, ServiceID: fetched.ServiceID, Kind: sdk.SourceProvider})
		if record.Failure != nil {
			return sdk.Record{}, errCmpUnverified
		}
		return record, nil
	}
)

// cmpMemoryJournal keeps measured intents out of the publication journal.
type cmpMemoryJournal map[string]sdk.JournalEntry

func (j cmpMemoryJournal) Put(entry sdk.JournalEntry) error {
	j[entry.MessageID] = entry
	return nil
}

func (j cmpMemoryJournal) Get(id string) (sdk.JournalEntry, bool, error) {
	entry, ok := j[id]
	return entry, ok, nil
}

func (j cmpMemoryJournal) List() ([]sdk.JournalEntry, error) {
	entries := make([]sdk.JournalEntry, 0, len(j))
	for _, entry := range j {
		entries = append(entries, entry)
	}
	return entries, nil
}

type cmpBlock struct {
	Title, Text, Href, Link string
}

type cmpNetwork struct {
	Value, Label string
}

type cmpForm struct {
	Format, Text, Network, ReplyTo, Supersedes string
	AllowMissingParent                         bool
	Media                                      []cmpMediaRow
	Refs                                       []cmpRefRow
}

type cmpParent struct {
	ID      string
	Post    *post
	Missing bool
}

type cmpMeasureView struct {
	Bytes, Units              int
	Fee, Unit, ScheduleID     string
	Base, PerKiB              string
	Findings, Problems, Inert []string
}

type cmpBalanceView struct {
	Known, Short         bool
	Available, Shortfall string
}

type cmpQuoteView struct {
	Cost                               postCost
	MessageID, QuoteID, ScheduleID     string
	ProviderID, ProviderLabel, Expires string
	Amount, Unit, Retention            string
	Balance                            cmpBalanceView
	Findings, Problems                 []string
	PermanenceError, AmountError       string
}

type cmpResultView struct {
	MessageID, ReceiptID, Amount, Unit, IssuedAt string
	Already                                      bool
}

type cmpFailure struct {
	Heading, Message, Href, Link, RetryID string
	Status                                int
}

type cmpEntry struct {
	MessageID, State, Deadline, Started, Href string
}

// cmpPage is the data behind the composer page and each of its fragments.
type cmpPage struct {
	Layout
	Blocked  []cmpBlock
	Networks []cmpNetwork
	Form     cmpForm
	Parent   *cmpParent
	Refused  *cmpFailure
	Measure  *cmpMeasureView
	Quote    *cmpQuoteView
	Result   *cmpResultView
	Failure  *cmpFailure
	Pending  []cmpEntry
	Recent   []cmpEntry
}

// quotePreview is the read-only fee preview of a text; it publishes nothing.
type quotePreview struct {
	Layout Layout
	Cost   postCost
	Bytes  int
	Text   string
}

// quoteHandler prices a text with the fee schedule in force, for a reader to check before writing the post.
func (a *App) quoteHandler(w http.ResponseWriter, r *http.Request) {
	text := r.URL.Query().Get("text")
	if r.Method == http.MethodPost {
		if !parseSmallForm(w, r, 50000) {
			return
		}
		text = r.PostForm.Get("text")
	}
	if !utf8.ValidString(text) || len(text) > 16000 || utf8.RuneCountInString(text) > 4000 {
		http.Error(w, "draft is too long or invalid", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(text) == "" {
		http.Error(w, "Write the post first. A post with no text and no media is not valid.", http.StatusBadRequest)
		return
	}
	cost, err := costOf(text)
	if err != nil {
		http.Error(w, "No fee quote is available.", http.StatusServiceUnavailable)
		return
	}
	preview := quotePreview{Layout: a.layout(r, "compose", "New post"), Cost: cost, Bytes: len(text), Text: text}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/quote" {
		a.page(w, r, "compose-quote-page", preview)
		return
	}
	a.fragment(w, r, "compose-quote-response", preview)
}

// cmpSession holds what a publishing action needs once publishing is ready.
type cmpSession struct {
	client *sdk.Client
	root   *sdk.TrustRoot
	key    ed25519.PrivateKey
	flow   cmpFlow
	label  string
}

func draftToken() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

func (a *App) composeHandler(w http.ResponseWriter, r *http.Request) {
	profile, err := a.profile(w, r)
	if err != nil {
		http.Error(w, "unable to open your profile", http.StatusServiceUnavailable)
		return
	}
	rememberTheme(w, r)
	page := a.cmpBase(r, profile)
	page.Form = cmpForm{Format: content.FormatPlain, Network: publishNetwork(a, profile)}
	if supersedes := r.URL.Query().Get("supersedes"); supersedes != "" && len(page.Blocked) == 0 {
		a.cmpSupersede(r, profile, &page, supersedes)
	} else if replyTo := r.URL.Query().Get("reply_to"); replyTo != "" {
		page.Form.ReplyTo = replyTo
		page.Parent = a.cmpParentFor(r, replyTo)
	}
	a.page(w, r, "compose-page", page)
}

// cmpSupersede opens the editor for a new version of an earlier post. Only the author of that post gets an editor.
func (a *App) cmpSupersede(r *http.Request, profile string, page *cmpPage, id string) {
	if !canonicalMessageID(id) {
		refused := cmpNotYours()
		page.Refused = &refused
		return
	}
	session, err := a.cmpOpen(profile)
	if err != nil {
		refused := cmpStartFailure()
		page.Refused = &refused
		return
	}
	record, err := cmpFetchTarget(r.Context(), session.client, a.verifier(profile), id)
	if err != nil {
		refused := cmpFailureFor(err, "", session.label)
		page.Refused = &refused
		return
	}
	if record.AuthorID != keys.DIDOf(session.key) {
		refused := cmpNotYours()
		page.Refused = &refused
		return
	}
	payload := record.Message.Payload
	if payload.ContentFormat != "" {
		page.Form.Format = payload.ContentFormat
	}
	page.Form.Supersedes = id
	page.Form.Text = payload.Text
	page.Form.ReplyTo = payload.ReplyTo
	page.Form.Media = cmpMediaRows(payload.MediaSlots)
	page.Form.Refs = cmpRefRows(payload.Refs)
	if payload.ReplyTo != "" {
		page.Parent = a.cmpParentFor(r, payload.ReplyTo)
	}
}

func (a *App) cmpBase(r *http.Request, profile string) cmpPage {
	page := cmpPage{
		Layout:   a.layout(r, "compose", "New post"),
		Blocked:  a.cmpBlocks(profile),
		Networks: []cmpNetwork{{Value: wire.BitcoinMainnet, Label: "Bitcoin, paid in msat"}, {Value: wire.EthereumMainnet, Label: "Ethereum, paid in wei"}},
	}
	if journal, err := a.cmpJournal(profile); err == nil {
		if entries, err := journal.List(); err == nil {
			page.Pending, page.Recent = cmpEntries(entries)
		}
	}
	return page
}

// cmpBlocks lists what must be set up before a post can be priced and published.
func (a *App) cmpBlocks(profile string) []cmpBlock {
	var blocks []cmpBlock
	if _, found, err := a.keystore().Identity(profile); err != nil || !found {
		blocks = append(blocks, cmpBlock{Title: "Create an identity first.", Text: "Your posts are signed with your identity key.", Href: "/identity", Link: "Open identity and keys"})
	}
	settings, err := a.settings(profile)
	if _, acknowledged := settings.Acknowledgements[ackKeyWarnings]; err != nil || !acknowledged {
		blocks = append(blocks, cmpBlock{Title: "Read the key warnings before your first post.", Text: "Keys cannot be recovered by the provider.", Href: "/identity", Link: "Read the key warnings"})
	}
	if _, ok := a.publishingProvider(profile); !ok {
		blocks = append(blocks, cmpBlock{Title: "Choose a publishing provider.", Text: "Posts are sent to a provider you have chosen and trust.", Href: "/network", Link: "Open the network"})
	}
	if root, status := a.trustRoot(); root == nil || status.State != TrustReady {
		blocks = append(blocks, cmpBlock{Title: "Publishing is paused.", Text: "The fee schedule cannot be verified right now, so no fee can be shown.", Href: "/network", Link: "See the trust root"})
	}
	return blocks
}

func (a *App) cmpJournal(profile string) (sdk.Journal, error) {
	return sdk.FileJournal(filepath.Join(a.profiles.root, profile, "publish"))
}

// cmpOpen gathers the provider, trust root, author key and publication flow. It fails with errCmpBlocked while publishing is not set up.
func (a *App) cmpOpen(profile string) (cmpSession, error) {
	choice, ok := a.publishingProvider(profile)
	if !ok {
		return cmpSession{}, errCmpBlocked
	}
	root, status := a.trustRoot()
	if root == nil || status.State != TrustReady {
		return cmpSession{}, errCmpBlocked
	}
	client, err := a.providerClient(Endpoint{URL: choice.Endpoint, ServiceID: choice.OperatorKey, Label: choice.Label})
	if err != nil {
		return cmpSession{}, err
	}
	key, err := a.keystore().PrivateKey(profile)
	if err != nil {
		return cmpSession{}, err
	}
	journal, err := a.cmpJournal(profile)
	if err != nil {
		return cmpSession{}, err
	}
	return cmpSession{client: client, root: root, key: key, flow: cmpNewFlow(client, root, journal, a.config.Now), label: choice.Label}, nil
}

// cmpTarget fetches and verifies the post a draft supersedes. It is nil when the draft supersedes nothing.
func (a *App) cmpTarget(ctx context.Context, profile string, session cmpSession, id string) (*sdk.TargetEvidence, error) {
	if id == "" {
		return nil, nil
	}
	record, err := cmpFetchTarget(ctx, session.client, a.verifier(profile), id)
	if err != nil {
		return nil, err
	}
	return &sdk.TargetEvidence{Message: record.Message}, nil
}

func (a *App) cmpParentFor(r *http.Request, id string) *cmpParent {
	if !canonicalMessageID(id) {
		return &cmpParent{ID: id, Missing: true}
	}
	p, found := a.requestPost(r, id)
	if !found {
		return &cmpParent{ID: id, Missing: true}
	}
	return &cmpParent{ID: id, Post: &p}
}

func cmpEntries(entries []sdk.JournalEntry) (pending, recent []cmpEntry) {
	for _, entry := range entries {
		switch entry.State {
		case "authorized", "admitted", "signed":
			pending = append(pending, cmpEntry{MessageID: entry.MessageID, State: entry.State, Deadline: entry.Deadline, Started: entry.CreatedAt})
		case "accepted":
			recent = append(recent, cmpEntry{MessageID: entry.MessageID, Href: "/posts/" + entry.MessageID})
		}
	}
	return pending, recent
}

func cmpReadForm(values url.Values) cmpForm {
	return cmpForm{
		Format:             values.Get("format"),
		Text:               values.Get("text"),
		Network:            values.Get("network"),
		ReplyTo:            values.Get("reply_to"),
		Supersedes:         values.Get("supersedes"),
		AllowMissingParent: values.Get("allow_missing_parent") == "on",
		Media:              cmpReadMedia(values),
		Refs:               cmpReadRefs(values),
	}
}

// cmpDraft turns the form into the exact draft to price, reporting what the author must fix first.
func cmpDraft(form cmpForm) (sdk.Draft, []string) {
	var problems []string
	format := content.FormatPlain
	switch form.Format {
	case content.FormatPlain:
	case content.FormatMarkdown:
		format = content.FormatMarkdown
	default:
		problems = append(problems, "Choose plain text or Markdown.")
	}
	if form.Network != wire.BitcoinMainnet && form.Network != wire.EthereumMainnet {
		problems = append(problems, "Choose the network to publish on.")
	}
	if form.ReplyTo != "" && !canonicalMessageID(form.ReplyTo) {
		problems = append(problems, "The post you are replying to has an invalid ID.")
	}
	if form.Supersedes != "" && !canonicalMessageID(form.Supersedes) {
		problems = append(problems, "The post you are replacing has an invalid ID.")
	}
	slots, slotProblems := cmpMediaSlots(form.Media)
	refs, refProblems := cmpRefs(form.Refs)
	problems = append(problems, slotProblems...)
	problems = append(problems, refProblems...)
	if strings.TrimSpace(form.Text) == "" && len(slots) == 0 {
		problems = append(problems, "Write something before you ask for a quote.")
	}
	return sdk.Draft{ContentFormat: format, Text: form.Text, MediaSlots: slots, Refs: refs, ReplyTo: form.ReplyTo, Supersedes: form.Supersedes}, problems
}

func (a *App) cmpRespond(w http.ResponseWriter, r *http.Request, page cmpPage, status int, fragment string) {
	if strings.HasSuffix(r.URL.Path, "/fragment") {
		a.renderOutput(w, fragment, page, status, "no-store")
		return
	}
	a.renderOutput(w, "compose-page", page, status, "private, no-store")
}

// cmpMeasureHandler shows the billable size and fee of the text as it is written. Nothing is signed or stored.
func (a *App) cmpMeasureHandler(w http.ResponseWriter, r *http.Request) {
	if !requireSameOrigin(w, r) || !parseSmallForm(w, r, cmpFormLimit) {
		return
	}
	profile, err := a.profile(w, r)
	if err != nil {
		http.Error(w, "unable to open your profile", http.StatusServiceUnavailable)
		return
	}
	page := a.cmpBase(r, profile)
	page.Form = cmpReadForm(r.PostForm)
	if page.Form.ReplyTo != "" {
		page.Parent = a.cmpParentFor(r, page.Form.ReplyTo)
	}
	if len(page.Blocked) > 0 {
		a.cmpRespond(w, r, page, http.StatusConflict, "compose-measure")
		return
	}
	draft, problems := cmpDraft(page.Form)
	measure := cmpMeasureView{Problems: problems}
	if len(problems) == 0 {
		session, err := a.cmpOpen(profile)
		if err != nil {
			failure := cmpStartFailure()
			page.Failure = &failure
			a.cmpRespond(w, r, page, failure.Status, "compose-measure")
			return
		}
		measure = a.cmpMeasureDraft(r.Context(), profile, session, draft, page.Form.Network)
	}
	page.Measure = &measure
	a.cmpRespond(w, r, page, http.StatusOK, "compose-measure")
}

func (a *App) cmpMeasureDraft(ctx context.Context, profile string, session cmpSession, draft sdk.Draft, network string) cmpMeasureView {
	target, err := a.cmpTarget(ctx, profile, session, draft.Supersedes)
	if err != nil {
		return cmpMeasureView{Problems: []string{cmpFailureFor(err, "", session.label).Heading}}
	}
	flow := cmpNewFlow(session.client, session.root, make(cmpMemoryJournal), a.config.Now)
	return cmpMeasure(flow, session, draft, network, target, a.now())
}

func cmpMeasure(flow cmpFlow, session cmpSession, draft sdk.Draft, network string, target *sdk.TargetEvidence, now time.Time) cmpMeasureView {
	prepared, err := flow.Prepare(draft, session.key, network, target)
	if err != nil {
		if errors.Is(err, sdk.ErrNoSchedule) {
			return cmpMeasureView{Problems: []string{"No approved fee schedule is in force, so the fee cannot be checked."}}
		}
		return cmpMeasureView{Problems: []string{"This post cannot be published as written: " + err.Error()}}
	}
	view := cmpMeasureView{
		Bytes:      prepared.BillableBytes,
		Units:      (prepared.BillableBytes + 1023) / 1024,
		Fee:        groupDigits(prepared.Fee),
		Unit:       cmpUnit(prepared),
		ScheduleID: prepared.ScheduleID,
		Findings:   cmpFindings(prepared.Findings),
		Inert:      cmpInert(draft),
	}
	if schedule, ok := session.root.ScheduleInForce(network, now); ok {
		view.Base = groupDigits(schedule.BaseFeeBaseUnits)
		view.PerKiB = groupDigits(schedule.PerKiBFeeBaseUnits)
	}
	return view
}

// cmpQuoteHandler asks the provider for a signed quote of the exact draft and shows it for confirmation.
func (a *App) cmpQuoteHandler(w http.ResponseWriter, r *http.Request) {
	if !requireSameOrigin(w, r) || !parseSmallForm(w, r, cmpFormLimit) {
		return
	}
	profile, err := a.profile(w, r)
	if err != nil {
		http.Error(w, "unable to open your profile", http.StatusServiceUnavailable)
		return
	}
	page := a.cmpBase(r, profile)
	page.Form = cmpReadForm(r.PostForm)
	if page.Form.ReplyTo != "" {
		page.Parent = a.cmpParentFor(r, page.Form.ReplyTo)
	}
	if len(page.Blocked) > 0 {
		a.cmpRespond(w, r, page, http.StatusConflict, "compose-quote")
		return
	}
	draft, problems := cmpDraft(page.Form)
	if page.Parent != nil && page.Parent.Missing && !page.Form.AllowMissingParent {
		problems = append(problems, "Parent not available from your sources. Tick \"Reply to this exact ID anyway\" to reply without it.")
	}
	if len(problems) > 0 {
		page.Quote = &cmpQuoteView{Problems: problems}
		a.cmpRespond(w, r, page, http.StatusBadRequest, "compose-quote")
		return
	}
	session, err := a.cmpOpen(profile)
	if err != nil {
		failure := cmpStartFailure()
		page.Failure = &failure
		a.cmpRespond(w, r, page, failure.Status, "compose-quote")
		return
	}
	target, err := a.cmpTarget(r.Context(), profile, session, draft.Supersedes)
	if err != nil {
		failure := cmpFailureFor(err, "", session.label)
		page.Failure = &failure
		a.cmpRespond(w, r, page, failure.Status, "compose-quote")
		return
	}
	quoted, err := session.flow.QuoteDraft(r.Context(), draft, session.key, page.Form.Network, target)
	if err != nil {
		failure := cmpFailureFor(err, "", session.label)
		page.Failure = &failure
		a.cmpRespond(w, r, page, failure.Status, "compose-quote")
		return
	}
	view, err := cmpQuoteFor(quoted, a.now(), session.label)
	if err != nil {
		failure := cmpFailureFor(err, "", session.label)
		page.Failure = &failure
		a.cmpRespond(w, r, page, failure.Status, "compose-quote")
		return
	}
	view.Balance = cmpBalanceFor(r.Context(), session, quoted)
	view.Retention = cmpRetentionFor(r.Context(), session.client)
	page.Quote = &view
	_ = a.updateSettings(profile, func(s *ProfileSettings) error {
		s.PublishNetwork = page.Form.Network
		return nil
	})
	a.cmpRespond(w, r, page, http.StatusOK, "compose-quote")
}

// cmpStartFailure says publishing cannot start yet and what to check first.
func cmpStartFailure() cmpFailure {
	return cmpFailure{Heading: "Publishing could not start", Message: "Check the setup steps on this page, then try again.", Status: http.StatusConflict}
}

// cmpNotYours refuses a new version of a post the author did not write.
func cmpNotYours() cmpFailure {
	return cmpFailure{Heading: "You can only publish a new version of your own post", Message: "Choose a post you wrote.", Status: http.StatusConflict}
}

func cmpQuoteFor(q sdk.Quoted, now time.Time, provider string) (cmpQuoteView, error) {
	cost, err := quotedCost(q)
	if err != nil {
		return cmpQuoteView{}, err
	}
	expires, err := wire.ParseTime(q.Quote.ExpiresAt)
	if err != nil {
		return cmpQuoteView{}, err
	}
	minutes := int(expires.Sub(now).Minutes())
	return cmpQuoteView{
		Cost:          cost,
		MessageID:     q.Prepared.MessageID,
		QuoteID:       q.Quote.QuoteID,
		ScheduleID:    q.Quote.ScheduleID,
		ProviderID:    q.Quote.ProviderID,
		ProviderLabel: provider,
		Expires:       fmt.Sprintf("valid until %s UTC, %d minutes", expires.UTC().Format("15:04"), minutes),
		Amount:        groupDigits(q.Quote.AmountBaseUnits),
		Unit:          cost.Fees[0].Unit,
		Findings:      cmpFindings(q.Prepared.Findings),
	}, nil
}

// cmpBalanceFor compares the author's balance at the provider with the quoted fee. Both are integers in base units.
func cmpBalanceFor(ctx context.Context, session cmpSession, quoted sdk.Quoted) cmpBalanceView {
	balance, err := cmpBalance(ctx, session.client, session.key, quoted.Quote.Network)
	if err != nil {
		return cmpBalanceView{}
	}
	available, ok := new(big.Int).SetString(balance.Available, 10)
	amount, err := fee.ParseAmount(quoted.Quote.AmountBaseUnits)
	if !ok || err != nil {
		return cmpBalanceView{}
	}
	view := cmpBalanceView{Known: true, Available: groupDigits(fee.FormatAmount(available))}
	if available.Cmp(amount) < 0 {
		view.Short = true
		view.Shortfall = groupDigits(fee.FormatAmount(new(big.Int).Sub(amount, available)))
	}
	return view
}

// cmpRetentionFor names the period the provider declares for keeping posts. It is empty when the provider does not answer.
func cmpRetentionFor(ctx context.Context, client *sdk.Client) string {
	page, err := cmpRetention(ctx, client)
	if err != nil || len(page.Declarations) == 0 {
		return ""
	}
	current := page.Declarations[0]
	for _, declaration := range page.Declarations[1:] {
		if declaration.EffectiveAt > current.EffectiveAt {
			current = declaration
		}
	}
	if current.RetentionSeconds%86400 != 0 {
		return fmt.Sprintf("%d seconds", current.RetentionSeconds)
	}
	return fmt.Sprintf("%d days", current.RetentionSeconds/86400)
}

// cmpUnit names the base unit of a network's fee in its short form, such as msat or wei.
func cmpUnit(prepared sdk.Prepared) string {
	if unit := feeLabels[prepared.Network].Unit; unit != "" {
		return unit
	}
	return prepared.Denomination
}

func cmpFindings(findings []sdk.Finding) []string {
	lines := make([]string, 0, len(findings))
	for _, finding := range findings {
		lines = append(lines, fmt.Sprintf("Possible secret in %s: %s", finding.Field, finding.Excerpt))
	}
	return lines
}

// cmpPublishHandler admits the exact quote stored in the journal once the author has approved it.
func (a *App) cmpPublishHandler(w http.ResponseWriter, r *http.Request) {
	if !requireSameOrigin(w, r) || !parseSmallForm(w, r, cmpFormLimit) {
		return
	}
	profile, err := a.profile(w, r)
	if err != nil {
		http.Error(w, "unable to open your profile", http.StatusServiceUnavailable)
		return
	}
	page := a.cmpBase(r, profile)
	if len(page.Blocked) > 0 {
		a.cmpRespond(w, r, page, http.StatusConflict, "compose-quote")
		return
	}
	session, err := a.cmpOpen(profile)
	if err != nil {
		failure := cmpStartFailure()
		page.Failure = &failure
		a.cmpRespond(w, r, page, failure.Status, "compose-quote")
		return
	}
	id := r.PostForm.Get("message_id")
	entry, found, err := session.flow.Entry(id)
	if err != nil || !canonicalMessageID(id) || !found || entry.State != "quoted" {
		failure := cmpStaleFailure(found && entry.State == "accepted", id)
		page.Failure = &failure
		a.cmpRespond(w, r, page, failure.Status, "compose-quote")
		return
	}
	quoted := sdk.Quoted{Prepared: entry.Prepared, Quote: entry.Quote, Raw: entry.QuoteRaw}
	view, err := cmpQuoteFor(quoted, a.now(), session.label)
	if err != nil {
		failure := cmpFailureFor(err, "", session.label)
		page.Failure = &failure
		a.cmpRespond(w, r, page, failure.Status, "compose-quote")
		return
	}
	if r.PostForm.Get("permanence") != "on" {
		view.PermanenceError = "Tick the permanence notice to publish."
	}
	if r.PostForm.Get("approve_amount") != "on" {
		view.AmountError = "Tick the approval for this exact amount to publish."
	}
	if view.PermanenceError != "" || view.AmountError != "" {
		page.Quote = &view
		a.cmpRespond(w, r, page, http.StatusBadRequest, "compose-quote")
		return
	}
	if err := a.updateSettings(profile, func(s *ProfileSettings) error {
		if s.Acknowledgements == nil {
			s.Acknowledgements = make(map[string]time.Time)
		}
		s.Acknowledgements[ackPermanence] = a.now()
		return nil
	}); err != nil {
		failure := cmpFailure{Heading: "Publishing did not start", Message: "Your permanence notice could not be recorded. Try again.", Status: http.StatusServiceUnavailable}
		page.Failure = &failure
		a.cmpRespond(w, r, page, failure.Status, "compose-quote")
		return
	}
	approval := sdk.Approval{MessageID: id, Amount: quoted.Quote.AmountBaseUnits, Network: quoted.Quote.Network, ApprovedAt: a.now()}
	published, err := session.flow.Publish(r.Context(), session.key, quoted, approval)
	if err != nil {
		failure := cmpFailureFor(err, id, session.label)
		page.Failure = &failure
		a.cmpRespond(w, r, page, failure.Status, "compose-quote")
		return
	}
	a.cmpKeep(profile, session, published)
	accepted, _, _ := session.flow.Entry(id)
	result := cmpResultFor(published, accepted)
	page.Result = &result
	a.cmpRespond(w, r, page, http.StatusOK, "compose-quote")
}

// cmpKeep holds an accepted post in this server's verified local copy, so that it shows before the next sync.
func (a *App) cmpKeep(profile string, session cmpSession, published sdk.Published) {
	source, ok := a.networkSource()
	if !ok {
		return
	}
	if err := source.keepPublished(profile, published, session.client); err != nil {
		a.logger().Printf("compose: keep published record: %v", err)
	}
}

func cmpResultFor(published sdk.Published, entry sdk.JournalEntry) cmpResultView {
	return cmpResultView{
		MessageID: published.MessageID,
		ReceiptID: entry.Receipt.ReceiptID,
		Amount:    groupDigits(entry.Quote.AmountBaseUnits),
		Unit:      cmpUnit(entry.Prepared),
		IssuedAt:  walWhen(entry.Receipt.IssuedAt),
		Already:   published.Retry,
	}
}

// cmpResumeHandler retries only the exact stored request of an unresolved publication.
func (a *App) cmpResumeHandler(w http.ResponseWriter, r *http.Request) {
	if !requireSameOrigin(w, r) || !parseSmallForm(w, r, cmpFormLimit) {
		return
	}
	profile, err := a.profile(w, r)
	if err != nil {
		http.Error(w, "unable to open your profile", http.StatusServiceUnavailable)
		return
	}
	page := a.cmpBase(r, profile)
	if len(page.Blocked) > 0 {
		a.cmpRespond(w, r, page, http.StatusConflict, "compose-quote")
		return
	}
	session, err := a.cmpOpen(profile)
	if err != nil {
		failure := cmpStartFailure()
		page.Failure = &failure
		a.cmpRespond(w, r, page, failure.Status, "compose-quote")
		return
	}
	id := r.PostForm.Get("message_id")
	if !canonicalMessageID(id) {
		failure := cmpFailure{Heading: "This publication cannot be found", Message: "Check the unfinished publications list and choose the post again.", Status: http.StatusBadRequest}
		page.Failure = &failure
		a.cmpRespond(w, r, page, failure.Status, "compose-quote")
		return
	}
	entry, err := session.flow.Resume(r.Context(), session.key, id)
	if err != nil {
		failure := cmpFailureFor(err, id, session.label)
		page.Failure = &failure
		a.cmpRespond(w, r, page, failure.Status, "compose-quote")
		return
	}
	switch entry.State {
	case "accepted":
		published := sdk.Published{MessageID: id, Raw: entry.FinalMessage}
		a.cmpKeep(profile, session, published)
		result := cmpResultFor(published, entry)
		page.Result = &result
		a.cmpRespond(w, r, page, http.StatusOK, "compose-quote")
	case "rejected":
		failure := cmpStatusFailure(entry.Rejection, 0, session.label)
		page.Failure = &failure
		a.cmpRespond(w, r, page, failure.Status, "compose-quote")
	default:
		failure := cmpFailure{Heading: "Publishing is still pending", Message: "The provider has not confirmed this post yet. Retry the same request later.", RetryID: id, Status: http.StatusConflict}
		page.Failure = &failure
		a.cmpRespond(w, r, page, failure.Status, "compose-quote")
	}
}

// cmpRowsHandler adds or removes media and reference rows, keeping everything else the author wrote.
func (a *App) cmpRowsHandler(w http.ResponseWriter, r *http.Request) {
	if !requireSameOrigin(w, r) || !parseSmallForm(w, r, cmpFormLimit) {
		return
	}
	profile, err := a.profile(w, r)
	if err != nil {
		http.Error(w, "unable to open your profile", http.StatusServiceUnavailable)
		return
	}
	page := a.cmpBase(r, profile)
	if len(page.Blocked) > 0 {
		a.cmpRespond(w, r, page, http.StatusConflict, "compose-blocked")
		return
	}
	action := r.PostForm.Get("row_action")
	page.Form = cmpEditRows(cmpReadForm(r.PostForm), action)
	if verb, rest, _ := strings.Cut(action, ":"); verb == "digest-media" {
		page.Form = cmpDigestRow(r.Context(), page.Form, rest)
	}
	if page.Form.ReplyTo != "" {
		page.Parent = a.cmpParentFor(r, page.Form.ReplyTo)
	}
	a.cmpRespond(w, r, page, http.StatusOK, "compose-editor")
}

// cmpFailureFor explains a publishing error by its public name and the next step. It never offers a retry that creates a new authorization, except the exact stored request of an unknown outcome.
func cmpFailureFor(err error, messageID, provider string) cmpFailure {
	if errors.Is(err, sdk.ErrOutcomeUnknown) {
		return cmpFailure{Heading: "We could not confirm the result", Message: "We could not confirm the result. Your exact signed request is saved.", RetryID: messageID, Status: http.StatusBadGateway}
	}
	var answered *sdk.Error
	if errors.As(err, &answered) {
		return cmpStatusFailure(answered.Status, answered.RetryAfter, provider)
	}
	switch {
	case errors.Is(err, errCmpUnverified):
		return cmpFailure{Heading: "The post you are replacing could not be verified", Message: "Choose the post again, or try later.", Status: http.StatusBadGateway}
	case errors.Is(err, sdk.ErrStale):
		return cmpFailure{Heading: "The quote is too old", Message: "Get a new quote and confirm it again.", Status: http.StatusConflict}
	case errors.Is(err, sdk.ErrQuoteMismatch):
		return cmpFailure{Heading: "The provider's quote differs from the fee set by the schedule", Message: "Nothing was signed. Get a new quote.", Status: http.StatusConflict}
	case errors.Is(err, sdk.ErrInsufficientBalance):
		return cmpStatusFailure("insufficient_balance", 0, provider)
	case errors.Is(err, sdk.ErrNotApproved):
		return cmpFailure{Heading: "The approval does not match the quote", Message: "Get a new quote and confirm it again.", Status: http.StatusConflict}
	case errors.Is(err, sdk.ErrUpdateAuthor):
		return cmpNotYours()
	case errors.Is(err, sdk.ErrUpdateThread):
		return cmpFailure{Heading: "The new version belongs to another thread", Message: "Keep the same reply target as the earlier version.", Status: http.StatusConflict}
	case errors.Is(err, sdk.ErrDeadlinePassed):
		return cmpFailure{Heading: "The preparation deadline has passed", Message: "Write the post again to start a new publication.", Status: http.StatusConflict}
	case errors.Is(err, sdk.ErrPendingAuthorization):
		return cmpFailure{Heading: "This post already has a pending authorization", Message: "Resume it from the unfinished publications list.", Status: http.StatusConflict}
	case errors.Is(err, sdk.ErrNoSchedule):
		return cmpFailure{Heading: "No approved fee schedule is in force", Message: "Publishing is paused until one is approved.", Status: http.StatusServiceUnavailable}
	case errors.Is(err, sdk.ErrTooLarge):
		return cmpFailure{Heading: "The post is too large", Message: "Shorten the text or remove media and references.", Status: http.StatusConflict}
	}
	return cmpFailure{Heading: "Publishing did not finish", Message: "Check the unfinished publications below before you write the post again.", Status: http.StatusBadGateway}
}

// cmpStatusFailure maps a public provider error name to its explanation. provider is the label of the provider that answered.
func cmpStatusFailure(status string, retryAfter time.Duration, provider string) cmpFailure {
	switch status {
	case "insufficient_balance":
		heading := "Your balance is too low"
		if provider != "" {
			heading = "Your balance at " + provider + " is too low"
		}
		return cmpFailure{Heading: heading, Message: "Fund your balance there, then get a new quote.", Href: "/wallet", Link: "Open the wallet", Status: http.StatusPaymentRequired}
	case "quote_expired":
		return cmpFailure{Heading: "The quote has expired", Message: "Get a new quote and confirm it again.", Status: http.StatusConflict}
	case "quote_conflict":
		return cmpFailure{Heading: "The provider quoted this post differently", Message: "Get a new quote.", Status: http.StatusConflict}
	case "bad_authorization":
		return cmpFailure{Heading: "The provider did not accept your signature", Message: "Check your identity, then try again.", Href: "/identity", Link: "Open identity and keys", Status: http.StatusUnauthorized}
	case "invalid_message":
		return cmpFailure{Heading: "The provider rejected the message", Message: "Check the text and media, then write the post again.", Status: http.StatusBadRequest}
	case "message_too_large":
		return cmpFailure{Heading: "The post is too large", Message: "Shorten the text or remove media and references.", Status: http.StatusRequestEntityTooLarge}
	case "id_mismatch":
		return cmpFailure{Heading: "The message ID does not match its content", Message: "Write the post again.", Status: http.StatusBadRequest}
	case "id_collision":
		return cmpFailure{Heading: "This message ID is already used by different content", Message: "Change the text slightly and try again.", Status: http.StatusConflict}
	case "delegation_revoked":
		return cmpFailure{Heading: "This key may no longer publish for you", Message: "Publish with your own identity key.", Href: "/identity", Link: "Open identity and keys", Status: http.StatusForbidden}
	case "rate_limited":
		message := "Wait a little, then try again."
		if retryAfter > 0 {
			message = fmt.Sprintf("Wait %d seconds, then try again.", int(retryAfter.Seconds()))
		}
		return cmpFailure{Heading: "The provider is limiting requests", Message: message, Status: http.StatusTooManyRequests}
	case "unavailable":
		return cmpFailure{Heading: "The provider is unavailable", Message: "Try again later.", Status: http.StatusServiceUnavailable}
	}
	return cmpFailure{Heading: "The provider refused this post", Message: "The provider answered: " + status + ".", Status: http.StatusBadGateway}
}

// cmpStaleFailure explains a confirmation for a quote that is gone or already used.
func cmpStaleFailure(accepted bool, messageID string) cmpFailure {
	if accepted {
		return cmpFailure{Heading: "This post is already published", Message: "The provider accepted it earlier.", Href: "/posts/" + messageID, Link: "View the post", Status: http.StatusConflict}
	}
	return cmpFailure{Heading: "This quote is no longer available", Message: "Write the post again to get a new quote.", Status: http.StatusConflict}
}
