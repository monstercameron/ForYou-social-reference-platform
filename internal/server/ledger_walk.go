package server

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strconv"

	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

// ldgHead is the title and visible state of one walk step.
type ldgHead struct{ ID, Title, State, Class string }

// ldgWalkPage is the Follow the fee page.
type ldgWalkPage struct {
	Layout  Layout
	ID      string
	Missing bool
	Gone    bool
	Sources []string
	Invalid string
	Found   bool
	Fee     ldgFeeStep
	Receipt ldgReceiptStep
	Claim   ldgClaimStep
	Witness ldgWitnessStep
	Vault   ldgVaultStep
}

type ldgFeeStep struct {
	Head                                 ldgHead
	Summary, Working, QuoteID, QuoteFull string
	Matches                              bool
	Signer                               ldgSigner
}

type ldgReceiptStep struct {
	Head                                 ldgHead
	Provider, ProviderShort, ProviderKey string
	ReceiptID, ReceiptFull               string
	Amount, Network, Authorized, Issued  string
	Signer                               ldgSigner
}

type ldgClaimStep struct {
	Head      ldgHead
	Pending   string
	Asked     []string
	Facts     ldgClaimRow
	HasProof  bool
	ProofFrom string
	Leaf      string
	Chain     string
}

type ldgWitnessStep struct {
	Head     ldgHead
	Rows     []ldgMonitorRow
	Summary  string
	Settled  string
	Class    string
	Note     string
	HasProof bool
}

type ldgVaultStep struct {
	Head   ldgHead
	ID     string
	Result ldgVaultResult
	Shown  bool
}

// ldgVaultResult is the answer to one Lifetime Vault check.
type ldgVaultResult struct {
	Text      string
	Stored    bool
	StoredAt  ldgTime
	HasSigner bool
	Signer    ldgSigner
}

// ldgProofRead is the settlement proof a source supplied, and the sources that did not.
type ldgProofRead struct {
	Proof *wire.SettlementProof
	From  string
	Asked []string
}

// ldgFee renders Follow the fee for one post.
func (a *App) ldgFee(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	layout := a.layout(r, "ledger", "Follow the fee")
	if !canonicalMessageID(id) {
		a.renderStatus(w, r, "ledger-walk-page", ldgWalkPage{Layout: layout, ID: id, Missing: true}, http.StatusNotFound)
		return
	}
	page := a.ldgWalk(r, requestProfile(r), id)
	page.Layout = layout
	if r.URL.Query().Get("vault") == "1" && page.Found {
		page.Vault.Shown = true
		page.Vault.Result = a.ldgVaultResult(r, id)
		page.Vault.Head = ldgVaultHead(page.Vault.Result)
	}
	if !page.Gone {
		a.page(w, r, "ledger-walk-page", page)
		return
	}
	a.renderStatus(w, r, "ledger-walk-page", page, http.StatusNotFound)
}

// ldgFeeVault renders the Lifetime Vault check for one post as a fragment.
func (a *App) ldgFeeVault(w http.ResponseWriter, r *http.Request) {
	id := r.URL.Query().Get("id")
	if !canonicalMessageID(id) {
		a.renderStatus(w, r, "ledger-walk-vault", ldgVaultResult{Text: "Only network posts have a fee to follow. Demo posts are not paid."}, http.StatusNotFound)
		return
	}
	a.renderStatus(w, r, "ledger-walk-vault", a.ldgVaultResult(r, id), http.StatusOK)
}

// ldgWalk reads one post from the reader's sources and works out each step of its fee.
func (a *App) ldgWalk(r *http.Request, profile, id string) ldgWalkPage {
	page := ldgWalkPage{ID: id}
	var asked []string
	var fetched sdk.Fetched
	var kind sdk.SourceKind
	var found bool
	for _, choice := range a.selection(profile).Providers {
		endpoint := Endpoint{URL: choice.Endpoint, ServiceID: choice.OperatorKey, Label: choice.Label}
		label := ldgNamed(choice.Label, "a provider")
		message, err := a.ldgGetMessage(r, endpoint, id)
		if err != nil {
			asked = append(asked, ldgFailure(label, err))
			continue
		}
		fetched, kind, found = message, sdk.SourceProvider, true
		break
	}
	if !found {
		for _, mirror := range a.config.Mirrors {
			label := ldgNamed(mirror.Label, "a mirror")
			message, err := a.ldgGetMessage(r, mirror, id)
			if err != nil {
				asked = append(asked, ldgFailure(label, err))
				continue
			}
			fetched, kind, found = message, sdk.SourceMirror, true
			break
		}
	}
	if !found {
		page.Gone, page.Sources = true, asked
		return page
	}
	record := a.verifier(profile).Verify(fetched.Raw, id, sdk.Source{Endpoint: fetched.Endpoint, ServiceID: fetched.ServiceID, Kind: kind})
	if record.Class == sdk.ClassInvalid {
		page.Invalid = fmt.Sprintf("This record failed verification at step %d: %s. Its fee is not shown.", record.Failure.Step, record.Failure.Detail)
		return page
	}
	page.Found = true
	root, _ := a.trustRoot()
	issued, _ := wire.ParseTime(record.Receipt.IssuedAt)
	schedule, _ := root.ScheduleForReceipt(record.ScheduleID, record.Network, issued)
	by := fmt.Sprintf("the release keys (%d of %d)", root.Summary().Threshold, len(root.Summary().ReleaseKeyIDs))
	kib := (record.BillableBytes + 1023) / 1024
	base := ldgAmount(record.Network, schedule.BaseFeeBaseUnits)
	rate := ldgAmount(record.Network, schedule.PerKiBFeeBaseUnits)
	page.Fee = ldgFeeStep{
		Head:    ldgHead{ID: "step-fee", Title: "1. Fee", State: "Checked", Class: "ok"},
		Summary: fmt.Sprintf("%s for %s bytes (%d started KiB)", ldgAmount(record.Network, record.Fee), groupDigits(strconv.Itoa(record.BillableBytes)), kib),
		Working: fmt.Sprintf("Base %s + %s × %d = %s", base, rate, kib, ldgAmount(record.Network, record.Fee)),
		QuoteID: shortDID(record.Receipt.QuoteID), QuoteFull: record.Receipt.QuoteID,
		Matches: true,
		Signer:  ldgVerified(by, ""),
	}
	receiptChecked := ldgStepPassed(record, 4)
	providerBy := a.ldgWho(profile, record.Receipt.ProviderID)
	page.Receipt = ldgReceiptStep{
		Head:          ldgHead{ID: "step-receipt", Title: "2. Receipt", State: "Checked", Class: "ok"},
		Provider:      providerBy,
		ProviderShort: shortDID(record.Receipt.ProviderID),
		ProviderKey:   record.Receipt.ProviderID,
		ReceiptID:     shortDID(record.Receipt.ReceiptID),
		ReceiptFull:   record.Receipt.ReceiptID,
		Amount:        ldgAmount(record.Network, record.Receipt.AmountBaseUnits),
		Network:       ldgNetworkName(record.Network),
		Authorized:    ldgTimeText(record.Receipt.AuthorizationAt),
		Issued:        ldgTimeText(record.Receipt.IssuedAt),
	}
	if receiptChecked {
		page.Receipt.Signer = ldgVerified(providerBy, record.Receipt.ProviderID)
	} else {
		page.Receipt.Signer = ldgUnchecked(providerBy, record.Receipt.ProviderID, "step 4 did not pass")
		page.Receipt.Head = ldgHead{ID: "step-receipt", Title: "2. Receipt", State: "Problem", Class: "bad"}
	}

	read := a.ldgReadProof(r, profile, record.Receipt.ReceiptID, record.Receipt.ProviderID)
	var proof *wire.SettlementProof
	if read.Proof != nil {
		proof = read.Proof
	}
	rec := a.verifier(profile).Settle(record, proof, nil)
	page.Claim = a.ldgClaimStepView(profile, record, read, rec)
	page.Witness = a.ldgWitnessStepView(profile, read, rec, root)
	page.Vault = ldgVaultStep{Head: ldgHead{ID: "step-vault", Title: "5. Lifetime Vault", State: "Not yet", Class: "none"}, ID: id}
	return page
}

// ldgStepPassed reports whether one ordered verification step passed.
func ldgStepPassed(record sdk.Record, step int) bool {
	for _, check := range record.Checks {
		if check.Step == step {
			return check.OK
		}
	}
	return false
}

// ldgGetMessage fetches one message from one endpoint, bounded by its own deadline.
func (a *App) ldgGetMessage(r *http.Request, endpoint Endpoint, id string) (sdk.Fetched, error) {
	client, err := a.clientFor(endpoint)
	if err != nil {
		return sdk.Fetched{}, err
	}
	ctx, cancel := context.WithTimeout(r.Context(), ldgReadTimeout)
	defer cancel()
	return client.GetMessage(ctx, id)
}

// ldgReadProof asks the provider that signed the receipt, then the Foundation's vault, for its settlement proof.
func (a *App) ldgReadProof(r *http.Request, profile, receiptID, providerKey string) ldgProofRead {
	var read ldgProofRead
	for _, choice := range a.selection(profile).Providers {
		if choice.OperatorKey != providerKey {
			continue
		}
		label := ldgNamed(choice.Label, "a provider")
		client, err := a.clientFor(Endpoint{URL: choice.Endpoint, ServiceID: choice.OperatorKey, Label: choice.Label})
		if err == nil {
			proof, err := ldgSettlementCall(r, client, receiptID)
			if err == nil {
				read.Proof, read.From = &proof, label
				return read
			}
			read.Asked = append(read.Asked, ldgFailure(label, err))
		} else {
			read.Asked = append(read.Asked, ldgFailure(label, err))
		}
		break
	}
	foundation, err := a.foundationClient()
	if err != nil {
		return read
	}
	proof, err := ldgSettlementCall(r, foundation, receiptID)
	if err != nil {
		read.Asked = append(read.Asked, ldgFailure(a.ldgFoundationLabel(), err))
		return read
	}
	read.Proof, read.From = &proof, a.ldgFoundationLabel()
	return read
}

func ldgSettlementCall(r *http.Request, client *sdk.Client, receiptID string) (wire.SettlementProof, error) {
	ctx, cancel := context.WithTimeout(r.Context(), ldgReadTimeout)
	defer cancel()
	proof, _, err := client.Settlement(ctx, receiptID)
	return proof, err
}

// ldgClaimStepView states the settlement claim that holds the receipt, or that there is none yet.
func (a *App) ldgClaimStepView(profile string, record sdk.Record, read ldgProofRead, rec sdk.Record) ldgClaimStep {
	step := ldgClaimStep{Head: ldgHead{ID: "step-claim", Title: "3. Settlement claim", State: "Not yet", Class: "none"}}
	if read.Proof == nil {
		step.Asked = read.Asked
		if rec.Settlement == settlement.StatePending {
			step.Pending = fmt.Sprintf("No settlement proof yet. Claims are published within 24 hours after their period ends. Until %s your trust in %s lets this client treat the post as admitted; that is not proof of payment.", rec.RecheckBy.UTC().Format("2 Jan 2006"), a.ldgWho(profile, record.Receipt.ProviderID))
			return step
		}
		step.Head.State, step.Head.Class = "Not yet", "none"
		step.Pending = "No settlement proof from your sources. This post is not settled."
		return step
	}
	proof := read.Proof
	provider := ProviderChoice{OperatorKey: record.Receipt.ProviderID}
	step.HasProof = true
	step.ProofFrom = "Proof from " + read.From
	step.Facts = a.ldgClaimRow(profile, provider, proof.Claim, nil, sdk.CommitteeView{}, false)
	if len(proof.Chain) > 0 {
		last := proof.Chain[len(proof.Chain)-1]
		step.Chain = fmt.Sprintf("Witnessed through a later claim (sequence %d), linked by %d claims.", last.Sequence, len(proof.Chain))
	}
	if err := ldgLinkError(record.Receipt, *proof); err != nil {
		step.Leaf = "The proof does not link this receipt to the claim: " + err.Error() + "."
		step.Head = ldgHead{ID: "step-claim", Title: "3. Settlement claim", State: "Problem", Class: "bad"}
		return step
	}
	step.Leaf = fmt.Sprintf("This receipt is leaf %d of %d. A path of %s links it to the claim's receipts root.", proof.LeafIndex+1, proof.Claim.ReceiptCount, ldgSteps(len(proof.AuditPath)))
	if settlement.VerifyClaimObject(proof.Claim) == nil {
		step.Head = ldgHead{ID: "step-claim", Title: "3. Settlement claim", State: "Checked", Class: "ok"}
	} else {
		step.Head = ldgHead{ID: "step-claim", Title: "3. Settlement claim", State: "Problem", Class: "bad"}
	}
	return step
}

// ldgLinkError reports why a settlement proof does not place this receipt under its claim's receipts root.
func ldgLinkError(receipt wire.SubmissionReceipt, proof wire.SettlementProof) error {
	if proof.LeafIndex < 0 || proof.LeafIndex >= proof.Claim.ReceiptCount {
		return errors.New("the leaf index is outside the claim's receipts")
	}
	leaf, err := settlement.LeafOf(receipt)
	if err != nil {
		return errors.New("the receipt does not derive a settlement leaf")
	}
	root, err := settlement.RootFromPath(leaf, int(proof.LeafIndex), int(proof.Claim.ReceiptCount), proof.AuditPath)
	if err != nil {
		return errors.New("the audit path does not fit the leaf position")
	}
	if hex.EncodeToString(root.Hash[:]) != proof.Claim.ReceiptsRoot {
		return errors.New("the audit path ends at a different receipts root")
	}
	return nil
}

// ldgSteps names an audit path length in words a reader can follow.
func ldgSteps(n int) string {
	if n == 1 {
		return "1 step"
	}
	return fmt.Sprintf("%d steps", n)
}

// ldgWitnessStepView counts the committee's witnesses in the proof, for the claim that witnessed the receipt.
func (a *App) ldgWitnessStepView(profile string, read ldgProofRead, rec sdk.Record, root *sdk.TrustRoot) ldgWitnessStep {
	step := ldgWitnessStep{}
	step.Settled, step.Class, step.Head = ldgSettlementPill(rec.Settlement)
	if read.Proof == nil {
		return step
	}
	proof := read.Proof
	step.HasProof = true
	witnessed := proof.Claim
	if len(proof.Chain) > 0 {
		witnessed = proof.Chain[len(proof.Chain)-1]
	}
	counted, members := ldgCommitteeCount(witnessed, proof.Witnesses, root.Committee(a.now()))
	for _, member := range members {
		row := ldgMonitorRow{Label: a.ldgWho(profile, member), Key: member, Short: shortDID(member)}
		if _, ok := counted[member]; ok {
			for _, witness := range proof.Witnesses {
				if witness.WitnessID == member && settlement.WitnessMatches(witness, witnessed) && settlement.VerifyWitness(witness) == nil {
					at, _ := wire.ParseTime(witness.WitnessedAt)
					row.Witnessed, row.When, row.Signer = true, ldgTimeOf(at), ldgVerified(row.Label, member)
					break
				}
			}
		}
		if !row.Witnessed {
			row.Text = "No witness in this proof"
		}
		step.Rows = append(step.Rows, row)
	}
	n := len(members)
	step.Summary = fmt.Sprintf("%d of %d committee monitors witnessed; quorum is more than two-thirds (%d).", len(counted), n, settlement.Quorum(n))
	step.Note = "A settlement proof shows a quorum of the committee audited the fee as paid. It does not prove the provider is solvent, that the post is true, or when it was written."
	return step
}

// ldgSettlementPill names the settlement state of a post and the state of its committee step.
func ldgSettlementPill(state settlement.State) (string, string, ldgHead) {
	head := ldgHead{ID: "step-witnesses", Title: "4. Committee witnesses", State: "Not yet", Class: "none"}
	switch state {
	case settlement.StateSettled:
		head.State, head.Class = "Checked", "ok"
		return "Settled: the fee is proven paid to the Foundation", "ok", head
	case settlement.StatePending:
		return "Pending settlement", "warn", head
	default:
		head.State, head.Class = "Problem", "bad"
		return "Unsettled", "bad", head
	}
}

// ldgVaultResult asks the Foundation whether its Lifetime Vault holds a post, and verifies the receipt it returns.
func (a *App) ldgVaultResult(r *http.Request, id string) ldgVaultResult {
	foundation, err := a.foundationClient()
	if err != nil {
		return ldgVaultResult{Text: "No Foundation endpoint is configured in this client."}
	}
	ctx, cancel := context.WithTimeout(r.Context(), ldgReadTimeout)
	defer cancel()
	receipt, _, err := foundation.VaultReceipt(ctx, id)
	if err != nil {
		var answered *sdk.Error
		if errors.As(err, &answered) && answered.HTTPStatus == http.StatusNotFound {
			return ldgVaultResult{Text: "The vault does not hold this post yet. A provider delivers settled posts within 7 days after settlement."}
		}
		return ldgVaultResult{Text: ldgFailure(a.ldgFoundationLabel(), err)}
	}
	by := a.ldgFoundationLabel()
	root, _ := a.trustRoot()
	if root == nil {
		return ldgVaultResult{Text: "A vault receipt was returned but could not be verified: the trust root is unavailable.", HasSigner: true, Signer: ldgUnchecked(by, receipt.FoundationID, "the trust root is unavailable")}
	}
	if err := root.VerifyVaultReceipt(receipt, id, nil); err != nil {
		return ldgVaultResult{Text: "A vault receipt was returned but could not be verified: " + err.Error() + ".", HasSigner: true, Signer: ldgFailed(by, receipt.FoundationID, err)}
	}
	stored, _ := wire.ParseTime(receipt.StoredAt)
	return ldgVaultResult{Stored: true, StoredAt: ldgTimeOf(stored), HasSigner: true, Signer: ldgVerified(by, receipt.FoundationID)}
}

// ldgVaultHead gives the Lifetime Vault step its state after a check.
func ldgVaultHead(result ldgVaultResult) ldgHead {
	head := ldgHead{ID: "step-vault", Title: "5. Lifetime Vault", State: "Problem", Class: "bad"}
	if result.Stored && result.Signer.State == "verified" {
		head.State, head.Class = "Checked", "ok"
	}
	return head
}

// ldgCommitteeCount counts witnesses for a claim against the committee in force, and the committee before it during a handover.
func ldgCommitteeCount(claim wire.SettlementClaim, witnesses []wire.SettlementWitness, view sdk.CommitteeView) (map[string]*big.Int, []string) {
	counted := settlement.CountWitnesses(claim, witnesses, view.Current, nil)
	if settlement.Settled(counted, view.Current) || len(view.Previous) == 0 || view.ChangedAt.IsZero() {
		return counted, view.Current
	}
	changed := view.ChangedAt
	previous := settlement.CountWitnesses(claim, witnesses, view.Previous, &changed)
	if settlement.Settled(previous, view.Previous) {
		return previous, view.Previous
	}
	return counted, view.Current
}
