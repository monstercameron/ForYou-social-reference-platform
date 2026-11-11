package wire

// EncryptedStateRecipient is the EncryptedStateRecipient object in docs/10-owner-export-profile.md Owner-export encryption.
type EncryptedStateRecipient struct {
	KID             string `json:"kid"`
	RecipientPubkey string `json:"recipient_pubkey"`
	EPK             string `json:"epk"`
	WrapNonce       string `json:"wrap_nonce"`
	WrappedKey      string `json:"wrapped_key"`
}

// EncryptedStateEnvelope is the EncryptedStateEnvelope object in docs/10-owner-export-profile.md Owner-export encryption.
type EncryptedStateEnvelope struct {
	V          int64                     `json:"v"`
	Nonce      string                    `json:"nonce"`
	Ciphertext string                    `json:"ciphertext"`
	Recipients []EncryptedStateRecipient `json:"recipients"`
}

// ArchivePayload is the ArchivePayload object in docs/10-owner-export-profile.md Owner-export encryption.
type ArchivePayload struct {
	StateKind        string                 `json:"state_kind"`
	EncryptedState   EncryptedStateEnvelope `json:"encrypted_state"`
	EncryptionScheme string                 `json:"encryption_scheme"`
	EncryptionScope  string                 `json:"encryption_scope"`
	DeviceGroupID    string                 `json:"device_group_id"`
	StateSequence    int64                  `json:"state_sequence"`
	Supersedes       []string               `json:"supersedes"`
}

// ArchiveRecord is the flattened ArchiveRecord object in docs/10-owner-export-profile.md Owner-export encryption.
type ArchiveRecord struct {
	MessageID   string         `json:"message_id"`
	AuthorID    string         `json:"author_id"`
	Timestamp   string         `json:"timestamp"`
	MessageType string         `json:"message_type"`
	Version     Version        `json:"version"`
	Signature   string         `json:"signature"`
	Payload     ArchivePayload `json:"payload"`
}
