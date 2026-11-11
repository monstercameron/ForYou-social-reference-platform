package governance

import (
	"errors"
	"testing"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/wire"
)

func TestCheckGenesis(t *testing.T) {
	example := governanceExample(t)
	genesis := decodeExample[wire.GenesisManifest](t, example, "genesis_manifest")
	t.Run("example trust root", func(t *testing.T) {
		set, err := CheckGenesis(genesis)
		if err != nil {
			t.Fatal(err)
		}
		if len(set.IDs) != 5 || set.Threshold != 4 {
			t.Fatalf("CheckGenesis set = %#v; want five keys and threshold four", set)
		}
	})
	t.Run("changed signed fee amount rejected", func(t *testing.T) {
		changed := genesis
		changed.FeeSchedules = append([]wire.FeeSchedule(nil), genesis.FeeSchedules...)
		changed.FeeSchedules[0].PerKiBFeeBaseUnits = "137"
		if _, err := CheckGenesis(changed); !errors.Is(err, ErrInvalid) {
			t.Fatalf("CheckGenesis error = %v; want ErrInvalid", err)
		}
	})
	t.Run("release threshold boundaries", func(t *testing.T) {
		low := genesis
		low.ReleaseThreshold = 1
		if _, err := CheckGenesis(low); !errors.Is(err, ErrInvalid) {
			t.Fatalf("CheckGenesis threshold one error = %v; want ErrInvalid", err)
		}
		high := genesis
		high.ReleaseThreshold = 3
		if _, err := CheckGenesis(high); !errors.Is(err, ErrInvalid) {
			t.Fatalf("CheckGenesis threshold three error = %v; want ErrInvalid", err)
		}
	})
	t.Run("three signatures cannot authorize genesis", func(t *testing.T) {
		changed := genesis
		unsignedBytes, err := canon.EncodeWithout(changed, "release_signatures")
		if err != nil {
			t.Fatal(err)
		}
		unsigned, err := canon.Parse(unsignedBytes, canon.Limits{MaxDepth: 32})
		if err != nil {
			t.Fatal(err)
		}
		changed.ReleaseSignatures, err = SignRelease("genesis-manifest", unsigned, syntheticReleaseKeys(t)[:3])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := CheckGenesis(changed); !errors.Is(err, ErrInvalid) {
			t.Fatalf("CheckGenesis three-signature error = %v; want ErrInvalid", err)
		}
	})
	t.Run("key set boundaries", func(t *testing.T) {
		keys := genesis.ReleaseKeyIDs
		if err := (KeySet{IDs: keys, Threshold: 4}).Validate(); err != nil {
			t.Fatalf("five keys at threshold four: %v", err)
		}
		if err := (KeySet{IDs: keys[:4], Threshold: 4}).Validate(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("four keys at threshold four error = %v; want ErrInvalid", err)
		}
		six := append(append([]string(nil), keys...), "did:key:z6MkhFwXNFWosLeugvSf4wcL9t3uuRXueGSFTRgSvHhWj5G2")
		if err := (KeySet{IDs: six, Threshold: 4}).Validate(); err != nil {
			t.Fatalf("six keys at threshold four: %v", err)
		}
		seven := append(six, "did:key:z6MkrPuMzSSYYddw1BKtyrH5mMJzQmUX42Lr8eiKpsPVb3C1")
		if err := (KeySet{IDs: seven, Threshold: 4}).Validate(); !errors.Is(err, ErrInvalid) {
			t.Fatalf("seven keys at threshold four error = %v; want ErrInvalid", err)
		}
	})
}
