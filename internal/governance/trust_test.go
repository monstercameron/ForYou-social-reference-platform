package governance

import (
	"errors"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/wire"
)

func TestFoundationExample(t *testing.T) {
	example := governanceExample(t)
	genesis := decodeExample[wire.GenesisManifest](t, example, "genesis_manifest")
	certificate := genesis.FoundationKeyCertificates[0]
	head := decodeExample[wire.FoundationLogHead](t, example, "foundation_log_head")
	receipt := decodeExample[wire.VaultReceipt](t, example, "vault_receipt")
	t.Run("genesis certificate", func(t *testing.T) {
		set := KeySet{IDs: genesis.ReleaseKeyIDs, Threshold: int(genesis.ReleaseThreshold)}
		if err := CheckCertificate(certificate, set, true); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("signed log head", func(t *testing.T) {
		if err := VerifyLogHead(head); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("single-leaf proof root", func(t *testing.T) {
		hash, err := ObjectHash(receipt)
		if err != nil {
			t.Fatal(err)
		}
		root, err := RootFromProof(hash, wire.FoundationLogProof{Index: 0, Size: 1, AuditPath: []string{}})
		if err != nil {
			t.Fatal(err)
		}
		if root != head.Root {
			t.Fatalf("RootFromProof = %s; want %s", root, head.Root)
		}
	})
	t.Run("receipt signed by certified key", func(t *testing.T) {
		trust, err := NewTrust(genesis, nil, nil, nil, []wire.FoundationLogHead{head})
		if err != nil {
			t.Fatal(err)
		}
		signedAt, err := wire.ParseTime(receipt.StoredAt)
		if err != nil {
			t.Fatal(err)
		}
		if err := trust.VerifyFoundation(receipt, signedAt, "vault-receipt", nil); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("release approval verifies through Trust", func(t *testing.T) {
		trust, err := NewTrust(genesis, nil, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		approval := genesis.FeeScheduleApprovals[0]
		unsignedBytes, err := canon.EncodeWithout(approval, "release_signatures")
		if err != nil {
			t.Fatal(err)
		}
		unsigned, err := canon.Parse(unsignedBytes, canon.Limits{MaxDepth: 32})
		if err != nil {
			t.Fatal(err)
		}
		signedAt, err := wire.ParseTime(approval.ApprovedAt)
		if err != nil {
			t.Fatal(err)
		}
		if err := trust.VerifyRelease("fee-schedule-approval", unsigned, approval.ReleaseSignatures, signedAt, approval); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("certificate page may repeat genesis certificate", func(t *testing.T) {
		trust, err := NewTrust(genesis, nil, genesis.FoundationKeyCertificates, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if id, ok := trust.CurrentFoundationID(mustTime(t, "2027-01-01T00:00:00Z")); !ok || id != genesis.FoundationKeyCertificates[0].FoundationID {
			t.Fatalf("CurrentFoundationID = %q, %t; want the genesis Foundation key", id, ok)
		}
	})
	t.Run("release verifier rejects genesis and update labels", func(t *testing.T) {
		trust, err := NewTrust(genesis, nil, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		forbiddenTime := mustTime(t, genesis.SignedAt)
		if err := trust.VerifyRelease("genesis-manifest", map[string]any{}, nil, forbiddenTime, genesis); !errors.Is(err, ErrInvalid) {
			t.Fatalf("VerifyRelease genesis error = %v; want ErrInvalid", err)
		}
		if err := trust.VerifyRelease("release-key-set-update", map[string]any{}, nil, forbiddenTime, map[string]any{}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("VerifyRelease update error = %v; want ErrInvalid", err)
		}
	})
	t.Run("recipient remains eligible after certificate expiry", func(t *testing.T) {
		trust, err := NewTrust(genesis, nil, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		certificate := genesis.FoundationKeyCertificates[0]
		from := mustTime(t, certificate.ValidFrom)
		until := mustTime(t, certificate.ValidUntil)
		if trust.RecipientCounts(wire.BitcoinMainnet, certificate.Recipients.BitcoinMainnet, from.Add(-time.Second)) {
			t.Fatal("recipient counted before certificate validity")
		}
		if !trust.RecipientCounts(wire.BitcoinMainnet, certificate.Recipients.BitcoinMainnet, until.Add(time.Second)) {
			t.Fatal("recipient stopped counting after certificate expiry")
		}
	})
	t.Run("receipt signed inside a renewed certificate verifies", func(t *testing.T) {
		renewal := makeCertificate(t, certificate, mustTime(t, "2027-08-01T00:00:00Z"), mustTime(t, "2027-10-01T00:00:00Z"))
		trust, err := NewTrust(genesis, nil, []wire.FoundationKeyCertificate{renewal}, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		late := signedTestReceipt(t, receipt.MessageID, "2027-11-01T00:00:00Z")
		if err := trust.VerifyFoundation(late, mustTime(t, late.StoredAt), "vault-receipt", nil); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("receipt signed after every certificate's valid_until rejected", func(t *testing.T) {
		trust, err := NewTrust(genesis, nil, nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		late := signedTestReceipt(t, receipt.MessageID, "2027-11-01T00:00:00Z")
		if err := trust.VerifyFoundation(late, mustTime(t, late.StoredAt), "vault-receipt", nil); !errors.Is(err, ErrInvalid) {
			t.Fatalf("VerifyFoundation error = %v; want ErrInvalid", err)
		}
	})
}

func TestTrustRejectsNoncanonicalSignedTime(t *testing.T) {
	example := governanceExample(t)
	genesis := decodeExample[wire.GenesisManifest](t, example, "genesis_manifest")
	trust, err := NewTrust(genesis, nil, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	signedAt := mustTime(t, "2027-01-01T00:00:00Z").Add(time.Nanosecond)
	if err := trust.VerifyRelease("fee-schedule-approval", map[string]any{}, nil, signedAt, map[string]any{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("VerifyRelease error = %v; want ErrInvalid", err)
	}
}

func mustTime(t *testing.T, value string) time.Time {
	t.Helper()
	parsed, err := wire.ParseTime(value)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}
