package server

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"foryou.local/foryou/internal/keys"
)

const reportListLimit = 100

// reportEntry is one report this browser sent, without its secret or statement.
type reportEntry struct {
	MessageID        string `json:"message_id"`
	Kind             string `json:"kind"`
	ProviderEndpoint string `json:"provider_endpoint"`
	OperatorID       string `json:"operator_id"`
	RequestID        string `json:"request_id"`
	FormID           string `json:"form_id"`
	FormVersion      string `json:"form_version"`
	FormDigest       string `json:"form_digest"`
	RequestDigest    string `json:"request_digest"`
	ReceivedAt       string `json:"received_at"`
}

type reportFile struct {
	Version int           `json:"version"`
	Reports []reportEntry `json:"reports"`
}

// reportValidEntry reports whether a stored entry has the shape this client writes.
func reportValidEntry(entry reportEntry) bool {
	if !canonicalMessageID(entry.MessageID) {
		return false
	}
	if _, ok := reportKindByName(entry.Kind); !ok {
		return false
	}
	if _, err := keys.DecodeB64URL(entry.RequestID, 32); err != nil {
		return false
	}
	return reportLowerHex(entry.FormDigest) && reportLowerHex(entry.RequestDigest)
}

func reportLowerHex(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, c := range value {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// reportLoad reads the profile's list. A missing file is an empty list; a file
// that cannot be read or does not match the format is reported as damaged.
func (a *App) reportLoad(profile string) ([]reportEntry, bool) {
	if !validProfileID(profile) {
		return nil, false
	}
	file, err := os.Open(filepath.Join(a.profiles.root, profile, "reports.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false
	}
	if err != nil {
		return nil, true
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var stored reportFile
	if err := decoder.Decode(&stored); err != nil {
		return nil, true
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nil, true
	}
	if stored.Version != 1 {
		return nil, true
	}
	for _, entry := range stored.Reports {
		if !reportValidEntry(entry) {
			return nil, true
		}
	}
	return stored.Reports, false
}

// reportList returns the profile's reports, newest first, and whether the stored list was unreadable.
func (a *App) reportList(profile string) ([]reportEntry, bool) {
	if !validProfileID(profile) {
		return nil, false
	}
	lock := a.profiles.lock(profile)
	lock.Lock()
	defer lock.Unlock()
	return a.reportLoad(profile)
}

// reportAdd puts a verified receipt at the top of the profile's list. An
// unreadable list is replaced, as the check page says.
func (a *App) reportAdd(profile string, entry reportEntry) error {
	if !validProfileID(profile) {
		return errors.New("invalid profile id")
	}
	lock := a.profiles.lock(profile)
	lock.Lock()
	defer lock.Unlock()
	entries, _ := a.reportLoad(profile)
	entries = append([]reportEntry{entry}, entries...)
	if len(entries) > reportListLimit {
		entries = entries[:reportListLimit]
	}
	data, err := json.Marshal(reportFile{Version: 1, Reports: entries})
	if err != nil {
		return err
	}
	dir := filepath.Join(a.profiles.root, profile)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".reports-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryName, filepath.Join(dir, "reports.json"))
}
