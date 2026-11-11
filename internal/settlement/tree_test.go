package settlement

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"reflect"
	"testing"

	"foryou.local/foryou/internal/wire"
)

type settlementTreeExample struct {
	Receipt          wire.SubmissionReceipt `json:"receipt"`
	SettlementClaim  wire.SettlementClaim   `json:"settlement_claim"`
	SettlementLeaves []wire.SettlementLeaf  `json:"settlement_leaves"`
	SettlementProof  wire.SettlementProof   `json:"settlement_proof"`
}

func readSettlementTreeExample(t *testing.T) settlementTreeExample {
	t.Helper()
	data, err := os.ReadFile("testdata/settlement.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var example settlementTreeExample
	if err := json.Unmarshal(data, &example); err != nil {
		t.Fatal(err)
	}
	return example
}

func TestSettlementTreeMatchesExample(t *testing.T) {
	example := readSettlementTreeExample(t)
	t.Run("root matches claim", func(t *testing.T) {
		got, err := TreeRoot(example.SettlementLeaves)
		if err != nil {
			t.Fatal(err)
		}
		if gotHash := fmt.Sprintf("%x", got.Hash); gotHash != example.SettlementClaim.ReceiptsRoot {
			t.Errorf("root hash = %q; want %q", gotHash, example.SettlementClaim.ReceiptsRoot)
		}
		if got.Sum.String() != example.SettlementClaim.TotalBaseUnits {
			t.Errorf("root sum = %q; want %q", got.Sum, example.SettlementClaim.TotalBaseUnits)
		}
	})
	t.Run("audit path matches proof", func(t *testing.T) {
		got, err := AuditPath(int(example.SettlementProof.LeafIndex), example.SettlementLeaves)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got, example.SettlementProof.AuditPath) {
			t.Errorf("audit path = %#v; want %#v", got, example.SettlementProof.AuditPath)
		}
		root, err := RootFromPath(example.SettlementProof.Leaf, int(example.SettlementProof.LeafIndex), int(example.SettlementClaim.ReceiptCount), got)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprintf("%x", root.Hash) != example.SettlementClaim.ReceiptsRoot || root.Sum.String() != example.SettlementClaim.TotalBaseUnits {
			t.Errorf("proof root = (%x, %s); want (%s, %s)", root.Hash, root.Sum, example.SettlementClaim.ReceiptsRoot, example.SettlementClaim.TotalBaseUnits)
		}
	})
}

func TestRootFromPathRejectsInvalidProof(t *testing.T) {
	example := readSettlementTreeExample(t)
	leaf := example.SettlementProof.Leaf
	index := int(example.SettlementProof.LeafIndex)
	count := int(example.SettlementClaim.ReceiptCount)
	path := example.SettlementProof.AuditPath
	t.Run("one extra path element", func(t *testing.T) {
		withExtra := append(append([]wire.AuditNode(nil), path...), path[0])
		if _, err := RootFromPath(leaf, index, count, withExtra); err == nil {
			t.Fatal("RootFromPath accepted an extra sibling")
		}
	})
	t.Run("index equal to count", func(t *testing.T) {
		if _, err := RootFromPath(leaf, count, count, path); err == nil {
			t.Fatal("RootFromPath accepted an out-of-range index")
		}
	})
	t.Run("negative sibling sum", func(t *testing.T) {
		badPath := append([]wire.AuditNode(nil), path...)
		badPath[0].Sum = "-1"
		if _, err := RootFromPath(leaf, index, count, badPath); err == nil {
			t.Fatal("RootFromPath accepted a negative sibling sum")
		}
	})
	t.Run("noncanonical sibling sum", func(t *testing.T) {
		badPath := append([]wire.AuditNode(nil), path...)
		badPath[0].Sum = "0136"
		if _, err := RootFromPath(leaf, index, count, badPath); err == nil {
			t.Fatal("RootFromPath accepted a noncanonical sibling sum")
		}
	})
	t.Run("changed leaf amount changes root", func(t *testing.T) {
		changed := leaf
		changed.AmountBaseUnits = "1"
		got, err := RootFromPath(changed, index, count, path)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprintf("%x", got.Hash) == example.SettlementClaim.ReceiptsRoot {
			t.Fatal("changed leaf amount preserved the root hash")
		}
	})
	t.Run("lower sibling sum changes root", func(t *testing.T) {
		badPath := append([]wire.AuditNode(nil), path...)
		badPath[0].Sum = "9383"
		got, err := RootFromPath(leaf, index, count, badPath)
		if err != nil {
			t.Fatal(err)
		}
		if fmt.Sprintf("%x", got.Hash) == example.SettlementClaim.ReceiptsRoot {
			t.Fatal("changed sibling sum preserved the root hash")
		}
	})
}

func TestSettlementTreeShapeBoundaries(t *testing.T) {
	example := readSettlementTreeExample(t)
	onePath, err := AuditPath(0, example.SettlementLeaves[:1])
	if err != nil {
		t.Fatal(err)
	}
	if len(onePath) != 0 {
		t.Errorf("single-leaf path length = %d; want 0", len(onePath))
	}
	lastPath, err := AuditPath(4, example.SettlementLeaves)
	if err != nil {
		t.Fatal(err)
	}
	if len(lastPath) != 1 {
		t.Errorf("five-leaf index-4 path length = %d; want 1", len(lastPath))
	}
	firstPath, err := AuditPath(0, example.SettlementLeaves)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstPath) != 3 {
		t.Errorf("five-leaf index-0 path length = %d; want 3", len(firstPath))
	}
}

func TestTreeRootRejectsInvalidSumsAndEmptyTree(t *testing.T) {
	t.Run("zero leaves", func(t *testing.T) {
		if _, err := TreeRoot(nil); err == nil {
			t.Fatal("TreeRoot accepted zero leaves")
		}
	})
	t.Run("leaf sum at U128 limit", func(t *testing.T) {
		leaf := wire.SettlementLeaf{AmountBaseUnits: new(big.Int).Lsh(big.NewInt(1), 128).String()}
		if _, err := TreeRoot([]wire.SettlementLeaf{leaf}); err == nil {
			t.Fatal("TreeRoot accepted a leaf sum of 2^128")
		}
	})
}
