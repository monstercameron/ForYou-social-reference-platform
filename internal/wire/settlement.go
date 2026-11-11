package wire

import "encoding/json"

// SettlementLeaf is a receipt commitment in the Claims and leaves section of docs/03-economics.md.
type SettlementLeaf struct {
	ReceiptHash     string `json:"receipt_hash"`
	ScheduleID      string `json:"schedule_id"`
	AmountBaseUnits string `json:"amount_base_units"`
	IssuedAt        string `json:"issued_at"`
}

// SettlementClaim is a provider's signed accounting record in the Claims and leaves section of docs/03-economics.md.
type SettlementClaim struct {
	ClaimID             string  `json:"claim_id"`
	ProviderID          string  `json:"provider_id"`
	Network             string  `json:"network"`
	Sequence            int64   `json:"sequence"`
	PreviousClaimID     *string `json:"previous_claim_id"`
	PeriodStart         string  `json:"period_start"`
	PeriodEnd           string  `json:"period_end"`
	DeclarationID       string  `json:"declaration_id"`
	SuccessionID        *string `json:"succession_id"`
	ReceiptCount        int64   `json:"receipt_count"`
	TotalBaseUnits      string  `json:"total_base_units"`
	CumulativeBaseUnits string  `json:"cumulative_base_units"`
	ReceiptsRoot        string  `json:"receipts_root"`
	ProviderSignature   string  `json:"provider_signature"`
}

// AuditNode is an audit_path item in SettlementProof, described in Proofs and receipt states in docs/03-economics.md.
type AuditNode struct {
	Hash string `json:"hash"`
	Sum  string `json:"sum"`
}

// SettlementProof is an inclusion proof described in Proofs and receipt states in docs/03-economics.md.
type SettlementProof struct {
	Claim     SettlementClaim     `json:"claim"`
	Leaf      SettlementLeaf      `json:"leaf"`
	LeafIndex int64               `json:"leaf_index"`
	AuditPath []AuditNode         `json:"audit_path"`
	Witnesses []SettlementWitness `json:"witnesses"`
	Chain     []SettlementClaim   `json:"chain,omitempty"`
}

// SettlementWitness is a monitor's attestation described in The settlement committee in docs/03-economics.md.
type SettlementWitness struct {
	WitnessID           string `json:"witness_id"`
	ClaimID             string `json:"claim_id"`
	ProviderID          string `json:"provider_id"`
	Network             string `json:"network"`
	Sequence            int64  `json:"sequence"`
	PeriodEnd           string `json:"period_end"`
	TotalBaseUnits      string `json:"total_base_units"`
	CumulativeBaseUnits string `json:"cumulative_base_units"`
	ReceiptsRoot        string `json:"receipts_root"`
	CreditBaseUnits     string `json:"credit_base_units"`
	WitnessedAt         string `json:"witnessed_at"`
	WitnessSignature    string `json:"witness_signature"`
}

// SettlementRefusal is a monitor's signed refusal described in The settlement committee in docs/03-economics.md.
type SettlementRefusal struct {
	WitnessID        string `json:"witness_id"`
	ClaimID          string `json:"claim_id"`
	ProviderID       string `json:"provider_id"`
	Network          string `json:"network"`
	Sequence         int64  `json:"sequence"`
	Reason           string `json:"reason"`
	RefusedAt        string `json:"refused_at"`
	WitnessSignature string `json:"witness_signature"`
}

// SettlementHold is a monitor's signed hold described in The settlement committee in docs/03-economics.md.
type SettlementHold struct {
	WitnessID        string   `json:"witness_id"`
	ProviderID       string   `json:"provider_id"`
	Network          string   `json:"network"`
	Sequence         int64    `json:"sequence"`
	ClaimIDs         []string `json:"claim_ids"`
	HeldAt           string   `json:"held_at"`
	WitnessSignature string   `json:"witness_signature"`
}

// SettlementClaimsPage is the claims response described in Settlement in docs/03-economics.md.
type SettlementClaimsPage struct {
	Claims []SettlementClaim `json:"claims"`
}

// SettlementLeavesPage is the leaves response described in Claims and leaves in docs/03-economics.md.
type SettlementLeavesPage struct {
	ClaimID string           `json:"claim_id"`
	Offset  int64            `json:"offset"`
	Leaves  []SettlementLeaf `json:"leaves"`
}

// SettlementWitnessesPage is the witnesses response described in The settlement committee in docs/03-economics.md.
type SettlementWitnessesPage struct {
	Witnesses []SettlementWitness `json:"witnesses"`
}

// MonitorWitnessesPage is a monitor's settlement records response described in The settlement committee in docs/03-economics.md.
type MonitorWitnessesPage struct {
	Witnesses []SettlementWitness `json:"witnesses"`
	Refusals  []SettlementRefusal `json:"refusals"`
	Holds     []SettlementHold    `json:"holds"`
	Claims    []SettlementClaim   `json:"claims"`
}

// EquivocationProof is evidence of conflicting claims described in The settlement committee in docs/03-economics.md.
type EquivocationProof struct {
	Claims      []SettlementClaim `json:"claims"`
	Predecessor *SettlementClaim  `json:"predecessor"`
	Successions []KeySuccession   `json:"successions"`
}

// EquivocationsPage is the equivocation evidence response described in The settlement committee in docs/03-economics.md.
type EquivocationsPage struct {
	Proofs []EquivocationProof `json:"proofs"`
}

// VaultDelivery is a delivered message and its settlement proof described in Retention and the Lifetime Vault in docs/03-economics.md.
type VaultDelivery struct {
	Message json.RawMessage `json:"message"`
	Proof   SettlementProof `json:"proof"`
}
