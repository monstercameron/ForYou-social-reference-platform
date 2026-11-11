package governance

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

type logVector struct {
	Hashes []string            `json:"hashes"`
	Root   string              `json:"root"`
	Paths  map[string][]string `json:"paths"`
}

type logVectorFile struct {
	Three   logVector   `json:"3"`
	Five    logVector   `json:"5"`
	Example logExamples `json:"example"`
	Heads   struct {
		TwoRoot       string `json:"two_root"`
		RewrittenRoot string `json:"rewritten_root"`
	} `json:"heads"`
}

type logExamples struct {
	Head    wire.FoundationLogHead `json:"foundation_log_head"`
	Receipt wire.VaultReceipt      `json:"vault_receipt"`
}

func TestFoundationLogExample(t *testing.T) {
	data, err := os.ReadFile("testdata/log_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors logVectorFile
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	example := vectors.Example
	canonical, err := canon.Encode(example.Receipt)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	objectHash := hex.EncodeToString(digest[:])

	log, err := NewLog(nil)
	if err != nil {
		t.Fatal(err)
	}
	entry, added, err := log.Append(LogKindVaultReceipt, objectHash)
	if err != nil || !added || entry.Index != 0 || entry.Kind != string(LogKindVaultReceipt) || entry.ObjectHash != objectHash {
		t.Fatalf("Append() = (%+v, %t, %v)", entry, added, err)
	}
	t.Run("one entry root and proof", func(t *testing.T) {
		root, err := log.Root(1)
		if err != nil || root != example.Head.Root {
			t.Fatalf("Root(1) = %q, %v; want %q", root, err, example.Head.Root)
		}
		proof, err := log.Proof(objectHash, 1)
		if err != nil || proof.Index != 0 || proof.Size != 1 || len(proof.AuditPath) != 0 {
			t.Fatalf("Proof() = %+v, %v; want index 0, size 1, empty path", proof, err)
		}
	})
	t.Run("signed head matches fixture", func(t *testing.T) {
		seed := []byte{128, 129, 130, 131, 132, 133, 134, 135, 136, 137, 138, 139, 140, 141, 142, 143, 144, 145, 146, 147, 148, 149, 150, 151, 152, 153, 154, 155, 156, 157, 158, 159}
		key := ed25519.NewKeyFromSeed(seed)
		head, err := SignHead(key, example.Head.FoundationID, example.Head.Root, 1, time.Date(2027, 1, 20, 0, 0, 0, 0, time.UTC))
		if err != nil || !reflect.DeepEqual(head, example.Head) {
			t.Fatalf("SignHead() = %+v, %v; want fixture %+v", head, err, example.Head)
		}
	})
}

func TestFoundationLogVectors(t *testing.T) {
	data, err := os.ReadFile("testdata/log_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors logVectorFile
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	checkRoot := func(t *testing.T, key string) {
		t.Helper()
		vector := vectors.Three
		if key == "5" {
			vector = vectors.Five
		}
		root, err := TreeRoot(vector.Hashes)
		if err != nil || root != vector.Root {
			t.Fatalf("TreeRoot(%s entries) = %q, %v; want %q", key, root, err, vector.Root)
		}
	}
	checkProof := func(t *testing.T, count, index int, want []string) {
		t.Helper()
		vector := vectors.Three
		if count == 5 {
			vector = vectors.Five
		}
		var entries []wire.FoundationLogEntry
		if count == 3 {
			entries = []wire.FoundationLogEntry{
				{Index: 0, Kind: string(LogKindVaultReceipt), ObjectHash: vector.Hashes[0]},
				{Index: 1, Kind: string(LogKindVaultReceipt), ObjectHash: vector.Hashes[1]},
				{Index: 2, Kind: string(LogKindVaultReceipt), ObjectHash: vector.Hashes[2]},
			}
		} else {
			entries = []wire.FoundationLogEntry{
				{Index: 0, Kind: string(LogKindVaultReceipt), ObjectHash: vector.Hashes[0]},
				{Index: 1, Kind: string(LogKindVaultReceipt), ObjectHash: vector.Hashes[1]},
				{Index: 2, Kind: string(LogKindVaultReceipt), ObjectHash: vector.Hashes[2]},
				{Index: 3, Kind: string(LogKindVaultReceipt), ObjectHash: vector.Hashes[3]},
				{Index: 4, Kind: string(LogKindVaultReceipt), ObjectHash: vector.Hashes[4]},
			}
		}
		log, err := NewLog(entries)
		if err != nil {
			t.Fatal(err)
		}
		proof, err := log.Proof(vector.Hashes[index], int64(count))
		if err != nil || proof.Index != int64(index) || proof.Size != int64(count) || !reflect.DeepEqual(proof.AuditPath, want) {
			t.Fatalf("Proof(%d, %d) = %+v, %v; want path %v", index, count, proof, err, want)
		}
	}
	t.Run("three entry root", func(t *testing.T) { checkRoot(t, "3") })
	t.Run("five entry root", func(t *testing.T) { checkRoot(t, "5") })
	t.Run("three entry first proof", func(t *testing.T) { checkProof(t, 3, 0, vectors.Three.Paths["0"]) })
	t.Run("three entry last proof", func(t *testing.T) { checkProof(t, 3, 2, vectors.Three.Paths["2"]) })
	t.Run("five entry first proof", func(t *testing.T) { checkProof(t, 5, 0, vectors.Five.Paths["0"]) })
	t.Run("five entry middle proof", func(t *testing.T) { checkProof(t, 5, 2, vectors.Five.Paths["2"]) })
	t.Run("five entry last proof", func(t *testing.T) { checkProof(t, 5, 4, vectors.Five.Paths["4"]) })
}

func TestFoundationLogBoundariesAndPaging(t *testing.T) {
	log, err := NewLog(nil)
	if err != nil {
		t.Fatal(err)
	}
	first, added, err := log.Append(LogKindVaultReceipt, "0000000000000000000000000000000000000000000000000000000000000000")
	if err != nil || !added {
		t.Fatalf("first Append() = %+v, %t, %v", first, added, err)
	}
	duplicate, added, err := log.Append(LogKindStatement, first.ObjectHash)
	if err != nil || added || duplicate != first || log.Len() != 1 {
		t.Fatalf("duplicate Append() = %+v, %t, %v; Len() = %d", duplicate, added, err, log.Len())
	}
	_, _, err = log.Append("unknown", "1111111111111111111111111111111111111111111111111111111111111111")
	if err == nil {
		t.Fatal("Append() accepted an unknown kind")
	}
	_, _, err = log.Append(LogKindStatement, "A111111111111111111111111111111111111111111111111111111111111111")
	if err == nil {
		t.Fatal("Append() accepted a malformed object hash")
	}
	t.Run("root size zero", func(t *testing.T) {
		if _, err := log.Root(0); err == nil {
			t.Fatal("Root(0) succeeded")
		}
	})
	t.Run("root size past end", func(t *testing.T) {
		if _, err := log.Root(2); err == nil {
			t.Fatal("Root(2) succeeded for a one-entry log")
		}
	})
	_, _, err = log.Append(LogKindStatement, "1111111111111111111111111111111111111111111111111111111111111111")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = log.Append(LogKindVaultStatus, "2222222222222222222222222222222222222222222222222222222222222222")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("proof outside requested tree", func(t *testing.T) {
		if _, err := log.Proof("2222222222222222222222222222222222222222222222222222222222222222", 2); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Proof() error = %v; want ErrNotFound", err)
		}
	})
	_, _, err = log.Append(LogKindStatement, "3333333333333333333333333333333333333333333333333333333333333333")
	if err != nil {
		t.Fatal(err)
	}
	t.Run("first page", func(t *testing.T) {
		got := log.Entries(-1, 2)
		want := []wire.FoundationLogEntry{first, {Index: 1, Kind: string(LogKindStatement), ObjectHash: "1111111111111111111111111111111111111111111111111111111111111111"}}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Entries(-1, 2) = %+v; want %+v", got, want)
		}
	})
	t.Run("page after index one", func(t *testing.T) {
		got := log.Entries(1, 1000)
		want := []wire.FoundationLogEntry{
			{Index: 2, Kind: string(LogKindVaultStatus), ObjectHash: "2222222222222222222222222222222222222222222222222222222222222222"},
			{Index: 3, Kind: string(LogKindStatement), ObjectHash: "3333333333333333333333333333333333333333333333333333333333333333"},
		}
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("Entries(1, 1000) = %+v; want %+v", got, want)
		}
	})
}

func TestFoundationLogHeadsConsistent(t *testing.T) {
	hashes := []string{
		"0000000000000000000000000000000000000000000000000000000000000000",
		"1111111111111111111111111111111111111111111111111111111111111111",
		"2222222222222222222222222222222222222222222222222222222222222222",
	}
	vectorData, err := os.ReadFile("testdata/log_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors logVectorFile
	if err := json.Unmarshal(vectorData, &vectors); err != nil {
		t.Fatal(err)
	}
	head2 := wire.FoundationLogHead{Size: 2, Root: vectors.Heads.TwoRoot}
	head3 := wire.FoundationLogHead{Size: 3, Root: vectors.Three.Root}
	t.Run("shared prefix", func(t *testing.T) {
		if !HeadsConsistent(hashes, hashes, head2, head3) {
			t.Fatal("HeadsConsistent() rejected matching prefix heads")
		}
	})
	t.Run("entries after smaller head do not affect prefix", func(t *testing.T) {
		olderList := []string{hashes[0], hashes[1], "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}
		if !HeadsConsistent(olderList, hashes, head2, head3) {
			t.Fatal("HeadsConsistent() compared entries beyond the smaller head")
		}
	})
	t.Run("rewritten first entry", func(t *testing.T) {
		rewritten := []string{"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", hashes[1], hashes[2]}
		rewrittenHead := wire.FoundationLogHead{Size: 3, Root: vectors.Heads.RewrittenRoot}
		if HeadsConsistent(hashes, rewritten, head2, rewrittenHead) {
			t.Fatal("HeadsConsistent() accepted a rewritten prefix")
		}
	})
	t.Run("hash list does not match root", func(t *testing.T) {
		wrongRootHashes := []string{"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", hashes[1], hashes[2]}
		if HeadsConsistent(hashes, wrongRootHashes, head3, head3) {
			t.Fatal("HeadsConsistent() accepted a hash list with the wrong root")
		}
	})
}

func TestFoundationLogNewLogValidation(t *testing.T) {
	valid := wire.FoundationLogEntry{Index: 0, Kind: string(LogKindVaultReceipt), ObjectHash: "0000000000000000000000000000000000000000000000000000000000000000"}
	t.Run("journal index gap", func(t *testing.T) {
		second := wire.FoundationLogEntry{Index: 2, Kind: string(LogKindStatement), ObjectHash: "1111111111111111111111111111111111111111111111111111111111111111"}
		if _, err := NewLog([]wire.FoundationLogEntry{valid, second}); err == nil {
			t.Fatal("NewLog() accepted a journal with an index gap")
		}
	})
	t.Run("unknown journal kind", func(t *testing.T) {
		invalid := wire.FoundationLogEntry{Index: 0, Kind: "unknown", ObjectHash: valid.ObjectHash}
		if _, err := NewLog([]wire.FoundationLogEntry{invalid}); err == nil {
			t.Fatal("NewLog() accepted an unknown kind")
		}
	})
}

func TestFoundationLogSignHeadValidation(t *testing.T) {
	seed := []byte{128, 129, 130, 131, 132, 133, 134, 135, 136, 137, 138, 139, 140, 141, 142, 143, 144, 145, 146, 147, 148, 149, 150, 151, 152, 153, 154, 155, 156, 157, 158, 159}
	key, err := keys.NewKeyFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	id := keys.DIDOf(key)
	t.Run("empty log uses zero root", func(t *testing.T) {
		head, err := SignHead(key, id, "0000000000000000000000000000000000000000000000000000000000000000", 0, time.Date(2027, 1, 20, 0, 0, 0, 0, time.UTC))
		if err != nil || head.Size != 0 {
			t.Fatalf("SignHead() = %+v, %v", head, err)
		}
	})
	t.Run("empty log rejects other root", func(t *testing.T) {
		if _, err := SignHead(key, id, "1111111111111111111111111111111111111111111111111111111111111111", 0, time.Date(2027, 1, 20, 0, 0, 0, 0, time.UTC)); err == nil {
			t.Fatal("SignHead() accepted a nonzero empty root")
		}
	})
	t.Run("wrong foundation ID", func(t *testing.T) {
		if _, err := SignHead(key, "did:key:wrong", "1111111111111111111111111111111111111111111111111111111111111111", 1, time.Date(2027, 1, 20, 0, 0, 0, 0, time.UTC)); err == nil {
			t.Fatal("SignHead() accepted a mismatched foundation ID")
		}
	})
	t.Run("time outside canonical year range", func(t *testing.T) {
		if _, err := SignHead(key, id, "0000000000000000000000000000000000000000000000000000000000000000", 0, time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)); err == nil {
			t.Fatal("SignHead() accepted a time outside the protocol range")
		}
	})
}
