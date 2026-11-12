package wire

// DepositRequest asks a provider for a network deposit invoice as described in "Funding a provider-local balance" in the economics specification.
type DepositRequest struct {
	ProviderID       string `json:"provider_id"`
	AuthorID         string `json:"author_id"`
	Network          string `json:"network"`
	AmountChainUnits string `json:"amount_chain_units"`
	RequestID        string `json:"request_id"`
	NotAfter         string `json:"not_after"`
}

// DepositInvoice identifies a provider's requested chain deposit as described in "Funding the provider-local balance" in the protocol specification.
type DepositInvoice struct {
	DepositID         string `json:"deposit_id"`
	ProviderID        string `json:"provider_id"`
	AuthorID          string `json:"author_id"`
	Network           string `json:"network"`
	AmountChainUnits  string `json:"amount_chain_units"`
	Recipient         string `json:"recipient"`
	IssuedAt          string `json:"issued_at"`
	ExpiresAt         string `json:"expires_at"`
	ProviderSignature string `json:"provider_signature"`
}

// DepositTransfer identifies a Bitcoin or Ethereum transfer used by funding and payout objects described in "Funding the provider-local balance" in the protocol specification.
type DepositTransfer struct {
	Network     string `json:"network"`
	TxID        string `json:"txid,omitempty"`
	OutputIndex *int64 `json:"output_index,omitempty"`
	TxHash      string `json:"tx_hash,omitempty"`
}

// DepositProof supplies an untrusted chain lookup hint, as described in "Funding the provider-local balance" in the protocol specification.
type DepositProof struct {
	ProviderID string          `json:"provider_id"`
	AuthorID   string          `json:"author_id"`
	DepositID  string          `json:"deposit_id"`
	Transfer   DepositTransfer `json:"transfer"`
}

// DepositCredit is the provider-signed accounting credit from a confirmed deposit described in "Funding the provider-local balance" in the protocol specification.
type DepositCredit struct {
	CreditID              string          `json:"credit_id"`
	ProviderID            string          `json:"provider_id"`
	AuthorID              string          `json:"author_id"`
	DepositID             string          `json:"deposit_id"`
	Network               string          `json:"network"`
	Transfer              DepositTransfer `json:"transfer"`
	AmountChainUnits      string          `json:"amount_chain_units"`
	AmountAccountingUnits string          `json:"amount_accounting_units"`
	CreditSequence        int64           `json:"credit_sequence"`
	CreditedAt            string          `json:"credited_at"`
	ProviderSignature     string          `json:"provider_signature"`
}

// BalanceStatus is a provider assertion about private available, frozen, and reserved funds described in "Funding the provider-local balance" in the protocol specification.
type BalanceStatus struct {
	ProviderID        string `json:"provider_id"`
	AuthorID          string `json:"author_id"`
	Network           string `json:"network"`
	Denomination      string `json:"denomination"`
	Available         string `json:"available"`
	Frozen            string `json:"frozen"`
	Reserved          string `json:"reserved"`
	Revision          int64  `json:"revision"`
	UpdatedAt         string `json:"updated_at"`
	ProviderSignature string `json:"provider_signature"`
}

// WithdrawalRequest requests transfer of funds to a network recipient as described in "Funding a provider-local balance" in the economics specification.
type WithdrawalRequest struct {
	ProviderID      string `json:"provider_id"`
	AuthorID        string `json:"author_id"`
	Network         string `json:"network"`
	AmountBaseUnits string `json:"amount_base_units"`
	Recipient       string `json:"recipient"`
	RequestID       string `json:"request_id"`
}

// WithdrawalStatus reports a provider's withdrawal state and optional transfer as described in "Funding a provider-local balance" in the economics specification.
type WithdrawalStatus struct {
	ProviderID        string           `json:"provider_id"`
	AuthorID          string           `json:"author_id"`
	Network           string           `json:"network"`
	AmountBaseUnits   string           `json:"amount_base_units"`
	Recipient         string           `json:"recipient"`
	RequestID         string           `json:"request_id"`
	Status            string           `json:"status"`
	Transfer          *DepositTransfer `json:"transfer"`
	UpdatedAt         string           `json:"updated_at"`
	ProviderSignature string           `json:"provider_signature"`
}

// AccountHistoryEntry is one change to a provider-local balance, an entries item of AccountHistoryPage described in "Funding the provider-local balance" in the protocol specification.
type AccountHistoryEntry struct {
	Sequence        int64  `json:"sequence"`
	At              string `json:"at"`
	Kind            string `json:"kind"`
	AmountBaseUnits string `json:"amount_base_units"`
	Reference       string `json:"reference"`
}

// AccountHistoryPage is a provider-signed page of one account's history described in "Funding the provider-local balance" in the protocol specification.
type AccountHistoryPage struct {
	ProviderID        string                `json:"provider_id"`
	AuthorID          string                `json:"author_id"`
	Network           string                `json:"network"`
	Entries           []AccountHistoryEntry `json:"entries"`
	More              bool                  `json:"more"`
	ProviderSignature string                `json:"provider_signature"`
}
