package delegation

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

func challengeTestKeys(t *testing.T) (keys.PrivateKey, keys.PrivateKey) {
	t.Helper()
	authorSeed := []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31}
	providerSeed := []byte{32, 33, 34, 35, 36, 37, 38, 39, 40, 41, 42, 43, 44, 45, 46, 47, 48, 49, 50, 51, 52, 53, 54, 55, 56, 57, 58, 59, 60, 61, 62, 63}
	author, err := keys.NewKeyFromSeed(authorSeed)
	if err != nil {
		t.Fatal(err)
	}
	provider, err := keys.NewKeyFromSeed(providerSeed)
	if err != nil {
		t.Fatal(err)
	}
	return author, provider
}

func challengeTestAuth(t *testing.T, now *time.Time, limits Limits, random []byte) (*ServiceAuth, keys.PrivateKey, keys.PrivateKey, ChallengeRequest) {
	t.Helper()
	author, provider := challengeTestKeys(t)
	bodyHash, err := BodyHash(BodyJSON, []byte(`{"b":1,"a":2}`))
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewServiceAuth(ServiceAuthConfig{ServiceID: keys.DIDOf(provider), Store: NewMemoryChallengeStore(), Now: func() time.Time { return *now }, Random: bytes.NewReader(random), Limits: limits})
	if err != nil {
		t.Fatal(err)
	}
	return service, author, provider, ChallengeRequest{AuthorID: keys.DIDOf(author), Method: "POST", Resource: "/v1/revocations", BodyHash: bodyHash}
}

func challengeTestRandom() []byte {
	return append(append(append(append(bytes.Repeat([]byte{0}, 32), bytes.Repeat([]byte{1}, 32)...), bytes.Repeat([]byte{2}, 32)...), bytes.Repeat([]byte{3}, 32)...), bytes.Repeat([]byte{4}, 256)...)
}

func challengeTestRequest(t *testing.T, signer keys.PrivateKey, challenge wire.ServiceChallenge, body []byte) AuthRequest {
	t.Helper()
	challengeHeader, signature, err := SignChallenge(signer, challenge)
	if err != nil {
		t.Fatal(err)
	}
	return AuthRequest{Method: challenge.Method, EscapedPath: challenge.Resource, ContentType: "application/json", Kind: BodyJSON, Body: body, Challenge: challengeHeader, Signature: signature}
}

func challengeAssertMalformed(t *testing.T, service *ServiceAuth, source string, request ChallengeRequest) {
	t.Helper()
	_, err := service.Issue(source, request)
	if !errors.Is(err, ErrMalformedRequest) {
		t.Fatalf("Issue() error = %v, want ErrMalformedRequest", err)
	}
	if status, name := AuthErrorStatus(err); status != 400 || name != "invalid_query" {
		t.Fatalf("AuthErrorStatus() = (%d, %q), want (400, invalid_query)", status, name)
	}
}

func TestServiceAuthPublishedDIDs(t *testing.T) {
	author, provider := challengeTestKeys(t)
	t.Run("author seed", func(t *testing.T) {
		if got := keys.DIDOf(author); got != "did:key:z6MkehRgf7yJbgaGfYsdoAsKdBPE3dj2CYhowQdcjqSJgvVd" {
			t.Fatalf("AUTHOR DID = %q", got)
		}
	})
	t.Run("provider seed", func(t *testing.T) {
		if got := keys.DIDOf(provider); got != "did:key:z6MkhFwXNFWosLeugvSf4wcL9t3uuRXueGSFTRgSvHhWj5G2" {
			t.Fatalf("PROVIDER DID = %q", got)
		}
	})
}

func TestBodyHash(t *testing.T) {
	check := func(t *testing.T, kind BodyKind, body []byte, want string) {
		t.Helper()
		got, err := BodyHash(kind, body)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("BodyHash() = %q, want %q", got, want)
		}
	}
	t.Run("canonical JSON", func(t *testing.T) {
		check(t, BodyJSON, []byte(`{"b":1,"a":2}`), "d3626ac30a87e6f7a6428233b3c68299976865fa5508e4267c5415c76af7a772")
	})
	t.Run("raw octets", func(t *testing.T) {
		check(t, BodyOctets, []byte{0, 1, 2}, "ae4b3280e56e2faf83f414a6e3dabe9d5fbe18976544c05fed121accb85b53fc")
	})
	t.Run("empty body", func(t *testing.T) {
		check(t, BodyNone, nil, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855")
	})
	t.Run("duplicate JSON key", func(t *testing.T) {
		if _, err := BodyHash(BodyJSON, []byte(`{"a":1,"a":2}`)); err == nil {
			t.Fatal("BodyHash accepted duplicate JSON keys")
		}
	})
}

func TestServiceAuthIssuanceRejections(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	service, _, _, valid := challengeTestAuth(t, &now, Limits{MaxOutstanding: 20, MaxPerSource: 20}, challengeTestRandom())
	t.Run("4097-byte otherwise valid request", func(t *testing.T) {
		prefix := `{"author_id":"` + valid.AuthorID + `","method":"POST","resource":"/v1/revocations","body_hash":"`
		suffix := `"}`
		body := []byte(prefix + valid.BodyHash + strings.Repeat("0", 4097-len(prefix)-len(valid.BodyHash)-len(suffix)) + suffix)
		if _, err := ParseChallengeRequest(body); !errors.Is(err, ErrMalformedRequest) {
			t.Fatalf("ParseChallengeRequest() error = %v", err)
		}
		if status, name := AuthErrorStatus(ErrMalformedRequest); status != 400 || name != "invalid_query" {
			t.Fatalf("AuthErrorStatus() = (%d, %q)", status, name)
		}
	})
	t.Run("2049-byte resource", func(t *testing.T) {
		request := valid
		request.Resource = "/v1/" + strings.Repeat("x", 2045)
		challengeAssertMalformed(t, service, "resource-long", request)
	})
	t.Run("dot segment", func(t *testing.T) {
		request := valid
		request.Resource = "/v1/balances/../x"
		challengeAssertMalformed(t, service, "dot-segment", request)
	})
	t.Run("query in resource", func(t *testing.T) {
		request := valid
		request.Resource = "/v1/revocations?x=1"
		challengeAssertMalformed(t, service, "query", request)
	})
	t.Run("lowercase percent escape", func(t *testing.T) {
		request := valid
		request.Resource = "/v1/exports/%2f"
		challengeAssertMalformed(t, service, "lowercase-escape", request)
	})
	t.Run("encoded unreserved byte", func(t *testing.T) {
		request := valid
		request.Resource = "/v1/exports/%41"
		challengeAssertMalformed(t, service, "encoded-unreserved", request)
	})
	t.Run("unsupported route shape", func(t *testing.T) {
		request := valid
		request.Resource = "/v1/messages"
		challengeAssertMalformed(t, service, "unsupported-route", request)
	})
	t.Run("GET with nonempty body hash", func(t *testing.T) {
		request := valid
		request.Method = "GET"
		request.Resource = "/v1/balances/bitcoin-mainnet"
		challengeAssertMalformed(t, service, "get-body", request)
	})
	t.Run("noncanonical DID", func(t *testing.T) {
		request := valid
		request.AuthorID += "="
		challengeAssertMalformed(t, service, "bad-did", request)
	})
}

func TestServiceAuthRoundTripAndFailedCheck(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	service, author, _, request := challengeTestAuth(t, &now, Limits{MaxOutstanding: 4, MaxPerSource: 2}, challengeTestRandom())
	t.Run("issue and verify", func(t *testing.T) {
		challenge, err := service.Issue("source-a", request)
		if err != nil {
			t.Fatal(err)
		}
		if challenge.ExpiresAt != "2026-11-11T12:01:00Z" {
			t.Fatalf("expires_at = %q", challenge.ExpiresAt)
		}
		if _, err := keys.DecodeB64URL(challenge.Challenge, 32); err != nil {
			t.Fatalf("challenge is not canonical 32-byte base64url: %v", err)
		}
		valid := challengeTestRequest(t, author, challenge, []byte(`{"a":2,"b":1}`))
		if got, err := service.Verify(valid); err != nil || got != request.AuthorID {
			t.Fatalf("Verify(valid) = %q, %v", got, err)
		}
	})
	t.Run("signed bytes are the service-auth label over hand-built JCS", func(t *testing.T) {
		challenge, err := service.Issue("source-c", request)
		if err != nil {
			t.Fatal(err)
		}
		canonical := []byte(`{"author_id":"` + challenge.AuthorID + `","body_hash":"` + challenge.BodyHash + `","challenge":"` + challenge.Challenge + `","expires_at":"2026-11-11T12:01:00Z","method":"POST","resource":"/v1/revocations","service_id":"` + challenge.ServiceID + `"}`)
		challengeHeader, _, err := SignChallenge(author, challenge)
		if err != nil {
			t.Fatal(err)
		}
		if challengeHeader != keys.EncodeB64URL(canonical) {
			t.Fatalf("SignChallenge header = %q; want base64url of %s", challengeHeader, canonical)
		}
		signature, err := keys.Sign(author, "service-auth", canonical)
		if err != nil {
			t.Fatal(err)
		}
		authRequest := AuthRequest{Method: "POST", EscapedPath: "/v1/revocations", ContentType: "application/json", Kind: BodyJSON, Body: []byte(`{"a":2,"b":1}`), Challenge: keys.EncodeB64URL(canonical), Signature: signature}
		if got, err := service.Verify(authRequest); err != nil || got != request.AuthorID {
			t.Fatalf("Verify(independently signed) = %q, %v", got, err)
		}
	})
	t.Run("failed verification does not consume", func(t *testing.T) {
		challenge, err := service.Issue("source-b", request)
		if err != nil {
			t.Fatal(err)
		}
		wrong := challengeTestRequest(t, author, challenge, []byte(`{"a":2,"b":3}`))
		if _, err := service.Verify(wrong); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("Verify(wrong body) error = %v", err)
		}
		valid := challengeTestRequest(t, author, challenge, []byte(`{"a":2,"b":1}`))
		if got, err := service.Verify(valid); err != nil || got != request.AuthorID {
			t.Fatalf("Verify(correct request after failure) = %q, %v", got, err)
		}
	})
}

func TestServiceAuthVerifyRejections(t *testing.T) {
	newCase := func(t *testing.T) (*ServiceAuth, keys.PrivateKey, keys.PrivateKey, ChallengeRequest, wire.ServiceChallenge, []byte) {
		t.Helper()
		now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		service, author, provider, request := challengeTestAuth(t, &now, Limits{MaxOutstanding: 8, MaxPerSource: 8}, challengeTestRandom())
		challenge, err := service.Issue("source", request)
		if err != nil {
			t.Fatal(err)
		}
		return service, author, provider, request, challenge, []byte(`{"a":2,"b":1}`)
	}
	check := func(t *testing.T, service *ServiceAuth, request AuthRequest) {
		t.Helper()
		if _, err := service.Verify(request); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("Verify() error = %v, want ErrUnauthenticated", err)
		}
	}
	t.Run("other method", func(t *testing.T) {
		service, author, _, _, challenge, body := newCase(t)
		request := challengeTestRequest(t, author, challenge, body)
		request.Method = "PUT"
		check(t, service, request)
	})
	t.Run("other resource", func(t *testing.T) {
		service, author, _, _, challenge, body := newCase(t)
		request := challengeTestRequest(t, author, challenge, body)
		request.EscapedPath = "/v1/withdrawals"
		check(t, service, request)
	})
	t.Run("changed body", func(t *testing.T) {
		service, author, _, _, challenge, _ := newCase(t)
		check(t, service, challengeTestRequest(t, author, challenge, []byte(`{"a":2,"b":9}`)))
	})
	t.Run("query string", func(t *testing.T) {
		service, author, _, _, challenge, body := newCase(t)
		request := challengeTestRequest(t, author, challenge, body)
		request.RawQuery = "x=1"
		check(t, service, request)
	})
	t.Run("different service ID in signed object", func(t *testing.T) {
		service, author, _, _, challenge, body := newCase(t)
		challenge.ServiceID = keys.DIDOf(author)
		check(t, service, challengeTestRequest(t, author, challenge, body))
	})
	t.Run("signed by provider", func(t *testing.T) {
		service, _, provider, _, challenge, body := newCase(t)
		check(t, service, challengeTestRequest(t, provider, challenge, body))
	})
	t.Run("signed under message label", func(t *testing.T) {
		service, author, _, _, challenge, body := newCase(t)
		canonical, err := canon.Encode(challenge)
		if err != nil {
			t.Fatal(err)
		}
		signature, err := keys.Sign(author, "message", canonical)
		if err != nil {
			t.Fatal(err)
		}
		request := challengeTestRequest(t, author, challenge, body)
		request.Signature = signature
		check(t, service, request)
	})
	t.Run("exactly at expires_at", func(t *testing.T) {
		now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
		service, author, _, request := challengeTestAuth(t, &now, Limits{MaxOutstanding: 2, MaxPerSource: 2}, challengeTestRandom())
		issued, err := service.Issue("source", request)
		if err != nil {
			t.Fatal(err)
		}
		authRequest := challengeTestRequest(t, author, issued, []byte(`{"a":2,"b":1}`))
		now = now.Add(ChallengeLifetime)
		check(t, service, authRequest)
	})
	t.Run("second use", func(t *testing.T) {
		service, author, _, _, challenge, body := newCase(t)
		request := challengeTestRequest(t, author, challenge, body)
		if _, err := service.Verify(request); err != nil {
			t.Fatal(err)
		}
		check(t, service, request)
	})
	t.Run("base64 padding", func(t *testing.T) {
		service, author, _, _, challenge, body := newCase(t)
		request := challengeTestRequest(t, author, challenge, body)
		request.Challenge += "="
		check(t, service, request)
	})
	t.Run("content type mismatch", func(t *testing.T) {
		service, author, _, _, challenge, body := newCase(t)
		request := challengeTestRequest(t, author, challenge, body)
		request.ContentType = "text/plain"
		if _, err := service.Verify(request); !errors.Is(err, ErrContentType) {
			t.Fatalf("Verify() error = %v, want ErrContentType", err)
		}
	})
}

func TestServiceAuthConcurrentConsumption(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	service, author, _, request := challengeTestAuth(t, &now, Limits{MaxOutstanding: 2, MaxPerSource: 2}, challengeTestRandom())
	challenge, err := service.Issue("source", request)
	if err != nil {
		t.Fatal(err)
	}
	authRequest := challengeTestRequest(t, author, challenge, []byte(`{"a":2,"b":1}`))
	start := make(chan struct{})
	results := make(chan error, 4)
	var wait sync.WaitGroup
	wait.Add(4)
	go func() { defer wait.Done(); <-start; _, err := service.Verify(authRequest); results <- err }()
	go func() { defer wait.Done(); <-start; _, err := service.Verify(authRequest); results <- err }()
	go func() { defer wait.Done(); <-start; _, err := service.Verify(authRequest); results <- err }()
	go func() { defer wait.Done(); <-start; _, err := service.Verify(authRequest); results <- err }()
	close(start)
	wait.Wait()
	first := <-results
	second := <-results
	third := <-results
	fourth := <-results
	successes := 0
	if first == nil {
		successes++
	}
	if second == nil {
		successes++
	}
	if third == nil {
		successes++
	}
	if fourth == nil {
		successes++
	}
	if successes != 1 {
		t.Fatalf("successful verifications = %d; want exactly one (errors %v, %v, %v, %v)", successes, first, second, third, fourth)
	}
}

func TestServiceAuthFence(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	service, author, _, request := challengeTestAuth(t, &now, Limits{MaxOutstanding: 3, MaxPerSource: 3}, challengeTestRandom())
	t.Run("old generation is fenced", func(t *testing.T) {
		old, err := service.Issue("source", request)
		if err != nil {
			t.Fatal(err)
		}
		if err := service.Fence(); err != nil {
			t.Fatal(err)
		}
		if _, err := service.Verify(challengeTestRequest(t, author, old, []byte(`{"a":2,"b":1}`))); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("Verify(old generation) error = %v", err)
		}
	})
	t.Run("new generation verifies", func(t *testing.T) {
		challenge, err := service.Issue("source", request)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := service.Verify(challengeTestRequest(t, author, challenge, []byte(`{"a":2,"b":1}`))); err != nil {
			t.Fatal(err)
		}
	})
}

func TestServiceAuthClock(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	service, author, _, request := challengeTestAuth(t, &now, Limits{MaxOutstanding: 3, MaxPerSource: 3}, challengeTestRandom())
	challenge, err := service.Issue("source", request)
	if err != nil {
		t.Fatal(err)
	}
	authRequest := challengeTestRequest(t, author, challenge, []byte(`{"a":2,"b":1}`))
	t.Run("expiry does not revive after 30-second rollback", func(t *testing.T) {
		now = now.Add(ChallengeLifetime)
		if _, err := service.Verify(authRequest); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("Verify(at expiry) error = %v", err)
		}
		now = now.Add(-30 * time.Second)
		if _, err := service.Verify(authRequest); !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("Verify(after rollback) error = %v", err)
		}
	})
	t.Run("rollback beyond 60 seconds blocks verification", func(t *testing.T) {
		now = now.Add(-31 * time.Second)
		if _, err := service.Verify(authRequest); !errors.Is(err, ErrClockUncertain) {
			t.Fatalf("Verify(after large rollback) error = %v", err)
		}
	})
}

func TestServiceAuthCapacity(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	service, author, _, request := challengeTestAuth(t, &now, Limits{MaxOutstanding: 1, MaxPerSource: 2}, challengeTestRandom())
	challenge, err := service.Issue("source-a", request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Verify(challengeTestRequest(t, author, challenge, []byte(`{"a":2,"b":1}`))); err != nil {
		t.Fatal(err)
	}
	t.Run("consumed challenge retains capacity", func(t *testing.T) {
		if _, err := service.Issue("source-b", request); !errors.Is(err, ErrSaturated) {
			t.Fatalf("Issue() error = %v, want ErrSaturated", err)
		}
		now = now.Add(ChallengeLifetime - time.Second)
		if _, err := service.Issue("source-b", request); !errors.Is(err, ErrSaturated) {
			t.Fatalf("Issue(before expiry) error = %v, want ErrSaturated", err)
		}
	})
	t.Run("capacity returns at expiry", func(t *testing.T) {
		now = now.Add(time.Second)
		if _, err := service.Issue("source-b", request); err != nil {
			t.Fatalf("Issue(at expiry) error = %v", err)
		}
	})
}

func TestServiceAuthPerSourceLimit(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	service, _, _, request := challengeTestAuth(t, &now, Limits{MaxOutstanding: 4, MaxPerSource: 1}, challengeTestRandom())
	if _, err := service.Issue("source-a", request); err != nil {
		t.Fatal(err)
	}
	t.Run("same source is rate limited", func(t *testing.T) {
		if _, err := service.Issue("source-a", request); !errors.Is(err, ErrRateLimited) {
			t.Fatalf("Issue(same source) error = %v", err)
		}
	})
	t.Run("different source can issue", func(t *testing.T) {
		if _, err := service.Issue("source-b", request); err != nil {
			t.Fatalf("Issue(different source) error = %v", err)
		}
	})
}

func TestServiceAuthProtectedCapacity(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	service, _, _, protected := challengeTestAuth(t, &now, Limits{MaxOutstanding: 2, MaxPerSource: 2, ReservedProtected: 1}, challengeTestRandom())
	unprotected := protected
	unprotected.Resource = "/v1/deposits"
	if _, err := service.Issue("source-a", unprotected); err != nil {
		t.Fatal(err)
	}
	t.Run("unprotected issuance cannot use reserved slot", func(t *testing.T) {
		if _, err := service.Issue("source-b", unprotected); !errors.Is(err, ErrSaturated) {
			t.Fatalf("Issue(unprotected) error = %v", err)
		}
	})
	t.Run("protected route can use reserved slot", func(t *testing.T) {
		if _, err := service.Issue("source-b", protected); err != nil {
			t.Fatalf("Issue(protected) error = %v", err)
		}
	})
}

func TestCheckBeforeSigning(t *testing.T) {
	now := time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)
	service, author, _, request := challengeTestAuth(t, &now, Limits{MaxOutstanding: 2, MaxPerSource: 2}, challengeTestRandom())
	challenge, err := service.Issue("source", request)
	if err != nil {
		t.Fatal(err)
	}
	checkRejected := func(t *testing.T, candidate wire.ServiceChallenge, want ChallengeRequest, pinned string, at time.Time, tolerance time.Duration) {
		t.Helper()
		if err := CheckBeforeSigning(candidate, want, pinned, at, tolerance); !errors.Is(err, ErrChallengeMismatch) {
			t.Fatalf("CheckBeforeSigning() error = %v, want ErrChallengeMismatch", err)
		}
	}
	t.Run("accepts issued challenge", func(t *testing.T) {
		if err := CheckBeforeSigning(challenge, request, challenge.ServiceID, now, time.Second); err != nil {
			t.Fatal(err)
		}
		challengeHeader, signature, err := SignChallenge(author, challenge)
		if err != nil || challengeHeader == "" || signature == "" {
			t.Fatalf("SignChallenge() = (%q, %q, %v)", challengeHeader, signature, err)
		}
	})
	t.Run("rejects expiry 61 seconds ahead", func(t *testing.T) {
		candidate := challenge
		candidate.ExpiresAt = wire.FormatTime(now.Add(61 * time.Second))
		checkRejected(t, candidate, request, challenge.ServiceID, now, 0)
	})
	t.Run("rejects expired challenge", func(t *testing.T) {
		candidate := challenge
		candidate.ExpiresAt = wire.FormatTime(now)
		checkRejected(t, candidate, request, challenge.ServiceID, now, 0)
	})
	t.Run("rejects different resource", func(t *testing.T) {
		want := request
		want.Resource = "/v1/withdrawals"
		checkRejected(t, challenge, want, challenge.ServiceID, now, 0)
	})
	t.Run("rejects different service ID", func(t *testing.T) {
		checkRejected(t, challenge, request, keys.DIDOf(author), now, 0)
	})
	t.Run("rejects 31-byte challenge", func(t *testing.T) {
		candidate := challenge
		candidate.Challenge = keys.EncodeB64URL(bytes.Repeat([]byte{1}, 31))
		checkRejected(t, candidate, request, challenge.ServiceID, now, 0)
	})
	t.Run("expiry lower bound uses clock tolerance", func(t *testing.T) {
		candidate := challenge
		candidate.ExpiresAt = wire.FormatTime(now.Add(-time.Second))
		if err := CheckBeforeSigning(candidate, request, challenge.ServiceID, now, 2*time.Second); err != nil {
			t.Fatalf("CheckBeforeSigning(within tolerance) error = %v", err)
		}
	})
}

func TestAuthErrorStatus(t *testing.T) {
	check := func(t *testing.T, err error, wantStatus int, wantName string) {
		t.Helper()
		if status, name := AuthErrorStatus(err); status != wantStatus || name != wantName {
			t.Fatalf("AuthErrorStatus(%v) = (%d, %q), want (%d, %q)", err, status, name, wantStatus, wantName)
		}
	}
	t.Run("malformed request", func(t *testing.T) { check(t, ErrMalformedRequest, 400, "invalid_query") })
	t.Run("content type", func(t *testing.T) { check(t, ErrContentType, 400, "invalid_query") })
	t.Run("unauthenticated", func(t *testing.T) { check(t, ErrUnauthenticated, 401, "unauthenticated") })
	t.Run("rate limited", func(t *testing.T) { check(t, ErrRateLimited, 429, "rate_limited") })
	t.Run("clock uncertain", func(t *testing.T) { check(t, ErrClockUncertain, 503, "unavailable") })
	t.Run("saturated", func(t *testing.T) { check(t, ErrSaturated, 503, "unavailable") })
	t.Run("state uncertain", func(t *testing.T) { check(t, ErrStateUncertain, 503, "unavailable") })
}
