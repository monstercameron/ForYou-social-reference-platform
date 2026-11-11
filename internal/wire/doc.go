// Package wire defines the ForYou JSON schema validator and shared wire values.
//
// Every object in a schema $defs entry has one data-only Go struct named exactly
// after that entry. A $defs entry that only references another definition is a type
// alias. An allOf composition is one flattened struct. Inline object names come
// from the owning issue. The string-only definitions DidKey, MessageId, Signature,
// FinancialAmount, FinancialPositiveAmount, FinancialTime, FinancialDid,
// FinancialSignature, FinancialNonce, and FinancialHash have no Go type; their
// fields are strings checked by Check. PublicReadError belongs to internal/apierr,
// which wire does not duplicate. KeySuccession occurs in both schemas and has one
// Go type.
//
// Field names use Go CamelCase and common initialisms (MessageID, SHA256, URL,
// TxID, TxHash, KID, EPK, HTTPStatus, BitcoinMainnet, LatencyP50Ms, IndexSHA256,
// ReportIDs, PerKiBFeeBaseUnits, and VaultShareBP); JSON tags preserve the exact
// schema property names. Integers use int64, booleans use bool, and enums and
// constants use string. Decimal amount strings are read with Amount or
// PositiveAmount, and time strings are read with ParseTime.
//
// Required non-null fields use value types without omitempty. Required nullable
// fields use pointers without omitempty. Optional fields may be absent but are
// never null; they use omitempty and also use pointers when the Go zero value is
// schema-valid, keeping absence distinct from zero. Examples include optional
// objects, alt_text (*string), output_index (*int64), media_slots (*[]MediaReference),
// and refs (*[]PostReference). Required arrays must be non-nil when marshalled.
// MessageBase.payload, FeePreflightRequest.intent, ServiceEvidence.object, and
// VaultDelivery.message use json.RawMessage; IntakeRequest.regional_fields uses
// map[string]string.
//
// Decoding a schema-valid document and re-encoding it must preserve the same JSON
// value, including member presence and equal values, for canonical hashing and
// signing. Wire types have no methods or validation code; structural validation
// uses Check and semantic rules belong to their owning packages. Only this file
// carries the package comment. Every wire issue prefixes test helpers and test-only
// identifiers with its issue prefix and keeps copied test data under
// testdata/<prefix>/.
package wire
