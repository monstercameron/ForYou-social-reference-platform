package sdk

import (
	"crypto/ed25519"
	"encoding/json"
	"os"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/governance"
	"foryou.local/foryou/internal/wire"
)

func vfExample(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	b, err := os.ReadFile("testdata/vf/settlement.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var example map[string]json.RawMessage
	if err := json.Unmarshal(b, &example); err != nil {
		t.Fatal(err)
	}
	return example
}

func vfTrust(t *testing.T, now time.Time) *TrustRoot {
	t.Helper()
	example := vfExample(t)
	var g wire.GenesisManifest
	if err := json.Unmarshal(example["genesis_manifest"], &g); err != nil {
		t.Fatal(err)
	}
	root, err := BuildTrustRoot(Pin{ReleaseKeyIDs: g.ReleaseKeyIDs, Threshold: int(g.ReleaseThreshold)}, Publications{Genesis: g, FetchedAt: now, Endpoint: "https://foundation.example"})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestBuildTrustRootPinsGenesis(t *testing.T) {
	t.Run("exact manifest pin builds", func(t *testing.T) {
		root := vfTrust(t, time.Date(2027, 1, 7, 0, 0, 0, 0, time.UTC))
		if got, ok := root.ScheduleInForce("bitcoin-mainnet", time.Date(2027, 1, 5, 0, 0, 0, 0, time.UTC)); !ok || got.PerKiBFeeBaseUnits != "136" {
			t.Fatalf("schedule = %#v, %v", got, ok)
		}
		if _, ok := root.ScheduleInForce("bitcoin-mainnet", time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC)); ok {
			t.Fatal("schedule unexpectedly in force")
		}
		if got := root.Committee(time.Date(2027, 1, 7, 0, 0, 0, 0, time.UTC)); len(got.Current) != 5 || len(got.Previous) != 0 || !got.ChangedAt.IsZero() {
			t.Fatalf("committee = %#v", got)
		}
	})
	t.Run("different key pin is rejected", func(t *testing.T) {
		example := vfExample(t)
		var g wire.GenesisManifest
		if err := json.Unmarshal(example["genesis_manifest"], &g); err != nil {
			t.Fatal(err)
		}
		ids := append([]string(nil), g.ReleaseKeyIDs...)
		ids[0] = "different"
		if _, err := BuildTrustRoot(Pin{ReleaseKeyIDs: ids, Threshold: 4}, Publications{Genesis: g}); err == nil {
			t.Fatal("accepted a different release key pin")
		}
	})
	t.Run("different threshold is rejected", func(t *testing.T) {
		example := vfExample(t)
		var g wire.GenesisManifest
		if err := json.Unmarshal(example["genesis_manifest"], &g); err != nil {
			t.Fatal(err)
		}
		if _, err := BuildTrustRoot(Pin{ReleaseKeyIDs: g.ReleaseKeyIDs, Threshold: 3}, Publications{Genesis: g}); err == nil {
			t.Fatal("accepted a different release threshold")
		}
	})
}

func TestCommitteeNilTrustRoot(t *testing.T) {
	t.Run("nil trust root has no committee", func(t *testing.T) {
		var root *TrustRoot
		if got := root.Committee(time.Date(2027, 1, 7, 0, 0, 0, 0, time.UTC)); len(got.Current) != 0 || got.PolicyID != "" || len(got.Previous) != 0 || !got.ChangedAt.IsZero() {
			t.Fatalf("committee = %#v", got)
		}
	})
}

func TestBuildTrustRootRejectsFeeSchedules(t *testing.T) {
	t.Run("revision within bounds approved by the release keys is in force", func(t *testing.T) {
		genesis := devnet.Genesis()
		schedule, approval := vfRevisionTwo(t, genesis, "200", devnet.ReleaseKeys())
		root, err := BuildTrustRoot(Pin{ReleaseKeyIDs: genesis.ReleaseKeyIDs, Threshold: int(genesis.ReleaseThreshold)}, Publications{Genesis: genesis, FeeSchedules: wire.FeeSchedulesPage{Schedules: []wire.ApprovedFeeSchedule{{Schedule: schedule, Approval: approval}}}})
		if err != nil {
			t.Fatal(err)
		}
		got, ok := root.ScheduleInForce("bitcoin-mainnet", time.Date(2027, 7, 1, 0, 0, 0, 0, time.UTC))
		if !ok || got.ScheduleID != schedule.ScheduleID || got.PerKiBFeeBaseUnits != "200" || len(root.Rejected()) != 0 {
			t.Fatalf("schedule = %#v, %v; rejected = %#v", got, ok, root.Rejected())
		}
	})
	t.Run("revision outside fee bounds is listed as rejected", func(t *testing.T) {
		genesis := devnet.Genesis()
		schedule, approval := vfRevisionTwo(t, genesis, "273", devnet.ReleaseKeys())
		root, err := BuildTrustRoot(Pin{ReleaseKeyIDs: genesis.ReleaseKeyIDs, Threshold: int(genesis.ReleaseThreshold)}, Publications{Genesis: genesis, FeeSchedules: wire.FeeSchedulesPage{Schedules: []wire.ApprovedFeeSchedule{{Schedule: schedule, Approval: approval}}}})
		if err != nil {
			t.Fatal(err)
		}
		rejected := root.Rejected()
		if len(rejected) != 1 || rejected[0].ID != schedule.ScheduleID {
			t.Fatalf("rejected = %#v, want schedule %q", rejected, schedule.ScheduleID)
		}
	})
	t.Run("revision with only three release signatures is rejected", func(t *testing.T) {
		genesis := devnet.Genesis()
		schedule, approval := vfRevisionTwo(t, genesis, "200", devnet.ReleaseKeys()[:3])
		root, err := BuildTrustRoot(Pin{ReleaseKeyIDs: genesis.ReleaseKeyIDs, Threshold: int(genesis.ReleaseThreshold)}, Publications{Genesis: genesis, FeeSchedules: wire.FeeSchedulesPage{Schedules: []wire.ApprovedFeeSchedule{{Schedule: schedule, Approval: approval}}}})
		if err != nil {
			t.Fatal(err)
		}
		rejected := root.Rejected()
		if len(rejected) != 1 || rejected[0].ID != schedule.ScheduleID {
			t.Fatalf("rejected = %#v, want schedule %q", rejected, schedule.ScheduleID)
		}
	})
}

func vfRevisionTwo(t *testing.T, genesis wire.GenesisManifest, rate string, signers []ed25519.PrivateKey) (wire.FeeSchedule, wire.FeeScheduleApproval) {
	t.Helper()
	schedule := genesis.FeeSchedules[0]
	schedule.ScheduleID = ""
	schedule.Revision = 2
	schedule.PerKiBFeeBaseUnits = rate
	schedule.EffectiveAt = "2027-07-01T00:00:00Z"
	schedule.ExpiresAt = "2028-01-01T00:00:00Z"
	var err error
	schedule.ScheduleID, err = fee.ScheduleID(schedule)
	if err != nil {
		t.Fatal(err)
	}
	approval := wire.FeeScheduleApproval{ScheduleID: schedule.ScheduleID, ApprovedAt: "2027-04-01T00:00:00Z"}
	encoded, err := canon.EncodeWithout(approval, "release_signatures")
	if err != nil {
		t.Fatal(err)
	}
	unsigned, err := canon.Parse(encoded, canon.Limits{MaxDepth: 32})
	if err != nil {
		t.Fatal(err)
	}
	approval.ReleaseSignatures, err = governance.SignRelease("fee-schedule-approval", unsigned, signers)
	if err != nil {
		t.Fatal(err)
	}
	return schedule, approval
}
