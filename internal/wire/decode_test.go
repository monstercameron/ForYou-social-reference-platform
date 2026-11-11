package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"foryou.local/foryou/internal/canon"
)

func decodeRead(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/decode/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func decodeMember(t *testing.T, data []byte, name string) json.RawMessage {
	t.Helper()
	var members map[string]json.RawMessage
	if err := json.Unmarshal(data, &members); err != nil {
		t.Fatal(err)
	}
	member, ok := members[name]
	if !ok {
		t.Fatalf("example member %q is missing", name)
	}
	return member
}

func decodeRoundTrip(t *testing.T, name, def string, input []byte, value any) {
	t.Helper()
	t.Run(name, func(t *testing.T) {
		if err := Decode(input, def, value); err != nil {
			t.Fatalf("Decode(%s) error = %v", def, err)
		}
		got, err := Encode(def, value)
		if err != nil {
			t.Fatalf("Encode(%s) error = %v", def, err)
		}
		want, err := canon.Canonicalize(input, canon.MessageLimits)
		if err != nil {
			t.Fatalf("Canonicalize example error = %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("round trip = %s; want %s", got, want)
		}
	})
}

func decodeArrayItem(t *testing.T, name, def string, input []byte, index int, value any) {
	t.Helper()
	var items []json.RawMessage
	if err := json.Unmarshal(input, &items); err != nil {
		t.Fatal(err)
	}
	if index >= len(items) {
		t.Fatalf("%s item %d missing", name, index)
	}
	decodeRoundTrip(t, name, def, items[index], value)
}

func TestDecodeStrict(t *testing.T) {
	vector := decodeRead(t, "security-vectors.json")
	message := decodeMember(t, vector, "message")
	t.Run("decodes security vector message", func(t *testing.T) {
		var got PostMessage
		if err := Decode(message, "PostMessage", &got); err != nil {
			t.Fatalf("Decode() error = %v", err)
		}
		if got.Payload.Text != "Unicode: café 🌐" {
			t.Errorf("text = %q; want vector text", got.Payload.Text)
		}
	})
	t.Run("rejects duplicate object member", func(t *testing.T) {
		duplicated := bytes.Replace(message, []byte(`"text": "Unicode:`), []byte(`"text": "first", "text": "Unicode:`), 1)
		var got PostMessage
		if err := Decode(duplicated, "PostMessage", &got); !errors.Is(err, canon.ErrDuplicateKey) {
			t.Errorf("Decode() error = %v; want ErrDuplicateKey", err)
		}
	})
	t.Run("rejects extra payload member", func(t *testing.T) {
		extra := bytes.Replace(message, []byte(`"text": "Unicode:`), []byte(`"extra": true, "text": "Unicode:`), 1)
		var got PostMessage
		var validation *ValidationError
		if err := Decode(extra, "PostMessage", &got); !errors.As(err, &validation) {
			t.Errorf("Decode() error = %v; want *ValidationError", err)
		}
	})
	t.Run("enforces default message limit", func(t *testing.T) {
		oversized := append(append([]byte(nil), message...), bytes.Repeat([]byte{' '}, (1<<20)+1-len(message))...)
		var got PostMessage
		if err := Decode(oversized, "PostMessage", &got); !errors.Is(err, canon.ErrTooLarge) {
			t.Errorf("Decode() error = %v; want ErrTooLarge", err)
		}
	})
	t.Run("listing limit admits one MiB document", func(t *testing.T) {
		oversized := append(append([]byte(nil), message...), bytes.Repeat([]byte{' '}, (1<<20)+1-len(message))...)
		var got PostMessage
		if err := DecodeLimits(oversized, canon.ListingLimits, "PostMessage", &got); err != nil {
			t.Errorf("DecodeLimits() error = %v; want size step to pass", err)
		}
	})
	t.Run("caller target remains unchanged on failure", func(t *testing.T) {
		got := PostMessage{Payload: PostPayload{Text: "trusted"}}
		if err := Decode([]byte(`{"unknown":true}`), "PostMessage", &got); err == nil {
			t.Fatal("Decode() succeeded for invalid input")
		}
		if got.Payload.Text != "trusted" {
			t.Errorf("target text = %q; want unchanged", got.Payload.Text)
		}
	})
}

func TestEncodeStrict(t *testing.T) {
	vectorMessage := decodeMember(t, decodeRead(t, "security-vectors.json"), "message")
	var decoded PostMessage
	if err := Decode(vectorMessage, "PostMessage", &decoded); err != nil {
		t.Fatal(err)
	}
	t.Run("emits canonical vector bytes", func(t *testing.T) {
		got, err := Encode("PostMessage", decoded)
		if err != nil {
			t.Fatal(err)
		}
		want, err := canon.Canonicalize(vectorMessage, canon.MessageLimits)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("Encode() = %s; want %s", got, want)
		}
	})
	t.Run("rejects invalid empty payload text", func(t *testing.T) {
		bad := decoded
		bad.Payload.Text = ""
		bad.Payload.MediaSlots = nil
		var validation *ValidationError
		if _, err := Encode("PostMessage", bad); !errors.As(err, &validation) {
			t.Errorf("Encode() error = %v; want *ValidationError", err)
		}
	})
}

func TestDecodeExamples(t *testing.T) {
	comment := decodeRead(t, "post-comment-thread.json")
	commentMessages := decodeMember(t, comment, "messages")
	decodeArrayItem(t, "comment thread post 1", "PostMessage", commentMessages, 0, new(PostMessage))
	decodeArrayItem(t, "comment thread post 2", "PostMessage", commentMessages, 1, new(PostMessage))
	decodeArrayItem(t, "comment thread post 3", "PostMessage", commentMessages, 2, new(PostMessage))
	commentQuotes := decodeMember(t, comment, "quotes")
	decodeArrayItem(t, "comment thread quote 1", "FeeQuote", commentQuotes, 0, new(FeeQuote))
	decodeArrayItem(t, "comment thread quote 2", "FeeQuote", commentQuotes, 1, new(FeeQuote))
	decodeArrayItem(t, "comment thread quote 3", "FeeQuote", commentQuotes, 2, new(FeeQuote))
	decodeRoundTrip(t, "comment thread fee schedule", "FeeSchedule", decodeMember(t, comment, "schedule"), new(FeeSchedule))

	version := decodeRead(t, "post-version-chain.json")
	versionMessages := decodeMember(t, version, "messages")
	decodeArrayItem(t, "version chain post 1", "PostMessage", versionMessages, 0, new(PostMessage))
	decodeArrayItem(t, "version chain post 2", "PostMessage", versionMessages, 1, new(PostMessage))
	decodeArrayItem(t, "version chain post 3", "PostMessage", versionMessages, 2, new(PostMessage))
	decodeArrayItem(t, "version chain post 4", "PostMessage", versionMessages, 3, new(PostMessage))
	decodeArrayItem(t, "version chain post 5", "PostMessage", versionMessages, 4, new(PostMessage))
	versionQuotes := decodeMember(t, version, "quotes")
	decodeArrayItem(t, "version chain quote 1", "FeeQuote", versionQuotes, 0, new(FeeQuote))
	decodeArrayItem(t, "version chain quote 2", "FeeQuote", versionQuotes, 1, new(FeeQuote))
	decodeArrayItem(t, "version chain quote 3", "FeeQuote", versionQuotes, 2, new(FeeQuote))
	decodeArrayItem(t, "version chain quote 4", "FeeQuote", versionQuotes, 3, new(FeeQuote))
	decodeArrayItem(t, "version chain quote 5", "FeeQuote", versionQuotes, 4, new(FeeQuote))
	decodeRoundTrip(t, "version chain fee schedule", "FeeSchedule", decodeMember(t, version, "schedule"), new(FeeSchedule))

	media := decodeRead(t, "post-with-media-slots.json")
	decodeRoundTrip(t, "media post", "PostMessage", decodeMember(t, media, "message"), new(PostMessage))
	decodeArrayItem(t, "media quote", "FeeQuote", decodeMember(t, media, "quotes"), 0, new(FeeQuote))
	decodeRoundTrip(t, "media fee schedule", "FeeSchedule", decodeMember(t, media, "schedule"), new(FeeSchedule))

	refs := decodeRead(t, "post-with-refs.json")
	decodeRoundTrip(t, "reference post", "PostMessage", decodeMember(t, refs, "message"), new(PostMessage))
	decodeRoundTrip(t, "reference quote", "FeeQuote", decodeMember(t, refs, "quote"), new(FeeQuote))
	decodeRoundTrip(t, "reference fee schedule", "FeeSchedule", decodeMember(t, refs, "schedule"), new(FeeSchedule))

	fee := decodeRead(t, "fee-schedule.example.json")
	decodeRoundTrip(t, "fee schedule example", "FeeSchedule", decodeMember(t, fee, "schedule"), new(FeeSchedule))

	settlement := decodeRead(t, "settlement.example.json")
	decodeRoundTrip(t, "genesis manifest", "GenesisManifest", decodeMember(t, settlement, "genesis_manifest"), new(GenesisManifest))
	retentions := decodeMember(t, settlement, "retention_declarations")
	decodeArrayItem(t, "retention declaration 1", "RetentionDeclaration", retentions, 0, new(RetentionDeclaration))
	decodeArrayItem(t, "retention declaration 2", "RetentionDeclaration", retentions, 1, new(RetentionDeclaration))
	decodeRoundTrip(t, "submission receipt", "SubmissionReceipt", decodeMember(t, settlement, "receipt"), new(SubmissionReceipt))
	decodeRoundTrip(t, "settlement claim", "SettlementClaim", decodeMember(t, settlement, "settlement_claim"), new(SettlementClaim))
	leaves := decodeMember(t, settlement, "settlement_leaves")
	decodeArrayItem(t, "settlement leaf 1", "SettlementLeaf", leaves, 0, new(SettlementLeaf))
	decodeArrayItem(t, "settlement leaf 2", "SettlementLeaf", leaves, 1, new(SettlementLeaf))
	decodeArrayItem(t, "settlement leaf 3", "SettlementLeaf", leaves, 2, new(SettlementLeaf))
	decodeArrayItem(t, "settlement leaf 4", "SettlementLeaf", leaves, 3, new(SettlementLeaf))
	decodeArrayItem(t, "settlement leaf 5", "SettlementLeaf", leaves, 4, new(SettlementLeaf))
	decodeRoundTrip(t, "settlement proof", "SettlementProof", decodeMember(t, settlement, "settlement_proof"), new(SettlementProof))
	decodeRoundTrip(t, "second settlement claim", "SettlementClaim", decodeMember(t, settlement, "second_claim"), new(SettlementClaim))
	secondWitnesses := decodeMember(t, settlement, "second_claim_witnesses")
	decodeArrayItem(t, "second claim witness 1", "SettlementWitness", secondWitnesses, 0, new(SettlementWitness))
	decodeArrayItem(t, "second claim witness 2", "SettlementWitness", secondWitnesses, 1, new(SettlementWitness))
	decodeArrayItem(t, "second claim witness 3", "SettlementWitness", secondWitnesses, 2, new(SettlementWitness))
	decodeArrayItem(t, "second claim witness 4", "SettlementWitness", secondWitnesses, 3, new(SettlementWitness))
	decodeArrayItem(t, "second claim witness 5", "SettlementWitness", secondWitnesses, 4, new(SettlementWitness))
	decodeRoundTrip(t, "settlement refusal", "SettlementRefusal", decodeMember(t, settlement, "settlement_refusal_example"), new(SettlementRefusal))
	decodeRoundTrip(t, "foundation log head", "FoundationLogHead", decodeMember(t, settlement, "foundation_log_head"), new(FoundationLogHead))
	decodeRoundTrip(t, "vault receipt", "VaultReceipt", decodeMember(t, settlement, "vault_receipt"), new(VaultReceipt))
	decodeRoundTrip(t, "distribution statement", "DistributionStatement", decodeMember(t, settlement, "distribution_statement"), new(DistributionStatement))
	decodeRoundTrip(t, "vault status", "VaultStatus", decodeMember(t, settlement, "vault_status"), new(VaultStatus))
	decodeRoundTrip(t, "key succession", "KeySuccession", decodeMember(t, settlement, "key_succession"), new(KeySuccession))
}
