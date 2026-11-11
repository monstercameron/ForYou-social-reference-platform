package sdk

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"
)

func ksTestOpen(t *testing.T) *Keystore {
	t.Helper()
	store, err := OpenKeystore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func ksTestFastKDF(t *testing.T) {
	t.Helper()
	previous := ksKDFParams
	ksKDFParams = ksKDFParameters{Time: 1, MemoryKiB: 8192, Threads: 1}
	t.Cleanup(func() { ksKDFParams = previous })
}

func TestBackupRoundTripAndInvalidInputs(t *testing.T) {
	ksTestFastKDF(t)
	store := ksTestOpen(t)
	if _, err := store.Create("backup"); err != nil {
		t.Fatal(err)
	}
	other, err := store.Create("other")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Rotate("backup", time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	const passphrase = "correct horse battery staple"
	data, err := store.ExportBackup("backup", passphrase)
	if err != nil {
		t.Fatal(err)
	}
	identity, ok, err := store.Identity("backup")
	if err != nil || !ok {
		t.Fatalf("Identity() = %+v, %t, %v", identity, ok, err)
	}
	if got, err := VerifyBackup(data, passphrase); err != nil || got != identity.DID {
		t.Fatalf("VerifyBackup() = %q, %v; want %q", got, err, identity.DID)
	}
	t.Run("wrong passphrase", func(t *testing.T) {
		if _, err := VerifyBackup(data, "wrong horse battery staple"); !errors.Is(err, ErrBackupInvalid) {
			t.Fatalf("VerifyBackup error = %v; want ErrBackupInvalid", err)
		}
	})
	t.Run("modified ciphertext", func(t *testing.T) {
		var file map[string]any
		if err := json.Unmarshal(data, &file); err != nil {
			t.Fatal(err)
		}
		ciphertext, err := base64.RawURLEncoding.DecodeString(file["ciphertext_b64url"].(string))
		if err != nil {
			t.Fatal(err)
		}
		ciphertext[0] ^= 1
		file["ciphertext_b64url"] = base64.RawURLEncoding.EncodeToString(ciphertext)
		altered, err := json.Marshal(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyBackup(altered, passphrase); !errors.Is(err, ErrBackupInvalid) {
			t.Fatalf("VerifyBackup error = %v; want ErrBackupInvalid", err)
		}
	})
	t.Run("edited DID binds authenticated data", func(t *testing.T) {
		var file map[string]any
		if err := json.Unmarshal(data, &file); err != nil {
			t.Fatal(err)
		}
		file["did"] = other.DID
		altered, err := json.Marshal(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyBackup(altered, passphrase); !errors.Is(err, ErrBackupInvalid) {
			t.Fatalf("VerifyBackup error = %v; want ErrBackupInvalid", err)
		}
	})
	t.Run("weak passphrase", func(t *testing.T) {
		if _, err := store.ExportBackup("backup", "12345678901"); !errors.Is(err, ErrWeakPassphrase) {
			t.Fatalf("ExportBackup error = %v; want ErrWeakPassphrase", err)
		}
	})
	t.Run("KDF memory limit checked", func(t *testing.T) {
		var file map[string]any
		if err := json.Unmarshal(data, &file); err != nil {
			t.Fatal(err)
		}
		kdf := file["kdf"].(map[string]any)
		kdf["memory_kib"] = float64(1048576)
		altered, err := json.Marshal(file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := VerifyBackup(altered, passphrase); !errors.Is(err, ErrBackupInvalid) {
			t.Fatalf("VerifyBackup error = %v; want ErrBackupInvalid", err)
		}
	})
}

func TestBackupConfirmationAndImport(t *testing.T) {
	ksTestFastKDF(t)
	store := ksTestOpen(t)
	first, err := store.Create("first")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create("other"); err != nil {
		t.Fatal(err)
	}
	const passphrase = "correct horse battery staple"
	backup, err := store.ExportBackup("first", passphrase)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("confirm with another identity's backup fails", func(t *testing.T) {
		otherBackup, err := store.ExportBackup("other", passphrase)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.ConfirmBackup("first", otherBackup, passphrase); !errors.Is(err, ErrBackupInvalid) {
			t.Fatalf("ConfirmBackup(first) error = %v; want ErrBackupInvalid", err)
		}
	})
	t.Run("confirm sets BackupConfirmedForCurrent", func(t *testing.T) {
		if err := store.ConfirmBackup("first", backup, passphrase); err != nil {
			t.Fatal(err)
		}
		confirmed, ok, err := store.Identity("first")
		if err != nil || !ok || !confirmed.BackupConfirmedForCurrent || confirmed.BackupConfirmedAt.IsZero() {
			t.Fatalf("confirmed identity = %+v, %t, %v", confirmed, ok, err)
		}
		if confirmed.DID != first.DID {
			t.Fatal("confirmation changed the identity")
		}
	})
	if _, err := store.Rotate("first", time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC)); err != nil {
		t.Fatal(err)
	}
	rotated, ok, err := store.Identity("first")
	if err != nil || !ok || rotated.BackupConfirmedForCurrent {
		t.Fatalf("rotated identity = %+v, %t, %v; want backup confirmation cleared", rotated, ok, err)
	}
	rotatedBackup, err := store.ExportBackup("first", passphrase)
	if err != nil {
		t.Fatal(err)
	}
	restoredStore := ksTestOpen(t)
	t.Run("import restores DID and retired keys", func(t *testing.T) {
		imported, err := restoredStore.ImportBackup("restored", rotatedBackup, passphrase)
		if err != nil {
			t.Fatal(err)
		}
		if imported.DID != rotated.DID || len(imported.Retired) != 1 || imported.Retired[0].DID != first.DID || !imported.BackupConfirmedForCurrent {
			t.Fatalf("imported identity = %+v; want current DID %q and retired DID %q", imported, rotated.DID, first.DID)
		}
	})
	t.Run("second import returns ErrIdentityExists", func(t *testing.T) {
		if _, err := restoredStore.ImportBackup("restored", rotatedBackup, passphrase); !errors.Is(err, ErrIdentityExists) {
			t.Fatalf("second ImportBackup error = %v; want ErrIdentityExists", err)
		}
	})
}

func TestBackupDefaultKDFParameters(t *testing.T) {
	store := ksTestOpen(t)
	identity, err := store.Create("default")
	if err != nil {
		t.Fatal(err)
	}
	const passphrase = "default parameters passphrase"
	backup, err := store.ExportBackup("default", passphrase)
	if err != nil {
		t.Fatal(err)
	}
	var file ksBackupFile
	if err := json.Unmarshal(backup, &file); err != nil {
		t.Fatal(err)
	}
	if file.Format != "foryou-key-backup-1" || file.KDF.Name != "argon2id" || file.KDF.Time != 3 || file.KDF.MemoryKiB != 65536 || file.KDF.Threads != 1 || file.DID != identity.DID {
		t.Fatalf("backup metadata = %+v; want specified format, KDF and DID", file)
	}
	salt, err := base64.RawURLEncoding.DecodeString(file.KDF.Salt)
	if err != nil || len(salt) != 16 {
		t.Fatalf("salt length = %d, %v; want 16 bytes", len(salt), err)
	}
	nonce, err := base64.RawURLEncoding.DecodeString(file.Nonce)
	if err != nil || len(nonce) != 24 {
		t.Fatalf("nonce length = %d, %v; want 24 bytes", len(nonce), err)
	}
	if got, err := VerifyBackup(backup, passphrase); err != nil || got != identity.DID {
		t.Fatalf("VerifyBackup(default KDF) = %q, %v; want %q", got, err, identity.DID)
	}
}

func TestBackupUsesFreshSaltNonceAndCiphertext(t *testing.T) {
	ksTestFastKDF(t)
	store := ksTestOpen(t)
	if _, err := store.Create("fresh"); err != nil {
		t.Fatal(err)
	}
	const passphrase = "same passphrase for both backups"
	first, err := store.ExportBackup("fresh", passphrase)
	if err != nil {
		t.Fatal(err)
	}
	second, err := store.ExportBackup("fresh", passphrase)
	if err != nil {
		t.Fatal(err)
	}
	var firstFile, secondFile ksBackupFile
	if err := json.Unmarshal(first, &firstFile); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(second, &secondFile); err != nil {
		t.Fatal(err)
	}
	if firstFile.KDF.Salt == secondFile.KDF.Salt {
		t.Fatal("two exports reused a salt")
	}
	if firstFile.Nonce == secondFile.Nonce {
		t.Fatal("two exports reused a nonce")
	}
	if firstFile.Ciphertext == secondFile.Ciphertext {
		t.Fatal("two exports produced the same ciphertext")
	}
}
