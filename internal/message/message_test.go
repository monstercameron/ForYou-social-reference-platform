package message

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

const msgNow = "2026-11-11T00:01:00Z"

// Source files: tests/security-vectors.json, examples/post-comment-thread.json,
// examples/post-with-media-slots.json and examples/post-with-refs.json.
func msgFixture(t *testing.T, name string) ([]byte, any) {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	tree, err := canon.Parse(data, canon.MessageLimits)
	if err != nil {
		t.Fatal(err)
	}
	return data, tree
}

func msgVector(t *testing.T) (wire.PostMessage, []byte) {
	t.Helper()
	data, tree := msgFixture(t, "security-vectors.json")
	object := tree.(map[string]any)
	message, err := canon.Marshal(object["message"])
	if err != nil {
		t.Fatal(err)
	}
	messageTree, err := canon.Parse(message, canon.MessageLimits)
	if err != nil || wire.Check("PostMessage", messageTree) != nil {
		t.Fatal("security vector message does not match PostMessage")
	}
	var m wire.PostMessage
	if err := json.Unmarshal(message, &m); err != nil {
		t.Fatal(err)
	}
	return m, data
}

func msgExampleMessage(t *testing.T, filename string, index int) wire.PostMessage {
	t.Helper()
	_, tree := msgFixture(t, filename)
	fixture := tree.(map[string]any)
	items, ok := fixture["messages"].([]any)
	var item any
	if ok {
		if index >= len(items) {
			t.Fatal("fixture has no requested message")
		}
		item = items[index]
	} else if index == 0 {
		item = fixture["message"]
	} else {
		t.Fatal("fixture has no requested message")
	}
	encoded, err := canon.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	value, err := canon.Parse(encoded, canon.MessageLimits)
	if err != nil || wire.Check("PostMessage", value) != nil {
		t.Fatal("example message does not match PostMessage")
	}
	var m wire.PostMessage
	if err := json.Unmarshal(encoded, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func msgStatus(t *testing.T, err error, want apierr.Status) {
	t.Helper()
	if err == nil {
		t.Fatal("expected an error")
	}
	got, ok := apierr.StatusOf(err)
	if !ok || got != want {
		t.Fatalf("status = %q, %v; want %q", got, ok, want)
	}
}

func msgNowTime(t *testing.T) time.Time {
	t.Helper()
	parsed, err := wire.ParseTime(msgNow)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

func TestID(t *testing.T) {
	t.Run("security vector", func(t *testing.T) {
		m, _ := msgVector(t)
		got, err := ID(IntentOf(m))
		if err != nil || got != "fy1:27e02e49a7cebf57976046e00d00ddd5e502cf700394f80c514adb5551bcab37" {
			t.Fatalf("ID = %q, %v", got, err)
		}
	})
}

func TestVerify(t *testing.T) {
	t.Run("security vector", func(t *testing.T) {
		m, _ := msgVector(t)
		if err := Verify(m, VerifyOptions{Now: msgNowTime(t)}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("comment thread root", func(t *testing.T) {
		if err := Verify(msgExampleMessage(t, "post-comment-thread.json", 0), VerifyOptions{Now: msgNowTime(t)}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("comment thread comment", func(t *testing.T) {
		if err := Verify(msgExampleMessage(t, "post-comment-thread.json", 1), VerifyOptions{Now: msgNowTime(t)}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("comment thread nested comment", func(t *testing.T) {
		if err := Verify(msgExampleMessage(t, "post-comment-thread.json", 2), VerifyOptions{Now: msgNowTime(t)}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("media slots", func(t *testing.T) {
		if err := Verify(msgExampleMessage(t, "post-with-media-slots.json", 0), VerifyOptions{Now: msgNowTime(t)}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("structured references", func(t *testing.T) {
		if err := Verify(msgExampleMessage(t, "post-with-refs.json", 0), VerifyOptions{Now: msgNowTime(t)}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("intent ID mismatch", func(t *testing.T) {
		m, _ := msgVector(t)
		m.Payload.ReplyTo = "fy1:" + strings.Repeat("b", 64)
		msgStatus(t, Verify(m, VerifyOptions{Now: msgNowTime(t)}), apierr.IDMismatch)
	})
	t.Run("signature mismatch", func(t *testing.T) {
		m, _ := msgVector(t)
		m.Signature = "x" + m.Signature[1:]
		err := Verify(m, VerifyOptions{Now: msgNowTime(t)})
		msgStatus(t, err, apierr.InvalidMessage)
		if !strings.Contains(err.Error(), "message signature") {
			t.Fatalf("error %q does not identify the signature rule", err)
		}
	})
	t.Run("receipt author mismatch", func(t *testing.T) {
		m, _ := msgVector(t)
		m.SubmissionReceipt.AuthorID = "did:key:z6Mkg26jczDiqsPK4momfvhZTTyFefWEyxYiSisFJ2wWJFkg"
		err := Verify(m, VerifyOptions{Now: msgNowTime(t)})
		msgStatus(t, err, apierr.InvalidMessage)
		if !strings.Contains(err.Error(), "receipt author binding") {
			t.Fatalf("error %q does not identify the receipt author rule", err)
		}
	})
	t.Run("receipt 61 seconds ahead", func(t *testing.T) {
		m, _ := msgVector(t)
		now, _ := wire.ParseTime("2026-11-10T23:58:59Z")
		err := Verify(m, VerifyOptions{Now: now})
		msgStatus(t, err, apierr.InvalidMessage)
		if !strings.Contains(err.Error(), "receipt issued_at more than 60 seconds ahead") {
			t.Fatalf("error %q does not identify the receipt time rule", err)
		}
	})
	t.Run("receipt exactly 60 seconds ahead", func(t *testing.T) {
		m, _ := msgVector(t)
		now, _ := wire.ParseTime("2026-11-10T23:59:00Z")
		if err := Verify(m, VerifyOptions{Now: now}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("copied receipt after message ID recompute", func(t *testing.T) {
		m, _ := msgVector(t)
		m.Payload.Text = "changed after the receipt was issued"
		id, err := ID(IntentOf(m))
		if err != nil {
			t.Fatal(err)
		}
		m.MessageID = id
		author, err := keys.NewKeyFromSeed([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31})
		if err != nil {
			t.Fatal(err)
		}
		if err := Sign(&m, author); err != nil {
			t.Fatal(err)
		}
		err = Verify(m, VerifyOptions{Now: msgNowTime(t)})
		msgStatus(t, err, apierr.InvalidMessage)
		if !strings.Contains(err.Error(), "receipt message ID binding") {
			t.Fatalf("error %q does not identify the receipt message ID rule", err)
		}
	})
}

func TestValidateIntent(t *testing.T) {
	t.Run("invalid leap day", func(t *testing.T) {
		m, _ := msgVector(t)
		i := IntentOf(m)
		i.Timestamp = "2026-02-29T00:00:00Z"
		msgStatus(t, ValidateIntent(i), apierr.InvalidMessage)
	})
	t.Run("unsupported minor version", func(t *testing.T) {
		m, _ := msgVector(t)
		i := IntentOf(m)
		i.Version.Minor = 1
		msgStatus(t, ValidateIntent(i), apierr.InvalidMessage)
	})
	t.Run("intent exceeds message budget", func(t *testing.T) {
		m, _ := msgVector(t)
		i := IntentOf(m)
		i.Payload.Text = strings.Repeat("a", 1048576)
		msgStatus(t, ValidateIntent(i), apierr.MessageTooLarge)
	})
	t.Run("slot and reference IDs use separate namespaces", func(t *testing.T) {
		m, _ := msgVector(t)
		i := IntentOf(m)
		slots := []wire.MediaReference{{ID: "a", URL: "https://example.com/a", Kind: "image", SHA256: strings.Repeat("a", 64)}}
		refs := []wire.PostReference{{ID: "a", Target: m.MessageID}}
		i.Payload.MediaSlots = &slots
		i.Payload.Refs = &refs
		if err := ValidateIntent(i); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCheckTimestamp(t *testing.T) {
	check := func(t *testing.T, timestamp string, wantError bool) {
		t.Helper()
		m, _ := msgVector(t)
		i := IntentOf(m)
		now, _ := wire.ParseTime("2026-11-11T00:05:00Z")
		i.Timestamp = timestamp
		err := CheckTimestamp(i, now)
		if wantError {
			msgStatus(t, err, apierr.InvalidMessage)
			return
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Run("300 seconds old passes", func(t *testing.T) { check(t, "2026-11-11T00:00:00Z", false) })
	t.Run("301 seconds old fails", func(t *testing.T) { check(t, "2026-11-10T23:59:59Z", true) })
	t.Run("60 seconds ahead passes", func(t *testing.T) { check(t, "2026-11-11T00:06:00Z", false) })
	t.Run("61 seconds ahead fails", func(t *testing.T) { check(t, "2026-11-11T00:06:01Z", true) })
}

func TestFinalSize(t *testing.T) {
	t.Run("vector exact size", func(t *testing.T) {
		m, _ := msgVector(t)
		size, err := FinalSize(IntentOf(m), m.SubmissionReceipt)
		if err != nil || size != 1139 {
			t.Fatalf("FinalSize = %d, %v; want 1139", size, err)
		}
	})
	t.Run("maximum accepted budget", func(t *testing.T) {
		m, _ := msgVector(t)
		i := IntentOf(m)
		i.Payload.Text = strings.Repeat("a", 1047456)
		size, err := FinalSize(i, m.SubmissionReceipt)
		if err != nil || size != MaxMessageBytes {
			t.Fatalf("FinalSize = %d, %v; want %d", size, err, MaxMessageBytes)
		}
		if err := CheckBudget(i, m.SubmissionReceipt); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("over maximum budget", func(t *testing.T) {
		m, _ := msgVector(t)
		i := IntentOf(m)
		i.Payload.Text = strings.Repeat("a", 1047457)
		msgStatus(t, CheckBudget(i, m.SubmissionReceipt), apierr.MessageTooLarge)
	})
}

func TestAssembleAndSign(t *testing.T) {
	t.Run("author signs assembled message", func(t *testing.T) {
		m, _ := msgVector(t)
		i := IntentOf(m)
		assembled, err := Assemble(i, m.SubmissionReceipt)
		if err != nil {
			t.Fatal(err)
		}
		seed := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31}
		priv, err := keys.NewKeyFromSeed(seed)
		if err != nil {
			t.Fatal(err)
		}
		if err := Sign(&assembled, priv); err != nil {
			t.Fatal(err)
		}
		if err := Verify(assembled, VerifyOptions{Now: msgNowTime(t)}); err != nil {
			t.Fatal(err)
		}
		wrong, err := keys.NewKeyFromSeed([]byte{31, 30, 29, 28, 27, 26, 25, 24, 23, 22, 21, 20, 19, 18, 17, 16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1, 0})
		if err != nil {
			t.Fatal(err)
		}
		msgStatus(t, Sign(&assembled, wrong), apierr.InvalidMessage)
	})
}

func TestDelegationVerification(t *testing.T) {
	t.Run("nil verifier fails closed", func(t *testing.T) {
		m, _, _ := msgDelegatedPost(t)
		msgStatus(t, Verify(m, VerifyOptions{Now: msgNowTime(t)}), apierr.InvalidMessage)
	})
	t.Run("callback receives historical bindings", func(t *testing.T) {
		m, providerID, wantAt := msgDelegatedPost(t)
		called := false
		err := Verify(m, VerifyOptions{Now: msgNowTime(t), Delegation: func(_ wire.Delegation, _ wire.Intent, gotProvider string, at time.Time) error {
			called = true
			if gotProvider != providerID || !at.Equal(wantAt) {
				t.Fatalf("callback bindings = %q, %s", gotProvider, at)
			}
			return nil
		}})
		if err != nil || !called {
			t.Fatalf("Verify = %v, callback called = %v", err, called)
		}
	})
	t.Run("public callback error is preserved", func(t *testing.T) {
		m, _, _ := msgDelegatedPost(t)
		err := Verify(m, VerifyOptions{Now: msgNowTime(t), Delegation: func(wire.Delegation, wire.Intent, string, time.Time) error {
			return apierr.New(apierr.DelegationRevoked, nil)
		}})
		msgStatus(t, err, apierr.DelegationRevoked)
	})
}

func msgDelegatedPost(t *testing.T) (wire.PostMessage, string, time.Time) {
	t.Helper()
	author, err := keys.NewKeyFromSeed([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31})
	if err != nil {
		t.Fatal(err)
	}
	provider, err := keys.NewKeyFromSeed([]byte{32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63})
	if err != nil {
		t.Fatal(err)
	}
	delegate, err := keys.NewKeyFromSeed([]byte{64, 65, 66, 67, 68, 69, 70, 71, 72, 73, 74, 75, 76, 77, 78, 79, 80, 81, 82, 83, 84, 85, 86, 87, 88, 89, 90, 91, 92, 93, 94, 95})
	if err != nil {
		t.Fatal(err)
	}
	authorID, providerID, delegateID := keys.DIDOf(author), keys.DIDOf(provider), keys.DIDOf(delegate)
	nonce := keys.EncodeB64URL([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31})
	delegation := wire.Delegation{
		AuthorID: authorID, SignerID: delegateID, DelegationID: "fyd1:" + strings.Repeat("a", 64),
		ValidFrom: "2026-11-11T00:00:00Z", ValidUntil: "2026-11-11T01:00:00Z",
		DelegationSignature: strings.Repeat("A", SignatureLength), ProviderID: providerID,
		Scope: []string{"post"}, Epoch: wire.AuthorizationEpoch{
			EpochID: "fye1:" + strings.Repeat("b", 64), ProviderID: providerID, AuthorID: authorID,
			EpochSequence: 1, ActivatedAt: "2026-11-11T00:00:00Z", Nonce: nonce,
			ProviderSignature: strings.Repeat("A", SignatureLength),
		},
	}
	i := wire.Intent{AuthorID: authorID, SignerID: delegateID, Delegation: &delegation,
		Timestamp: "2026-11-11T00:00:00Z", MessageType: "post", Version: wire.Version{Major: 2},
		Payload: wire.PostPayload{ContentFormat: "text/plain", Text: "delegated"}}
	id, err := ID(i)
	if err != nil {
		t.Fatal(err)
	}
	receipt := wire.SubmissionReceipt{ProviderID: providerID, AuthorID: authorID, MessageID: id,
		ReceiptID: nonce, QuoteID: "fyfq2:" + strings.Repeat("c", 64), ScheduleID: "fyfs2:" + strings.Repeat("d", 64),
		Network: wire.BitcoinMainnet, AmountBaseUnits: "1", AuthorizationAt: "2026-11-11T00:00:00Z", IssuedAt: "2026-11-11T00:00:00Z"}
	receiptCanonical, err := canon.EncodeWithout(receipt, "provider_signature")
	if err != nil {
		t.Fatal(err)
	}
	receipt.ProviderSignature, err = keys.Sign(provider, "receipt", receiptCanonical)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Assemble(i, receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := Sign(&m, delegate); err != nil {
		t.Fatal(err)
	}
	at, err := wire.ParseTime(receipt.AuthorizationAt)
	if err != nil {
		t.Fatal(err)
	}
	return m, providerID, at
}
