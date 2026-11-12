package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/names"
)

const (
	namesPetnameFile  = "petnames.json"
	namesPetnameLimit = 1000
	namesPetnameBytes = 512 << 10
)

func init() { routeGroups = append(routeGroups, registerNamesPetnames) }

func registerNamesPetnames(a *App, mux *http.ServeMux) {
	mux.HandleFunc("POST /authors/{did}/petname", a.namesPetnameHandler)
	mux.HandleFunc("POST /authors/{did}/petname/fragment", a.namesPetnameHandler)
	mux.HandleFunc("POST /authors/{did}/petname/clear", a.namesPetnameClearHandler)
	mux.HandleFunc("POST /authors/{did}/petname/clear/fragment", a.namesPetnameClearHandler)
}

// petnames returns the reader's petnames for one profile, keyed by author key. The
// file is local application state, never protocol state, and is never published.
func (a *App) petnames(profile string) (map[string]string, error) {
	if !validProfileID(profile) {
		return nil, errors.New("invalid profile id")
	}
	lock := a.profiles.lock(profile)
	lock.Lock()
	defer lock.Unlock()
	return a.readPetnames(profile)
}

func (a *App) readPetnames(profile string) (map[string]string, error) {
	path := filepath.Join(a.profiles.root, profile, namesPetnameFile)
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, err
	}
	if info.Size() > namesPetnameBytes {
		return nil, errors.New("petname file is too large")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	petnames := map[string]string{}
	if err := decoder.Decode(&petnames); err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("multiple petname values")
	}
	if len(petnames) > namesPetnameLimit {
		return nil, errors.New("too many petnames")
	}
	for author, petname := range petnames {
		if _, err := keys.ParseDID(author); err != nil {
			return nil, err
		}
		if _, err := names.NormalizeName(petname); err != nil {
			return nil, err
		}
	}
	return petnames, nil
}

// updatePetnames applies update to the reader's petnames and replaces the file atomically.
func (a *App) updatePetnames(profile string, update func(map[string]string) error) error {
	if !validProfileID(profile) {
		return errors.New("invalid profile id")
	}
	lock := a.profiles.lock(profile)
	lock.Lock()
	defer lock.Unlock()
	petnames, err := a.readPetnames(profile)
	if err != nil {
		return err
	}
	if err := update(petnames); err != nil {
		return err
	}
	if len(petnames) > namesPetnameLimit {
		return errNamesPetnameFull
	}
	dir := filepath.Join(a.profiles.root, profile)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".petnames-*")
	if err != nil {
		return err
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)
	if err := temporary.Chmod(0600); err != nil {
		temporary.Close()
		return err
	}
	if err := json.NewEncoder(temporary).Encode(petnames); err != nil {
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
	return os.Rename(temporaryName, filepath.Join(dir, namesPetnameFile))
}

var errNamesPetnameFull = errors.New("petname limit reached")

// namesPetnameHandler sets the reader's petname for a key. The fragment route answers
// the petname form; the plain form returns to the author page.
func (a *App) namesPetnameHandler(w http.ResponseWriter, r *http.Request) {
	author, ok := namesRequestDID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	form, ok := readActionForm(w, r, "petname")
	if !ok {
		return
	}
	profile, err := a.profile(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "Could not open your local names.")
		return
	}
	status := "saved"
	petname, err := names.NormalizeName(form.Get("petname"))
	if err != nil {
		status = "invalid"
	} else if err := a.updatePetnames(profile, func(petnames map[string]string) error {
		petnames[author] = petname
		return nil
	}); err != nil {
		status = "failed"
		if errors.Is(err, errNamesPetnameFull) {
			status = "full"
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	if !strings.HasSuffix(r.URL.Path, "/fragment") {
		http.Redirect(w, r, namesAuthorPath(author)+"?petname="+status, http.StatusSeeOther)
		return
	}
	a.fragment(w, r, "names-petname-form", a.namesPetnameData(r, profile, author, status))
}

// namesPetnameClearHandler removes the reader's petname for a key.
func (a *App) namesPetnameClearHandler(w http.ResponseWriter, r *http.Request) {
	author, ok := namesRequestDID(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	if !requireSameOrigin(w, r) || !parseSmallForm(w, r, actionFormLimit) {
		return
	}
	profile, err := a.profile(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "Could not open your local names.")
		return
	}
	status := "cleared"
	if err := a.updatePetnames(profile, func(petnames map[string]string) error {
		delete(petnames, author)
		return nil
	}); err != nil {
		status = "failed"
	}
	w.Header().Set("Cache-Control", "no-store")
	if !strings.HasSuffix(r.URL.Path, "/fragment") {
		http.Redirect(w, r, namesAuthorPath(author)+"?petname="+status, http.StatusSeeOther)
		return
	}
	a.fragment(w, r, "names-petname-form", a.namesPetnameData(r, profile, author, status))
}

func namesAuthorPath(author string) string {
	return "/authors/" + namesPathID(author)
}

// namesPathID percent-encodes a DID for a path segment with every colon as %3A.
func namesPathID(author string) string {
	return strings.ReplaceAll(url.PathEscape(author), ":", "%3A")
}
