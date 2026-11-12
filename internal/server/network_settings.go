package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/retention"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

const netFetchTimeout = 5 * time.Second

var errNetProviderListed = errors.New("provider already listed")

func init() { routeGroups = append(routeGroups, registerNetworkSettings) }

func registerNetworkSettings(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /network", a.networkPageHandler)
	mux.HandleFunc("POST /network/providers", a.networkAddHandler)
	mux.HandleFunc("POST /network/providers/trust", a.networkTrustHandler)
	mux.HandleFunc("POST /network/providers/primary", a.networkPrimaryHandler)
	mux.HandleFunc("POST /network/providers/remove", a.networkRemoveHandler)
	mux.HandleFunc("POST /network/defaults/restore", a.networkRestoreHandler)
	mux.HandleFunc("GET /network/provider", a.networkDetailHandler)
	mux.HandleFunc("POST /network/manifest/reset", a.networkResetHandler)
	mux.HandleFunc("GET /network/monitor", a.networkMonitorHandler)
}

// netNotice is a one-line result shown in the status region of a section.
type netNotice struct {
	Text  string
	Error bool
}

// netPage is the full Network page; the same sections are fragments on their own.
type netPage struct {
	Layout    Layout
	Providers netProviders
	Trust     netTrust
	Committee netCommittee
	Monitors  []netMonitor
	Checked   *netMonitorResult
}

// netProviders is the Providers section and the data its forms return.
type netProviders struct {
	Notice      netNotice
	Providers   []netProviderView
	Removed     []netProviderView
	Defaults    ListDisclosure
	HasDefaults bool
	Single      bool
	Open        string
	Detail      netDetail
}

type netProviderView struct {
	Anchor, Label, Endpoint, OperatorKey string
	Default, Manual, Trusted, Publishing bool
	Verified, CanPublish                 bool
}

// netDetail is the verified manifest, pin state and retention of one provider.
type netDetail struct {
	Anchor, Label, Endpoint string
	Notice                  netNotice
	Message                 string
	Fields                  []netField
	PinTone, PinText        string
	CanReset                bool
	Retention               []string
	Lifetime                bool
}

type netField struct {
	Name, Value string
}

type netTrust struct {
	Known        bool
	Tone, Status string
	Endpoint     string
	ReleaseKeys  []string
	Threshold    int
	Foundations  []netFoundation
	Schedules    []netSchedule
	Rejected     []sdk.RejectedObject
	Closures     int
}

type netFoundation struct {
	ID, ValidFrom, ValidUntil, Bitcoin, Ethereum string
}

type netSchedule struct {
	Network, Denomination, BaseFee, PerKiBFee, EffectiveAt, ExpiresAt string
	Revision                                                          int64
}

type netCommittee struct {
	Known                 bool
	Current, Previous     []string
	Count, Quorum         int
	PolicyID, EffectiveAt string
}

type netMonitor struct {
	Anchor, Label, Endpoint string
}

type netMonitorResult struct {
	Label, Endpoint string
	Lines           []string
}

func netAnchor(endpoint string) string {
	sum := sha256.Sum256([]byte(endpoint))
	return "provider-" + hex.EncodeToString(sum[:6])
}

func netHost(endpoint string) string {
	if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
		return u.Host
	}
	return endpoint
}

func (a *App) networkPageHandler(w http.ResponseWriter, r *http.Request) {
	a.page(w, r, "network-page", a.netPageData(r, a.netProvidersView(requestProfile(r), netNotice{})))
}

func (a *App) netPageData(r *http.Request, providers netProviders) netPage {
	return netPage{Layout: a.layout(r, "network", "Network"), Providers: providers, Trust: a.netTrustView(), Committee: a.netCommitteeView(), Monitors: a.netMonitorViews()}
}

func (a *App) netProvidersView(profile string, notice netNotice) netProviders {
	settings, _ := a.settings(profile)
	pins := a.netPinsFor(profile)
	selected := a.selection(profile).Providers
	view := netProviders{Notice: notice, Defaults: a.config.DefaultList, HasDefaults: a.config.DefaultList.Controller != "", Single: len(selected) == 1}
	publishing := ""
	for _, provider := range selected {
		if provider.Trusted {
			publishing = provider.Endpoint
			break
		}
	}
	for _, provider := range selected {
		_, _, verified := pins.Pinned(provider.OperatorKey)
		view.Providers = append(view.Providers, netProviderView{Anchor: netAnchor(provider.Endpoint), Label: provider.Label, Endpoint: provider.Endpoint, OperatorKey: provider.OperatorKey, Default: provider.Source == "default", Manual: provider.Source == "manual", Trusted: provider.Trusted, Publishing: provider.Endpoint == publishing, Verified: verified, CanPublish: provider.Trusted && provider.Endpoint != publishing})
	}
	for _, endpoint := range a.config.Providers {
		if containsString(settings.RemovedDefaults, endpoint.URL) {
			view.Removed = append(view.Removed, netProviderView{Anchor: netAnchor(endpoint.URL), Label: endpoint.Label, Endpoint: endpoint.URL})
		}
	}
	return view
}

func (a *App) netTrustView() netTrust {
	root, status := a.trustRoot()
	view := netTrust{Endpoint: status.Endpoint}
	switch status.State {
	case TrustReady:
		view.Tone, view.Status = "ok", "Verified "+relativeTime(status.FetchedAt, a.now())
	case TrustStale:
		view.Tone, view.Status = "warn", "Stale since "+relativeTime(status.FetchedAt, a.now())+": "+status.Err
	default:
		view.Tone, view.Status = "bad", "Unavailable: "+status.Err
	}
	if root == nil {
		return view
	}
	summary := root.Summary()
	view.Known = true
	view.ReleaseKeys = summary.ReleaseKeyIDs
	view.Threshold = summary.Threshold
	view.Closures = len(summary.Closures)
	view.Rejected = summary.Rejected
	for _, f := range summary.Foundations {
		view.Foundations = append(view.Foundations, netFoundation{ID: f.FoundationID, ValidFrom: netDate(f.ValidFrom), ValidUntil: netDate(f.ValidUntil), Bitcoin: f.Recipients.BitcoinMainnet, Ethereum: f.Recipients.EthereumMainnet})
	}
	for _, s := range summary.Schedules {
		view.Schedules = append(view.Schedules, netSchedule{Network: s.Network, Denomination: s.Denomination, BaseFee: s.BaseFeeBaseUnits, PerKiBFee: s.PerKiBFeeBaseUnits, EffectiveAt: netDate(s.EffectiveAt), ExpiresAt: netDate(s.ExpiresAt), Revision: s.Revision})
	}
	return view
}

func (a *App) netCommitteeView() netCommittee {
	root, _ := a.trustRoot()
	if root == nil {
		return netCommittee{}
	}
	view := root.Committee(a.now())
	return netCommittee{Known: true, Current: view.Current, Previous: view.Previous, Count: len(view.Current), Quorum: settlement.Quorum(len(view.Current)), PolicyID: view.PolicyID, EffectiveAt: netDate(root.Summary().CommitteeEffectiveAt)}
}

func (a *App) netMonitorViews() []netMonitor {
	monitors := make([]netMonitor, 0, len(a.config.Monitors))
	for _, monitor := range a.config.Monitors {
		label := monitor.Label
		if label == "" {
			label = monitor.URL
		}
		monitors = append(monitors, netMonitor{Anchor: netAnchor("monitor\x00" + monitor.URL), Label: label, Endpoint: monitor.URL})
	}
	return monitors
}

func (a *App) netProvider(profile, endpoint string) (ProviderChoice, bool) {
	for _, provider := range a.selection(profile).Providers {
		if provider.Endpoint == endpoint {
			return provider, true
		}
	}
	return ProviderChoice{}, false
}

// netFinish answers a state-changing form: the section as a fragment, the page
// again when the form failed without script, or a redirect to the section.
func (a *App) netFinish(w http.ResponseWriter, r *http.Request, profile, anchor string, notice netNotice) {
	providers := a.netProvidersView(profile, notice)
	if r.Header.Get("Sec-Fetch-Dest") == "empty" {
		a.fragment(w, r, "network-providers", providers)
		return
	}
	if notice.Error {
		a.page(w, r, "network-page", a.netPageData(r, providers))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/network#"+anchor, http.StatusSeeOther)
}

func (a *App) networkAddHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := readActionForm(w, r, "endpoint", "operator_key")
	if !ok {
		return
	}
	profile, err := a.profile(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "Profile unavailable.")
		return
	}
	notice := a.netAdd(r.Context(), profile, strings.TrimSpace(form.Get("endpoint")), strings.TrimSpace(form.Get("operator_key")), form.Get("unverified") == "1")
	a.netFinish(w, r, profile, "network-providers", notice)
}

// netAdd adds a provider untrusted. Its manifest must verify against the entered key,
// unless the reader explicitly accepts an unverified provider after the fetch failed.
func (a *App) netAdd(ctx context.Context, profile, rawEndpoint, operatorKey string, acceptUnverified bool) netNotice {
	endpoint, err := a.netEndpoint(rawEndpoint)
	if err != nil {
		return netNotice{Text: err.Error(), Error: true}
	}
	public, err := keys.ParseDID(operatorKey)
	if err != nil || keys.DID(public) != operatorKey {
		return netNotice{Text: "The operator key must be a did:key for an Ed25519 key, written as the operator shows it.", Error: true}
	}
	for _, provider := range a.selection(profile).Providers {
		if provider.Endpoint == endpoint {
			return netNotice{Text: "That provider is already in your list.", Error: true}
		}
	}
	client, err := a.providerClient(Endpoint{URL: endpoint, ServiceID: operatorKey})
	if err != nil {
		return netNotice{Text: "The provider address could not be used.", Error: true}
	}
	ctx, cancel := context.WithTimeout(ctx, netFetchTimeout)
	defer cancel()
	manifest, raw, err := client.OperatorManifest(ctx)
	verified := err == nil
	if err == nil {
		switch status := a.netCheckManifest(profile, operatorKey, raw, manifest); status {
		case sdk.ManifestCurrent, sdk.ManifestSame:
		default:
			return netNotice{Text: netRefusal(status), Error: true}
		}
	} else if !acceptUnverified {
		return netNotice{Text: "The operator manifest could not be fetched: " + err.Error() + ". To add the provider without a verified manifest, tick the box and submit again.", Error: true}
	}
	host := netHost(endpoint)
	err = a.updateSettings(profile, func(s *ProfileSettings) error {
		for _, provider := range s.Providers {
			if provider.Endpoint == endpoint {
				return errNetProviderListed
			}
		}
		s.Providers = append(s.Providers, ProviderChoice{Endpoint: endpoint, OperatorKey: operatorKey, Label: host, Source: "manual"})
		return nil
	})
	if errors.Is(err, errNetProviderListed) {
		return netNotice{Text: "That provider is already in your list.", Error: true}
	}
	if err != nil {
		return netNotice{Text: "The provider could not be saved.", Error: true}
	}
	if !verified {
		return netNotice{Text: "Added " + host + " without a verified manifest. It is not trusted yet."}
	}
	return netNotice{Text: "Added " + host + ". Its manifest is verified. It is not trusted until you turn trust on."}
}

func (a *App) netEndpoint(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("The endpoint must be an address with no user name, query or fragment.")
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && a.config.Dev && loopbackHost(u.Hostname()):
	case u.Scheme == "http":
		return "", errors.New("Remote endpoints must use https://. Plain http:// is accepted only for loopback addresses in dev mode.")
	default:
		return "", errors.New("The endpoint must be an https:// address.")
	}
	return strings.TrimRight(u.String(), "/"), nil
}

func netRefusal(status sdk.ManifestStatus) string {
	switch status {
	case sdk.ManifestRollback:
		return "The provider offered an older manifest than one already pinned. It is refused."
	case sdk.ManifestEquivocation:
		return "Equivocation: two different manifests claim this version. Review the operator key and policy."
	case sdk.ManifestExpired:
		return "The manifest is outside its validity window or has expired. It is refused."
	case sdk.ManifestBadSignature:
		return "The manifest does not verify against this operator key. It is refused."
	default:
		return "The manifest is refused."
	}
}

func (a *App) networkTrustHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := readActionForm(w, r, "endpoint", "trusted")
	if !ok {
		return
	}
	value := form.Get("trusted")
	if value != "0" && value != "1" {
		http.Error(w, "invalid trust choice", http.StatusBadRequest)
		return
	}
	profile, err := a.profile(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "Profile unavailable.")
		return
	}
	endpoint := form.Get("endpoint")
	provider, found := a.netProvider(profile, endpoint)
	if !found {
		a.netFinish(w, r, profile, netAnchor(endpoint), netNotice{Text: "That provider is not in your list.", Error: true})
		return
	}
	trusted := value == "1"
	err = a.updateSettings(profile, func(s *ProfileSettings) error {
		netSetTrusted(s, provider, trusted)
		return nil
	})
	notice := netNotice{Text: "Not trusted. Its records are shown as untrusted."}
	if trusted {
		notice = netNotice{Text: "Trusted. Its records are classified as trusted from now on."}
	}
	if err != nil {
		notice = netNotice{Text: "The choice could not be saved.", Error: true}
	}
	a.netFinish(w, r, profile, netAnchor(endpoint), notice)
}

func netSetTrusted(s *ProfileSettings, provider ProviderChoice, trusted bool) {
	for i := range s.Providers {
		if s.Providers[i].Endpoint == provider.Endpoint {
			s.Providers[i].Trusted = trusted
			return
		}
	}
	provider.Trusted = trusted
	s.Providers = append(s.Providers, provider)
}

func (a *App) networkPrimaryHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := readActionForm(w, r, "endpoint")
	if !ok {
		return
	}
	profile, err := a.profile(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "Profile unavailable.")
		return
	}
	endpoint := form.Get("endpoint")
	notice := netNotice{Text: "This provider is now used for publishing."}
	provider, found := a.netProvider(profile, endpoint)
	switch {
	case !found:
		notice = netNotice{Text: "That provider is not in your list.", Error: true}
	case !provider.Trusted:
		notice = netNotice{Text: "Turn trust on before using this provider for publishing.", Error: true}
	default:
		if err := a.updateSettings(profile, func(s *ProfileSettings) error {
			s.PrimaryProvider = endpoint
			return nil
		}); err != nil {
			notice = netNotice{Text: "The choice could not be saved.", Error: true}
		}
	}
	a.netFinish(w, r, profile, netAnchor(endpoint), notice)
}

func (a *App) networkRemoveHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := readActionForm(w, r, "endpoint")
	if !ok {
		return
	}
	profile, err := a.profile(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "Profile unavailable.")
		return
	}
	endpoint := form.Get("endpoint")
	provider, found := a.netProvider(profile, endpoint)
	notice := netNotice{Text: "Removed " + netHost(endpoint) + "."}
	if !found {
		notice = netNotice{Text: "That provider is not in your list.", Error: true}
	} else if err := a.updateSettings(profile, func(s *ProfileSettings) error {
		netRemoveProvider(s, provider)
		return nil
	}); err != nil {
		notice = netNotice{Text: "The provider could not be removed.", Error: true}
	}
	a.netFinish(w, r, profile, "network-providers", notice)
}

func netRemoveProvider(s *ProfileSettings, provider ProviderChoice) {
	kept := make([]ProviderChoice, 0, len(s.Providers))
	for _, choice := range s.Providers {
		if choice.Endpoint != provider.Endpoint {
			kept = append(kept, choice)
		}
	}
	s.Providers = kept
	if provider.Source == "default" && !containsString(s.RemovedDefaults, provider.Endpoint) {
		s.RemovedDefaults = append(s.RemovedDefaults, provider.Endpoint)
	}
	if s.PrimaryProvider == provider.Endpoint {
		s.PrimaryProvider = ""
	}
}

func (a *App) networkRestoreHandler(w http.ResponseWriter, r *http.Request) {
	if !requireSameOrigin(w, r) || !parseSmallForm(w, r, actionFormLimit) {
		return
	}
	profile, err := a.profile(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "Profile unavailable.")
		return
	}
	notice := netNotice{Text: "Default providers restored."}
	if err := a.updateSettings(profile, func(s *ProfileSettings) error {
		s.RemovedDefaults = nil
		return nil
	}); err != nil {
		notice = netNotice{Text: "The defaults could not be restored.", Error: true}
	}
	a.netFinish(w, r, profile, "network-providers", notice)
}

func (a *App) networkDetailHandler(w http.ResponseWriter, r *http.Request) {
	profile := requestProfile(r)
	provider, found := a.netProvider(profile, r.URL.Query().Get("endpoint"))
	if !found {
		a.fail(w, r, http.StatusNotFound, "That provider is not in your list.")
		return
	}
	a.netDetailResponse(w, r, profile, provider, netNotice{})
}

func (a *App) netDetailResponse(w http.ResponseWriter, r *http.Request, profile string, provider ProviderChoice, notice netNotice) {
	detail := a.netDetail(r.Context(), profile, provider, notice)
	if r.Header.Get("Sec-Fetch-Dest") == "empty" {
		a.fragment(w, r, "network-provider-detail", detail)
		return
	}
	providers := a.netProvidersView(profile, netNotice{})
	providers.Open = detail.Anchor
	providers.Detail = detail
	a.page(w, r, "network-page", a.netPageData(r, providers))
}

func (a *App) netDetail(ctx context.Context, profile string, provider ProviderChoice, notice netNotice) netDetail {
	detail := netDetail{Anchor: netAnchor(provider.Endpoint), Label: provider.Label, Endpoint: provider.Endpoint, Notice: notice}
	client, err := a.providerClient(Endpoint{URL: provider.Endpoint, ServiceID: provider.OperatorKey})
	if err != nil {
		detail.Message = "The provider address could not be used."
		return detail
	}
	ctx, cancel := context.WithTimeout(ctx, netFetchTimeout)
	defer cancel()
	manifest, raw, err := client.OperatorManifest(ctx)
	if err != nil {
		detail.Message = "Manifest unavailable: " + err.Error()
		if version, _, pinned := a.netPinsFor(profile).Pinned(provider.OperatorKey); pinned {
			detail.PinTone, detail.PinText = "ok", fmt.Sprintf("Pinned version %d", version)
		}
	} else {
		status := a.netCheckManifest(profile, provider.OperatorKey, raw, manifest)
		if status != sdk.ManifestBadSignature {
			detail.Fields = netManifestFields(manifest)
		}
		detail.PinTone, detail.PinText = netPinStatus(status, manifest.ManifestVersion)
		detail.CanReset = status == sdk.ManifestEquivocation
	}
	detail.Retention = netRetentionLines(ctx, client, provider.OperatorKey)
	detail.Lifetime = a.config.Foundation.URL != ""
	return detail
}

func netManifestFields(m wire.OperatorManifest) []netField {
	return []netField{
		{"Operator ID", m.OperatorID},
		{"Manifest version", fmt.Sprint(m.ManifestVersion)},
		{"Issued", m.IssuedAt},
		{"Expires", m.ExpiresAt},
		{"Service roles", strings.Join(m.ServiceRoles, ", ")},
		{"Service endpoints", strings.Join(m.ServiceEndpoints, ", ")},
		{"Supported profiles", strings.Join(m.SupportedProfiles, ", ")},
		{"Policy URL (text only)", m.PolicyURL},
		{"Policy digest", m.PolicyDigest},
		{"Legal contact", m.LegalContact},
		{"Abuse contact", m.AbuseContact},
		{"Privacy contact", m.PrivacyContact},
		{"Appeal URL (text only)", m.AppealURL},
		{"Markets assessed", strings.Join(m.MarketsAssessed, ", ")},
	}
}

func netPinStatus(status sdk.ManifestStatus, version int64) (string, string) {
	switch status {
	case sdk.ManifestCurrent, sdk.ManifestSame:
		return "ok", fmt.Sprintf("Pinned version %d", version)
	case sdk.ManifestRollback:
		return "bad", "Older version offered; rejected"
	case sdk.ManifestEquivocation:
		return "bad", fmt.Sprintf("Equivocation: two different manifests claim version %d. Review the operator key and policy.", version)
	case sdk.ManifestExpired:
		return "bad", "Manifest expired or outside its validity window; not pinned"
	default:
		return "bad", "Manifest signature does not verify; not pinned"
	}
}

// netRetentionLines lists only declarations of the operator key whose signature verifies and that meet the protocol minimum.
func netRetentionLines(ctx context.Context, client *sdk.Client, operatorKey string) []string {
	page, _, err := client.Retention(ctx)
	if err != nil {
		return nil
	}
	var lines []string
	for _, declaration := range retention.NewestFirst(page.Declarations) {
		if declaration.ProviderID != operatorKey || retention.Verify(declaration) != nil {
			continue
		}
		lines = append(lines, fmt.Sprintf("Keeps records at least %d days (declaration %s, effective %s)", declaration.RetentionSeconds/86400, declaration.DeclarationID, netDate(declaration.EffectiveAt)))
	}
	return lines
}

func netDate(value string) string {
	if at, err := wire.ParseTime(value); err == nil {
		return at.Format("2006-01-02")
	}
	return value
}

func (a *App) networkResetHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := readActionForm(w, r, "endpoint")
	if !ok {
		return
	}
	profile, err := a.profile(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "Profile unavailable.")
		return
	}
	provider, found := a.netProvider(profile, form.Get("endpoint"))
	if !found {
		a.fail(w, r, http.StatusNotFound, "That provider is not in your list.")
		return
	}
	notice := netNotice{Text: "Trust in this manifest was reset. The next check pins the manifest the provider offers."}
	if form.Get("confirm") != "1" {
		notice = netNotice{Text: "Confirm that you reviewed the operator key and policy before resetting.", Error: true}
	} else if err := a.netResetPin(profile, provider.OperatorKey); err != nil {
		notice = netNotice{Text: "The reset could not be saved.", Error: true}
	}
	a.netDetailResponse(w, r, profile, provider, notice)
}

func (a *App) networkMonitorHandler(w http.ResponseWriter, r *http.Request) {
	monitor, found := a.netMonitorEndpoint(r.URL.Query().Get("endpoint"))
	if !found {
		a.fail(w, r, http.StatusNotFound, "That monitor is not configured.")
		return
	}
	profile := requestProfile(r)
	result := a.netMonitorCheck(r.Context(), profile, monitor)
	if r.Header.Get("Sec-Fetch-Dest") == "empty" {
		a.fragment(w, r, "network-monitor-result", result)
		return
	}
	page := a.netPageData(r, a.netProvidersView(profile, netNotice{}))
	page.Checked = &result
	a.page(w, r, "network-page", page)
}

func (a *App) netMonitorEndpoint(endpoint string) (Endpoint, bool) {
	for _, monitor := range a.config.Monitors {
		if monitor.URL == endpoint {
			return monitor, true
		}
	}
	return Endpoint{}, false
}

func (a *App) netMonitorCheck(ctx context.Context, profile string, monitor Endpoint) netMonitorResult {
	label := monitor.Label
	if label == "" {
		label = monitor.URL
	}
	result := netMonitorResult{Label: label, Endpoint: monitor.URL}
	client, err := a.providerClient(monitor)
	if err != nil {
		result.Lines = []string{"Monitor unavailable: " + err.Error()}
		return result
	}
	for _, provider := range a.selection(profile).Providers {
		ctx, cancel := context.WithTimeout(ctx, netFetchTimeout)
		page, _, err := client.Equivocations(ctx, provider.OperatorKey)
		cancel()
		if err != nil {
			result.Lines = append(result.Lines, "Could not ask this monitor about "+provider.Label+": "+err.Error())
			continue
		}
		if len(page.Proofs) == 0 {
			result.Lines = append(result.Lines, "No equivocation proofs for "+provider.Label+".")
		}
		for _, proof := range page.Proofs {
			if settlement.VerifyEquivocation(proof) && netProofAbout(proof, provider.OperatorKey) {
				result.Lines = append(result.Lines, "Equivocation proof for "+provider.Label+".")
			} else {
				result.Lines = append(result.Lines, "Claim about "+provider.Label+" is an unverified claim.")
			}
		}
	}
	return result
}

func netProofAbout(proof wire.EquivocationProof, operatorKey string) bool {
	for _, claim := range proof.Claims {
		if claim.ProviderID != operatorKey {
			return false
		}
	}
	return true
}

// netPrimaryFirst moves the provider chosen for publishing to the front of the selection.
func netPrimaryFirst(providers []ProviderChoice, primary string) []ProviderChoice {
	for i := range providers {
		if providers[i].Endpoint == primary && i > 0 {
			ordered := append([]ProviderChoice{providers[i]}, providers[:i]...)
			return append(ordered, providers[i+1:]...)
		}
	}
	return providers
}

func (a *App) netPinsPath(profile string) string {
	return filepath.Join(a.profiles.root, profile, "manifests.json")
}

// netReadPins loads the profile's manifest pins and the bytes they were read from.
func (a *App) netReadPins(profile string) (*sdk.ManifestPins, []byte, error) {
	data, err := os.ReadFile(a.netPinsPath(profile))
	if errors.Is(err, os.ErrNotExist) {
		pins := sdk.NewManifestPins()
		snapshot, snapErr := pins.Snapshot()
		return pins, snapshot, snapErr
	}
	if err != nil {
		return nil, nil, err
	}
	pins, err := sdk.RestoreManifestPins(data)
	return pins, data, err
}

func (a *App) netPinsFor(profile string) *sdk.ManifestPins {
	if !validProfileID(profile) {
		return sdk.NewManifestPins()
	}
	pins, _, err := a.netReadPins(profile)
	if err != nil {
		return sdk.NewManifestPins()
	}
	return pins
}

// netCheckManifest pins a fetched manifest for the profile, persisting the pin state only when it changes.
func (a *App) netCheckManifest(profile, operatorKey string, raw []byte, manifest wire.OperatorManifest) sdk.ManifestStatus {
	if !validProfileID(profile) {
		return sdk.NewManifestPins().Check(operatorKey, raw, manifest, a.now())
	}
	lock := a.profiles.lock(profile)
	lock.Lock()
	defer lock.Unlock()
	pins, before, err := a.netReadPins(profile)
	if err != nil {
		return sdk.ManifestBadSignature
	}
	status := pins.Check(operatorKey, raw, manifest, a.now())
	after, err := pins.Snapshot()
	if err != nil {
		return sdk.ManifestBadSignature
	}
	if !bytes.Equal(before, after) && saveLedger(a.netPinsPath(profile), after) != nil {
		return sdk.ManifestBadSignature
	}
	return status
}

func (a *App) netResetPin(profile, operatorKey string) error {
	lock := a.profiles.lock(profile)
	lock.Lock()
	defer lock.Unlock()
	pins, _, err := a.netReadPins(profile)
	if err != nil {
		return err
	}
	pins.Reset(operatorKey)
	snapshot, err := pins.Snapshot()
	if err != nil {
		return err
	}
	return saveLedger(a.netPinsPath(profile), snapshot)
}
