package wire

import "encoding/json"

// ExportRequest is the `ExportRequest` definition in schemas/operations.schema.json, starting an owner export job as described in Owner-export transport in docs/10-owner-export-profile.md.
type ExportRequest struct {
	AuthorID  string `json:"author_id"`
	RequestID string `json:"request_id"`
}

// ExportEntry is the `ExportEntry` definition in schemas/operations.schema.json, describing one file in an owner archive as described in Parts and entries in docs/10-owner-export-profile.md.
type ExportEntry struct {
	Path       string `json:"path"`
	MediaType  string `json:"media_type"`
	ByteLength int64  `json:"byte_length"`
	SHA256     string `json:"sha256"`
	Category   string `json:"category"`
}

// ExportPartManifest is the `ExportPartManifest` definition in schemas/operations.schema.json, describing one archive part as described in Parts and entries in docs/10-owner-export-profile.md.
type ExportPartManifest struct {
	Profile    string        `json:"profile"`
	SnapshotID string        `json:"snapshot_id"`
	AuthorID   string        `json:"author_id"`
	CreatedAt  string        `json:"created_at"`
	PartNumber int64         `json:"part_number"`
	Entries    []ExportEntry `json:"entries"`
}

// ExportPartDescriptor is the `ExportPartDescriptor` definition in schemas/operations.schema.json, identifying a part's length, digest, and entry count as described in Set manifest and index in docs/10-owner-export-profile.md.
type ExportPartDescriptor struct {
	PartNumber int64  `json:"part_number"`
	ByteLength int64  `json:"byte_length"`
	SHA256     string `json:"sha256"`
	EntryCount int64  `json:"entry_count"`
}

// ExportSetIndex is the `ExportSetIndex` definition in schemas/operations.schema.json, a provider-proposed snapshot index described in Set manifest and index in docs/10-owner-export-profile.md.
type ExportSetIndex struct {
	Profile      string                 `json:"profile"`
	SnapshotID   string                 `json:"snapshot_id"`
	AuthorID     string                 `json:"author_id"`
	CreatedAt    string                 `json:"created_at"`
	TotalEntries int64                  `json:"total_entries"`
	TotalBytes   int64                  `json:"total_bytes"`
	Coverage     string                 `json:"coverage"`
	Parts        []ExportPartDescriptor `json:"parts"`
}

// ExportSetManifest is the `ExportSetManifest` definition in schemas/operations.schema.json, the owner-signed archive manifest described in Set manifest and index in docs/10-owner-export-profile.md.
type ExportSetManifest struct {
	Profile           string                 `json:"profile"`
	SnapshotID        string                 `json:"snapshot_id"`
	AuthorID          string                 `json:"author_id"`
	CreatedAt         string                 `json:"created_at"`
	TotalEntries      int64                  `json:"total_entries"`
	TotalBytes        int64                  `json:"total_bytes"`
	Coverage          string                 `json:"coverage"`
	Parts             []ExportPartDescriptor `json:"parts"`
	SourceIndexSHA256 *string                `json:"source_index_sha256"`
	Signature         string                 `json:"signature"`
}

// ExportJobStatus is the `ExportJobStatus` definition in schemas/operations.schema.json, reporting job state as described in Owner-export transport in docs/10-owner-export-profile.md.
type ExportJobStatus struct {
	JobID       string `json:"job_id"`
	Status      string `json:"status"`
	SnapshotID  string `json:"snapshot_id,omitempty"`
	IndexSHA256 string `json:"index_sha256,omitempty"`
	ExpiresAt   string `json:"expires_at,omitempty"`
	ReadyAt     string `json:"ready_at,omitempty"`
}

// ExportJobCreated is the `ExportJobCreated` definition in schemas/operations.schema.json, acknowledging creation as described in Owner-export transport in docs/10-owner-export-profile.md.
type ExportJobCreated struct {
	JobID  string `json:"job_id"`
	Status string `json:"status"`
}

// ProviderServiceEvidence is the `ProviderServiceEvidence` definition in schemas/operations.schema.json, provider-signed evidence carried as `ServiceEvidence` kind `provider_evidence` and described in Import and verification in docs/10-owner-export-profile.md.
type ProviderServiceEvidence struct {
	ProviderID        string `json:"provider_id"`
	AuthorID          string `json:"author_id"`
	EvidenceType      string `json:"evidence_type"`
	Payload           string `json:"payload"`
	ProviderSignature string `json:"provider_signature"`
}

// ServiceEvidence is the `ServiceEvidence` definition in schemas/operations.schema.json, wrapping service evidence as described in the entry-content table of Import and verification in docs/10-owner-export-profile.md.
type ServiceEvidence struct {
	Kind   string          `json:"kind"`
	Object json.RawMessage `json:"object"`
}
