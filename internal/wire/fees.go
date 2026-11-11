package wire

import "encoding/json"

// FeeSchedule is a release-approved rate and validity interval described in "Fee schedule and confirmed provider balance" in the protocol specification.
type FeeSchedule struct {
	ScheduleID         string `json:"schedule_id"`
	ProfileMajor       int64  `json:"profile_major"`
	ProfileMinor       int64  `json:"profile_minor"`
	Revision           int64  `json:"revision"`
	Network            string `json:"network"`
	Denomination       string `json:"denomination"`
	BaseFeeBaseUnits   string `json:"base_fee_base_units"`
	PerKiBFeeBaseUnits string `json:"per_kib_fee_base_units"`
	EffectiveAt        string `json:"effective_at"`
	ExpiresAt          string `json:"expires_at"`
}

// FeeScheduleApproval records release-threshold approval of a fee schedule described in "Fee schedule governance" in the governance specification.
type FeeScheduleApproval struct {
	ScheduleID        string             `json:"schedule_id"`
	ApprovedAt        string             `json:"approved_at"`
	Drift             bool               `json:"drift"`
	ReleaseSignatures []ReleaseSignature `json:"release_signatures"`
}

// ReleaseSignature is one release-key signature used by fee, distribution, Foundation-key, genesis, and key-set objects described in "Release keys" in the governance specification.
type ReleaseSignature struct {
	ReleaseKeyID string `json:"release_key_id"`
	Signature    string `json:"signature"`
}

// FeeSchedulesPage lists approved schedules described in "Fee schedule governance" in the governance specification.
type FeeSchedulesPage struct {
	Schedules []ApprovedFeeSchedule `json:"schedules"`
}

// ApprovedFeeSchedule pairs a schedule with its required approval as described in "Fee schedule governance" in the governance specification.
type ApprovedFeeSchedule struct {
	Schedule FeeSchedule         `json:"schedule"`
	Approval FeeScheduleApproval `json:"approval"`
}

// FeePreflightRequest asks a provider to quote an intent under the schedule and authorization rules in "Quote and atomic admission" in the protocol specification.
type FeePreflightRequest struct {
	ProviderID    string          `json:"provider_id"`
	AuthorID      string          `json:"author_id"`
	ScheduleID    string          `json:"schedule_id"`
	Intent        json.RawMessage `json:"intent"`
	Authorization string          `json:"authorization"`
}

// FeeQuote is the provider-signed price and expiry for an intent described in "Quote and atomic admission" in the protocol specification.
type FeeQuote struct {
	ProviderID        string `json:"provider_id"`
	AuthorID          string `json:"author_id"`
	MessageID         string `json:"message_id"`
	ScheduleID        string `json:"schedule_id"`
	QuoteID           string `json:"quote_id"`
	BillableBytes     int64  `json:"billable_bytes"`
	Network           string `json:"network"`
	AmountBaseUnits   string `json:"amount_base_units"`
	PreflightAt       string `json:"preflight_at"`
	ExpiresAt         string `json:"expires_at"`
	ProviderSignature string `json:"provider_signature"`
}
