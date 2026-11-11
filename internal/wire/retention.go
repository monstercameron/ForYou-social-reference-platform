package wire

// PayoutRecipients is the payout_recipients object in RetentionDeclaration, described in Provider retention in docs/03-economics.md.
type PayoutRecipients struct {
	BitcoinMainnet  string `json:"bitcoin-mainnet,omitempty"`
	EthereumMainnet string `json:"ethereum-mainnet,omitempty"`
}

// RetentionDeclaration is a provider's signed retention commitment described in Provider retention in docs/03-economics.md.
type RetentionDeclaration struct {
	DeclarationID     string           `json:"declaration_id"`
	ProviderID        string           `json:"provider_id"`
	RetentionSeconds  int64            `json:"retention_seconds"`
	PayoutRecipients  PayoutRecipients `json:"payout_recipients"`
	EffectiveAt       string           `json:"effective_at"`
	ProviderSignature string           `json:"provider_signature"`
}

// RetentionDeclarationsPage is the retention declaration response described in Provider retention in docs/03-economics.md.
type RetentionDeclarationsPage struct {
	Declarations []RetentionDeclaration `json:"declarations"`
}
