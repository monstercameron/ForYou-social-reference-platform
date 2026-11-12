package sdk

import (
	"time"

	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/succession"
	"foryou.local/foryou/internal/wire"
)

// Settle applies proof, pending-settlement, and delinquency rules to a valid record.
func (v *Verifier) Settle(rec Record, proof *wire.SettlementProof, successions []wire.KeySuccession) Record {
	if rec.Rejected() {
		return rec
	}
	now := v.now().UTC().Truncate(time.Second)
	verified := append([]wire.KeySuccession(nil), v.successions...)
	for _, s := range successions {
		if succession.Verify(s) == nil {
			verified = append(verified, s)
		}
	}
	line, byID := vfProviderLine(rec.Receipt.ProviderID, verified)
	view := CommitteeView{}
	if v.root != nil {
		view = v.root.Committee(now)
	}
	ctx := settlement.ProofContext{Committee: view.Current, PreviousCommittee: view.Previous, Successions: func(id string) (wire.KeySuccession, bool) { s, ok := byID[id]; return s, ok }}
	if !view.ChangedAt.IsZero() {
		changed := view.ChangedAt
		ctx.ChangedAt = &changed
	}
	state := settlement.ReceiptState(rec.Receipt, proof, ctx)
	if state == settlement.StateSettled {
		rec.Settlement = state
		copyProof := *proof
		rec.Proof = &copyProof
		rec.SettledUnderPolicy = view.PolicyID
		rec.RecheckBy = time.Time{}
		v.ledger.vfClear(rec.Receipt.ReceiptID)
		return rec
	}
	rec.Settlement = settlement.StateUnsettled
	rec.Proof = nil
	rec.SettledUnderPolicy = ""
	providerRoute := rec.Source.Kind == SourceProvider && vfOnLine(rec.Source.ServiceID, rec.Receipt.ProviderID, verified)
	input := settlement.PendingInput{Receipt: rec.Receipt, FromProviderRoute: providerRoute, TrustedProvider: v.vfTrusted(rec.Receipt.ProviderID), ProviderLine: line}
	if delinquency, ok := settlement.Delinquent(input, now); ok {
		v.ledger.vfAdd(delinquency)
	}
	rec.Settlement = settlement.PendingState(input, v.ledger.vfMap(), now)
	if rec.Settlement == settlement.StatePending {
		issued, _ := wire.ParseTime(rec.Receipt.IssuedAt)
		rec.RecheckBy = issued.Add(7 * 24 * time.Hour)
	} else {
		rec.RecheckBy = time.Time{}
	}
	return rec
}

func vfOnLine(service, provider string, successions []wire.KeySuccession) bool {
	serviceLine, _ := vfProviderLine(service, successions)
	providerLine, _ := vfProviderLine(provider, successions)
	return serviceLine == providerLine
}

// Admit marks a trusted record locally admitted only after its host durably stores it.
func (v *Verifier) Admit(rec Record) Record {
	if rec.Class == ClassValidTrustedUnaccepted && (rec.Settlement == settlement.StateSettled || rec.Settlement == settlement.StatePending) {
		rec.Class = ClassLocallyAdmitted
	}
	return rec
}

// NeedsRecheck reports when cached pending or settled evidence should be refreshed.
func (v *Verifier) NeedsRecheck(rec Record) bool {
	now := v.now().UTC().Truncate(time.Second)
	if rec.Settlement == settlement.StatePending {
		if !rec.RecheckBy.IsZero() && !now.Before(rec.RecheckBy.Add(-24*time.Hour)) {
			return true
		}
		line, _ := vfProviderLine(rec.Receipt.ProviderID, v.successions)
		for _, r := range v.ledger.Records() {
			if r.ProviderLine == line {
				return true
			}
		}
	}
	if rec.Settlement == settlement.StateSettled && v.root != nil {
		return v.root.Committee(now).PolicyID != rec.SettledUnderPolicy
	}
	return false
}
