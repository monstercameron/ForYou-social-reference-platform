package provider

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"foryou.local/foryou/internal/delegation"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/funding"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

func TestFundingRoutes(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	cfg, err := TestConfig(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	author := fdTestAuthor(t)
	authorID := keys.DIDOf(author)
	requestID := keys.EncodeB64URL(bytes.Repeat([]byte{7}, 32))
	request := wire.DepositRequest{ProviderID: s.ProviderID(), AuthorID: authorID, Network: wire.BitcoinMainnet, AmountChainUnits: "50000", RequestID: requestID, NotAfter: wire.FormatTime(now.Add(time.Minute))}
	body := fdTestJSON(t, request)
	response := fdTestServe(t, s, author, http.MethodPost, "/v1/deposits", body, true, "192.0.2.10:1234")
	if response.Code != http.StatusCreated {
		t.Fatalf("POST /v1/deposits status = %d, body %s", response.Code, response.Body)
	}
	var invoice wire.DepositInvoice
	if err := json.Unmarshal(response.Body.Bytes(), &invoice); err != nil {
		t.Fatal(err)
	}
	if err := funding.CheckInvoice(invoice, request, s.ProviderID(), now); err != nil {
		t.Fatalf("funding.CheckInvoice() error = %v", err)
	}
	t.Run("identical invoice retry returns original", func(t *testing.T) {
		got := fdTestServe(t, s, author, http.MethodPost, "/v1/deposits", body, true, "192.0.2.10:1234")
		if got.Code != http.StatusOK || !bytes.Equal(got.Body.Bytes(), response.Body.Bytes()) {
			t.Fatalf("retry = %d %s; want original invoice", got.Code, got.Body)
		}
	})
	t.Run("changed retained request conflicts", func(t *testing.T) {
		changed := request
		changed.AmountChainUnits = "50001"
		got := fdTestServe(t, s, author, http.MethodPost, "/v1/deposits", fdTestJSON(t, changed), true, "192.0.2.10:1234")
		fdTestError(t, got, http.StatusConflict, "request_conflict")
	})
	t.Run("new request deadline over one minute is invalid", func(t *testing.T) {
		late := request
		late.RequestID = keys.EncodeB64URL(bytes.Repeat([]byte{8}, 32))
		late.NotAfter = wire.FormatTime(now.Add(61 * time.Second))
		got := fdTestServe(t, s, author, http.MethodPost, "/v1/deposits", fdTestJSON(t, late), true, "192.0.2.10:1234")
		fdTestError(t, got, http.StatusBadRequest, "invalid_query")
	})
	t.Run("custody cap rejects new invoice", func(t *testing.T) {
		tooLarge := request
		tooLarge.RequestID = keys.EncodeB64URL(bytes.Repeat([]byte{9}, 32))
		tooLarge.AmountChainUnits = "136001"
		got := fdTestServe(t, s, author, http.MethodPost, "/v1/deposits", fdTestJSON(t, tooLarge), true, "192.0.2.10:1234")
		fdTestError(t, got, http.StatusConflict, "request_conflict")
	})
	t.Run("deposit per-source limit returns retry hint", func(t *testing.T) {
		first := fdTestServe(t, s, author, http.MethodPost, "/v1/deposits", []byte(`{}`), false, "198.51.100.9:1234")
		if first.Code != http.StatusUnauthorized {
			t.Fatalf("unauthenticated limited request status = %d", first.Code)
		}
		fdTestServe(t, s, author, http.MethodPost, "/v1/deposits", []byte(`{}`), false, "198.51.100.9:1234")
		fdTestServe(t, s, author, http.MethodPost, "/v1/deposits", []byte(`{}`), false, "198.51.100.9:1234")
		fdTestServe(t, s, author, http.MethodPost, "/v1/deposits", []byte(`{}`), false, "198.51.100.9:1234")
		fdTestServe(t, s, author, http.MethodPost, "/v1/deposits", []byte(`{}`), false, "198.51.100.9:1234")
		fdTestServe(t, s, author, http.MethodPost, "/v1/deposits", []byte(`{}`), false, "198.51.100.9:1234")
		limited := fdTestServe(t, s, author, http.MethodPost, "/v1/deposits", []byte(`{}`), false, "198.51.100.9:1234")
		var publicError map[string]any
		if limited.Code != http.StatusTooManyRequests || json.Unmarshal(limited.Body.Bytes(), &publicError) != nil || publicError["status"] != "rate_limited" || publicError["retry_after_seconds"] == nil {
			t.Fatalf("rate limited response = %d %s", limited.Code, limited.Body)
		}
	})
	t.Run("observation stays pending then commits one signed credit", func(t *testing.T) {
		chain := s.Chain.(*faux.Ledger)
		transfer, err := chain.Submit(context.Background(), faux.SubmitRequest{Network: request.Network, Recipient: invoice.Recipient, AmountChainUnits: request.AmountChainUnits, Pending: true})
		if err != nil {
			t.Fatal(err)
		}
		proof := wire.DepositProof{ProviderID: s.ProviderID(), AuthorID: authorID, DepositID: invoice.DepositID, Transfer: wire.DepositTransfer{Network: transfer.Network, TxID: transfer.TxID, OutputIndex: fdTestOutputIndex(transfer.OutputIndex)}}
		pending := fdTestServe(t, s, author, http.MethodPost, "/v1/deposits/observe", fdTestJSON(t, proof), true, "192.0.2.10:1234")
		if pending.Code != http.StatusAccepted || pending.Body.String() != `{"status":"pending"}` {
			t.Fatalf("pending observation = %d %s", pending.Code, pending.Body)
		}
		got, balanceErr := s.Ledger.Balance(authorID, request.Network)
		if balanceErr != nil || got.Available.Cmp(big.NewInt(0)) != 0 {
			t.Fatalf("balance before confirmation = %#v, %v", got, balanceErr)
		}
		if _, err := chain.Mine(context.Background(), request.Network, 5); err != nil {
			t.Fatal(err)
		}
		credited := fdTestServe(t, s, author, http.MethodPost, "/v1/deposits/observe", fdTestJSON(t, proof), true, "192.0.2.10:1234")
		var credit wire.DepositCredit
		if credited.Code != http.StatusCreated || json.Unmarshal(credited.Body.Bytes(), &credit) != nil || credit.AmountAccountingUnits != "50000000" {
			t.Fatalf("credit response = %d %s", credited.Code, credited.Body)
		}
		if err := funding.VerifyCredit(credit, s.ProviderID()); err != nil {
			t.Fatalf("funding.VerifyCredit() error = %v", err)
		}
		retry := fdTestServe(t, s, author, http.MethodPost, "/v1/deposits/observe", fdTestJSON(t, proof), true, "192.0.2.10:1234")
		if retry.Code != http.StatusOK || retry.Body.String() != credited.Body.String() {
			t.Fatalf("credit retry = %d %s", retry.Code, retry.Body)
		}
		underpaidRequest := request
		underpaidRequest.RequestID = keys.EncodeB64URL(bytes.Repeat([]byte{13}, 32))
		underpaidRequest.AmountChainUnits = "2"
		underpaidInvoiceResponse := fdTestServe(t, s, author, http.MethodPost, "/v1/deposits", fdTestJSON(t, underpaidRequest), true, "192.0.2.10:1234")
		var underpaidInvoice wire.DepositInvoice
		if underpaidInvoiceResponse.Code != http.StatusCreated || json.Unmarshal(underpaidInvoiceResponse.Body.Bytes(), &underpaidInvoice) != nil {
			t.Fatalf("underpayment invoice = %d %s", underpaidInvoiceResponse.Code, underpaidInvoiceResponse.Body)
		}
		underpaidTransfer, err := chain.Submit(context.Background(), faux.SubmitRequest{Network: request.Network, Recipient: underpaidInvoice.Recipient, AmountChainUnits: "1"})
		if err != nil {
			t.Fatal(err)
		}
		underpaidProof := wire.DepositProof{ProviderID: s.ProviderID(), AuthorID: authorID, DepositID: underpaidInvoice.DepositID, Transfer: wire.DepositTransfer{Network: underpaidTransfer.Network, TxID: underpaidTransfer.TxID, OutputIndex: fdTestOutputIndex(underpaidTransfer.OutputIndex)}}
		claimed := fdTestServe(t, s, author, http.MethodPost, "/v1/deposits/observe", fdTestJSON(t, underpaidProof), true, "192.0.2.10:1234")
		fdTestError(t, claimed, http.StatusConflict, "request_conflict")
	})
	t.Run("balance is signed and unsupported network is absent", func(t *testing.T) {
		got := fdTestServe(t, s, author, http.MethodGet, "/v1/balances/bitcoin-mainnet", nil, true, "192.0.2.10:1234")
		var balance wire.BalanceStatus
		if got.Code != http.StatusOK || json.Unmarshal(got.Body.Bytes(), &balance) != nil || balance.Available != "50000000" || funding.VerifyBalance(balance, s.ProviderID()) != nil {
			t.Fatalf("balance response = %d %s", got.Code, got.Body)
		}
		unknown := fdTestServe(t, s, author, http.MethodGet, "/v1/balances/litecoin", nil, true, "192.0.2.10:1234")
		fdTestError(t, unknown, http.StatusNotFound, "not_found")
	})
	t.Run("withdrawal sends once and removes reserved balance", func(t *testing.T) {
		withdrawalRequest := wire.WithdrawalRequest{ProviderID: s.ProviderID(), AuthorID: authorID, Network: wire.BitcoinMainnet, AmountBaseUnits: "1000000", Recipient: faux.DevAddress(wire.BitcoinMainnet, "author-wallet"), RequestID: keys.EncodeB64URL(bytes.Repeat([]byte{10}, 32))}
		body := fdTestJSON(t, withdrawalRequest)
		response := fdTestServe(t, s, author, http.MethodPost, "/v1/withdrawals", body, true, "192.0.2.10:1234")
		var status wire.WithdrawalStatus
		if response.Code != http.StatusAccepted || json.Unmarshal(response.Body.Bytes(), &status) != nil || status.Status != funding.WithdrawalSent || status.Transfer == nil {
			t.Fatalf("withdrawal response = %d %s", response.Code, response.Body)
		}
		if err := funding.VerifyWithdrawal(status, s.ProviderID()); err != nil {
			t.Fatal(err)
		}
		retry := fdTestServe(t, s, author, http.MethodPost, "/v1/withdrawals", body, true, "192.0.2.10:1234")
		if retry.Code != http.StatusOK || retry.Body.String() != response.Body.String() {
			t.Fatalf("withdrawal retry = %d %s", retry.Code, retry.Body)
		}
		chain := s.Chain.(*faux.Ledger)
		transfers, err := chain.Transfers(context.Background(), faux.Query{Network: wire.BitcoinMainnet, Recipient: withdrawalRequest.Recipient, Limit: 10})
		if err != nil || len(transfers) != 1 {
			t.Fatalf("withdrawal transfers = %d, %v; want exactly one", len(transfers), err)
		}
		if transfers[0].TxID != status.Transfer.TxID || transfers[0].OutputIndex == nil || status.Transfer.OutputIndex == nil || int64(*transfers[0].OutputIndex) != *status.Transfer.OutputIndex {
			t.Fatalf("faux transfer = %#v; want status transfer %#v", transfers[0], status.Transfer)
		}
		if transfers[0].From != faux.DevAddress(wire.BitcoinMainnet, "provider/0/withdrawal") || transfers[0].Recipient != withdrawalRequest.Recipient || transfers[0].AmountChainUnits != "1000" {
			t.Fatalf("faux withdrawal transfer = %#v; want exact funded payout", transfers[0])
		}
		got := fdTestServe(t, s, author, http.MethodGet, "/v1/balances/bitcoin-mainnet", nil, true, "192.0.2.10:1234")
		var balance wire.BalanceStatus
		if got.Code != http.StatusOK || json.Unmarshal(got.Body.Bytes(), &balance) != nil || balance.Available != "49000000" || balance.Reserved != "0" {
			t.Fatalf("post-withdrawal balance = %d %s", got.Code, got.Body)
		}
		backgroundRequest := wire.WithdrawalRequest{ProviderID: s.ProviderID(), AuthorID: authorID, Network: wire.BitcoinMainnet, AmountBaseUnits: "2000000", Recipient: faux.DevAddress(wire.BitcoinMainnet, "author-wallet-background"), RequestID: keys.EncodeB64URL(bytes.Repeat([]byte{14}, 32))}
		pending, created, err := s.Ledger.RequestWithdrawal(backgroundRequest)
		if err != nil || !created || pending.Status != funding.WithdrawalPending {
			t.Fatalf("direct withdrawal = %#v, created %v, error %v", pending, created, err)
		}
		if err := s.RunOnce(context.Background()); err != nil {
			t.Fatal(err)
		}
		sent, exists := s.Ledger.Withdrawal(authorID, backgroundRequest.RequestID)
		if !exists || sent.Status != funding.WithdrawalSent || sent.Transfer == nil {
			t.Fatalf("background withdrawal = %#v, exists %v; want sent with transfer", sent, exists)
		}
		backgroundTransfer, err := chain.Transfer(context.Background(), wire.BitcoinMainnet, sent.Transfer.TxID)
		if err != nil || backgroundTransfer.Recipient != backgroundRequest.Recipient || backgroundTransfer.AmountChainUnits != "2000" {
			t.Fatalf("background faux transfer = %#v, error %v", backgroundTransfer, err)
		}
	})
	t.Run("withdrawal above available balance requires payment", func(t *testing.T) {
		tooMuch := wire.WithdrawalRequest{ProviderID: s.ProviderID(), AuthorID: authorID, Network: wire.BitcoinMainnet, AmountBaseUnits: "999999999000", Recipient: faux.DevAddress(wire.BitcoinMainnet, "author-wallet"), RequestID: keys.EncodeB64URL(bytes.Repeat([]byte{11}, 32))}
		got := fdTestServe(t, s, author, http.MethodPost, "/v1/withdrawals", fdTestJSON(t, tooMuch), true, "192.0.2.10:1234")
		fdTestError(t, got, http.StatusPaymentRequired, "insufficient_balance")
	})
}

func TestFundingRoutesRequireServiceAuth(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	cfg, err := TestConfig(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	badPost := func(path string) {
		r := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(`{}`)))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		fdTestError(t, w, http.StatusUnauthorized, "unauthenticated")
	}
	t.Run("deposit", func(t *testing.T) { badPost("/v1/deposits") })
	t.Run("deposit observation", func(t *testing.T) { badPost("/v1/deposits/observe") })
	t.Run("balance", func(t *testing.T) {
		r := httptest.NewRequest(http.MethodGet, "/v1/balances/bitcoin-mainnet", nil)
		w := httptest.NewRecorder()
		s.Handler().ServeHTTP(w, r)
		fdTestError(t, w, http.StatusUnauthorized, "unauthenticated")
	})
	t.Run("withdrawal", func(t *testing.T) { badPost("/v1/withdrawals") })
}

func TestDepositInvoiceOpenLimit(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	cfg, err := TestConfig(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	author := fdTestAuthor(t)
	newRequest := func(n byte) wire.DepositRequest {
		return wire.DepositRequest{ProviderID: s.ProviderID(), AuthorID: keys.DIDOf(author), Network: wire.BitcoinMainnet, AmountChainUnits: "1", RequestID: keys.EncodeB64URL(bytes.Repeat([]byte{n}, 32)), NotAfter: wire.FormatTime(now.Add(time.Minute))}
	}
	first := fdTestServe(t, s, author, http.MethodPost, "/v1/deposits", fdTestJSON(t, newRequest(1)), true, "203.0.113.8:1234")
	second := fdTestServe(t, s, author, http.MethodPost, "/v1/deposits", fdTestJSON(t, newRequest(2)), true, "203.0.113.8:1234")
	third := fdTestServe(t, s, author, http.MethodPost, "/v1/deposits", fdTestJSON(t, newRequest(3)), true, "203.0.113.8:1234")
	fourth := fdTestServe(t, s, author, http.MethodPost, "/v1/deposits", fdTestJSON(t, newRequest(4)), true, "203.0.113.8:1234")
	if first.Code != http.StatusCreated || second.Code != http.StatusCreated || third.Code != http.StatusCreated || fourth.Code != http.StatusCreated {
		t.Fatalf("first four invoice statuses = %d, %d, %d, %d", first.Code, second.Code, third.Code, fourth.Code)
	}
	fifth := fdTestServe(t, s, author, http.MethodPost, "/v1/deposits", fdTestJSON(t, newRequest(5)), true, "203.0.113.8:1234")
	fdTestError(t, fifth, http.StatusTooManyRequests, "rate_limited")
	var result map[string]any
	if json.Unmarshal(fifth.Body.Bytes(), &result) != nil || result["retry_after_seconds"] == nil {
		t.Fatalf("invoice limit response lacks retry_after_seconds: %s", fifth.Body)
	}
}

func TestFundingRequestAuthorBinding(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	cfg, err := TestConfig(func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	author := fdTestAuthor(t)
	other := keys.PrivateKey(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{43}, ed25519.SeedSize)))
	req := wire.DepositRequest{ProviderID: s.ProviderID(), AuthorID: keys.DIDOf(other), Network: wire.BitcoinMainnet, AmountChainUnits: "1", RequestID: keys.EncodeB64URL(bytes.Repeat([]byte{12}, 32)), NotAfter: wire.FormatTime(now.Add(time.Minute))}
	response := fdTestServe(t, s, author, http.MethodPost, "/v1/deposits", fdTestJSON(t, req), true, "192.0.2.30:1234")
	fdTestError(t, response, http.StatusUnauthorized, "unauthenticated")
}

func fdTestAuthor(t *testing.T) keys.PrivateKey {
	t.Helper()
	seed := bytes.Repeat([]byte{42}, ed25519.SeedSize)
	return keys.PrivateKey(ed25519.NewKeyFromSeed(seed))
}

func fdTestJSON(t *testing.T, value any) []byte {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func fdTestServe(t *testing.T, s *Service, author keys.PrivateKey, method, path string, body []byte, authenticate bool, remote string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, path, bytes.NewReader(body))
	r.RemoteAddr = remote
	if method == http.MethodPost {
		r.Header.Set("Content-Type", "application/json")
	}
	if authenticate {
		kind := delegation.BodyJSON
		if method == http.MethodGet {
			kind = delegation.BodyNone
		}
		bodyHash, err := delegation.BodyHash(kind, body)
		if err != nil {
			t.Fatal(err)
		}
		challenge, err := s.ServiceAuth.Issue(remote, delegation.ChallengeRequest{AuthorID: keys.DIDOf(author), Method: method, Resource: r.URL.EscapedPath(), BodyHash: bodyHash})
		if err != nil {
			t.Fatal(err)
		}
		header, signature, err := delegation.SignChallenge(author, challenge)
		if err != nil {
			t.Fatal(err)
		}
		r.Header.Set(delegation.HeaderChallenge, header)
		r.Header.Set(delegation.HeaderSignature, signature)
	}
	w := httptest.NewRecorder()
	s.Handler().ServeHTTP(w, r)
	return w
}

func fdTestError(t *testing.T, response *httptest.ResponseRecorder, status int, name string) {
	t.Helper()
	var body map[string]any
	if response.Code != status || json.Unmarshal(response.Body.Bytes(), &body) != nil || body["status"] != name {
		t.Fatalf("response = %d %s; want %d %q", response.Code, response.Body, status, name)
	}
}

func fdTestOutputIndex(value *uint32) *int64 {
	if value == nil {
		return nil
	}
	index := int64(*value)
	return &index
}
