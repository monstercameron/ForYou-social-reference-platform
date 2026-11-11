package wire

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func feesRoundTrip(t *testing.T, raw []byte, v any) {
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
	var got any
	gotDecoder := json.NewDecoder(bytes.NewReader(encoded))
	gotDecoder.UseNumber()
	if err := gotDecoder.Decode(&got); err != nil {
		t.Fatalf("decode marshalled value: %v", err)
	}
	var want any
	wantDecoder := json.NewDecoder(bytes.NewReader(raw))
	wantDecoder.UseNumber()
	if err := wantDecoder.Decode(&want); err != nil {
		t.Fatalf("decode source value: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip changed JSON value\n got: %#v\nwant: %#v", got, want)
	}
}

func feesReadExample(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile("testdata/fees/" + name)
	if err != nil {
		t.Fatalf("read example: %v", err)
	}
	return raw
}

func feesExample(t *testing.T, file, key string) []byte {
	t.Helper()
	var document map[string]json.RawMessage
	if err := json.Unmarshal(feesReadExample(t, file), &document); err != nil {
		t.Fatalf("decode example %q: %v", file, err)
	}
	raw, ok := document[key]
	if !ok {
		t.Fatalf("example %q has no %q member", file, key)
	}
	return raw
}

func TestFeesExampleRoundTrips(t *testing.T) {
	t.Run("genesis manifest", func(t *testing.T) {
		feesRoundTrip(t, feesExample(t, "settlement.example.json", "genesis_manifest"), &GenesisManifest{})
	})
	t.Run("distribution statement", func(t *testing.T) {
		feesRoundTrip(t, feesExample(t, "settlement.example.json", "distribution_statement"), &DistributionStatement{})
	})
	t.Run("foundation log head", func(t *testing.T) {
		feesRoundTrip(t, feesExample(t, "settlement.example.json", "foundation_log_head"), &FoundationLogHead{})
	})
	t.Run("vault receipt", func(t *testing.T) {
		feesRoundTrip(t, feesExample(t, "settlement.example.json", "vault_receipt"), &VaultReceipt{})
	})
	t.Run("vault status", func(t *testing.T) {
		feesRoundTrip(t, feesExample(t, "settlement.example.json", "vault_status"), &VaultStatus{})
	})
	t.Run("fee schedule", func(t *testing.T) {
		feesRoundTrip(t, feesExample(t, "fee-schedule.example.json", "schedule"), &FeeSchedule{})
	})
	t.Run("quotes", func(t *testing.T) {
		feesRoundTrip(t, feesExample(t, "post-with-media-slots.json", "quotes"), &[]FeeQuote{})
	})
}

func TestFeesOptionalAndNullableFields(t *testing.T) {
	t.Run("Bitcoin transfer preserves zero output index", func(t *testing.T) {
		var transfer DepositTransfer
		feesRoundTrip(t, []byte(`{"network":"bitcoin-mainnet","txid":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","output_index":0}`), &transfer)
		if transfer.OutputIndex == nil || *transfer.OutputIndex != 0 {
			t.Fatalf("OutputIndex = %v; want pointer to zero", transfer.OutputIndex)
		}
	})
	t.Run("Ethereum transfer omits Bitcoin fields", func(t *testing.T) {
		var transfer DepositTransfer
		feesRoundTrip(t, []byte(`{"network":"ethereum-mainnet","tx_hash":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}`), &transfer)
	})
	t.Run("withdrawal transfer is null", func(t *testing.T) {
		var status WithdrawalStatus
		feesRoundTrip(t, []byte(`{"provider_id":"did:key:z6MkhFwXNFWosLeugvSf4wcL9t3uuRXueGSFTRgSvHhWj5G2","author_id":"did:key:z6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd","network":"bitcoin-mainnet","amount_base_units":"1","recipient":"bc1qw508d6qejxtdg4y5r3zarvary0c5xw7kv8f3t4","request_id":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","status":"pending","transfer":null,"updated_at":"2026-11-11T00:00:00Z","provider_signature":"WY35B3jN44O6GSfQu70jKYvC69W65jo6zuzkofBFD1wEE2hSKcwglpSINSukgb0tfCH5KpOcIPikwE8wKEJqCg"}`), &status)
		if status.Transfer != nil {
			t.Fatalf("Transfer = %#v; want nil", status.Transfer)
		}
	})
	t.Run("Foundation log page head is null", func(t *testing.T) {
		var page FoundationLogPage
		feesRoundTrip(t, []byte(`{"entries":[],"head":null}`), &page)
		if page.Head != nil {
			t.Fatalf("Head = %#v; want nil", page.Head)
		}
	})
	t.Run("previous release update is null", func(t *testing.T) {
		var update ReleaseKeySetUpdate
		feesRoundTrip(t, []byte(`{"update_id":"fyku2:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","sequence":1,"previous_update_id":null,"release_key_ids":["did:key:z6Mkf3cuJVj9Frb5QcR2P7qx8tB86LfzbVS1NaGKuYc7ppjM","did:key:z6MkfUA6q63QayZpLUtQTtotdTSD3VsTk6JNpMpCeJ7CJpb6","did:key:z6MkhkzxiZyrERq5s5RXGZRwboQ3eJhK9rgA7nvTTHP18VxC","did:key:z6Mkq9YUgmGe3uY4RBebSnCENnMkQvBLG1bYAdW3cyWn74YX","did:key:z6Mks9FrbHCWz1axuS2g8X323RZsJkqxi3e3e7c5B2c1PPKB"],"release_threshold":4,"prior_objects":[],"effective_at":"2026-11-11T00:00:00Z","release_signatures":[{"release_key_id":"did:key:z6Mkf3cuJVj9Frb5QcR2P7qx8tB86LfzbVS1NaGKuYc7ppjM","signature":"WY35B3jN44O6GSfQu70jKYvC69W65jo6zuzkofBFD1wEE2hSKcwglpSINSukgb0tfCH5KpOcIPikwE8wKEJqCg"},{"release_key_id":"did:key:z6MkfUA6q63QayZpLUtQTtotdTSD3VsTk6JNpMpCeJ7CJpb6","signature":"WY35B3jN44O6GSfQu70jKYvC69W65jo6zuzkofBFD1wEE2hSKcwglpSINSukgb0tfCH5KpOcIPikwE8wKEJqCg"},{"release_key_id":"did:key:z6MkhkzxiZyrERq5s5RXGZRwboQ3eJhK9rgA7nvTTHP18VxC","signature":"WY35B3jN44O6GSfQu70jKYvC69W65jo6zuzkofBFD1wEE2hSKcwglpSINSukgb0tfCH5KpOcIPikwE8wKEJqCg"},{"release_key_id":"did:key:z6Mkq9YUgmGe3uY4RBebSnCENnMkQvBLG1bYAdW3cyWn74YX","signature":"WY35B3jN44O6GSfQu70jKYvC69W65jo6zuzkofBFD1wEE2hSKcwglpSINSukgb0tfCH5KpOcIPikwE8wKEJqCg"}]}`), &update)
		if update.PreviousUpdateID != nil {
			t.Fatalf("PreviousUpdateID = %#v; want nil", update.PreviousUpdateID)
		}
	})
	t.Run("preflight intent is preserved", func(t *testing.T) {
		var request FeePreflightRequest
		feesRoundTrip(t, []byte(`{"provider_id":"did:key:z6MkhFwXNFWosLeugvSf4wcL9t3uuRXueGSFTRgSvHhWj5G2","author_id":"did:key:z6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd","schedule_id":"fyfs2:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","intent":{"message_type":"post","payload":{"text":"hello"}},"authorization":"WY35B3jN44O6GSfQu70jKYvC69W65jo6zuzkofBFD1wEE2hSKcwglpSINSukgb0tfCH5KpOcIPikwE8wKEJqCg"}`), &request)
	})
}

func TestFeesAdditionalObjectsRoundTrip(t *testing.T) {
	t.Run("deposit credit", func(t *testing.T) {
		var credit DepositCredit
		feesRoundTrip(t, []byte(`{"credit_id":"fydc2:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","provider_id":"did:key:z6MkhFwXNFWosLeugvSf4wcL9t3uuRXueGSFTRgSvHhWj5G2","author_id":"did:key:z6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd","deposit_id":"fydi2:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","network":"ethereum-mainnet","transfer":{"network":"ethereum-mainnet","tx_hash":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"},"amount_chain_units":"1","amount_accounting_units":"1","credit_sequence":1,"credited_at":"2026-11-11T00:00:00Z","provider_signature":"WY35B3jN44O6GSfQu70jKYvC69W65jo6zuzkofBFD1wEE2hSKcwglpSINSukgb0tfCH5KpOcIPikwE8wKEJqCg"}`), &credit)
	})
	t.Run("balance status", func(t *testing.T) {
		var status BalanceStatus
		feesRoundTrip(t, []byte(`{"provider_id":"did:key:z6MkhFwXNFWosLeugvSf4wcL9t3uuRXueGSFTRgSvHhWj5G2","author_id":"did:key:z6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd","network":"bitcoin-mainnet","denomination":"millisatoshi","available":"1","frozen":"0","reserved":"0","revision":0,"updated_at":"2026-11-11T00:00:00Z","provider_signature":"WY35B3jN44O6GSfQu70jKYvC69W65jo6zuzkofBFD1wEE2hSKcwglpSINSukgb0tfCH5KpOcIPikwE8wKEJqCg"}`), &status)
	})
	t.Run("Foundation key closure", func(t *testing.T) {
		var closure FoundationKeyClosure
		feesRoundTrip(t, []byte(`{"foundation_id":"did:key:z6MktFovzcapNZyZBWzFJpCXf26B8XLKdXtwfwnXXFebPgzM","closed_at":"2026-11-11T00:00:00Z","head":{"foundation_id":"did:key:z6MktFovzcapNZyZBWzFJpCXf26B8XLKdXtwfwnXXFebPgzM","size":0,"root":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","signed_at":"2026-11-11T00:00:00Z","foundation_signature":"WY35B3jN44O6GSfQu70jKYvC69W65jo6zuzkofBFD1wEE2hSKcwglpSINSukgb0tfCH5KpOcIPikwE8wKEJqCg"},"voided":[],"release_signatures":[{"release_key_id":"did:key:z6Mkf3cuJVj9Frb5QcR2P7qx8tB86LfzbVS1NaGKuYc7ppjM","signature":"WY35B3jN44O6GSfQu70jKYvC69W65jo6zuzkofBFD1wEE2hSKcwglpSINSukgb0tfCH5KpOcIPikwE8wKEJqCg"},{"release_key_id":"did:key:z6MkfUA6q63QayZpLUtQTtotdTSD3VsTk6JNpMpCeJ7CJpb6","signature":"WY35B3jN44O6GSfQu70jKYvC69W65jo6zuzkofBFD1wEE2hSKcwglpSINSukgb0tfCH5KpOcIPikwE8wKEJqCg"},{"release_key_id":"did:key:z6MkhkzxiZyrERq5s5RXGZRwboQ3eJhK9rgA7nvTTHP18VxC","signature":"WY35B3jN44O6GSfQu70jKYvC69W65jo6zuzkofBFD1wEE2hSKcwglpSINSukgb0tfCH5KpOcIPikwE8wKEJqCg"},{"release_key_id":"did:key:z6Mkq9YUgmGe3uY4RBebSnCENnMkQvBLG1bYAdW3cyWn74YX","signature":"WY35B3jN44O6GSfQu70jKYvC69W65jo6zuzkofBFD1wEE2hSKcwglpSINSukgb0tfCH5KpOcIPikwE8wKEJqCg"}]}`), &closure)
	})
	t.Run("fee schedules page", func(t *testing.T) {
		var page FeeSchedulesPage
		feesRoundTrip(t, []byte(`{"schedules":[{"schedule":{"schedule_id":"fyfs2:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","profile_major":2,"profile_minor":0,"revision":1,"network":"bitcoin-mainnet","denomination":"millisatoshi","base_fee_base_units":"1","per_kib_fee_base_units":"1","effective_at":"2026-01-01T00:00:00Z","expires_at":"2027-01-01T00:00:00Z"},"approval":{"schedule_id":"fyfs2:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","approved_at":"2025-11-01T00:00:00Z","drift":false,"release_signatures":[{"release_key_id":"did:key:z6Mkf3cuJVj9Frb5QcR2P7qx8tB86LfzbVS1NaGKuYc7ppjM","signature":"WY35B3jN44O6GSfQu70jKYvC69W65jo6zuzkofBFD1wEE2hSKcwglpSINSukgb0tfCH5KpOcIPikwE8wKEJqCg"},{"release_key_id":"did:key:z6MkfUA6q63QayZpLUtQTtotdTSD3VsTk6JNpMpCeJ7CJpb6","signature":"WY35B3jN44O6GSfQu70jKYvC69W65jo6zuzkofBFD1wEE2hSKcwglpSINSukgb0tfCH5KpOcIPikwE8wKEJqCg"},{"release_key_id":"did:key:z6MkhkzxiZyrERq5s5RXGZRwboQ3eJhK9rgA7nvTTHP18VxC","signature":"WY35B3jN44O6GSfQu70jKYvC69W65jo6zuzkofBFD1wEE2hSKcwglpSINSukgb0tfCH5KpOcIPikwE8wKEJqCg"},{"release_key_id":"did:key:z6Mkq9YUgmGe3uY4RBebSnCENnMkQvBLG1bYAdW3cyWn74YX","signature":"WY35B3jN44O6GSfQu70jKYvC69W65jo6zuzkofBFD1wEE2hSKcwglpSINSukgb0tfCH5KpOcIPikwE8wKEJqCg"}]}}]}`), &page)
	})
}
