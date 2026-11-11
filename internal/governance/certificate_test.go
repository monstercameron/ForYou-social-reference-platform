package governance

import (
	"crypto/ed25519"
	"errors"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/wire"
)

func TestCertificateRules(t *testing.T) {
	example := governanceExample(t)
	genesis := decodeExample[wire.GenesisManifest](t, example, "genesis_manifest")
	certificate := genesis.FoundationKeyCertificates[0]
	set := KeySet{IDs: genesis.ReleaseKeyIDs, Threshold: int(genesis.ReleaseThreshold)}
	t.Run("three release signatures are below threshold", func(t *testing.T) {
		changed := certificate
		unsignedBytes, err := canon.EncodeWithout(changed, "release_signatures")
		if err != nil {
			t.Fatal(err)
		}
		unsigned, err := canon.Parse(unsignedBytes, canon.Limits{MaxDepth: 32})
		if err != nil {
			t.Fatal(err)
		}
		changed.ReleaseSignatures, err = SignRelease("foundation-key-certificate", unsigned, syntheticReleaseKeys(t)[:3])
		if err != nil {
			t.Fatal(err)
		}
		if err := CheckCertificate(changed, set, true); !errors.Is(err, ErrInvalid) {
			t.Fatalf("CheckCertificate three-signature error = %v; want ErrInvalid", err)
		}
	})
	t.Run("two-year validity is rejected", func(t *testing.T) {
		changed := certificate
		from, err := wire.ParseTime(changed.ValidFrom)
		if err != nil {
			t.Fatal(err)
		}
		changed.ValidUntil = wire.FormatTime(from.Add(2 * 365 * 24 * time.Hour))
		changed = resignCertificate(t, changed, syntheticReleaseKeys(t)[:4])
		if err := CheckCertificate(changed, set, true); !errors.Is(err, ErrInvalid) {
			t.Fatalf("CheckCertificate two-year error = %v; want ErrInvalid", err)
		}
	})
	t.Run("repeated signature entries are rejected", func(t *testing.T) {
		changed := certificate
		entry := certificate.ReleaseSignatures[0]
		changed.ReleaseSignatures = []wire.ReleaseSignature{entry, entry, entry, entry}
		if err := CheckCertificate(changed, set, true); !errors.Is(err, ErrInvalid) {
			t.Fatalf("CheckCertificate repeated signature error = %v; want ErrInvalid", err)
		}
	})
	t.Run("four non-release signers do not meet threshold", func(t *testing.T) {
		changed := resignCertificate(t, certificate, []ed25519.PrivateKey{syntheticFoundationKey(), syntheticProviderBKey(), syntheticProviderKey(), syntheticProviderNextKey()})
		if err := CheckCertificate(changed, set, true); !errors.Is(err, ErrInvalid) {
			t.Fatalf("CheckCertificate non-release signer error = %v; want ErrInvalid", err)
		}
	})
	t.Run("changed recipient requires a fresh valid release signature", func(t *testing.T) {
		changed := certificate
		changed.Recipients.BitcoinMainnet = "bc1qchanged"
		if err := CheckCertificate(changed, set, true); !errors.Is(err, ErrInvalid) {
			t.Fatalf("CheckCertificate changed recipient error = %v; want ErrInvalid", err)
		}
	})
	t.Run("later certificate needs thirty days notice", func(t *testing.T) {
		issuedAt := mustTime(t, "2027-01-01T00:00:00Z")
		oneDay := makeCertificate(t, certificate, issuedAt, issuedAt.Add(24*time.Hour))
		if _, err := NewTrust(genesis, nil, []wire.FoundationKeyCertificate{oneDay}, nil, nil); !errors.Is(err, ErrInvalid) {
			t.Fatalf("NewTrust one-day notice error = %v; want ErrInvalid", err)
		}
		thirtyDays := makeCertificate(t, certificate, issuedAt, issuedAt.Add(30*24*time.Hour))
		if _, err := NewTrust(genesis, nil, []wire.FoundationKeyCertificate{thirtyDays}, nil, nil); err != nil {
			t.Fatalf("NewTrust thirty-day notice: %v", err)
		}
	})
}

func resignCertificate(t *testing.T, certificate wire.FoundationKeyCertificate, signers []ed25519.PrivateKey) wire.FoundationKeyCertificate {
	t.Helper()
	var err error
	certificate.CertificateID, err = CertificateID(certificate)
	if err != nil {
		t.Fatal(err)
	}
	unsignedBytes, err := canon.EncodeWithout(certificate, "release_signatures")
	if err != nil {
		t.Fatal(err)
	}
	unsigned, err := canon.Parse(unsignedBytes, canon.Limits{MaxDepth: 32})
	if err != nil {
		t.Fatal(err)
	}
	certificate.ReleaseSignatures, err = SignRelease("foundation-key-certificate", unsigned, signers)
	if err != nil {
		t.Fatal(err)
	}
	return certificate
}

func makeCertificate(t *testing.T, base wire.FoundationKeyCertificate, issuedAt, validFrom time.Time) wire.FoundationKeyCertificate {
	t.Helper()
	certificate := base
	certificate.IssuedAt = wire.FormatTime(issuedAt)
	certificate.ValidFrom = wire.FormatTime(validFrom)
	certificate.ValidUntil = wire.FormatTime(validFrom.Add(365 * 24 * time.Hour))
	return resignCertificate(t, certificate, syntheticReleaseKeys(t)[:4])
}
