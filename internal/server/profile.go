package server

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const profileCookie = "foryou_profile"

const (
	// ackKeyWarnings is set by identity and required by compose.
	ackKeyWarnings = "key-warnings"
	// ackPermanence records acknowledgment of publication permanence.
	ackPermanence = "permanence"
	// ackLinkability records acknowledgment of funding linkability.
	ackLinkability = "funding-linkability"
	// ackCustodyPrefix prefixes provider custody acknowledgments.
	ackCustodyPrefix = "custody:"
)

// ProfileSettings stores local application preferences, not protocol state.
type ProfileSettings struct {
	Providers        []ProviderChoice     `json:"providers,omitempty"`
	RemovedDefaults  []string             `json:"removed_defaults,omitempty"`
	Monitors         []MonitorChoice      `json:"monitors,omitempty"`
	MediaOrigins     []string             `json:"media_origins,omitempty"`
	Acknowledgements map[string]time.Time `json:"acknowledgements,omitempty"`
	PublishNetwork   string               `json:"publish_network,omitempty"`
}

// ProviderChoice stores one locally selected provider.
type ProviderChoice struct {
	Endpoint    string `json:"endpoint"`
	OperatorKey string `json:"operator_key"`
	Label       string `json:"label"`
	Source      string `json:"source"`
	Trusted     bool   `json:"trusted"`
}

// MonitorChoice stores one locally selected monitor.
type MonitorChoice struct {
	Endpoint  string `json:"endpoint"`
	MonitorID string `json:"monitor_id"`
}

type profileStore struct {
	root  string
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

func (a *App) profile(w http.ResponseWriter, r *http.Request) (string, error) {
	if cookie, err := r.Cookie(profileCookie); err == nil && validProfileID(cookie.Value) {
		return cookie.Value, nil
	}
	var random [32]byte
	if _, err := rand.Read(random[:]); err != nil {
		return "", err
	}
	id := base64.RawURLEncoding.EncodeToString(random[:])
	http.SetCookie(w, &http.Cookie{Name: profileCookie, Value: id, Path: "/", MaxAge: 31536000, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode})
	return id, nil
}

func validProfileID(id string) bool {
	decoded, err := base64.RawURLEncoding.DecodeString(id)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == id
}

func (a *App) settings(id string) (ProfileSettings, error) {
	if !validProfileID(id) {
		return ProfileSettings{}, errors.New("invalid profile id")
	}
	lock := a.profiles.lock(id)
	lock.Lock()
	defer lock.Unlock()
	return a.readSettings(id)
}

func (a *App) updateSettings(id string, update func(*ProfileSettings) error) error {
	if !validProfileID(id) {
		return errors.New("invalid profile id")
	}
	lock := a.profiles.lock(id)
	lock.Lock()
	defer lock.Unlock()
	settings, err := a.readSettings(id)
	if err != nil {
		return err
	}
	if err := update(&settings); err != nil {
		return err
	}
	dir := filepath.Join(a.profiles.root, id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".settings-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return err
	}
	if err := json.NewEncoder(temporary).Encode(settings); err != nil {
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
	if err := os.Rename(temporaryName, filepath.Join(dir, "settings.json")); err != nil {
		return fmt.Errorf("replace profile settings: %w", err)
	}
	return nil
}

func (a *App) readSettings(id string) (ProfileSettings, error) {
	path := filepath.Join(a.profiles.root, id, "settings.json")
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return ProfileSettings{}, nil
	}
	if err != nil {
		return ProfileSettings{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var settings ProfileSettings
	if err := decoder.Decode(&settings); err != nil {
		return ProfileSettings{}, err
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		if err == nil {
			return ProfileSettings{}, errors.New("multiple settings values")
		}
		return ProfileSettings{}, err
	}
	return settings, nil
}

func (s *profileStore) lock(id string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.locks == nil {
		s.locks = make(map[string]*sync.Mutex)
	}
	if s.locks[id] == nil {
		s.locks[id] = &sync.Mutex{}
	}
	return s.locks[id]
}
