package sdk

import (
	"crypto/ed25519"
	"encoding/json"
	"sort"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/distribution"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/governance"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/succession"
	"foryou.local/foryou/internal/wire"
)

func TestSettleExampleProof(t *testing.T) {
	t.Run("example quorum settles receipt", func(t *testing.T) {
		example := vfExample(t)
		var receipt wire.SubmissionReceipt
		var proof wire.SettlementProof
		if err := json.Unmarshal(example["receipt"], &receipt); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(example["settlement_proof"], &proof); err != nil {
			t.Fatal(err)
		}
		now := time.Date(2027, 1, 7, 0, 0, 0, 0, time.UTC)
		root := vfTrust(t, now)
		v := NewVerifier(VerifierConfig{Root: root, Now: func() time.Time { return now }})
		rec := v.Settle(Record{Class: ClassValidTrustedUnaccepted, Receipt: receipt}, &proof, nil)
		if rec.Settlement != settlement.StateSettled {
			t.Fatalf("settlement = %q", rec.Settlement)
		}
	})
	t.Run("missing proof remains unsettled", func(t *testing.T) {
		example := vfExample(t)
		var receipt wire.SubmissionReceipt
		if err := json.Unmarshal(example["receipt"], &receipt); err != nil {
			t.Fatal(err)
		}
		now := time.Date(2027, 1, 7, 0, 0, 0, 0, time.UTC)
		root := vfTrust(t, now)
		v := NewVerifier(VerifierConfig{Root: root, Now: func() time.Time { return now }})
		rec := v.Settle(Record{Class: ClassValidTrustedUnaccepted, Receipt: receipt}, nil, nil)
		if rec.Settlement != settlement.StateUnsettled {
			t.Fatalf("settlement = %q", rec.Settlement)
		}
	})
	t.Run("three witnesses do not meet quorum", func(t *testing.T) {
		example := vfExample(t)
		var receipt wire.SubmissionReceipt
		var proof wire.SettlementProof
		if err := json.Unmarshal(example["receipt"], &receipt); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(example["settlement_proof"], &proof); err != nil {
			t.Fatal(err)
		}
		proof.Witnesses = proof.Witnesses[:3]
		now := time.Date(2027, 1, 7, 0, 0, 0, 0, time.UTC)
		v := NewVerifier(VerifierConfig{Root: vfTrust(t, now), Now: func() time.Time { return now }})
		if got := v.Settle(Record{Class: ClassValidTrustedUnaccepted, Receipt: receipt}, &proof, nil).Settlement; got != settlement.StateUnsettled {
			t.Fatalf("settlement = %q, want unsettled", got)
		}
	})
	t.Run("proof leaf amount must match receipt", func(t *testing.T) {
		example := vfExample(t)
		var receipt wire.SubmissionReceipt
		var proof wire.SettlementProof
		if err := json.Unmarshal(example["receipt"], &receipt); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(example["settlement_proof"], &proof); err != nil {
			t.Fatal(err)
		}
		proof.Leaf.AmountBaseUnits = "137"
		now := time.Date(2027, 1, 7, 0, 0, 0, 0, time.UTC)
		v := NewVerifier(VerifierConfig{Root: vfTrust(t, now), Now: func() time.Time { return now }})
		if got := v.Settle(Record{Class: ClassValidTrustedUnaccepted, Receipt: receipt}, &proof, nil).Settlement; got != settlement.StateUnsettled {
			t.Fatalf("settlement = %q, want unsettled", got)
		}
	})
	t.Run("valid proof clears an existing delinquency", func(t *testing.T) {
		example := vfExample(t)
		var receipt wire.SubmissionReceipt
		var proof wire.SettlementProof
		if err := json.Unmarshal(example["receipt"], &receipt); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(example["settlement_proof"], &proof); err != nil {
			t.Fatal(err)
		}
		issued, _ := wire.ParseTime(receipt.IssuedAt)
		now := issued.Add(7 * 24 * time.Hour)
		ledger := NewLedger()
		v := NewVerifier(VerifierConfig{Root: vfTrust(t, now), Ledger: ledger, Trusts: func(string) bool { return true }, Now: func() time.Time { return now }})
		rec := Record{Class: ClassValidTrustedUnaccepted, Receipt: receipt, Source: Source{Kind: SourceProvider, ServiceID: receipt.ProviderID}}
		if got := v.Settle(rec, nil, nil); got.Settlement != settlement.StateUnsettled || len(ledger.Records()) != 1 {
			t.Fatalf("delinquent state/records = %q, %#v", got.Settlement, ledger.Records())
		}
		if got := v.Settle(rec, &proof, nil); got.Settlement != settlement.StateSettled || len(ledger.Records()) != 0 {
			t.Fatalf("settled state/records = %q, %#v", got.Settlement, ledger.Records())
		}
	})
}

func TestLedgerSnapshotRestore(t *testing.T) {
	t.Run("restored ledger preserves delinquency", func(t *testing.T) {
		ledger := NewLedger()
		want := settlement.DelinquencyRecord{ProviderLine: "provider", ReceiptID: "receipt"}
		ledger.vfAdd(want)
		data, err := ledger.Snapshot()
		if err != nil {
			t.Fatal(err)
		}
		restored, err := RestoreLedger(data)
		if err != nil {
			t.Fatal(err)
		}
		got := restored.Records()
		if len(got) != 1 || got[0] != want {
			t.Fatalf("records = %#v, want %#v", got, want)
		}
	})
}

func TestPendingAndDelinquency(t *testing.T) {
	example := vfExample(t)
	var receipt wire.SubmissionReceipt
	if err := json.Unmarshal(example["receipt"], &receipt); err != nil {
		t.Fatal(err)
	}
	issued, err := wire.ParseTime(receipt.IssuedAt)
	if err != nil {
		t.Fatal(err)
	}
	provider := receipt.ProviderID
	t.Run("trusted provider route remains pending before seven days", func(t *testing.T) {
		now := issued.Add(6*24*time.Hour + 23*time.Hour)
		v := NewVerifier(VerifierConfig{Trusts: func(id string) bool { return id == provider }, Now: func() time.Time { return now }})
		rec := v.Settle(Record{Class: ClassValidTrustedUnaccepted, Receipt: receipt, Source: Source{Kind: SourceProvider, ServiceID: provider}}, nil, nil)
		if rec.Settlement != settlement.StatePending || !rec.RecheckBy.Equal(issued.Add(7*24*time.Hour)) {
			t.Fatalf("state/recheck = %q, %s", rec.Settlement, rec.RecheckBy)
		}
	})
	t.Run("seven days records delinquency and blocks later receipts", func(t *testing.T) {
		now := issued.Add(7 * 24 * time.Hour)
		ledger := NewLedger()
		v := NewVerifier(VerifierConfig{Ledger: ledger, Trusts: func(id string) bool { return id == provider }, Now: func() time.Time { return now }})
		old := v.Settle(Record{Class: ClassValidTrustedUnaccepted, Receipt: receipt, Source: Source{Kind: SourceProvider, ServiceID: provider}}, nil, nil)
		want := settlement.DelinquencyRecord{ProviderLine: provider, ReceiptID: receipt.ReceiptID}
		if old.Settlement != settlement.StateUnsettled || len(ledger.Records()) != 1 || ledger.Records()[0] != want {
			t.Fatalf("old state/records = %q, %#v", old.Settlement, ledger.Records())
		}
		fresh := receipt
		fresh.ReceiptID = "fresh-receipt"
		fresh.IssuedAt = wire.FormatTime(now)
		fresh.AuthorizationAt = fresh.IssuedAt
		blocked := v.Settle(Record{Class: ClassValidTrustedUnaccepted, Receipt: fresh, Source: Source{Kind: SourceProvider, ServiceID: provider}}, nil, nil)
		if blocked.Settlement != settlement.StateUnsettled {
			t.Fatalf("fresh receipt state = %q", blocked.Settlement)
		}
	})
	t.Run("vault source cannot create pending or delinquency", func(t *testing.T) {
		now := issued.Add(7 * 24 * time.Hour)
		ledger := NewLedger()
		v := NewVerifier(VerifierConfig{Ledger: ledger, Trusts: func(id string) bool { return id == provider }, Now: func() time.Time { return now }})
		rec := v.Settle(Record{Class: ClassValidTrustedUnaccepted, Receipt: receipt, Source: Source{Kind: SourceVault, ServiceID: provider}}, nil, nil)
		if rec.Settlement != settlement.StateUnsettled || len(ledger.Records()) != 0 {
			t.Fatalf("state/records = %q, %#v", rec.Settlement, ledger.Records())
		}
	})
	t.Run("untrusted provider cannot be pending", func(t *testing.T) {
		now := issued.Add(6*24*time.Hour + 23*time.Hour)
		v := NewVerifier(VerifierConfig{Now: func() time.Time { return now }, Trusts: func(string) bool { return false }})
		rec := v.Settle(Record{Class: ClassValidUntrusted, Receipt: receipt, Source: Source{Kind: SourceProvider, ServiceID: provider}}, nil, nil)
		if rec.Settlement != settlement.StateUnsettled {
			t.Fatalf("settlement = %q, want unsettled", rec.Settlement)
		}
	})
	t.Run("provider succession preserves delinquency and own-route trust", func(t *testing.T) {
		oldKey := vfProviderSeed(32)
		newKey := vfProviderSeed(192)
		effective := issued.Add(time.Hour)
		link, err := succession.New(oldKey, newKey, effective)
		if err != nil {
			t.Fatal(err)
		}
		oldID, newID := keys.DIDOf(oldKey), keys.DIDOf(newKey)
		if provider != oldID {
			t.Fatalf("fixture provider = %q, want %q", provider, oldID)
		}
		ledger := NewLedger()
		trusted := func(id string) bool { return id == oldID || id == newID }
		pendingAt := issued.Add(6*24*time.Hour + 23*time.Hour)
		v := NewVerifier(VerifierConfig{Ledger: ledger, Trusts: trusted, ProviderSuccessions: []wire.KeySuccession{link}, Now: func() time.Time { return pendingAt }})
		servedBySuccessor := v.Settle(Record{Class: ClassValidTrustedUnaccepted, Receipt: receipt, Source: Source{Kind: SourceProvider, ServiceID: newID}}, nil, nil)
		if servedBySuccessor.Settlement != settlement.StatePending {
			t.Fatalf("successor route settlement = %q, want pending_settlement", servedBySuccessor.Settlement)
		}
		v = NewVerifier(VerifierConfig{Ledger: ledger, Trusts: trusted, ProviderSuccessions: []wire.KeySuccession{link}, Now: func() time.Time { return issued.Add(7 * 24 * time.Hour) }})
		oldReceipt := v.Settle(Record{Class: ClassValidTrustedUnaccepted, Receipt: receipt, Source: Source{Kind: SourceProvider, ServiceID: oldID}}, nil, nil)
		if oldReceipt.Settlement != settlement.StateUnsettled {
			t.Fatalf("old receipt settlement = %q, want unsettled", oldReceipt.Settlement)
		}
		fresh := receipt
		fresh.ProviderID = newID
		freshID := [32]byte{1}
		fresh.ReceiptID = keys.EncodeB64URL(freshID[:])
		fresh.IssuedAt = wire.FormatTime(issued.Add(7 * 24 * time.Hour))
		fresh.AuthorizationAt = fresh.IssuedAt
		fresh, err = fee.SignReceipt(fresh, newKey)
		if err != nil {
			t.Fatal(err)
		}
		blocked := v.Settle(Record{Class: ClassValidTrustedUnaccepted, Receipt: fresh, Source: Source{Kind: SourceProvider, ServiceID: newID}}, nil, nil)
		if blocked.Settlement != settlement.StateUnsettled {
			t.Fatalf("successor receipt settlement = %q, want unsettled", blocked.Settlement)
		}
		if got := ledger.Records(); len(got) != 1 || got[0] != (settlement.DelinquencyRecord{ProviderLine: oldID, ReceiptID: receipt.ReceiptID}) {
			t.Fatalf("ledger = %#v", got)
		}
	})
	t.Run("ambiguous successor does not create a provider route", func(t *testing.T) {
		oldKey := vfProviderSeed(32)
		otherOldKey := ed25519.NewKeyFromSeed([]byte{64, 65, 66, 67, 68, 69, 70, 71, 72, 73, 74, 75, 76, 77, 78, 79, 80, 81, 82, 83, 84, 85, 86, 87, 88, 89, 90, 91, 92, 93, 94, 95})
		newKey := vfProviderSeed(192)
		first, err := succession.New(oldKey, newKey, issued.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		second, err := succession.New(otherOldKey, newKey, issued.Add(2*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		changed := receipt
		changed.ProviderID = keys.DIDOf(newKey)
		now := issued.Add(6*24*time.Hour + 23*time.Hour)
		v := NewVerifier(VerifierConfig{Trusts: func(string) bool { return true }, ProviderSuccessions: []wire.KeySuccession{first, second}, Now: func() time.Time { return now }})
		rec := v.Settle(Record{Class: ClassValidTrustedUnaccepted, Receipt: changed, Source: Source{Kind: SourceProvider, ServiceID: keys.DIDOf(oldKey)}}, nil, nil)
		if rec.Settlement != settlement.StateUnsettled {
			t.Fatalf("settlement = %q, want unsettled for an ambiguous successor", rec.Settlement)
		}
	})
}

func vfProviderSeed(first byte) ed25519.PrivateKey {
	seed := []byte{32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63}
	if first == 192 {
		seed = []byte{192, 193, 194, 195, 196, 197, 198, 199, 200, 201, 202, 203, 204, 205, 206, 207, 208, 209, 210, 211, 212, 213, 214, 215, 216, 217, 218, 219, 220, 221, 222, 223}
	}
	return ed25519.NewKeyFromSeed(seed)
}

func TestAdmitAndNeedsRecheck(t *testing.T) {
	t.Run("only settled or pending trusted records become admitted", func(t *testing.T) {
		v := NewVerifier(VerifierConfig{})
		pending := v.Admit(Record{Class: ClassValidTrustedUnaccepted, Settlement: settlement.StatePending})
		if pending.Class != ClassLocallyAdmitted {
			t.Fatalf("pending class = %q", pending.Class)
		}
		unsettled := v.Admit(Record{Class: ClassValidTrustedUnaccepted, Settlement: settlement.StateUnsettled})
		if unsettled.Class != ClassValidTrustedUnaccepted {
			t.Fatalf("unsettled class = %q", unsettled.Class)
		}
		settled := v.Admit(Record{Class: ClassValidTrustedUnaccepted, Settlement: settlement.StateSettled})
		if settled.Class != ClassLocallyAdmitted {
			t.Fatalf("settled class = %q", settled.Class)
		}
		untrusted := v.Admit(Record{Class: ClassValidUntrusted, Settlement: settlement.StatePending})
		if untrusted.Class != ClassValidUntrusted {
			t.Fatalf("untrusted class = %q", untrusted.Class)
		}
	})
	t.Run("settled evidence is rechecked after the committee policy changes", func(t *testing.T) {
		genesis := devnet.Genesis()
		policyB := genesis.DistributionPolicy
		policyB.Monitors = append([]string(nil), policyB.Monitors...)
		policyB.Revision = 2
		policyB.ApprovedAt = "2026-12-01T00:00:00Z"
		policyB.EffectiveAt = "2027-01-01T00:00:00Z"
		policyB.Monitors[0] = keys.DIDOf(ed25519.NewKeyFromSeed([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}))
		policyB.Monitors = vfSorted(policyB.Monitors)
		policyB.PolicyID, _ = distribution.PolicyID(policyB)
		unsignedBytes, err := canon.EncodeWithout(policyB, "release_signatures")
		if err != nil {
			t.Fatal(err)
		}
		unsigned, err := canon.Parse(unsignedBytes, canon.Limits{MaxDepth: 32})
		if err != nil {
			t.Fatal(err)
		}
		policyB.ReleaseSignatures, err = governance.SignRelease("distribution-policy", unsigned, devnet.ReleaseKeys())
		if err != nil {
			t.Fatal(err)
		}
		pinned := Pin{ReleaseKeyIDs: genesis.ReleaseKeyIDs, Threshold: int(genesis.ReleaseThreshold)}
		root, err := BuildTrustRoot(pinned, Publications{Genesis: genesis, Distribution: wire.DistributionPage{Policies: []wire.DistributionPolicy{policyB}}})
		if err != nil {
			t.Fatal(err)
		}
		policyA := genesis.DistributionPolicy.PolicyID
		t.Run("old committee remains current before policy B", func(t *testing.T) {
			at := time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC)
			v := NewVerifier(VerifierConfig{Root: root, Now: func() time.Time { return at }})
			if v.NeedsRecheck(Record{Settlement: settlement.StateSettled, SettledUnderPolicy: policyA}) {
				t.Fatal("record under policy A requested recheck while policy A is in force")
			}
		})
		t.Run("policy B requires settled evidence recheck", func(t *testing.T) {
			at := time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC)
			v := NewVerifier(VerifierConfig{Root: root, Now: func() time.Time { return at }})
			if !v.NeedsRecheck(Record{Settlement: settlement.StateSettled, SettledUnderPolicy: policyA}) {
				t.Fatal("record under policy A did not request recheck while policy B is in force")
			}
		})
	})
	t.Run("conflicting successions from one old key keep each new key off the delinquent line", func(t *testing.T) {
		oldKey := vfProviderSeed(32)
		newKey := vfProviderSeed(192)
		otherNewKey := ed25519.NewKeyFromSeed([]byte{128, 129, 130, 131, 132, 133, 134, 135, 136, 137, 138, 139, 140, 141, 142, 143, 144, 145, 146, 147, 148, 149, 150, 151, 152, 153, 154, 155, 156, 157, 158, 159})
		effective := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
		toNew, err := succession.New(oldKey, newKey, effective)
		if err != nil {
			t.Fatal(err)
		}
		toOther, err := succession.New(oldKey, otherNewKey, effective)
		if err != nil {
			t.Fatal(err)
		}
		ledger := NewLedger()
		ledger.vfAdd(settlement.DelinquencyRecord{ProviderLine: keys.DIDOf(oldKey), ReceiptID: "r1"})
		now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
		v := NewVerifier(VerifierConfig{Ledger: ledger, ProviderSuccessions: []wire.KeySuccession{toNew, toOther}, Now: func() time.Time { return now }})
		pending := Record{Settlement: settlement.StatePending, Receipt: wire.SubmissionReceipt{ProviderID: keys.DIDOf(newKey)}, RecheckBy: now.Add(30 * 24 * time.Hour)}
		if v.NeedsRecheck(pending) {
			t.Fatal("pending receipt from new key y matched the delinquent line of old key x")
		}
	})
	t.Run("one unconflicted succession puts the new key on the delinquent line", func(t *testing.T) {
		oldKey := vfProviderSeed(32)
		newKey := vfProviderSeed(192)
		effective := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
		toNew, err := succession.New(oldKey, newKey, effective)
		if err != nil {
			t.Fatal(err)
		}
		ledger := NewLedger()
		ledger.vfAdd(settlement.DelinquencyRecord{ProviderLine: keys.DIDOf(oldKey), ReceiptID: "r1"})
		now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
		v := NewVerifier(VerifierConfig{Ledger: ledger, ProviderSuccessions: []wire.KeySuccession{toNew}, Now: func() time.Time { return now }})
		pending := Record{Settlement: settlement.StatePending, Receipt: wire.SubmissionReceipt{ProviderID: keys.DIDOf(newKey)}, RecheckBy: now.Add(30 * 24 * time.Hour)}
		if !v.NeedsRecheck(pending) {
			t.Fatal("pending receipt from new key y did not match the delinquent line of old key x")
		}
	})
}

func vfSorted(values []string) []string {
	result := append([]string(nil), values...)
	sort.Strings(result)
	return result
}
