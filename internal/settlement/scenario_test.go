package settlement

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

func exampleProofContext(t *testing.T) ProofContext {
	t.Helper()
	vectors := loadWitnessVectors(t)
	return ProofContext{Committee: vectors.Monitors}
}

func providerBKey(t *testing.T) keys.PrivateKey {
	t.Helper()
	seed, err := hex.DecodeString("a0a1a2a3a4a5a6a7a8a9aaabacadaeafb0b1b2b3b4b5b6b7b8b9babbbcbdbebf")
	if err != nil {
		t.Fatal(err)
	}
	key, err := keys.NewKeyFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func providerNextKey(t *testing.T) keys.PrivateKey {
	t.Helper()
	seed, err := hex.DecodeString("c0c1c2c3c4c5c6c7c8c9cacbcccdcecfd0d1d2d3d4d5d6d7d8d9dadbdcdddedf")
	if err != nil {
		t.Fatal(err)
	}
	key, err := keys.NewKeyFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func foundationKey(t *testing.T) keys.PrivateKey {
	t.Helper()
	seed, err := hex.DecodeString("808182838485868788898a8b8c8d8e8f909192939495969798999a9b9c9d9e9f")
	if err != nil {
		t.Fatal(err)
	}
	key, err := keys.NewKeyFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func syntheticReceipt(t *testing.T, template wire.SubmissionReceipt, messageID, issuedAt string) wire.SubmissionReceipt {
	t.Helper()
	receipt := template
	receipt.MessageID = messageID
	receipt.ReceiptID = "fyr2:synthetic-" + messageID
	receipt.IssuedAt = issuedAt
	return receipt
}

func resignReceipt(t *testing.T, receipt wire.SubmissionReceipt, key keys.PrivateKey) wire.SubmissionReceipt {
	t.Helper()
	encoded, err := canon.EncodeWithout(receipt, "provider_signature")
	if err != nil {
		t.Fatal(err)
	}
	receipt.ProviderSignature, err = keys.Sign(key, "receipt", encoded)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func syntheticClaim(t *testing.T, key keys.PrivateKey, previous *wire.SettlementClaim, receipt wire.SubmissionReceipt, start, end time.Time) (wire.SettlementClaim, wire.SettlementLeaf) {
	t.Helper()
	leaf, err := LeafOf(receipt)
	if err != nil {
		t.Fatal(err)
	}
	root, err := TreeRoot([]wire.SettlementLeaf{leaf})
	if err != nil {
		t.Fatal(err)
	}
	claim, err := BuildClaim(key, ClaimInput{
		Network: wire.BitcoinMainnet, Previous: previous, PeriodStart: start, PeriodEnd: end,
		DeclarationID: "fyrd2:synthetic", ReceiptCount: 1, Total: root.Sum,
		ReceiptsRoot: hex.EncodeToString(root.Hash[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	return claim, leaf
}

func fourWitnesses(t *testing.T, claim wire.SettlementClaim) []wire.SettlementWitness {
	t.Helper()
	at := time.Date(2027, time.January, 15, 12, 0, 0, 0, time.UTC)
	return []wire.SettlementWitness{
		mustSignWitness(t, monitorKey(t, "0"), claim, at),
		mustSignWitness(t, monitorKey(t, "1"), claim, at),
		mustSignWitness(t, monitorKey(t, "2"), claim, at),
		mustSignWitness(t, monitorKey(t, "3"), claim, at),
	}
}

func mustSignWitness(t *testing.T, key keys.PrivateKey, claim wire.SettlementClaim, at time.Time) wire.SettlementWitness {
	t.Helper()
	witness, err := SignWitness(key, claim, big.NewInt(100), at)
	if err != nil {
		t.Fatal(err)
	}
	return witness
}

func redateWitness(t *testing.T, witness wire.SettlementWitness, key keys.PrivateKey, at string) wire.SettlementWitness {
	t.Helper()
	witness.WitnessedAt = at
	encoded, err := canon.EncodeWithout(witness, "witness_signature")
	if err != nil {
		t.Fatal(err)
	}
	witness.WitnessSignature, err = keys.Sign(key, witnessLabel, encoded)
	if err != nil {
		t.Fatal(err)
	}
	return witness
}

func containsJSONField(encoded []byte, field string) bool {
	return strings.Contains(string(encoded), field)
}

type ancestorScenario struct {
	receiptA wire.SubmissionReceipt
	receiptB wire.SubmissionReceipt
	claimA   wire.SettlementClaim
	claimB   wire.SettlementClaim
	child    wire.SettlementClaim
	leafA    wire.SettlementLeaf
	leafB    wire.SettlementLeaf
	context  ProofContext
}

func buildAncestorScenario(t *testing.T) ancestorScenario {
	t.Helper()
	example := readSettlementTreeExample(t)
	parent := example.SettlementClaim
	start1, _ := wire.ParseTime("2027-01-12T00:00:00Z")
	end1, _ := wire.ParseTime("2027-01-13T00:00:00Z")
	receiptA := syntheticReceipt(t, example.Receipt, "synthetic-a1", "2027-01-12T12:00:00Z")
	receiptB := syntheticReceipt(t, example.Receipt, "synthetic-b1", "2027-01-12T12:01:00Z")
	claimA, leafA := syntheticClaim(t, testProviderKey(t), &parent, receiptA, start1, end1)
	claimB, leafB := syntheticClaim(t, testProviderKey(t), &parent, receiptB, start1, end1)
	start2, _ := wire.ParseTime("2027-01-13T00:00:00Z")
	end2, _ := wire.ParseTime("2027-01-14T00:00:00Z")
	receiptChild := syntheticReceipt(t, example.Receipt, "synthetic-a2", "2027-01-13T12:00:00Z")
	child, _ := syntheticClaim(t, testProviderKey(t), &claimA, receiptChild, start2, end2)
	return ancestorScenario{receiptA: receiptA, receiptB: receiptB, claimA: claimA, claimB: claimB, child: child, leafA: leafA, leafB: leafB, context: exampleProofContext(t)}
}

func forkClaims(t *testing.T) (wire.SettlementClaim, wire.SettlementClaim, wire.SettlementClaim) {
	t.Helper()
	vectors := loadClaimVectors(t)
	previous := vectors.SettlementClaim
	input := claimInputFromFixture(t, vectors.SecondClaim, &previous)
	input.Total = big.NewInt(100)
	input.ReceiptsRoot = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	first, err := BuildClaim(testProviderKey(t), input)
	if err != nil {
		t.Fatal(err)
	}
	input.Total = big.NewInt(101)
	second, err := BuildClaim(testProviderKey(t), input)
	if err != nil {
		t.Fatal(err)
	}
	return previous, first, second
}
