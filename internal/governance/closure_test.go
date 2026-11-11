package governance

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

func TestRootFromProofRejectsInvalidShapes(t *testing.T) {
	t.Run("index equals size", func(t *testing.T) {
		if _, err := RootFromProof("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", wire.FoundationLogProof{Index: 1, Size: 1, AuditPath: []string{}}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("RootFromProof error = %v; want ErrInvalid", err)
		}
	})
	t.Run("audit path has an extra sibling", func(t *testing.T) {
		if _, err := RootFromProof("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", wire.FoundationLogProof{Index: 0, Size: 1, AuditPath: []string{"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("RootFromProof error = %v; want ErrInvalid", err)
		}
	})
}

func TestRootFromProofThreeLeafRFC6962Tree(t *testing.T) {
	example := governanceExample(t)
	first := decodeExample[wire.VaultReceipt](t, example, "vault_receipt")
	second := first
	second.MessageID = "fy1:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	third := first
	third.MessageID = "fy1:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	firstHash, err := ObjectHash(first)
	if err != nil {
		t.Fatal(err)
	}
	secondHash, err := ObjectHash(second)
	if err != nil {
		t.Fatal(err)
	}
	thirdHash, err := ObjectHash(third)
	if err != nil {
		t.Fatal(err)
	}
	firstLeaf := testLogLeaf(firstHash)
	secondLeaf := testLogLeaf(secondHash)
	thirdLeaf := testLogLeaf(thirdHash)
	left := testLogNode(firstLeaf, secondLeaf)
	want := testLogNode(left, thirdLeaf)
	got, err := RootFromProof(firstHash, wire.FoundationLogProof{Index: 0, Size: 3, AuditPath: []string{hex.EncodeToString(secondLeaf[:]), hex.EncodeToString(thirdLeaf[:])}})
	if err != nil {
		t.Fatal(err)
	}
	if wantHex := hex.EncodeToString(want[:]); got != wantHex {
		t.Fatalf("RootFromProof = %s; want independently computed root %s", got, wantHex)
	}
}

func testLogLeaf(objectHash string) [32]byte {
	object, _ := hex.DecodeString(objectHash)
	input := append([]byte{0}, object...)
	return sha256.Sum256(input)
}

func testLogNode(left, right [32]byte) [32]byte {
	input := append([]byte{1}, left[:]...)
	input = append(input, right[:]...)
	return sha256.Sum256(input)
}

func TestClosureRules(t *testing.T) {
	example := governanceExample(t)
	genesis := decodeExample[wire.GenesisManifest](t, example, "genesis_manifest")
	release := syntheticReleaseKeys(t)
	first := signedTestReceipt(t, "fy1:"+strings.Repeat("a1", 32), "2027-01-07T00:00:00Z")
	second := signedTestReceipt(t, "fy1:"+strings.Repeat("b2", 32), "2027-01-07T00:00:00Z")
	third := signedTestReceipt(t, "fy1:"+strings.Repeat("c3", 32), "2027-01-07T00:00:00Z")
	firstHash, secondHash := mustObjectHash(t, first), mustObjectHash(t, second)
	firstLeaf, secondLeaf, thirdLeaf := testLogLeaf(firstHash), testLogLeaf(secondHash), testLogLeaf(mustObjectHash(t, third))
	pair := testLogNode(firstLeaf, secondLeaf)
	root := testLogNode(pair, thirdLeaf)
	foundationID := first.FoundationID
	head := signedTestHead(t, syntheticFoundationKey(), foundationID, 3, hex.EncodeToString(root[:]), "2027-01-20T00:00:00Z")
	closure := signedTestClosure(t, head, nil, release[:4])
	firstProof := &wire.FoundationLogProof{Index: 0, Size: 3, AuditPath: []string{hex.EncodeToString(secondLeaf[:]), hex.EncodeToString(thirdLeaf[:])}}
	secondProof := &wire.FoundationLogProof{Index: 1, Size: 3, AuditPath: []string{hex.EncodeToString(firstLeaf[:]), hex.EncodeToString(thirdLeaf[:])}}
	thirdProof := &wire.FoundationLogProof{Index: 2, Size: 3, AuditPath: []string{hex.EncodeToString(pair[:])}}
	storedAt := mustTime(t, first.StoredAt)
	closedTrust := func(t *testing.T, c wire.FoundationKeyClosure, heads []wire.FoundationLogHead) *Trust {
		t.Helper()
		trust, err := NewTrust(genesis, nil, nil, []wire.FoundationKeyClosure{c}, heads)
		if err != nil {
			t.Fatal(err)
		}
		return trust
	}
	expectRejected := func(t *testing.T, trust *Trust, receipt wire.VaultReceipt, proof *wire.FoundationLogProof) {
		t.Helper()
		if err := trust.VerifyFoundation(receipt, storedAt, "vault-receipt", proof); !errors.Is(err, ErrInvalid) {
			t.Fatalf("VerifyFoundation error = %v; want ErrInvalid", err)
		}
	}
	t.Run("logged object verifies with a proof against the closure head", func(t *testing.T) {
		if err := closedTrust(t, closure, []wire.FoundationLogHead{head}).VerifyFoundation(first, storedAt, "vault-receipt", firstProof); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("missing proof rejected", func(t *testing.T) {
		expectRejected(t, closedTrust(t, closure, nil), first, nil)
	})
	t.Run("proof against another size rejected", func(t *testing.T) {
		expectRejected(t, closedTrust(t, closure, nil), first, &wire.FoundationLogProof{Index: 0, Size: 2, AuditPath: []string{hex.EncodeToString(secondLeaf[:])}})
	})
	t.Run("forged object signed by the closed key rejected", func(t *testing.T) {
		forged := signedTestReceipt(t, "fy1:"+strings.Repeat("ee", 32), first.StoredAt)
		expectRejected(t, closedTrust(t, closure, nil), forged, firstProof)
	})
	t.Run("voided object rejected while another passes", func(t *testing.T) {
		voiding := closedTrust(t, signedTestClosure(t, head, []string{firstHash}, release[:4]), nil)
		expectRejected(t, voiding, first, firstProof)
		if err := voiding.VerifyFoundation(second, storedAt, "vault-receipt", secondProof); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("closure over a smaller head stands and reports the larger head", func(t *testing.T) {
		smallHead := signedTestHead(t, syntheticFoundationKey(), foundationID, 1, hex.EncodeToString(firstLeaf[:]), "2027-01-08T00:00:00Z")
		shrunk := closedTrust(t, signedTestClosure(t, smallHead, nil, release[:4]), []wire.FoundationLogHead{head})
		if got := shrunk.ClosureEvidence(); len(got) != 1 || got[0] != head {
			t.Fatalf("ClosureEvidence = %#v; want the size-3 head", got)
		}
		expectRejected(t, shrunk, third, thirdProof)
		if got := closedTrust(t, closure, []wire.FoundationLogHead{head}).ClosureEvidence(); len(got) != 0 {
			t.Fatalf("ClosureEvidence for a matching head = %#v; want none", got)
		}
	})
	t.Run("closure with three signers rejected", func(t *testing.T) {
		if _, err := NewTrust(genesis, nil, nil, []wire.FoundationKeyClosure{signedTestClosure(t, head, nil, release[:3])}, nil); !errors.Is(err, ErrInvalid) {
			t.Fatalf("NewTrust error = %v; want ErrInvalid", err)
		}
	})
	t.Run("closure head signed by another key rejected", func(t *testing.T) {
		other := syntheticProviderBKey()
		otherHead := signedTestHead(t, other, keys.DIDOf(other), 3, head.Root, head.SignedAt)
		mismatched := signedTestClosure(t, otherHead, nil, release[:4])
		mismatched.FoundationID = foundationID
		mismatched = resignClosure(t, mismatched, release[:4])
		if _, err := NewTrust(genesis, nil, nil, []wire.FoundationKeyClosure{mismatched}, nil); !errors.Is(err, ErrInvalid) {
			t.Fatalf("NewTrust error = %v; want ErrInvalid", err)
		}
	})
	t.Run("deposits stop counting at closed_at", func(t *testing.T) {
		trust := closedTrust(t, closure, nil)
		recipient := genesis.FoundationKeyCertificates[0].Recipients.BitcoinMainnet
		closedAt := mustTime(t, closure.ClosedAt)
		if !trust.RecipientCounts(wire.BitcoinMainnet, recipient, closedAt.Add(-time.Second)) {
			t.Error("deposit one second before closed_at did not count")
		}
		if trust.RecipientCounts(wire.BitcoinMainnet, recipient, closedAt) {
			t.Error("deposit at closed_at counted")
		}
		if _, ok := trust.CurrentFoundationID(storedAt); ok {
			t.Error("CurrentFoundationID returned a closed key")
		}
	})
}

func signedTestReceipt(t *testing.T, messageID, storedAt string) wire.VaultReceipt {
	t.Helper()
	foundation := syntheticFoundationKey()
	receipt := wire.VaultReceipt{FoundationID: keys.DIDOf(foundation), MessageID: messageID, StoredAt: storedAt}
	unsigned, err := canon.EncodeWithout(receipt, "foundation_signature")
	if err != nil {
		t.Fatal(err)
	}
	receipt.FoundationSignature, err = keys.Sign(foundation, "vault-receipt", unsigned)
	if err != nil {
		t.Fatal(err)
	}
	return receipt
}

func mustObjectHash(t *testing.T, object any) string {
	t.Helper()
	hash, err := ObjectHash(object)
	if err != nil {
		t.Fatal(err)
	}
	return hash
}

func signedTestHead(t *testing.T, signer ed25519.PrivateKey, foundationID string, size int64, root, signedAt string) wire.FoundationLogHead {
	t.Helper()
	head := wire.FoundationLogHead{FoundationID: foundationID, Size: size, Root: root, SignedAt: signedAt}
	unsigned, err := canon.EncodeWithout(head, "foundation_signature")
	if err != nil {
		t.Fatal(err)
	}
	head.FoundationSignature, err = keys.Sign(signer, "foundation-log-head", unsigned)
	if err != nil {
		t.Fatal(err)
	}
	return head
}

func signedTestClosure(t *testing.T, head wire.FoundationLogHead, voided []string, signers []ed25519.PrivateKey) wire.FoundationKeyClosure {
	t.Helper()
	if voided == nil {
		voided = []string{}
	}
	closure := wire.FoundationKeyClosure{FoundationID: head.FoundationID, ClosedAt: "2027-06-01T00:00:00Z", Head: head, Voided: voided}
	return resignClosure(t, closure, signers)
}

func resignClosure(t *testing.T, closure wire.FoundationKeyClosure, signers []ed25519.PrivateKey) wire.FoundationKeyClosure {
	t.Helper()
	unsigned, err := canon.EncodeWithout(closure, "release_signatures")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := canon.Parse(unsigned, canon.Limits{MaxDepth: 32})
	if err != nil {
		t.Fatal(err)
	}
	closure.ReleaseSignatures, err = SignRelease("foundation-key-closure", parsed, signers)
	if err != nil {
		t.Fatal(err)
	}
	return closure
}
