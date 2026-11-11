package wire

import "encoding/json"

// Version is the Version object in the launch schema; docs/02-protocol.md Scope and compatibility defines the message version.
type Version struct {
	Major int64 `json:"major"`
	Minor int64 `json:"minor"`
}

// SubmissionReceipt is the SubmissionReceipt object in docs/02-protocol.md Submission receipt.
type SubmissionReceipt struct {
	ProviderID        string `json:"provider_id"`
	AuthorID          string `json:"author_id"`
	MessageID         string `json:"message_id"`
	ReceiptID         string `json:"receipt_id"`
	QuoteID           string `json:"quote_id"`
	ScheduleID        string `json:"schedule_id"`
	Network           string `json:"network"`
	AmountBaseUnits   string `json:"amount_base_units"`
	AuthorizationAt   string `json:"authorization_at"`
	IssuedAt          string `json:"issued_at"`
	ProviderSignature string `json:"provider_signature"`
}

// PostPayload is the PostPayload object in docs/02-protocol.md Text payloads and external media references and Posts, comments and versions.
type PostPayload struct {
	ContentFormat string            `json:"content_format"`
	MediaSlots    *[]MediaReference `json:"media_slots,omitempty"`
	Refs          *[]PostReference  `json:"refs,omitempty"`
	Text          string            `json:"text"`
	Supersedes    string            `json:"supersedes,omitempty"`
	ReplyTo       string            `json:"reply_to,omitempty"`
}

// MediaReference is the MediaReference object in docs/02-protocol.md Text payloads and external media references.
type MediaReference struct {
	ID        string  `json:"id"`
	URL       string  `json:"url"`
	Kind      string  `json:"kind"`
	SHA256    string  `json:"sha256"`
	AltText   *string `json:"alt_text,omitempty"`
	SizeBytes int64   `json:"size_bytes,omitempty"`
}

// PostReference is the PostReference object in docs/02-protocol.md Signed structured references.
type PostReference struct {
	ID     string `json:"id"`
	Target string `json:"target"`
}

// MessageBase is the MessageBase object in docs/02-protocol.md Intent and receipts.
type MessageBase struct {
	MessageID         string             `json:"message_id"`
	AuthorID          string             `json:"author_id"`
	SignerID          string             `json:"signer_id,omitempty"`
	Delegation        *Delegation        `json:"delegation,omitempty"`
	Timestamp         string             `json:"timestamp"`
	MessageType       string             `json:"message_type"`
	Version           Version            `json:"version"`
	SubmissionReceipt *SubmissionReceipt `json:"submission_receipt,omitempty"`
	Signature         string             `json:"signature"`
	Payload           json.RawMessage    `json:"payload"`
}

// PostMessage is the PostMessage object in docs/02-protocol.md Intent and receipts.
type PostMessage struct {
	MessageID         string            `json:"message_id"`
	AuthorID          string            `json:"author_id"`
	SignerID          string            `json:"signer_id,omitempty"`
	Delegation        *Delegation       `json:"delegation,omitempty"`
	Timestamp         string            `json:"timestamp"`
	MessageType       string            `json:"message_type"`
	Version           Version           `json:"version"`
	SubmissionReceipt SubmissionReceipt `json:"submission_receipt"`
	Signature         string            `json:"signature"`
	Payload           PostPayload       `json:"payload"`
}

// TransportMessage is the schema alias of PostMessage, used for retrieval in docs/05-portability.md Snapshot pagination.
type TransportMessage = PostMessage

// PublicTransportMessage is the schema alias of PostMessage, used for retrieval in docs/05-portability.md Snapshot pagination.
type PublicTransportMessage = PostMessage

// Intent is the protocol intent in docs/02-protocol.md Intent and receipts.
type Intent struct {
	AuthorID    string      `json:"author_id"`
	SignerID    string      `json:"signer_id,omitempty"`
	Delegation  *Delegation `json:"delegation,omitempty"`
	Timestamp   string      `json:"timestamp"`
	MessageType string      `json:"message_type"`
	Version     Version     `json:"version"`
	Payload     PostPayload `json:"payload"`
}

// AdmissionAuthorization is the AdmissionAuthorization object in docs/02-protocol.md Admission under Quote and atomic admission.
type AdmissionAuthorization struct {
	ProviderID string `json:"provider_id"`
	AuthorID   string `json:"author_id"`
	MessageID  string `json:"message_id"`
	QuoteID    string `json:"quote_id"`
}

// AdmissionRequest is the AdmissionRequest object in docs/02-protocol.md Admission under Quote and atomic admission.
type AdmissionRequest struct {
	ProviderID    string `json:"provider_id"`
	Intent        Intent `json:"intent"`
	Authorization string `json:"authorization"`
	QuoteID       string `json:"quote_id"`
}

// MessageAcceptance is the MessageAcceptance object in docs/02-protocol.md Acceptance, retries and conflicts.
type MessageAcceptance struct {
	MessageID string `json:"message_id"`
	Status    string `json:"status"`
}

// PreparedAdmissionRecord is the PreparedAdmissionRecord object in docs/10-owner-export-profile.md Prepared admissions.
type PreparedAdmissionRecord struct {
	Request AdmissionRequest  `json:"request"`
	Receipt SubmissionReceipt `json:"receipt"`
}
