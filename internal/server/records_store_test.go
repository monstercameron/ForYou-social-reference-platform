package server

import (
	"bytes"
	"testing"
	"time"

	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/sdk"
)

func TestNetworkRecordSecondReceipt(t *testing.T) {
	rawA, messageA := nrTestReceipt(t, 0, "One intent, two receipts.", nrTestNow.Add(-time.Hour), "")
	rawB, messageB := nrTestReceipt(t, 1, "One intent, two receipts.", nrTestNow.Add(-time.Hour), "")
	if messageA.MessageID != messageB.MessageID {
		t.Fatalf("message IDs %q and %q differ; the receipts must answer one intent", messageA.MessageID, messageB.MessageID)
	}
	providerA := keys.DIDOf(devnet.ProviderKey(0))
	providerB := keys.DIDOf(devnet.ProviderKey(1))
	clock := nrTestNow
	client := nrTestClient(t, t.TempDir(), &clock, []Endpoint{{URL: "https://provider-b.example", ServiceID: providerB, Label: "Provider B"}}, Endpoint{URL: "https://vault.example", ServiceID: providerA, Label: "Vault"})
	s, ok := client.app.networkSource()
	if !ok {
		t.Fatal("the network record source is not selected")
	}
	rd := s.reader(nrTestRequest())
	fromA := sdk.Fetched{Raw: rawA, Message: messageA, ID: messageA.MessageID, Endpoint: "https://vault.example", ServiceID: providerA}
	fromB := sdk.Fetched{Raw: rawB, Message: messageB, ID: messageB.MessageID, Endpoint: "https://provider-b.example", ServiceID: providerB}
	t.Run("the first receipt is held from the vault", func(t *testing.T) {
		if _, shown := s.show(rd, fromA, sdk.SourceVault); !shown {
			t.Fatal("first receipt from the vault was not shown")
		}
	})
	t.Run("a second receipt from its provider is shown but not admitted", func(t *testing.T) {
		p, shown := s.show(rd, fromB, sdk.SourceProvider)
		if !shown || p.Class != "cryptographically_valid_trusted_unaccepted" {
			t.Fatalf("second receipt class %q, shown %v; want cryptographically_valid_trusted_unaccepted", p.Class, shown)
		}
	})
	t.Run("a later read classifies the second receipt the same way", func(t *testing.T) {
		p, shown := s.show(rd, fromB, sdk.SourceProvider)
		if !shown || p.Class != "cryptographically_valid_trusted_unaccepted" {
			t.Fatalf("later read of the second receipt class %q, shown %v; want cryptographically_valid_trusted_unaccepted", p.Class, shown)
		}
	})
	t.Run("the local copy keeps the first receipt's bytes", func(t *testing.T) {
		entry, ok := s.store.get(messageA.MessageID)
		if !ok || !bytes.Equal(entry.Raw, rawA) {
			t.Fatalf("held copy found %v; want the first receipt's bytes", ok)
		}
	})
	t.Run("the local copy keeps the first receipt's source", func(t *testing.T) {
		entry, ok := s.store.get(messageA.MessageID)
		if !ok || entry.Source.Endpoint != "https://vault.example" {
			t.Fatalf("held source endpoint %q, found %v; want https://vault.example", entry.Source.Endpoint, ok)
		}
	})
}
