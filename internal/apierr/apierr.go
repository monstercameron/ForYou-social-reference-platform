// Package apierr defines the public error body and HTTP status catalog.
// Route-specific precedence, including restriction precedence over retention expiry, belongs to each route.
package apierr

import (
	"errors"
	"fmt"
)

// Status is a public error name from the operations schema.
type Status string

const (
	PolicyRestricted    Status = "policy_restricted"     // PolicyRestricted names a policy restriction.
	LegalRestricted     Status = "legal_restricted"      // LegalRestricted names a legal restriction.
	Restricted          Status = "restricted"            // Restricted names a disclosable restriction.
	NotFound            Status = "not_found"             // NotFound names an unknown or nondisclosable object.
	RetentionExpired    Status = "retention_expired"     // RetentionExpired names ended provider retention.
	ExportExpired       Status = "export_expired"        // ExportExpired names an expired export job.
	InvalidQuery        Status = "invalid_query"         // InvalidQuery names malformed query input.
	InvalidCursor       Status = "invalid_cursor"        // InvalidCursor names an invalid listing cursor.
	Unauthenticated     Status = "unauthenticated"       // Unauthenticated names missing or invalid authentication.
	IDCollision         Status = "id_collision"          // IDCollision names distinct intents sharing an ID.
	IDMismatch          Status = "id_mismatch"           // IDMismatch names a recomputed message ID mismatch.
	NotReady            Status = "not_ready"             // NotReady names state that is not ready.
	ExportInvalidated   Status = "export_invalidated"    // ExportInvalidated names an invalidated export.
	QuoteConflict       Status = "quote_conflict"        // QuoteConflict names a quote state conflict.
	StaleState          Status = "stale_state"           // StaleState names a stale state conflict.
	MessageTooLarge     Status = "message_too_large"     // MessageTooLarge names a message over its budget.
	InvalidMessage      Status = "invalid_message"       // InvalidMessage names semantically invalid message data.
	RateLimited         Status = "rate_limited"          // RateLimited names a disclosed rate or work limit.
	Unavailable         Status = "unavailable"           // Unavailable names unavailable service, state, or capacity.
	InsufficientBalance Status = "insufficient_balance"  // InsufficientBalance names an insufficient balance.
	DelegationRevoked   Status = "delegation_revoked"    // DelegationRevoked names a revoked delegation.
	QuoteExpired        Status = "quote_expired"         // QuoteExpired names an expired quote.
	BadAuthorization    Status = "bad_authorization"     // BadAuthorization names invalid authorization.
	RequestConflict     Status = "request_conflict"      // RequestConflict names a request conflict.
	RangeNotSatisfiable Status = "range_not_satisfiable" // RangeNotSatisfiable names an invalid range request.
	TooLarge            Status = "too_large"             // TooLarge is schema-defined without a catalog HTTP status.
)

// DefaultRetryAfterSeconds is the platform's fallback retry delay.
const DefaultRetryAfterSeconds = 30

var statusCodes = map[Status]int{
	InvalidQuery:        400,
	InvalidCursor:       400,
	Unauthenticated:     401,
	InsufficientBalance: 402,
	PolicyRestricted:    403,
	LegalRestricted:     403,
	Restricted:          403,
	DelegationRevoked:   403,
	NotFound:            404,
	IDCollision:         409,
	NotReady:            409,
	ExportInvalidated:   409,
	QuoteConflict:       409,
	StaleState:          409,
	RequestConflict:     409,
	RetentionExpired:    410,
	ExportExpired:       410,
	RangeNotSatisfiable: 416,
	IDMismatch:          422,
	MessageTooLarge:     422,
	InvalidMessage:      422,
	QuoteExpired:        422,
	BadAuthorization:    422,
	RateLimited:         429,
	Unavailable:         503,
}

// Valid reports whether s is one of the schema's public error names.
func (s Status) Valid() bool {
	switch s {
	case PolicyRestricted, LegalRestricted, Restricted, NotFound, RetentionExpired, ExportExpired,
		InvalidQuery, InvalidCursor, Unauthenticated, IDCollision, IDMismatch, NotReady,
		ExportInvalidated, QuoteConflict, StaleState, MessageTooLarge, InvalidMessage,
		RateLimited, Unavailable, InsufficientBalance, DelegationRevoked, QuoteExpired,
		BadAuthorization, RequestConflict, RangeNotSatisfiable, TooLarge:
		return true
	default:
		return false
	}
}

// HTTPStatus returns the catalog code, if the status has an HTTP mapping.
func (s Status) HTTPStatus() (code int, ok bool) {
	code, ok = statusCodes[s]
	return code, ok
}

// PublicReadError is the closed public error response body.
type PublicReadError struct {
	// RetryAfterSeconds is the optional retry delay in seconds.
	RetryAfterSeconds int `json:"retry_after_seconds,omitempty"`
	// Status is the public error name.
	Status Status `json:"status"`
}

// Validate checks the body against the PublicReadError schema and status catalog.
func (e PublicReadError) Validate() error {
	if !e.Status.Valid() {
		return fmt.Errorf("invalid public error status %q", e.Status)
	}
	if e.RetryAfterSeconds != 0 && (e.RetryAfterSeconds < 1 || e.RetryAfterSeconds > 86400) {
		return errors.New("retry_after_seconds must be between 1 and 86400")
	}
	code, mapped := e.Status.HTTPStatus()
	if mapped && (code == 429 || code == 503) && e.RetryAfterSeconds == 0 {
		return fmt.Errorf("status %q requires retry_after_seconds", e.Status)
	}
	return nil
}

// Error carries a public status and a private internal cause.
type Error struct {
	// Status is the public outcome known to the package.
	Status Status
	// RetryAfterSeconds is the optional retry delay in seconds.
	RetryAfterSeconds int
	// Err is the internal cause and is never included in an HTTP body.
	Err error
}

// New returns an Error with status s and cause.
func New(s Status, cause error) *Error {
	return &Error{Status: s, Err: cause}
}

// Retry returns an Error with a retry delay and cause.
func Retry(s Status, seconds int, cause error) *Error {
	return &Error{Status: s, RetryAfterSeconds: seconds, Err: cause}
}

// Error returns a diagnostic string that may include the private cause.
func (e *Error) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err == nil {
		return string(e.Status)
	}
	return fmt.Sprintf("%s: %v", e.Status, e.Err)
}

// Unwrap returns the internal cause.
func (e *Error) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// StatusOf finds the first Error in err's chain.
func StatusOf(err error) (Status, bool) {
	var publicError *Error
	if !errors.As(err, &publicError) || publicError == nil {
		return "", false
	}
	return publicError.Status, true
}
