package wire

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"
)

func operatorRoundTrip(t *testing.T, raw []byte, v any) {
	t.Helper()

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		t.Fatalf("decode document: %v", err)
	}

	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal decoded value: %v", err)
	}

	decodeValue := func(document []byte) any {
		var value any
		decoder := json.NewDecoder(bytes.NewReader(document))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			t.Fatalf("decode JSON value: %v", err)
		}
		return value
	}

	if got, want := decodeValue(encoded), decodeValue(raw); !reflect.DeepEqual(got, want) {
		t.Errorf("round trip changed JSON value: got %s, want %s", encoded, raw)
	}
}

func TestOperatorWireRoundTrip(t *testing.T) {
	t.Run("operator manifest", func(t *testing.T) {
		raw := []byte(`{"operator_id":"did:key:z6Mkabc","manifest_version":1,"issued_at":"2026-01-01T00:00:00Z","expires_at":"2026-01-02T00:00:00Z","service_roles":["storage"],"service_endpoints":["https://operator.example"],"supported_profiles":["ForYouExportSet-1"],"policy_url":"https://operator.example/policy","policy_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","legal_contact":"legal@example.org","abuse_contact":"abuse@example.org","privacy_contact":"privacy@example.org","appeal_url":"https://operator.example/appeal","markets_assessed":[],"signature":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
		operatorRoundTrip(t, raw, &OperatorManifest{})
	})

	t.Run("service challenge", func(t *testing.T) {
		raw := []byte(`{"challenge":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","service_id":"did:key:z6Mkabc","author_id":"did:key:z6Mkdef","method":"POST","resource":"/v1/rights-requests","body_hash":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","expires_at":"2026-01-01T00:00:30Z"}`)
		operatorRoundTrip(t, raw, &ServiceChallenge{})
	})

	t.Run("intake form", func(t *testing.T) {
		raw := []byte(`{"operator_id":"did:key:z6Mkabc","form_id":"rights","form_version":"1","kind":"rights_request","fields":[{"name":"details","required":true,"max_utf8_bytes":4096,"description":"Describe the request."}],"instructions":"Submit an owner rights request."}`)
		operatorRoundTrip(t, raw, &IntakeForm{})
	})

	t.Run("rights request intake", func(t *testing.T) {
		raw := []byte(`{"operator_id":"did:key:z6Mkabc","request_id":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","accept_until":"2026-01-01T00:10:00Z","kind":"rights_request","form_id":"rights","form_version":"1","form_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","locators":["message:abc"],"statement":"Please provide my records.","evidence_refs":[{"locator":"message:abc","description":"The affected record."}],"regional_fields":{"jurisdiction":"example"},"contact":{"kind":"email","address":"owner@example.org"},"requested_right":"access"}`)
		operatorRoundTrip(t, raw, &IntakeRequest{})
	})

	t.Run("notice intake omits rights fields", func(t *testing.T) {
		raw := []byte(`{"operator_id":"did:key:z6Mkabc","request_id":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","accept_until":"2026-01-01T00:10:00Z","kind":"notice","form_id":"notice","form_version":"1","form_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","locators":[],"statement":"Please review this notice.","evidence_refs":[],"regional_fields":{}}`)
		operatorRoundTrip(t, raw, &IntakeRequest{})
	})

	t.Run("intake recovery", func(t *testing.T) {
		raw := []byte(`{"request":{"operator_id":"did:key:z6Mkabc","request_id":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","accept_until":"2026-01-01T00:10:00Z","kind":"notice","form_id":"notice","form_version":"1","form_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","locators":[],"statement":"Please review this notice.","evidence_refs":[],"regional_fields":{}},"receipt":{"operator_id":"did:key:z6Mkabc","request_id":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","request_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","received_at":"2026-01-01T00:00:00Z","status":"received","signature":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}`)
		operatorRoundTrip(t, raw, &IntakeRecovery{})
	})

	t.Run("export part manifest", func(t *testing.T) {
		raw := []byte(`{"profile":"ForYouExportPart-1","snapshot_id":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","author_id":"did:key:z6Mkabc","created_at":"2026-01-01T00:00:00Z","part_number":0,"entries":[{"path":"data/message.json","media_type":"application/json","byte_length":0,"sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","category":"public_message"}]}`)
		operatorRoundTrip(t, raw, &ExportPartManifest{})
	})

	t.Run("export set manifest with null source index", func(t *testing.T) {
		raw := []byte(`{"profile":"ForYouExportSet-1","snapshot_id":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","author_id":"did:key:z6Mkabc","created_at":"2026-01-01T00:00:00Z","total_entries":0,"total_bytes":0,"coverage":"complete","parts":[],"source_index_sha256":null,"signature":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
		operatorRoundTrip(t, raw, &ExportSetManifest{})
	})

	t.Run("preparing export job status", func(t *testing.T) {
		raw := []byte(`{"job_id":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","status":"preparing"}`)
		operatorRoundTrip(t, raw, &ExportJobStatus{})
	})

	t.Run("ready export job status", func(t *testing.T) {
		raw := []byte(`{"job_id":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","status":"ready","snapshot_id":"BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB","index_sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","expires_at":"2026-01-01T01:00:00Z","ready_at":"2026-01-01T00:00:00Z"}`)
		operatorRoundTrip(t, raw, &ExportJobStatus{})
	})

	t.Run("terms service evidence string", func(t *testing.T) {
		raw := []byte(`{"kind":"terms","object":"Terms version 1"}`)
		operatorRoundTrip(t, raw, &ServiceEvidence{})
	})

	t.Run("receipt service evidence object", func(t *testing.T) {
		raw := []byte(`{"kind":"receipt","object":{"status":"accepted","sequence":1}}`)
		operatorRoundTrip(t, raw, &ServiceEvidence{})
	})
}
