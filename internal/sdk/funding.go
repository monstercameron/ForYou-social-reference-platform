package sdk

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"fmt"
	"net/http"
	"time"

	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/funding"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

var (
	// ErrUnsupportedNetwork identifies a funding network outside the two supported mainnets.
	ErrUnsupportedNetwork = errors.New("sdk: unsupported funding network")
	// ErrDisclosureRequired requires custody, balance, cost, loss, remedy, and linkability disclosures before funding.
	ErrDisclosureRequired = errors.New("sdk: funding disclosure acknowledgement required")
	// ErrCustodyCap identifies a deposit that would exceed the signed schedule's custody cap.
	ErrCustodyCap = errors.New("sdk: custody cap exceeded")
	// ErrInvoiceRejected means the provider invoice failed local verification and must not be handed to a wallet.
	ErrInvoiceRejected = errors.New("sdk: deposit invoice rejected")
)

// DepositPlan contains the locally approved terms and accounting state used before requesting an invoice.
type DepositPlan struct {
	// Network is the provider-local balance network.
	Network string
	// AmountChainUnits is the exact positive native chain amount.
	AmountChainUnits string
	// DisclosureAcknowledged confirms receipt of the eight custody disclosures and linkability notice.
	DisclosureAcknowledged bool
	// Schedule supplies the locally approved network fee schedule.
	Schedule wire.FeeSchedule
	// Confirmed is the current confirmed balance in accounting units.
	Confirmed string
	// Pending is the current pending balance in accounting units.
	Pending string
}

// ObserveResult distinguishes unspendable pending funding from a provider-signed committed credit.
type ObserveResult struct {
	// Pending means the transfer has not become spendable.
	Pending bool
	// Credit is the verified provider-signed credit, when one was committed.
	Credit *wire.DepositCredit
}

// RequestDeposit performs all client funding checks, durably records the attempt, and verifies the invoice before returning it.
func (c *Client) RequestDeposit(ctx context.Context, key ed25519.PrivateKey, p DepositPlan, log FundingLog) (wire.DepositInvoice, wire.DepositRequest, error) {
	if log == nil {
		return wire.DepositInvoice{}, wire.DepositRequest{}, errors.New("sdk: funding log required")
	}
	if p.Network != wire.BitcoinMainnet && p.Network != wire.EthereumMainnet {
		return wire.DepositInvoice{}, wire.DepositRequest{}, ErrUnsupportedNetwork
	}
	if !p.DisclosureAcknowledged {
		return wire.DepositInvoice{}, wire.DepositRequest{}, ErrDisclosureRequired
	}
	if p.Schedule.Network != p.Network {
		return wire.DepositInvoice{}, wire.DepositRequest{}, errors.New("sdk: fee schedule network does not match deposit network")
	}
	amount, err := fee.ParsePositiveAmount(p.AmountChainUnits)
	if err != nil {
		return wire.DepositInvoice{}, wire.DepositRequest{}, fmt.Errorf("sdk: invalid deposit amount: %w", err)
	}
	confirmed, err := fee.ParseAmount(p.Confirmed)
	if err != nil {
		return wire.DepositInvoice{}, wire.DepositRequest{}, fmt.Errorf("sdk: invalid confirmed balance: %w", err)
	}
	pending, err := fee.ParseAmount(p.Pending)
	if err != nil {
		return wire.DepositInvoice{}, wire.DepositRequest{}, fmt.Errorf("sdk: invalid pending balance: %w", err)
	}
	accounting, err := fee.AccountingUnits(p.Network, amount)
	if err != nil {
		return wire.DepositInvoice{}, wire.DepositRequest{}, err
	}
	allowed, err := fee.DepositAllowed(p.Schedule, confirmed, pending, accounting)
	if err != nil {
		return wire.DepositInvoice{}, wire.DepositRequest{}, fmt.Errorf("sdk: invalid custody cap inputs: %w", err)
	}
	if !allowed {
		return wire.DepositInvoice{}, wire.DepositRequest{}, ErrCustodyCap
	}
	requestID := make([]byte, 32)
	if _, err = rand.Read(requestID); err != nil {
		return wire.DepositInvoice{}, wire.DepositRequest{}, err
	}
	now := c.now()
	req := wire.DepositRequest{ProviderID: c.ServiceID(), AuthorID: keys.DIDOf(key), Network: p.Network, AmountChainUnits: p.AmountChainUnits, RequestID: keys.EncodeB64URL(requestID), NotAfter: wire.FormatTime(now.Add(time.Minute))}
	if err = funding.ValidateDepositRequest(req, c.ServiceID(), req.AuthorID, now); err != nil {
		return wire.DepositInvoice{}, wire.DepositRequest{}, err
	}
	if err = log.Append(FundingEvent{Kind: FundingRequested, RequestID: req.RequestID, Network: req.Network, AmountChainUnits: req.AmountChainUnits, At: wire.FormatTime(now)}); err != nil {
		return wire.DepositInvoice{}, wire.DepositRequest{}, err
	}
	body, err := acJSON(req)
	if err != nil {
		err = acLogFailure(log, req, err, c.now())
		return wire.DepositInvoice{}, wire.DepositRequest{}, err
	}
	path := "/v1/deposits"
	headers, err := c.acAuthenticate(ctx, key, http.MethodPost, acResource(c, path), body)
	if err != nil {
		if !IsAmbiguous(err) {
			err = acLogFailure(log, req, err, c.now())
		}
		return wire.DepositInvoice{}, wire.DepositRequest{}, err
	}
	res, err := c.send(ctx, http.MethodPost, path, nil, body, headers, rdObjectLimit)
	if err != nil {
		if !IsAmbiguous(err) {
			err = acLogFailure(log, req, err, c.now())
		}
		return wire.DepositInvoice{}, wire.DepositRequest{}, err
	}
	if res.status != http.StatusCreated && res.status != http.StatusOK {
		err = rdProtocol("unexpected deposit invoice status")
		err = acLogFailure(log, req, err, c.now())
		return wire.DepositInvoice{}, wire.DepositRequest{}, err
	}
	var invoice wire.DepositInvoice
	if err = acDecode(res.body, "DepositInvoice", &invoice); err == nil {
		err = funding.CheckInvoice(invoice, req, c.ServiceID(), c.now())
	}
	if err != nil {
		err = acLogFailure(log, req, err, c.now())
		return wire.DepositInvoice{}, wire.DepositRequest{}, fmt.Errorf("%w: %w", ErrInvoiceRejected, err)
	}
	if err = log.Append(FundingEvent{Kind: FundingInvoiced, RequestID: req.RequestID, DepositID: invoice.DepositID, Network: invoice.Network, AmountChainUnits: invoice.AmountChainUnits, Invoice: &invoice, At: wire.FormatTime(c.now())}); err != nil {
		return wire.DepositInvoice{}, wire.DepositRequest{}, err
	}
	return invoice, req, nil
}

// ObserveDeposit submits a transfer hint; a deposit must never be described as available before this signed credit.
func (c *Client) ObserveDeposit(ctx context.Context, key ed25519.PrivateKey, inv wire.DepositInvoice, t wire.DepositTransfer, log FundingLog) (ObserveResult, error) {
	if log == nil {
		return ObserveResult{}, errors.New("sdk: funding log required")
	}
	attempts, err := log.Attempts()
	if err != nil {
		return ObserveResult{}, err
	}
	var recordedInvoice *wire.DepositInvoice
	var requestID string
	for _, attempt := range attempts {
		if attempt.DepositID == inv.DepositID {
			requestID = attempt.RequestID
			recordedInvoice = attempt.Invoice
			break
		}
	}
	if requestID == "" {
		return ObserveResult{}, errors.New("sdk: deposit invoice is not in funding log")
	}
	if recordedInvoice == nil || inv != *recordedInvoice {
		return ObserveResult{}, errors.New("sdk: invoice does not match verified funding log invoice")
	}
	proof := wire.DepositProof{ProviderID: c.ServiceID(), AuthorID: keys.DIDOf(key), DepositID: inv.DepositID, Transfer: t}
	if err = funding.ValidateDepositProof(proof, c.ServiceID(), proof.AuthorID); err != nil {
		return ObserveResult{}, err
	}
	if t.Network != inv.Network {
		return ObserveResult{}, errors.New("sdk: transfer network does not match invoice")
	}
	body, err := acJSON(proof)
	if err != nil {
		return ObserveResult{}, err
	}
	if err = log.Append(FundingEvent{Kind: FundingTransferSent, RequestID: requestID, DepositID: inv.DepositID, Network: t.Network, Transfer: &t, At: wire.FormatTime(c.now())}); err != nil {
		return ObserveResult{}, err
	}
	path := "/v1/deposits/observe"
	headers, err := c.acAuthenticate(ctx, key, http.MethodPost, acResource(c, path), body)
	if err != nil {
		if !IsAmbiguous(err) {
			err = acLogEventFailure(log, requestID, inv.DepositID, t.Network, err, c.now())
		}
		return ObserveResult{}, err
	}
	res, err := c.send(ctx, http.MethodPost, path, nil, body, headers, rdObjectLimit)
	if err != nil {
		if !IsAmbiguous(err) {
			err = acLogEventFailure(log, requestID, inv.DepositID, t.Network, err, c.now())
		}
		return ObserveResult{}, err
	}
	if res.status == http.StatusAccepted {
		if err = acDecodeExactStatus(res.body, "pending"); err != nil {
			err = acLogEventFailure(log, requestID, inv.DepositID, t.Network, err, c.now())
			return ObserveResult{}, err
		}
		if err = log.Append(FundingEvent{Kind: FundingObservedPending, RequestID: requestID, DepositID: inv.DepositID, Network: t.Network, At: wire.FormatTime(c.now())}); err != nil {
			return ObserveResult{}, err
		}
		return ObserveResult{Pending: true}, nil
	}
	if res.status != http.StatusCreated && res.status != http.StatusOK {
		err = rdProtocol("unexpected deposit observation status")
		err = acLogEventFailure(log, requestID, inv.DepositID, t.Network, err, c.now())
		return ObserveResult{}, err
	}
	var credit wire.DepositCredit
	if err = acDecode(res.body, "DepositCredit", &credit); err != nil {
		err = acLogEventFailure(log, requestID, inv.DepositID, t.Network, err, c.now())
		return ObserveResult{}, err
	}
	if err = funding.VerifyCredit(credit, c.ServiceID()); err != nil {
		err = rdProtocol("invalid DepositCredit")
		err = acLogEventFailure(log, requestID, inv.DepositID, t.Network, err, c.now())
		return ObserveResult{}, err
	}
	amount, amountErr := fee.ParsePositiveAmount(inv.AmountChainUnits)
	if amountErr != nil {
		return ObserveResult{}, rdProtocol("invalid invoice amount in funding log")
	}
	accounting, convErr := fee.AccountingUnits(inv.Network, amount)
	if convErr != nil || credit.DepositID != inv.DepositID || credit.AuthorID != proof.AuthorID || credit.Network != inv.Network || credit.AmountChainUnits != inv.AmountChainUnits || !acSameTransfer(credit.Transfer, t) || accounting.String() != credit.AmountAccountingUnits {
		err = rdProtocol("DepositCredit does not match invoice and transfer")
		err = acLogEventFailure(log, requestID, inv.DepositID, t.Network, err, c.now())
		return ObserveResult{}, err
	}
	if err = log.Append(FundingEvent{Kind: FundingCredited, RequestID: requestID, DepositID: inv.DepositID, Network: inv.Network, AmountChainUnits: inv.AmountChainUnits, CreditID: credit.CreditID, At: wire.FormatTime(c.now())}); err != nil {
		return ObserveResult{}, err
	}
	return ObserveResult{Credit: &credit}, nil
}

// Balance returns a verified provider-signed balance assertion, not proof of solvency.
func (c *Client) Balance(ctx context.Context, key ed25519.PrivateKey, network string) (wire.BalanceStatus, error) {
	if network != wire.BitcoinMainnet && network != wire.EthereumMainnet {
		return wire.BalanceStatus{}, ErrUnsupportedNetwork
	}
	path := "/v1/balances/" + rdEscape(network)
	headers, err := c.acAuthenticate(ctx, key, http.MethodGet, acResource(c, path), nil)
	if err != nil {
		return wire.BalanceStatus{}, err
	}
	res, err := c.send(ctx, http.MethodGet, path, nil, nil, headers, rdObjectLimit)
	if err != nil {
		return wire.BalanceStatus{}, err
	}
	if res.status != http.StatusOK {
		return wire.BalanceStatus{}, rdProtocol("unexpected balance status")
	}
	var balance wire.BalanceStatus
	if err = acDecode(res.body, "BalanceStatus", &balance); err != nil {
		return balance, err
	}
	if err = funding.VerifyBalance(balance, c.ServiceID()); err != nil || balance.AuthorID != keys.DIDOf(key) || balance.Network != network {
		return wire.BalanceStatus{}, rdProtocol("invalid BalanceStatus binding or signature")
	}
	return balance, nil
}

// Withdraw requests a provider-local withdrawal; callers must reuse requestID after an ambiguous result.
func (c *Client) Withdraw(ctx context.Context, key ed25519.PrivateKey, network, amountBaseUnits, recipient, requestID string) (wire.WithdrawalStatus, error) {
	req := wire.WithdrawalRequest{ProviderID: c.ServiceID(), AuthorID: keys.DIDOf(key), Network: network, AmountBaseUnits: amountBaseUnits, Recipient: recipient, RequestID: requestID}
	if err := funding.ValidateWithdrawalRequest(req, c.ServiceID(), req.AuthorID); err != nil {
		return wire.WithdrawalStatus{}, err
	}
	body, err := acJSON(req)
	if err != nil {
		return wire.WithdrawalStatus{}, err
	}
	path := "/v1/withdrawals"
	headers, err := c.acAuthenticate(ctx, key, http.MethodPost, acResource(c, path), body)
	if err != nil {
		return wire.WithdrawalStatus{}, err
	}
	res, err := c.send(ctx, http.MethodPost, path, nil, body, headers, rdObjectLimit)
	if err != nil {
		return wire.WithdrawalStatus{}, err
	}
	if res.status != http.StatusAccepted && res.status != http.StatusOK {
		return wire.WithdrawalStatus{}, rdProtocol("unexpected withdrawal status")
	}
	var status wire.WithdrawalStatus
	if err = acDecode(res.body, "WithdrawalStatus", &status); err != nil {
		return status, err
	}
	if err = funding.VerifyWithdrawal(status, c.ServiceID()); err != nil {
		return wire.WithdrawalStatus{}, rdProtocol("invalid WithdrawalStatus")
	}
	sameRecipient, recipientErr := funding.SameRecipient(network, status.Recipient, recipient)
	if recipientErr != nil || !sameRecipient || status.AuthorID != req.AuthorID || status.Network != network || status.AmountBaseUnits != amountBaseUnits || status.RequestID != requestID {
		return wire.WithdrawalStatus{}, rdProtocol("WithdrawalStatus does not match request")
	}
	return status, nil
}

// Admission retrieves and verifies a committed receipt for one message.
func (c *Client) Admission(ctx context.Context, key ed25519.PrivateKey, messageID string) (wire.SubmissionReceipt, error) {
	path := "/v1/admissions/" + rdEscape(messageID)
	headers, err := c.acAuthenticate(ctx, key, http.MethodGet, acResource(c, path), nil)
	if err != nil {
		return wire.SubmissionReceipt{}, err
	}
	res, err := c.send(ctx, http.MethodGet, path, nil, nil, headers, rdObjectLimit)
	if err != nil {
		return wire.SubmissionReceipt{}, err
	}
	if res.status != http.StatusOK {
		return wire.SubmissionReceipt{}, rdProtocol("unexpected admission status")
	}
	var receipt wire.SubmissionReceipt
	if err = acDecode(res.body, "SubmissionReceipt", &receipt); err != nil {
		return receipt, err
	}
	if err = fee.VerifyReceipt(receipt); err != nil || receipt.ProviderID != c.ServiceID() || receipt.AuthorID != keys.DIDOf(key) || receipt.MessageID != messageID {
		return wire.SubmissionReceipt{}, rdProtocol("invalid SubmissionReceipt binding or signature")
	}
	return receipt, nil
}

func acLogFailure(log FundingLog, req wire.DepositRequest, err error, now time.Time) error {
	if logErr := log.Append(FundingEvent{Kind: FundingFailed, RequestID: req.RequestID, Network: req.Network, Error: err.Error(), At: wire.FormatTime(now)}); logErr != nil {
		return errors.Join(err, logErr)
	}
	return err
}
func acLogEventFailure(log FundingLog, requestID, depositID, network string, err error, now time.Time) error {
	if logErr := log.Append(FundingEvent{Kind: FundingFailed, RequestID: requestID, DepositID: depositID, Network: network, Error: err.Error(), At: wire.FormatTime(now)}); logErr != nil {
		return errors.Join(err, logErr)
	}
	return err
}

func acSameTransfer(a, b wire.DepositTransfer) bool {
	return a.Network == b.Network && a.TxID == b.TxID && a.TxHash == b.TxHash && acIndex(a.OutputIndex) == acIndex(b.OutputIndex)
}

func acIndex(value *int64) int64 {
	if value == nil {
		return -1
	}
	return *value
}
