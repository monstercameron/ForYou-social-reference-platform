package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"foryou.local/foryou/internal/names"
)

const (
	nameidFileName  = "display-name.json"
	nameidFileLimit = 64 << 10
)

// nameidState is the profile's local display name state: the name kept on this
// device, the last record the profile signed and the outcomes of its last
// publication. It is local application state, not protocol state.
type nameidState struct {
	Local    string            `json:"local,omitempty"`
	Record   *names.NameRecord `json:"record,omitempty"`
	Pending  *names.NameRecord `json:"pending,omitempty"`
	Outcomes []nameidOutcome   `json:"outcomes,omitempty"`
}

// nameidOutcome is what one selected provider answered for the last record it was sent.
type nameidOutcome struct {
	Endpoint   string `json:"endpoint"`
	Label      string `json:"label"`
	Code       string `json:"code"`
	Message    string `json:"message,omitempty"`
	RetryAfter int    `json:"retry_after_seconds,omitempty"`
	Ambiguous  bool   `json:"ambiguous,omitempty"`
}

// nameidLoad reads the profile's display name state. A profile without the file has an empty state.
func (a *App) nameidLoad(profile string) (nameidState, error) {
	if !validProfileID(profile) {
		return nameidState{}, errors.New("invalid profile id")
	}
	lock := a.profiles.lock(profile)
	lock.Lock()
	defer lock.Unlock()
	return a.nameidRead(profile)
}

// nameidUpdate applies change to the profile's display name state and writes the result atomically.
func (a *App) nameidUpdate(profile string, change func(*nameidState)) error {
	if !validProfileID(profile) {
		return errors.New("invalid profile id")
	}
	lock := a.profiles.lock(profile)
	lock.Lock()
	defer lock.Unlock()
	state, err := a.nameidRead(profile)
	if err != nil {
		return err
	}
	change(&state)
	return a.nameidWrite(profile, state)
}

func (a *App) nameidRead(profile string) (nameidState, error) {
	file, err := os.Open(filepath.Join(a.profiles.root, profile, nameidFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nameidState{}, nil
	}
	if err != nil {
		return nameidState{}, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, nameidFileLimit+1))
	if err != nil {
		return nameidState{}, err
	}
	if len(data) > nameidFileLimit {
		return nameidState{}, errors.New("display name file exceeds its size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var state nameidState
	if err := decoder.Decode(&state); err != nil {
		return nameidState{}, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return nameidState{}, errors.New("display name file holds more than one value")
	}
	return state, nil
}

func (a *App) nameidWrite(profile string, state nameidState) error {
	dir := filepath.Join(a.profiles.root, profile)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".display-name-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return err
	}
	if err := json.NewEncoder(temporary).Encode(state); err != nil {
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
	return os.Rename(temporaryName, filepath.Join(dir, nameidFileName))
}
