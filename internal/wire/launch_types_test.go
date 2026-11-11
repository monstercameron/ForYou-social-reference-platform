package wire

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func launchRoundTrip(t *testing.T, raw []byte, v any) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	encoded, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var original, roundTripped any
	if err := launchDecodeJSONValue(raw, &original); err != nil {
		t.Fatalf("decode original JSON value: %v", err)
	}
	if err := launchDecodeJSONValue(encoded, &roundTripped); err != nil {
		t.Fatalf("decode marshalled JSON value: %v", err)
	}
	if !reflect.DeepEqual(roundTripped, original) {
		t.Errorf("round-trip JSON value differs\n got: %#v\nwant: %#v", roundTripped, original)
	}
}

func launchDecodeJSONValue(raw []byte, value *any) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	return decoder.Decode(value)
}

func launchFixture(t *testing.T, name string) []byte {
	t.Helper()
	// security-vectors.json is copied from tests/; post-*.json files are copied from examples/ in the specification repository.
	data, err := os.ReadFile("testdata/launch/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

func TestLaunchSecurityVectorRoundTrips(t *testing.T) {
	var vector struct {
		Message json.RawMessage `json:"message"`
	}
	if err := json.Unmarshal(launchFixture(t, "security-vectors.json"), &vector); err != nil {
		t.Fatalf("decode security vector: %v", err)
	}
	t.Run("message", func(t *testing.T) {
		var message PostMessage
		launchRoundTrip(t, vector.Message, &message)
	})
	t.Run("intent", func(t *testing.T) {
		var message map[string]json.RawMessage
		if err := json.Unmarshal(vector.Message, &message); err != nil {
			t.Fatalf("decode message object: %v", err)
		}
		delete(message, "message_id")
		delete(message, "signature")
		delete(message, "submission_receipt")
		intentRaw, err := json.Marshal(message)
		if err != nil {
			t.Fatalf("marshal intent object: %v", err)
		}
		var intent Intent
		launchRoundTrip(t, intentRaw, &intent)
	})
}

func TestLaunchExampleRoundTrips(t *testing.T) {
	t.Run("comment thread", func(t *testing.T) {
		var example struct {
			Messages json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal(launchFixture(t, "post-comment-thread.json"), &example); err != nil {
			t.Fatalf("decode example: %v", err)
		}
		var messages []PostMessage
		launchRoundTrip(t, example.Messages, &messages)
	})
	t.Run("version chain", func(t *testing.T) {
		var example struct {
			Messages json.RawMessage `json:"messages"`
		}
		if err := json.Unmarshal(launchFixture(t, "post-version-chain.json"), &example); err != nil {
			t.Fatalf("decode example: %v", err)
		}
		var messages []PostMessage
		launchRoundTrip(t, example.Messages, &messages)
	})
	t.Run("media slots", func(t *testing.T) {
		var example struct {
			Message json.RawMessage `json:"message"`
		}
		if err := json.Unmarshal(launchFixture(t, "post-with-media-slots.json"), &example); err != nil {
			t.Fatalf("decode example: %v", err)
		}
		var message PostMessage
		launchRoundTrip(t, example.Message, &message)
	})
	t.Run("references", func(t *testing.T) {
		var example struct {
			Message json.RawMessage `json:"message"`
		}
		if err := json.Unmarshal(launchFixture(t, "post-with-refs.json"), &example); err != nil {
			t.Fatalf("decode example: %v", err)
		}
		var message PostMessage
		launchRoundTrip(t, example.Message, &message)
	})
}

func TestLaunchOptionalAndNullableRoundTrips(t *testing.T) {
	t.Run("present empty media slots", func(t *testing.T) {
		raw := []byte(`{"content_format":"text/plain","text":"hello","media_slots":[]}`)
		var payload PostPayload
		launchRoundTrip(t, raw, &payload)
	})
	t.Run("absent media slots", func(t *testing.T) {
		raw := []byte(`{"content_format":"text/plain","text":"hello"}`)
		var payload PostPayload
		launchRoundTrip(t, raw, &payload)
	})
	t.Run("empty alt text", func(t *testing.T) {
		raw := []byte(`{"id":"image","url":"https://example.com/image.png","kind":"image","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","alt_text":""}`)
		var media MediaReference
		launchRoundTrip(t, raw, &media)
	})
	t.Run("null next cursor", func(t *testing.T) {
		raw := []byte(`{"generated_at":"2026-11-11T00:00:00Z","expires_at":"2026-11-11T00:01:00Z","messages":[],"next_cursor":null}`)
		var page DiscoveryPage
		launchRoundTrip(t, raw, &page)
	})
	t.Run("unregistered authorization epoch", func(t *testing.T) {
		raw := []byte(`{"provider_id":"did:key:z6MkhFwXNFWosLeugvSf4wcL9t3uuRXueGSFTRgSvHhWj5G2","author_id":"did:key:z6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd","state_revision":0,"status":"unregistered","epoch":null,"provider_signature":"qr_G40_hkvbLiRzNWteB0ystu4mYOZ9V8JPiT_0yqK3M7NBFkWoDdJpyqf_Txp76W6QB79uUUlX2RKD4lo-gAA"}`)
		var state AuthorizationEpochState
		launchRoundTrip(t, raw, &state)
	})
	t.Run("archive with empty supersedes", func(t *testing.T) {
		raw := []byte(`{"message_id":"fy1:27e02e49a7cebf57976046e00d00ddd5e502cf700394f80c514adb5551bcab37","author_id":"did:key:z6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd","timestamp":"2026-11-11T00:00:00Z","message_type":"archive_record","version":{"major":2,"minor":0},"signature":"qr_G40_hkvbLiRzNWteB0ystu4mYOZ9V8JPiT_0yqK3M7NBFkWoDdJpyqf_Txp76W6QB79uUUlX2RKD4lo-gAA","payload":{"state_kind":"export.prepared-admission.v1","encrypted_state":{"v":1,"nonce":"AAECAwQFBgcICQoLDA0ODxAREhMUFRYX","ciphertext":"4t6kdTyoP3xWxeaopUleA6l-T4_qobZrtGkRsSjY315ZZWsC","recipients":[{"kid":"j0DFrbaPJWJK5bIU6nZ6bslNgp09e14a0bpvPiE4KF8","recipient_pubkey":"j0DFrbaPJWJK5bIU6nZ6bslNgp09e14a0bpvPiE4KF8","epk":"NYBy1jZYgNGu6jKa35EhODhR7SGijjt16WXQ0s0WYlQ","wrap_nonce":"GBkaGxwdHh8gISIjJCUmJygpKissLS4v","wrapped_key":"gz6PSRxbVYLlyahSKGbyypD5Xwz6UNGtEhojTIE7oC6EYQ4Co29b1W6SHxo9b6OK"}]},"encryption_scheme":"x25519-xchacha20poly1305","encryption_scope":"device_group","device_group_id":"group","state_sequence":0,"supersedes":[]}}`)
		var record ArchiveRecord
		launchRoundTrip(t, raw, &record)
	})
	t.Run("delegated intent", func(t *testing.T) {
		raw := []byte(`{"author_id":"did:key:z6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd","signer_id":"did:key:z6Mkf5rQX9F4PzzMNHgEaD9jK1e7P4eFVEf9vB8T2Dh8Xk9","delegation":{"author_id":"did:key:z6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd","signer_id":"did:key:z6Mkf5rQX9F4PzzMNHgEaD9jK1e7P4eFVEf9vB8T2Dh8Xk9","delegation_id":"fyd1:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","valid_from":"2026-11-11T00:00:00Z","valid_until":"2026-11-11T01:00:00Z","delegation_signature":"qr_G40_hkvbLiRzNWteB0ystu4mYOZ9V8JPiT_0yqK3M7NBFkWoDdJpyqf_Txp76W6QB79uUUlX2RKD4lo-gAA","provider_id":"did:key:z6MkhFwXNFWosLeugvSf4wcL9t3uuRXueGSFTRgSvHhWj5G2","scope":["post"],"epoch":{"epoch_id":"fye1:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","provider_id":"did:key:z6MkhFwXNFWosLeugvSf4wcL9t3uuRXueGSFTRgSvHhWj5G2","author_id":"did:key:z6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd","epoch_sequence":1,"activated_at":"2026-11-11T00:00:00Z","nonce":"j0DFrbaPJWJK5bIU6nZ6bslNgp09e14a0bpvPiE4KF8","provider_signature":"qr_G40_hkvbLiRzNWteB0ystu4mYOZ9V8JPiT_0yqK3M7NBFkWoDdJpyqf_Txp76W6QB79uUUlX2RKD4lo-gAA"}},"timestamp":"2026-11-11T00:00:00Z","message_type":"post","version":{"major":2,"minor":0},"payload":{"content_format":"text/plain","text":"hello"}}`)
		var intent Intent
		launchRoundTrip(t, raw, &intent)
	})
	t.Run("changes page item", func(t *testing.T) {
		raw := []byte(`{"provider_id":"did:key:z6MkhFwXNFWosLeugvSf4wcL9t3uuRXueGSFTRgSvHhWj5G2","items":[{"sequence":1,"message":{"message_id":"fy1:27e02e49a7cebf57976046e00d00ddd5e502cf700394f80c514adb5551bcab37","author_id":"did:key:z6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd","timestamp":"2026-11-11T00:00:00Z","message_type":"post","version":{"major":2,"minor":0},"submission_receipt":{"provider_id":"did:key:z6MkhFwXNFWosLeugvSf4wcL9t3uuRXueGSFTRgSvHhWj5G2","author_id":"did:key:z6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd","message_id":"fy1:27e02e49a7cebf57976046e00d00ddd5e502cf700394f80c514adb5551bcab37","receipt_id":"AZ3V8WktCua9iRO1BKMMRwbvjIxTz3VGIVbitQKZQ94","quote_id":"fyfq2:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","schedule_id":"fyfs2:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","network":"bitcoin-mainnet","amount_base_units":"1","authorization_at":"2026-11-11T00:00:00Z","issued_at":"2026-11-11T00:00:00Z","provider_signature":"qr_G40_hkvbLiRzNWteB0ystu4mYOZ9V8JPiT_0yqK3M7NBFkWoDdJpyqf_Txp76W6QB79uUUlX2RKD4lo-gAA"},"signature":"qr_G40_hkvbLiRzNWteB0ystu4mYOZ9V8JPiT_0yqK3M7NBFkWoDdJpyqf_Txp76W6QB79uUUlX2RKD4lo-gAA","payload":{"content_format":"text/plain","text":"hello"}}}],"next_after":1,"more":false}`)
		var page ChangesPage
		launchRoundTrip(t, raw, &page)
	})
	t.Run("author query with required member", func(t *testing.T) {
		raw := []byte(`{"author_id":"did:key:z6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd"}`)
		var query AuthorMessageListQuery
		launchRoundTrip(t, raw, &query)
	})
}

func TestLaunchZeroPostMessageOmitsOptionalDelegation(t *testing.T) {
	encoded, err := json.Marshal(PostMessage{})
	if err != nil {
		t.Fatalf("marshal zero message: %v", err)
	}
	var members map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &members); err != nil {
		t.Fatalf("decode marshalled message: %v", err)
	}
	if _, ok := members["signer_id"]; ok {
		t.Error("zero message includes signer_id")
	}
	if _, ok := members["delegation"]; ok {
		t.Error("zero message includes delegation")
	}
}
