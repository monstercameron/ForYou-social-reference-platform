package sdk

import (
	"bytes"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
	"unicode/utf8"

	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/keys"
	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

var (
	// ErrWeakPassphrase indicates that a passphrase has fewer than twelve Unicode characters.
	ErrWeakPassphrase = errors.New("sdk: passphrase too short")
	// ErrBackupInvalid deliberately hides whether a backup was malformed or opened with the wrong passphrase.
	ErrBackupInvalid = errors.New("sdk: backup cannot be opened")
	ksKDFParams      = ksKDFParameters{Time: 3, MemoryKiB: 65536, Threads: 1}
)

type ksKDFParameters struct {
	Time      uint32
	MemoryKiB uint32
	Threads   uint8
}

type ksBackupKDF struct {
	Name      string `json:"name"`
	Time      uint32 `json:"time"`
	MemoryKiB uint32 `json:"memory_kib"`
	Threads   uint8  `json:"threads"`
	Salt      string `json:"salt_b64url"`
}

type ksBackupFile struct {
	Format     string      `json:"format"`
	KDF        ksBackupKDF `json:"kdf"`
	Nonce      string      `json:"nonce_b64url"`
	Ciphertext string      `json:"ciphertext_b64url"`
	DID        string      `json:"did"`
}

type ksBackupPlaintext struct {
	Current ksStoredKey          `json:"current"`
	Retired []ksRetiredStoredKey `json:"retired"`
}

// ExportBackup creates a local encrypted file format, not a protocol object; it is dev custody, with unencrypted seeds stored on the client server, and must never protect real funds or identities.
func (k *Keystore) ExportBackup(name, passphrase string) ([]byte, error) {
	if !ksNamePattern.MatchString(name) {
		return nil, errors.New("sdk: invalid identity name")
	}
	if utf8.RuneCountInString(passphrase) < 12 {
		return nil, ErrWeakPassphrase
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
	plain, err := canon.Encode(ksBackupPlaintext{Current: record.Current, Retired: record.Retired})
	if err != nil {
		return nil, errors.New("sdk: encode backup")
	}
	defer ksZero(plain)
	salt := make([]byte, 16)
	nonce := make([]byte, chacha20poly1305.NonceSizeX)
	if _, err := rand.Read(salt); err != nil {
		return nil, errors.New("sdk: create backup")
	}
	if _, err := rand.Read(nonce); err != nil {
		return nil, errors.New("sdk: create backup")
	}
	passphraseBytes := []byte(passphrase)
	defer ksZero(passphraseBytes)
	derived := argon2.IDKey(passphraseBytes, salt, ksKDFParams.Time, ksKDFParams.MemoryKiB, ksKDFParams.Threads, chacha20poly1305.KeySize)
	defer ksZero(derived)
	aead, err := chacha20poly1305.NewX(derived)
	if err != nil {
		return nil, errors.New("sdk: create backup")
	}
	sealed := aead.Seal(nil, nonce, plain, []byte("foryou-key-backup-1\n"+record.Current.DID))
	file := ksBackupFile{Format: "foryou-key-backup-1", KDF: ksBackupKDF{Name: "argon2id", Time: ksKDFParams.Time, MemoryKiB: ksKDFParams.MemoryKiB, Threads: ksKDFParams.Threads, Salt: keys.EncodeB64URL(salt)}, Nonce: keys.EncodeB64URL(nonce), Ciphertext: keys.EncodeB64URL(sealed), DID: record.Current.DID}
	encoded, err := canon.Encode(file)
	if err != nil {
		return nil, errors.New("sdk: encode backup")
	}
	return encoded, nil
}

// VerifyBackup restores and checks a dev-custody backup in memory without changing a stored identity. Seeds remain unencrypted on the client server, so this must never protect real funds or identities and is not recovery for a compromised identity.
func VerifyBackup(data []byte, passphrase string) (string, error) {
	file, plain, err := ksOpenBackup(data, passphrase)
	if err != nil {
		return "", ErrBackupInvalid
	}
	defer ksZero(plain)
	backup, err := ksDecodeBackupPlaintext(plain)
	if err != nil {
		return "", ErrBackupInvalid
	}
	did, err := ksCheckBackupPlaintext(backup)
	if err != nil || subtle.ConstantTimeCompare([]byte(did), []byte(file.DID)) != 1 {
		return "", ErrBackupInvalid
	}
	return did, nil
}

// ConfirmBackup marks the matching dev-custody backup as locally restored; seeds remain unencrypted on the client server and must never protect real funds or identities.
func (k *Keystore) ConfirmBackup(name string, data []byte, passphrase string) error {
	did, err := VerifyBackup(data, passphrase)
	if err != nil {
		return ErrBackupInvalid
	}
	if !ksNamePattern.MatchString(name) {
		return errors.New("sdk: invalid identity name")
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	path := filepath.Join(k.dir, name, "identity.json")
	record, err := ksReadIdentity(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrNoIdentity
		}
		return err
	}
	if subtle.ConstantTimeCompare([]byte(did), []byte(record.Current.DID)) != 1 {
		return ErrBackupInvalid
	}
	record.BackupConfirmedAt = ksNow()
	record.BackupConfirmedForDID = did
	return ksWriteIdentity(path, record)
}

// ImportBackup restores a user-controlled dev-custody backup to an empty profile; this cannot recover a compromised identity and the server stores seeds unencrypted.
func (k *Keystore) ImportBackup(name string, data []byte, passphrase string) (Identity, error) {
	if !ksNamePattern.MatchString(name) {
		return Identity{}, errors.New("sdk: invalid identity name")
	}
	file, plain, err := ksOpenBackup(data, passphrase)
	if err != nil {
		return Identity{}, ErrBackupInvalid
	}
	defer ksZero(plain)
	backup, err := ksDecodeBackupPlaintext(plain)
	if err != nil {
		return Identity{}, ErrBackupInvalid
	}
	did, err := ksCheckBackupPlaintext(backup)
	if err != nil || subtle.ConstantTimeCompare([]byte(did), []byte(file.DID)) != 1 {
		return Identity{}, ErrBackupInvalid
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
	record := ksIdentityFile{Format: "foryou-dev-keystore-1", Current: backup.Current, Retired: backup.Retired, BackupConfirmedAt: ksNow(), BackupConfirmedForDID: did}
	if err := ksWriteIdentity(path, record); err != nil {
		return Identity{}, err
	}
	identity, err := ksPublicIdentity(record)
	if err != nil {
		return Identity{}, err
	}
	identity.BackupConfirmedForCurrent = true
	return identity, nil
}

func ksOpenBackup(data []byte, passphrase string) (ksBackupFile, []byte, error) {
	if len(data) == 0 || len(data) > 16<<20 || utf8.RuneCountInString(passphrase) < 12 {
		return ksBackupFile{}, nil, ErrBackupInvalid
	}
	canonical, err := canon.Canonicalize(data, canon.Limits{MaxBytes: 16 << 20, MaxDepth: 8})
	if err != nil || !bytes.Equal(canonical, data) {
		return ksBackupFile{}, nil, ErrBackupInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var file ksBackupFile
	if decoder.Decode(&file) != nil || decoder.Decode(new(any)) != io.EOF || file.Format != "foryou-key-backup-1" || file.KDF.Name != "argon2id" || file.DID == "" || file.KDF.Time < 1 || file.KDF.Time > 10 || file.KDF.MemoryKiB < 8192 || file.KDF.MemoryKiB > 262144 || file.KDF.Threads < 1 || file.KDF.Threads > 4 {
		return ksBackupFile{}, nil, ErrBackupInvalid
	}
	salt, err := keys.DecodeB64URL(file.KDF.Salt, 16)
	if err != nil {
		return ksBackupFile{}, nil, ErrBackupInvalid
	}
	nonce, err := keys.DecodeB64URL(file.Nonce, chacha20poly1305.NonceSizeX)
	if err != nil {
		return ksBackupFile{}, nil, ErrBackupInvalid
	}
	ciphertext, err := base64.RawURLEncoding.DecodeString(file.Ciphertext)
	if err != nil || base64.RawURLEncoding.EncodeToString(ciphertext) != file.Ciphertext || len(ciphertext) < chacha20poly1305.Overhead {
		return ksBackupFile{}, nil, ErrBackupInvalid
	}
	passphraseBytes := []byte(passphrase)
	defer ksZero(passphraseBytes)
	derived := argon2.IDKey(passphraseBytes, salt, file.KDF.Time, file.KDF.MemoryKiB, file.KDF.Threads, chacha20poly1305.KeySize)
	defer ksZero(derived)
	aead, err := chacha20poly1305.NewX(derived)
	if err != nil {
		return ksBackupFile{}, nil, ErrBackupInvalid
	}
	plain, err := aead.Open(nil, nonce, ciphertext, []byte("foryou-key-backup-1\n"+file.DID))
	if err != nil {
		return ksBackupFile{}, nil, ErrBackupInvalid
	}
	return file, plain, nil
}

func ksDecodeBackupPlaintext(data []byte) (ksBackupPlaintext, error) {
	canonical, err := canon.Canonicalize(data, canon.Limits{MaxBytes: 16 << 20, MaxDepth: 8})
	if err != nil || !bytes.Equal(canonical, data) {
		return ksBackupPlaintext{}, ErrBackupInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var backup ksBackupPlaintext
	if decoder.Decode(&backup) != nil || decoder.Decode(new(any)) != io.EOF {
		return ksBackupPlaintext{}, ErrBackupInvalid
	}
	return backup, nil
}

func ksCheckBackupPlaintext(backup ksBackupPlaintext) (string, error) {
	if len(backup.Current.DID) == 0 {
		return "", ErrBackupInvalid
	}
	if err := ksValidateStoredKey(backup.Current); err != nil {
		return "", ErrBackupInvalid
	}
	for _, retired := range backup.Retired {
		if err := ksValidateStoredKey(retired.ksStoredKey); err != nil || retired.RetiredAt == "" || retired.SuccessionID == "" {
			return "", ErrBackupInvalid
		}
		if _, err := time.Parse(time.RFC3339Nano, retired.RetiredAt); err != nil {
			return "", ErrBackupInvalid
		}
	}
	return backup.Current.DID, nil
}

func ksValidateStoredKey(stored ksStoredKey) error {
	seed, err := keys.DecodeB64URL(stored.SeedB64URL, 32)
	if err != nil {
		return ErrBackupInvalid
	}
	priv, err := keys.NewKeyFromSeed(seed)
	if err != nil || subtle.ConstantTimeCompare([]byte(keys.DIDOf(priv)), []byte(stored.DID)) != 1 {
		ksZero(seed)
		return ErrBackupInvalid
	}
	if _, err := time.Parse(time.RFC3339Nano, stored.CreatedAt); err != nil {
		ksZero(seed)
		return ErrBackupInvalid
	}
	ksZero(seed)
	return nil
}

func ksNow() string { return time.Now().UTC().Format(time.RFC3339Nano) }
