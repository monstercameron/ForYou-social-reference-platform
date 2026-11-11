package wire

// KeySuccession represents the same object in both schemas and is described in Key succession in docs/02-protocol.md.
type KeySuccession struct {
	SuccessionID string `json:"succession_id"`
	OldID        string `json:"old_id"`
	NewID        string `json:"new_id"`
	EffectiveAt  string `json:"effective_at"`
	OldSignature string `json:"old_signature"`
	NewSignature string `json:"new_signature"`
}
