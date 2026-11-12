package wire

// DistributionPolicy sets the quarterly return shares and evidence criteria described in "Distribution" in the economics specification.
type DistributionPolicy struct {
	PolicyID          string             `json:"policy_id"`
	Revision          int64              `json:"revision"`
	VaultShareBP      int64              `json:"vault_share_bp"`
	MonitorShareBP    int64              `json:"monitor_share_bp"`
	MaxRetentionYears int64              `json:"max_retention_years"`
	MinReports        int64              `json:"min_reports"`
	Monitors          []string           `json:"monitors"`
	ApprovedAt        string             `json:"approved_at"`
	EffectiveAt       string             `json:"effective_at"`
	ReleaseSignatures []ReleaseSignature `json:"release_signatures"`
}

// DistributionStatement publishes the Foundation's quarterly fee distribution described in "Distribution" in the economics specification.
type DistributionStatement struct {
	StatementID          string              `json:"statement_id"`
	FoundationID         string              `json:"foundation_id"`
	Network              string              `json:"network"`
	PeriodStart          string              `json:"period_start"`
	PeriodEnd            string              `json:"period_end"`
	PolicyID             string              `json:"policy_id"`
	ReceivedBaseUnits    string              `json:"received_base_units"`
	DistributedBaseUnits string              `json:"distributed_base_units"`
	MonitorPayments      []MonitorPayment    `json:"monitor_payments"`
	Entries              []DistributionEntry `json:"entries"`
	FoundationSignature  string              `json:"foundation_signature"`
}

// MonitorPayment records a monitor's allocation and optional payout in a DistributionStatement described in "Distribution" in the economics specification.
type MonitorPayment struct {
	MonitorID       string           `json:"monitor_id"`
	AmountBaseUnits string           `json:"amount_base_units"`
	Payout          *DepositTransfer `json:"payout"`
}

// DistributionEntry records a provider's evidence, calculated allocation, accrual, and optional payout in a DistributionStatement described in "Distribution" in the economics specification.
type DistributionEntry struct {
	ProviderID           string           `json:"provider_id"`
	SettledFeesBaseUnits string           `json:"settled_fees_base_units"`
	QualityBP            int64            `json:"quality_bp"`
	ReportIDs            []string         `json:"report_ids"`
	RetentionYears       int64            `json:"retention_years"`
	ProvenQuarters       int64            `json:"proven_quarters"`
	AmountBaseUnits      string           `json:"amount_base_units"`
	AccruedBaseUnits     string           `json:"accrued_base_units"`
	Payout               *DepositTransfer `json:"payout"`
}

// DistributionPage lists Foundation policies and statements described in "Foundation publications" in the economics specification.
type DistributionPage struct {
	Policies    []DistributionPolicy    `json:"policies"`
	Statements  []DistributionStatement `json:"statements"`
	Disclosures []MonitorDisclosure     `json:"disclosures"`
}

// MonitorDisclosure is the Foundation's unsigned statement of how one committee monitor of a policy was chosen and what ties it has, described in "Quality evidence and independence" in the governance specification.
type MonitorDisclosure struct {
	PolicyID          string                `json:"policy_id"`
	MonitorID         string                `json:"monitor_id"`
	SelectionCriteria string                `json:"selection_criteria"`
	Relationships     []MonitorRelationship `json:"relationships"`
}

// MonitorRelationship is a relationships item in MonitorDisclosure: a funding or ownership tie to the Foundation or to a provider.
type MonitorRelationship struct {
	Party       string `json:"party"`
	Kind        string `json:"kind"`
	Description string `json:"description"`
}

// FoundationEvidence is a monitor's signed report of an inconsistency in the Foundation's log, closures or deposits described in "Foundation log" in the governance specification.
type FoundationEvidence struct {
	MonitorID        string                      `json:"monitor_id"`
	Kind             string                      `json:"kind"`
	Heads            []FoundationLogHead         `json:"heads"`
	Closure          *FoundationKeyClosure       `json:"closure"`
	Transfer         *FoundationEvidenceTransfer `json:"transfer"`
	ObservedAt       string                      `json:"observed_at"`
	MonitorSignature string                      `json:"monitor_signature"`
}

// FoundationEvidenceTransfer is the transfer member of FoundationEvidence: a provider deposit funded from a Foundation recipient address.
type FoundationEvidenceTransfer struct {
	Network     string `json:"network"`
	Transaction string `json:"transaction"`
	Recipient   string `json:"recipient"`
}

// FoundationEvidencePage lists the Foundation evidence a monitor holds, described in "Foundation log" in the governance specification.
type FoundationEvidencePage struct {
	Evidence []FoundationEvidence `json:"evidence"`
}

// FoundationKeyCertificate binds a Foundation signing key to network recipients under release-key governance described in "Foundation key certificate" in the governance specification.
type FoundationKeyCertificate struct {
	CertificateID     string               `json:"certificate_id"`
	FoundationID      string               `json:"foundation_id"`
	Recipients        FoundationRecipients `json:"recipients"`
	IssuedAt          string               `json:"issued_at"`
	ValidFrom         string               `json:"valid_from"`
	ValidUntil        string               `json:"valid_until"`
	ReleaseSignatures []ReleaseSignature   `json:"release_signatures"`
}

// FoundationRecipients gives the settlement recipient for each supported network as described in "Foundation key certificate" in the governance specification.
type FoundationRecipients struct {
	BitcoinMainnet  string `json:"bitcoin-mainnet"`
	EthereumMainnet string `json:"ethereum-mainnet"`
}

// FoundationKeyClosure closes a Foundation key against its signed log head as described in "Foundation key certificate" in the governance specification.
type FoundationKeyClosure struct {
	FoundationID      string             `json:"foundation_id"`
	ClosedAt          string             `json:"closed_at"`
	Head              FoundationLogHead  `json:"head"`
	Voided            []string           `json:"voided"`
	ReleaseSignatures []ReleaseSignature `json:"release_signatures"`
}

// FoundationLogEntry identifies one signed Foundation object in the append-only log described in "Foundation log" in the governance specification.
type FoundationLogEntry struct {
	Index      int64  `json:"index"`
	Kind       string `json:"kind"`
	ObjectHash string `json:"object_hash"`
}

// FoundationLogHead signs the current append-only log root described in "Foundation log" in the governance specification.
type FoundationLogHead struct {
	FoundationID        string `json:"foundation_id"`
	Size                int64  `json:"size"`
	Root                string `json:"root"`
	SignedAt            string `json:"signed_at"`
	FoundationSignature string `json:"foundation_signature"`
}

// FoundationLogProof carries an RFC 6962 audit path for a log entry described in "Foundation log" in the governance specification.
type FoundationLogProof struct {
	Index     int64    `json:"index"`
	Size      int64    `json:"size"`
	AuditPath []string `json:"audit_path"`
}

// FoundationLogPage returns log entries and the current nullable head described in "Foundation log" in the governance specification.
type FoundationLogPage struct {
	Entries []FoundationLogEntry `json:"entries"`
	Head    *FoundationLogHead   `json:"head"`
}

// FoundationLogHeadsPage lists signed log heads described in "Foundation log" in the governance specification.
type FoundationLogHeadsPage struct {
	Heads []FoundationLogHead `json:"heads"`
}

// ReleaseKeySetUpdate changes the release-key set under the outgoing threshold described in "Release keys" in the governance specification.
type ReleaseKeySetUpdate struct {
	UpdateID          string             `json:"update_id"`
	Sequence          int64              `json:"sequence"`
	PreviousUpdateID  *string            `json:"previous_update_id"`
	ReleaseKeyIDs     []string           `json:"release_key_ids"`
	ReleaseThreshold  int64              `json:"release_threshold"`
	PriorObjects      []string           `json:"prior_objects"`
	EffectiveAt       string             `json:"effective_at"`
	ReleaseSignatures []ReleaseSignature `json:"release_signatures"`
}

// FoundationCertificatesPage lists Foundation key certificates, closures, and release-key updates described in "Foundation key certificate" in the governance specification.
type FoundationCertificatesPage struct {
	Certificates  []FoundationKeyCertificate `json:"certificates"`
	Closures      []FoundationKeyClosure     `json:"closures"`
	KeySetUpdates []ReleaseKeySetUpdate      `json:"key_set_updates"`
}

// GenesisManifest pins the initial release keys, Foundation certificate, fee schedules, and distribution policy described in "Genesis manifest" in the governance specification.
type GenesisManifest struct {
	Release                   string                     `json:"release"`
	ReleaseKeyIDs             []string                   `json:"release_key_ids"`
	ReleaseThreshold          int64                      `json:"release_threshold"`
	FoundationKeyCertificates []FoundationKeyCertificate `json:"foundation_key_certificates"`
	FeeSchedules              []FeeSchedule              `json:"fee_schedules"`
	FeeScheduleApprovals      []FeeScheduleApproval      `json:"fee_schedule_approvals"`
	DistributionPolicy        DistributionPolicy         `json:"distribution_policy"`
	SignedAt                  string                     `json:"signed_at"`
	ReleaseSignatures         []ReleaseSignature         `json:"release_signatures"`
}

// VaultStatus reports the Foundation Lifetime Vault's retained records and funding described in "Lifetime Vault" in the economics specification.
type VaultStatus struct {
	FoundationID        string         `json:"foundation_id"`
	PeriodStart         string         `json:"period_start"`
	PeriodEnd           string         `json:"period_end"`
	RecordCount         int64          `json:"record_count"`
	StoredBytes         int64          `json:"stored_bytes"`
	Funding             []VaultFunding `json:"funding"`
	FoundationSignature string         `json:"foundation_signature"`
}

// VaultFunding records incoming and spent settlement amounts by network in a VaultStatus described in "Lifetime Vault" in the economics specification.
type VaultFunding struct {
	Network           string `json:"network"`
	ReceivedBaseUnits string `json:"received_base_units"`
	SpentBaseUnits    string `json:"spent_base_units"`
}

// VaultStatusPage lists signed Lifetime Vault status records described in "Foundation publications" in the economics specification.
type VaultStatusPage struct {
	Statuses []VaultStatus `json:"statuses"`
}

// VaultReceipt confirms that the Foundation Lifetime Vault retained a message as described in "Lifetime Vault" in the economics specification.
type VaultReceipt struct {
	FoundationID        string `json:"foundation_id"`
	MessageID           string `json:"message_id"`
	StoredAt            string `json:"stored_at"`
	FoundationSignature string `json:"foundation_signature"`
}
