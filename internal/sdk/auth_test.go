package sdk

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/delegation"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/funding"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

var acNow = time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)

// acProviderKey uses the specification's provider seed bytes(range(32,64)).
func acProviderKey() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed([]byte{32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63})
}

// acAuthorKey uses the fixed author root seed bytes(range(0,32)).
func acAuthorKey() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed([]byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31})
}

// acOtherKey uses the fixed unrelated seed bytes(range(160,192)).
func acOtherKey() ed25519.PrivateKey {
	return ed25519.NewKeyFromSeed([]byte{160, 161, 162, 163, 164, 165, 166, 167, 168, 169, 170, 171, 172, 173, 174, 175, 176, 177, 178, 179, 180, 181, 182, 183, 184, 185, 186, 187, 188, 189, 190, 191})
}

// acProvider is an httptest provider answering as the API catalog says; it issues and verifies service-auth with internal/delegation.
type acProvider struct {
	t         *testing.T
	key       ed25519.PrivateKey
	auth      *delegation.ServiceAuth
	authority *delegation.Authority
	client    *Client

	mu         sync.Mutex
	challenges int
	calls      map[string]int

	mutateChallenge func(*wire.ServiceChallenge)
	mutateInvoice   func(*wire.DepositInvoice)
	invoiceSigner   ed25519.PrivateKey
	mutateCredit    func(*wire.DepositCredit)
	pendingObserves int
	observeGate     chan struct{}
	balanceSigner   ed25519.PrivateKey
	insufficient    string
	withdrawals     map[string]bool
	receiptMessage  string
}

// acNewProvider configures the fake with setup, when not nil, before its server starts.
func acNewProvider(t *testing.T, setup func(*acProvider)) *acProvider {
	t.Helper()
	p := &acProvider{t: t, key: acProviderKey(), calls: map[string]int{}, withdrawals: map[string]bool{}}
	p.invoiceSigner, p.balanceSigner = p.key, p.key
	if setup != nil {
		setup(p)
	}
	clock := func() time.Time { return acNow }
	auth, err := delegation.NewServiceAuth(delegation.ServiceAuthConfig{ServiceID: keys.DIDOf(p.key), Store: delegation.NewMemoryChallengeStore(), Now: clock, Limits: delegation.Limits{MaxOutstanding: 64, MaxPerSource: 64}})
	if err != nil {
		t.Fatal(err)
	}
	authority, err := delegation.NewAuthority(delegation.AuthorityConfig{ProviderKey: p.key, Store: delegation.NewMemoryEpochStore(), Now: clock, MaxRevocationsPerAuthor: 8})
	if err != nil {
		t.Fatal(err)
	}
	p.auth, p.authority = auth, authority
	server := httptest.NewServer(http.HandlerFunc(p.serve))
	t.Cleanup(server.Close)
	client, err := New(server.URL, Options{ServiceID: keys.DIDOf(p.key), AllowLoopbackHTTP: true, Now: clock})
	if err != nil {
		t.Fatal(err)
	}
	p.client = client
	return p
}

func (p *acProvider) count(route string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls[route]
}

func (p *acProvider) issued() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.challenges
}

func (p *acProvider) record(route string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls[route]++
}

func (p *acProvider) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		apierr.Write(w, apierr.PublicReadError{Status: apierr.InvalidQuery})
		return
	}
	route := r.Method + " " + r.URL.EscapedPath()
	switch route {
	case "POST /v1/service-challenges":
		p.issueChallenge(w, body)
		return
	case "POST /v1/successions":
		p.record(route)
		var s wire.KeySuccession
		p.decode(body, "KeySuccession", &s)
		acWriteJSON(w, http.StatusCreated, s)
		return
	}
	kind := delegation.BodyJSON
	if r.Method == http.MethodGet {
		kind = delegation.BodyNone
	}
	author, err := p.auth.Verify(delegation.NewAuthRequest(r.Method, r.URL.EscapedPath(), r.URL.RawQuery, r.Header.Get, kind, body))
	if err != nil {
		apierr.Write(w, apierr.PublicReadError{Status: apierr.Unauthenticated})
		return
	}
	p.record(route)
	switch {
	case route == "POST /v1/deposits":
		p.deposit(w, author, body)
	case route == "POST /v1/deposits/observe":
		p.observe(w, author, body)
	case strings.HasPrefix(route, "GET /v1/balances/"):
		p.balance(w, author, strings.TrimPrefix(route, "GET /v1/balances/"))
	case route == "POST /v1/withdrawals":
		p.withdraw(w, author, body)
	case strings.HasPrefix(route, "GET /v1/admissions/"):
		messageID, err := url.PathUnescape(strings.TrimPrefix(route, "GET /v1/admissions/"))
		if err != nil {
			apierr.Write(w, apierr.PublicReadError{Status: apierr.InvalidQuery})
			return
		}
		p.admission(w, author, messageID)
	case route == "POST /v1/authorization-epochs":
		var req wire.AuthorizationEpochEnroll
		p.decode(body, "AuthorizationEpochEnroll", &req)
		state, created, err := p.authority.Enroll(author, req)
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		p.authorityResult(w, status, state, err)
	case route == "POST /v1/authorization-epochs/control":
		var req wire.AuthorizationEpochControl
		p.decode(body, "AuthorizationEpochControl", &req)
		state, err := p.authority.Control(author, req)
		p.authorityResult(w, http.StatusOK, state, err)
	case route == "POST /v1/authorization-epochs/state":
		var req struct {
			AuthorID  string `json:"author_id"`
			Challenge string `json:"challenge"`
		}
		if json.Unmarshal(body, &req) != nil || req.AuthorID != author {
			apierr.Write(w, apierr.PublicReadError{Status: apierr.Unauthenticated})
			return
		}
		response, err := p.authority.State(author, req.Challenge)
		p.authorityResult(w, http.StatusOK, response, err)
	case route == "POST /v1/revocations":
		var req wire.RevocationRequest
		p.decode(body, "RevocationRequest", &req)
		id, err := p.authority.Revoke(author, req)
		p.authorityResult(w, http.StatusOK, map[string]string{"delegation_id": id, "status": "revoked"}, err)
	default:
		apierr.Write(w, apierr.PublicReadError{Status: apierr.NotFound})
	}
}

func (p *acProvider) decode(body []byte, definition string, out any) {
	if err := wire.Decode(body, definition, out); err != nil {
		p.t.Errorf("fake provider received an invalid %s: %v", definition, err)
	}
}

func (p *acProvider) issueChallenge(w http.ResponseWriter, body []byte) {
	p.mu.Lock()
	p.challenges++
	p.mu.Unlock()
	request, err := delegation.ParseChallengeRequest(body)
	if err != nil {
		apierr.Write(w, apierr.PublicReadError{Status: apierr.InvalidQuery})
		return
	}
	challenge, err := p.auth.Issue("test", request)
	if err != nil {
		apierr.Write(w, apierr.PublicReadError{Status: apierr.InvalidQuery})
		return
	}
	if p.mutateChallenge != nil {
		p.mutateChallenge(&challenge)
	}
	acWriteJSON(w, http.StatusOK, challenge)
}

func (p *acProvider) deposit(w http.ResponseWriter, author string, body []byte) {
	var req wire.DepositRequest
	p.decode(body, "DepositRequest", &req)
	if err := funding.ValidateDepositRequest(req, keys.DIDOf(p.key), author, acNow); err != nil {
		p.t.Errorf("fake provider received an invalid deposit request: %v", err)
	}
	inv := wire.DepositInvoice{ProviderID: keys.DIDOf(p.key), AuthorID: author, Network: req.Network, AmountChainUnits: req.AmountChainUnits, Recipient: faux.DevAddress(req.Network, "ac-invoice"), IssuedAt: wire.FormatTime(acNow), ExpiresAt: wire.FormatTime(acNow.Add(time.Hour))}
	if p.mutateInvoice != nil {
		p.mutateInvoice(&inv)
	}
	acWriteJSON(w, http.StatusCreated, acSealInvoice(p.t, p.invoiceSigner, inv))
}

func (p *acProvider) observe(w http.ResponseWriter, author string, body []byte) {
	if p.observeGate != nil {
		<-p.observeGate
		return
	}
	var proof wire.DepositProof
	p.decode(body, "DepositProof", &proof)
	if err := funding.ValidateDepositProof(proof, keys.DIDOf(p.key), author); err != nil {
		p.t.Errorf("fake provider received an invalid deposit proof: %v", err)
	}
	p.mu.Lock()
	pending := p.pendingObserves > 0
	if pending {
		p.pendingObserves--
	}
	p.mu.Unlock()
	if pending {
		acWriteJSON(w, http.StatusAccepted, map[string]string{"status": "pending"})
		return
	}
	credit := wire.DepositCredit{ProviderID: keys.DIDOf(p.key), AuthorID: author, DepositID: proof.DepositID, Network: proof.Transfer.Network, Transfer: proof.Transfer, AmountChainUnits: "10000", AmountAccountingUnits: "10000000", CreditSequence: 1, CreditedAt: wire.FormatTime(acNow)}
	if p.mutateCredit != nil {
		p.mutateCredit(&credit)
	}
	sealed, err := (funding.Signer{Key: p.key}).SealCredit(credit)
	if err != nil {
		p.t.Errorf("seal credit: %v", err)
	}
	acWriteJSON(w, http.StatusCreated, sealed)
}

func (p *acProvider) balance(w http.ResponseWriter, author, network string) {
	status := wire.BalanceStatus{ProviderID: keys.DIDOf(p.key), AuthorID: author, Network: network, Denomination: "millisatoshi", Available: "10000000", Frozen: "0", Reserved: "136", Revision: 2, UpdatedAt: wire.FormatTime(acNow)}
	encoded, err := canon.EncodeWithout(status, "provider_signature")
	if err == nil {
		status.ProviderSignature, err = keys.Sign(p.balanceSigner, "balance-status", encoded)
	}
	if err != nil {
		p.t.Errorf("sign balance: %v", err)
	}
	acWriteJSON(w, http.StatusOK, status)
}

func (p *acProvider) withdraw(w http.ResponseWriter, author string, body []byte) {
	var req wire.WithdrawalRequest
	p.decode(body, "WithdrawalRequest", &req)
	if err := funding.ValidateWithdrawalRequest(req, keys.DIDOf(p.key), author); err != nil {
		p.t.Errorf("fake provider received an invalid withdrawal request: %v", err)
	}
	if req.AmountBaseUnits == p.insufficient {
		apierr.Write(w, apierr.PublicReadError{Status: apierr.InsufficientBalance})
		return
	}
	p.mu.Lock()
	retry := p.withdrawals[req.RequestID]
	p.withdrawals[req.RequestID] = true
	p.mu.Unlock()
	status, err := (funding.Signer{Key: p.key}).SignWithdrawal(wire.WithdrawalStatus{ProviderID: req.ProviderID, AuthorID: req.AuthorID, Network: req.Network, AmountBaseUnits: req.AmountBaseUnits, Recipient: req.Recipient, RequestID: req.RequestID, Status: "pending", UpdatedAt: wire.FormatTime(acNow)})
	if err != nil {
		p.t.Errorf("sign withdrawal: %v", err)
	}
	code := http.StatusAccepted
	if retry {
		code = http.StatusOK
	}
	acWriteJSON(w, code, status)
}

func (p *acProvider) admission(w http.ResponseWriter, author, messageID string) {
	if p.receiptMessage != "" {
		messageID = p.receiptMessage
	}
	receipt, err := fee.SignReceipt(wire.SubmissionReceipt{ProviderID: keys.DIDOf(p.key), AuthorID: author, MessageID: messageID, ReceiptID: keys.EncodeB64URL(make([]byte, 32)), QuoteID: "fyfq2:" + strings.Repeat("1", 64), ScheduleID: "fyfs2:4c6b3daa8dd073011c7b347468b8c530413b37b9ecbabe79ad443ae1588a5ede", Network: wire.BitcoinMainnet, AmountBaseUnits: "136", AuthorizationAt: wire.FormatTime(acNow), IssuedAt: wire.FormatTime(acNow)}, p.key)
	if err != nil {
		p.t.Errorf("sign receipt: %v", err)
	}
	acWriteJSON(w, http.StatusOK, receipt)
}

func (p *acProvider) authorityResult(w http.ResponseWriter, status int, value any, err error) {
	if err != nil {
		_, name := delegation.ErrorStatus(err)
		apierr.Write(w, apierr.PublicReadError{Status: apierr.Status(name)})
		return
	}
	acWriteJSON(w, status, value)
}

func acWriteJSON(w http.ResponseWriter, status int, value any) {
	encoded, err := canon.Encode(value)
	if err != nil {
		apierr.Write(w, apierr.PublicReadError{Status: apierr.Unavailable, RetryAfterSeconds: 1})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(encoded)
}

// acSealInvoice derives the content ID and signs inv with signer without validating it, so the fake can serve invalid invoices.
func acSealInvoice(t *testing.T, signer ed25519.PrivateKey, inv wire.DepositInvoice) wire.DepositInvoice {
	t.Helper()
	id, err := funding.DepositID(inv)
	if err != nil {
		t.Fatal(err)
	}
	inv.DepositID = id
	encoded, err := canon.EncodeWithout(inv, "provider_signature")
	if err == nil {
		inv.ProviderSignature, err = keys.Sign(signer, "deposit-invoice", encoded)
	}
	if err != nil {
		t.Fatal(err)
	}
	return inv
}

func TestACServiceAuthSignsOnlyCheckedChallenges(t *testing.T) {
	t.Run("authenticated call verifies at the provider with one challenge", func(t *testing.T) {
		p := acNewProvider(t, nil)
		if _, err := p.client.Balance(context.Background(), acAuthorKey(), wire.BitcoinMainnet); err != nil {
			t.Fatal(err)
		}
		if p.issued() != 1 || p.count("GET /v1/balances/bitcoin-mainnet") != 1 {
			t.Fatalf("challenges/verified requests = %d/%d; want 1/1", p.issued(), p.count("GET /v1/balances/bitcoin-mainnet"))
		}
	})
	t.Run("expiry 65 seconds ahead is within the 5 second tolerance", func(t *testing.T) {
		p := acNewProvider(t, func(p *acProvider) {
			p.mutateChallenge = func(c *wire.ServiceChallenge) { c.ExpiresAt = "2026-11-11T12:01:05Z" }
		})
		headers, err := p.client.acAuthenticate(context.Background(), acAuthorKey(), http.MethodGet, "/v1/balances/bitcoin-mainnet", nil)
		if err != nil || headers.Get(delegation.HeaderSignature) == "" {
			t.Fatalf("acAuthenticate = %v, %v; want a signed challenge", headers, err)
		}
	})
	t.Run("resource differs", func(t *testing.T) {
		acCheckMismatch(t, func(c *wire.ServiceChallenge) { c.Resource = "/v1/balances/ethereum-mainnet" })
	})
	t.Run("body hash differs", func(t *testing.T) {
		acCheckMismatch(t, func(c *wire.ServiceChallenge) { c.BodyHash = hex.EncodeToString(make([]byte, sha256.Size)) })
	})
	t.Run("service_id is another key", func(t *testing.T) {
		acCheckMismatch(t, func(c *wire.ServiceChallenge) { c.ServiceID = keys.DIDOf(acOtherKey()) })
	})
	t.Run("expiry 66 seconds ahead exceeds 60 seconds plus tolerance", func(t *testing.T) {
		acCheckMismatch(t, func(c *wire.ServiceChallenge) { c.ExpiresAt = "2026-11-11T12:01:06Z" })
	})
	t.Run("challenge is 31 bytes", func(t *testing.T) {
		acCheckMismatch(t, func(c *wire.ServiceChallenge) { c.Challenge = keys.EncodeB64URL(make([]byte, 31)) })
	})
}

func acCheckMismatch(t *testing.T, mutate func(*wire.ServiceChallenge)) {
	t.Helper()
	p := acNewProvider(t, func(p *acProvider) { p.mutateChallenge = mutate })
	_, err := p.client.Balance(context.Background(), acAuthorKey(), wire.BitcoinMainnet)
	if !errors.Is(err, ErrChallengeMismatch) {
		t.Fatalf("Balance error = %v; want ErrChallengeMismatch", err)
	}
	if p.issued() != 1 || p.count("GET /v1/balances/bitcoin-mainnet") != 0 {
		t.Fatalf("challenges/signed requests = %d/%d; want 1/0", p.issued(), p.count("GET /v1/balances/bitcoin-mainnet"))
	}
}
