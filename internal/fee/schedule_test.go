package fee

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"math/big"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

type syntheticReleaseKey struct {
	id  string
	key ed25519.PrivateKey
}

func syntheticReleaseKeys(t *testing.T) []syntheticReleaseKey {
	t.Helper()
	derive := func(n int) syntheticReleaseKey {
		seed := sha256.Sum256([]byte("synthetic release key " + strconv.Itoa(n)))
		key, err := keys.NewKeyFromSeed(seed[:])
		if err != nil {
			t.Fatal(err)
		}
		return syntheticReleaseKey{id: keys.DIDOf(key), key: key}
	}
	keysByID := []syntheticReleaseKey{derive(0), derive(1), derive(2), derive(3), derive(4)}
	sort.Slice(keysByID, func(i, j int) bool { return keysByID[i].id < keysByID[j].id })
	return keysByID
}

func signedRevision(t *testing.T, previous wire.FeeSchedule, perKiB, base, effective, approved string, drift bool) (wire.FeeSchedule, wire.FeeScheduleApproval, []string) {
	t.Helper()
	s := previous
	s.ScheduleID = ""
	s.Revision = previous.Revision + 1
	s.BaseFeeBaseUnits = base
	s.PerKiBFeeBaseUnits = perKiB
	s.EffectiveAt = effective
	s.ExpiresAt = "2029-01-01T00:00:00Z"
	var err error
	s.ScheduleID, err = ScheduleID(s)
	if err != nil {
		t.Fatal(err)
	}
	a := wire.FeeScheduleApproval{ScheduleID: s.ScheduleID, ApprovedAt: approved, Drift: drift}
	keysByID := syntheticReleaseKeys(t)
	signed, err := canon.EncodeWithout(a, "release_signatures")
	if err != nil {
		t.Fatal(err)
	}
	a.ReleaseSignatures = []wire.ReleaseSignature{}
	ids := []string{}
	forIndex := func(i int) {
		entry := keysByID[i]
		ids = append(ids, entry.id)
		if i < 4 {
			signature, signErr := keys.Sign(entry.key, "fee-schedule-approval", signed)
			if signErr != nil {
				t.Fatal(signErr)
			}
			a.ReleaseSignatures = append(a.ReleaseSignatures, wire.ReleaseSignature{ReleaseKeyID: entry.id, Signature: signature})
		}
	}
	forIndex(0)
	forIndex(1)
	forIndex(2)
	forIndex(3)
	forIndex(4)
	return s, a, ids
}

func readGenesis(t *testing.T) wire.GenesisManifest {
	t.Helper()
	b, err := os.ReadFile("testdata/settlement.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Genesis wire.GenesisManifest `json:"genesis_manifest"`
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	return doc.Genesis
}

func TestScheduleIDsAndValidation(t *testing.T) {
	g := readGenesis(t)
	t.Run("genesis bitcoin", func(t *testing.T) {
		id, err := ScheduleID(g.FeeSchedules[0])
		if err != nil || id != "fyfs2:4c6b3daa8dd073011c7b347468b8c530413b37b9ecbabe79ad443ae1588a5ede" {
			t.Fatalf("ScheduleID = %s, %v", id, err)
		}
	})
	t.Run("genesis ethereum", func(t *testing.T) {
		id, err := ScheduleID(g.FeeSchedules[1])
		if err != nil || id != "fyfs2:8111419e42dd20bb0a02395af966a20c51a78c425bbbb585d4b29a53b66fb98d" {
			t.Fatalf("ScheduleID = %s, %v", id, err)
		}
	})
	t.Run("published schedule example", func(t *testing.T) {
		b, err := os.ReadFile("testdata/fee-schedule.example.json")
		if err != nil {
			t.Fatal(err)
		}
		var doc struct {
			Schedule wire.FeeSchedule `json:"schedule"`
		}
		if err = json.Unmarshal(b, &doc); err != nil {
			t.Fatal(err)
		}
		id, err := ScheduleID(doc.Schedule)
		if err != nil || id != "fyfs2:28f524f836f7e81dc358cf5d86711185cad127a876cec0b1d770db471a5dc854" {
			t.Fatalf("ScheduleID = %s, %v", id, err)
		}
	})
	t.Run("both rates zero", func(t *testing.T) {
		s := g.FeeSchedules[0]
		s.BaseFeeBaseUnits = "0"
		s.PerKiBFeeBaseUnits = "0"
		s.ScheduleID, _ = ScheduleID(s)
		if ValidateSchedule(s) == nil {
			t.Fatal("zero-rate schedule accepted")
		}
	})
	t.Run("unsupported minor version", func(t *testing.T) {
		s := g.FeeSchedules[0]
		s.ProfileMinor = 1
		s.ScheduleID, _ = ScheduleID(s)
		if ValidateSchedule(s) == nil {
			t.Fatal("minor version 1 accepted")
		}
	})
	t.Run("expired schedule remains structurally valid", func(t *testing.T) {
		s := g.FeeSchedules[0]
		s.ExpiresAt = "2026-11-01T00:00:00Z"
		s.ScheduleID, _ = ScheduleID(s)
		if err := ValidateSchedule(s); err != nil {
			t.Fatalf("ValidateSchedule = %v", err)
		}
	})
}

func TestApprovalsAndRevisionBound(t *testing.T) {
	g := readGenesis(t)
	t.Run("genesis approvals", func(t *testing.T) {
		if err := VerifyApproval(g.FeeSchedules[0], g.FeeScheduleApprovals[0], g.ReleaseKeyIDs, int(g.ReleaseThreshold), nil); err != nil {
			t.Fatal(err)
		}
		if err := VerifyApproval(g.FeeSchedules[1], g.FeeScheduleApprovals[1], g.ReleaseKeyIDs, int(g.ReleaseThreshold), nil); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("three of five below threshold", func(t *testing.T) {
		a := g.FeeScheduleApprovals[0]
		a.ReleaseSignatures = a.ReleaseSignatures[:3]
		if VerifyApproval(g.FeeSchedules[0], a, g.ReleaseKeyIDs, 4, nil) == nil {
			t.Fatal("insufficient threshold accepted")
		}
	})
	t.Run("unsorted release signatures rejected", func(t *testing.T) {
		a := g.FeeScheduleApprovals[0]
		a.ReleaseSignatures[0], a.ReleaseSignatures[1] = a.ReleaseSignatures[1], a.ReleaseSignatures[0]
		if !errors.Is(VerifyApproval(g.FeeSchedules[0], a, g.ReleaseKeyIDs, 4, nil), ErrApproval) {
			t.Fatal("unsorted release signatures accepted")
		}
	})
	t.Run("approval for other schedule rejected", func(t *testing.T) {
		err := VerifyApproval(g.FeeSchedules[0], g.FeeScheduleApprovals[1], g.ReleaseKeyIDs, 4, nil)
		if !errors.Is(err, ErrApproval) {
			t.Fatalf("VerifyApproval error = %v; want ErrApproval", err)
		}
	})
	t.Run("insufficient release key set rejected", func(t *testing.T) {
		s, a, ids := signedRevision(t, g.FeeSchedules[0], "136", "0", "2027-07-01T00:00:00Z", "2027-04-01T00:00:00Z", false)
		if err := VerifyApproval(s, a, ids[:3], 4, &g.FeeSchedules[0]); !errors.Is(err, ErrApproval) {
			t.Fatalf("VerifyApproval error = %v; want ErrApproval", err)
		}
	})
	t.Run("bad in-set signature rejected despite threshold signatures", func(t *testing.T) {
		s, a, ids := signedRevision(t, g.FeeSchedules[0], "136", "0", "2027-07-01T00:00:00Z", "2027-04-01T00:00:00Z", false)
		keysByID := syntheticReleaseKeys(t)
		a.ReleaseSignatures = append(a.ReleaseSignatures, wire.ReleaseSignature{ReleaseKeyID: keysByID[4].id, Signature: g.FeeScheduleApprovals[1].ReleaseSignatures[0].Signature})
		ids = append(ids, keysByID[4].id)
		sort.Strings(ids)
		sort.Slice(a.ReleaseSignatures, func(i, j int) bool { return a.ReleaseSignatures[i].ReleaseKeyID < a.ReleaseSignatures[j].ReleaseKeyID })
		if !errors.Is(VerifyApproval(s, a, ids, 4, &g.FeeSchedules[0]), ErrApproval) {
			t.Fatal("bad in-set signature accepted")
		}
	})
	t.Run("notice period enforced", func(t *testing.T) {
		a := g.FeeScheduleApprovals[0]
		a.ApprovedAt = "2026-12-01T00:00:00Z"
		err := VerifyApproval(g.FeeSchedules[0], a, g.ReleaseKeyIDs, 4, nil)
		if err == nil || !strings.Contains(err.Error(), "notice") {
			t.Fatalf("VerifyApproval error = %v; want notice error", err)
		}
	})
	t.Run("revision one cannot use drift", func(t *testing.T) {
		a := g.FeeScheduleApprovals[0]
		a.Drift = true
		if err := VerifyApproval(g.FeeSchedules[0], a, g.ReleaseKeyIDs, 4, nil); err == nil {
			t.Fatal("drift revision one was accepted")
		}
	})
	t.Run("schedule set rejects unpaired revision", func(t *testing.T) {
		s := append([]wire.FeeSchedule(nil), g.FeeSchedules...)
		next := s[0]
		next.Revision = 2
		next.EffectiveAt = "2028-01-01T00:00:00Z"
		next.ExpiresAt = "2029-01-01T00:00:00Z"
		next.ScheduleID, _ = ScheduleID(next)
		s = append(s, next)
		if CheckScheduleSet(s) == nil {
			t.Fatal("unpaired revision accepted")
		}
	})
	t.Run("schedule set accepts synchronized revisions", func(t *testing.T) {
		s := append([]wire.FeeSchedule(nil), g.FeeSchedules...)
		next := s[0]
		next.Revision = 2
		next.EffectiveAt = "2028-01-01T00:00:00Z"
		next.ExpiresAt = "2029-01-01T00:00:00Z"
		next.ScheduleID, _ = ScheduleID(next)
		s = append(s, next)
		partner := s[1]
		partner.Revision = 2
		partner.EffectiveAt = next.EffectiveAt
		partner.ExpiresAt = next.ExpiresAt
		partner.ScheduleID, _ = ScheduleID(partner)
		s = append(s, partner)
		if err := CheckScheduleSet(s); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("revision fee bounds", func(t *testing.T) {
		check := func(t *testing.T, perKiB, base string, wantErr bool) {
			t.Helper()
			s, a, ids := signedRevision(t, g.FeeSchedules[0], perKiB, base, "2027-07-01T00:00:00Z", "2027-04-01T00:00:00Z", false)
			err := VerifyApproval(s, a, ids[:4], 4, &g.FeeSchedules[0])
			if wantErr && !errors.Is(err, ErrRevisionBound) {
				t.Fatalf("VerifyApproval error = %v; want ErrRevisionBound", err)
			}
			if !wantErr && err != nil {
				t.Fatal(err)
			}
		}
		t.Run("half rate accepted", func(t *testing.T) { check(t, "68", "0", false) })
		t.Run("unchanged rate accepted", func(t *testing.T) { check(t, "136", "0", false) })
		t.Run("double rate accepted", func(t *testing.T) { check(t, "272", "0", false) })
		t.Run("base fee within bound accepted", func(t *testing.T) { check(t, "136", "100", false) })
		t.Run("combined rate within bound accepted", func(t *testing.T) { check(t, "100", "36", false) })
		t.Run("below half rejected", func(t *testing.T) { check(t, "67", "0", true) })
		t.Run("above double rejected", func(t *testing.T) { check(t, "273", "0", true) })
		t.Run("maximum size above double rejected", func(t *testing.T) { check(t, "100000", "0", true) })
		t.Run("base fee pushes one kib above double", func(t *testing.T) { check(t, "136", "137", true) })
		t.Run("base fee pulls maximum below half", func(t *testing.T) { check(t, "1", "135", true) })
	})
	t.Run("revision spacing", func(t *testing.T) {
		t.Run("drift spacing accepted", func(t *testing.T) {
			s, a, ids := signedRevision(t, g.FeeSchedules[0], "136", "0", "2027-06-24T00:00:00Z", "2027-06-10T00:00:00Z", true)
			if err := VerifyApproval(s, a, ids[:4], 4, &g.FeeSchedules[0]); err != nil {
				t.Fatal(err)
			}
		})
		t.Run("non-drift spacing rejected", func(t *testing.T) {
			s, a, ids := signedRevision(t, g.FeeSchedules[0], "136", "0", "2027-03-15T00:00:00Z", "2027-01-05T00:00:00Z", false)
			if err := VerifyApproval(s, a, ids[:4], 4, &g.FeeSchedules[0]); !errors.Is(err, ErrRevisionBound) {
				t.Fatalf("VerifyApproval error = %v; want ErrRevisionBound", err)
			}
		})
	})
}

func TestScheduleInForceAndCustodyCap(t *testing.T) {
	g := readGenesis(t)
	s := g.FeeSchedules[0]
	next := s
	next.Revision = 2
	next.EffectiveAt = "2028-01-01T00:00:00Z"
	next.ExpiresAt = "2029-01-01T00:00:00Z"
	next.ScheduleID, _ = ScheduleID(next)
	t.Run("just before replacement returns genesis", func(t *testing.T) {
		got, err := InForce([]wire.FeeSchedule{s, next}, s.Network, time.Date(2027, 12, 31, 23, 59, 59, 0, time.UTC))
		if err != nil || got.ScheduleID != s.ScheduleID {
			t.Fatalf("InForce = %v, %v", got, err)
		}
	})
	t.Run("replacement instant returns revision two", func(t *testing.T) {
		got, err := InForce([]wire.FeeSchedule{s, next}, s.Network, time.Date(2028, 1, 1, 0, 0, 0, 0, time.UTC))
		if err != nil || got.ScheduleID != next.ScheduleID {
			t.Fatalf("InForce = %v, %v", got, err)
		}
	})
	t.Run("before first schedule", func(t *testing.T) {
		_, err := InForce([]wire.FeeSchedule{s}, s.Network, time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC))
		if !errors.Is(err, ErrNoScheduleInForce) {
			t.Fatalf("InForce error = %v", err)
		}
	})
	t.Run("bitcoin cap", func(t *testing.T) {
		limit, err := CustodyCap(s)
		if err != nil || limit.String() != "136000000" {
			t.Fatalf("CustodyCap = %v, %v", limit, err)
		}
		allowed, err := DepositAllowed(s, new(big.Int), new(big.Int), big.NewInt(136000000))
		if err != nil || !allowed {
			t.Fatalf("DepositAllowed = %v, %v", allowed, err)
		}
		allowed, _ = DepositAllowed(s, new(big.Int), new(big.Int), big.NewInt(136000001))
		if allowed {
			t.Fatal("over-cap deposit accepted")
		}
		allowed, err = DepositAllowed(s, big.NewInt(135999990), big.NewInt(5), big.NewInt(6))
		if err != nil || allowed {
			t.Fatalf("over-cap combined balance = %v, %v", allowed, err)
		}
	})
	t.Run("base-only schedule cap", func(t *testing.T) {
		base := s
		base.BaseFeeBaseUnits = "10"
		base.PerKiBFeeBaseUnits = "0"
		limit, err := CustodyCap(base)
		if err != nil || limit.String() != "10000000" {
			t.Fatalf("CustodyCap = %v, %v", limit, err)
		}
	})
	t.Run("ethereum cap", func(t *testing.T) {
		limit, err := CustodyCap(g.FeeSchedules[1])
		if err != nil || limit.String() != "48900000000000000" {
			t.Fatalf("CustodyCap = %v, %v", limit, err)
		}
	})
}
