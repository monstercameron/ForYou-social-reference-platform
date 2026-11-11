package faux

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHTTPClient(t *testing.T) {
	l, err := Open(Config{Now: func() time.Time { return time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC) }})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(NewHandler(l))
	defer server.Close()
	client := NewClient(server.URL, server.Client())
	ctx := context.Background()
	req := SubmitRequest{Network: BitcoinMainnet, Recipient: DevAddress(BitcoinMainnet, "alice"), AmountChainUnits: "500", Data: "001122", ClientRef: "http-1"}
	t.Run("submit_and_replay", func(t *testing.T) {
		resp, err := server.Client().Post(server.URL+"/faux/v1/transfers", "application/json", strings.NewReader(`{"network":"bitcoin-mainnet","recipient":"`+req.Recipient+`","amount_chain_units":"500","data":"001122","client_ref":"http-1"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != 201 || resp.Header.Get("ForYou-Faux") != "dev-only" {
			t.Fatalf("response = %d, header %q", resp.StatusCode, resp.Header.Get("ForYou-Faux"))
		}
		got, err := client.Submit(ctx, req)
		if err != nil || got.Sequence != 1 {
			t.Fatalf("replay = %+v, %v", got, err)
		}
		response, err := server.Client().Post(server.URL+"/faux/v1/transfers", "application/json", strings.NewReader(`{"network":"bitcoin-mainnet","recipient":"`+req.Recipient+`","amount_chain_units":"500","data":"001122","client_ref":"http-1"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatalf("replay status = %d", response.StatusCode)
		}
	})
	t.Run("get_missing", func(t *testing.T) {
		if _, err := client.Transfer(ctx, BitcoinMainnet, strings.Repeat("0", 64)); !errors.Is(err, ErrNotFound) {
			t.Fatalf("error = %v; want ErrNotFound", err)
		}
	})
	t.Run("conflicting_replay", func(t *testing.T) {
		changed := req
		changed.AmountChainUnits = "501"
		if _, err := client.Submit(ctx, changed); !errors.Is(err, ErrConflict) {
			t.Fatalf("client error = %v; want ErrConflict", err)
		}
	})
	t.Run("recipient_filter_pages_with_next_cursor", func(t *testing.T) {
		second := req
		second.ClientRef = "http-2"
		second.AmountChainUnits = "502"
		if _, err := client.Submit(ctx, second); err != nil {
			t.Fatal(err)
		}
		getPage := func(after string) (int, []Transfer, *uint64) {
			t.Helper()
			url := server.URL + "/faux/v1/transfers?network=" + BitcoinMainnet + "&recipient=" + req.Recipient + "&limit=1"
			if after != "" {
				url += "&after=" + after
			}
			response, err := server.Client().Get(url)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			var body struct {
				Transfers []Transfer `json:"transfers"`
				NextAfter *uint64    `json:"next_after"`
			}
			if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			return response.StatusCode, body.Transfers, body.NextAfter
		}
		status, first, cursor := getPage("")
		if status != http.StatusOK || len(first) != 1 || cursor == nil || first[0].Recipient != req.Recipient {
			t.Fatalf("first page = %d, %+v, %v", status, first, cursor)
		}
		status, secondPage, cursor := getPage(strconv.FormatUint(*cursor, 10))
		if status != http.StatusOK || len(secondPage) != 1 || cursor != nil || secondPage[0].Sequence <= first[0].Sequence {
			t.Fatalf("second page = %d, %+v, %v", status, secondPage, cursor)
		}
	})
	t.Run("invalid_limit_is_bad_request", func(t *testing.T) {
		response, err := server.Client().Get(server.URL + "/faux/v1/transfers?network=" + BitcoinMainnet + "&limit=-1")
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusBadRequest {
			t.Fatalf("status = %d", response.StatusCode)
		}
	})
	t.Run("unknown_route_has_json_error", func(t *testing.T) {
		response, err := server.Client().Get(server.URL + "/unknown")
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var body map[string]string
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusNotFound || body["error"] != "not_found" || response.Header.Get("ForYou-Faux") != "dev-only" {
			t.Fatalf("response = %d, %v, %q", response.StatusCode, body, response.Header.Get("ForYou-Faux"))
		}
	})
	t.Run("large_request", func(t *testing.T) {
		response, err := server.Client().Post(server.URL+"/faux/v1/transfers", "application/json", strings.NewReader(strings.Repeat(" ", 4097)))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var body map[string]string
		if err := json.NewDecoder(response.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != 413 || body["error"] != "too_large" {
			t.Fatalf("response = %d, %v", response.StatusCode, body)
		}
	})
	t.Run("unknown_fields", func(t *testing.T) {
		response, err := server.Client().Post(server.URL+"/faux/v1/transfers", "application/json", strings.NewReader(`{"network":"bitcoin-mainnet","extra":1}`))
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != 400 {
			t.Fatalf("status = %d", response.StatusCode)
		}
	})
}
