package sdk

import (
	"crypto/ed25519"
	"encoding/json"
	"os"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/message"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

func vfVersionMessages(t *testing.T) []json.RawMessage {
	t.Helper()
	b, err := os.ReadFile("testdata/vf/post-version-chain.json")
	if err != nil {
		t.Fatal(err)
	}
	var example struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(b, &example); err != nil {
		t.Fatal(err)
	}
	return example.Messages
}

func vfSignedRecord(t *testing.T, issuedAt time.Time) ([]byte, string) {
	t.Helper()
	seed := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31}
	author := ed25519.NewKeyFromSeed(seed)
	genesis := devnet.Genesis()
	schedule := genesis.FeeSchedules[0]
	intent := wire.Intent{AuthorID: keys.DIDOf(author), Timestamp: wire.FormatTime(time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)), MessageType: "post", Version: wire.Version{Major: 2, Minor: 0}, Payload: wire.PostPayload{ContentFormat: "text/plain", Text: "fee test"}}
	id, err := message.ID(intent)
	if err != nil {
		t.Fatal(err)
	}
	provider := devnet.ProviderKey(0)
	receipt := wire.SubmissionReceipt{ProviderID: keys.DIDOf(provider), AuthorID: intent.AuthorID, MessageID: id, ReceiptID: keys.EncodeB64URL(make([]byte, 32)), QuoteID: "fyfq2:0000000000000000000000000000000000000000000000000000000000000000", ScheduleID: schedule.ScheduleID, Network: schedule.Network, AmountBaseUnits: "136", AuthorizationAt: wire.FormatTime(issuedAt), IssuedAt: wire.FormatTime(issuedAt)}
	receipt, err = fee.SignReceipt(receipt, provider)
	if err != nil {
		t.Fatal(err)
	}
	post, err := message.Assemble(intent, receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := message.Sign(&post, author); err != nil {
		t.Fatal(err)
	}
	raw, err := canon.Encode(post)
	if err != nil {
		t.Fatal(err)
	}
	return raw, receipt.ProviderID
}

func TestVerifyApprovedDevnetRecord(t *testing.T) {
	t.Run("signed post verifies with the specified fee and local trust", func(t *testing.T) {
		genesis := devnet.Genesis()
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		root, err := BuildTrustRoot(Pin{ReleaseKeyIDs: genesis.ReleaseKeyIDs, Threshold: int(genesis.ReleaseThreshold)}, Publications{Genesis: genesis, FetchedAt: now})
		if err != nil {
			t.Fatal(err)
		}
		raw, provider := vfSignedRecord(t, now)
		rec := NewVerifier(VerifierConfig{Root: root, Trusts: func(id string) bool { return id == provider }, Now: func() time.Time { return now }}).Verify(raw, "", Source{})
		if rec.Failure != nil || rec.Class != ClassValidTrustedUnaccepted || rec.Fee != "136" || rec.Denomination != "millisatoshi" || rec.Settlement != settlement.StateUnsettled {
			t.Fatalf("record = %#v", rec)
		}
	})
	t.Run("untrusted provider stays cryptographically valid", func(t *testing.T) {
		genesis := devnet.Genesis()
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		root, err := BuildTrustRoot(Pin{ReleaseKeyIDs: genesis.ReleaseKeyIDs, Threshold: int(genesis.ReleaseThreshold)}, Publications{Genesis: genesis, FetchedAt: now})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := vfSignedRecord(t, now)
		rec := NewVerifier(VerifierConfig{Root: root, Now: func() time.Time { return now }}).Verify(raw, "", Source{})
		if rec.Failure != nil || rec.Class != ClassValidUntrusted {
			t.Fatalf("failure/class = %#v, %q", rec.Failure, rec.Class)
		}
	})
	t.Run("receipt issued 301 seconds after message timestamp fails step five", func(t *testing.T) {
		genesis := devnet.Genesis()
		now := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		root, err := BuildTrustRoot(Pin{ReleaseKeyIDs: genesis.ReleaseKeyIDs, Threshold: int(genesis.ReleaseThreshold)}, Publications{Genesis: genesis, FetchedAt: now})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := vfSignedRecord(t, now.Add(301*time.Second))
		rec := NewVerifier(VerifierConfig{Root: root, Now: func() time.Time { return now.Add(301 * time.Second) }}).Verify(raw, "", Source{})
		if rec.Failure == nil || rec.Failure.Step != 5 {
			t.Fatalf("failure = %#v", rec.Failure)
		}
	})
	t.Run("receipt 61 seconds ahead of local clock is not evaluated yet, never invalid", func(t *testing.T) {
		genesis := devnet.Genesis()
		issued := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		root, err := BuildTrustRoot(Pin{ReleaseKeyIDs: genesis.ReleaseKeyIDs, Threshold: int(genesis.ReleaseThreshold)}, Publications{Genesis: genesis, FetchedAt: issued})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := vfSignedRecord(t, issued)
		rec := NewVerifier(VerifierConfig{Root: root, Now: func() time.Time { return issued.Add(-61 * time.Second) }}).Verify(raw, "", Source{})
		if rec.Class != ClassNotEvaluated || !rec.EvaluateAfter.Equal(issued) || !rec.Rejected() {
			t.Fatalf("class %q, evaluate after %v, rejected %v; want not_evaluated until the receipt time, and not usable as verified", rec.Class, rec.EvaluateAfter, rec.Rejected())
		}
		if settled := NewVerifier(VerifierConfig{Root: root}).Settle(rec, nil, nil); settled.Class != ClassNotEvaluated {
			t.Fatalf("settling changed the class to %q; a record that is not evaluated stays so", settled.Class)
		}
	})
	t.Run("the same receipt verifies once the clock has passed it", func(t *testing.T) {
		genesis := devnet.Genesis()
		issued := time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC)
		root, err := BuildTrustRoot(Pin{ReleaseKeyIDs: genesis.ReleaseKeyIDs, Threshold: int(genesis.ReleaseThreshold)}, Publications{Genesis: genesis, FetchedAt: issued})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := vfSignedRecord(t, issued)
		rec := NewVerifier(VerifierConfig{Root: root, Now: func() time.Time { return issued.Add(-60 * time.Second) }}).Verify(raw, "", Source{})
		if rec.Class != ClassValidUntrusted {
			t.Fatalf("class %q at exactly 60 seconds ahead; want cryptographically_valid_untrusted", rec.Class)
		}
	})
}

func TestVerifyVersionExampleStopsAtFeeSchedule(t *testing.T) {
	t.Run("unapproved synthetic schedule fails step five", func(t *testing.T) {
		raw := vfVersionMessages(t)[0]
		now := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
		root := vfTrust(t, time.Date(2027, 1, 7, 0, 0, 0, 0, time.UTC))
		record := NewVerifier(VerifierConfig{Root: root, Now: func() time.Time { return now }}).Verify(raw, "", Source{Kind: SourceProvider})
		if record.Failure == nil || record.Failure.Step != 5 || record.Class != ClassInvalid {
			t.Fatalf("failure/class = %#v, %q", record.Failure, record.Class)
		}
		if len(record.Checks) != 5 || !record.Checks[0].OK || !record.Checks[1].OK || !record.Checks[2].OK || !record.Checks[3].OK {
			t.Fatalf("checks = %#v", record.Checks)
		}
	})
	t.Run("changed intent fails canonical ID step", func(t *testing.T) {
		var m wire.TransportMessage
		if err := json.Unmarshal(vfVersionMessages(t)[0], &m); err != nil {
			t.Fatal(err)
		}
		m.Payload.Text = "changed"
		raw, err := canon.Encode(m)
		if err != nil {
			t.Fatal(err)
		}
		root := vfTrust(t, time.Date(2027, 1, 7, 0, 0, 0, 0, time.UTC))
		record := NewVerifier(VerifierConfig{Root: root, Now: func() time.Time { return time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC) }}).Verify(raw, "", Source{})
		if record.Failure == nil || record.Failure.Step != 2 {
			t.Fatalf("failure = %#v", record.Failure)
		}
	})
	t.Run("substituted author signature fails step three", func(t *testing.T) {
		messages := vfVersionMessages(t)
		var first, second wire.TransportMessage
		if err := json.Unmarshal(messages[0], &first); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(messages[1], &second); err != nil {
			t.Fatal(err)
		}
		first.Signature = second.Signature
		raw, err := canon.Encode(first)
		if err != nil {
			t.Fatal(err)
		}
		root := vfTrust(t, time.Date(2027, 1, 7, 0, 0, 0, 0, time.UTC))
		record := NewVerifier(VerifierConfig{Root: root, Now: func() time.Time { return time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC) }}).Verify(raw, "", Source{})
		if record.Failure == nil || record.Failure.Step != 3 {
			t.Fatalf("failure = %#v", record.Failure)
		}
	})
	t.Run("changed receipt amount fails provider signature at step four", func(t *testing.T) {
		var m wire.TransportMessage
		if err := json.Unmarshal(vfVersionMessages(t)[0], &m); err != nil {
			t.Fatal(err)
		}
		m.SubmissionReceipt.AmountBaseUnits = "137"
		authorSeed := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31}
		if err := message.Sign(&m, ed25519.NewKeyFromSeed(authorSeed)); err != nil {
			t.Fatal(err)
		}
		raw, err := canon.Encode(m)
		if err != nil {
			t.Fatal(err)
		}
		root := vfTrust(t, time.Date(2027, 1, 7, 0, 0, 0, 0, time.UTC))
		record := NewVerifier(VerifierConfig{Root: root, Now: func() time.Time { return time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC) }}).Verify(raw, "", Source{})
		if record.Failure == nil || record.Failure.Step != 4 || record.Class != ClassInvalid {
			t.Fatalf("failure/class = %#v, %q", record.Failure, record.Class)
		}
	})
}

func TestVerifyProviderLineStopsAtConflictingSuccessions(t *testing.T) {
	t.Run("one old key succeeding to two new keys leaves each new key off the old line", func(t *testing.T) {
		successions := []wire.KeySuccession{
			{SuccessionID: "s1", OldID: "did:x", NewID: "did:y"},
			{SuccessionID: "s2", OldID: "did:x", NewID: "did:z"},
		}
		if line, _ := vfProviderLine("did:y", successions); line != "did:y" {
			t.Fatalf("line from new key y = %q", line)
		}
		if line, _ := vfProviderLine("did:z", successions); line != "did:z" {
			t.Fatalf("line from new key z = %q", line)
		}
		if vfOnLine("did:y", "did:x", successions) {
			t.Fatal("receipt from new key y is on the old key's line")
		}
		if vfOnLine("did:z", "did:x", successions) {
			t.Fatal("receipt from new key z is on the old key's line")
		}
	})
	t.Run("two old keys succeeding to one new key leave the new key unresolved", func(t *testing.T) {
		successions := []wire.KeySuccession{
			{SuccessionID: "s1", OldID: "did:a", NewID: "did:n"},
			{SuccessionID: "s2", OldID: "did:b", NewID: "did:n"},
		}
		if line, _ := vfProviderLine("did:n", successions); line != "did:n" {
			t.Fatalf("line from new key n = %q", line)
		}
		if vfOnLine("did:a", "did:n", successions) {
			t.Fatal("receipt from old key a is on the new key's line")
		}
	})
	t.Run("one unconflicted succession links the new key to the old key's line", func(t *testing.T) {
		successions := []wire.KeySuccession{{SuccessionID: "s1", OldID: "did:x", NewID: "did:y"}}
		if line, _ := vfProviderLine("did:y", successions); line != "did:x" {
			t.Fatalf("line from new key y = %q", line)
		}
	})
}
