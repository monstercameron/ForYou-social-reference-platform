package server

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"foryou.local/foryou/internal/sdk"
	"foryou.local/foryou/internal/succession"
	"foryou.local/foryou/internal/wire"
)

func init() { routeGroups = append(routeGroups, registerIdentity) }

const (
	identPassphraseMin     = 12
	identBackupLimit       = 16 << 10
	identSuccessionTimeout = 10 * time.Second
)

func registerIdentity(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /identity", a.identityPageHandler)
	mux.HandleFunc("GET /identity/successions", a.identitySuccessionsHandler)
	mux.HandleFunc("POST /identity/create", a.identityCreateHandler)
	mux.HandleFunc("POST /identity/create/fragment", a.identityCreateHandler)
	mux.HandleFunc("POST /identity/acknowledge", a.identityAcknowledgeHandler)
	mux.HandleFunc("POST /identity/acknowledge/fragment", a.identityAcknowledgeHandler)
	mux.HandleFunc("POST /identity/backup", a.identityBackupHandler)
	mux.HandleFunc("POST /identity/backup/check", a.identityBackupCheckHandler)
	mux.HandleFunc("POST /identity/backup/check/fragment", a.identityBackupCheckHandler)
	mux.HandleFunc("POST /identity/rotate", a.identityRotateHandler)
	mux.HandleFunc("POST /identity/rotate/fragment", a.identityRotateHandler)
	mux.HandleFunc("POST /identity/restore", a.identityRestoreHandler)
	mux.HandleFunc("POST /identity/restore/fragment", a.identityRestoreHandler)
}

// identityView is the data every identity page and fragment renders.
type identityView struct {
	Layout          Layout
	Found           bool
	DID             string
	Created         string
	Acked           bool
	AckedOn         string
	AckError        string
	AckNotice       string
	BackupChecked   bool
	BackupCheckedOn string
	BackupError     string
	CheckMessage    string
	CheckError      string
	RotateError     string
	RestoreError    string
	Notice          string
	Publications    []identityPublication
	Retired         []identityRetired
	Checked         bool
	Successions     []identityLine
}

type identityRetired struct {
	DID, Retired, SuccessionID string
}

type identityPublication struct {
	Provider, Result string
	Failed           bool
}

type identityLine struct {
	Text  string
	Notes []string
}

// identityUpload is a backup file and its passphrase, as sent by a multipart form.
type identityUpload struct {
	data       []byte
	passphrase string
}

func (a *App) identityPageHandler(w http.ResponseWriter, r *http.Request) {
	profile, view, err := a.identityRequest(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The identity could not be read.")
		return
	}
	if r.URL.Query().Get("check") == "1" && view.Found {
		view.Checked = true
		view.Successions = a.identityLines(r.Context(), profile, view)
	}
	w.Header().Set("Cache-Control", "private, no-store")
	a.page(w, r, "identity-page", view)
}

func (a *App) identitySuccessionsHandler(w http.ResponseWriter, r *http.Request) {
	profile, view, err := a.identityRequest(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The identity could not be read.")
		return
	}
	if !view.Found {
		a.fail(w, r, http.StatusConflict, "Create a key before you check its successions.")
		return
	}
	view.Checked = true
	view.Successions = a.identityLines(r.Context(), profile, view)
	a.fragment(w, r, "identity-successions", view)
}

func (a *App) identityCreateHandler(w http.ResponseWriter, r *http.Request) {
	if _, ok := readActionForm(w, r); !ok {
		return
	}
	profile, view, err := a.identityRequest(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The identity could not be read.")
		return
	}
	if !view.Found {
		if _, err := a.keystore().Create(profile); err != nil && !errors.Is(err, sdk.ErrIdentityExists) {
			a.fail(w, r, http.StatusInternalServerError, "The key could not be created.")
			return
		}
		if view, err = a.identityFor(r, profile); err != nil {
			a.fail(w, r, http.StatusInternalServerError, "The identity could not be read.")
			return
		}
	}
	a.identityAnswer(w, r, identFragment(r), "identity-create", view, http.StatusOK)
}

func (a *App) identityAcknowledgeHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := readActionForm(w, r)
	if !ok {
		return
	}
	profile, view, err := a.identityRequest(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The identity could not be read.")
		return
	}
	fragment := identFragment(r)
	if form.Get("understood") != "yes" {
		view.AckError = "Tick the box to confirm you understand these three points."
		a.identityAnswer(w, r, fragment, "identity-warnings", view, http.StatusBadRequest)
		return
	}
	err = a.updateSettings(profile, func(settings *ProfileSettings) error {
		if settings.Acknowledgements == nil {
			settings.Acknowledgements = make(map[string]time.Time)
		}
		settings.Acknowledgements[ackKeyWarnings] = a.now()
		return nil
	})
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The acknowledgement could not be saved.")
		return
	}
	if view, err = a.identityFor(r, profile); err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The identity could not be read.")
		return
	}
	view.AckNotice = "Saved."
	a.identityAnswer(w, r, fragment, "identity-warnings", view, http.StatusOK)
}

// identityBackupHandler answers with the encrypted file. It is a plain form post, so its errors come back as the full page.
func (a *App) identityBackupHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := readActionForm(w, r, "passphrase", "confirm")
	if !ok {
		return
	}
	profile, view, err := a.identityRequest(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The identity could not be read.")
		return
	}
	if !view.Found {
		a.fail(w, r, http.StatusConflict, "Create a key before you back it up.")
		return
	}
	passphrase := form.Get("passphrase")
	switch {
	case utf8.RuneCountInString(passphrase) < identPassphraseMin:
		view.BackupError = "Use a passphrase of at least 12 characters."
	case passphrase != form.Get("confirm"):
		view.BackupError = "The two passphrases do not match."
	}
	if view.BackupError != "" {
		a.identityAnswer(w, r, false, "identity-page", view, http.StatusBadRequest)
		return
	}
	data, err := a.keystore().ExportBackup(profile, passphrase)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The backup could not be created.")
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="foryou-key-`+identFileStem(view.DID)+`.json"`)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (a *App) identityBackupCheckHandler(w http.ResponseWriter, r *http.Request) {
	upload, problem, ok := identReadUpload(w, r)
	if !ok {
		return
	}
	profile, view, err := a.identityRequest(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The identity could not be read.")
		return
	}
	if !view.Found {
		a.fail(w, r, http.StatusConflict, "Create a key before you check a backup.")
		return
	}
	if problem == "" {
		switch err := a.keystore().ConfirmBackup(profile, upload.data, upload.passphrase); {
		case err == nil:
		case errors.Is(err, sdk.ErrBackupInvalid):
			problem = "This backup cannot be opened with that passphrase, or it belongs to another key."
		default:
			a.fail(w, r, http.StatusInternalServerError, "The backup could not be checked.")
			return
		}
	}
	checked, err := a.identityFor(r, profile)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The identity could not be read.")
		return
	}
	status := http.StatusOK
	if problem != "" {
		checked.CheckError = problem
		status = http.StatusBadRequest
	} else {
		checked.CheckMessage = "Backup opens and matches " + shortDID(checked.DID) + "."
	}
	a.identityAnswer(w, r, identFragment(r), "identity-backup", checked, status)
}

func (a *App) identityRotateHandler(w http.ResponseWriter, r *http.Request) {
	form, ok := readActionForm(w, r)
	if !ok {
		return
	}
	profile, view, err := a.identityRequest(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The identity could not be read.")
		return
	}
	if !view.Found {
		a.fail(w, r, http.StatusConflict, "Create a key before you rotate it.")
		return
	}
	if form.Get("confirm") != "yes" {
		view.RotateError = "Tick the box to confirm that rotation does not recover a lost key."
		a.identityAnswer(w, r, identFragment(r), "identity-main", view, http.StatusBadRequest)
		return
	}
	next, err := a.keystore().Rotate(profile, a.now())
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The key could not be rotated.")
		return
	}
	if view, err = a.identityFor(r, profile); err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The identity could not be read.")
		return
	}
	view.Publications = a.identityPublish(r.Context(), profile, next)
	view.Notice = "Key rotated. The earlier key stays listed as retired."
	if len(view.Publications) == 0 {
		view.Notice += " No provider is selected, so the succession was not published."
	}
	a.identityAnswer(w, r, identFragment(r), "identity-main", view, http.StatusOK)
}

func (a *App) identityRestoreHandler(w http.ResponseWriter, r *http.Request) {
	upload, problem, ok := identReadUpload(w, r)
	if !ok {
		return
	}
	profile, view, err := a.identityRequest(w, r)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The identity could not be read.")
		return
	}
	if problem == "" {
		_, err := a.keystore().ImportBackup(profile, upload.data, upload.passphrase)
		switch {
		case err == nil:
			restored, err := a.identityFor(r, profile)
			if err != nil {
				a.fail(w, r, http.StatusInternalServerError, "The identity could not be read.")
				return
			}
			restored.Notice = "Restored key " + shortDID(restored.DID) + "."
			a.identityAnswer(w, r, identFragment(r), "identity-main", restored, http.StatusOK)
			return
		case errors.Is(err, sdk.ErrIdentityExists):
			problem = "This profile already has a key. Restore only into a profile with no key."
		case errors.Is(err, sdk.ErrBackupInvalid):
			problem = "This backup cannot be opened with that passphrase, or it is not a key backup."
		default:
			a.fail(w, r, http.StatusInternalServerError, "The backup could not be restored.")
			return
		}
	}
	view.RestoreError = problem
	a.identityAnswer(w, r, identFragment(r), "identity-main", view, http.StatusBadRequest)
}

// identityRequest returns the profile and its identity page data, creating the profile cookie if the reader has none.
func (a *App) identityRequest(w http.ResponseWriter, r *http.Request) (string, identityView, error) {
	profile, err := a.profile(w, r)
	if err != nil {
		return "", identityView{}, err
	}
	view, err := a.identityFor(r, profile)
	return profile, view, err
}

func (a *App) identityFor(r *http.Request, profile string) (identityView, error) {
	view := identityView{Layout: a.layout(r, "identity", "Identity and keys")}
	view.Layout.Rightbar = false
	identity, found, err := a.keystore().Identity(profile)
	if err != nil {
		return identityView{}, err
	}
	settings, err := a.settings(profile)
	if err != nil {
		return identityView{}, err
	}
	if acknowledged, ok := settings.Acknowledgements[ackKeyWarnings]; ok {
		view.Acked, view.AckedOn = true, identDate(acknowledged)
	}
	if !found {
		return view, nil
	}
	view.Found, view.DID, view.Created = true, identity.DID, identDate(identity.CreatedAt)
	if identity.BackupConfirmedForCurrent {
		view.BackupChecked, view.BackupCheckedOn = true, identDate(identity.BackupConfirmedAt)
	}
	for _, retired := range identity.Retired {
		view.Retired = append(view.Retired, identityRetired{DID: retired.DID, Retired: identDate(retired.RetiredAt), SuccessionID: retired.SuccessionID})
	}
	return view, nil
}

// identityAnswer sends a page, or a fragment for HTMLeX. Fragments answer 200 because HTMLeX drops the body of an error response; the message is in the fragment.
func (a *App) identityAnswer(w http.ResponseWriter, r *http.Request, fragment bool, name string, view identityView, status int) {
	if fragment {
		a.renderStatus(w, r, name, view, http.StatusOK)
		return
	}
	w.Header().Set("Cache-Control", "private, no-store")
	a.renderStatus(w, r, "identity-page", view, status)
}

// identityLines checks the providers for successions of the current key and every earlier key.
func (a *App) identityLines(ctx context.Context, profile string, view identityView) []identityLine {
	ctx, cancel := context.WithTimeout(ctx, identSuccessionTimeout)
	defer cancel()
	dids := []string{view.DID}
	for _, retired := range view.Retired {
		dids = append(dids, retired.DID)
	}
	providers := a.selection(profile).Providers
	if len(providers) == 0 {
		return []identityLine{{Text: "No provider is selected, so no successions were checked."}}
	}
	lines := make([]identityLine, 0, len(dids))
	for _, did := range dids {
		var held []wire.KeySuccession
		var notes []string
		answered := 0
		for _, choice := range providers {
			client, err := a.providerClient(identEndpoint(choice))
			if err != nil {
				notes = append(notes, identProviderName(choice)+": could not be reached")
				continue
			}
			items, _, signal, err := client.Successions(ctx, did)
			if err != nil {
				notes = append(notes, identProviderName(choice)+": "+identSignalWording(signal))
				continue
			}
			answered++
			held = append(held, items...)
		}
		resolved := sdk.ResolveAuthorSuccession(did, held)
		line := identityLine{Text: "No succession published for " + did + " by the providers that answered.", Notes: notes}
		switch {
		case answered == 0:
			line.Text = "No selected provider answered for " + did + ", so nothing is known about its successions."
		case resolved.Resolution == succession.Linked:
			line.Text = "Linked: " + did + " → " + resolved.NewID + ", asserted by the old key."
		case resolved.Resolution == succession.Unresolved:
			line.Text = "Conflict: " + did + " names more than one successor. Readers will not follow either."
		}
		lines = append(lines, line)
	}
	return lines
}

// identityPublish sends the succession to every selected provider and reports each outcome in words.
func (a *App) identityPublish(ctx context.Context, profile string, next wire.KeySuccession) []identityPublication {
	ctx, cancel := context.WithTimeout(ctx, identSuccessionTimeout)
	defer cancel()
	providers := a.selection(profile).Providers
	publications := make([]identityPublication, 0, len(providers))
	for _, choice := range providers {
		publication := identityPublication{Provider: identProviderName(choice), Result: "Published"}
		client, err := a.providerClient(identEndpoint(choice))
		if err == nil {
			err = client.PublishSuccession(ctx, next)
		}
		if err != nil {
			publication.Result = "Not published: " + identSignalWording(sdk.Classify(err))
			publication.Failed = true
		}
		publications = append(publications, publication)
	}
	return publications
}

// identReadUpload reads one backup file and a passphrase. A problem is a message for the reader; ok false means the response is already written.
func identReadUpload(w http.ResponseWriter, r *http.Request) (identityUpload, string, bool) {
	if !requireSameOrigin(w, r) {
		return identityUpload{}, "", false
	}
	r.Body = http.MaxBytesReader(w, r.Body, identBackupLimit+4096)
	if err := r.ParseMultipartForm(identBackupLimit); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return identityUpload{}, "This file is too large to be a key backup.", true
		}
		http.Error(w, "invalid form", http.StatusBadRequest)
		return identityUpload{}, "", false
	}
	defer r.MultipartForm.RemoveAll()
	passphrase := r.MultipartForm.Value["passphrase"]
	files := r.MultipartForm.File["backup"]
	for name := range r.MultipartForm.File {
		if name != "backup" {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return identityUpload{}, "", false
		}
	}
	if len(passphrase) != 1 || len(files) > 1 {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return identityUpload{}, "", false
	}
	if len(files) != 1 {
		return identityUpload{}, "Choose a backup file.", true
	}
	if utf8.RuneCountInString(passphrase[0]) < identPassphraseMin {
		return identityUpload{}, "Use a passphrase of at least 12 characters.", true
	}
	file, err := files[0].Open()
	if err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return identityUpload{}, "", false
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, identBackupLimit+1))
	if err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return identityUpload{}, "", false
	}
	if len(data) > identBackupLimit {
		return identityUpload{}, "This file is too large to be a key backup.", true
	}
	return identityUpload{data: data, passphrase: passphrase[0]}, "", true
}

func identFragment(r *http.Request) bool { return strings.HasSuffix(r.URL.Path, "/fragment") }

func identDate(t time.Time) string { return t.UTC().Format("2 January 2006") }

// identFileStem is the first eight characters after did:key:, which name a backup file without revealing the key.
func identFileStem(did string) string {
	suffix := strings.TrimPrefix(did, "did:key:")
	if len(suffix) > 8 {
		return suffix[:8]
	}
	return suffix
}

func identEndpoint(choice ProviderChoice) Endpoint {
	return Endpoint{URL: choice.Endpoint, ServiceID: choice.OperatorKey, Label: choice.Label}
}

func identProviderName(choice ProviderChoice) string {
	if choice.Label != "" {
		return choice.Label
	}
	return choice.Endpoint
}

func identSignalWording(signal sdk.Signal) string {
	switch signal {
	case sdk.SignalTimeout:
		return "timed out"
	case sdk.SignalUnavailable:
		return "unavailable"
	case sdk.SignalRateLimited:
		return "rate limited, try again later"
	case sdk.SignalNotFound:
		return "not found here"
	case sdk.SignalPolicyRestricted, sdk.SignalLegalRestricted, sdk.SignalRestricted:
		return "restricted"
	case sdk.SignalProtocol:
		return "sent an invalid answer"
	default:
		return "could not be reached"
	}
}
