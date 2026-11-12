package server

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"foryou.local/foryou/internal/distribution"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

// ldgReadTimeout bounds each remote call a ledger page makes.
const ldgReadTimeout = 5 * time.Second

const (
	ldgPageSize   = 10
	ldgPageLimit  = 100
	ldgScanPages  = 10
	ldgMaxClaimID = 9007199254740991
)

var ldgNetworkOrder = []string{wire.BitcoinMainnet, wire.EthereumMainnet}

func init() { routeGroups = append(routeGroups, ldgRegister) }

func ldgRegister(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /ledger", a.ldgPage)
	mux.HandleFunc("GET /ledger/claims", a.ldgClaimsFragment)
	mux.HandleFunc("GET /ledger/fee", a.ldgFee)
	mux.HandleFunc("GET /ledger/fee/vault", a.ldgFeeVault)
}

// ldgPageData is the full Ledger page.
type ldgPageData struct {
	Layout       Layout
	Claims       ldgClaimsView
	Schedules    []ldgSchedule
	ScheduleNote string
	Vault        ldgVault
	Distribution ldgDistribution
}

// ldgQuery is the state of the claims list, carried in the URL only.
type ldgQuery struct {
	Network  string
	Provider int
	After    uint64
	HasAfter bool
}

// ldgTime is a UTC instant shown in words and as a machine-readable time.
type ldgTime struct{ Text, ISO string }

func ldgTimeOf(t time.Time) ldgTime {
	t = t.UTC()
	return ldgTime{Text: t.Format("2 Jan 2006, 15:04 UTC"), ISO: t.Format(time.RFC3339)}
}

func ldgTimeText(value string) string {
	t, err := wire.ParseTime(value)
	if err != nil {
		return value
	}
	return ldgTimeOf(t).Text
}

// ldgPeriod names a claim or statement period; a period on one day is written once.
func ldgPeriod(start, end time.Time) string {
	start, end = start.UTC(), end.UTC()
	if start.Format("2 Jan 2006") == end.Format("2 Jan 2006") {
		return start.Format("2 Jan 2006, 15:04") + " to " + end.Format("15:04") + " UTC"
	}
	return start.Format("2 Jan 2006, 15:04 UTC") + " to " + end.Format("2 Jan 2006, 15:04 UTC")
}

// ldgAmount shows an integer in base units with its unit; it never shows a float or a price.
func ldgAmount(network, baseUnits string) string {
	label, ok := feeLabels[network]
	if !ok {
		return "unreadable amount"
	}
	if _, err := wire.Amount(baseUnits); err != nil {
		return "unreadable amount"
	}
	return groupDigits(baseUnits) + " " + label.Unit
}

// ldgNetworkName gives the reader's name for a network.
func ldgNetworkName(network string) string {
	if label, ok := feeLabels[network]; ok {
		return label.Asset
	}
	return network
}

// ldgNamed returns a configured label, or the fallback when none was set.
func ldgNamed(label, fallback string) string {
	if label == "" {
		return fallback
	}
	return label
}

// ldgWho names the service behind a key using this client's own configuration only.
func (a *App) ldgWho(profile, key string) string {
	for _, provider := range a.selection(profile).Providers {
		if provider.OperatorKey == key {
			return ldgNamed(provider.Label, "a provider")
		}
	}
	for _, monitor := range a.config.Monitors {
		if monitor.ServiceID == key {
			return ldgNamed(monitor.Label, "a monitor")
		}
	}
	if a.config.Foundation.URL != "" && a.config.Foundation.ServiceID == key {
		return ldgNamed(a.config.Foundation.Label, "the ForYou Foundation")
	}
	return "an unknown key"
}

// ldgKnown reports whether a key is named by this client's configuration.
func (a *App) ldgKnown(profile, key string) bool {
	return a.ldgWho(profile, key) != "an unknown key"
}

// ldgSigner says which service signed a value and whether this client verified it.
type ldgSigner struct{ By, Key, State, Reason string }

// Short shortens the signing key for display.
func (s ldgSigner) Short() string { return shortDID(s.Key) }

func ldgVerified(by, key string) ldgSigner {
	return ldgSigner{By: by, Key: key, State: "verified"}
}

func ldgFailed(by, key string, err error) ldgSigner {
	return ldgSigner{By: by, Key: key, State: "failed", Reason: err.Error()}
}

func ldgUnchecked(by, key, reason string) ldgSigner {
	return ldgSigner{By: by, Key: key, State: "unchecked", Reason: reason}
}

// ldgReason gives a short cause for a failed service call.
func ldgReason(err error) string {
	var answered *sdk.Error
	if errors.As(err, &answered) {
		return "HTTP " + strconv.Itoa(answered.HTTPStatus)
	}
	return "the request could not be completed"
}

// ldgFailure says what a service did, in words, without claiming that a thing does not exist anywhere.
func ldgFailure(label string, err error) string {
	var answered *sdk.Error
	if errors.As(err, &answered) {
		switch answered.HTTPStatus {
		case http.StatusNotFound:
			return label + " has no such record."
		case http.StatusGone:
			return label + " no longer holds it."
		case http.StatusForbidden:
			return label + " is restricted from serving it."
		case http.StatusTooManyRequests:
			return label + " asked us to slow down."
		}
	}
	return label + " did not answer (" + ldgReason(err) + ")."
}

// ldgFoundationLabel names the configured Foundation.
func (a *App) ldgFoundationLabel() string {
	return ldgNamed(a.config.Foundation.Label, "the ForYou Foundation")
}

// ldgDigits reports whether a query value is a plain run of decimal digits.
func ldgDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, c := range value {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// ldgParse reads the claims parameters. A parameter that is present must be valid.
func ldgParse(values url.Values, providers int) (ldgQuery, string) {
	q := ldgQuery{Network: wire.BitcoinMainnet}
	if values.Has("network") {
		if !slices.Contains(ldgNetworkOrder, values.Get("network")) {
			return q, "Unknown network."
		}
		q.Network = values.Get("network")
	}
	if values.Has("provider") {
		raw := values.Get("provider")
		index, err := strconv.Atoi(raw)
		if !ldgDigits(raw) || err != nil || index >= providers {
			return q, "Unknown provider choice."
		}
		q.Provider = index
	}
	if values.Has("after") {
		raw := values.Get("after")
		position, err := strconv.ParseUint(raw, 10, 64)
		if !ldgDigits(raw) || err != nil || position > ldgMaxClaimID {
			return q, "Invalid claim position."
		}
		q.After, q.HasAfter = position, true
	}
	return q, ""
}

// ldgQueryString encodes the claims state for links and forms.
func ldgQueryString(network string, provider int, after uint64, hasAfter bool) string {
	values := url.Values{"network": {network}, "provider": {strconv.Itoa(provider)}}
	if hasAfter {
		values.Set("after", strconv.FormatUint(after, 10))
	}
	return values.Encode()
}

// ldgOption is one choice in a switch or a select.
type ldgOption struct {
	Value, Label string
	Selected     bool
}

// ldgClaimsView is the settlement claims section and everything it shows.
type ldgClaimsView struct {
	Network, NetworkName string
	Networks             []ldgOption
	Providers            []ldgOption
	ProviderLabel        string
	ProviderIndex        string
	MultiProvider        bool
	NoProvider           bool
	Status               string
	Notice               string
	Failed               string
	Retry                string
	Empty                string
	Older, Newer         string
	OlderGet, NewerGet   string
	Claims               []ldgClaimRow
	Committee            ldgCommittee
	Sources              string
	Dev                  bool
}

// ldgCommittee describes the committee in force, and the one before it during a handover.
type ldgCommittee struct {
	Known         bool
	Warn          string
	Count         int
	Quorum        int
	Policy        string
	Handover      bool
	ChangedAt     string
	Until         string
	PreviousCount int
}

// ldgClaimRow is one settlement claim with its witnesses and quorum.
type ldgClaimRow struct {
	Sequence      int64
	Heading       string
	Period        string
	StartISO      string
	Receipts      string
	Total         string
	Running       string
	ClaimID       string
	ClaimFull     string
	Retention     string
	RetentionFull string
	Previous      string
	PreviousFull  string
	Signer        ldgSigner
	WrongKey      bool
	Known         bool
	Quorum        bool
	Counted       int
	Members       int
	Rows          []ldgMonitorRow
	PreviousRows  []ldgMonitorRow
	Outside       []ldgMonitorRow
	Handover      bool
}

// ldgMonitorRow is one monitor's state for one claim.
type ldgMonitorRow struct {
	Label, Key, Short string
	Text              string
	Witnessed         bool
	When              ldgTime
	Signer            ldgSigner
}

// ldgMonitorPage is what one configured monitor said, after its own statements were verified.
type ldgMonitorPage struct {
	Endpoint  Endpoint
	Err       error
	Witnesses []wire.SettlementWitness
	Refusals  []wire.SettlementRefusal
	Holds     []wire.SettlementHold
}

// ldgSchedule is a fee schedule in force for one network.
type ldgSchedule struct {
	Network, Revision, PerKiB, Base, OneKiB string
	Effective, Expires, ID, IDFull          string
	Signer                                  ldgSigner
	Checked                                 string
	Missing                                 string
}

// ldgVault is the latest Lifetime Vault status.
type ldgVault struct {
	Missing, Empty bool
	Failure        string
	Period         string
	Records        string
	Bytes          string
	Funding        []ldgOption
	Signer         ldgSigner
	Note           string
}

// ldgDistribution is the latest distribution statement for each network.
type ldgDistribution struct {
	Missing, Empty bool
	Failure        string
	Statements     []ldgStatement
	Disclosures    []ldgDisclosure
	Tied           string
}

// ldgDisclosure is what the Foundation states about one monitor it relies on.
type ldgDisclosure struct {
	Monitor, Short, Key string
	Known               bool
	Policies            string
	Criteria            string
	Relationships       []ldgRelationship
}

type ldgRelationship struct{ Party, Kind, Description string }

// ldgDisclosures folds the page's disclosures into one entry per monitor and statement text,
// counting the policies each covers, and says how many monitors declare a tie.
func (a *App) ldgDisclosures(profile string, disclosures []wire.MonitorDisclosure) ([]ldgDisclosure, string) {
	var out []ldgDisclosure
	policies := map[int]int{}
	index := map[string]int{}
	tied, monitors := map[string]bool{}, map[string]bool{}
	for _, d := range disclosures {
		monitors[d.MonitorID] = true
		if len(d.Relationships) > 0 {
			tied[d.MonitorID] = true
		}
		key := d.MonitorID + "\x00" + d.SelectionCriteria
		for _, rel := range d.Relationships {
			key += "\x00" + rel.Party + "\x00" + rel.Kind + "\x00" + rel.Description
		}
		at, seen := index[key]
		if !seen {
			at = len(out)
			index[key] = at
			row := ldgDisclosure{Key: d.MonitorID, Short: shortDID(d.MonitorID), Criteria: d.SelectionCriteria, Known: a.ldgKnown(profile, d.MonitorID)}
			if row.Known {
				row.Monitor = a.ldgWho(profile, d.MonitorID)
			}
			for _, rel := range d.Relationships {
				row.Relationships = append(row.Relationships, ldgRelationship{Party: ldgPartyText(rel.Party), Kind: rel.Kind, Description: rel.Description})
			}
			out = append(out, row)
		}
		policies[at]++
	}
	for i := range out {
		out[i].Policies = "1 policy"
		if policies[i] != 1 {
			out[i].Policies = fmt.Sprintf("%d policies", policies[i])
		}
	}
	if len(monitors) == 0 {
		return out, ""
	}
	return out, fmt.Sprintf("%d of %d monitors have a declared funding or ownership tie to the Foundation or a provider.", len(tied), len(monitors))
}

// ldgPartyText names the other side of a declared tie.
func ldgPartyText(party string) string {
	if party == "foundation" {
		return "The Foundation"
	}
	if strings.HasPrefix(party, "did:key:") {
		return "Provider " + shortDID(party)
	}
	return party
}

type ldgStatement struct {
	Heading, Received, Distributed, Policy string
	Signer                                 ldgSigner
	Entries                                []ldgEntry
	Monitors                               []ldgPayment
}

type ldgEntry struct {
	Provider, Short, Key, Settled, Quality, Retention, Amount, Owed, Payout, Transfer string
	Known, Paid                                                                       bool
}

type ldgPayment struct {
	Monitor, Short, Key, Amount, Transfer string
	Known, Paid                           bool
}

// ldgQualityText writes a basis-point quality as a percentage with two decimals.
func ldgQualityText(bp int64) string {
	return fmt.Sprintf("%d.%02d%%", bp/100, bp%100)
}

// ldgPage renders the Ledger page: claims, fee schedules, the vault and the distribution statement.
func (a *App) ldgPage(w http.ResponseWriter, r *http.Request) {
	profile := requestProfile(r)
	providers := len(a.selection(profile).Providers)
	q, message := ldgParse(r.URL.Query(), providers)
	if message != "" {
		a.fail(w, r, http.StatusBadRequest, message)
		return
	}
	var (
		wg           sync.WaitGroup
		vault        ldgVault
		distribution ldgDistribution
	)
	wg.Add(2)
	go func() {
		defer wg.Done()
		vault = a.ldgVaultSection(r, profile)
	}()
	go func() {
		defer wg.Done()
		distribution = a.ldgDistributionSection(r, profile)
	}()
	claims := a.ldgClaimsView(r, profile, q)
	schedules, note := a.ldgSchedules()
	wg.Wait()
	layout := a.layout(r, "ledger", "Ledger")
	a.page(w, r, "ledger-page", ldgPageData{Layout: layout, Claims: claims, Schedules: schedules, ScheduleNote: note, Vault: vault, Distribution: distribution})
}

// ldgClaimsFragment renders only the claims section for the network and provider switches.
func (a *App) ldgClaimsFragment(w http.ResponseWriter, r *http.Request) {
	profile := requestProfile(r)
	q, message := ldgParse(r.URL.Query(), len(a.selection(profile).Providers))
	if message != "" {
		a.fail(w, r, http.StatusBadRequest, message)
		return
	}
	a.fragment(w, r, "ledger-claims", a.ldgClaimsView(r, profile, q))
}

// ldgClaimPages is the set of claims one request reads from a provider.
type ldgClaimPages struct {
	Claims    []wire.SettlementClaim
	Truncated bool
	Read      int
	Newer     *uint64
}

// ldgSorted returns claims in ascending sequence order.
func ldgSorted(claims []wire.SettlementClaim) []wire.SettlementClaim {
	sorted := slices.Clone(claims)
	slices.SortFunc(sorted, func(a, b wire.SettlementClaim) int {
		switch {
		case a.Sequence < b.Sequence:
			return -1
		case a.Sequence > b.Sequence:
			return 1
		}
		return 0
	})
	return sorted
}

// ldgReadClaims reads claims from one provider: a single page after a position, or a bounded forward scan keeping the newest.
func (a *App) ldgReadClaims(r *http.Request, provider Endpoint, q ldgQuery) (ldgClaimPages, error) {
	client, err := a.providerClient(provider)
	if err != nil {
		return ldgClaimPages{}, err
	}
	ctx, cancel := context.WithTimeout(r.Context(), ldgReadTimeout)
	defer cancel()
	fetch := func(after uint64) ([]wire.SettlementClaim, error) {
		page, _, err := client.SettlementClaims(ctx, q.Network, after)
		return ldgSorted(page.Claims), err
	}
	var result ldgClaimPages
	if q.HasAfter {
		claims, err := fetch(q.After)
		if err != nil {
			return result, err
		}
		if len(claims) > ldgPageSize {
			claims = claims[:ldgPageSize]
			newer := uint64(claims[len(claims)-1].Sequence)
			result.Newer = &newer
		}
		result.Claims = claims
		return result, nil
	}
	var collected []wire.SettlementClaim
	var after uint64
	for pass := range ldgScanPages {
		claims, err := fetch(after)
		if err != nil {
			if pass == 0 {
				return result, err
			}
			result.Truncated = true
			break
		}
		collected = append(collected, claims...)
		if len(claims) == 0 {
			break
		}
		after = uint64(claims[len(claims)-1].Sequence)
		if len(claims) < ldgPageLimit {
			break
		}
		if pass+1 == ldgScanPages {
			result.Truncated = true
			break
		}
	}
	result.Read = len(collected)
	if len(collected) > ldgPageSize {
		collected = collected[len(collected)-ldgPageSize:]
	}
	result.Claims = collected
	if result.Truncated {
		newer := after
		result.Newer = &newer
	}
	return result, nil
}

// ldgReadMonitors asks every configured monitor for its witnesses, refusals and holds about one provider and network.
func (a *App) ldgReadMonitors(r *http.Request, providerKey, network string, after uint64) []ldgMonitorPage {
	pages := make([]ldgMonitorPage, len(a.config.Monitors))
	var wg sync.WaitGroup
	for i, monitor := range a.config.Monitors {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pages[i].Endpoint = monitor
			client, err := a.clientFor(monitor)
			if err != nil {
				pages[i].Err = err
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), ldgReadTimeout)
			defer cancel()
			page, _, err := client.Witnesses(ctx, providerKey, network, after)
			if err != nil {
				pages[i].Err = err
				return
			}
			for _, witness := range page.Witnesses {
				if witness.WitnessID == monitor.ServiceID && witness.ProviderID == providerKey && witness.Network == network && settlement.VerifyWitness(witness) == nil {
					pages[i].Witnesses = append(pages[i].Witnesses, witness)
				}
			}
			for _, refusal := range page.Refusals {
				if refusal.WitnessID == monitor.ServiceID && refusal.ProviderID == providerKey && settlement.VerifyRefusal(refusal) == nil {
					pages[i].Refusals = append(pages[i].Refusals, refusal)
				}
			}
			for _, hold := range page.Holds {
				if hold.WitnessID == monitor.ServiceID && hold.ProviderID == providerKey && settlement.VerifyHold(hold) == nil {
					pages[i].Holds = append(pages[i].Holds, hold)
				}
			}
		}()
	}
	wg.Wait()
	return pages
}

// ldgClaimsView builds the claims section for a query.
func (a *App) ldgClaimsView(r *http.Request, profile string, q ldgQuery) ldgClaimsView {
	view := ldgClaimsView{Network: q.Network, NetworkName: ldgNetworkName(q.Network), Dev: a.config.Dev}
	for _, network := range ldgNetworkOrder {
		view.Networks = append(view.Networks, ldgOption{Value: network, Label: ldgNetworkName(network), Selected: network == q.Network})
	}
	providers := a.selection(profile).Providers
	if len(providers) == 0 {
		view.NoProvider = true
		return view
	}
	provider := providers[q.Provider]
	view.MultiProvider = len(providers) > 1
	view.ProviderIndex = strconv.Itoa(q.Provider)
	view.ProviderLabel = ldgNamed(provider.Label, "a provider")
	for i, choice := range providers {
		view.Providers = append(view.Providers, ldgOption{Value: strconv.Itoa(i), Label: ldgNamed(choice.Label, "a provider"), Selected: i == q.Provider})
	}
	here := ldgQueryString(q.Network, q.Provider, q.After, q.HasAfter)
	view.Retry = "/ledger?" + here + "#claims"
	endpoint := Endpoint{URL: provider.Endpoint, ServiceID: provider.OperatorKey, Label: provider.Label}
	pages, err := a.ldgReadClaims(r, endpoint, q)
	if err != nil {
		view.Failed = ldgFailure(view.ProviderLabel, err)
		return view
	}
	if len(pages.Claims) == 0 {
		view.Empty = view.ProviderLabel + " has published no settlement claims on " + view.NetworkName + " yet. Claims appear within 24 hours after a settlement period ends."
		view.Status = "No claims to show."
		return view
	}
	lowest := pages.Claims[0].Sequence
	if lowest > 1 {
		older := uint64(0)
		if lowest > ldgPageSize+1 {
			older = uint64(lowest) - ldgPageSize - 1
		}
		view.Older = "/ledger?" + ldgQueryString(q.Network, q.Provider, older, true) + "#claims"
		view.OlderGet = "/ledger/claims?" + ldgQueryString(q.Network, q.Provider, older, true)
	}
	if pages.Newer != nil {
		view.Newer = "/ledger?" + ldgQueryString(q.Network, q.Provider, *pages.Newer, true) + "#claims"
		view.NewerGet = "/ledger/claims?" + ldgQueryString(q.Network, q.Provider, *pages.Newer, true)
	}
	if pages.Truncated {
		view.Notice = fmt.Sprintf("Read %d claims before stopping; newer claims may exist.", pages.Read)
	}
	view.Status = fmt.Sprintf("Showing %d claims from %s on %s.", len(pages.Claims), view.ProviderLabel, view.NetworkName)

	var after uint64
	if lowest > 0 {
		after = uint64(lowest) - 1
	}
	monitors := a.ldgReadMonitors(r, provider.OperatorKey, q.Network, after)
	root, _ := a.trustRoot()
	now := a.now()
	var committee sdk.CommitteeView
	if root != nil {
		committee = root.Committee(now)
	}
	view.Committee = ldgCommitteeOf(committee, len(committee.Current) > 0)
	if root == nil || len(committee.Current) == 0 {
		view.Committee.Warn = "The committee is unknown until the trust root is verified, so quorum is not shown."
	}
	for _, claim := range pages.Claims {
		view.Claims = append(view.Claims, a.ldgClaimRow(profile, provider, claim, monitors, committee, view.Committee.Known))
	}
	slices.Reverse(view.Claims)
	view.Sources = fmt.Sprintf("Claims come from %s's own claim pages. Witnesses come from the pages of the %d monitors this client is configured to ask. This is what those pages said just now, not a network total.", view.ProviderLabel, len(a.config.Monitors))
	return view
}

func ldgCommitteeOf(view sdk.CommitteeView, known bool) ldgCommittee {
	c := ldgCommittee{Known: known, Count: len(view.Current), Quorum: settlement.Quorum(len(view.Current)), Policy: shortDID(view.PolicyID)}
	if view.PolicyID == "" {
		c.Policy = ""
	}
	if known && !view.ChangedAt.IsZero() {
		c.Handover = true
		c.PreviousCount = len(view.Previous)
		c.ChangedAt = ldgTimeOf(view.ChangedAt).Text
		c.Until = ldgTimeOf(view.ChangedAt.Add(30 * 24 * time.Hour)).Text
	}
	return c
}

// ldgWitnessFor finds a verified witness from one member's own page that attests to the claim.
func ldgWitnessFor(pages []ldgMonitorPage, member string, claim wire.SettlementClaim, before *time.Time) (wire.SettlementWitness, bool) {
	for _, page := range pages {
		if page.Endpoint.ServiceID != member {
			continue
		}
		for _, witness := range page.Witnesses {
			if witness.WitnessID != member || !settlement.WitnessMatches(witness, claim) {
				continue
			}
			at, err := wire.ParseTime(witness.WitnessedAt)
			if err != nil || before != nil && !at.Before(*before) {
				continue
			}
			return witness, true
		}
	}
	return wire.SettlementWitness{}, false
}

// ldgClaimRow builds one claim: its facts, its signer, its witnesses and whether they reach quorum.
func (a *App) ldgClaimRow(profile string, provider ProviderChoice, claim wire.SettlementClaim, pages []ldgMonitorPage, committee sdk.CommitteeView, known bool) ldgClaimRow {
	start, _ := wire.ParseTime(claim.PeriodStart)
	end, _ := wire.ParseTime(claim.PeriodEnd)
	row := ldgClaimRow{
		Sequence:      claim.Sequence,
		Heading:       fmt.Sprintf("Claim %d · %s", claim.Sequence, ldgNetworkName(claim.Network)),
		Period:        ldgPeriod(start, end),
		StartISO:      start.Format(time.RFC3339),
		Receipts:      fmt.Sprintf("%d receipts", claim.ReceiptCount),
		Total:         ldgAmount(claim.Network, claim.TotalBaseUnits),
		Running:       ldgAmount(claim.Network, claim.CumulativeBaseUnits),
		ClaimID:       shortDID(claim.ClaimID),
		ClaimFull:     claim.ClaimID,
		Retention:     shortDID(claim.DeclarationID),
		RetentionFull: claim.DeclarationID,
	}
	if claim.ReceiptCount == 1 {
		row.Receipts = "1 receipt"
	}
	if claim.PreviousClaimID != nil {
		row.Previous, row.PreviousFull = shortDID(*claim.PreviousClaimID), *claim.PreviousClaimID
	}
	by := a.ldgWho(profile, provider.OperatorKey)
	if err := settlement.VerifyClaimObject(claim); err != nil {
		row.Signer = ldgFailed(by, claim.ProviderID, errors.New("the claim ID or provider signature does not match"))
	} else {
		row.Signer = ldgVerified(by, claim.ProviderID)
	}
	row.WrongKey = claim.ProviderID != provider.OperatorKey

	var witnesses []wire.SettlementWitness
	for _, page := range pages {
		witnesses = append(witnesses, page.Witnesses...)
	}
	var (
		current   = map[string]*big.Int{}
		previous  map[string]*big.Int
		changedAt *time.Time
	)
	row.Known = known
	if known {
		current = settlement.CountWitnesses(claim, witnesses, committee.Current, nil)
		counted, members := current, committee.Current
		reached := settlement.Settled(current, committee.Current)
		if len(committee.Previous) > 0 && !committee.ChangedAt.IsZero() {
			changed := committee.ChangedAt
			changedAt = &changed
			previous = settlement.CountWitnesses(claim, witnesses, committee.Previous, changedAt)
			if !reached && settlement.Settled(previous, committee.Previous) {
				reached = true
				counted, members = previous, committee.Previous
			}
		}
		row.Quorum = reached
		row.Counted = len(counted)
		row.Members = len(members)
		row.Handover = changedAt != nil
	}
	row.Rows, row.PreviousRows, row.Outside = a.ldgMonitorRows(profile, claim, provider, pages, committee, known, current, previous, changedAt)
	return row
}

// ldgMonitorRows explains each committee member's state for one claim, without adding the current and previous committee together.
func (a *App) ldgMonitorRows(profile string, claim wire.SettlementClaim, provider ProviderChoice, pages []ldgMonitorPage, committee sdk.CommitteeView, known bool, current, previous map[string]*big.Int, changedAt *time.Time) (rows, previousRows, outside []ldgMonitorRow) {
	if !known {
		for _, page := range pages {
			rows = append(rows, a.ldgMonitorRow(profile, page.Endpoint.ServiceID, claim, provider, pages, nil, nil, true))
		}
		return rows, nil, nil
	}
	inForce := map[string]bool{}
	for _, member := range committee.Current {
		inForce[member] = true
	}
	for _, member := range committee.Previous {
		inForce[member] = true
	}
	for _, member := range committee.Current {
		rows = append(rows, a.ldgMonitorRow(profile, member, claim, provider, pages, current, nil, false))
	}
	if changedAt != nil {
		for _, member := range committee.Previous {
			previousRows = append(previousRows, a.ldgMonitorRow(profile, member, claim, provider, pages, previous, changedAt, false))
		}
	}
	for _, page := range pages {
		if inForce[page.Endpoint.ServiceID] {
			continue
		}
		row := a.ldgMonitorRow(profile, page.Endpoint.ServiceID, claim, provider, pages, nil, nil, false)
		row.Text = "Not in the committee in force; not counted"
		outside = append(outside, row)
	}
	return rows, previousRows, outside
}

// ldgMonitorRow states one monitor's answer about one claim, from its own verified pages.
func (a *App) ldgMonitorRow(profile, member string, claim wire.SettlementClaim, provider ProviderChoice, pages []ldgMonitorPage, counted map[string]*big.Int, before *time.Time, committeeUnknown bool) ldgMonitorRow {
	row := ldgMonitorRow{Key: member, Short: shortDID(member), Label: a.ldgWho(profile, member)}
	var page *ldgMonitorPage
	for i := range pages {
		if pages[i].Endpoint.ServiceID == member {
			page = &pages[i]
		}
	}
	if page == nil {
		row.Text = "No endpoint for this monitor in this client, not asked"
		return row
	}
	if _, ok := counted[member]; ok || committeeUnknown {
		if witness, found := ldgWitnessFor(pages, member, claim, before); found {
			at, _ := wire.ParseTime(witness.WitnessedAt)
			row.Witnessed = true
			row.When = ldgTimeOf(at)
			row.Signer = ldgVerified(row.Label, member)
			return row
		}
	}
	for _, refusal := range page.Refusals {
		if refusal.ClaimID == claim.ClaimID {
			row.Text = "Refused: " + refusal.Reason
			return row
		}
	}
	for _, hold := range page.Holds {
		if hold.Sequence == claim.Sequence && hold.ProviderID == provider.OperatorKey {
			row.Text = "Holds this sequence"
			return row
		}
	}
	if page.Err != nil {
		row.Text = "Did not answer (" + ldgReason(page.Err) + ")"
		return row
	}
	row.Text = "No witness on its page yet"
	return row
}

// ldgSchedules describes the fee schedule in force for each network, from the verified trust root only.
func (a *App) ldgSchedules() ([]ldgSchedule, string) {
	root, status := a.trustRoot()
	if root == nil {
		return nil, "Fee schedules are not shown until the trust root is verified. " + status.Err
	}
	summary := root.Summary()
	by := fmt.Sprintf("the release keys (%d of %d)", summary.Threshold, len(summary.ReleaseKeyIDs))
	var schedules []ldgSchedule
	now := a.now()
	for _, network := range ldgNetworkOrder {
		schedule, ok := root.ScheduleInForce(network, now)
		if !ok {
			schedules = append(schedules, ldgSchedule{Network: ldgNetworkName(network), Missing: "No fee schedule is in force for this network."})
			continue
		}
		base, _ := wire.Amount(schedule.BaseFeeBaseUnits)
		rate, _ := wire.Amount(schedule.PerKiBFeeBaseUnits)
		sum := new(big.Int).Add(base, rate)
		schedules = append(schedules, ldgSchedule{
			Network:   ldgNetworkName(network),
			Revision:  strconv.FormatInt(schedule.Revision, 10),
			PerKiB:    ldgAmount(network, schedule.PerKiBFeeBaseUnits),
			Base:      ldgAmount(network, schedule.BaseFeeBaseUnits),
			OneKiB:    ldgAmount(network, sum.String()),
			Effective: ldgTimeText(schedule.EffectiveAt),
			Expires:   ldgTimeText(schedule.ExpiresAt),
			ID:        shortDID(schedule.ScheduleID),
			IDFull:    schedule.ScheduleID,
			Signer:    ldgVerified(by, ""),
			Checked:   "Checked when the trust root was built, " + relativeTime(status.FetchedAt, now) + ".",
		})
	}
	return schedules, ""
}

// ldgVaultSection reads the latest Lifetime Vault status and verifies it.
func (a *App) ldgVaultSection(r *http.Request, profile string) ldgVault {
	client, err := a.foundationClient()
	if err != nil {
		return ldgVault{Missing: true}
	}
	ctx, cancel := context.WithTimeout(r.Context(), ldgReadTimeout)
	defer cancel()
	page, _, err := client.VaultStatus(ctx)
	if err != nil {
		return ldgVault{Failure: ldgFailure(a.ldgFoundationLabel(), err)}
	}
	var latest *wire.VaultStatus
	var latestEnd time.Time
	for i := range page.Statuses {
		end, err := wire.ParseTime(page.Statuses[i].PeriodEnd)
		if err == nil && (latest == nil || end.After(latestEnd)) {
			latest, latestEnd = &page.Statuses[i], end
		}
	}
	if latest == nil {
		return ldgVault{Empty: true}
	}
	start, _ := wire.ParseTime(latest.PeriodStart)
	vault := ldgVault{
		Period:  ldgPeriod(start, latestEnd),
		Records: groupDigits(strconv.FormatInt(latest.RecordCount, 10)),
		Bytes:   groupDigits(strconv.FormatInt(latest.StoredBytes, 10)),
		Note:    "The vault keeps settled posts with no scheduled expiry. It is an obligation of the Foundation, not a guarantee.",
	}
	for _, funding := range latest.Funding {
		vault.Funding = append(vault.Funding, ldgOption{Value: funding.Network, Label: fmt.Sprintf("%s: Received %s, spent %s.", ldgNetworkName(funding.Network), ldgAmount(funding.Network, funding.ReceivedBaseUnits), ldgAmount(funding.Network, funding.SpentBaseUnits))})
	}
	by := a.ldgFoundationLabel()
	root, _ := a.trustRoot()
	switch {
	case root == nil:
		vault.Signer = ldgUnchecked(by, latest.FoundationID, "the trust root is unavailable")
	default:
		if err := root.VerifyFoundationObject(*latest, latestEnd, "vault-status", nil); err != nil {
			vault.Signer = ldgFailed(by, latest.FoundationID, err)
		} else {
			vault.Signer = ldgVerified(by, latest.FoundationID)
		}
	}
	return vault
}

// ldgDistributionSection reads the latest distribution statement for each network and verifies it.
func (a *App) ldgDistributionSection(r *http.Request, profile string) ldgDistribution {
	client, err := a.foundationClient()
	if err != nil {
		return ldgDistribution{Missing: true}
	}
	ctx, cancel := context.WithTimeout(r.Context(), ldgReadTimeout)
	defer cancel()
	page, _, err := client.Distribution(ctx)
	if err != nil {
		return ldgDistribution{Failure: ldgFailure(a.ldgFoundationLabel(), err)}
	}
	var result ldgDistribution
	result.Disclosures, result.Tied = a.ldgDisclosures(profile, page.Disclosures)
	if len(page.Statements) == 0 {
		result.Empty = true
		return result
	}
	root, _ := a.trustRoot()
	for _, network := range ldgNetworkOrder {
		var latest *wire.DistributionStatement
		var latestEnd time.Time
		for i := range page.Statements {
			statement := &page.Statements[i]
			end, err := wire.ParseTime(statement.PeriodEnd)
			if statement.Network == network && err == nil && (latest == nil || end.After(latestEnd)) {
				latest, latestEnd = statement, end
			}
		}
		if latest != nil {
			result.Statements = append(result.Statements, a.ldgStatementView(profile, *latest, latestEnd, root))
		}
	}
	return result
}

func (a *App) ldgStatementView(profile string, s wire.DistributionStatement, end time.Time, root *sdk.TrustRoot) ldgStatement {
	start, _ := wire.ParseTime(s.PeriodStart)
	view := ldgStatement{
		Heading:     ldgNetworkName(s.Network) + ", " + ldgPeriod(start, end),
		Received:    ldgAmount(s.Network, s.ReceivedBaseUnits),
		Distributed: ldgAmount(s.Network, s.DistributedBaseUnits),
		Policy:      shortDID(s.PolicyID),
	}
	by := a.ldgFoundationLabel()
	if err := distribution.CheckStatementID(s); err != nil {
		view.Signer = ldgFailed(by, s.FoundationID, err)
	} else if root == nil {
		view.Signer = ldgUnchecked(by, s.FoundationID, "the trust root is unavailable")
	} else if err := root.VerifyFoundationObject(s, end, "distribution-statement", nil); err != nil {
		view.Signer = ldgFailed(by, s.FoundationID, err)
	} else {
		view.Signer = ldgVerified(by, s.FoundationID)
	}
	for _, entry := range s.Entries {
		row := ldgEntry{
			Settled:   ldgAmount(s.Network, entry.SettledFeesBaseUnits),
			Quality:   ldgQualityText(entry.QualityBP),
			Retention: fmt.Sprintf("%d years", entry.RetentionYears),
			Amount:    ldgAmount(s.Network, entry.AmountBaseUnits),
			Owed:      ldgAmount(s.Network, entry.AccruedBaseUnits),
			Key:       entry.ProviderID,
			Short:     shortDID(entry.ProviderID),
			Known:     a.ldgKnown(profile, entry.ProviderID),
		}
		if row.Known {
			row.Provider = a.ldgWho(profile, entry.ProviderID)
		}
		if entry.Payout != nil {
			row.Paid, row.Payout = true, "Paid"
			row.Transfer = ldgTransferID(entry.Payout)
		} else {
			row.Payout = "Not paid; carried forward"
		}
		view.Entries = append(view.Entries, row)
	}
	for _, payment := range s.MonitorPayments {
		row := ldgPayment{
			Key:    payment.MonitorID,
			Short:  shortDID(payment.MonitorID),
			Amount: ldgAmount(s.Network, payment.AmountBaseUnits),
			Known:  a.ldgKnown(profile, payment.MonitorID),
		}
		if row.Known {
			row.Monitor = a.ldgWho(profile, payment.MonitorID)
		}
		if payment.Payout != nil {
			row.Paid, row.Transfer = true, ldgTransferID(payment.Payout)
		}
		view.Monitors = append(view.Monitors, row)
	}
	return view
}

// ldgTransferID names a payout transfer by its transaction ID, or its hash when no ID is given.
func ldgTransferID(t *wire.DepositTransfer) string {
	if t.TxID != "" {
		return shortDID(t.TxID)
	}
	return shortDID(t.TxHash)
}
