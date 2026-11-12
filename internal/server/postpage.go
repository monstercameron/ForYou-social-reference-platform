package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

const ppPendingWindow = 7 * 24 * time.Hour

const ppRetentionUnknown = "Retention is known once the settlement claim naming a declaration is available."

const ppNotChecked = "Not checked: the trust root is unavailable."

func init() { routeGroups = append(routeGroups, registerPostPage) }

func registerPostPage(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /posts/{id}", a.postPageHandler)
	mux.HandleFunc("GET /posts/{id}/raw", a.ppRawHandler)
	mux.HandleFunc("GET /posts/{id}/vault", a.ppVaultPageHandler)
	mux.HandleFunc("GET /posts/{id}/vault/fragment", a.ppVaultFragmentHandler)
	mux.HandleFunc("POST /posts/{id}/recheck", a.ppRecheckPageHandler)
	mux.HandleFunc("POST /posts/{id}/recheck/fragment", a.ppRecheckFragmentHandler)
}

type ppPage struct {
	Layout         Layout
	ID             string
	Missing        bool
	RetentionEnded bool
	Coverage       Coverage
	Post           post
	Thread         thread
	Mine           bool
	Verify         ppVerification
	Receipt        ppReceipt
	Proof          ppProof
	Retention      ppRetention
	Sources        ppSources
	Raw            ppRaw
}

type ppVerification struct {
	Demo           bool
	Unverifiable   string
	Pill           string
	PillClass      string
	Checks         []sdk.Check
	Provider       string
	Trusted        bool
	Settlement     string
	SettlementPill string
}

// ppRow is one labelled value; chips show a shortened ID and the page repeats the full value beside them.
type ppRow struct {
	Term, Value, Short string
	Chip               bool
}

type ppReceipt struct {
	Demo         bool
	Unverifiable string
	Rows         []ppRow
	Billable     string
	Fee          string
	FeeMatches   bool
}

type ppWitness struct {
	Row   ppRow
	At    string
	Where string
}

type ppProof struct {
	Demo         bool
	Unverifiable string
	ID           string
	Pill         string
	PillClass    string
	Checked      string
	Pending      string
	Reasons      []string
	Claim        []ppRow
	Witnesses    []ppWitness
	Quorum       string
}

type ppRetention struct {
	Demo         bool
	Unverifiable string
	ID           string
	Text         string
	Vault        string
}

type ppSources struct {
	Served string
	Lines  []string
}

type ppRaw struct {
	Demo         bool
	Unverifiable string
	ID           string
	Text         string
}

// postPageHandler shows one post with why it is trusted. The page never fetches the Lifetime Vault or re-checks settlement by itself.
func (a *App) postPageHandler(w http.ResponseWriter, r *http.Request) {
	a.ppServe(w, r, false, false)
}

func (a *App) ppVaultPageHandler(w http.ResponseWriter, r *http.Request) {
	a.ppServe(w, r, true, false)
}

func (a *App) ppRecheckPageHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := readActionForm(w, r); !ok {
		return
	}
	a.ppServe(w, r, false, true)
}

func (a *App) ppServe(w http.ResponseWriter, r *http.Request, vault, recheck bool) {
	id := r.PathValue("id")
	p, coverage, found, err := a.records().Get(r.Context(), r, id)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "This post could not be read.")
		return
	}
	layout := a.layout(r, "post", "Post")
	w.Header().Set("Cache-Control", "private, no-store")
	if !found {
		if !canonicalMessageID(id) {
			a.writeDetailNotFound(w, r)
			return
		}
		a.renderStatus(w, r, "post-page", ppPage{Layout: layout, ID: id, Missing: true, Coverage: coverage, RetentionEnded: ppRetired(coverage)}, http.StatusNotFound)
		return
	}
	decorated := []post{p}
	a.forReader(r, decorated)
	p = decorated[0]
	page := a.ppBuild(r, p, coverage)
	page.Layout = layout
	page.Thread, _ = a.ppThread(r, id)
	checkable := p.Verified != nil && p.Unverifiable == ""
	if recheck && checkable {
		page.Proof = a.ppRecheckedProof(r, *p.Verified)
	}
	if vault && checkable {
		page.Retention.Vault = a.ppVaultText(r.Context(), *p.Verified)
	}
	a.page(w, r, "post-page", page)
}

// ppThread reads the first page of a post's replies for rendering below its card.
func (a *App) ppThread(r *http.Request, id string) (thread, int) {
	threadRequest := r.Clone(r.Context())
	threadRequest.URL = &url.URL{Path: "/thread", RawQuery: url.Values{"id": {id}}.Encode()}
	t, status := a.readThread(threadRequest)
	t.Static = true
	return t, status
}

// ppRawHandler returns the exact signed bytes of a network record.
func (a *App) ppRawHandler(w http.ResponseWriter, r *http.Request) {
	rec, ok := a.ppRecord(r, r.PathValue("id"))
	if !ok || len(rec.Raw) == 0 {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	header := w.Header()
	header.Set("Content-Type", "application/json")
	header.Set("Content-Disposition", `attachment; filename="post-`+strings.TrimPrefix(rec.ID, "fy1:")+`.json"`)
	header.Set("Content-Security-Policy", "default-src 'none'")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Cache-Control", "private, no-store")
	_, _ = w.Write(rec.Raw)
}

func (a *App) ppVaultFragmentHandler(w http.ResponseWriter, r *http.Request) {
	rec, ok := a.ppRecord(r, r.PathValue("id"))
	if !ok {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	a.fragment(w, r, "post-vault", a.ppVaultText(r.Context(), rec))
}

func (a *App) ppRecheckFragmentHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := readActionForm(w, r); !ok {
		return
	}
	rec, ok := a.ppRecord(r, r.PathValue("id"))
	if !ok {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	a.fragment(w, r, "post-proof", a.ppRecheckedProof(r, rec))
}

// ppRecord returns the verified record a read finds for the ID, if it is a network record.
func (a *App) ppRecord(r *http.Request, id string) (sdk.Record, bool) {
	p, _, found, err := a.records().Get(r.Context(), r, id)
	if err != nil || !found || p.Verified == nil || p.Unverifiable != "" {
		return sdk.Record{}, false
	}
	return *p.Verified, true
}

func (a *App) ppBuild(r *http.Request, p post, coverage Coverage) ppPage {
	page := ppPage{ID: p.ID, Post: p, Coverage: coverage, RetentionEnded: ppRetired(coverage)}
	if p.Verified == nil {
		page.Verify = ppVerification{Demo: true}
		page.Receipt = ppReceipt{Demo: true}
		page.Proof = ppProof{Demo: true}
		page.Retention = ppRetention{Demo: true}
		page.Sources = ppSources{Lines: coverage.Lines()}
		page.Raw = ppRaw{Demo: true}
		return page
	}
	if p.Unverifiable != "" {
		page.Verify = ppVerification{Unverifiable: p.Unverifiable}
		page.Receipt = ppReceipt{Unverifiable: ppNotChecked}
		page.Proof = ppProof{Unverifiable: ppNotChecked}
		page.Retention = ppRetention{Unverifiable: ppNotChecked}
		page.Sources = ppSources{Lines: coverage.Lines()}
		page.Raw = ppRaw{Unverifiable: ppNotChecked}
		return page
	}
	rec := *p.Verified
	page.Mine = a.ppAuthoredByReader(r, rec.AuthorID)
	page.Verify = ppVerification{Checks: rec.Checks, Trusted: rec.Class != sdk.ClassValidUntrusted, Provider: a.ppProviderName(r, rec.Receipt.ProviderID)}
	page.Verify.Pill, page.Verify.PillClass = ppClassPill(rec.Class)
	page.Receipt = ppReceiptOf(rec)
	page.Proof = a.ppProofOf(r, rec)
	page.Proof.ID = p.ID
	page.Verify.Settlement, page.Verify.SettlementPill = page.Proof.Pill, page.Proof.PillClass
	page.Retention = ppRetention{ID: p.ID, Text: a.ppRetentionText(r, rec)}
	page.Sources = a.ppSourcesOf(r, rec, coverage)
	page.Raw = ppRawOf(p.ID, rec)
	return page
}

func (a *App) ppAuthoredByReader(r *http.Request, did string) bool {
	profile := requestProfile(r)
	if profile == "" {
		return false
	}
	identity, ok, err := a.keystore().Identity(profile)
	return err == nil && ok && identity.DID == did
}

func ppClassPill(c sdk.Class) (string, string) {
	switch c {
	case sdk.ClassLocallyAdmitted:
		return "Locally admitted", "pill-ok"
	case sdk.ClassValidTrustedUnaccepted:
		return "Valid, provider trusted, not yet accepted here", "pill-warn"
	case sdk.ClassValidUntrusted:
		return "Valid, provider not trusted", "pill-muted"
	}
	return "Invalid", "pill-bad"
}

// ppProviderEndpoint finds the selected provider that operates the given provider ID.
func (a *App) ppProviderEndpoint(r *http.Request, providerID string) (Endpoint, bool) {
	for _, src := range a.nrSourcesFor(requestProfile(r)) {
		if src.kind == sdk.SourceProvider && src.endpoint.ServiceID == providerID {
			return src.endpoint, true
		}
	}
	return Endpoint{}, false
}

func (a *App) ppProviderName(r *http.Request, providerID string) string {
	if endpoint, ok := a.ppProviderEndpoint(r, providerID); ok {
		return nrSource{endpoint: endpoint}.label()
	}
	return shortDID(providerID)
}

func ppReceiptOf(rec sdk.Record) ppReceipt {
	rc := rec.Receipt
	unit := ppUnit(rc.Network, rec.Denomination)
	rows := []ppRow{
		{Term: "Provider ID", Value: rc.ProviderID, Short: shortReceipt(rc.ProviderID), Chip: true},
		{Term: "Author", Value: rc.AuthorID, Short: shortReceipt(rc.AuthorID), Chip: true},
		{Term: "Message ID", Value: rc.MessageID, Short: shortReceipt(rc.MessageID), Chip: true},
		{Term: "Receipt ID", Value: rc.ReceiptID, Short: shortReceipt(rc.ReceiptID), Chip: true},
		{Term: "Quote ID", Value: rc.QuoteID, Short: shortReceipt(rc.QuoteID), Chip: true},
		{Term: "Schedule ID", Value: rc.ScheduleID, Short: shortReceipt(rc.ScheduleID), Chip: true},
		{Term: "Network", Value: rc.Network},
		{Term: "Amount", Value: rc.AmountBaseUnits + " " + unit},
		{Term: "Authorized at", Value: rc.AuthorizationAt},
		{Term: "Issued at", Value: rc.IssuedAt},
	}
	return ppReceipt{
		Rows:       rows,
		Billable:   strconv.Itoa(rec.BillableBytes) + " bytes",
		Fee:        rec.Fee + " " + unit,
		FeeMatches: rec.Fee == rc.AmountBaseUnits,
	}
}

// ppUnit names the base unit of a network's amounts.
func ppUnit(network, fallback string) string {
	if label := feeLabels[network]; label.Unit != "" {
		return label.Unit
	}
	return fallback
}

// ppProofOf says what settlement shows for a record, and for a settled one the claim and the witnesses that settled it.
func (a *App) ppProofOf(r *http.Request, rec sdk.Record) ppProof {
	root, _ := a.trustRoot()
	committee := root.Committee(a.now())
	var proof ppProof
	switch rec.Settlement {
	case settlement.StateSettled:
		proof.Pill, proof.PillClass = "Settled", "pill-ok"
		if rec.Proof != nil {
			proof.Claim, proof.Witnesses, proof.Quorum = ppSettledDetail(a.ppProviderName(r, rec.Receipt.ProviderID), *rec.Proof, committee)
		}
	case settlement.StatePending:
		proof.Pill, proof.PillClass = "Pending settlement", "pill-warn"
		proof.Pending = "Not proof of payment."
		if rec.Class != sdk.ClassValidUntrusted && !rec.RecheckBy.IsZero() {
			proof.Pending += " Your trust in this provider lets you treat it as admitted until " + rec.RecheckBy.UTC().Format("2006-01-02") + "."
		}
	default:
		proof.Pill, proof.PillClass = "Unsettled", "pill-muted"
		proof.Reasons = ppUnsettledReasons(rec, a.now())
	}
	return proof
}

func ppSettledDetail(provider string, pr wire.SettlementProof, committee sdk.CommitteeView) ([]ppRow, []ppWitness, string) {
	c := pr.Claim
	unit := ppUnit(c.Network, "")
	rows := []ppRow{
		{Term: "Claim ID", Value: c.ClaimID, Short: shortReceipt(c.ClaimID), Chip: true},
		{Term: "Provider", Value: provider},
		{Term: "Network", Value: c.Network},
		{Term: "Sequence", Value: strconv.FormatInt(c.Sequence, 10)},
		{Term: "Period start", Value: c.PeriodStart},
		{Term: "Period end", Value: c.PeriodEnd},
		{Term: "Receipts in claim", Value: strconv.FormatInt(c.ReceiptCount, 10)},
		{Term: "Total", Value: c.TotalBaseUnits + " " + unit},
		{Term: "Cumulative total", Value: c.CumulativeBaseUnits + " " + unit},
		{Term: "Receipts root", Value: c.ReceiptsRoot, Short: shortReceipt(c.ReceiptsRoot), Chip: true},
		{Term: "Leaf index", Value: strconv.FormatInt(pr.LeafIndex, 10)},
		{Term: "Audit path length", Value: strconv.Itoa(len(pr.AuditPath))},
	}
	if len(pr.Chain) > 0 {
		rows = append(rows, ppRow{Term: "Chain length", Value: strconv.Itoa(len(pr.Chain))})
	}
	var witnesses []ppWitness
	signed := make(map[string]bool)
	for _, w := range pr.Witnesses {
		if slices.Contains(committee.Current, w.WitnessID) {
			signed[w.WitnessID] = true
		}
		witnesses = append(witnesses, ppWitness{Row: ppRow{Term: "Monitor", Value: w.WitnessID, Short: shortReceipt(w.WitnessID), Chip: true}, At: w.WitnessedAt, Where: ppCommitteeWhere(committee, w.WitnessID)})
	}
	quorum := "The trust root names no committee, so the quorum cannot be counted."
	if n := len(committee.Current); n > 0 {
		quorum = fmt.Sprintf("%d of %d monitors witnessed; quorum is more than two-thirds (%d)", len(signed), n, settlement.Quorum(n))
	}
	return rows, witnesses, quorum
}

func ppCommitteeWhere(committee sdk.CommitteeView, monitor string) string {
	switch {
	case slices.Contains(committee.Current, monitor):
		return "in current committee"
	case slices.Contains(committee.Previous, monitor):
		return "previous committee (handover)"
	}
	return "not in the committee the trust root names"
}

func ppUnsettledReasons(rec sdk.Record, now time.Time) []string {
	var reasons []string
	if rec.Source.Kind != sdk.SourceProvider {
		reasons = append(reasons, "No proof from your sources: this copy came from a mirror or the Lifetime Vault, not the provider's own routes.")
	}
	if issued, err := wire.ParseTime(rec.Receipt.IssuedAt); err == nil && now.Sub(issued) > ppPendingWindow {
		reasons = append(reasons, "The receipt is older than 7 days, so no pending period applies.")
	}
	if rec.Class == sdk.ClassValidUntrusted {
		reasons = append(reasons, "This provider is not trusted here.")
	}
	if len(reasons) == 0 {
		reasons = append(reasons, "No settlement proof from your sources.")
	}
	return reasons
}

// ppRetentionText says how long the provider keeps a settled record, when the claim names a declaration the provider publishes.
func (a *App) ppRetentionText(r *http.Request, rec sdk.Record) string {
	if rec.Settlement != settlement.StateSettled || rec.Proof == nil {
		return ppRetentionUnknown
	}
	endpoint, ok := a.ppProviderEndpoint(r, rec.Receipt.ProviderID)
	if !ok {
		return ppRetentionUnknown
	}
	client, err := a.providerClient(endpoint)
	if err != nil {
		return ppRetentionUnknown
	}
	ctx, cancel := context.WithTimeout(r.Context(), networkReadTimeout)
	defer cancel()
	page, _, err := client.Retention(ctx)
	if err != nil {
		return ppRetentionUnknown
	}
	view, err := sdk.RetainUntil(rec.Receipt, rec.Proof.Claim, page.Declarations)
	if err != nil {
		return ppRetentionUnknown
	}
	return fmt.Sprintf("Kept by %s until at least %s (declaration %s).", nrSource{endpoint: endpoint}.label(), view.RetainUntil.UTC().Format("2006-01-02"), view.DeclarationID)
}

// ppVaultText checks the Lifetime Vault for a record and verifies its Foundation receipt.
func (a *App) ppVaultText(ctx context.Context, rec sdk.Record) string {
	if a.config.Foundation.URL == "" {
		return "No Lifetime Vault is configured on this server."
	}
	ctx, cancel := context.WithTimeout(ctx, networkReadTimeout)
	defer cancel()
	client, err := a.providerClient(a.config.Foundation)
	if err != nil {
		return "Lifetime Vault: " + nrSignalText(err)
	}
	receipt, _, err := client.VaultReceipt(ctx, rec.ID)
	if err != nil {
		return "Lifetime Vault: " + nrSignalText(err)
	}
	root, _ := a.trustRoot()
	if root == nil {
		return "The vault receipt could not be verified: the trust root is unavailable."
	}
	if err := root.VerifyVaultReceipt(receipt, rec.ID, nil); err != nil {
		return "The vault receipt could not be verified."
	}
	stored, err := wire.ParseTime(receipt.StoredAt)
	if err != nil {
		return "The vault receipt could not be verified."
	}
	return "Stored in the Lifetime Vault on " + stored.UTC().Format("2006-01-02") + " (signed by the Foundation)."
}

// ppRecheckedProof checks settlement again with the admitting provider, then the vault. It changes only what this page shows.
func (a *App) ppRecheckedProof(r *http.Request, rec sdk.Record) ppProof {
	checked := rec
	if source, ok := a.networkSource(); ok {
		ctx, cancel := context.WithTimeout(r.Context(), nrPageTimeout)
		defer cancel()
		profile := requestProfile(r)
		verifier := a.verifier(profile)
		checked = verifier.Admit(source.settleFresh(ctx, verifier, a.nrSourcesFor(profile), rec, rec.Proof))
	}
	proof := a.ppProofOf(r, checked)
	proof.ID = rec.ID
	proof.Checked = "Checked against your sources just now."
	return proof
}

func (a *App) ppSourcesOf(r *http.Request, rec sdk.Record, coverage Coverage) ppSources {
	label := rec.Source.Endpoint
	for _, src := range a.nrSourcesFor(requestProfile(r)) {
		if src.endpoint.URL == rec.Source.Endpoint {
			label = src.label()
			break
		}
	}
	served := fmt.Sprintf("%s (%s, %s)", label, ppKindName(rec.Source.Kind), rec.Source.Endpoint)
	return ppSources{Served: served, Lines: coverage.Lines()}
}

func ppKindName(kind sdk.SourceKind) string {
	switch kind {
	case sdk.SourceProvider:
		return "provider"
	case sdk.SourceMirror:
		return "mirror"
	case sdk.SourceVault:
		return "vault"
	}
	return "unknown"
}

func ppRawOf(id string, rec sdk.Record) ppRaw {
	var out bytes.Buffer
	if err := json.Indent(&out, rec.Raw, "", "  "); err != nil {
		return ppRaw{ID: id}
	}
	return ppRaw{ID: id, Text: out.String()}
}

func ppRetired(coverage Coverage) bool {
	for _, source := range coverage.Sources {
		if source.Signal == sdk.SignalRetentionExpired {
			return true
		}
	}
	return false
}
