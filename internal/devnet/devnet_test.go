package devnet

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/distribution"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/governance"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

type expectedValues struct {
	Command                   string `json:"command"`
	Method                    string `json:"method"`
	Release0DID               string `json:"release_0_did"`
	Foundation0DID            string `json:"foundation_0_did"`
	Monitor0DID               string `json:"monitor_0_did"`
	FoundationEthereumAddress string `json:"foundation_ethereum_address"`
	BitcoinScheduleID         string `json:"bitcoin_schedule_id"`
}

func expected(t *testing.T) expectedValues {
	t.Helper()
	data, err := os.ReadFile("testdata/expected.json")
	if err != nil {
		t.Fatal(err)
	}
	var value expectedValues
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestDeterministicGenesisAndIndependentValues(t *testing.T) {
	t.Run("identical canonical manifest", func(t *testing.T) {
		first, err := canon.Encode(Genesis())
		if err != nil {
			t.Fatal(err)
		}
		second, err := canon.Encode(Genesis())
		if err != nil {
			t.Fatal(err)
		}
		if string(first) != string(second) {
			t.Fatal("Genesis returned different canonical bytes")
		}
	})
	t.Run("keys addresses and schedule match independent fixture", func(t *testing.T) {
		want := expected(t)
		seed := sha256.Sum256([]byte("foryou-dev/v1/" + Foundation0Label))
		derived, err := keys.NewKeyFromSeed(seed[:])
		if err != nil {
			t.Fatal(err)
		}
		if got := keys.DIDOf(FoundationKey()); got != keys.DIDOf(derived) {
			t.Fatalf("FoundationKey DID = %q; re-derived = %q", got, keys.DIDOf(derived))
		}
		if got := keys.DIDOf(privateKey(Release0Label)); got != want.Release0DID {
			t.Fatalf("release/0 DID = %q; want %q", got, want.Release0DID)
		}
		if got := keys.DIDOf(FoundationKey()); got != want.Foundation0DID {
			t.Fatalf("foundation/0 DID = %q; want %q", got, want.Foundation0DID)
		}
		if got := keys.DIDOf(MonitorKey(0)); got != want.Monitor0DID {
			t.Fatalf("monitor/0 DID = %q; want %q", got, want.Monitor0DID)
		}
		if got := Recipient(Foundation0Label, faux.EthereumMainnet); got != want.FoundationEthereumAddress {
			t.Fatalf("Foundation Ethereum recipient = %q; want %q", got, want.FoundationEthereumAddress)
		}
		if got := Genesis().FeeSchedules[0].ScheduleID; got != want.BitcoinScheduleID {
			t.Fatalf("Bitcoin schedule ID = %q; want %q", got, want.BitcoinScheduleID)
		}
	})
}

func TestGenesisGovernanceChecks(t *testing.T) {
	t.Run("manifest and both approved schedules", func(t *testing.T) {
		manifest := Genesis()
		set, err := governance.CheckGenesis(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := fee.CheckScheduleSet(manifest.FeeSchedules); err != nil {
			t.Fatal(err)
		}
		if err := fee.VerifyApproval(manifest.FeeSchedules[0], manifest.FeeScheduleApprovals[0], set.IDs, set.Threshold, nil); err != nil {
			t.Fatalf("Bitcoin approval: %v", err)
		}
		if err := fee.VerifyApproval(manifest.FeeSchedules[1], manifest.FeeScheduleApprovals[1], set.IDs, set.Threshold, nil); err != nil {
			t.Fatalf("Ethereum approval: %v", err)
		}
		if err := faux.ValidateRecipient(faux.BitcoinMainnet, manifest.FoundationKeyCertificates[0].Recipients.BitcoinMainnet); err != nil {
			t.Fatalf("Bitcoin recipient: %v", err)
		}
		if err := faux.ValidateRecipient(faux.EthereumMainnet, manifest.FoundationKeyCertificates[0].Recipients.EthereumMainnet); err != nil {
			t.Fatalf("Ethereum recipient: %v", err)
		}
	})
	t.Run("policy verifies against trust", func(t *testing.T) {
		manifest := Genesis()
		trust, err := Trust(time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC))
		if err != nil {
			t.Fatal(err)
		}
		if err := distribution.CheckPolicy(manifest.DistributionPolicy, nil, trust); err != nil {
			t.Fatal(err)
		}
	})
}

func TestGenesisFixedValues(t *testing.T) {
	t.Run("genesis and certificate timestamps", func(t *testing.T) {
		manifest := Genesis()
		if manifest.Release != "1.0" {
			t.Errorf("release = %q; want 1.0", manifest.Release)
		}
		if manifest.ReleaseThreshold != 4 {
			t.Errorf("release threshold = %d; want 4", manifest.ReleaseThreshold)
		}
		if manifest.SignedAt != "2026-07-01T00:00:00Z" {
			t.Errorf("signed_at = %q; want 2026-07-01T00:00:00Z", manifest.SignedAt)
		}
		certificate := manifest.FoundationKeyCertificates[0]
		if certificate.IssuedAt != "2026-07-01T00:00:00Z" {
			t.Errorf("genesis certificate issued_at = %q; want 2026-07-01T00:00:00Z", certificate.IssuedAt)
		}
		if certificate.ValidFrom != "2026-07-01T00:00:00Z" {
			t.Errorf("genesis certificate valid_from = %q; want 2026-07-01T00:00:00Z", certificate.ValidFrom)
		}
		if certificate.ValidUntil != "2027-07-02T00:00:00Z" {
			t.Errorf("genesis certificate valid_until = %q; want 2027-07-02T00:00:00Z", certificate.ValidUntil)
		}
	})
	t.Run("network schedules and approvals", func(t *testing.T) {
		manifest := Genesis()
		bitcoin := manifest.FeeSchedules[0]
		if bitcoin.Revision != 1 || bitcoin.ProfileMajor != 2 || bitcoin.ProfileMinor != 0 {
			t.Errorf("Bitcoin schedule version = revision %d, profile %d.%d; want revision 1, profile 2.0", bitcoin.Revision, bitcoin.ProfileMajor, bitcoin.ProfileMinor)
		}
		if bitcoin.EffectiveAt != "2026-10-01T00:00:00Z" || bitcoin.ExpiresAt != "2027-04-01T00:00:00Z" {
			t.Errorf("Bitcoin schedule dates = %q to %q; want 2026-10-01T00:00:00Z to 2027-04-01T00:00:00Z", bitcoin.EffectiveAt, bitcoin.ExpiresAt)
		}
		if bitcoin.BaseFeeBaseUnits != "0" || bitcoin.PerKiBFeeBaseUnits != "136" || bitcoin.Denomination != "millisatoshi" {
			t.Errorf("Bitcoin schedule fees = %q/%q %q; want 0/136 millisatoshi", bitcoin.BaseFeeBaseUnits, bitcoin.PerKiBFeeBaseUnits, bitcoin.Denomination)
		}
		ethereum := manifest.FeeSchedules[1]
		if ethereum.Revision != 1 || ethereum.ProfileMajor != 2 || ethereum.ProfileMinor != 0 {
			t.Errorf("Ethereum schedule version = revision %d, profile %d.%d; want revision 1, profile 2.0", ethereum.Revision, ethereum.ProfileMajor, ethereum.ProfileMinor)
		}
		if ethereum.EffectiveAt != "2026-10-01T00:00:00Z" || ethereum.ExpiresAt != "2027-04-01T00:00:00Z" {
			t.Errorf("Ethereum schedule dates = %q to %q; want 2026-10-01T00:00:00Z to 2027-04-01T00:00:00Z", ethereum.EffectiveAt, ethereum.ExpiresAt)
		}
		if ethereum.BaseFeeBaseUnits != "0" || ethereum.PerKiBFeeBaseUnits != "48900000000" || ethereum.Denomination != "wei" {
			t.Errorf("Ethereum schedule fees = %q/%q %q; want 0/48900000000 wei", ethereum.BaseFeeBaseUnits, ethereum.PerKiBFeeBaseUnits, ethereum.Denomination)
		}
		if manifest.FeeScheduleApprovals[0].ApprovedAt != "2026-07-01T00:00:00Z" || manifest.FeeScheduleApprovals[0].Drift {
			t.Errorf("Bitcoin approval = (%q, drift %t); want (2026-07-01T00:00:00Z, false)", manifest.FeeScheduleApprovals[0].ApprovedAt, manifest.FeeScheduleApprovals[0].Drift)
		}
		if manifest.FeeScheduleApprovals[1].ApprovedAt != "2026-07-01T00:00:00Z" || manifest.FeeScheduleApprovals[1].Drift {
			t.Errorf("Ethereum approval = (%q, drift %t); want (2026-07-01T00:00:00Z, false)", manifest.FeeScheduleApprovals[1].ApprovedAt, manifest.FeeScheduleApprovals[1].Drift)
		}
	})
	t.Run("distribution policy dates and values", func(t *testing.T) {
		policy := Genesis().DistributionPolicy
		if policy.Revision != 1 {
			t.Errorf("policy revision = %d; want 1", policy.Revision)
		}
		if policy.ApprovedAt != "2026-07-01T00:00:00Z" || policy.EffectiveAt != "2026-10-01T00:00:00Z" {
			t.Errorf("policy dates = %q to %q; want 2026-07-01T00:00:00Z to 2026-10-01T00:00:00Z", policy.ApprovedAt, policy.EffectiveAt)
		}
		if policy.VaultShareBP != 5000 || policy.MonitorShareBP != 500 || policy.MinReports != 3 || policy.MaxRetentionYears != 10 {
			t.Errorf("policy values = shares %d/%d, reports %d, retention %d; want 5000/500, 3, 10", policy.VaultShareBP, policy.MonitorShareBP, policy.MinReports, policy.MaxRetentionYears)
		}
	})
	t.Run("renewal timestamps", func(t *testing.T) {
		certificate := Certificates(time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC))[1]
		if certificate.IssuedAt != "2027-06-01T00:00:00Z" {
			t.Errorf("renewal issued_at = %q; want 2027-06-01T00:00:00Z", certificate.IssuedAt)
		}
		if certificate.ValidFrom != "2027-07-01T00:00:00Z" {
			t.Errorf("renewal valid_from = %q; want 2027-07-01T00:00:00Z", certificate.ValidFrom)
		}
		if certificate.ValidUntil != "2028-07-01T00:00:00Z" {
			t.Errorf("renewal valid_until = %q; want 2028-07-01T00:00:00Z", certificate.ValidUntil)
		}
	})
}

func TestGovernanceInForceAndCertificateRenewals(t *testing.T) {
	t.Run("genesis items are in force in November 2026", func(t *testing.T) {
		at := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		trust, err := Trust(at)
		if err != nil {
			t.Fatal(err)
		}
		if id, ok := trust.CurrentFoundationID(at); !ok || id != keys.DIDOf(FoundationKey()) {
			t.Fatalf("CurrentFoundationID = %q, %t", id, ok)
		}
		approved := FeeSchedules()
		schedules := []wire.FeeSchedule{approved[0].Schedule, approved[1].Schedule}
		inForce, err := fee.InForce(schedules, faux.BitcoinMainnet, at)
		if err != nil {
			t.Fatal(err)
		}
		if inForce.ScheduleID != schedules[0].ScheduleID {
			t.Fatalf("in-force schedule = %q; want %q", inForce.ScheduleID, schedules[0].ScheduleID)
		}
		policies := Policies()
		policy, ok := distribution.InForce(policies, at)
		if !ok || policy.PolicyID != policies[0].PolicyID {
			t.Fatalf("in-force policy ID = %q, %t; want %q", policy.PolicyID, ok, policies[0].PolicyID)
		}
	})
	t.Run("renewal is issued at the notice boundary", func(t *testing.T) {
		before := Certificates(time.Date(2027, 5, 31, 0, 0, 0, 0, time.UTC))
		if len(before) != 1 {
			t.Fatalf("certificates issued May 31 = %d; want 1", len(before))
		}
		atNotice := Certificates(time.Date(2027, 6, 1, 0, 0, 0, 0, time.UTC))
		if len(atNotice) != 2 {
			t.Fatalf("certificates issued June 1 = %d; want 2", len(atNotice))
		}
		set, err := governance.CheckGenesis(Genesis())
		if err != nil {
			t.Fatal(err)
		}
		if err := governance.CheckCertificate(atNotice[1], set, false); err != nil {
			t.Fatalf("renewal certificate: %v", err)
		}
		signedAt := time.Date(2027, 7, 1, 12, 0, 0, 0, time.UTC)
		receipt := signedReceipt(t, signedAt)
		trust, err := Trust(signedAt)
		if err != nil {
			t.Fatal(err)
		}
		if err := trust.VerifyFoundation(receipt, signedAt, "vault-receipt", nil); err != nil {
			t.Fatalf("receipt under genesis certificate: %v", err)
		}
	})
	t.Run("renewal keeps Foundation signatures valid beyond genesis expiry", func(t *testing.T) {
		signedAt := time.Date(2027, 7, 3, 0, 0, 0, 0, time.UTC)
		receipt := signedReceipt(t, signedAt)
		trust, err := Trust(signedAt)
		if err != nil {
			t.Fatal(err)
		}
		if err := trust.VerifyFoundation(receipt, signedAt, "vault-receipt", nil); err != nil {
			t.Fatalf("receipt with renewal: %v", err)
		}
		genesisOnly, err := governance.NewTrust(Genesis(), nil, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := genesisOnly.VerifyFoundation(receipt, signedAt, "vault-receipt", nil); err == nil {
			t.Fatal("accepted receipt after genesis certificate expiry without renewal")
		}
	})
}

func signedReceipt(t *testing.T, signedAt time.Time) wire.VaultReceipt {
	t.Helper()
	receipt := wire.VaultReceipt{FoundationID: keys.DIDOf(FoundationKey()), MessageID: "fy1:" + "a3b611487ed295e46340f4132f3281d8c32e0e8dc9546776be535594c2e3cfa9", StoredAt: wire.FormatTime(signedAt)}
	canonical, err := canon.EncodeWithout(receipt, "foundation_signature")
	if err != nil {
		t.Fatal(err)
	}
	receipt.FoundationSignature, err = keys.Sign(FoundationKey(), "vault-receipt", canonical)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func TestDevelopmentCalendar(t *testing.T) {
	t.Run("standard calendar for zero and one-day duration", func(t *testing.T) {
		zero, err := Calendar(0)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := zero.(distribution.CalendarQuarters); !ok {
			t.Fatalf("Calendar(0) = %T", zero)
		}
		day, err := Calendar(24 * time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := day.(distribution.CalendarQuarters); !ok {
			t.Fatalf("Calendar(24h) = %T", day)
		}
	})
	t.Run("dev quarter follows fixed 90-day formula", func(t *testing.T) {
		calendar, err := Calendar(30 * time.Second)
		if err != nil {
			t.Fatal(err)
		}
		start, end := calendar.Quarter(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
		if !start.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) || end.Sub(start) != 45*time.Minute {
			t.Fatalf("dev quarter = %s to %s; want 45 minutes from policy epoch", start, end)
		}
	})
	t.Run("reject durations below the issue floor", func(t *testing.T) {
		if _, err := Calendar(10 * time.Second); err == nil {
			t.Fatal("accepted a 10-second dev day")
		}
		if _, err := Calendar(-time.Second); err == nil {
			t.Fatal("accepted a negative dev day")
		}
	})
}
