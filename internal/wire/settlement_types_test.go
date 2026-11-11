package wire

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
)

func settleRoundTrip(t *testing.T, raw []byte, v any) {
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

	settleDecodeValue := func(document []byte) any {
		t.Helper()
		decoder := json.NewDecoder(bytes.NewReader(document))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			t.Fatalf("decode JSON value: %v", err)
		}
		return value
	}

	if got, want := settleDecodeValue(encoded), settleDecodeValue(raw); !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip changed JSON value:\n got: %#v\nwant: %#v", got, want)
	}
}

func settleExample(t *testing.T) map[string]json.RawMessage {
	t.Helper()

	raw, err := os.ReadFile("testdata/settle/settlement.example.json")
	if err != nil {
		t.Fatalf("read settlement example: %v", err)
	}
	var example map[string]json.RawMessage
	if err := json.Unmarshal(raw, &example); err != nil {
		t.Fatalf("decode settlement example: %v", err)
	}
	return example
}

func settleExampleMember(t *testing.T, example map[string]json.RawMessage, name string) json.RawMessage {
	t.Helper()
	raw, ok := example[name]
	if !ok {
		t.Fatalf("settlement example has no %q member", name)
	}
	return raw
}

func settleClaimJSON(claimID string) string {
	return fmt.Sprintf(`{"claim_id":%q,"provider_id":"did:key:z6MkhFwXNFWosLeugvSf4wcL9t3uuRXueGSFTRgSvHhWj5G2","network":"bitcoin-mainnet","sequence":1,"previous_claim_id":null,"period_start":"2027-01-05T00:00:00Z","period_end":"2027-01-06T00:00:00Z","declaration_id":"fyrd2:16cadc2610d20c84575d03a409c29beee0acf70fb6e963800814340d3acb17d1","succession_id":null,"receipt_count":1,"total_base_units":"136","cumulative_base_units":"136","receipts_root":"a3b611487ed295e46340f4132f3281d8c32e0e8dc9546776be535594c2e3cfa9","provider_signature":"OcPmII9r9BQaZzkzV0UeFnOxGH9EK0Gg4AivCdMrXCgJ4nrCGi0qBNOTUb5f3G1-90t78KHSy7h1y__F4BHFCw"}`, claimID)
}

func TestSettleExampleRoundTrips(t *testing.T) {
	example := settleExample(t)
	t.Run("retention declarations", func(t *testing.T) {
		var got []RetentionDeclaration
		settleRoundTrip(t, settleExampleMember(t, example, "retention_declarations"), &got)
	})
	t.Run("settlement claim", func(t *testing.T) {
		var got SettlementClaim
		settleRoundTrip(t, settleExampleMember(t, example, "settlement_claim"), &got)
	})
	t.Run("second claim", func(t *testing.T) {
		var got SettlementClaim
		settleRoundTrip(t, settleExampleMember(t, example, "second_claim"), &got)
	})
	t.Run("settlement leaves", func(t *testing.T) {
		var got []SettlementLeaf
		settleRoundTrip(t, settleExampleMember(t, example, "settlement_leaves"), &got)
	})
	t.Run("settlement proof", func(t *testing.T) {
		var got SettlementProof
		settleRoundTrip(t, settleExampleMember(t, example, "settlement_proof"), &got)
	})
	t.Run("second claim witnesses", func(t *testing.T) {
		var got []SettlementWitness
		settleRoundTrip(t, settleExampleMember(t, example, "second_claim_witnesses"), &got)
	})
	t.Run("settlement refusal", func(t *testing.T) {
		var got SettlementRefusal
		settleRoundTrip(t, settleExampleMember(t, example, "settlement_refusal_example"), &got)
	})
	t.Run("key succession", func(t *testing.T) {
		var got KeySuccession
		settleRoundTrip(t, settleExampleMember(t, example, "key_succession"), &got)
	})
}

func TestSettleSchemaShapesRoundTrip(t *testing.T) {
	t.Run("settlement hold", func(t *testing.T) {
		raw := []byte(`{"witness_id":"did:key:z6MkrPuMzSSYYddw1BKtyrH5mMJzQmUX42Lr8eiKpsPVb3C1","provider_id":"did:key:z6MkhFwXNFWosLeugvSf4wcL9t3uuRXueGSFTRgSvHhWj5G2","network":"bitcoin-mainnet","sequence":1,"claim_ids":["fysc2:a3854d69cc661226d0ec03420f6d0e892a2be098f7609e027231592ba199d8ae"],"held_at":"2027-01-06T12:00:00Z","witness_signature":"vcBkyteXp0nmgN4RrB32yyF0aVdePVGHLybOQkiyk0iuZlbhnr4ld5Dv1XD7-nJlnvaipPT670_EkGSXXYGSCg"}`)
		var got SettlementHold
		settleRoundTrip(t, raw, &got)
	})
	t.Run("equivocation proof", func(t *testing.T) {
		raw := []byte(fmt.Sprintf(`{"claims":[%s,%s],"predecessor":null,"successions":[]}`, settleClaimJSON("fysc2:a3854d69cc661226d0ec03420f6d0e892a2be098f7609e027231592ba199d8ae"), settleClaimJSON("fysc2:b3854d69cc661226d0ec03420f6d0e892a2be098f7609e027231592ba199d8ae")))
		var got EquivocationProof
		settleRoundTrip(t, raw, &got)
	})
	t.Run("empty monitor page", func(t *testing.T) {
		raw := []byte(`{"witnesses":[],"refusals":[],"holds":[],"claims":[]}`)
		var got MonitorWitnessesPage
		settleRoundTrip(t, raw, &got)
		if got.Witnesses == nil || got.Refusals == nil || got.Holds == nil || got.Claims == nil {
			t.Fatal("required empty arrays decoded as nil")
		}
	})
	t.Run("nullable quality report counts", func(t *testing.T) {
		raw := []byte(`{"report_id":"fyqr2:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","monitor_id":"did:key:z6MkrPuMzSSYYddw1BKtyrH5mMJzQmUX42Lr8eiKpsPVb3C1","provider_id":"did:key:z6MkhFwXNFWosLeugvSf4wcL9t3uuRXueGSFTRgSvHhWj5G2","period_start":"2027-01-01T00:00:00Z","period_end":"2027-01-02T00:00:00Z","created_at":"2027-01-02T00:00:00Z","methodology_digest":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","observations_digest":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","sample_seed":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA","sample_seed_commitment":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","sample_coverage":"complete","availability_attempts":0,"availability_successes":0,"retrieval_attempts":0,"retrieval_passes":0,"restore_attempts":0,"restore_passes":0,"latency_p50_ms":null,"latency_p95_ms":null,"latency_p99_ms":null,"replica_operators":null,"independent_controllers":null,"sponsors":[],"conflicts":"","monitor_signature":"vcBkyteXp0nmgN4RrB32yyF0aVdePVGHLybOQkiyk0iuZlbhnr4ld5Dv1XD7-nJlnvaipPT670_EkGSXXYGSCg","sample_frame_digest":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"}`)
		var got QualityReport
		settleRoundTrip(t, raw, &got)
	})
	t.Run("nullable observation members", func(t *testing.T) {
		raw := []byte(`{"provider_id":"did:key:z6MkhFwXNFWosLeugvSf4wcL9t3uuRXueGSFTRgSvHhWj5G2","monitor_id":"did:key:z6MkrPuMzSSYYddw1BKtyrH5mMJzQmUX42Lr8eiKpsPVb3C1","period_start":"2027-01-01T00:00:00Z","period_end":"2027-01-02T00:00:00Z","observations":[{"kind":"availability","message_id":null,"started_at":"2027-01-01T00:00:00Z","duration_ms":0,"outcome":"pass","http_status":null,"within_limits":true}]}`)
		var got QualityObservationSet
		settleRoundTrip(t, raw, &got)
	})
	t.Run("empty payout recipients", func(t *testing.T) {
		raw := []byte(`{"declaration_id":"fyrd2:16cadc2610d20c84575d03a409c29beee0acf70fb6e963800814340d3acb17d1","provider_id":"did:key:z6MkhFwXNFWosLeugvSf4wcL9t3uuRXueGSFTRgSvHhWj5G2","retention_seconds":31536000,"payout_recipients":{},"effective_at":"2027-01-01T00:00:00Z","provider_signature":"cXISY1ScrLHaEccQpXRkETP60e9MW7_8rxKDxmL5PuIENnbZ4Ax1YkG5Y0TPn9SJJOqp0dJHZR5_N1aVRArpAA"}`)
		var got RetentionDeclaration
		settleRoundTrip(t, raw, &got)
	})
	t.Run("vault message preservation", func(t *testing.T) {
		example := settleExample(t)
		proof := settleExampleMember(t, example, "settlement_proof")
		raw := []byte(fmt.Sprintf(`{"message":{"type":"post","payload":{"text":"preserve this"}},"proof":%s}`, proof))
		var got VaultDelivery
		settleRoundTrip(t, raw, &got)
		var message map[string]json.RawMessage
		if err := json.Unmarshal(got.Message, &message); err != nil {
			t.Fatalf("decode preserved message: %v", err)
		}
		if _, ok := message["payload"]; !ok {
			t.Fatal("vault message lost its payload member")
		}
	})
}
