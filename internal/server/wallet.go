package server

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/funding"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/wire"
)

const (
	walPollWindow  = 10 * time.Minute
	walBalanceWait = 5 * time.Second
	walFundingWait = 15 * time.Second

	walFragmentPath = "/fragment"
)

// walPayMu serialises transfer recording so one process never sends two faux transfers for one invoice.
var walPayMu sync.Mutex

var walNetworks = []string{wire.BitcoinMainnet, wire.EthereumMainnet}

var walLabels = map[string]string{wire.BitcoinMainnet: "Bitcoin", wire.EthereumMainnet: "Ethereum"}

var walErrNotFound = errors.New("wallet: not found")

var walErrState = errors.New("wallet: state")

// walFunding is the provider account surface the wallet calls; *sdk.Client satisfies it.
type walFunding interface {
	OperatorManifest(ctx context.Context) (wire.OperatorManifest, []byte, error)
	Balance(ctx context.Context, key ed25519.PrivateKey, network string) (wire.BalanceStatus, error)
	RequestDeposit(ctx context.Context, key ed25519.PrivateKey, p sdk.DepositPlan, log sdk.FundingLog) (wire.DepositInvoice, wire.DepositRequest, error)
	ObserveDeposit(ctx context.Context, key ed25519.PrivateKey, inv wire.DepositInvoice, t wire.DepositTransfer, log sdk.FundingLog) (sdk.ObserveResult, error)
	Withdraw(ctx context.Context, key ed25519.PrivateKey, network, amountBaseUnits, recipient, requestID string) (wire.WithdrawalStatus, error)
}

// walFundingFor opens the provider surface for a profile's funding calls; tests replace it with a fake.
var walFundingFor = func(a *App, endpoint Endpoint) (walFunding, error) {
	client, err := a.providerClient(endpoint)
	if err != nil {
		return nil, err
	}
	return client, nil
}

func init() { routeGroups = append(routeGroups, registerWallet) }

func registerWallet(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /wallet", a.walletHandler)
	mux.HandleFunc("POST /wallet/acknowledge", a.walAcknowledgeHandler)
	mux.HandleFunc("POST /wallet/acknowledge"+walFragmentPath, a.walAcknowledgeHandler)
	mux.HandleFunc("POST /wallet/deposits", a.walDepositHandler)
	mux.HandleFunc("POST /wallet/deposits"+walFragmentPath, a.walDepositHandler)
	mux.HandleFunc("POST /wallet/deposits/{id}/pay", a.walPayHandler)
	mux.HandleFunc("POST /wallet/deposits/{id}/pay"+walFragmentPath, a.walPayHandler)
	mux.HandleFunc("POST /wallet/deposits/{id}/transfer", a.walTransferHandler)
	mux.HandleFunc("POST /wallet/deposits/{id}/transfer"+walFragmentPath, a.walTransferHandler)
	mux.HandleFunc("POST /wallet/deposits/{id}/observe", a.walObserveHandler)
	mux.HandleFunc("POST /wallet/deposits/{id}/observe"+walFragmentPath, a.walObserveHandler)
	mux.HandleFunc("POST /wallet/withdrawals", a.walWithdrawHandler)
	mux.HandleFunc("POST /wallet/withdrawals"+walFragmentPath, a.walWithdrawHandler)
}

type walRow struct{ Name, Text string }

type walNetwork struct{ Value, Label, Cap string }

type walBalance struct {
	Label, Available, Reserved, Frozen, Stated, Err string
}

type walAttempt struct{ DepositID, Amount string }

type walObserve struct {
	Show             bool
	DepositID, State string
	Poll             bool
	PollURL          string
	Credit, CreditID string
	Message          string
}

type walInvoice struct {
	DepositID, Network, NetworkLabel, Amount, Recipient, Expires, Signer, Alert string
	Paid, CanPay, CanTransfer, Expired                                          bool
	Observe                                                                     walObserve
}

type walRefusal struct {
	Status         int
	Alert, Message string
}

type walWithdrawal struct {
	Pill, Label, Amount, Recipient, Transfer string
}

type walWithdraw struct {
	RequestID string
	Status    *walWithdrawal
}

type walErrors struct {
	Ack, Deposit, Withdraw string
	Invoice                map[string]string
}

type walView struct {
	Layout       Layout
	Dev          bool
	HasIdentity  bool
	HasProvider  bool
	Provider     ProviderChoice
	ProviderKey  string
	Acknowledged bool
	Disclosure   []walRow
	Networks     []walNetwork
	Balances     []walBalance
	Invoices     []walInvoice
	Interrupted  []walAttempt
	Withdraw     walWithdraw
	Notice       string
	Errors       walErrors
}

type walOptions struct {
	Balances  bool
	RequestID string
	Observed  map[string]walObserve
	Errors    walErrors
}

type walPayResponse struct {
	Invoice  walInvoice
	Balances *walView
}

type walObserveResponse struct {
	Observe  walObserve
	Balances *walView
}

// walDOMKey turns a deposit ID into an element ID that is also a valid CSS selector: deposit IDs carry a colon.
func walDOMKey(depositID string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '-'
	}, depositID)
}

// Key is the element ID suffix of the invoice card.
func (v walInvoice) Key() string { return walDOMKey(v.DepositID) }

// Key is the element ID suffix of the transfer status card.
func (v walObserve) Key() string { return walDOMKey(v.DepositID) }

func walFragment(r *http.Request) bool { return strings.HasSuffix(r.URL.Path, walFragmentPath) }

func walSupported(network string) bool {
	_, ok := walLabels[network]
	return ok
}

func walEndpoint(choice ProviderChoice) Endpoint {
	return Endpoint{URL: choice.Endpoint, ServiceID: choice.OperatorKey, Label: choice.Label}
}

func walLog(a *App, profile string) (sdk.FundingLog, error) {
	return sdk.FileFundingLog(filepath.Join(a.profiles.root, profile, "funding"))
}

// walKey returns the profile's existing author key without creating one, so the wallet never makes an identity.
func (a *App) walKey(profile string) (ed25519.PrivateKey, bool, error) {
	key, err := a.keystore().PrivateKey(profile)
	if errors.Is(err, sdk.ErrNoIdentity) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return key, true, nil
}

func walAcknowledged(settings ProfileSettings, operatorKey string) bool {
	_, custody := settings.Acknowledgements[ackCustodyPrefix+operatorKey]
	_, linkability := settings.Acknowledgements[ackLinkability]
	return custody && linkability
}

func walNewRequestID() string {
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return ""
	}
	return keys.EncodeB64URL(random[:])
}

func walSince(r *http.Request, now time.Time) time.Time {
	value, err := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	if err != nil || value <= 0 || value > now.Unix() {
		return now
	}
	return time.Unix(value, 0)
}

func walGroup(n *big.Int) string { return groupDigits(n.String()) }

// walFormat states an accounting amount in msat or wei, with the satoshi or ETH equivalent.
func walFormat(network string, n *big.Int) string {
	if network == wire.EthereumMainnet {
		unit := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
		whole := new(big.Int).Quo(n, unit)
		frac := new(big.Int).Quo(new(big.Int).Rem(n, unit), big.NewInt(1000000000000))
		return fmt.Sprintf("%s wei (%s.%06d ETH)", walGroup(n), walGroup(whole), frac.Int64())
	}
	if new(big.Int).Rem(n, big.NewInt(1000)).Sign() == 0 {
		return fmt.Sprintf("%s msat (%s sat)", walGroup(n), walGroup(new(big.Int).Quo(n, big.NewInt(1000))))
	}
	return walGroup(n) + " msat"
}

func walChainUnit(network string) string {
	if network == wire.EthereumMainnet {
		return "wei"
	}
	return "sat"
}

// walChainText states a chain amount in satoshis or wei.
func walChainText(network string, n *big.Int) string {
	return walGroup(n) + " " + walChainUnit(network)
}

func walWhen(value string) string {
	t, err := wire.ParseTime(value)
	if err != nil {
		return value
	}
	return t.In(time.Local).Format("2 Jan 2006, 15:04 MST")
}

func walAccounting(network, chain string) (*big.Int, error) {
	n, err := fee.ParseAmount(chain)
	if err != nil {
		return nil, err
	}
	return fee.AccountingUnits(network, n)
}

// walPendingPayments sums invoices on a network that are not yet credited and not definitively failed.
// The sdk checks the custody cap against these, so they must count here too.
func walPendingPayments(attempts []sdk.FundingAttempt, network string) (*big.Int, error) {
	total := new(big.Int)
	for _, at := range attempts {
		if at.Invoice == nil || at.Credited || at.Network != network || at.Failed && !at.TransferSent {
			continue
		}
		amount, err := walAccounting(network, at.Invoice.AmountChainUnits)
		if err != nil {
			return nil, err
		}
		total.Add(total, amount)
	}
	return total, nil
}

func walInterrupted(attempts []sdk.FundingAttempt) []walAttempt {
	var out []walAttempt
	for _, at := range attempts {
		if !sdk.NeedsReconciliation(at) || at.Invoice == nil {
			continue
		}
		out = append(out, walAttempt{DepositID: at.DepositID, Amount: walChainText(at.Network, walMustChain(at.AmountChainUnits))})
	}
	return out
}

// walMustChain reads a decimal amount from the funding log, or zero when it is not one.
func walMustChain(digits string) *big.Int {
	n, ok := new(big.Int).SetString(digits, 10)
	if !ok {
		return new(big.Int)
	}
	return n
}

func walWithdrawalFor(st wire.WithdrawalStatus) walWithdrawal {
	view := walWithdrawal{Recipient: st.Recipient}
	if amount, err := fee.ParseAmount(st.AmountBaseUnits); err == nil {
		view.Amount = walFormat(st.Network, amount)
	}
	switch st.Status {
	case "pending":
		view.Pill, view.Label = "pill-warn", "Pending"
	case "sent":
		view.Pill, view.Label = "pill-ok", "Sent"
	case "failed":
		view.Pill, view.Label = "pill-bad", "Failed"
	default:
		view.Pill, view.Label = "pill-muted", st.Status
	}
	if st.Transfer != nil {
		view.Transfer = st.Transfer.TxID + st.Transfer.TxHash
	}
	return view
}

func walNetworkOptions(a *App) []walNetwork {
	root, _ := a.trustRoot()
	out := make([]walNetwork, 0, len(walNetworks))
	for _, network := range walNetworks {
		item := walNetwork{Value: network, Label: walLabels[network]}
		if root != nil {
			if schedule, ok := root.ScheduleInForce(network, a.now()); ok {
				if limit, err := fee.CustodyCap(schedule); err == nil {
					item.Cap = walFormat(network, limit)
				}
			}
		}
		out = append(out, item)
	}
	return out
}

func walDisclosure(label, appeal string) []walRow {
	remedy := "Claims, data-rights requests and appeals go through the provider's claims channel, listed on the Network page."
	if appeal != "" {
		remedy = "Claims, data-rights requests and appeals go through the provider's claims channel. Appeal link as stated in its manifest: " + appeal + "."
	}
	return []walRow{
		{Name: "Custody", Text: label + " holds your balance. Neither the protocol nor the ForYou Foundation guarantees its solvency."},
		{Name: "Scope", Text: "The balance belongs to your author key, at this provider and on one network. It cannot move to another provider or author, and it is not a token."},
		{Name: "Fee", Text: "Each admitted post or reply is charged the protocol fee set by the specification. " + label + " does not keep it and cannot change it; it is owed in full to the ForYou Foundation."},
		{Name: "Crediting", Text: "A deposit is credited only after the network confirms it and " + label + " signs the credit. Until then it is not spendable."},
		{Name: "Costs outside the fee", Text: "Network transaction costs for deposits and withdrawals are separate from the protocol fee and are paid to the network."},
		{Name: "Loss", Text: "If " + label + " becomes insolvent, its custody fails or a legal restriction applies, an unspent balance may not be recoverable."},
		{Name: "Remedy", Text: remedy},
		{Name: "Linkability", Text: "Payments on a public chain can be correlated with " + label + " and, through its records, with your author DID."},
	}
}

func (a *App) walDisclosureRows(ctx context.Context, funding walFunding, choice ProviderChoice) []walRow {
	appeal := ""
	callCtx, cancel := context.WithTimeout(ctx, walBalanceWait)
	defer cancel()
	if manifest, _, err := funding.OperatorManifest(callCtx); err == nil {
		appeal = manifest.AppealURL
	}
	return walDisclosure(choice.Label, appeal)
}

func (a *App) walBalances(ctx context.Context, funding walFunding, key ed25519.PrivateKey) []walBalance {
	out := make([]walBalance, 0, len(walNetworks))
	for _, network := range walNetworks {
		row := walBalance{Label: walLabels[network]}
		callCtx, cancel := context.WithTimeout(ctx, walBalanceWait)
		status, err := funding.Balance(callCtx, key, network)
		cancel()
		if err != nil {
			row.Err = "Unavailable: the provider did not return a verified balance right now."
			out = append(out, row)
			continue
		}
		row.Available, row.Reserved, row.Frozen, err = walAmounts(network, status)
		if err != nil {
			row.Err = "Unavailable: the provider's balance statement could not be verified."
			out = append(out, row)
			continue
		}
		row.Stated = status.UpdatedAt
		out = append(out, row)
	}
	return out
}

func walAmounts(network string, status wire.BalanceStatus) (string, string, string, error) {
	var out [3]string
	for i, digits := range []string{status.Available, status.Reserved, status.Frozen} {
		n, err := fee.ParseAmount(digits)
		if err != nil {
			return "", "", "", err
		}
		out[i] = walFormat(network, n)
	}
	return out[0], out[1], out[2], nil
}

func (a *App) walInvoices(attempts []sdk.FundingAttempt, choice ProviderChoice, opts walOptions, now time.Time) []walInvoice {
	var out []walInvoice
	for _, at := range attempts {
		_, observed := opts.Observed[at.DepositID]
		if at.Invoice == nil || !observed && (at.Credited || at.Failed && !at.TransferSent) {
			continue
		}
		inv := *at.Invoice
		item := walInvoice{DepositID: inv.DepositID, Network: inv.Network, NetworkLabel: walLabels[inv.Network], Recipient: inv.Recipient, Expires: walWhen(inv.ExpiresAt), Signer: choice.Label, Paid: at.TransferSent, Alert: opts.Errors.Invoice[inv.DepositID]}
		if n, err := fee.ParseAmount(inv.AmountChainUnits); err == nil {
			item.Amount = walChainText(inv.Network, n)
		}
		expiry, err := wire.ParseTime(inv.ExpiresAt)
		item.Expired = !at.TransferSent && err == nil && !now.Before(expiry)
		item.CanPay = a.config.Dev && a.faux != nil && !at.TransferSent && !item.Expired
		item.CanTransfer = !a.config.Dev && !at.TransferSent && !item.Expired
		if at.TransferSent {
			if observed, ok := opts.Observed[inv.DepositID]; ok {
				item.Observe = observed
			} else {
				item.Observe = walPendingView(inv.DepositID, now, now)
			}
		}
		out = append(out, item)
	}
	return out
}

func walPendingView(id string, since, now time.Time) walObserve {
	view := walObserve{Show: true, DepositID: id, State: "pending"}
	if now.Sub(since) >= walPollWindow {
		view.State = "timeout"
		return view
	}
	view.Poll = true
	view.PollURL = "/wallet/deposits/" + url.PathEscape(id) + "/observe" + walFragmentPath + "?since=" + strconv.FormatInt(since.Unix(), 10)
	return view
}

// walView gathers what the wallet page and its fragments show, from verified sdk calls and the funding log.
func (a *App) walView(r *http.Request, profile string, opts walOptions) walView {
	requestID := opts.RequestID
	if requestID == "" {
		requestID = walNewRequestID()
	}
	view := walView{Layout: a.layout(r, "wallet", "Wallet"), Dev: a.config.Dev, Networks: walNetworkOptions(a), Withdraw: walWithdraw{RequestID: requestID}, Errors: opts.Errors}
	key, hasKey, err := a.walKey(profile)
	if err != nil || !hasKey {
		return view
	}
	view.HasIdentity = true
	choice, ok := a.publishingProvider(profile)
	if !ok {
		return view
	}
	view.HasProvider, view.Provider, view.ProviderKey = true, choice, shortDID(choice.OperatorKey)
	settings, _ := a.settings(profile)
	view.Acknowledged = walAcknowledged(settings, choice.OperatorKey)
	funding, err := walFundingFor(a, walEndpoint(choice))
	if err != nil {
		return view
	}
	if !view.Acknowledged {
		view.Disclosure = a.walDisclosureRows(r.Context(), funding, choice)
	}
	if opts.Balances {
		view.Balances = a.walBalances(r.Context(), funding, key)
	}
	log, err := walLog(a, profile)
	if err != nil {
		return view
	}
	attempts, err := log.Attempts()
	if err != nil {
		return view
	}
	view.Invoices = a.walInvoices(attempts, choice, opts, a.now())
	view.Interrupted = walInterrupted(attempts)
	return view
}

func (a *App) walletHandler(w http.ResponseWriter, r *http.Request) {
	profile, err := a.profile(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The wallet could not start a session.")
		return
	}
	a.page(w, r, "wallet-page", a.walView(r, profile, walOptions{Balances: true}))
}

// walFail answers a refused wallet action: the message in the form's alert for an HTMLeX fragment, the full page with the status otherwise.
func (a *App) walFail(w http.ResponseWriter, r *http.Request, profile string, status int, alert string, opts walOptions, message string) {
	if walFragment(r) {
		a.walRefuse(w, r, status, alert, message)
		return
	}
	a.renderStatus(w, r, "wallet-page", a.walView(r, profile, opts), status)
}

// walRefuse answers an HTMLeX request with 200 and a fragment that carries the refusal status and writes the message into the alert. HTMLeX drops the body of a non-2xx response, so the status travels in the fragment instead.
func (a *App) walRefuse(w http.ResponseWriter, r *http.Request, status int, alert, message string) {
	a.renderOutput(w, "wallet-refusal", walRefusal{Status: status, Alert: alert, Message: message}, http.StatusOK, "no-store")
}

func (a *App) walAcknowledgeHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := readActionForm(w, r); !ok {
		return
	}
	profile, err := a.profile(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The wallet could not start a session.")
		return
	}
	choice, ok := a.publishingProvider(profile)
	if !ok {
		a.fail(w, r, http.StatusConflict, "Choose a provider on the network page first.")
		return
	}
	if r.PostForm.Get("custody") != "on" || r.PostForm.Get("linkability") != "on" {
		msg := "Tick both boxes to continue."
		a.walFail(w, r, profile, http.StatusBadRequest, "wallet-ack-alert", walOptions{Errors: walErrors{Ack: msg}}, msg)
		return
	}
	now := a.now()
	if err := a.updateSettings(profile, func(s *ProfileSettings) error {
		if s.Acknowledgements == nil {
			s.Acknowledgements = make(map[string]time.Time)
		}
		s.Acknowledgements[ackCustodyPrefix+choice.OperatorKey] = now
		s.Acknowledgements[ackLinkability] = now
		return nil
	}); err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The acknowledgement could not be saved.")
		return
	}
	if walFragment(r) {
		a.walRenderFunding(w, r, profile, walOptions{})
		return
	}
	http.Redirect(w, r, "/wallet#wallet-funding", http.StatusSeeOther)
}

func (a *App) walRenderFunding(w http.ResponseWriter, r *http.Request, profile string, opts walOptions) {
	view := a.walView(r, profile, opts)
	a.fragment(w, r, "wallet-funding", view)
}

func (a *App) walDepositHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := readActionForm(w, r, "network", "amount")
	if !ok {
		return
	}
	profile, err := a.profile(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The wallet could not start a session.")
		return
	}
	fail := func(status int, message string) {
		a.walFail(w, r, profile, status, "wallet-deposit-alert", walOptions{Errors: walErrors{Deposit: message}}, message)
	}
	choice, ok := a.publishingProvider(profile)
	if !ok {
		fail(http.StatusConflict, "Choose a provider on the network page first.")
		return
	}
	if !walAcknowledged(a.walSettings(profile), choice.OperatorKey) {
		msg := "Read the funding disclosure and confirm both points first."
		a.walFail(w, r, profile, http.StatusBadRequest, "wallet-deposit-alert", walOptions{Errors: walErrors{Ack: msg}}, msg)
		return
	}
	key, hasKey, err := a.walKey(profile)
	if err != nil || !hasKey {
		fail(http.StatusConflict, "Create an identity before you fund a balance.")
		return
	}
	network := form.Get("network")
	if !walSupported(network) {
		fail(http.StatusBadRequest, "Choose Bitcoin or Ethereum.")
		return
	}
	amount := form.Get("amount")
	if _, err := fee.ParsePositiveAmount(amount); err != nil {
		fail(http.StatusBadRequest, "Enter a positive whole number of "+walChainUnit(network)+".")
		return
	}
	root, _ := a.trustRoot()
	if root == nil {
		fail(http.StatusServiceUnavailable, "The fee schedule is not available yet. Try again later.")
		return
	}
	schedule, ok := root.ScheduleInForce(network, a.now())
	if !ok {
		fail(http.StatusServiceUnavailable, "No approved fee schedule covers this network yet.")
		return
	}
	funding, err := walFundingFor(a, walEndpoint(choice))
	if err != nil {
		fail(http.StatusServiceUnavailable, "The provider could not be reached. Try again later.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), walFundingWait)
	defer cancel()
	status, err := funding.Balance(ctx, key, network)
	if err != nil {
		fail(http.StatusServiceUnavailable, "The provider balance could not be read. Try again.")
		return
	}
	confirmed, err := walConfirmed(status)
	if err != nil {
		fail(http.StatusServiceUnavailable, "The provider balance statement could not be verified.")
		return
	}
	log, err := walLog(a, profile)
	if err != nil {
		fail(http.StatusInternalServerError, "The funding log could not be opened.")
		return
	}
	attempts, err := log.Attempts()
	if err != nil {
		fail(http.StatusInternalServerError, "The funding log could not be read.")
		return
	}
	pending, err := walPendingPayments(attempts, network)
	if err != nil {
		fail(http.StatusInternalServerError, "The funding log could not be read.")
		return
	}
	plan := sdk.DepositPlan{Network: network, AmountChainUnits: amount, DisclosureAcknowledged: true, Schedule: schedule, Confirmed: confirmed.String(), Pending: pending.String()}
	if _, _, err := funding.RequestDeposit(ctx, key, plan, log); err != nil {
		fail(walDepositStatus(err), walDepositMessage(err, schedule))
		return
	}
	if walFragment(r) {
		a.walRenderDeposit(w, r, profile, "Invoice requested. Pay it below; the balance is not spendable until the provider confirms it.")
		return
	}
	http.Redirect(w, r, "/wallet#wallet-invoices", http.StatusSeeOther)
}

func (a *App) walSettings(profile string) ProfileSettings {
	settings, err := a.settings(profile)
	if err != nil {
		return ProfileSettings{}
	}
	return settings
}

func (a *App) walRenderDeposit(w http.ResponseWriter, r *http.Request, profile, notice string) {
	view := a.walView(r, profile, walOptions{})
	view.Notice = notice
	a.fragment(w, r, "wallet-deposit-response", view)
}

func walConfirmed(status wire.BalanceStatus) (*big.Int, error) {
	total := new(big.Int)
	for _, digits := range []string{status.Available, status.Reserved, status.Frozen} {
		n, err := fee.ParseAmount(digits)
		if err != nil {
			return nil, err
		}
		total.Add(total, n)
	}
	return total, nil
}

func walDepositStatus(err error) int {
	switch {
	case errors.Is(err, sdk.ErrCustodyCap), errors.Is(err, sdk.ErrInvoiceRejected), errors.Is(err, sdk.ErrUnsupportedNetwork), errors.Is(err, sdk.ErrDisclosureRequired):
		return http.StatusBadRequest
	}
	return http.StatusBadGateway
}

func walDepositMessage(err error, schedule wire.FeeSchedule) string {
	switch {
	case errors.Is(err, sdk.ErrCustodyCap):
		limit := "the custody cap"
		if cap, capErr := fee.CustodyCap(schedule); capErr == nil {
			limit = "the custody cap of " + walFormat(schedule.Network, cap)
		}
		return "This deposit would take your balance above " + limit + "."
	case errors.Is(err, sdk.ErrInvoiceRejected):
		return "The provider's invoice was rejected and will not be paid: " + strings.TrimPrefix(err.Error(), "sdk: deposit invoice rejected: ")
	case errors.Is(err, sdk.ErrUnsupportedNetwork):
		return "Choose Bitcoin or Ethereum."
	case errors.Is(err, sdk.ErrDisclosureRequired):
		return "Read the funding disclosure and confirm both points first."
	}
	return "The deposit request could not be completed. Check the status before you try again."
}

func walAttemptFor(attempts []sdk.FundingAttempt, id string) (sdk.FundingAttempt, bool) {
	for _, at := range attempts {
		if at.DepositID == id {
			return at, true
		}
	}
	return sdk.FundingAttempt{}, false
}

// walCheck observes a transfer the log already holds, or reports the credit it already has.
func (a *App) walCheck(ctx context.Context, profile, id string, now time.Time, since time.Time) (walObserve, bool, error) {
	key, hasKey, err := a.walKey(profile)
	if err != nil || !hasKey {
		return walObserve{}, false, fmt.Errorf("%w: identity", walErrState)
	}
	log, err := walLog(a, profile)
	if err != nil {
		return walObserve{}, false, err
	}
	attempts, err := log.Attempts()
	if err != nil {
		return walObserve{}, false, err
	}
	at, ok := walAttemptFor(attempts, id)
	if !ok || at.Invoice == nil {
		return walObserve{}, false, walErrNotFound
	}
	if at.Credited {
		amount, convErr := walAccounting(at.Network, at.Invoice.AmountChainUnits)
		if convErr != nil {
			return walObserve{}, false, convErr
		}
		return walObserve{Show: true, DepositID: id, State: "credited", Credit: walFormat(at.Network, amount), CreditID: at.CreditID}, false, nil
	}
	if at.Transfer == nil {
		return walObserve{}, false, fmt.Errorf("%w: no transfer", walErrState)
	}
	if now.Sub(since) >= walPollWindow {
		return walPendingView(id, since, now), false, nil
	}
	choice, ok := a.publishingProvider(profile)
	if !ok {
		return walObserve{}, false, fmt.Errorf("%w: provider", walErrState)
	}
	funding, err := walFundingFor(a, walEndpoint(choice))
	if err != nil {
		return walObserve{}, false, err
	}
	return a.walObserveWith(ctx, funding, key, log, *at.Invoice, *at.Transfer, since, now)
}

func (a *App) walObserveWith(ctx context.Context, funding walFunding, key ed25519.PrivateKey, log sdk.FundingLog, inv wire.DepositInvoice, t wire.DepositTransfer, since, now time.Time) (walObserve, bool, error) {
	callCtx, cancel := context.WithTimeout(ctx, walFundingWait)
	defer cancel()
	res, err := funding.ObserveDeposit(callCtx, key, inv, t, log)
	if err != nil {
		return walObserve{Show: true, DepositID: inv.DepositID, State: "error", Message: "The provider did not confirm this check. Check again in a moment."}, false, nil
	}
	if res.Credit == nil {
		return walPendingView(inv.DepositID, since, now), false, nil
	}
	amount, err := fee.ParseAmount(res.Credit.AmountAccountingUnits)
	if err != nil {
		return walObserve{Show: true, DepositID: inv.DepositID, State: "error", Message: "The credit could not be read. Check again in a moment."}, false, nil
	}
	return walObserve{Show: true, DepositID: inv.DepositID, State: "credited", Credit: walFormat(inv.Network, amount), CreditID: res.Credit.CreditID}, true, nil
}

func walTransferFrom(t faux.Transfer) wire.DepositTransfer {
	out := wire.DepositTransfer{Network: t.Network, TxID: t.TxID, TxHash: t.TxHash}
	if t.OutputIndex != nil {
		index := int64(*t.OutputIndex)
		out.OutputIndex = &index
	}
	return out
}

// walRecordTransfer durably records a sent transfer before observing it, so an interrupted request is reconciled rather than repaid.
func (a *App) walRecordTransfer(ctx context.Context, profile, id string, now time.Time, transfer func(inv wire.DepositInvoice) (wire.DepositTransfer, error)) (walObserve, error) {
	walPayMu.Lock()
	defer walPayMu.Unlock()
	key, hasKey, err := a.walKey(profile)
	if err != nil || !hasKey {
		return walObserve{}, fmt.Errorf("%w: identity", walErrState)
	}
	log, err := walLog(a, profile)
	if err != nil {
		return walObserve{}, err
	}
	attempts, err := log.Attempts()
	if err != nil {
		return walObserve{}, err
	}
	at, ok := walAttemptFor(attempts, id)
	if !ok || at.Invoice == nil {
		return walObserve{}, walErrNotFound
	}
	if at.Credited || at.TransferSent {
		view, _, err := a.walCheck(ctx, profile, id, now, now)
		return view, err
	}
	inv := *at.Invoice
	if expiry, parseErr := wire.ParseTime(inv.ExpiresAt); parseErr == nil && !now.Before(expiry) {
		return walObserve{}, fmt.Errorf("%w: expired", walErrState)
	}
	t, err := transfer(inv)
	if err != nil {
		return walObserve{}, err
	}
	if err := log.Append(sdk.FundingEvent{Kind: sdk.FundingTransferSent, RequestID: at.RequestID, DepositID: inv.DepositID, Network: inv.Network, Transfer: &t, At: wire.FormatTime(now)}); err != nil {
		return walObserve{}, err
	}
	choice, ok := a.publishingProvider(profile)
	if !ok {
		return walObserve{}, fmt.Errorf("%w: provider", walErrState)
	}
	funding, err := walFundingFor(a, walEndpoint(choice))
	if err != nil {
		return walObserve{}, err
	}
	view, _, err := a.walObserveWith(ctx, funding, key, log, inv, t, now, now)
	return view, err
}

func (a *App) walPayHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := readActionForm(w, r); !ok {
		return
	}
	profile, err := a.profile(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The wallet could not start a session.")
		return
	}
	id := r.PathValue("id")
	if !a.config.Dev || a.faux == nil {
		a.walFail(w, r, profile, http.StatusConflict, "wallet-pay-alert-"+walDOMKey(id), walOptions{}, "Paying with the faux chain is available only in dev mode.")
		return
	}
	view, err := a.walRecordTransfer(r.Context(), profile, id, a.now(), func(inv wire.DepositInvoice) (wire.DepositTransfer, error) {
		ctx, cancel := context.WithTimeout(r.Context(), walFundingWait)
		defer cancel()
		sent, err := a.faux.Submit(ctx, faux.SubmitRequest{Network: inv.Network, Recipient: inv.Recipient, AmountChainUnits: inv.AmountChainUnits, ClientRef: "wallet:" + inv.DepositID})
		if err != nil {
			return wire.DepositTransfer{}, err
		}
		return walTransferFrom(sent), nil
	})
	if err != nil {
		a.walRespondError(w, r, profile, id, err)
		return
	}
	a.walRespondObserve(w, r, profile, id, view)
}

// walTransferHandler records a transfer the author paid from their own wallet; only dev mode is supported today.
func (a *App) walTransferHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := readActionForm(w, r, "transaction")
	if !ok {
		return
	}
	profile, err := a.profile(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The wallet could not start a session.")
		return
	}
	id := r.PathValue("id")
	transaction := strings.TrimSpace(form.Get("transaction"))
	index := strings.TrimSpace(form.Get("output_index"))
	if transaction == "" {
		a.walRespondError(w, r, profile, id, fmt.Errorf("%w: transaction", walErrState))
		return
	}
	view, err := a.walRecordTransfer(r.Context(), profile, id, a.now(), func(inv wire.DepositInvoice) (wire.DepositTransfer, error) {
		t := wire.DepositTransfer{Network: inv.Network}
		if inv.Network == wire.BitcoinMainnet {
			n, parseErr := strconv.ParseInt(index, 10, 64)
			if parseErr != nil || n < 0 {
				return wire.DepositTransfer{}, fmt.Errorf("%w: output index", walErrState)
			}
			t.TxID, t.OutputIndex = transaction, &n
		} else {
			t.TxHash = transaction
		}
		proof := wire.DepositProof{ProviderID: inv.ProviderID, AuthorID: inv.AuthorID, DepositID: inv.DepositID, Transfer: t}
		if err := funding.ValidateDepositProof(proof, inv.ProviderID, inv.AuthorID); err != nil {
			return wire.DepositTransfer{}, fmt.Errorf("%w: transfer", walErrState)
		}
		return t, nil
	})
	if err != nil {
		a.walRespondError(w, r, profile, id, err)
		return
	}
	a.walRespondObserve(w, r, profile, id, view)
}

// walRespondObserve answers a pay or transfer with the invoice card it targets, so the paid state replaces the pay control.
func (a *App) walRespondObserve(w http.ResponseWriter, r *http.Request, profile, id string, view walObserve) {
	if !walFragment(r) {
		http.Redirect(w, r, "/wallet#wallet-invoice-"+walDOMKey(id), http.StatusSeeOther)
		return
	}
	credited := view.State == "credited"
	page := a.walView(r, profile, walOptions{Balances: credited, Observed: map[string]walObserve{id: view}})
	for _, invoice := range page.Invoices {
		if invoice.DepositID == id {
			response := walPayResponse{Invoice: invoice}
			if credited {
				response.Balances = &page
			}
			a.fragment(w, r, "wallet-pay-response", response)
			return
		}
	}
	a.fragment(w, r, "wallet-observe", view)
}

// walRespondError maps a refused invoice action onto an alert under its card, or the page for a full-page request.
func (a *App) walRespondError(w http.ResponseWriter, r *http.Request, profile, id string, err error) {
	status, message := http.StatusBadGateway, "The payment could not be recorded. Check the status before you try again."
	switch {
	case errors.Is(err, walErrNotFound):
		status, message = http.StatusNotFound, "This invoice is not in your funding log."
	case errors.Is(err, walErrState):
		status, message = http.StatusConflict, "This invoice cannot take that action right now."
	}
	opts := walOptions{Errors: walErrors{Invoice: map[string]string{id: message}}}
	if walFragment(r) {
		a.walRefuse(w, r, status, "wallet-pay-alert-"+walDOMKey(id), message)
		return
	}
	a.renderStatus(w, r, "wallet-page", a.walView(r, profile, opts), status)
}

func (a *App) walObserveHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := readActionForm(w, r); !ok {
		return
	}
	profile, err := a.profile(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The wallet could not start a session.")
		return
	}
	id := r.PathValue("id")
	now := a.now()
	since := walSince(r, now)
	view, refresh, err := a.walCheck(r.Context(), profile, id, now, since)
	if err != nil {
		a.walRespondError(w, r, profile, id, err)
		return
	}
	if !walFragment(r) {
		http.Redirect(w, r, "/wallet#wallet-invoice-"+walDOMKey(id), http.StatusSeeOther)
		return
	}
	response := walObserveResponse{Observe: view}
	if refresh {
		balances := a.walView(r, profile, walOptions{Balances: true})
		response.Balances = &balances
	}
	a.fragment(w, r, "wallet-observe-response", response)
}

func (a *App) walWithdrawHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := readActionForm(w, r, "network", "amount", "recipient", "request_id")
	if !ok {
		return
	}
	profile, err := a.profile(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The wallet could not start a session.")
		return
	}
	requestID := form.Get("request_id")
	fail := func(status int, message string) {
		a.walFail(w, r, profile, status, "wallet-withdraw-alert", walOptions{RequestID: requestID, Errors: walErrors{Withdraw: message}}, message)
	}
	network, amount, recipient := form.Get("network"), strings.TrimSpace(form.Get("amount")), strings.TrimSpace(form.Get("recipient"))
	if !walSupported(network) {
		fail(http.StatusBadRequest, "Choose Bitcoin or Ethereum.")
		return
	}
	if _, err := fee.ParsePositiveAmount(amount); err != nil {
		fail(http.StatusBadRequest, "Enter a positive whole number of base units (msat or wei).")
		return
	}
	if err := faux.ValidateRecipient(network, recipient); err != nil {
		fail(http.StatusBadRequest, "The recipient address is not valid for this network.")
		return
	}
	key, hasKey, err := a.walKey(profile)
	if err != nil || !hasKey {
		fail(http.StatusConflict, "Create an identity before you withdraw.")
		return
	}
	choice, ok := a.publishingProvider(profile)
	if !ok {
		fail(http.StatusConflict, "Choose a provider on the network page first.")
		return
	}
	funding, err := walFundingFor(a, walEndpoint(choice))
	if err != nil {
		fail(http.StatusServiceUnavailable, "The provider could not be reached. Try again later.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), walFundingWait)
	defer cancel()
	status, err := funding.Withdraw(ctx, key, network, amount, recipient, requestID)
	if err != nil {
		code := http.StatusBadGateway
		message := "The withdrawal was not accepted. Submitting again sends the same request."
		var public *sdk.Error
		if errors.As(err, &public) && public.HTTPStatus == http.StatusPaymentRequired {
			code, message = http.StatusPaymentRequired, "Not enough available balance."
		}
		fail(code, message)
		return
	}
	if walFragment(r) {
		view := a.walView(r, profile, walOptions{})
		withdrawal := walWithdrawalFor(status)
		view.Withdraw.Status = &withdrawal
		a.fragment(w, r, "wallet-withdraw-response", view)
		return
	}
	http.Redirect(w, r, "/wallet#wallet-withdraw", http.StatusSeeOther)
}
