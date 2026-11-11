package foundation

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/devnet"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/message"
	"foryou.local/foryou/internal/wire"
)

func TestVaultReadDirectAndSettlementRoutes(t *testing.T) {
	service, records := vaultReadTestService(t)
	handler := service.Handler()

	t.Run("direct GET returns exact bytes and public cache policy", func(t *testing.T) {
		response := vaultReadRequest(handler, http.MethodGet, "/v1/messages/"+records[0].MessageID)
		if response.Code != http.StatusOK || !bytes.Equal(response.Body.Bytes(), records[0].Message) {
			t.Fatalf("direct response = (%d, %q), want exact stored message", response.Code, response.Body.Bytes())
		}
		if got := response.Header().Get("Cache-Control"); got != "public, max-age=3600" {
			t.Errorf("Cache-Control = %q; want public, max-age=3600", got)
		}
	})

	t.Run("HEAD on a stored message returns no body and is not cached", func(t *testing.T) {
		response := vaultReadRequest(handler, http.MethodHead, "/v1/messages/"+records[0].MessageID)
		if response.Code != http.StatusOK || response.Body.Len() != 0 {
			t.Fatalf("HEAD response = (%d, %q), want 200 with no body", response.Code, response.Body.Bytes())
		}
		if got := response.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q; want no-store", got)
		}
	})

	t.Run("unknown direct ID is not found and not cached", func(t *testing.T) {
		response := vaultReadRequest(handler, http.MethodGet, "/v1/messages/fy1:"+fmt.Sprintf("%064x", 9))
		if response.Code != http.StatusNotFound || response.Body.String() != `{"status":"not_found"}` {
			t.Fatalf("unknown direct response = (%d, %s), want 404 not_found", response.Code, response.Body)
		}
		if got := response.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q; want no-store", got)
		}
	})

	t.Run("settlement returns stored proof", func(t *testing.T) {
		response := vaultReadRequest(handler, http.MethodGet, "/v1/settlements/"+records[0].SettlementReceiptID)
		var proof wire.SettlementProof
		if err := json.Unmarshal(response.Body.Bytes(), &proof); err != nil {
			t.Fatalf("decode settlement proof: %v", err)
		}
		if response.Code != http.StatusOK || proof.LeafIndex != 1 || proof.Claim.ClaimID != "fycc2:fixture" || proof.Claim.Network != "bitcoin-mainnet" || proof.Claim.Sequence != 1 || proof.Leaf.ScheduleID != "fyfs2:"+fmt.Sprintf("%064x", 1) || proof.Leaf.AmountBaseUnits != "1" || len(proof.AuditPath) != 0 || len(proof.Witnesses) != 0 {
			t.Fatalf("settlement response = (%d, %#v), want stored fixture proof fields", response.Code, proof)
		}
		if got := response.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q; want no-store", got)
		}
	})

	t.Run("unknown settlement is not found", func(t *testing.T) {
		response := vaultReadRequest(handler, http.MethodGet, "/v1/settlements/missing-receipt")
		if response.Code != http.StatusNotFound || response.Body.String() != `{"status":"not_found"}` {
			t.Fatalf("unknown settlement response = (%d, %s), want 404 not_found", response.Code, response.Body)
		}
		if got := response.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q; want no-store", got)
		}
	})
}

func TestVaultReadListingRoutes(t *testing.T) {
	service, records := vaultReadTestService(t)
	handler := service.Handler()

	t.Run("date filter orders timestamp descending and cursor completes", func(t *testing.T) {
		path := "/v1/messages?date=2026-11-10&limit=1"
		first := vaultReadRequest(handler, http.MethodGet, path)
		if first.Code != http.StatusOK {
			t.Fatalf("first page status = %d, body %s", first.Code, first.Body)
		}
		page := vaultReadDecodePage(t, first)
		if len(page.Messages) != 1 || !bytes.Equal(page.Messages[0], records[0].Message) || page.NextCursor == nil {
			t.Fatalf("first page = (%d messages, cursor %v), want newest day record and cursor", len(page.Messages), page.NextCursor)
		}
		second := vaultReadRequest(handler, http.MethodGet, path+"&cursor="+*page.NextCursor)
		if second.Code != http.StatusOK {
			t.Fatalf("second page status = %d, body %s", second.Code, second.Body)
		}
		page = vaultReadDecodePage(t, second)
		if len(page.Messages) != 1 || !bytes.Equal(page.Messages[0], records[1].Message) || page.NextCursor != nil {
			t.Fatalf("second page = (%d messages, cursor %v), want older day record and null cursor", len(page.Messages), page.NextCursor)
		}
		if got := first.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q; want no-store", got)
		}
	})

	t.Run("date filter excludes other day", func(t *testing.T) {
		response := vaultReadRequest(handler, http.MethodGet, "/v1/messages?date=2026-11-11")
		if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), records[2].Message) || bytes.Contains(response.Body.Bytes(), records[0].Message) {
			t.Fatalf("day listing = (%d, %s), want only Nov 11 record", response.Code, response.Body)
		}
	})

	t.Run("unfiltered route rejects author_id", func(t *testing.T) {
		response := vaultReadRequest(handler, http.MethodGet, "/v1/messages?author_id="+keys.DIDOf(devnet.ProviderKey(0)))
		vaultReadAssertQueryError(t, response, "invalid_query")
	})

	t.Run("author route requires author_id", func(t *testing.T) {
		response := vaultReadRequest(handler, http.MethodGet, "/v1/author-messages")
		vaultReadAssertQueryError(t, response, "invalid_query")
	})

	t.Run("author route returns matching records", func(t *testing.T) {
		author := keys.DIDOf(devnet.ProviderKey(0))
		response := vaultReadRequest(handler, http.MethodGet, "/v1/author-messages?author_id="+url.QueryEscape(author))
		if response.Code != http.StatusOK || !bytes.Contains(response.Body.Bytes(), records[0].Message) || !bytes.Contains(response.Body.Bytes(), records[2].Message) || bytes.Contains(response.Body.Bytes(), records[1].Message) {
			t.Fatalf("author listing = (%d, %s), want exactly author records", response.Code, response.Body)
		}
	})

	t.Run("limit zero is invalid", func(t *testing.T) {
		vaultReadAssertQueryError(t, vaultReadRequest(handler, http.MethodGet, "/v1/messages?limit=0"), "invalid_query")
	})
	t.Run("leading zero limit is invalid", func(t *testing.T) {
		vaultReadAssertQueryError(t, vaultReadRequest(handler, http.MethodGet, "/v1/messages?limit=01"), "invalid_query")
	})
	t.Run("repeated date is invalid", func(t *testing.T) {
		vaultReadAssertQueryError(t, vaultReadRequest(handler, http.MethodGet, "/v1/messages?date=2026-11-10&date=2026-11-11"), "invalid_query")
	})
	t.Run("42 character cursor is invalid cursor", func(t *testing.T) {
		vaultReadAssertQueryError(t, vaultReadRequest(handler, http.MethodGet, "/v1/messages?cursor="+string(bytes.Repeat([]byte{'A'}, 42))), "invalid_cursor")
	})
}

type vaultReadPage struct {
	Messages   []json.RawMessage `json:"messages"`
	NextCursor *string           `json:"next_cursor"`
}

func vaultReadDecodePage(t *testing.T, response *httptest.ResponseRecorder) vaultReadPage {
	t.Helper()
	var page vaultReadPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode discovery page: %v", err)
	}
	return page
}

func TestVaultReadChangesRoute(t *testing.T) {
	service, _ := vaultReadTestService(t)
	handler := service.Handler()

	t.Run("returns acceptance sequence and foundation identity", func(t *testing.T) {
		response := vaultReadRequest(handler, http.MethodGet, "/v1/changes?after=0&limit=2")
		var page struct {
			ProviderID string `json:"provider_id"`
			Items      []struct {
				Sequence int `json:"sequence"`
			} `json:"items"`
			NextAfter int  `json:"next_after"`
			More      bool `json:"more"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		wantID := keys.DIDOf(devnet.FoundationKey())
		if response.Code != http.StatusOK || page.ProviderID != wantID || len(page.Items) != 2 || page.Items[0].Sequence != 1 || page.Items[1].Sequence != 2 || page.NextAfter != 2 || !page.More {
			t.Fatalf("changes page = (%d, %#v), want sequences 1,2 and more", response.Code, page)
		}
		if got := response.Header().Get("Cache-Control"); got != "no-store" {
			t.Errorf("Cache-Control = %q; want no-store", got)
		}
	})

	t.Run("after final sequence is terminal", func(t *testing.T) {
		response := vaultReadRequest(handler, http.MethodGet, "/v1/changes?after=3")
		var page struct {
			Items     []json.RawMessage `json:"items"`
			NextAfter int               `json:"next_after"`
			More      bool              `json:"more"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusOK || len(page.Items) != 0 || page.NextAfter != 3 || page.More {
			t.Fatalf("terminal changes page = (%d, %#v), want empty after 3", response.Code, page)
		}
	})
}

func vaultReadTestService(t *testing.T) (*Service, []VaultRecord) {
	t.Helper()
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	service, err := New(testConfig(t, &now))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = service.Close() })
	posts := []struct {
		key       keys.PrivateKey
		timestamp string
		receiptID byte
	}{
		{devnet.ProviderKey(0), "2026-11-10T09:00:00Z", 1},
		{devnet.ProviderKey(1), "2026-11-10T08:00:00Z", 2},
		{devnet.ProviderKey(0), "2026-11-11T08:00:00Z", 3},
	}
	first := vaultReadStoreFixture(t, service, now, posts[0].key, posts[0].timestamp, posts[0].receiptID)
	second := vaultReadStoreFixture(t, service, now, posts[1].key, posts[1].timestamp, posts[1].receiptID)
	third := vaultReadStoreFixture(t, service, now, posts[2].key, posts[2].timestamp, posts[2].receiptID)
	return service, []VaultRecord{first, second, third}
}

func vaultReadStoreFixture(t *testing.T, service *Service, now time.Time, authorKey keys.PrivateKey, timestamp string, receiptByte byte) VaultRecord {
	t.Helper()
	intent := wire.Intent{
		AuthorID: keys.DIDOf(authorKey), Timestamp: timestamp, MessageType: "post",
		Version: wire.Version{Major: 2, Minor: 0},
		Payload: wire.PostPayload{ContentFormat: "text/plain", Text: "vault read test"},
	}
	messageID, err := message.ID(intent)
	if err != nil {
		t.Fatal(err)
	}
	receiptID := keys.EncodeB64URL(bytes.Repeat([]byte{receiptByte}, 32))
	receipt := wire.SubmissionReceipt{
		ProviderID: keys.DIDOf(devnet.ProviderKey(2)), AuthorID: intent.AuthorID, MessageID: messageID,
		ReceiptID: receiptID, QuoteID: "fyfq2:" + fmt.Sprintf("%064x", receiptByte), ScheduleID: "fyfs2:" + fmt.Sprintf("%064x", receiptByte), Network: "bitcoin-mainnet",
		AmountBaseUnits: "1", AuthorizationAt: "2026-11-11T11:00:00Z", IssuedAt: "2026-11-11T11:00:00Z",
	}
	receiptBytes, err := canon.EncodeWithout(receipt, "provider_signature")
	if err != nil {
		t.Fatal(err)
	}
	receipt.ProviderSignature, err = keys.Sign(devnet.ProviderKey(2), "receipt", receiptBytes)
	if err != nil {
		t.Fatal(err)
	}
	postMessage, err := message.Assemble(intent, receipt)
	if err != nil {
		t.Fatal(err)
	}
	if err := message.Sign(&postMessage, authorKey); err != nil {
		t.Fatal(err)
	}
	if err := message.Verify(postMessage, message.VerifyOptions{Now: now}); err != nil {
		t.Fatalf("verify fixture message: %v", err)
	}
	storedBytes, err := canon.Encode(postMessage)
	if err != nil {
		t.Fatal(err)
	}
	proof := wire.SettlementProof{
		Claim:     wire.SettlementClaim{ClaimID: "fycc2:fixture", Network: "bitcoin-mainnet", Sequence: 1},
		Leaf:      wire.SettlementLeaf{ScheduleID: receipt.ScheduleID, AmountBaseUnits: "1"},
		LeafIndex: int64(receiptByte), AuditPath: []wire.AuditNode{}, Witnesses: []wire.SettlementWitness{},
	}
	record, created, err := service.StoreVaultRecord(storedBytes, proof, now)
	if err != nil || !created {
		t.Fatalf("StoreVaultRecord() = (created %t, error %v)", created, err)
	}
	return record
}

func vaultReadRequest(handler http.Handler, method, target string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, target, nil)
	request.Header.Set("Range", "bytes=0-1")
	request.Header.Set("If-None-Match", `"old"`)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func vaultReadAssertQueryError(t *testing.T, response *httptest.ResponseRecorder, status string) {
	t.Helper()
	if response.Code != http.StatusBadRequest || response.Body.String() != fmt.Sprintf(`{"status":"%s"}`, status) {
		t.Fatalf("query response = (%d, %s), want %d %s", response.Code, response.Body, http.StatusBadRequest, status)
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q; want no-store", got)
	}
}
