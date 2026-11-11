package delegation

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

var (
	// ErrMalformedRequest identifies malformed challenge issuance input.
	ErrMalformedRequest = errors.New("serviceauth: malformed request")
	// ErrContentType identifies a body representation that does not match its route.
	ErrContentType = errors.New("serviceauth: content type does not match route")
	// ErrUnauthenticated identifies an invalid or replayed service-auth request.
	ErrUnauthenticated = errors.New("serviceauth: unauthenticated")
	// ErrRateLimited identifies a saturated source-specific issuance limit.
	ErrRateLimited = errors.New("serviceauth: per-source limit reached")
	// ErrSaturated identifies exhausted global challenge capacity.
	ErrSaturated = errors.New("serviceauth: issuance capacity exhausted")
	// ErrClockUncertain identifies a clock rollback beyond the allowed bound.
	ErrClockUncertain = errors.New("serviceauth: clock uncertain")
	// ErrStateUncertain identifies unavailable or inconsistent challenge state.
	ErrStateUncertain = errors.New("serviceauth: challenge state uncertain")
	// ErrChallengeMismatch identifies a challenge that differs from the approved operation.
	ErrChallengeMismatch = errors.New("serviceauth: challenge does not match the approved request")
)

// ServiceAuthConfig configures root-authenticated service requests.
type ServiceAuthConfig struct {
	// ServiceID is the provider's canonical signing DID.
	ServiceID string
	// Store holds outstanding challenges and the generation fence.
	Store ChallengeStore
	// Now supplies the current time; nil uses time.Now.
	Now func() time.Time
	// Random supplies challenge entropy; nil uses crypto/rand.Reader.
	Random io.Reader
	// Routes supplies supported route shapes; nil uses DefaultRouteShapes.
	Routes []RouteShape
	// Limits discloses and enforces issuance capacity.
	Limits Limits
}

// ServiceAuth issues and verifies one-use root-authenticated requests.
type ServiceAuth struct {
	serviceID  string
	store      ChallengeStore
	now        func() time.Time
	random     io.Reader
	routes     []RouteShape
	limits     Limits
	mu         sync.Mutex
	highWater  time.Time
	clockSet   bool
	generation uint64
}

// NewServiceAuth validates configuration and captures the store's current generation.
func NewServiceAuth(cfg ServiceAuthConfig) (*ServiceAuth, error) {
	if _, err := keys.ParseDID(cfg.ServiceID); err != nil || cfg.Store == nil || cfg.Limits.MaxOutstanding < 1 || cfg.Limits.MaxPerSource < 1 || cfg.Limits.ReservedProtected < 0 || cfg.Limits.ReservedProtected > cfg.Limits.MaxOutstanding {
		return nil, ErrMalformedRequest
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Random == nil {
		cfg.Random = rand.Reader
	}
	routes := cfg.Routes
	if routes == nil {
		routes = DefaultRouteShapes
	}
	generation, err := cfg.Store.Generation()
	if err != nil {
		return nil, ErrStateUncertain
	}
	return &ServiceAuth{serviceID: cfg.ServiceID, store: cfg.Store, now: cfg.Now, random: cfg.Random, routes: append([]RouteShape(nil), routes...), limits: cfg.Limits, generation: generation}, nil
}

// Issue stores and returns a short-lived challenge for a supported operation.
func (a *ServiceAuth) Issue(source string, req ChallengeRequest) (wire.ServiceChallenge, error) {
	a.mu.Lock()
	now, err := a.clockNow()
	generation := a.generation
	a.mu.Unlock()
	if err != nil {
		return wire.ServiceChallenge{}, err
	}
	if challengeValidateRequest(req) != nil {
		return wire.ServiceChallenge{}, ErrMalformedRequest
	}
	route, ok := challengeRouteMatch(req.Method, req.Resource, a.routes)
	if !ok {
		return wire.ServiceChallenge{}, ErrMalformedRequest
	}
	storedGeneration, err := a.store.Generation()
	if err != nil || storedGeneration != generation {
		return wire.ServiceChallenge{}, ErrStateUncertain
	}
	random := make([]byte, 32)
	if _, err := io.ReadFull(a.random, random); err != nil {
		return wire.ServiceChallenge{}, ErrStateUncertain
	}
	challenge := keys.EncodeB64URL(random)
	expires := now.Truncate(time.Second).Add(ChallengeLifetime)
	result := wire.ServiceChallenge{Challenge: challenge, ServiceID: a.serviceID, AuthorID: req.AuthorID, Method: req.Method, Resource: req.Resource, BodyHash: req.BodyHash, ExpiresAt: wire.FormatTime(expires)}
	record := ChallengeRecord{Challenge: result, Source: source, Protected: route.Protected, Generation: generation, ExpiresAt: expires}
	if err := a.store.Insert(record, a.limits, now); err != nil {
		if errors.Is(err, ErrRateLimited) || errors.Is(err, ErrSaturated) {
			return wire.ServiceChallenge{}, err
		}
		return wire.ServiceChallenge{}, ErrStateUncertain
	}
	return result, nil
}

// Limits returns the configured issuance limits.
func (a *ServiceAuth) Limits() Limits { return a.limits }

// Fence advances the issuance generation, invalidating challenges from prior generations.
func (a *ServiceAuth) Fence() error {
	a.mu.Lock()
	previousGeneration := a.generation
	a.mu.Unlock()
	generation, err := a.store.AdvanceGeneration()
	if err != nil || generation <= previousGeneration {
		return ErrStateUncertain
	}
	a.mu.Lock()
	if generation > a.generation {
		a.generation = generation
	}
	currentGeneration := a.generation
	a.mu.Unlock()
	if generation != currentGeneration {
		return ErrStateUncertain
	}
	return nil
}

// AuthRequest contains a bounded request's route, body and authentication headers.
type AuthRequest struct {
	Method, EscapedPath, RawQuery, ContentType string
	Kind                                       BodyKind
	Body                                       []byte
	Challenge, Signature                       string
}

// NewAuthRequest extracts service-auth header values without importing net/http.
func NewAuthRequest(method, escapedPath, rawQuery string, header func(string) string, kind BodyKind, body []byte) AuthRequest {
	request := AuthRequest{Method: method, EscapedPath: escapedPath, RawQuery: rawQuery, Kind: kind, Body: body}
	if header != nil {
		request.ContentType = header("Content-Type")
		request.Challenge = header(HeaderChallenge)
		request.Signature = header(HeaderSignature)
	}
	return request
}

// Verify checks and consumes a request proof, returning the authenticated root author.
func (a *ServiceAuth) Verify(r AuthRequest) (string, error) {
	if challengeContentType(r.Kind, r.ContentType) != nil || r.Method == "GET" && r.Kind != BodyNone || r.Kind == BodyNone && r.Method != "GET" {
		return "", ErrContentType
	}
	a.mu.Lock()
	now, err := a.clockNow()
	generation := a.generation
	a.mu.Unlock()
	if err != nil {
		return "", err
	}
	presented, canonical, err := challengeDecodeHeader(r.Challenge)
	if err != nil || r.Signature == "" {
		return "", ErrUnauthenticated
	}
	if err := challengeValidateWire(presented); err != nil {
		return "", ErrUnauthenticated
	}
	storedGeneration, err := a.store.Generation()
	if err != nil || storedGeneration != generation {
		return "", ErrStateUncertain
	}
	a.mu.Lock()
	currentGeneration := a.generation
	a.mu.Unlock()
	if currentGeneration != generation {
		return "", ErrStateUncertain
	}
	record, exists, err := a.store.Get(presented.Challenge)
	if err != nil {
		return "", ErrStateUncertain
	}
	if !exists || record.Generation != generation || record.Challenge != presented {
		return "", ErrUnauthenticated
	}
	_, ok := challengeRouteMatch(r.Method, r.EscapedPath, a.routes)
	if !ok || presented.ServiceID != a.serviceID || presented.Method != r.Method || presented.Resource != r.EscapedPath || r.RawQuery != "" || ValidResource(r.EscapedPath) != nil {
		return "", ErrUnauthenticated
	}
	bodyHash, err := BodyHash(r.Kind, r.Body)
	if err != nil || presented.BodyHash != bodyHash {
		return "", ErrUnauthenticated
	}
	expires, err := wire.ParseTime(presented.ExpiresAt)
	if err != nil || !now.Before(expires) || !record.ExpiresAt.Equal(expires) {
		return "", ErrUnauthenticated
	}
	if err := keys.Verify(presented.AuthorID, "service-auth", canonical, r.Signature); err != nil {
		return "", ErrUnauthenticated
	}
	consumed, err := a.store.Consume(presented.Challenge, generation, now)
	if err != nil {
		return "", ErrStateUncertain
	}
	if !consumed {
		storedGeneration, err := a.store.Generation()
		a.mu.Lock()
		currentGeneration := a.generation
		a.mu.Unlock()
		if err != nil || storedGeneration != generation || currentGeneration != generation {
			return "", ErrStateUncertain
		}
		return "", ErrUnauthenticated
	}
	return presented.AuthorID, nil
}

// AuthErrorStatus maps service-auth failures to the public status catalog.
func AuthErrorStatus(err error) (int, string) {
	switch {
	case errors.Is(err, ErrMalformedRequest), errors.Is(err, ErrContentType):
		return 400, string(apierr.InvalidQuery)
	case errors.Is(err, ErrUnauthenticated):
		return 401, string(apierr.Unauthenticated)
	case errors.Is(err, ErrRateLimited):
		return 429, string(apierr.RateLimited)
	case errors.Is(err, ErrSaturated), errors.Is(err, ErrClockUncertain), errors.Is(err, ErrStateUncertain):
		return 503, string(apierr.Unavailable)
	default:
		return 503, string(apierr.Unavailable)
	}
}

func (a *ServiceAuth) clockNow() (time.Time, error) {
	now := a.now().UTC()
	if a.clockSet && now.Before(a.highWater.Add(-60*time.Second)) {
		return time.Time{}, ErrClockUncertain
	}
	if now.After(a.highWater) {
		a.highWater = now
	}
	a.clockSet = true
	return a.highWater, nil
}

func challengeContentType(kind BodyKind, contentType string) error {
	switch kind {
	case BodyNone:
		if contentType != "" {
			return ErrContentType
		}
	case BodyJSON:
		if strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]) != "application/json" {
			return ErrContentType
		}
	case BodyOctets:
		if strings.TrimSpace(strings.SplitN(contentType, ";", 2)[0]) != "application/octet-stream" {
			return ErrContentType
		}
	default:
		return ErrContentType
	}
	return nil
}

func challengeDecodeHeader(header string) (wire.ServiceChallenge, []byte, error) {
	if header == "" || strings.Contains(header, "=") {
		return wire.ServiceChallenge{}, nil, ErrUnauthenticated
	}
	encoded, err := base64.RawURLEncoding.DecodeString(header)
	if err != nil || base64.RawURLEncoding.EncodeToString(encoded) != header {
		return wire.ServiceChallenge{}, nil, ErrUnauthenticated
	}
	canonical, err := canon.Canonicalize(encoded, canon.Limits{MaxBytes: 4096, MaxDepth: 4})
	if err != nil || string(canonical) != string(encoded) {
		return wire.ServiceChallenge{}, nil, ErrUnauthenticated
	}
	value, err := canon.Parse(encoded, canon.Limits{MaxBytes: 4096, MaxDepth: 4})
	if err != nil || wire.Check("ServiceChallenge", value) != nil {
		return wire.ServiceChallenge{}, nil, ErrUnauthenticated
	}
	var challenge wire.ServiceChallenge
	if err := json.Unmarshal(encoded, &challenge); err != nil {
		return wire.ServiceChallenge{}, nil, ErrUnauthenticated
	}
	return challenge, canonical, nil
}

func challengeValidateWire(challenge wire.ServiceChallenge) error {
	if _, err := keys.ParseDID(challenge.AuthorID); err != nil {
		return err
	}
	if _, err := keys.ParseDID(challenge.ServiceID); err != nil {
		return err
	}
	if _, err := keys.DecodeB64URL(challenge.Challenge, 32); err != nil {
		return err
	}
	if challengeValidateRequest(ChallengeRequest{AuthorID: challenge.AuthorID, Method: challenge.Method, Resource: challenge.Resource, BodyHash: challenge.BodyHash}) != nil {
		return ErrMalformedRequest
	}
	if _, err := wire.ParseTime(challenge.ExpiresAt); err != nil {
		return ErrMalformedRequest
	}
	return nil
}
