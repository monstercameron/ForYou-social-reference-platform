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

func TestBuildTrustRootPairsFeeSchedules(t *testing.T) {
	t.Run("devnet genesis schedules are both in force", func(t *testing.T) {
		genesis := devnet.Genesis()
		root := vfBuildWithSchedules(t, genesis)
		at := time.Date(2027, 1, 7, 0, 0, 0, 0, time.UTC)
		bitcoin, okBitcoin := root.ScheduleInForce("bitcoin-mainnet", at)
		ethereum, okEthereum := root.ScheduleInForce("ethereum-mainnet", at)
		if !okBitcoin || !okEthereum || bitcoin.PerKiBFeeBaseUnits != "136" || ethereum.PerKiBFeeBaseUnits != "48900000000" || len(root.Rejected()) != 0 {
			t.Fatalf("bitcoin = %#v, %v; ethereum = %#v, %v; rejected = %#v", bitcoin, okBitcoin, ethereum, okEthereum, root.Rejected())
		}
	})
	t.Run("revision pair with one activation instant is in force", func(t *testing.T) {
		genesis := devnet.Genesis()
		bitcoin, bitcoinApproval := vfRevisionTwo(t, genesis, 0, "200", "2027-07-01T00:00:00Z", devnet.ReleaseKeys())
		ethereum, ethereumApproval := vfRevisionTwo(t, genesis, 1, "60000000000", "2027-07-01T00:00:00Z", devnet.ReleaseKeys())
		root := vfBuildWithSchedules(t, genesis,
			wire.ApprovedFeeSchedule{Schedule: bitcoin, Approval: bitcoinApproval},
			wire.ApprovedFeeSchedule{Schedule: ethereum, Approval: ethereumApproval},
		)
		at := time.Date(2027, 7, 1, 0, 0, 0, 0, time.UTC)
		gotBitcoin, okBitcoin := root.ScheduleInForce("bitcoin-mainnet", at)
		gotEthereum, okEthereum := root.ScheduleInForce("ethereum-mainnet", at)
		if !okBitcoin || !okEthereum || gotBitcoin.ScheduleID != bitcoin.ScheduleID || gotEthereum.ScheduleID != ethereum.ScheduleID || len(root.Rejected()) != 0 {
			t.Fatalf("bitcoin = %#v, %v; ethereum = %#v, %v; rejected = %#v", gotBitcoin, okBitcoin, gotEthereum, okEthereum, root.Rejected())
		}
	})
	t.Run("revision for one network only is rejected and counted", func(t *testing.T) {
		genesis := devnet.Genesis()
		bitcoin, bitcoinApproval := vfRevisionTwo(t, genesis, 0, "200", "2027-07-01T00:00:00Z", devnet.ReleaseKeys())
		root := vfBuildWithSchedules(t, genesis, wire.ApprovedFeeSchedule{Schedule: bitcoin, Approval: bitcoinApproval})
		rejected := root.Rejected()
		if len(rejected) != 1 || rejected[0].ID != bitcoin.ScheduleID {
			t.Fatalf("rejected = %#v, want schedule %q", rejected, bitcoin.ScheduleID)
		}
		if got, ok := root.ScheduleInForce("bitcoin-mainnet", time.Date(2027, 7, 1, 0, 0, 0, 0, time.UTC)); !ok || got.PerKiBFeeBaseUnits != "136" {
			t.Fatalf("schedule = %#v, %v; want the genesis revision still in force", got, ok)
		}
	})
	t.Run("revision pair with different activation instants is rejected", func(t *testing.T) {
		genesis := devnet.Genesis()
		bitcoin, bitcoinApproval := vfRevisionTwo(t, genesis, 0, "200", "2027-07-01T00:00:00Z", devnet.ReleaseKeys())
		ethereum, ethereumApproval := vfRevisionTwo(t, genesis, 1, "60000000000", "2027-07-02T00:00:00Z", devnet.ReleaseKeys())
		root := vfBuildWithSchedules(t, genesis,
			wire.ApprovedFeeSchedule{Schedule: bitcoin, Approval: bitcoinApproval},
			wire.ApprovedFeeSchedule{Schedule: ethereum, Approval: ethereumApproval},
		)
		rejected := root.Rejected()
		if len(rejected) != 2 || rejected[0].ID != bitcoin.ScheduleID || rejected[1].ID != ethereum.ScheduleID {
			t.Fatalf("rejected = %#v, want schedules %q and %q", rejected, bitcoin.ScheduleID, ethereum.ScheduleID)
		}
		if got, ok := root.ScheduleInForce("bitcoin-mainnet", time.Date(2027, 7, 3, 0, 0, 0, 0, time.UTC)); !ok || got.PerKiBFeeBaseUnits != "136" {
			t.Fatalf("schedule = %#v, %v; want the genesis revision still in force", got, ok)
		}
	})
	t.Run("later revision without a partner is rejected and the paired revision stays in force", func(t *testing.T) {
		genesis := devnet.Genesis()
		bitcoin, bitcoinApproval := vfRevisionTwo(t, genesis, 0, "200", "2027-07-01T00:00:00Z", devnet.ReleaseKeys())
		ethereum, ethereumApproval := vfRevisionTwo(t, genesis, 1, "60000000000", "2027-07-01T00:00:00Z", devnet.ReleaseKeys())
		next, nextApproval := vfRevision(t, bitcoin, 3, "250", "2027-10-01T00:00:00Z", "2027-07-01T00:00:00Z", devnet.ReleaseKeys())
		root := vfBuildWithSchedules(t, genesis,
			wire.ApprovedFeeSchedule{Schedule: bitcoin, Approval: bitcoinApproval},
			wire.ApprovedFeeSchedule{Schedule: ethereum, Approval: ethereumApproval},
			wire.ApprovedFeeSchedule{Schedule: next, Approval: nextApproval},
		)
		rejected := root.Rejected()
		if len(rejected) != 1 || rejected[0].ID != next.ScheduleID {
			t.Fatalf("rejected = %#v, want schedule %q", rejected, next.ScheduleID)
		}
		at := time.Date(2027, 10, 2, 0, 0, 0, 0, time.UTC)
		gotBitcoin, okBitcoin := root.ScheduleInForce("bitcoin-mainnet", at)
		gotEthereum, okEthereum := root.ScheduleInForce("ethereum-mainnet", at)
		if !okBitcoin || !okEthereum || gotBitcoin.ScheduleID != bitcoin.ScheduleID || gotEthereum.ScheduleID != ethereum.ScheduleID {
			t.Fatalf("bitcoin = %#v, %v; ethereum = %#v, %v; want revision 2 of both networks", gotBitcoin, okBitcoin, gotEthereum, okEthereum)
		}
	})
	t.Run("revision whose predecessor was rejected is rejected with its partner", func(t *testing.T) {
		genesis := devnet.Genesis()
		bitcoin, bitcoinApproval := vfRevisionTwo(t, genesis, 0, "200", "2027-07-01T00:00:00Z", devnet.ReleaseKeys())
		next, nextApproval := vfRevision(t, bitcoin, 3, "250", "2027-10-01T00:00:00Z", "2027-07-01T00:00:00Z", devnet.ReleaseKeys())
		ethereum, ethereumApproval := vfRevisionTwo(t, genesis, 1, "60000000000", "2027-10-01T00:00:00Z", devnet.ReleaseKeys())
		root := vfBuildWithSchedules(t, genesis,
			wire.ApprovedFeeSchedule{Schedule: bitcoin, Approval: bitcoinApproval},
			wire.ApprovedFeeSchedule{Schedule: next, Approval: nextApproval},
			wire.ApprovedFeeSchedule{Schedule: ethereum, Approval: ethereumApproval},
		)
		rejected := root.Rejected()
		if len(rejected) != 3 || rejected[0].ID != bitcoin.ScheduleID || rejected[1].ID != next.ScheduleID || rejected[2].ID != ethereum.ScheduleID {
			t.Fatalf("rejected = %#v, want schedules %q, %q and %q", rejected, bitcoin.ScheduleID, next.ScheduleID, ethereum.ScheduleID)
		}
		at := time.Date(2027, 10, 2, 0, 0, 0, 0, time.UTC)
		gotBitcoin, okBitcoin := root.ScheduleInForce("bitcoin-mainnet", at)
		gotEthereum, okEthereum := root.ScheduleInForce("ethereum-mainnet", at)
		if !okBitcoin || !okEthereum || gotBitcoin.PerKiBFeeBaseUnits != "136" || gotEthereum.PerKiBFeeBaseUnits != "48900000000" {
			t.Fatalf("bitcoin = %#v, %v; ethereum = %#v, %v; want the genesis revisions in force", gotBitcoin, okBitcoin, gotEthereum, okEthereum)
		}
	})
}

func TestBuildTrustRootRejectsFeeSchedules(t *testing.T) {
	t.Run("revision outside fee bounds is listed as rejected", func(t *testing.T) {
		genesis := devnet.Genesis()
		bitcoin, bitcoinApproval := vfRevisionTwo(t, genesis, 0, "273", "2027-07-01T00:00:00Z", devnet.ReleaseKeys())
		ethereum, ethereumApproval := vfRevisionTwo(t, genesis, 1, "48900000000", "2027-07-01T00:00:00Z", devnet.ReleaseKeys())
		root := vfBuildWithSchedules(t, genesis,
			wire.ApprovedFeeSchedule{Schedule: bitcoin, Approval: bitcoinApproval},
			wire.ApprovedFeeSchedule{Schedule: ethereum, Approval: ethereumApproval},
		)
		rejected := root.Rejected()
		if len(rejected) != 2 || rejected[0].ID != bitcoin.ScheduleID || rejected[1].ID != ethereum.ScheduleID {
			t.Fatalf("rejected = %#v, want schedules %q and %q", rejected, bitcoin.ScheduleID, ethereum.ScheduleID)
		}
		if got, ok := root.ScheduleInForce("ethereum-mainnet", time.Date(2027, 7, 1, 0, 0, 0, 0, time.UTC)); !ok || got.PerKiBFeeBaseUnits != "48900000000" {
			t.Fatalf("schedule = %#v, %v; want the genesis revision still in force", got, ok)
		}
	})
	t.Run("revision with only three release signatures is rejected", func(t *testing.T) {
		genesis := devnet.Genesis()
		bitcoin, bitcoinApproval := vfRevisionTwo(t, genesis, 0, "200", "2027-07-01T00:00:00Z", devnet.ReleaseKeys()[:3])
		ethereum, ethereumApproval := vfRevisionTwo(t, genesis, 1, "48900000000", "2027-07-01T00:00:00Z", devnet.ReleaseKeys())
		root := vfBuildWithSchedules(t, genesis,
			wire.ApprovedFeeSchedule{Schedule: bitcoin, Approval: bitcoinApproval},
			wire.ApprovedFeeSchedule{Schedule: ethereum, Approval: ethereumApproval},
		)
		rejected := root.Rejected()
		if len(rejected) != 2 || rejected[0].ID != bitcoin.ScheduleID || rejected[1].ID != ethereum.ScheduleID {
			t.Fatalf("rejected = %#v, want schedules %q and %q", rejected, bitcoin.ScheduleID, ethereum.ScheduleID)
		}
		if got, ok := root.ScheduleInForce("ethereum-mainnet", time.Date(2027, 7, 1, 0, 0, 0, 0, time.UTC)); !ok || got.PerKiBFeeBaseUnits != "48900000000" {
			t.Fatalf("schedule = %#v, %v; want the genesis revision still in force", got, ok)
		}
	})
}

func vfBuildWithSchedules(t *testing.T, genesis wire.GenesisManifest, schedules ...wire.ApprovedFeeSchedule) *TrustRoot {
	t.Helper()
	root, err := BuildTrustRoot(Pin{ReleaseKeyIDs: genesis.ReleaseKeyIDs, Threshold: int(genesis.ReleaseThreshold)}, Publications{Genesis: genesis, FeeSchedules: wire.FeeSchedulesPage{Schedules: schedules}})
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func vfRevisionTwo(t *testing.T, genesis wire.GenesisManifest, index int, rate, effectiveAt string, signers []ed25519.PrivateKey) (wire.FeeSchedule, wire.FeeScheduleApproval) {
	t.Helper()
	return vfRevision(t, genesis.FeeSchedules[index], 2, rate, effectiveAt, "2027-04-01T00:00:00Z", signers)
}

func vfRevision(t *testing.T, schedule wire.FeeSchedule, revision int64, rate, effectiveAt, approvedAt string, signers []ed25519.PrivateKey) (wire.FeeSchedule, wire.FeeScheduleApproval) {
	t.Helper()
	schedule.ScheduleID = ""
	schedule.Revision = revision
	schedule.PerKiBFeeBaseUnits = rate
	schedule.EffectiveAt = effectiveAt
	schedule.ExpiresAt = "2028-01-01T00:00:00Z"
	var err error
	schedule.ScheduleID, err = fee.ScheduleID(schedule)
	if err != nil {
		t.Fatal(err)
	}
	approval := wire.FeeScheduleApproval{ScheduleID: schedule.ScheduleID, ApprovedAt: approvedAt}
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
