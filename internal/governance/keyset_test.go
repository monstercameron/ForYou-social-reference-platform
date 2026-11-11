package governance

import (
	"crypto/ed25519"
	"errors"
	"slices"
	"sort"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

func TestTimelineTransitions(t *testing.T) {
	release := syntheticReleaseKeys(t)
	monitors := syntheticMonitorKeys()
	genesis := KeySet{IDs: sortedIDs(keys.DIDOf(release[0]), keys.DIDOf(release[1]), keys.DIDOf(release[2]), keys.DIDOf(release[3]), keys.DIDOf(release[4])), Threshold: 4}
	newKeys := []ed25519.PrivateKey{release[1], release[2], release[3], release[4], monitors[0], monitors[1]}
	newIDs := sortedIDs(keys.DIDOf(release[1]), keys.DIDOf(release[2]), keys.DIDOf(release[3]), keys.DIDOf(release[4]), keys.DIDOf(monitors[0]), keys.DIDOf(monitors[1]))
	update := signedTestUpdate(t, 1, nil, newIDs, 4, []string{}, "2027-06-01T00:00:00Z", release[:4])
	expectRejected := func(t *testing.T, updates []wire.ReleaseKeySetUpdate) {
		t.Helper()
		if _, err := NewTimeline(genesis, updates); !errors.Is(err, ErrInvalid) {
			t.Fatalf("NewTimeline error = %v; want ErrInvalid", err)
		}
	}
	t.Run("set changes at effective time", func(t *testing.T) {
		timeline, err := NewTimeline(genesis, []wire.ReleaseKeySetUpdate{update})
		if err != nil {
			t.Fatal(err)
		}
		before, err := timeline.SetAt(mustTime(t, "2027-05-31T23:59:59Z"), nil)
		if err != nil || before.Threshold != 4 || !slices.Equal(before.IDs, genesis.IDs) {
			t.Fatalf("SetAt one second before = %#v, %v; want the genesis set", before, err)
		}
		at, err := timeline.SetAt(mustTime(t, "2027-06-01T00:00:00Z"), nil)
		if err != nil || at.Threshold != 4 || !slices.Equal(at.IDs, newIDs) {
			t.Fatalf("SetAt effective time = %#v, %v; want the six-key set", at, err)
		}
	})
	t.Run("identical duplicate updates are deduplicated", func(t *testing.T) {
		if _, err := NewTimeline(genesis, []wire.ReleaseKeySetUpdate{update, update}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("non-release signers rejected", func(t *testing.T) {
		outsiders := []ed25519.PrivateKey{syntheticFoundationKey(), syntheticProviderBKey(), syntheticProviderKey(), syntheticProviderNextKey()}
		expectRejected(t, []wire.ReleaseKeySetUpdate{signedTestUpdate(t, 1, nil, newIDs, 4, []string{}, "2027-06-01T00:00:00Z", outsiders)})
	})
	t.Run("skipped sequence rejected", func(t *testing.T) {
		expectRejected(t, []wire.ReleaseKeySetUpdate{signedTestUpdate(t, 2, nil, newIDs, 4, []string{}, "2027-06-01T00:00:00Z", release[:4])})
	})
	t.Run("threshold below two-thirds rejected", func(t *testing.T) {
		expectRejected(t, []wire.ReleaseKeySetUpdate{signedTestUpdate(t, 1, nil, newIDs, 3, []string{}, "2027-06-01T00:00:00Z", release[:4])})
	})
	t.Run("fewer than five keys rejected", func(t *testing.T) {
		expectRejected(t, []wire.ReleaseKeySetUpdate{signedTestUpdate(t, 1, nil, newIDs[:4], 4, []string{}, "2027-06-01T00:00:00Z", release[:4])})
	})
	t.Run("chained second update signed by the new set accepted", func(t *testing.T) {
		second := signedTestUpdate(t, 2, &update.UpdateID, genesis.IDs, 4, []string{}, "2027-09-01T00:00:00Z", newKeys[:4])
		if _, err := NewTimeline(genesis, []wire.ReleaseKeySetUpdate{update, second}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("unchained second update rejected", func(t *testing.T) {
		unlinked := signedTestUpdate(t, 2, nil, genesis.IDs, 4, []string{}, "2027-09-01T00:00:00Z", newKeys[:4])
		expectRejected(t, []wire.ReleaseKeySetUpdate{update, unlinked})
	})
	t.Run("second update signed by the replaced set rejected", func(t *testing.T) {
		stale := signedTestUpdate(t, 2, &update.UpdateID, genesis.IDs, 4, []string{}, "2027-09-01T00:00:00Z", release[:4])
		expectRejected(t, []wire.ReleaseKeySetUpdate{update, stale})
	})
	t.Run("rival updates at one sequence fork the root of trust", func(t *testing.T) {
		rival := signedTestUpdate(t, 1, nil, sortedIDs(keys.DIDOf(release[0]), keys.DIDOf(release[1]), keys.DIDOf(release[2]), keys.DIDOf(release[3]), keys.DIDOf(monitors[0])), 4, []string{}, "2027-06-01T00:00:00Z", release[:4])
		if _, err := NewTimeline(genesis, []wire.ReleaseKeySetUpdate{update, rival}); !errors.Is(err, ErrForkedRootOfTrust) {
			t.Fatalf("NewTimeline error = %v; want ErrForkedRootOfTrust", err)
		}
	})
}

func sortedIDs(ids ...string) []string {
	slices.Sort(ids)
	return ids
}

func TestObjectsSignedUnderEarlierSetNeedCarryOver(t *testing.T) {
	example := governanceExample(t)
	genesisManifest := decodeExample[wire.GenesisManifest](t, example, "genesis_manifest")
	oldKeys := syntheticReleaseKeys(t)
	newIDs := []string{keys.DIDOf(oldKeys[1]), keys.DIDOf(oldKeys[2]), keys.DIDOf(oldKeys[3]), keys.DIDOf(oldKeys[4]), keys.DIDOf(syntheticMonitorKeys()[0])}
	sort.Strings(newIDs)
	certificate := genesisManifest.FoundationKeyCertificates[0]
	btcApproval := genesisManifest.FeeScheduleApprovals[0]
	ethApproval := genesisManifest.FeeScheduleApprovals[1]
	policy := genesisManifest.DistributionPolicy
	certificateHash, err := ObjectHash(certificate)
	if err != nil {
		t.Fatal(err)
	}
	btcHash, err := ObjectHash(btcApproval)
	if err != nil {
		t.Fatal(err)
	}
	policyHash, err := ObjectHash(policy)
	if err != nil {
		t.Fatal(err)
	}
	prior := []string{certificateHash, btcHash, policyHash}
	sort.Strings(prior)
	update := signedTestUpdate(t, 1, nil, newIDs, 4, prior, "2027-01-01T00:00:00Z", oldKeys[:4])
	trust, err := NewTrust(genesisManifest, []wire.ReleaseKeySetUpdate{update}, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	verify := func(t *testing.T, label string, unsigned any, signatures []wire.ReleaseSignature, signedAt time.Time, signed any) {
		t.Helper()
		if err := trust.VerifyRelease(label, unsigned, signatures, signedAt, signed); err != nil {
			t.Fatal(err)
		}
	}
	t.Run("carried certificate remains valid", func(t *testing.T) {
		unsigned := unsignedValue(t, certificate, "release_signatures")
		verify(t, "foundation-key-certificate", unsigned, certificate.ReleaseSignatures, mustTime(t, certificate.IssuedAt), certificate)
	})
	t.Run("carried BTC approval remains valid", func(t *testing.T) {
		unsigned := unsignedValue(t, btcApproval, "release_signatures")
		verify(t, "fee-schedule-approval", unsigned, btcApproval.ReleaseSignatures, mustTime(t, btcApproval.ApprovedAt), btcApproval)
	})
	t.Run("carried distribution policy remains valid", func(t *testing.T) {
		unsigned := unsignedValue(t, policy, "release_signatures")
		verify(t, "distribution-policy", unsigned, policy.ReleaseSignatures, mustTime(t, policy.ApprovedAt), policy)
	})
	t.Run("uncarried ETH approval is rejected", func(t *testing.T) {
		unsigned := unsignedValue(t, ethApproval, "release_signatures")
		if err := trust.VerifyRelease("fee-schedule-approval", unsigned, ethApproval.ReleaseSignatures, mustTime(t, ethApproval.ApprovedAt), ethApproval); !errors.Is(err, ErrInvalid) {
			t.Fatalf("VerifyRelease uncarried approval error = %v; want ErrInvalid", err)
		}
	})
	t.Run("retired keys cannot issue a later certificate", func(t *testing.T) {
		hostile := makeCertificate(t, certificate, mustTime(t, "2027-01-02T00:00:00Z"), mustTime(t, "2027-02-01T00:00:00Z"))
		hostile = resignCertificate(t, hostile, oldKeys[:4])
		if _, err := NewTrust(genesisManifest, []wire.ReleaseKeySetUpdate{update}, []wire.FoundationKeyCertificate{hostile}, nil, nil); !errors.Is(err, ErrInvalid) {
			t.Fatalf("NewTrust retired-key certificate error = %v; want ErrInvalid", err)
		}
	})
}

func signedTestUpdate(t *testing.T, sequence int64, previous *string, ids []string, threshold int64, prior []string, effectiveAt string, signers []ed25519.PrivateKey) wire.ReleaseKeySetUpdate {
	t.Helper()
	update := wire.ReleaseKeySetUpdate{Sequence: sequence, PreviousUpdateID: previous, ReleaseKeyIDs: ids, ReleaseThreshold: threshold, PriorObjects: prior, EffectiveAt: effectiveAt}
	var err error
	update.UpdateID, err = UpdateID(update)
	if err != nil {
		t.Fatal(err)
	}
	unsigned := unsignedValue(t, update, "release_signatures")
	update.ReleaseSignatures, err = SignRelease("release-key-set-update", unsigned, signers)
	if err != nil {
		t.Fatal(err)
	}
	return update
}

func unsignedValue(t *testing.T, object any, field string) any {
	t.Helper()
	encoded, err := canon.EncodeWithout(object, field)
	if err != nil {
		t.Fatal(err)
	}
	value, err := canon.Parse(encoded, canon.Limits{MaxDepth: 32})
	if err != nil {
		t.Fatal(err)
	}
	return value
}
