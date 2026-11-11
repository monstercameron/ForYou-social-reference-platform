package sdk

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFileJournal(t *testing.T) {
	t.Run("persists a private entry across opens", func(t *testing.T) {
		dir := t.TempDir()
		journal, err := FileJournal(dir)
		if err != nil {
			t.Fatal(err)
		}
		entry := JournalEntry{MessageID: "fy1:" + strings.Repeat("a", 64), State: "prepared", ProviderEndpoint: "https://provider.example"}
		if err := journal.Put(entry); err != nil {
			t.Fatal(err)
		}
		entry.State = "quoted"
		if err := journal.Put(entry); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(filepath.Join(dir, entry.MessageID[4:]+".json"))
		if err != nil {
			t.Fatal(err)
		}
		if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
			t.Fatalf("journal file permissions = %o; want 600", info.Mode().Perm())
		}
		reopened, err := FileJournal(dir)
		if err != nil {
			t.Fatal(err)
		}
		got, ok, err := reopened.Get(entry.MessageID)
		if err != nil || !ok || got.State != "quoted" || got.ProviderEndpoint != entry.ProviderEndpoint {
			t.Fatalf("Get = %#v, %v, %v", got, ok, err)
		}
		entries, err := reopened.List()
		if err != nil || len(entries) != 1 {
			t.Fatalf("List = %#v, %v", entries, err)
		}
		if missing, ok, err := reopened.Get("fy1:" + strings.Repeat("b", 64)); err != nil || ok || missing.MessageID != "" {
			t.Fatalf("missing Get = %#v, %v, %v; want zero entry and ok=false", missing, ok, err)
		}
	})
}
