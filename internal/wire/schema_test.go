package wire

import (
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
)

func schemaFixture(t *testing.T, name string) map[string]any {
	t.Helper()
	b, err := os.ReadFile("testdata/schema/" + name)
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		t.Fatal(err)
	}
	return value
}

func schemaMessage(t *testing.T) map[string]any {
	t.Helper()
	return schemaFixture(t, "security-vectors.json")["message"].(map[string]any)
}

func schemaMediaSlot() map[string]any {
	return map[string]any{"id": "media-a", "url": "https://media.example/a", "kind": "image", "sha256": strings.Repeat("a", 64)}
}

func schemaExpect(t *testing.T, def string, doc any, wantPointer string, wantKeywords ...string) {
	t.Helper()
	err := Check(def, doc)
	if len(wantKeywords) == 0 {
		if err != nil {
			t.Fatalf("Check(%s) error = %v", def, err)
		}
		return
	}
	validation, ok := err.(*ValidationError)
	if !ok {
		t.Fatalf("Check(%s) error = %T %v; want ValidationError", def, err, err)
	}
	if validation.Pointer != wantPointer {
		t.Errorf("pointer = %q; want %q (%s)", validation.Pointer, wantPointer, validation.Keyword)
	}
	found := validation.Keyword == wantKeywords[0]
	if len(wantKeywords) > 1 {
		found = found || validation.Keyword == wantKeywords[1]
	}
	if len(wantKeywords) > 2 {
		found = found || validation.Keyword == wantKeywords[2]
	}
	if !found {
		t.Errorf("keyword = %q; want one of %v at %q", validation.Keyword, wantKeywords, wantPointer)
	}
}

func TestSchemaCheck(t *testing.T) {
	t.Run("unknown definition fails", func(t *testing.T) {
		if err := Check("NoSuchDefinition", nil); err == nil {
			t.Fatal("Check accepted an unknown definition")
		}
	})
	t.Run("operation and inline definitions resolve", func(t *testing.T) {
		schemaExpect(t, "KeySuccession", map[string]any{}, "", "required")
		schemaExpect(t, "AdmissionRequest/properties/intent", map[string]any{}, "", "required")
	})
	t.Run("security vector validates as both message definitions", func(t *testing.T) {
		message := schemaMessage(t)
		schemaExpect(t, "PostMessage", message, "")
		schemaExpect(t, "TransportMessage", message, "")
	})
	t.Run("settlement examples validate", func(t *testing.T) {
		example := schemaFixture(t, "settlement.example.json")
		schemaExpect(t, "GenesisManifest", example["genesis_manifest"], "")
		schemaExpect(t, "SettlementProof", example["settlement_proof"], "")
		schemaExpect(t, "KeySuccession", example["key_succession"], "")
	})
	t.Run("fee schedule example validates", func(t *testing.T) {
		schemaExpect(t, "FeeSchedule", schemaFixture(t, "fee-schedule.example.json")["schedule"], "")
	})
	t.Run("unsupported Go values fail", func(t *testing.T) {
		if err := Check("PostMessage", struct{}{}); err == nil {
			t.Fatal("Check accepted a Go struct")
		}
	})
	t.Run("invalid JSON number fails before schema validation", func(t *testing.T) {
		err := Check("PostMessage", json.Number("abc"))
		if err == nil {
			t.Fatal("Check accepted an invalid JSON number")
		}
		if _, ok := err.(*ValidationError); ok {
			t.Fatalf("Check error = %T; want value-model error", err)
		}
	})
	t.Run("closed payload rejects unknown member", func(t *testing.T) {
		message := schemaMessage(t)
		schemaExpect(t, "PostMessage", message, "")
		message["payload"].(map[string]any)["likes"] = json.Number("1")
		schemaExpect(t, "PostMessage", message, "/payload", "additionalProperties")
	})
	t.Run("empty text requires media", func(t *testing.T) {
		message := schemaMessage(t)
		schemaExpect(t, "PostMessage", message, "")
		payload := message["payload"].(map[string]any)
		payload["text"] = ""
		delete(payload, "media_slots")
		schemaExpect(t, "PostMessage", message, "/payload", "required")
		payload["media_slots"] = []any{}
		schemaExpect(t, "PostMessage", message, "/payload/media_slots", "minItems")
	})
	t.Run("media slot count is bounded", func(t *testing.T) {
		message := schemaMessage(t)
		message["payload"].(map[string]any)["media_slots"] = []any{schemaMediaSlot()}
		schemaExpect(t, "PostMessage", message, "")
		payload := message["payload"].(map[string]any)
		slots := payload["media_slots"].([]any)
		payload["media_slots"] = slices.Repeat(slots[:1], 17)
		schemaExpect(t, "PostMessage", message, "/payload/media_slots", "maxItems")
	})
	t.Run("reply identifiers use message id format", func(t *testing.T) {
		message := schemaMessage(t)
		message["payload"].(map[string]any)["reply_to"] = "https://attacker.example/parent"
		schemaExpect(t, "PostMessage", message, "/payload/reply_to", "pattern", "minLength")
	})
	t.Run("post requires bound receipt", func(t *testing.T) {
		message := schemaMessage(t)
		delete(message, "submission_receipt")
		schemaExpect(t, "PostMessage", message, "", "required")
	})
	t.Run("message type is constant", func(t *testing.T) {
		message := schemaMessage(t)
		message["message_type"] = "archive_record"
		schemaExpect(t, "PostMessage", message, "/message_type", "const")
	})
	t.Run("admission intent is closed", func(t *testing.T) {
		vector := schemaMessage(t)
		intent := map[string]any{"author_id": vector["author_id"], "message_type": "post", "payload": vector["payload"], "timestamp": vector["timestamp"], "version": vector["version"]}
		schemaExpect(t, "AdmissionRequest/properties/intent", intent, "")
		intent["message_id"] = "fy1:" + strings.Repeat("a", 64)
		schemaExpect(t, "AdmissionRequest/properties/intent", intent, "", "additionalProperties", "not")
	})
	t.Run("message signer requires delegation", func(t *testing.T) {
		message := schemaMessage(t)
		message["signer_id"] = message["author_id"]
		schemaExpect(t, "PostMessage", message, "", "required", "dependentRequired")
	})
	t.Run("media URL rejects Unicode whitespace", func(t *testing.T) {
		message := schemaMessage(t)
		message["payload"].(map[string]any)["media_slots"] = []any{schemaMediaSlot()}
		schemaExpect(t, "PostMessage", message, "")
		media := message["payload"].(map[string]any)["media_slots"].([]any)[0].(map[string]any)
		media["url"] = "https://media.example/a\u00a0b"
		schemaExpect(t, "PostMessage", message, "/payload/media_slots/0/url", "pattern")
	})
	t.Run("recipient rejects non-ASCII", func(t *testing.T) {
		did := schemaMessage(t)["author_id"]
		invoice := map[string]any{"deposit_id": "fydi2:" + strings.Repeat("a", 64), "provider_id": did, "author_id": did, "network": BitcoinMainnet, "amount_chain_units": "1", "recipient": "bc1qexample", "issued_at": "2026-11-11T00:00:00Z", "expires_at": "2026-11-11T00:00:00Z", "provider_signature": strings.Repeat("a", 86)}
		schemaExpect(t, "DepositInvoice", invoice, "")
		invoice["recipient"] = "bc1é"
		schemaExpect(t, "DepositInvoice", invoice, "/recipient", "pattern")
	})
	t.Run("listing page item count is bounded", func(t *testing.T) {
		messages := slices.Repeat([]any{schemaMessage(t)}, 100)
		page := map[string]any{"generated_at": "2026-11-11T00:00:00Z", "expires_at": "2026-11-11T00:00:00Z", "messages": messages, "next_cursor": nil}
		schemaExpect(t, "DiscoveryPage", page, "")
		page["messages"] = slices.Repeat(messages[:1], 101)
		schemaExpect(t, "DiscoveryPage", page, "/messages", "maxItems")
	})
	t.Run("deposit transfer variants are exclusive", func(t *testing.T) {
		transfer := map[string]any{"network": BitcoinMainnet, "txid": strings.Repeat("a", 64), "output_index": json.Number("0")}
		schemaExpect(t, "DepositTransfer", transfer, "")
		transfer["tx_hash"] = strings.Repeat("a", 64)
		schemaExpect(t, "DepositTransfer", transfer, "", "oneOf")
	})
	t.Run("fee schedule requires at least one fee", func(t *testing.T) {
		schedule := schemaFixture(t, "fee-schedule.example.json")["schedule"].(map[string]any)
		schemaExpect(t, "FeeSchedule", schedule, "")
		schedule["base_fee_base_units"] = "0"
		schedule["per_kib_fee_base_units"] = "0"
		schemaExpect(t, "FeeSchedule", schedule, "", "not")
	})
	t.Run("fee denomination follows network", func(t *testing.T) {
		schedule := schemaFixture(t, "fee-schedule.example.json")["schedule"].(map[string]any)
		schemaExpect(t, "FeeSchedule", schedule, "")
		schedule["denomination"] = "wei"
		schemaExpect(t, "FeeSchedule", schedule, "/denomination", "const")
	})
	t.Run("fee schedule rejects protocol-defined split", func(t *testing.T) {
		schedule := schemaFixture(t, "fee-schedule.example.json")["schedule"].(map[string]any)
		schemaExpect(t, "FeeSchedule", schedule, "")
		schedule["split"] = []any{map[string]any{"role": "replica", "basis_points": json.Number("2000")}}
		schemaExpect(t, "FeeSchedule", schedule, "", "additionalProperties")
	})
	t.Run("unregistered authorization state has no epoch", func(t *testing.T) {
		did := schemaMessage(t)["author_id"]
		epoch := map[string]any{"epoch_id": "fye1:" + strings.Repeat("a", 64), "provider_id": did, "author_id": did, "epoch_sequence": json.Number("1"), "activated_at": "2026-11-11T00:00:00Z", "nonce": strings.Repeat("a", 43), "provider_signature": strings.Repeat("a", 86)}
		state := map[string]any{"provider_id": did, "author_id": did, "state_revision": json.Number("0"), "status": "active", "epoch": epoch, "provider_signature": strings.Repeat("a", 86)}
		schemaExpect(t, "AuthorizationEpochState", state, "")
		state["status"] = "unregistered"
		schemaExpect(t, "AuthorizationEpochState", state, "/epoch", "type")
	})
	t.Run("authorization epoch integer bounds", func(t *testing.T) {
		did := schemaMessage(t)["author_id"]
		epoch := map[string]any{"epoch_id": "fye1:" + strings.Repeat("a", 64), "provider_id": did, "author_id": did, "epoch_sequence": json.Number("1"), "activated_at": "2026-11-11T00:00:00Z", "nonce": strings.Repeat("a", 43), "provider_signature": strings.Repeat("a", 86)}
		schemaExpect(t, "AuthorizationEpoch", epoch, "")
		epoch["epoch_sequence"] = json.Number("0")
		schemaExpect(t, "AuthorizationEpoch", epoch, "/epoch_sequence", "minimum")
		epoch["epoch_sequence"] = json.Number("9007199254740992")
		schemaExpect(t, "AuthorizationEpoch", epoch, "/epoch_sequence", "maximum")
	})
	t.Run("Unicode string length uses code points", func(t *testing.T) {
		message := schemaMessage(t)
		message["payload"].(map[string]any)["media_slots"] = []any{schemaMediaSlot()}
		schemaExpect(t, "PostMessage", message, "")
		media := message["payload"].(map[string]any)["media_slots"].([]any)[0].(map[string]any)
		media["alt_text"] = strings.Repeat("é", 2000)
		schemaExpect(t, "PostMessage", message, "")
		media["alt_text"] = strings.Repeat("é", 2001)
		schemaExpect(t, "PostMessage", message, "/payload/media_slots/0/alt_text", "maxLength")
	})
	t.Run("display name pattern handles lookahead", func(t *testing.T) {
		schemaExpect(t, "NameRecord/properties/display_name", "Ann Lee", "")
		schemaExpect(t, "NameRecord/properties/display_name", "Ann  Lee", "", "pattern")
		schemaExpect(t, "NameRecord/properties/display_name", "Ann　", "", "pattern")
		schemaExpect(t, "NameRecord/properties/display_name", " Ann", "", "pattern")
		schemaExpect(t, "NameRecord/properties/display_name", nil, "")
	})
}
