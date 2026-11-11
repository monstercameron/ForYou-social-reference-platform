package wire

// OperatorManifest is the `OperatorManifest` definition in schemas/operations.schema.json, described in the Operator manifest and privacy section of docs/04-compliance.md.
type OperatorManifest struct {
	OperatorID        string   `json:"operator_id"`
	ManifestVersion   int64    `json:"manifest_version"`
	IssuedAt          string   `json:"issued_at"`
	ExpiresAt         string   `json:"expires_at"`
	ServiceRoles      []string `json:"service_roles"`
	ServiceEndpoints  []string `json:"service_endpoints"`
	SupportedProfiles []string `json:"supported_profiles"`
	PolicyURL         string   `json:"policy_url"`
	PolicyDigest      string   `json:"policy_digest"`
	LegalContact      string   `json:"legal_contact"`
	AbuseContact      string   `json:"abuse_contact"`
	PrivacyContact    string   `json:"privacy_contact"`
	AppealURL         string   `json:"appeal_url"`
	MarketsAssessed   []string `json:"markets_assessed"`
	Signature         string   `json:"signature"`
}

// ServiceChallenge is the `ServiceChallenge` definition in schemas/operations.schema.json, binding a service-authentication challenge to an operation as described in Administrative request authentication in docs/02-protocol.md.
type ServiceChallenge struct {
	Challenge string `json:"challenge"`
	ServiceID string `json:"service_id"`
	AuthorID  string `json:"author_id"`
	Method    string `json:"method"`
	Resource  string `json:"resource"`
	BodyHash  string `json:"body_hash"`
	ExpiresAt string `json:"expires_at"`
}

// IntakeForm is the `IntakeForm` definition in schemas/operations.schema.json, an operator's versioned public notice or rights-intake form described in Bounded intake and recovery in docs/11-operator-compliance-guide.md.
type IntakeForm struct {
	OperatorID   string            `json:"operator_id"`
	FormID       string            `json:"form_id"`
	FormVersion  string            `json:"form_version"`
	Kind         string            `json:"kind"`
	Fields       []IntakeFormField `json:"fields"`
	Instructions string            `json:"instructions"`
}

// IntakeFormField is an item of `IntakeForm.fields` in schemas/operations.schema.json, described in Bounded intake and recovery in docs/11-operator-compliance-guide.md.
type IntakeFormField struct {
	Name         string `json:"name"`
	Required     bool   `json:"required"`
	MaxUTF8Bytes int64  `json:"max_utf8_bytes"`
	Description  string `json:"description"`
}

// IntakeRequest is the `IntakeRequest` definition in schemas/operations.schema.json, a bounded operator notice, rights request, or appeal described in Bounded intake and recovery in docs/11-operator-compliance-guide.md.
type IntakeRequest struct {
	OperatorID        string              `json:"operator_id"`
	RequestID         string              `json:"request_id"`
	AcceptUntil       string              `json:"accept_until"`
	Kind              string              `json:"kind"`
	FormID            string              `json:"form_id"`
	FormVersion       string              `json:"form_version"`
	FormDigest        string              `json:"form_digest"`
	Locators          []string            `json:"locators"`
	Statement         string              `json:"statement"`
	EvidenceRefs      []IntakeEvidenceRef `json:"evidence_refs"`
	RegionalFields    map[string]string   `json:"regional_fields"`
	Contact           *IntakeContact      `json:"contact,omitempty"`
	RequestedRight    string              `json:"requested_right,omitempty"`
	DecisionReference string              `json:"decision_reference,omitempty"`
}

// IntakeEvidenceRef is an item of `IntakeRequest.evidence_refs` in schemas/operations.schema.json, described in Bounded intake and recovery in docs/11-operator-compliance-guide.md.
type IntakeEvidenceRef struct {
	Locator     string `json:"locator"`
	Description string `json:"description"`
}

// IntakeContact is the `IntakeRequest.contact` value in schemas/operations.schema.json, described in Bounded intake and recovery in docs/11-operator-compliance-guide.md.
type IntakeContact struct {
	Kind    string `json:"kind"`
	Address string `json:"address"`
}

// IntakeReceipt is the `IntakeReceipt` definition in schemas/operations.schema.json, acknowledging durable receipt as described in Acceptance and receipt in docs/11-operator-compliance-guide.md.
type IntakeReceipt struct {
	OperatorID    string `json:"operator_id"`
	RequestID     string `json:"request_id"`
	RequestDigest string `json:"request_digest"`
	ReceivedAt    string `json:"received_at"`
	Status        string `json:"status"`
	Signature     string `json:"signature"`
}

// IntakeRecovery is the `IntakeRecovery` definition in schemas/operations.schema.json, returning the request and its receipt as described in Recovery route in docs/11-operator-compliance-guide.md.
type IntakeRecovery struct {
	Request IntakeRequest `json:"request"`
	Receipt IntakeReceipt `json:"receipt"`
}
