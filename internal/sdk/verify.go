package sdk

import (
	"errors"
	"math/big"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/delegation"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/message"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/succession"
	"foryou.local/foryou/internal/wire"
)

// Class is a local record trust classification.
type Class string

const (
	// ClassInvalid marks a record with a failed required verification.
	ClassInvalid Class = "invalid"
	// ClassValidUntrusted marks valid evidence from a locally untrusted provider.
	ClassValidUntrusted Class = "cryptographically_valid_untrusted"
	// ClassValidTrustedUnaccepted marks trusted-provider evidence not durably accepted here.
	ClassValidTrustedUnaccepted Class = "cryptographically_valid_trusted_unaccepted"
	// ClassLocallyAdmitted marks trusted evidence durably accepted with eligible settlement.
	ClassLocallyAdmitted Class = "locally_admitted"
)

// SourceKind identifies where a record representation was fetched.
type SourceKind int

const (
	// SourceProvider is a provider's own public read route.
	SourceProvider SourceKind = iota + 1
	// SourceVault is a Foundation Lifetime Vault read.
	SourceVault
	// SourceMirror is a replica or another relay.
	SourceMirror
)

// Source identifies the endpoint and service that supplied a record.
type Source struct {
	Endpoint, ServiceID string
	Kind                SourceKind
}

// Check records one ordered verification step.
type Check struct {
	Step   int
	Name   string
	OK     bool
	Detail string
}

// Record contains a verified message, receipt, and local verification state.
type Record struct {
	ID                 string
	AuthorID           string
	SignerID           string
	Delegated          bool
	Timestamp          time.Time
	Raw                []byte
	Message            wire.TransportMessage
	Receipt            wire.SubmissionReceipt
	BillableBytes      int
	Fee                string
	Network            string
	Denomination       string
	ScheduleID         string
	Source             Source
	Checks             []Check
	Failure            *Check
	Class              Class
	Settlement         settlement.State
	Proof              *wire.SettlementProof
	SettledUnderPolicy string
	RecheckBy          time.Time
	CheckedAt          time.Time
}

// VerifierConfig configures a verifier's pinned root, local provider policy, and time.
type VerifierConfig struct {
	Root                *TrustRoot
	Trusts              func(providerID string) bool
	Ledger              *Ledger
	ProviderSuccessions []wire.KeySuccession
	Now                 func() time.Time
}

// Verifier verifies records and applies local settlement and admission rules.
type Verifier struct {
	root        *TrustRoot
	trusts      func(string) bool
	ledger      *Ledger
	successions []wire.KeySuccession
	now         func() time.Time
}

// NewVerifier creates a verifier; a nil provider policy trusts no provider.
func NewVerifier(cfg VerifierConfig) *Verifier {
	if cfg.Ledger == nil {
		cfg.Ledger = NewLedger()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	verified := make([]wire.KeySuccession, 0, len(cfg.ProviderSuccessions))
	for _, s := range cfg.ProviderSuccessions {
		if succession.Verify(s) == nil {
			verified = append(verified, s)
		}
	}
	return &Verifier{root: cfg.Root, trusts: cfg.Trusts, ledger: cfg.Ledger, successions: verified, now: cfg.Now}
}

// Verify checks a downloaded message through schedule and fee verification; settlement remains unsettled.
func (v *Verifier) Verify(raw []byte, requestedID string, src Source) Record {
	now := v.now().UTC().Truncate(time.Second)
	r := Record{Raw: append([]byte(nil), raw...), Source: src, CheckedAt: now, Class: ClassInvalid, Settlement: settlement.StateUnsettled}
	check := func(step int, name string, err error) bool {
		c := Check{Step: step, Name: name, OK: err == nil}
		if err != nil {
			c.Detail = err.Error()
		}
		r.Checks = append(r.Checks, c)
		if err != nil {
			r.Failure = &r.Checks[len(r.Checks)-1]
			return false
		}
		return true
	}
	var m wire.TransportMessage
	var err error
	if len(raw) > message.MaxMessageBytes {
		err = errors.New("message exceeds 1048576 bytes")
	} else {
		err = wire.Decode(raw, "TransportMessage", &m)
	}
	if err == nil && (m.Version.Major != 2 || m.Version.Minor != 0) {
		err = errors.New("unsupported signed message version")
	}
	if !check(1, "version and closed schema", err) {
		return r
	}
	intent := message.IntentOf(m)
	id, idErr := message.ID(intent)
	if idErr == nil && (id != m.MessageID || (requestedID != "" && requestedID != id)) {
		idErr = errors.New("canonical message_id mismatch")
	}
	if !check(2, "canonical message ID", idErr) {
		return r
	}
	issuedAt, e := wire.ParseTime(m.SubmissionReceipt.AuthorizationAt)
	verifyErr := message.ValidateIntent(intent)
	signer := m.AuthorID
	if m.SignerID != "" {
		signer = m.SignerID
	}
	canonical, ce := canon.EncodeWithout(m, "signature")
	if verifyErr == nil && ce != nil {
		verifyErr = ce
	} else if verifyErr == nil {
		verifyErr = keys.Verify(signer, "message", canonical, m.Signature)
		if verifyErr == nil && m.Delegation != nil {
			verifyErr = delegation.CheckIntentDelegation(*m.Delegation, intent, m.SubmissionReceipt.ProviderID, issuedAt)
		}
	}
	if e != nil && verifyErr == nil {
		verifyErr = e
	}
	if !check(3, "author or delegated signature", verifyErr) {
		return r
	}
	receipt := m.SubmissionReceipt
	receiptErr := fee.VerifyReceipt(receipt)
	if receiptErr == nil && (receipt.MessageID != m.MessageID || receipt.AuthorID != m.AuthorID) {
		receiptErr = errors.New("receipt does not bind message and author")
	}
	if !check(4, "provider receipt signature and binding", receiptErr) {
		return r
	}
	issuedAt, e = wire.ParseTime(receipt.IssuedAt)
	var schedule wire.FeeSchedule
	var billable int
	var amount *big.Int
	step5err := e
	if step5err == nil && v.root == nil {
		step5err = errors.New("trust root is required")
	}
	if step5err == nil {
		schedule, step5err = v.root.ScheduleForReceipt(receipt.ScheduleID, receipt.Network, issuedAt)
	}
	if step5err == nil && receipt.Network != schedule.Network {
		step5err = errors.New("receipt network differs from schedule")
	}
	if step5err == nil {
		intentBytes, encErr := canon.Encode(intent)
		if encErr != nil {
			step5err = encErr
		} else {
			billable = len(intentBytes)
			amount, step5err = fee.Amount(schedule, billable)
		}
	}
	if step5err == nil && amount.String() != receipt.AmountBaseUnits {
		step5err = errors.New("receipt amount differs from recomputed fee")
	}
	timestamp, tsErr := wire.ParseTime(m.Timestamp)
	if step5err == nil && tsErr != nil {
		step5err = tsErr
	}
	if step5err == nil && (issuedAt.After(timestamp.Add(300*time.Second)) || issuedAt.Before(timestamp.Add(-60*time.Second))) {
		step5err = errors.New("receipt issued_at outside message timestamp window")
	}
	if step5err == nil && issuedAt.After(now.Add(60*time.Second)) {
		step5err = errors.New("receipt issued_at more than 60 seconds ahead")
	}
	if !check(5, "approved schedule and fee", step5err) {
		return r
	}
	r.ID = id
	r.AuthorID = m.AuthorID
	r.SignerID = signer
	r.Delegated = m.Delegation != nil
	r.Timestamp = timestamp
	r.Message = m
	r.Receipt = receipt
	r.BillableBytes = billable
	r.Fee = amount.String()
	r.Network = schedule.Network
	r.Denomination = schedule.Denomination
	r.ScheduleID = schedule.ScheduleID
	if v.trusts != nil && v.trusts(receipt.ProviderID) {
		r.Class = ClassValidTrustedUnaccepted
	} else {
		r.Class = ClassValidUntrusted
	}
	return r
}

func vfProviderLine(provider string, successions []wire.KeySuccession) (string, map[string]wire.KeySuccession) {
	byNew := map[string]wire.KeySuccession{}
	ambiguous := map[string]bool{}
	byID := map[string]wire.KeySuccession{}
	for _, s := range successions {
		if old, ok := byNew[s.NewID]; ok && old.OldID != s.OldID {
			delete(byNew, s.NewID)
			ambiguous[s.NewID] = true
			byID[s.SuccessionID] = s
			continue
		}
		if !ambiguous[s.NewID] {
			byNew[s.NewID] = s
		}
		byID[s.SuccessionID] = s
	}
	line := provider
	seen := map[string]bool{line: true}
	for i := 0; i < 128; i++ {
		s, ok := byNew[line]
		if !ok || seen[s.OldID] {
			break
		}
		line = s.OldID
		seen[line] = true
	}
	return line, byID
}

func (v *Verifier) vfTrusted(provider string) bool { return v.trusts != nil && v.trusts(provider) }
