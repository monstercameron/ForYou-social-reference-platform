package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"foryou.local/foryou/internal/wire"
)

func rdVersionMessage(t *testing.T, index int) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/rd/post-version-chain.json")
	if err != nil {
		t.Fatal(err)
	}
	var example struct {
		Messages []json.RawMessage `json:"messages"`
	}
	if err := json.Unmarshal(data, &example); err != nil {
		t.Fatal(err)
	}
	return example.Messages[index]
}

func TestGetMessage(t *testing.T) {
	first := rdVersionMessage(t, 0)
	var message struct {
		MessageID string `json:"message_id"`
	}
	if err := json.Unmarshal(first, &message); err != nil {
		t.Fatal(err)
	}
	t.Run("returns exact fixture bytes with escaped ID path", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got, want := r.URL.EscapedPath(), "/v1/messages/fy1%3A4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f"; got != want {
				t.Errorf("path=%q; want %q", got, want)
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write(first)
		}))
		defer server.Close()
		client := rdTestClient(t, server.URL, Options{AllowLoopbackHTTP: true})
		got, err := client.GetMessage(context.Background(), message.MessageID)
		if err != nil {
			t.Fatal(err)
		}
		if string(got.Raw) != string(first) {
			t.Fatal("GetMessage did not preserve the exact response bytes")
		}
		if got.ID != message.MessageID {
			t.Errorf("ID=%q; want %q", got.ID, message.MessageID)
		}
	})
	t.Run("rejects a different message ID", func(t *testing.T) {
		second := rdVersionMessage(t, 1)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write(second)
		}))
		defer server.Close()
		client := rdTestClient(t, server.URL, Options{AllowLoopbackHTTP: true})
		_, err := client.GetMessage(context.Background(), message.MessageID)
		if Classify(err) != SignalProtocol {
			t.Fatalf("Classify(error)=%s; want protocol", Classify(err))
		}
	})
	t.Run("rejects an oversized response", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(strings.Repeat(" ", 1<<20+1)))
		}))
		defer server.Close()
		client := rdTestClient(t, server.URL, Options{AllowLoopbackHTTP: true})
		_, err := client.GetMessage(context.Background(), message.MessageID)
		if Classify(err) != SignalProtocol {
			t.Fatalf("Classify(error)=%s; want protocol", Classify(err))
		}
	})
	t.Run("rejects redirect without contacting destination", func(t *testing.T) {
		contacted := false
		destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { contacted = true }))
		defer destination.Close()
		source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, destination.URL, http.StatusFound)
		}))
		defer source.Close()
		client := rdTestClient(t, source.URL, Options{AllowLoopbackHTTP: true})
		_, err := client.GetMessage(context.Background(), message.MessageID)
		if Classify(err) != SignalProtocol || contacted {
			t.Fatalf("Classify(error)=%s contacted=%t; want protocol and no contact", Classify(err), contacted)
		}
	})
}

func TestListQueryValidation(t *testing.T) {
	t.Run("date and from conflict", func(t *testing.T) {
		_, _, err := MessagesRoute().query(ListQuery{Date: "2026-11-11", From: "2026-11-11T00:00:00Z"}, "")
		if err == nil {
			t.Fatal("listing accepted conflicting filters")
		}
	})
	t.Run("equal bounds rejected", func(t *testing.T) {
		_, _, err := MessagesRoute().query(ListQuery{From: "2026-11-11T00:00:00Z", Until: "2026-11-11T00:00:00Z"}, "")
		if err == nil {
			t.Fatal("listing accepted equal bounds")
		}
	})
	t.Run("limits outside range rejected", func(t *testing.T) {
		_, _, err := MessagesRoute().query(ListQuery{Limit: 101}, "")
		if err == nil {
			t.Fatal("listing accepted limit 101")
		}
	})
	t.Run("unfiltered route cannot carry author", func(t *testing.T) {
		route, err := AuthorRoute(rdTestServiceID)
		if err != nil {
			t.Fatal(err)
		}
		if route.path() != "/v1/author-messages" {
			t.Fatalf("author path=%q", route.path())
		}
	})
}

func rdSettlementFixture(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile("testdata/rd/settlement.example.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture map[string]json.RawMessage
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

func TestFoundationReads(t *testing.T) {
	t.Run("genesis decodes the specification fixture", func(t *testing.T) {
		fixture := rdSettlementFixture(t)
		manifest := fixture["genesis_manifest"]
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(manifest)
		}))
		defer server.Close()
		client := rdTestClient(t, server.URL, Options{AllowLoopbackHTTP: true})
		got, raw, err := client.Genesis(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if got.Release == "" || string(raw) != string(manifest) {
			t.Fatal("Genesis did not decode and preserve fixture")
		}
	})
	t.Run("logged object hash mismatch is rejected", func(t *testing.T) {
		loggedObject := rdSettlementFixture(t)["vault_receipt"]
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(loggedObject)
		}))
		defer server.Close()
		client := rdTestClient(t, server.URL, Options{AllowLoopbackHTTP: true})
		_, _, err := client.LogObject(context.Background(), strings.Repeat("0", 64))
		if Classify(err) != SignalProtocol {
			t.Fatalf("Classify(error)=%s; want protocol", Classify(err))
		}
	})
	t.Run("vault distinguishes new and existing delivery", func(t *testing.T) {
		fixture := rdSettlementFixture(t)
		message := rdVersionMessage(t, 0)
		var proof wire.SettlementProof
		if err := json.Unmarshal(fixture["settlement_proof"], &proof); err != nil {
			t.Fatal(err)
		}
		var transport struct {
			MessageID string `json:"message_id"`
		}
		if err := json.Unmarshal(message, &transport); err != nil {
			t.Fatal(err)
		}
		var receipt wire.VaultReceipt
		if err := json.Unmarshal(fixture["vault_receipt"], &receipt); err != nil {
			t.Fatal(err)
		}
		receipt.MessageID = transport.MessageID
		receiptBody, err := wire.Encode("VaultReceipt", receipt)
		if err != nil {
			t.Fatal(err)
		}
		delivery := wire.VaultDelivery{Message: message, Proof: proof}
		check := func(t *testing.T, status int) {
			t.Helper()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPut || r.URL.EscapedPath() != "/v1/vault/messages/fy1%3A4d6821cffb7771682a92d14b537b5ac813236c45db6ea7cc7a8869fee143354f" {
					t.Errorf("request=%s %s", r.Method, r.URL.EscapedPath())
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				_, _ = w.Write(receiptBody)
			}))
			defer server.Close()
			client := rdTestClient(t, server.URL, Options{AllowLoopbackHTTP: true})
			_, _, gotStatus, err := client.DeliverToVault(context.Background(), delivery)
			if err != nil || gotStatus != status {
				t.Fatalf("DeliverToVault() status=%d error=%v; want %d", gotStatus, err, status)
			}
		}
		t.Run("201 new", func(t *testing.T) { check(t, http.StatusCreated) })
		t.Run("200 already held", func(t *testing.T) { check(t, http.StatusOK) })
		t.Run("409 not ready", func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusConflict)
				_, _ = w.Write([]byte(`{"status":"not_ready"}`))
			}))
			defer server.Close()
			client := rdTestClient(t, server.URL, Options{AllowLoopbackHTTP: true})
			_, _, gotStatus, err := client.DeliverToVault(context.Background(), delivery)
			var publicError *Error
			if !errors.As(err, &publicError) || gotStatus != http.StatusConflict || publicError.Status != "not_ready" {
				t.Fatalf("DeliverToVault() status=%d error=%v; want 409 not_ready", gotStatus, err)
			}
		})
	})
}

func TestMonitorQueryEncoding(t *testing.T) {
	t.Run("witness query percent-encodes DID colon", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !strings.Contains(r.URL.RawQuery, "provider_id=did%3Akey%3A") || !strings.Contains(r.URL.RawQuery, "network=bitcoin-mainnet") || !strings.Contains(r.URL.RawQuery, "after_sequence=7") {
				t.Errorf("raw query=%q", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"witnesses":[],"refusals":[],"holds":[],"claims":[]}`))
		}))
		defer server.Close()
		client := rdTestClient(t, server.URL, Options{AllowLoopbackHTTP: true})
		if _, _, err := client.Witnesses(context.Background(), rdTestServiceID, "bitcoin-mainnet", 7); err != nil {
			t.Fatal(err)
		}
	})
}

func TestQualityReportQuarterQuery(t *testing.T) {
	t.Run("requires canonical UTC time", func(t *testing.T) {
		contacted := false
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { contacted = true }))
		defer server.Close()
		client := rdTestClient(t, server.URL, Options{AllowLoopbackHTTP: true})
		_, _, err := client.QualityReports(context.Background(), rdTestServiceID, "2026-09-30")
		if Classify(err) != SignalInvalidQuery || contacted {
			t.Fatalf("Classify(error)=%s contacted=%t; want invalid_query and no request", Classify(err), contacted)
		}
	})
	t.Run("accepts canonical UTC quarter end", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get("quarter_end"); got != "2026-09-30T23:59:59Z" {
				t.Errorf("quarter_end=%q; want canonical UTC time", got)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"reports":[]}`))
		}))
		defer server.Close()
		client := rdTestClient(t, server.URL, Options{AllowLoopbackHTTP: true})
		if _, _, err := client.QualityReports(context.Background(), rdTestServiceID, "2026-09-30T23:59:59Z"); err != nil {
			t.Fatal(err)
		}
	})
}

func TestChanges(t *testing.T) {
	t.Run("accepts coverage beyond the last visible item", func(t *testing.T) {
		message := rdVersionMessage(t, 0)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"items":[{"message":` + string(message) + `,"sequence":5}],"more":false,"next_after":7,"provider_id":"` + rdTestServiceID + `"}`))
		}))
		defer server.Close()
		client := rdTestClient(t, server.URL, Options{AllowLoopbackHTTP: true})
		result, err := client.Changes(context.Background(), 0, 10)
		if err != nil || result.Page.NextAfter != 7 {
			t.Fatalf("Changes() next_after=%d error=%v; want accepted coverage 7", result.Page.NextAfter, err)
		}
	})
	t.Run("empty tail after high sequence succeeds", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("after") != "11" || r.URL.Query().Get("limit") != "100" {
				t.Errorf("query=%q", r.URL.RawQuery)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"items":[],"more":false,"next_after":11,"provider_id":"` + rdTestServiceID + `"}`))
		}))
		defer server.Close()
		client := rdTestClient(t, server.URL, Options{AllowLoopbackHTTP: true})
		result, err := client.Changes(context.Background(), 11, 100)
		if err != nil || result.Page.NextAfter != 11 || result.Page.More || len(result.Raw) == 0 {
			t.Fatalf("Changes()=(%+v,%v); want covered empty tail", result, err)
		}
	})
	t.Run("rejects a different provider identity", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"items":[],"more":false,"next_after":0,"provider_id":"did:key:z6MkhFwXNFWosLeugvSf4wcL9t3uuRXueGSFTRgSvHhWj5G2"}`))
		}))
		defer server.Close()
		client := rdTestClient(t, server.URL, Options{AllowLoopbackHTTP: true})
		_, err := client.Changes(context.Background(), 0, 1)
		if Classify(err) != SignalProtocol {
			t.Fatalf("Classify(error)=%s; want protocol", Classify(err))
		}
	})
	t.Run("rejects non increasing sequences", func(t *testing.T) {
		message := rdVersionMessage(t, 0)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"items":[{"message":` + string(message) + `,"sequence":5},{"message":` + string(message) + `,"sequence":5}],"more":false,"next_after":5,"provider_id":"` + rdTestServiceID + `"}`))
		}))
		defer server.Close()
		client := rdTestClient(t, server.URL, Options{AllowLoopbackHTTP: true})
		_, err := client.Changes(context.Background(), 0, 10)
		if Classify(err) != SignalProtocol {
			t.Fatalf("Classify(error)=%s; want protocol", Classify(err))
		}
	})
}
