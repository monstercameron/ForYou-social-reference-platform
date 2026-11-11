package delegation

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"time"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

// AuthorityConfig supplies provider identity, persistence and deterministic test hooks.
type AuthorityConfig struct {
	// ProviderKey signs epochs and state responses.
	ProviderKey keys.PrivateKey
	// Store durably records each author's authorization state.
	Store EpochStore
	// Now supplies the provider clock.
	Now func() time.Time
	// Random supplies epoch nonces.
	Random io.Reader
	// MaxRevocationsPerAuthor bounds live revocation records for one author.
	MaxRevocationsPerAuthor int
}

// Authority serializes authorization changes and delegated admission per author.
type Authority struct {
	providerKey    keys.PrivateKey
	providerID     string
	store          EpochStore
	now            func() time.Time
	random         io.Reader
	maxRevocations int
	locksMu        sync.Mutex
	locks          map[string]*rootLock
}

type rootLock struct {
	mu   sync.Mutex
	refs int
}

var errInvalidAuthorityConfig = errors.New("delegation: invalid authority config")

// NewAuthority constructs a provider authorization authority.
func NewAuthority(cfg AuthorityConfig) (*Authority, error) {
	if len(cfg.ProviderKey) != 64 || cfg.Store == nil || cfg.MaxRevocationsPerAuthor <= 0 {
		return nil, errInvalidAuthorityConfig
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Random == nil {
		cfg.Random = rand.Reader
	}
	key := append(keys.PrivateKey(nil), cfg.ProviderKey...)
	return &Authority{providerKey: key, providerID: keys.DIDOf(key), store: cfg.Store, now: cfg.Now, random: cfg.Random, maxRevocations: cfg.MaxRevocationsPerAuthor, locks: map[string]*rootLock{}}, nil
}

// Enroll activates an author's first authorization epoch or returns an exact retry.
func (a *Authority) Enroll(rootID string, req wire.AuthorizationEpochEnroll) (wire.AuthorizationEpochState, bool, error) {
	unlock := a.lockAuthor(rootID)
	defer unlock()
	current := a.now()
	now := second(current)
	if err := checkWire("AuthorizationEpochEnroll", req); err != nil {
		return wire.AuthorizationEpochState{}, false, ErrInvalidDelegation
	}
	if req.AuthorID != rootID {
		return wire.AuthorizationEpochState{}, false, ErrWrongAuthor
	}
	if req.ProviderID != a.providerID {
		return wire.AuthorizationEpochState{}, false, ErrInvalidDelegation
	}
	deadline, err := wire.ParseTime(req.NotAfter)
	if err != nil {
		return wire.AuthorizationEpochState{}, false, ErrInvalidDelegation
	}
	if current.After(deadline) {
		return wire.AuthorizationEpochState{}, false, ErrRequestExpired
	}
	if err = rootSigned(rootID, "authorization-epoch-enroll", req, req.AuthorSignature, "author_signature"); err != nil {
		return wire.AuthorizationEpochState{}, false, err
	}
	digest, err := requestDigest(req)
	if err != nil {
		return wire.AuthorizationEpochState{}, false, err
	}
	record, err := a.store.Load(rootID)
	if err != nil {
		return wire.AuthorizationEpochState{}, false, ErrUncertain
	}
	ensureRecordMaps(&record, rootID)
	pruneEnroll(&record, now)
	if used, ok := record.EnrollNonces[req.RequestNonce]; ok {
		if used.Digest != digest {
			return wire.AuthorizationEpochState{}, false, ErrRequestConflict
		}
		return record.State, false, nil
	}
	if record.State.Status != "unregistered" {
		return wire.AuthorizationEpochState{}, false, ErrStale
	}
	nonce, err := a.nonce()
	if err != nil {
		return wire.AuthorizationEpochState{}, false, ErrUncertain
	}
	epoch, err := signEpoch(a.providerKey, wire.AuthorizationEpoch{AuthorID: rootID, EpochSequence: 1, ActivatedAt: wire.FormatTime(now), Nonce: nonce})
	if err != nil {
		return wire.AuthorizationEpochState{}, false, err
	}
	state, err := signState(a.providerKey, wire.AuthorizationEpochState{AuthorID: rootID, StateRevision: 1, Status: "active", Epoch: &epoch})
	if err != nil {
		return wire.AuthorizationEpochState{}, false, err
	}
	record.State = state
	record.EnrollNonces[req.RequestNonce] = NonceUse{Digest: digest, NotAfter: deadline}
	if err = a.commitRecord(rootID, record); err != nil {
		return wire.AuthorizationEpochState{}, false, err
	}
	return state, true, nil
}

// Control freezes delegated admission or rotates the author's active epoch.
func (a *Authority) Control(rootID string, req wire.AuthorizationEpochControl) (wire.AuthorizationEpochState, error) {
	unlock := a.lockAuthor(rootID)
	defer unlock()
	if err := checkWire("AuthorizationEpochControl", req); err != nil {
		return wire.AuthorizationEpochState{}, ErrInvalidDelegation
	}
	if req.AuthorID != rootID {
		return wire.AuthorizationEpochState{}, ErrWrongAuthor
	}
	if req.ProviderID != a.providerID {
		return wire.AuthorizationEpochState{}, ErrInvalidDelegation
	}
	if err := rootSigned(rootID, "authorization-epoch-control", req, req.AuthorSignature, "author_signature"); err != nil {
		return wire.AuthorizationEpochState{}, err
	}
	digest, err := requestDigest(req)
	if err != nil {
		return wire.AuthorizationEpochState{}, err
	}
	record, err := a.store.Load(rootID)
	if err != nil {
		return wire.AuthorizationEpochState{}, ErrUncertain
	}
	ensureRecordMaps(&record, rootID)
	if existing, ok := record.ControlNonces[req.Nonce]; ok {
		if existing != digest {
			return wire.AuthorizationEpochState{}, ErrRequestConflict
		}
		return record.State, nil
	}
	if record.State.Status == "unregistered" || record.State.Epoch == nil || req.ExpectedEpochID != record.State.Epoch.EpochID || req.ExpectedStateRevision != record.State.StateRevision {
		return wire.AuthorizationEpochState{}, ErrStale
	}
	if req.Action == "freeze" && record.State.Status == "frozen" {
		return record.State, nil
	}
	if record.State.StateRevision >= 9007199254740991 {
		return wire.AuthorizationEpochState{}, ErrStale
	}
	next := record.State
	switch req.Action {
	case "freeze":
		next.Status = "frozen"
	case "rotate":
		if record.State.Epoch.EpochSequence >= 9007199254740991 {
			return wire.AuthorizationEpochState{}, ErrStale
		}
		nonce, e := a.nonce()
		if e != nil {
			return wire.AuthorizationEpochState{}, ErrUncertain
		}
		epoch, e := signEpoch(a.providerKey, wire.AuthorizationEpoch{AuthorID: rootID, EpochSequence: record.State.Epoch.EpochSequence + 1, ActivatedAt: wire.FormatTime(second(a.now())), Nonce: nonce})
		if e != nil {
			return wire.AuthorizationEpochState{}, e
		}
		next.Epoch = &epoch
		next.Status = "active"
	default:
		return wire.AuthorizationEpochState{}, ErrInvalidDelegation
	}
	next.StateRevision++
	next.ProviderSignature = ""
	next, err = signState(a.providerKey, next)
	if err != nil {
		return wire.AuthorizationEpochState{}, err
	}
	record.State = next
	record.ControlNonces[req.Nonce] = digest
	return next, a.commitRecord(rootID, record)
}

// State returns a signed authorization state response bound to a fresh challenge.
func (a *Authority) State(rootID, challenge string) (wire.AuthorizationEpochStateResponse, error) {
	if _, err := keys.DecodeB64URL(challenge, 32); err != nil {
		return wire.AuthorizationEpochStateResponse{}, ErrInvalidDelegation
	}
	unlock := a.lockAuthor(rootID)
	defer unlock()
	record, err := a.store.Load(rootID)
	if err != nil {
		return wire.AuthorizationEpochStateResponse{}, ErrUncertain
	}
	ensureRecordMaps(&record, rootID)
	state := record.State
	if state.Status == "unregistered" {
		state.AuthorID = rootID
		state.ProviderID = a.providerID
		state.StateRevision = 0
		state.Epoch = nil
		state.ProviderSignature = ""
		state, err = signState(a.providerKey, state)
		if err != nil {
			return wire.AuthorizationEpochStateResponse{}, err
		}
	}
	response := wire.AuthorizationEpochStateResponse{Challenge: challenge, State: state}
	return signResponse(a.providerKey, response)
}

// Revoke records root-authorized revocation for a valid delegation, including unseen delegations.
func (a *Authority) Revoke(rootID string, req wire.RevocationRequest) (string, error) {
	unlock := a.lockAuthor(rootID)
	defer unlock()
	if err := checkWire("RevocationRequest", req); err != nil {
		return "", ErrInvalidDelegation
	}
	if req.AuthorID != rootID {
		return "", ErrWrongAuthor
	}
	if req.ProviderID != a.providerID {
		return "", ErrInvalidDelegation
	}
	if err := rootSigned(rootID, "revoke", req, req.Signature, "signature"); err != nil {
		return "", err
	}
	d := req.Delegation
	from, err := wire.ParseTime(d.ValidFrom)
	if err != nil {
		return "", ErrInvalidDelegation
	}
	if req.DelegationID != d.DelegationID || d.AuthorID != rootID || d.ProviderID != a.providerID || d.Epoch.ProviderID != a.providerID || d.Epoch.AuthorID != rootID {
		return "", ErrInvalidDelegation
	}
	activated, err := wire.ParseTime(d.Epoch.ActivatedAt)
	if err != nil {
		return "", ErrInvalidDelegation
	}
	if activated.After(from) {
		from = activated
	}
	if err = VerifyDelegation(d, Use{AuthorID: rootID, SignerID: d.SignerID, MessageType: "post", ProviderID: a.providerID, At: from}); err != nil {
		return "", err
	}
	id := d.DelegationID
	record, err := a.store.Load(rootID)
	if err != nil {
		return "", ErrUncertain
	}
	ensureRecordMaps(&record, rootID)
	if _, ok := record.Revoked[id]; ok {
		return id, nil
	}
	until, err := wire.ParseTime(d.ValidUntil)
	if err != nil {
		return "", ErrInvalidDelegation
	}
	current := a.now()
	if !current.Before(until) {
		return id, nil
	}
	for revokedID, expiry := range record.Revoked {
		if !current.Before(expiry) {
			delete(record.Revoked, revokedID)
		}
	}
	if len(record.Revoked) >= a.maxRevocations {
		return "", ErrCapacity
	}
	record.Revoked[id] = until
	if err = a.commitRecord(rootID, record); err != nil {
		return "", err
	}
	return id, nil
}

// Admit checks current provider authorization and commits the admission under the author's lock.
func (a *Authority) Admit(d wire.Delegation, use Use, commit func(authorizationAt time.Time) error) error {
	if commit == nil {
		return ErrInvalidDelegation
	}
	unlock := a.lockAuthor(d.AuthorID)
	defer unlock()
	record, err := a.store.Load(d.AuthorID)
	if err != nil {
		return ErrUncertain
	}
	ensureRecordMaps(&record, d.AuthorID)
	at := second(a.now())
	use.At = at
	use.ProviderID = a.providerID
	if err = VerifyDelegation(d, use); err != nil {
		return err
	}
	if record.State.Status != "active" || record.State.Epoch == nil || d.Epoch.EpochID != record.State.Epoch.EpochID {
		return ErrRevoked
	}
	if expiry, ok := record.Revoked[d.DelegationID]; ok && at.Before(expiry) {
		return ErrRevoked
	}
	return commit(at)
}

// lockAuthor serializes all operations for one author and drops the lock entry when no operation is waiting, so unauthenticated author IDs cannot grow the map.
func (a *Authority) lockAuthor(authorID string) (unlock func()) {
	a.locksMu.Lock()
	lock := a.locks[authorID]
	if lock == nil {
		lock = &rootLock{}
		a.locks[authorID] = lock
	}
	lock.refs++
	a.locksMu.Unlock()
	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		a.locksMu.Lock()
		lock.refs--
		if lock.refs == 0 {
			delete(a.locks, authorID)
		}
		a.locksMu.Unlock()
	}
}

func (a *Authority) nonce() (string, error) {
	b := make([]byte, 32)
	if _, err := io.ReadFull(a.random, b); err != nil {
		return "", err
	}
	return keys.EncodeB64URL(b), nil
}

func requestDigest(v any) (string, error) {
	b, err := canon.Encode(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func ensureRecordMaps(r *AuthorRecord, authorID string) {
	if r.EnrollNonces == nil {
		r.EnrollNonces = map[string]NonceUse{}
	}
	if r.ControlNonces == nil {
		r.ControlNonces = map[string]string{}
	}
	if r.Revoked == nil {
		r.Revoked = map[string]time.Time{}
	}
	if r.State.AuthorID == "" {
		r.State = wire.AuthorizationEpochState{ProviderID: "", AuthorID: authorID, Status: "unregistered"}
	}
}

func pruneEnroll(r *AuthorRecord, now time.Time) {
	for nonce, entry := range r.EnrollNonces {
		if now.After(entry.NotAfter) {
			delete(r.EnrollNonces, nonce)
		}
	}
}

func (a *Authority) commitRecord(id string, r AuthorRecord) error {
	prev := r.Version
	r.Version++
	if err := a.store.Commit(id, prev, r); err != nil {
		return storeError(err)
	}
	return nil
}

func storeError(err error) error {
	if errors.Is(err, ErrStale) {
		return ErrStale
	}
	return ErrUncertain
}

// ErrorStatus maps authorization failures to the public API catalog.
func ErrorStatus(err error) (int, string) {
	switch {
	case err == nil:
		return 0, ""
	case errors.Is(err, ErrWrongAuthor):
		return 401, string(apierr.Unauthenticated)
	case errors.Is(err, ErrStale):
		return 409, string(apierr.StaleState)
	case errors.Is(err, ErrRequestConflict):
		return 409, string(apierr.RequestConflict)
	case errors.Is(err, ErrRevoked):
		return 403, string(apierr.DelegationRevoked)
	case errors.Is(err, ErrCapacity), errors.Is(err, ErrUncertain):
		return 503, string(apierr.Unavailable)
	default:
		return 422, string(apierr.BadAuthorization)
	}
}
