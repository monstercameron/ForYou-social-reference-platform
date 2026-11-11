// Package settlement holds the settlement sum tree, leaves, and claim-side rules of docs/03-economics.md.
package settlement

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

var treeLimit = new(big.Int).Lsh(big.NewInt(1), 128)

// Node is one sum-tree node.
type Node struct {
	Hash [32]byte
	Sum  *big.Int
}

func settlementSplit(count int) int {
	split := 1
	for split*2 < count {
		split *= 2
	}
	return split
}

func settlementU128(sum *big.Int) ([16]byte, error) {
	var encoded [16]byte
	if sum == nil || sum.Sign() < 0 || sum.Cmp(treeLimit) >= 0 {
		return encoded, errors.New("settlement: sum outside U128")
	}
	sum.FillBytes(encoded[:])
	return encoded, nil
}

func settlementJoin(left, right Node) (Node, error) {
	leftSum, err := settlementU128(left.Sum)
	if err != nil {
		return Node{}, err
	}
	rightSum, err := settlementU128(right.Sum)
	if err != nil {
		return Node{}, err
	}
	sum := new(big.Int).Add(left.Sum, right.Sum)
	if _, err := settlementU128(sum); err != nil {
		return Node{}, err
	}
	input := make([]byte, 0, 1+32+16+32+16)
	input = append(input, 0x01)
	input = append(input, left.Hash[:]...)
	input = append(input, leftSum[:]...)
	input = append(input, right.Hash[:]...)
	input = append(input, rightSum[:]...)
	return Node{Hash: sha256.Sum256(input), Sum: sum}, nil
}

func settlementTree(nodes []Node) (Node, error) {
	if len(nodes) == 1 {
		return nodes[0], nil
	}
	split := settlementSplit(len(nodes))
	left, err := settlementTree(nodes[:split])
	if err != nil {
		return Node{}, err
	}
	right, err := settlementTree(nodes[split:])
	if err != nil {
		return Node{}, err
	}
	return settlementJoin(left, right)
}

func settlementAuditPath(index int, nodes []Node) ([]wire.AuditNode, error) {
	if len(nodes) == 1 {
		return nil, nil
	}
	split := settlementSplit(len(nodes))
	if index < split {
		path, err := settlementAuditPath(index, nodes[:split])
		if err != nil {
			return nil, err
		}
		sibling, err := settlementTree(nodes[split:])
		if err != nil {
			return nil, err
		}
		return append(path, wire.AuditNode{Hash: hex.EncodeToString(sibling.Hash[:]), Sum: sibling.Sum.String()}), nil
	}
	path, err := settlementAuditPath(index-split, nodes[split:])
	if err != nil {
		return nil, err
	}
	sibling, err := settlementTree(nodes[:split])
	if err != nil {
		return nil, err
	}
	return append(path, wire.AuditNode{Hash: hex.EncodeToString(sibling.Hash[:]), Sum: sibling.Sum.String()}), nil
}

func settlementNodes(leaves []wire.SettlementLeaf) ([]Node, error) {
	if len(leaves) == 0 {
		return nil, errors.New("settlement: empty tree")
	}
	nodes := make([]Node, len(leaves))
	for index, leaf := range leaves {
		node, err := LeafNode(leaf)
		if err != nil {
			return nil, fmt.Errorf("settlement: leaf %d: %w", index, err)
		}
		nodes[index] = node
	}
	return nodes, nil
}

// LeafNode hashes a settlement leaf and assigns its amount as the node sum.
func LeafNode(leaf wire.SettlementLeaf) (Node, error) {
	amount, err := wire.PositiveAmount(leaf.AmountBaseUnits)
	if err != nil {
		return Node{}, err
	}
	if _, err := settlementU128(amount); err != nil {
		return Node{}, err
	}
	canonical, err := canon.Encode(leaf)
	if err != nil {
		return Node{}, err
	}
	input, err := keys.SigningInput("settlement-leaf", canonical)
	if err != nil {
		return Node{}, err
	}
	prefixed := append([]byte{0x00}, input...)
	return Node{Hash: sha256.Sum256(prefixed), Sum: amount}, nil
}

// TreeRoot computes the sum-tree root over leaves in their given order.
func TreeRoot(leaves []wire.SettlementLeaf) (Node, error) {
	nodes, err := settlementNodes(leaves)
	if err != nil {
		return Node{}, err
	}
	return settlementTree(nodes)
}

// AuditPath returns the sibling nodes from a leaf up to the root.
func AuditPath(index int, leaves []wire.SettlementLeaf) ([]wire.AuditNode, error) {
	if index < 0 || index >= len(leaves) {
		return nil, errors.New("settlement: leaf index outside tree")
	}
	nodes, err := settlementNodes(leaves)
	if err != nil {
		return nil, err
	}
	return settlementAuditPath(index, nodes)
}

func settlementPathSides(index, count int) ([]bool, error) {
	if count < 1 || index < 0 || index >= count {
		return nil, errors.New("settlement: leaf index outside tree")
	}
	rootToLeaf := make([]bool, 0)
	for count > 1 {
		split := settlementSplit(count)
		if index < split {
			rootToLeaf = append(rootToLeaf, false)
			count = split
		} else {
			rootToLeaf = append(rootToLeaf, true)
			index -= split
			count -= split
		}
	}
	leafToRoot := make([]bool, len(rootToLeaf))
	for sideIndex := range rootToLeaf {
		leafToRoot[sideIndex] = rootToLeaf[len(rootToLeaf)-1-sideIndex]
	}
	return leafToRoot, nil
}

func settlementAuditNode(node wire.AuditNode) (Node, error) {
	if len(node.Hash) != 64 || node.Hash != strings.ToLower(node.Hash) {
		return Node{}, errors.New("settlement: invalid sibling hash")
	}
	hash, err := hex.DecodeString(node.Hash)
	if err != nil || len(hash) != 32 {
		return Node{}, errors.New("settlement: invalid sibling hash")
	}
	sum, err := wire.Amount(node.Sum)
	if err != nil {
		return Node{}, errors.New("settlement: invalid sibling sum")
	}
	if _, err := settlementU128(sum); err != nil {
		return Node{}, err
	}
	var digest [32]byte
	copy(digest[:], hash)
	return Node{Hash: digest, Sum: sum}, nil
}

// RootFromPath recomputes a sum-tree root using the index-derived sibling sides.
func RootFromPath(leaf wire.SettlementLeaf, index, count int, path []wire.AuditNode) (Node, error) {
	if count > 10000 {
		return Node{}, errors.New("settlement: leaf count exceeds limit")
	}
	sides, err := settlementPathSides(index, count)
	if err != nil {
		return Node{}, err
	}
	if len(sides) != len(path) {
		return Node{}, errors.New("settlement: audit path depth mismatch")
	}
	node, err := LeafNode(leaf)
	if err != nil {
		return Node{}, err
	}
	for pathIndex, siblingRecord := range path {
		sibling, err := settlementAuditNode(siblingRecord)
		if err != nil {
			return Node{}, err
		}
		if sides[pathIndex] {
			node, err = settlementJoin(sibling, node)
		} else {
			node, err = settlementJoin(node, sibling)
		}
		if err != nil {
			return Node{}, err
		}
	}
	return node, nil
}
