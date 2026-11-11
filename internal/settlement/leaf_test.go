package settlement

import (
	"errors"
	"math/big"
	"reflect"
	"testing"
	"time"

	"foryou.local/foryou/internal/wire"
)

func TestReceiptLeafMatchesExample(t *testing.T) {
	example := readSettlementTreeExample(t)
	gotHash, err := ReceiptHash(example.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	if gotHash != example.SettlementProof.Leaf.ReceiptHash {
		t.Errorf("ReceiptHash() = %q; want %q", gotHash, example.SettlementProof.Leaf.ReceiptHash)
	}
	gotLeaf, err := LeafOf(example.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	if gotLeaf != example.SettlementProof.Leaf {
		t.Errorf("LeafOf() = %#v; want %#v", gotLeaf, example.SettlementProof.Leaf)
	}
}

func TestCheckLeafSet(t *testing.T) {
	example := readSettlementTreeExample(t)
	t.Run("example leaves", func(t *testing.T) {
		if err := CheckLeafSet(example.SettlementLeaves); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("reversed leaves", func(t *testing.T) {
		reversed := append([]wire.SettlementLeaf(nil), example.SettlementLeaves...)
		reversed[0], reversed[4] = reversed[4], reversed[0]
		reversed[1], reversed[3] = reversed[3], reversed[1]
		if err := CheckLeafSet(reversed); !errors.Is(err, ErrLeafSet) {
			t.Errorf("CheckLeafSet() error = %v; want ErrLeafSet", err)
		}
	})
	t.Run("duplicate leaf", func(t *testing.T) {
		duplicate := append([]wire.SettlementLeaf(nil), example.SettlementLeaves...)
		duplicate[1] = duplicate[0]
		if err := CheckLeafSet(duplicate); !errors.Is(err, ErrLeafSet) {
			t.Errorf("CheckLeafSet() error = %v; want ErrLeafSet", err)
		}
	})
	t.Run("empty leaves", func(t *testing.T) {
		if err := CheckLeafSet(nil); !errors.Is(err, ErrLeafSet) {
			t.Errorf("CheckLeafSet() error = %v; want ErrLeafSet", err)
		}
	})
}

func TestSortLeaves(t *testing.T) {
	example := readSettlementTreeExample(t)
	reversed := append([]wire.SettlementLeaf(nil), example.SettlementLeaves...)
	reversed[0], reversed[4] = reversed[4], reversed[0]
	reversed[1], reversed[3] = reversed[3], reversed[1]
	original := append([]wire.SettlementLeaf(nil), reversed...)
	got := SortLeaves(reversed)
	if !reflect.DeepEqual(got, example.SettlementLeaves) {
		t.Errorf("SortLeaves(reversed) = %#v; want %#v", got, example.SettlementLeaves)
	}
	if !reflect.DeepEqual(reversed, original) {
		t.Errorf("SortLeaves modified its input: got %#v; want %#v", reversed, original)
	}
}

type fakeSettlementSchedules struct {
	scheduleID string
	schedule   Schedule
	inForce    bool
}

func (s fakeSettlementSchedules) Lookup(scheduleID string) (Schedule, bool) {
	return s.schedule, scheduleID == s.scheduleID
}

func (s fakeSettlementSchedules) InForceWithin(scheduleID, network string, at time.Time) bool {
	return s.inForce && scheduleID == s.scheduleID && network == wire.BitcoinMainnet
}

func TestCheckLeafFeeRules(t *testing.T) {
	example := readSettlementTreeExample(t)
	claim := example.SettlementClaim
	leaf := example.SettlementProof.Leaf
	fees := fakeSettlementSchedules{
		scheduleID: leaf.ScheduleID,
		schedule:   Schedule{BaseFee: big.NewInt(0), PerKiBFee: big.NewInt(136)},
		inForce:    true,
	}
	t.Run("one KiB", func(t *testing.T) {
		if err := CheckLeaf(claim, leaf, fees); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("nonmultiple fee", func(t *testing.T) {
		changed := leaf
		changed.AmountBaseUnits = "137"
		if err := CheckLeaf(claim, changed, fees); !errors.Is(err, ErrLeafFee) {
			t.Errorf("CheckLeaf() error = %v; want ErrLeafFee", err)
		}
	})
	t.Run("maximum KiB multiplier", func(t *testing.T) {
		changed := leaf
		changed.AmountBaseUnits = "139264"
		if err := CheckLeaf(claim, changed, fees); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("multiplier above maximum", func(t *testing.T) {
		changed := leaf
		changed.AmountBaseUnits = "139400"
		if err := CheckLeaf(claim, changed, fees); !errors.Is(err, ErrLeafFee) {
			t.Errorf("CheckLeaf() error = %v; want ErrLeafFee", err)
		}
	})
	t.Run("period end is exclusive", func(t *testing.T) {
		changed := leaf
		changed.IssuedAt = claim.PeriodEnd
		if err := CheckLeaf(claim, changed, fees); !errors.Is(err, ErrLeafPeriod) {
			t.Errorf("CheckLeaf() error = %v; want ErrLeafPeriod", err)
		}
	})
	t.Run("unknown schedule", func(t *testing.T) {
		unknown := leaf
		unknown.ScheduleID = "fyfs2:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		if err := CheckLeaf(claim, unknown, fees); !errors.Is(err, ErrScheduleUnknown) {
			t.Errorf("CheckLeaf() error = %v; want ErrScheduleUnknown", err)
		}
	})
	t.Run("schedule not in force", func(t *testing.T) {
		notInForce := fees
		notInForce.inForce = false
		if err := CheckLeaf(claim, leaf, notInForce); !errors.Is(err, ErrLeafFee) {
			t.Errorf("CheckLeaf() error = %v; want ErrLeafFee", err)
		}
	})
}

func TestCheckTotals(t *testing.T) {
	example := readSettlementTreeExample(t)
	t.Run("example totals", func(t *testing.T) {
		if err := CheckTotals(example.SettlementClaim, example.SettlementLeaves); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("wrong receipt count", func(t *testing.T) {
		changed := example.SettlementClaim
		changed.ReceiptCount = 4
		if err := CheckTotals(changed, example.SettlementLeaves); !errors.Is(err, ErrTotals) {
			t.Errorf("CheckTotals() error = %v; want ErrTotals", err)
		}
	})
}

func TestPossibleFeeWithZeroRate(t *testing.T) {
	schedule := Schedule{BaseFee: big.NewInt(23), PerKiBFee: big.NewInt(0)}
	if !PossibleFee(schedule, big.NewInt(23)) {
		t.Fatal("PossibleFee rejected base fee with zero rate")
	}
	if PossibleFee(schedule, big.NewInt(24)) {
		t.Fatal("PossibleFee accepted amount above base fee with zero rate")
	}
}
