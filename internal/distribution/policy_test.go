package distribution

import (
	"encoding/json"
	"errors"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

type policyVerifier struct {
	reject   bool
	calls    int
	label    string
	signedAt time.Time
	unsigned any
	sigs     []wire.ReleaseSignature
	signed   any
}

func (v *policyVerifier) VerifyRelease(label string, unsigned any, sigs []wire.ReleaseSignature, signedAt time.Time, signed any) error {
	v.calls++
	v.label = label
	v.signedAt = signedAt
	v.unsigned = unsigned
	v.sigs = sigs
	v.signed = signed
	if v.reject {
		return os.ErrPermission
	}
	return nil
}

func examplePolicy(t *testing.T) wire.DistributionPolicy {
	t.Helper()
	// Source: examples/settlement.example.json in the ForYou specification repository.
	b, err := os.ReadFile("testdata/settlement.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var example struct {
		GenesisManifest struct {
			DistributionPolicy wire.DistributionPolicy `json:"distribution_policy"`
		} `json:"genesis_manifest"`
	}
	if err := json.Unmarshal(b, &example); err != nil {
		t.Fatal(err)
	}
	return example.GenesisManifest.DistributionPolicy
}

func TestPolicyIDAndCheck(t *testing.T) {
	p := examplePolicy(t)
	t.Run("example identifier and approval", func(t *testing.T) {
		id, err := PolicyID(p)
		if err != nil || id != "fydp2:1653998b4c4e2cc1ea65cfd738ebdf79ef631e711df09e202df3b617fb662a2e" {
			t.Fatalf("PolicyID() = %q, %v", id, err)
		}
		v := &policyVerifier{}
		if err := CheckPolicy(p, nil, v); err != nil {
			t.Fatal(err)
		}
		if v.calls != 1 || v.label != "distribution-policy" || v.signedAt.Format(time.RFC3339) != p.ApprovedAt {
			t.Fatalf("verifier call = %+v", v)
		}
		unsignedJSON, err := canon.Encode(v.unsigned)
		if err != nil {
			t.Fatal(err)
		}
		wantUnsigned, err := canon.EncodeWithout(p, "release_signatures")
		if err != nil || string(unsignedJSON) != string(wantUnsigned) {
			t.Fatalf("unsigned policy = %s, want %s, error %v", unsignedJSON, wantUnsigned, err)
		}
		signedJSON, err := canon.Encode(v.signed)
		fullJSON, fullErr := canon.Encode(p)
		if len(v.sigs) != len(p.ReleaseSignatures) || err != nil || fullErr != nil || string(signedJSON) != string(fullJSON) {
			t.Fatalf("signatures or signed policy do not match example")
		}
	})
	t.Run("reject verifier refusal", func(t *testing.T) {
		v := &policyVerifier{reject: true}
		if err := CheckPolicy(p, nil, v); !errors.Is(err, ErrReleaseApproval) {
			t.Fatal("accepted rejected approval")
		}
	})
	t.Run("tampered identifier", func(t *testing.T) {
		p := examplePolicy(t)
		p.PolicyID = "fydp2:" + strings.Repeat("0", 64)
		if err := CheckPolicy(p, nil, &policyVerifier{}); !errors.Is(err, ErrPolicyID) {
			t.Fatal("accepted tampered ID")
		}
	})
	t.Run("unsorted monitors", func(t *testing.T) {
		p := examplePolicy(t)
		p.Monitors[0], p.Monitors[1] = p.Monitors[1], p.Monitors[0]
		p = withID(t, p)
		if err := CheckPolicy(p, nil, &policyVerifier{}); !errors.Is(err, ErrPolicyInvalid) {
			t.Fatal("accepted unsorted monitors")
		}
	})
	t.Run("duplicate monitor", func(t *testing.T) {
		p := examplePolicy(t)
		p.Monitors[1] = p.Monitors[0]
		p = withID(t, p)
		if err := CheckPolicy(p, nil, &policyVerifier{}); !errors.Is(err, ErrPolicyInvalid) {
			t.Fatal("accepted duplicate monitor")
		}
	})
	t.Run("noncanonical monitor DID", func(t *testing.T) {
		p := examplePolicy(t)
		p.Monitors[0] += "#key-1"
		p = withID(t, p)
		if err := CheckPolicy(p, nil, &policyVerifier{}); !errors.Is(err, ErrPolicyInvalid) {
			t.Fatal("accepted noncanonical DID")
		}
	})
}

func seededMonitor(t *testing.T, seedByte byte) string {
	t.Helper()
	seed := make([]byte, 32)
	seed[0] = seedByte
	key, err := keys.NewKeyFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	return keys.DIDOf(key)
}

func revisedPolicy(t *testing.T, base wire.DistributionPolicy, effective, approved string) wire.DistributionPolicy {
	t.Helper()
	base.Revision++
	base.EffectiveAt = effective
	base.ApprovedAt = approved
	base.ReleaseSignatures = examplePolicy(t).ReleaseSignatures
	base = withID(t, base)
	return base
}

func withID(t *testing.T, p wire.DistributionPolicy) wire.DistributionPolicy {
	t.Helper()
	id, err := PolicyID(p)
	if err != nil {
		t.Fatal(err)
	}
	p.PolicyID = id
	return p
}

func TestCheckPolicyTimingAndChanges(t *testing.T) {
	base := examplePolicy(t)
	t.Run("short notice denied", func(t *testing.T) {
		p := revisedPolicy(t, base, "2027-01-01T00:00:00Z", "2026-12-01T00:00:00Z")
		if err := CheckPolicy(p, nil, &policyVerifier{}); !errors.Is(err, ErrPolicyTiming) {
			t.Fatal("accepted short notice")
		}
	})
	t.Run("replacement notice", func(t *testing.T) {
		p := revisedPolicy(t, base, "2027-02-15T00:00:00Z", "2027-02-01T00:00:00Z")
		p.Monitors = append([]string(nil), base.Monitors...)
		p.Monitors[4] = seededMonitor(t, 91)
		sort.Strings(p.Monitors)
		p = withID(t, p)
		if err := CheckPolicy(p, []wire.DistributionPolicy{base}, &policyVerifier{}); err != nil {
			t.Fatal(err)
		}
		if err := CheckPolicy(p, nil, &policyVerifier{}); !errors.Is(err, ErrPolicyTiming) {
			t.Fatal("accepted replacement without predecessor")
		}
	})
	t.Run("replacement bounds and spacing", func(t *testing.T) {
		p := revisedPolicy(t, base, "2027-02-15T00:00:00Z", "2027-02-01T00:00:00Z")
		p.Monitors = []string{seededMonitor(t, 91), seededMonitor(t, 92), seededMonitor(t, 93), seededMonitor(t, 94), seededMonitor(t, 95)}
		sort.Strings(p.Monitors)
		p = withID(t, p)
		if err := CheckPolicy(p, []wire.DistributionPolicy{base}, &policyVerifier{}); !errors.Is(err, ErrCommitteeChange) {
			t.Fatal("accepted three replacements")
		}
		p.Monitors = append([]string(nil), base.Monitors...)
		p.Monitors[4] = seededMonitor(t, 91)
		sort.Strings(p.Monitors)
		p.EffectiveAt = "2027-01-20T00:00:00Z"
		p.ApprovedAt = "2027-01-06T00:00:00Z"
		p = withID(t, p)
		if err := CheckPolicy(p, []wire.DistributionPolicy{base}, &policyVerifier{}); !errors.Is(err, ErrCommitteeSpacing) {
			t.Fatal("accepted early committee change")
		}
	})
	t.Run("replacement with share change requires standard notice", func(t *testing.T) {
		p := revisedPolicy(t, base, "2027-02-15T00:00:00Z", "2027-02-01T00:00:00Z")
		p.Monitors = append([]string(nil), base.Monitors...)
		p.Monitors[4] = seededMonitor(t, 91)
		sort.Strings(p.Monitors)
		p.VaultShareBP = 9000
		p = withID(t, p)
		if err := CheckPolicy(p, []wire.DistributionPolicy{base}, &policyVerifier{}); !errors.Is(err, ErrPolicyTiming) {
			t.Fatal("accepted combined change without quarter notice")
		}
	})
	t.Run("shares policy may follow a committee change inside 30 days", func(t *testing.T) {
		initial := revisedPolicy(t, base, "2026-10-01T00:00:00Z", "2026-08-01T00:00:00Z")
		committee := revisedPolicy(t, initial, "2026-12-12T00:00:00Z", "2026-11-28T00:00:00Z")
		committee.Monitors = append([]string(nil), initial.Monitors...)
		committee.Monitors[4] = seededMonitor(t, 91)
		sort.Strings(committee.Monitors)
		committee = withID(t, committee)
		if err := CheckPolicy(committee, []wire.DistributionPolicy{initial}, &policyVerifier{}); err != nil {
			t.Fatal(err)
		}
		shares := revisedPolicy(t, committee, "2027-01-01T00:00:00Z", "2026-11-01T00:00:00Z")
		shares.VaultShareBP = 6000
		shares = withID(t, shares)
		if err := CheckPolicy(shares, []wire.DistributionPolicy{initial, committee}, &policyVerifier{}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("committee change may follow shares policy inside 30 days", func(t *testing.T) {
		initial := revisedPolicy(t, base, "2026-10-01T00:00:00Z", "2026-08-01T00:00:00Z")
		shares := revisedPolicy(t, initial, "2027-01-01T00:00:00Z", "2026-11-01T00:00:00Z")
		shares.VaultShareBP = 6000
		shares = withID(t, shares)
		change := revisedPolicy(t, shares, "2027-01-21T00:00:00Z", "2027-01-07T00:00:00Z")
		change.Monitors = append([]string(nil), shares.Monitors...)
		change.Monitors[4] = seededMonitor(t, 91)
		sort.Strings(change.Monitors)
		change = withID(t, change)
		if err := CheckPolicy(change, []wire.DistributionPolicy{initial, shares}, &policyVerifier{}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("committee spacing looks past shares-only policy", func(t *testing.T) {
		initial := revisedPolicy(t, base, "2026-10-01T00:00:00Z", "2026-08-01T00:00:00Z")
		firstChange := revisedPolicy(t, initial, "2026-12-25T00:00:00Z", "2026-12-11T00:00:00Z")
		firstChange.Monitors = append([]string(nil), initial.Monitors...)
		firstChange.Monitors[4] = seededMonitor(t, 91)
		sort.Strings(firstChange.Monitors)
		firstChange = withID(t, firstChange)
		shares := revisedPolicy(t, firstChange, "2027-01-01T00:00:00Z", "2026-11-01T00:00:00Z")
		shares.VaultShareBP = 6000
		shares = withID(t, shares)
		secondChange := revisedPolicy(t, shares, "2027-01-21T00:00:00Z", "2027-01-07T00:00:00Z")
		secondChange.Monitors = append([]string(nil), shares.Monitors...)
		secondChange.Monitors[4] = seededMonitor(t, 92)
		sort.Strings(secondChange.Monitors)
		secondChange = withID(t, secondChange)
		history := []wire.DistributionPolicy{initial, firstChange, shares}
		if err := CheckPolicy(firstChange, []wire.DistributionPolicy{initial}, &policyVerifier{}); err != nil {
			t.Fatal(err)
		}
		if err := CheckPolicy(shares, []wire.DistributionPolicy{initial, firstChange}, &policyVerifier{}); err != nil {
			t.Fatal(err)
		}
		if err := CheckPolicy(secondChange, history, &policyVerifier{}); !errors.Is(err, ErrCommitteeSpacing) {
			t.Fatalf("committee spacing error = %v", err)
		}
	})
	t.Run("quarter start required", func(t *testing.T) {
		p := revisedPolicy(t, base, "2027-02-01T00:00:00Z", "2026-11-01T00:00:00Z")
		p.VaultShareBP = 6000
		p = withID(t, p)
		if err := CheckPolicy(p, []wire.DistributionPolicy{base}, &policyVerifier{}); !errors.Is(err, ErrPolicyTiming) {
			t.Fatal("accepted non-quarter start")
		}
	})
	t.Run("invalid earlier timestamp is rejected", func(t *testing.T) {
		p := revisedPolicy(t, base, "2027-02-15T00:00:00Z", "2027-02-01T00:00:00Z")
		prior := base
		prior.EffectiveAt = "not-a-time"
		if err := CheckPolicy(p, []wire.DistributionPolicy{prior}, &policyVerifier{}); !errors.Is(err, ErrPolicyInvalid) {
			t.Fatalf("invalid earlier timestamp error = %v", err)
		}
	})
	t.Run("bounds", func(t *testing.T) {
		check := func(name string, change func(*wire.DistributionPolicy)) {
			t.Run(name, func(t *testing.T) {
				p := examplePolicy(t)
				change(&p)
				p = withID(t, p)
				if err := CheckPolicy(p, nil, &policyVerifier{}); !errors.Is(err, ErrPolicyInvalid) {
					t.Fatal("accepted invalid bounds")
				}
			})
		}
		check("monitor share", func(p *wire.DistributionPolicy) { p.MonitorShareBP = 1001 })
		check("vault share", func(p *wire.DistributionPolicy) { p.VaultShareBP = 2499 })
		check("monitor count", func(p *wire.DistributionPolicy) { p.Monitors = p.Monitors[:4] })
		check("minimum reports", func(p *wire.DistributionPolicy) { p.MinReports = 2 })
	})
}

func TestCommitteeChange(t *testing.T) {
	base := examplePolicy(t).Monitors
	t.Run("five to five two replaced", func(t *testing.T) {
		next := []string{seededMonitor(t, 91), seededMonitor(t, 92), base[2], base[3], base[4]}
		if err := CheckCommitteeChange(base, next); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("five to five three replaced", func(t *testing.T) {
		next := []string{seededMonitor(t, 91), seededMonitor(t, 92), seededMonitor(t, 93), base[3], base[4]}
		if err := CheckCommitteeChange(base, next); !errors.Is(err, ErrCommitteeChange) {
			t.Fatal("accepted three replacements")
		}
	})
	t.Run("five to seven add two", func(t *testing.T) {
		next := append(append([]string(nil), base...), seededMonitor(t, 91), seededMonitor(t, 92))
		if err := CheckCommitteeChange(base, next); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("remove and add", func(t *testing.T) {
		old := append(append([]string(nil), base...), seededMonitor(t, 91), seededMonitor(t, 92))
		next := append([]string(nil), old[2:]...)
		next = append(next, seededMonitor(t, 93))
		if err := CheckCommitteeChange(old, next); !errors.Is(err, ErrCommitteeChange) {
			t.Fatal("accepted size change with removal and addition")
		}
	})
}

func TestInForceAndMembership(t *testing.T) {
	first := examplePolicy(t)
	second := revisedPolicy(t, first, "2027-02-15T00:00:00Z", "2027-02-01T00:00:00Z")
	second.Monitors = append([]string(nil), first.Monitors...)
	second.Monitors[4] = seededMonitor(t, 91)
	sort.Strings(second.Monitors)
	second = withID(t, second)
	t.Run("policy boundaries", func(t *testing.T) {
		policies := []wire.DistributionPolicy{second, first}
		check := func(at string, want string) {
			t.Helper()
			got, ok := InForce(policies, mustTime(t, at))
			if !ok || got.PolicyID != want {
				t.Fatalf("InForce(%s) = %q, %t", at, got.PolicyID, ok)
			}
		}
		if _, ok := InForce(policies, mustTime(t, "2026-12-31T23:59:59Z")); ok {
			t.Fatal("found policy before first effective time")
		}
		check("2027-01-01T00:00:00Z", first.PolicyID)
		check("2027-02-14T23:59:59Z", first.PolicyID)
		check("2027-02-15T00:00:00Z", second.PolicyID)
		check("2027-03-01T00:00:00Z", second.PolicyID)
	})
	t.Run("quarter membership", func(t *testing.T) {
		got := Membership([]wire.DistributionPolicy{first, second}, mustTime(t, "2027-01-01T00:00:00Z"), mustTime(t, "2027-04-01T00:00:00Z"))
		wantOld := []Interval{{mustTime(t, "2027-01-01T00:00:00Z"), mustTime(t, "2027-02-15T00:00:00Z")}}
		if len(got[first.Monitors[4]]) != 1 || got[first.Monitors[4]][0] != wantOld[0] {
			t.Fatalf("old membership = %#v", got[first.Monitors[4]])
		}
		if got[second.Monitors[4]][0] != (Interval{mustTime(t, "2027-02-15T00:00:00Z"), mustTime(t, "2027-04-01T00:00:00Z")}) {
			t.Fatalf("new membership = %#v", got[second.Monitors[4]])
		}
		if got[first.Monitors[0]][0] != (Interval{mustTime(t, "2027-01-01T00:00:00Z"), mustTime(t, "2027-04-01T00:00:00Z")}) {
			t.Fatalf("continuous membership = %#v", got[first.Monitors[0]])
		}
	})
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	value, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
