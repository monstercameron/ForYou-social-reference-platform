package sdk

import (
	"testing"
	"time"

	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/wire"
)

func TestCheckAppliedUpdates(t *testing.T) {
	applied := []AppliedUpdate{{Sequence: 1, UpdateID: "fyku2:first"}}
	t.Run("a publication that omits an applied update is refused", func(t *testing.T) {
		err := CheckAppliedUpdates(applied, []wire.ReleaseKeySetUpdate{})
		if err == nil || err.Error() != "release key-set update 1, which this client applied, is missing from the publication" {
			t.Errorf("error = %v; want the missing applied update named", err)
		}
	})
	t.Run("a rival update at an applied sequence is refused", func(t *testing.T) {
		err := CheckAppliedUpdates(applied, []wire.ReleaseKeySetUpdate{{Sequence: 1, UpdateID: "fyku2:rival"}})
		if err == nil || err.Error() != "release key-set update 1 differs from the one this client applied" {
			t.Errorf("error = %v; want the rival update refused", err)
		}
	})
	t.Run("a later update after the applied one is accepted", func(t *testing.T) {
		published := []wire.ReleaseKeySetUpdate{{Sequence: 1, UpdateID: "fyku2:first"}, {Sequence: 2, UpdateID: "fyku2:second"}}
		if err := CheckAppliedUpdates(applied, published); err != nil {
			t.Errorf("error = %v; want the later update accepted", err)
		}
	})
}

func TestRepublishedGenesisObjectsAreNotRejected(t *testing.T) {
	genesis := devnet.Genesis()
	now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
	root, err := BuildTrustRoot(Pin{ReleaseKeyIDs: genesis.ReleaseKeyIDs, Threshold: int(genesis.ReleaseThreshold)}, Publications{
		Genesis:      genesis,
		Certificates: wire.FoundationCertificatesPage{Certificates: devnet.Certificates(now)},
		FeeSchedules: wire.FeeSchedulesPage{Schedules: devnet.FeeSchedules()},
		Distribution: wire.DistributionPage{Policies: devnet.Policies()},
		FetchedAt:    now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if rejected := root.Rejected(); len(rejected) != 0 {
		t.Errorf("rejected = %+v; a schedule or policy the genesis already approved is not a rival when the Foundation lists it again", rejected)
	}
}
