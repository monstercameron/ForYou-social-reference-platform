package committee

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/retention"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/succession"
	"foryou.local/foryou/internal/wire"
)

type settlementExample struct {
	Genesis struct {
		FeeSchedules         []wire.FeeSchedule         `json:"fee_schedules"`
		FeeScheduleApprovals []wire.FeeScheduleApproval `json:"fee_schedule_approvals"`
		ReleaseKeyIDs        []string                   `json:"release_key_ids"`
		ReleaseThreshold     int                        `json:"release_threshold"`
	} `json:"genesis_manifest"`
	Declarations    []wire.RetentionDeclaration `json:"retention_declarations"`
	Claim           wire.SettlementClaim        `json:"settlement_claim"`
	Leaves          []wire.SettlementLeaf       `json:"settlement_leaves"`
	SecondClaim     wire.SettlementClaim        `json:"second_claim"`
	SecondWitnesses []wire.SettlementWitness    `json:"second_claim_witnesses"`
}

// fakeDeposits returns deposits by the key it is asked for and records every key it was asked for.
type fakeDeposits struct {
	amounts map[string]*big.Int
	asked   map[string]int
}

func (d *fakeDeposits) Deposited(firstKey, network string) (*big.Int, error) {
	d.asked[firstKey]++
	amount, ok := d.amounts[firstKey+" "+network]
	if !ok {
		return new(big.Int), nil
	}
	return new(big.Int).Set(amount), nil
}

func (d *fakeDeposits) set(firstKey, network string, amount int64) {
	d.amounts[firstKey+" "+network] = big.NewInt(amount)
}

// world is the model test fixture: the example's schedules, declarations and first claim, the specification's
// provider keys and five synthetic monitors sharing one deposit source.
type world struct {
	example                           settlementExample
	schedules                         *VerifiedSchedules
	deposits                          *fakeDeposits
	provider, providerB, providerNext keys.PrivateKey
	declA, declB                      wire.RetentionDeclaration
	btcSchedule, ethSchedule          string
	monitors                          [5]*Monitor
}

const (
	btc = wire.BitcoinMainnet
	eth = wire.EthereumMainnet
)

func newWorld(t *testing.T) *world {
	t.Helper()
	data, err := os.ReadFile("testdata/settlement.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var example settlementExample
	if err := json.Unmarshal(data, &example); err != nil {
		t.Fatal(err)
	}
	g := example.Genesis
	schedules, err := NewSchedules([]wire.ApprovedFeeSchedule{
		{Schedule: g.FeeSchedules[0], Approval: g.FeeScheduleApprovals[0]},
		{Schedule: g.FeeSchedules[1], Approval: g.FeeScheduleApprovals[1]},
	}, g.ReleaseKeyIDs, g.ReleaseThreshold)
	if err != nil {
		t.Fatal(err)
	}
	w := &world{
		example:      example,
		schedules:    schedules,
		deposits:     &fakeDeposits{amounts: map[string]*big.Int{}, asked: map[string]int{}},
		provider:     seedKey(t, 32),
		providerB:    seedKey(t, 160),
		providerNext: seedKey(t, 192),
		declA:        example.Declarations[0],
		declB:        example.Declarations[1],
		btcSchedule:  g.FeeSchedules[0].ScheduleID,
		ethSchedule:  g.FeeSchedules[1].ScheduleID,
	}
	if g.FeeSchedules[0].Network != btc || g.FeeSchedules[1].Network != eth {
		t.Fatal("example fee schedules are not bitcoin then ethereum")
	}
	if w.declA.ProviderID != keys.DIDOf(w.provider) || w.declB.ProviderID != keys.DIDOf(w.providerB) || example.Claim.ProviderID != keys.DIDOf(w.provider) {
		t.Fatal("example objects are not signed by PROVIDER and PROVIDER_B")
	}
	w.monitors = [5]*Monitor{w.newMonitor(t, "0"), w.newMonitor(t, "1"), w.newMonitor(t, "2"), w.newMonitor(t, "3"), w.newMonitor(t, "4")}
	w.deposits.set(keys.DIDOf(w.provider), btc, 10_000_000)
	return w
}

func (w *world) newMonitor(t *testing.T, n string) *Monitor {
	t.Helper()
	seed := sha256.Sum256([]byte("synthetic monitor " + n))
	key, err := keys.NewKeyFromSeed(seed[:])
	if err != nil {
		t.Fatal(err)
	}
	m, err := New(Config{Key: key, Schedules: w.schedules, Deposits: w.deposits})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterDeclaration(w.declA); err != nil {
		t.Fatal(err)
	}
	if err := m.RegisterDeclaration(w.declB); err != nil {
		t.Fatal(err)
	}
	return m
}

func (w *world) config(m *Monitor) Config {
	return Config{Key: m.key, Schedules: w.schedules, Deposits: w.deposits}
}

func seedKey(t *testing.T, start byte) keys.PrivateKey {
	t.Helper()
	var seed []byte
	switch start {
	case 32:
		seed = []byte{32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63}
	case 160:
		seed = []byte{160, 161, 162, 163, 164, 165, 166, 167, 168, 169, 170, 171, 172, 173, 174, 175, 176, 177, 178, 179, 180, 181, 182, 183, 184, 185, 186, 187, 188, 189, 190, 191}
	case 192:
		seed = []byte{192, 193, 194, 195, 196, 197, 198, 199, 200, 201, 202, 203, 204, 205, 206, 207, 208, 209, 210, 211, 212, 213, 214, 215, 216, 217, 218, 219, 220, 221, 222, 223}
	default:
		t.Fatalf("unknown seed start %d", start)
	}
	key, err := keys.NewKeyFromSeed(seed)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func at(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := wire.ParseTime(s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// leaf is a synthetic leaf whose receipt hash is the SHA-256 of label.
func leaf(label, scheduleID, amount, issuedAt string) wire.SettlementLeaf {
	sum := sha256.Sum256([]byte(label))
	return wire.SettlementLeaf{ReceiptHash: hex.EncodeToString(sum[:]), ScheduleID: scheduleID, AmountBaseUnits: amount, IssuedAt: issuedAt}
}

// claimSpec describes a claim to sign; zero values mean the PROVIDER key, bitcoin and the example's declaration.
type claimSpec struct {
	key         keys.PrivateKey
	network     string
	previous    *wire.SettlementClaim
	start, end  string
	declaration string
	succession  *string
	leaves      []wire.SettlementLeaf
}

func (w *world) claim(t *testing.T, s claimSpec) (wire.SettlementClaim, []wire.SettlementLeaf) {
	t.Helper()
	if s.key == nil {
		s.key = w.provider
	}
	if s.network == "" {
		s.network = btc
	}
	if s.declaration == "" {
		s.declaration = w.declA.DeclarationID
	}
	leaves := settlement.SortLeaves(s.leaves)
	root, err := settlement.TreeRoot(leaves)
	if err != nil {
		t.Fatal(err)
	}
	c, err := settlement.BuildClaim(s.key, settlement.ClaimInput{
		Network: s.network, Previous: s.previous, PeriodStart: at(t, s.start), PeriodEnd: at(t, s.end),
		DeclarationID: s.declaration, SuccessionID: s.succession, ReceiptCount: len(leaves),
		Total: root.Sum, ReceiptsRoot: hex.EncodeToString(root.Hash[:]),
	})
	if err != nil {
		t.Fatal(err)
	}
	return c, leaves
}

// resign signs a claim whose fields were edited, so that only the edited rule can fail.
func resign(t *testing.T, key keys.PrivateKey, c wire.SettlementClaim) wire.SettlementClaim {
	t.Helper()
	id, err := settlement.ClaimID(c)
	if err != nil {
		t.Fatal(err)
	}
	c.ClaimID = id
	body, err := canon.EncodeWithout(c, "provider_signature")
	if err != nil {
		t.Fatal(err)
	}
	if c.ProviderSignature, err = keys.Sign(key, "settlement-claim", body); err != nil {
		t.Fatal(err)
	}
	return c
}

func (w *world) declare(t *testing.T, key keys.PrivateKey, effectiveAt string) wire.RetentionDeclaration {
	t.Helper()
	d, err := retention.Declare(key, retention.MinSeconds, w.declA.PayoutRecipients, at(t, effectiveAt))
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func succeed(t *testing.T, oldKey, newKey keys.PrivateKey, effectiveAt string) wire.KeySuccession {
	t.Helper()
	s, err := succession.New(oldKey, newKey, at(t, effectiveAt))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func audit(t *testing.T, m *Monitor, c wire.SettlementClaim, leaves []wire.SettlementLeaf, seenAt string) Outcome {
	t.Helper()
	o, err := m.Audit(c, leaves, at(t, seenAt), at(t, seenAt))
	if err != nil {
		t.Fatal(err)
	}
	return o
}

// result is a witness's credit, or "refused " and the refusal reason.
func result(o Outcome) string {
	if o.Witness == nil {
		return "refused " + o.Refusal.Reason
	}
	return o.Witness.CreditBaseUnits
}

// auditAll has every committee monitor audit the claim, as the model's committee_audit, and returns each result.
func (w *world) auditAll(t *testing.T, c wire.SettlementClaim, leaves []wire.SettlementLeaf, seenAt string) [5]string {
	t.Helper()
	return [5]string{
		result(audit(t, w.monitors[0], c, leaves, seenAt)),
		result(audit(t, w.monitors[1], c, leaves, seenAt)),
		result(audit(t, w.monitors[2], c, leaves, seenAt)),
		result(audit(t, w.monitors[3], c, leaves, seenAt)),
		result(audit(t, w.monitors[4], c, leaves, seenAt)),
	}
}

func five(s string) [5]string { return [5]string{s, s, s, s, s} }

// primed returns a world in which all five monitors witnessed the example's first claim (cumulative 10472).
func primed(t *testing.T) *world {
	t.Helper()
	w := newWorld(t)
	if got := w.auditAll(t, w.example.Claim, w.example.Leaves, "2027-01-06T12:00:00Z"); got != five("9989528") {
		t.Fatalf("first claim results = %v", got)
	}
	return w
}

// second builds a valid sequence-2 claim on the example's first claim with one leaf of 136 base units.
func (w *world) second(t *testing.T, label string) (wire.SettlementClaim, []wire.SettlementLeaf) {
	t.Helper()
	return w.claim(t, claimSpec{previous: &w.example.Claim, start: "2027-01-12T00:00:00Z", end: "2027-01-13T00:00:00Z",
		leaves: []wire.SettlementLeaf{leaf(label, w.btcSchedule, "136", "2027-01-12T06:00:00Z")}})
}
