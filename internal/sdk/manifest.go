package sdk

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

// ManifestStatus reports the result of checking a signed operator manifest.
type ManifestStatus int

const (
	// ManifestCurrent means a higher valid version was pinned.
	ManifestCurrent ManifestStatus = iota
	// ManifestSame means the pinned version and digest match.
	ManifestSame
	// ManifestRollback means a lower version was rejected.
	ManifestRollback
	// ManifestEquivocation means this key has conflicting bodies at one version or was already marked.
	ManifestEquivocation
	// ManifestExpired means the signed validity window is invalid or elapsed.
	ManifestExpired
	// ManifestBadSignature means bytes, binding, or signature verification failed.
	ManifestBadSignature
)

// String returns the stable manifest status name.
func (s ManifestStatus) String() string {
	switch s {
	case ManifestCurrent:
		return "current"
	case ManifestSame:
		return "same"
	case ManifestRollback:
		return "rollback"
	case ManifestEquivocation:
		return "equivocation"
	case ManifestExpired:
		return "expired"
	case ManifestBadSignature:
		return "bad_signature"
	default:
		return "unknown"
	}
}

type asManifestPin struct {
	Version int64  `json:"version"`
	Digest  string `json:"digest"`
}
type asManifestSnapshot struct {
	Pins          map[string]asManifestPin `json:"pins"`
	Equivocations []string                 `json:"equivocations"`
}

// ManifestPins persists the highest verified manifest per operator key and equivocation marks.
type ManifestPins struct {
	mu            sync.Mutex
	pins          map[string]asManifestPin
	equivocations map[string]bool
}

// NewManifestPins creates empty, concurrency-safe pin state.
func NewManifestPins() *ManifestPins {
	return &ManifestPins{pins: map[string]asManifestPin{}, equivocations: map[string]bool{}}
}

// Check verifies raw as exactly m and checks its signature, binding, time window, and monotonic pin. A manifest not bound to its bytes is treated as unsigned.
func (p *ManifestPins) Check(operatorKey string, raw []byte, m wire.OperatorManifest, now time.Time) ManifestStatus {
	if p == nil {
		return ManifestBadSignature
	}
	var decoded wire.OperatorManifest
	if wire.Decode(raw, "OperatorManifest", &decoded) != nil || !reflect.DeepEqual(decoded, m) || m.OperatorID != operatorKey {
		return ManifestBadSignature
	}
	tree, err := canon.Parse(raw, canon.Limits{MaxBytes: 1 << 20, MaxDepth: 32})
	if err != nil {
		return ManifestBadSignature
	}
	obj, ok := tree.(map[string]any)
	if !ok {
		return ManifestBadSignature
	}
	delete(obj, "signature")
	body, err := canon.Marshal(obj)
	if err != nil || keys.Verify(operatorKey, "operator-manifest", body, m.Signature) != nil {
		return ManifestBadSignature
	}
	digestBytes := sha256.Sum256(body)
	digest := hex.EncodeToString(digestBytes[:])
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pins == nil {
		p.pins = make(map[string]asManifestPin)
	}
	if p.equivocations == nil {
		p.equivocations = make(map[string]bool)
	}
	if p.equivocations[operatorKey] {
		return ManifestEquivocation
	}
	issued, err1 := wire.ParseTime(m.IssuedAt)
	expires, err2 := wire.ParseTime(m.ExpiresAt)
	if err1 != nil || err2 != nil || issued.After(now) || !now.Before(expires) || expires.Sub(issued) > 30*24*time.Hour {
		return ManifestExpired
	}
	if m.ManifestVersion < 1 {
		return ManifestBadSignature
	}
	previous, exists := p.pins[operatorKey]
	if exists {
		if m.ManifestVersion < previous.Version {
			return ManifestRollback
		}
		if m.ManifestVersion == previous.Version {
			if digest == previous.Digest {
				return ManifestSame
			}
			p.equivocations[operatorKey] = true
			return ManifestEquivocation
		}
	}
	p.pins[operatorKey] = asManifestPin{Version: m.ManifestVersion, Digest: digest}
	return ManifestCurrent
}

// Reset clears a pin or equivocation only on explicit user action.
func (p *ManifestPins) Reset(operatorKey string) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.pins, operatorKey)
	delete(p.equivocations, operatorKey)
}

// Pinned returns the highest pinned version and its signed-body digest.
func (p *ManifestPins) Pinned(operatorKey string) (version int, digest string, ok bool) {
	if p == nil {
		return 0, "", false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	pin, ok := p.pins[operatorKey]
	return int(pin.Version), pin.Digest, ok
}

// Snapshot returns canonical JSON including equivocation marks.
func (p *ManifestPins) Snapshot() ([]byte, error) {
	if p == nil {
		return nil, errors.New("sdk: nil manifest pins")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.pins == nil {
		p.pins = make(map[string]asManifestPin)
	}
	if p.equivocations == nil {
		p.equivocations = make(map[string]bool)
	}
	marks := []string{}
	for key := range p.equivocations {
		marks = append(marks, key)
	}
	sort.Strings(marks)
	return canon.Encode(asManifestSnapshot{Pins: p.pins, Equivocations: marks})
}

// RestoreManifestPins restores validated canonical pin state.
func RestoreManifestPins(data []byte) (*ManifestPins, error) {
	canonical, err := canon.Canonicalize(data, canon.Limits{MaxBytes: 1 << 20, MaxDepth: 32})
	if err != nil || !bytes.Equal(canonical, data) {
		return nil, errors.New("restore manifest pins: input is not canonical JSON")
	}
	var snap asManifestSnapshot
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&snap); err != nil {
		return nil, fmt.Errorf("restore manifest pins: %w", err)
	}
	if snap.Pins == nil || snap.Equivocations == nil {
		return nil, errors.New("restore manifest pins: missing state")
	}
	p := NewManifestPins()
	for key, pin := range snap.Pins {
		if _, err := keys.ParseDID(key); err != nil || pin.Version < 1 || len(pin.Digest) != 64 {
			return nil, errors.New("restore manifest pins: invalid pin")
		}
		if _, err := hex.DecodeString(pin.Digest); err != nil || strings.ToLower(pin.Digest) != pin.Digest {
			return nil, errors.New("restore manifest pins: invalid digest")
		}
		p.pins[key] = pin
	}
	for _, key := range snap.Equivocations {
		if _, err := keys.ParseDID(key); err != nil {
			return nil, errors.New("restore manifest pins: invalid equivocation key")
		}
		p.equivocations[key] = true
	}
	return p, nil
}
