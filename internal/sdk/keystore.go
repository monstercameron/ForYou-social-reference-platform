package sdk

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/succession"
	"foryou.local/foryou/internal/wire"
)

var (
	// ErrIdentityExists indicates that a profile already has an identity.
	ErrIdentityExists = errors.New("sdk: identity already exists")
	// ErrNoIdentity indicates that a profile has no identity.
	ErrNoIdentity = errors.New("sdk: no identity")
	ksNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
)

type ksStoredKey struct {
	DID        string `json:"did"`
	SeedB64URL string `json:"seed_b64url"`
	CreatedAt  string `json:"created_at"`
}

type ksRetiredStoredKey struct {
	ksStoredKey
	RetiredAt    string `json:"retired_at"`
	SuccessionID string `json:"succession_id"`
}

type ksIdentityFile struct {
	Format                string               `json:"format"`
	Current               ksStoredKey          `json:"current"`
	Retired               []ksRetiredStoredKey `json:"retired"`
	BackupConfirmedAt     string               `json:"backup_confirmed_at"`
	BackupConfirmedForDID string               `json:"backup_confirmed_did"`
}

// RetiredKey describes a former public identity and its advisory succession.
type RetiredKey struct {
	DID          string
	RetiredAt    time.Time
	SuccessionID string
}

// Identity contains public identity state and backup confirmation information.
type Identity struct {
	DID                       string
	CreatedAt                 time.Time
	Retired                   []RetiredKey
	BackupConfirmedAt         time.Time
	BackupConfirmedForCurrent bool
}

// Keystore stores dev author seeds unencrypted on the client server; this is dev custody only and must never protect real funds or identities.
type Keystore struct {
	dir string
	mu  sync.Mutex
}

// OpenKeystore opens a file-backed dev keystore, creating its directory with mode 0700 when missing.
func OpenKeystore(dir string) (*Keystore, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("sdk: open keystore: %w", err)
	}
	return &Keystore{dir: dir}, nil
}

// Create generates a dev author identity for a validated profile name.
func (k *Keystore) Create(name string) (Identity, error) {
	if !ksNamePattern.MatchString(name) {
		return Identity{}, errors.New("sdk: invalid identity name")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	dir := filepath.Join(k.dir, name)
	path := filepath.Join(dir, "identity.json")
	if _, err := os.Stat(path); err == nil {
		return Identity{}, ErrIdentityExists
	} else if !errors.Is(err, os.ErrNotExist) {
		return Identity{}, fmt.Errorf("sdk: inspect identity: %w", err)
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return Identity{}, fmt.Errorf("sdk: create identity directory: %w", err)
	}
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		return Identity{}, fmt.Errorf("sdk: generate identity: %w", err)
	}
	defer ksZero(seed)
	priv, err := keys.NewKeyFromSeed(seed)
	if err != nil {
		return Identity{}, errors.New("sdk: create identity")
	}
	defer ksZero(priv)
	now := time.Now().UTC()
	record := ksIdentityFile{Format: "foryou-dev-keystore-1", Current: ksStoredKey{DID: keys.DIDOf(priv), SeedB64URL: keys.EncodeB64URL(seed), CreatedAt: now.Format(time.RFC3339Nano)}}
	if err := ksWriteIdentity(path, record); err != nil {
		return Identity{}, err
	}
	identity, err := ksPublicIdentity(record)
	if err != nil {
		return Identity{}, err
	}
	return identity, nil
}

// Identity returns only public identity data for a validated profile name.
func (k *Keystore) Identity(name string) (Identity, bool, error) {
	if !ksNamePattern.MatchString(name) {
		return Identity{}, false, errors.New("sdk: invalid identity name")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	record, err := ksReadIdentity(filepath.Join(k.dir, name, "identity.json"))
	if errors.Is(err, os.ErrNotExist) {
		return Identity{}, false, nil
	}
	if err != nil {
		return Identity{}, false, err
	}
	identity, err := ksPublicIdentity(record)
	return identity, err == nil, err
}

// PrivateKey returns the current dev signing key for a validated profile name.
func (k *Keystore) PrivateKey(name string) (keys.PrivateKey, error) {
	if !ksNamePattern.MatchString(name) {
		return nil, errors.New("sdk: invalid identity name")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	record, err := ksReadIdentity(filepath.Join(k.dir, name, "identity.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNoIdentity
		}
		return nil, err
	}
	seed, err := keys.DecodeB64URL(record.Current.SeedB64URL, 32)
	if err != nil {
		return nil, errors.New("sdk: invalid identity data")
	}
	defer ksZero(seed)
	priv, err := keys.NewKeyFromSeed(seed)
	if err != nil || subtle.ConstantTimeCompare([]byte(keys.DIDOf(priv)), []byte(record.Current.DID)) != 1 {
		ksZero(priv)
		return nil, errors.New("sdk: invalid identity data")
	}
	return priv, nil
}

// Rotate creates and verifies an advisory succession, not recovery; publishing the succession is the caller's job.
func (k *Keystore) Rotate(name string, effectiveAt time.Time) (wire.KeySuccession, error) {
	if !ksNamePattern.MatchString(name) {
		return wire.KeySuccession{}, errors.New("sdk: invalid identity name")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	path := filepath.Join(k.dir, name, "identity.json")
	record, err := ksReadIdentity(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return wire.KeySuccession{}, ErrNoIdentity
		}
		return wire.KeySuccession{}, err
	}
	oldSeed, err := keys.DecodeB64URL(record.Current.SeedB64URL, 32)
	if err != nil {
		return wire.KeySuccession{}, errors.New("sdk: invalid identity data")
	}
	defer ksZero(oldSeed)
	oldKey, err := keys.NewKeyFromSeed(oldSeed)
	if err != nil || subtle.ConstantTimeCompare([]byte(keys.DIDOf(oldKey)), []byte(record.Current.DID)) != 1 {
		ksZero(oldKey)
		return wire.KeySuccession{}, errors.New("sdk: invalid identity data")
	}
	defer ksZero(oldKey)
	newSeed := make([]byte, 32)
	if _, err := rand.Read(newSeed); err != nil {
		return wire.KeySuccession{}, fmt.Errorf("sdk: generate identity: %w", err)
	}
	defer ksZero(newSeed)
	newKey, err := keys.NewKeyFromSeed(newSeed)
	if err != nil {
		return wire.KeySuccession{}, errors.New("sdk: create identity")
	}
	defer ksZero(newKey)
	s, err := succession.New(oldKey, newKey, effectiveAt)
	if err != nil || succession.Verify(s) != nil {
		return wire.KeySuccession{}, errors.New("sdk: create succession")
	}
	record.Retired = append(record.Retired, ksRetiredStoredKey{ksStoredKey: record.Current, RetiredAt: s.EffectiveAt, SuccessionID: s.SuccessionID})
	record.Current = ksStoredKey{DID: s.NewID, SeedB64URL: keys.EncodeB64URL(newSeed), CreatedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	record.BackupConfirmedAt, record.BackupConfirmedForDID = "", ""
	if err := ksWriteIdentity(path, record); err != nil {
		return wire.KeySuccession{}, err
	}
	return s, nil
}

func ksWriteIdentity(path string, record ksIdentityFile) error {
	data, err := json.Marshal(record)
	if err != nil {
		return errors.New("sdk: encode identity")
	}
	if err := ksAtomicWrite(path, data); err != nil {
		return fmt.Errorf("sdk: write identity: %w", err)
	}
	return nil
}

func ksReadIdentity(path string) (ksIdentityFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ksIdentityFile{}, err
	}
	var record ksIdentityFile
	if json.Unmarshal(data, &record) != nil || record.Format != "foryou-dev-keystore-1" || record.Current.DID == "" {
		return ksIdentityFile{}, errors.New("sdk: invalid identity data")
	}
	seed, err := keys.DecodeB64URL(record.Current.SeedB64URL, 32)
	if err != nil {
		return ksIdentityFile{}, errors.New("sdk: invalid identity data")
	}
	defer ksZero(seed)
	priv, err := keys.NewKeyFromSeed(seed)
	if err != nil || subtle.ConstantTimeCompare([]byte(keys.DIDOf(priv)), []byte(record.Current.DID)) != 1 {
		ksZero(priv)
		return ksIdentityFile{}, errors.New("sdk: invalid identity data")
	}
	ksZero(priv)
	return record, nil
}

func ksPublicIdentity(record ksIdentityFile) (Identity, error) {
	created, err := time.Parse(time.RFC3339Nano, record.Current.CreatedAt)
	if err != nil {
		return Identity{}, errors.New("sdk: invalid identity data")
	}
	identity := Identity{DID: record.Current.DID, CreatedAt: created}
	for _, retired := range record.Retired {
		t, err := time.Parse(time.RFC3339Nano, retired.RetiredAt)
		if err != nil {
			return Identity{}, errors.New("sdk: invalid identity data")
		}
		identity.Retired = append(identity.Retired, RetiredKey{DID: retired.DID, RetiredAt: t, SuccessionID: retired.SuccessionID})
	}
	if record.BackupConfirmedAt != "" {
		identity.BackupConfirmedAt, err = time.Parse(time.RFC3339Nano, record.BackupConfirmedAt)
		if err != nil {
			return Identity{}, errors.New("sdk: invalid identity data")
		}
		identity.BackupConfirmedForCurrent = subtle.ConstantTimeCompare([]byte(record.BackupConfirmedForDID), []byte(record.Current.DID)) == 1
	}
	return identity, nil
}

func ksAtomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	file, err := os.CreateTemp(dir, ".identity-*")
	if err != nil {
		return err
	}
	temp := file.Name()
	defer os.Remove(temp)
	if err := file.Chmod(0600); err != nil {
		file.Close()
		return err
	}
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(temp, path)
}

func ksZero(data []byte) {
	for i := range data {
		data[i] = 0
	}
}
