package sdk

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/succession"
)

func TestKeystoreCreateReloadAndNameValidation(t *testing.T) {
	dir := t.TempDir()
	store, err := OpenKeystore(dir)
	if err != nil {
		t.Fatal(err)
	}
	created, err := store.Create("profile_1")
	if err != nil {
		t.Fatal(err)
	}
	priv, err := store.PrivateKey("profile_1")
	if err != nil {
		t.Fatal(err)
	}
	seed := priv.Seed()
	seedText := keys.EncodeB64URL(seed)
	identity, ok, err := store.Identity("profile_1")
	if err != nil || !ok || identity.DID != created.DID {
		t.Fatalf("Identity() = %+v, %t, %v; want created DID %q", identity, ok, err, created.DID)
	}

	t.Run("reopen keeps DID and timestamps", func(t *testing.T) {
		reopened, err := OpenKeystore(dir)
		if err != nil {
			t.Fatal(err)
		}
		reloaded, ok, err := reopened.Identity("profile_1")
		if err != nil || !ok || reloaded.DID != created.DID || !reloaded.CreatedAt.Equal(created.CreatedAt) {
			t.Fatalf("reopened Identity() = %+v, %t, %v; want created identity %+v", reloaded, ok, err, created)
		}
	})
	t.Run("identity file mode 0600", func(t *testing.T) {
		if runtime.GOOS == "windows" {
			t.Skip("Windows reports ACLs rather than Unix permission bits")
		}
		info, err := os.Stat(dir + string(os.PathSeparator) + "profile_1" + string(os.PathSeparator) + "identity.json")
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("identity file mode = %04o; want 0600", info.Mode().Perm())
		}
	})
	t.Run("second Create returns ErrIdentityExists", func(t *testing.T) {
		if _, err := store.Create("profile_1"); !errors.Is(err, ErrIdentityExists) {
			t.Fatalf("second Create error = %v; want ErrIdentityExists", err)
		}
	})
	t.Run("path traversal name rejected", func(t *testing.T) {
		if _, err := store.Create("../x"); err == nil {
			t.Fatal("Create accepted a path traversal name")
		}
	})
	t.Run("formatted identity has no seed", func(t *testing.T) {
		formattedIdentity := fmt.Sprintf("%+v", identity)
		if strings.Contains(formattedIdentity, seedText) || strings.Contains(formattedIdentity, string(seed)) {
			t.Fatal("formatted public identity exposed the seed")
		}
	})
}

func TestKeystoreRotate(t *testing.T) {
	store, err := OpenKeystore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	old, err := store.Create("author")
	if err != nil {
		t.Fatal(err)
	}
	successionRecord, err := store.Rotate("author", time.Date(2026, 11, 11, 12, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	current, ok, err := store.Identity("author")
	if err != nil || !ok {
		t.Fatalf("Identity() = %+v, %t, %v", current, ok, err)
	}
	t.Run("new DID differs", func(t *testing.T) {
		if current.DID == old.DID {
			t.Fatalf("rotation kept DID %q", old.DID)
		}
	})
	t.Run("succession verifies with old and new IDs", func(t *testing.T) {
		if err := succession.Verify(successionRecord); err != nil {
			t.Fatalf("succession.Verify() error = %v", err)
		}
		if successionRecord.OldID != old.DID || successionRecord.NewID != current.DID {
			t.Fatalf("succession IDs = (%q, %q); want (%q, %q)", successionRecord.OldID, successionRecord.NewID, old.DID, current.DID)
		}
	})
	t.Run("retired lists old DID and succession ID", func(t *testing.T) {
		if len(current.Retired) != 1 || current.Retired[0].DID != old.DID || current.Retired[0].SuccessionID != successionRecord.SuccessionID {
			t.Fatalf("Retired = %+v; want old DID and succession ID %q", current.Retired, successionRecord.SuccessionID)
		}
	})
	t.Run("backup confirmation cleared", func(t *testing.T) {
		if current.BackupConfirmedForCurrent {
			t.Fatal("rotation retained backup confirmation for the new key")
		}
	})
}
