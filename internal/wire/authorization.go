package wire

// AuthorizationEpoch is the AuthorizationEpoch object in docs/02-protocol.md Authorization epoch.
type AuthorizationEpoch struct {
	EpochID           string `json:"epoch_id"`
	ProviderID        string `json:"provider_id"`
	AuthorID          string `json:"author_id"`
	EpochSequence     int64  `json:"epoch_sequence"`
	ActivatedAt       string `json:"activated_at"`
	Nonce             string `json:"nonce"`
	ProviderSignature string `json:"provider_signature"`
}

// AuthorizationEpochState is the AuthorizationEpochState object in docs/02-protocol.md Revocation and emergency controls.
type AuthorizationEpochState struct {
	ProviderID        string              `json:"provider_id"`
	AuthorID          string              `json:"author_id"`
	StateRevision     int64               `json:"state_revision"`
	Status            string              `json:"status"`
	Epoch             *AuthorizationEpoch `json:"epoch"`
	ProviderSignature string              `json:"provider_signature"`
}

// AuthorizationEpochStateResponse is the AuthorizationEpochStateResponse object in docs/02-protocol.md Revocation and emergency controls.
type AuthorizationEpochStateResponse struct {
	Challenge         string                  `json:"challenge"`
	State             AuthorizationEpochState `json:"state"`
	ProviderSignature string                  `json:"provider_signature"`
}

// AuthorizationEpochEnroll is the AuthorizationEpochEnroll object in docs/02-protocol.md Revocation and emergency controls.
type AuthorizationEpochEnroll struct {
	ProviderID      string `json:"provider_id"`
	AuthorID        string `json:"author_id"`
	RequestNonce    string `json:"request_nonce"`
	NotAfter        string `json:"not_after"`
	AuthorSignature string `json:"author_signature"`
}

// AuthorizationEpochControl is the AuthorizationEpochControl object in docs/02-protocol.md Revocation and emergency controls.
type AuthorizationEpochControl struct {
	ProviderID            string `json:"provider_id"`
	AuthorID              string `json:"author_id"`
	ExpectedEpochID       string `json:"expected_epoch_id"`
	ExpectedStateRevision int64  `json:"expected_state_revision"`
	Action                string `json:"action"`
	Nonce                 string `json:"nonce"`
	AuthorSignature       string `json:"author_signature"`
}

// Delegation is the Delegation object in docs/02-protocol.md Delegation object.
type Delegation struct {
	AuthorID            string             `json:"author_id"`
	SignerID            string             `json:"signer_id"`
	DelegationID        string             `json:"delegation_id"`
	ValidFrom           string             `json:"valid_from"`
	ValidUntil          string             `json:"valid_until"`
	DelegationSignature string             `json:"delegation_signature"`
	ProviderID          string             `json:"provider_id"`
	Scope               []string           `json:"scope"`
	Epoch               AuthorizationEpoch `json:"epoch"`
}

// RevocationRequest is the RevocationRequest object in docs/02-protocol.md Revocation and emergency controls.
type RevocationRequest struct {
	AuthorID     string     `json:"author_id"`
	ProviderID   string     `json:"provider_id"`
	DelegationID string     `json:"delegation_id"`
	Delegation   Delegation `json:"delegation"`
	Signature    string     `json:"signature"`
}
