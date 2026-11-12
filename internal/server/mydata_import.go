package server

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/rank"
)

const (
	mydataFormat      = "foryou-client-data"
	mydataVersion     = 1
	mydataFileLimit   = 256 << 10
	mydataUploadLimit = 1 << 20
	mydataApplyLimit  = 32768
	mydataNotFile     = "This is not a ForYou data file."
	mydataOriginLimit = 32
	mydataOriginBytes = 300
)

// mydataImportable are the parts of a data file that can be applied to this browser and profile.
var mydataImportable = []string{"appearance", "filters", "feedback", "follows", "media"}

// mydataFile is the JSON data file: the export, the upload and the canonical data of an apply.
type mydataFile struct {
	Format            string                `json:"format"`
	Version           int                   `json:"version"`
	ExportedAt        string                `json:"exported_at"`
	Profile           string                `json:"profile"`
	PublicKey         string                `json:"public_key"`
	Appearance        *string               `json:"appearance,omitempty"`
	ContentFilters    *mydataFilters        `json:"content_filters,omitempty"`
	Feedback          *[]mydataVote         `json:"feedback,omitempty"`
	Follows           *[]string             `json:"follows,omitempty"`
	MediaOrigins      *[]string             `json:"media_origins,omitempty"`
	Network           *mydataNetwork        `json:"network,omitempty"`
	Acknowledgements  *map[string]time.Time `json:"acknowledgements,omitempty"`
	Other             *mydataOther          `json:"other,omitempty"`
	SettingsFileError string                `json:"settings_file_error,omitempty"`
}

type mydataFilters struct {
	Rubric string       `json:"rubric"`
	Levels mydataLevels `json:"levels"`
}

// mydataLevels is a map of dimension to level that writes its dimensions in the order the page shows them.
type mydataLevels map[string]int

func (l mydataLevels) MarshalJSON() ([]byte, error) {
	var out bytes.Buffer
	out.WriteByte('{')
	for i, d := range dimensions {
		if i > 0 {
			out.WriteByte(',')
		}
		name, err := json.Marshal(d.Key)
		if err != nil {
			return nil, err
		}
		out.Write(name)
		out.WriteByte(':')
		out.WriteString(strconv.Itoa(l[d.Key]))
	}
	out.WriteByte('}')
	return out.Bytes(), nil
}

type mydataVote struct {
	Post string `json:"post"`
	Vote string `json:"vote"`
}

type mydataNetwork struct {
	Providers       []ProviderChoice `json:"providers"`
	RemovedDefaults []string         `json:"removed_defaults"`
	Monitors        []MonitorChoice  `json:"monitors"`
	PublishNetwork  string           `json:"publish_network"`
}

func mydataMarshal(file mydataFile) ([]byte, error) {
	body, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(body, '\n'), nil
}

// mydataExportFile describes what this browser and profile keep, without any private key.
func mydataExportFile(s mydataState, exportedAt string) mydataFile {
	appearance := s.Theme
	feedback := make([]mydataVote, 0, len(s.Feedback))
	for _, e := range s.Feedback {
		feedback = append(feedback, mydataVote{Post: e.ID, Vote: e.Vote})
	}
	follows := append([]string{}, s.Follows...)
	file := mydataFile{
		Format:     mydataFormat,
		Version:    mydataVersion,
		ExportedAt: exportedAt,
		Profile:    s.Profile,
		PublicKey:  s.DID,
		Appearance: &appearance,
		ContentFilters: &mydataFilters{
			Rubric: rank.RubricVersion,
			Levels: mydataLevelsMap(s.Levels),
		},
		Feedback: &feedback,
		Follows:  &follows,
		Other:    mydataOtherFor(s.More),
	}
	if s.SettingsBad {
		file.SettingsFileError = "could not be read"
		return file
	}
	origins := append([]string{}, s.Settings.MediaOrigins...)
	file.MediaOrigins = &origins
	file.Network = &mydataNetwork{
		Providers:       append([]ProviderChoice{}, s.Settings.Providers...),
		RemovedDefaults: append([]string{}, s.Settings.RemovedDefaults...),
		Monitors:        append([]MonitorChoice{}, s.Settings.Monitors...),
		PublishNetwork:  s.Settings.PublishNetwork,
	}
	acks := make(map[string]time.Time, len(s.Settings.Acknowledgements))
	for name, at := range s.Settings.Acknowledgements {
		acks[name] = at
	}
	file.Acknowledgements = &acks
	return file
}

// mydataLevelsMap names the levels of every dimension, in dimension order.
func mydataLevelsMap(levels []int) mydataLevels {
	named := make(mydataLevels, len(dimensions))
	for i, d := range dimensions {
		named[d.Key] = levels[i]
	}
	return named
}

// mydataLevelsOf resolves a file's levels to one level per dimension, using the default for a missing kind.
func mydataLevelsOf(levels mydataLevels) []int {
	resolved := make([]int, len(dimensions))
	for i, d := range dimensions {
		level, ok := levels[d.Key]
		if !ok {
			level = d.Shown
		}
		resolved[i] = level
	}
	return resolved
}

// mydataParse decodes and validates a data file. It returns the reason for refusal, or an empty string.
func mydataParse(data []byte) (mydataFile, string) {
	var file mydataFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		if strings.Contains(err.Error(), "unknown field") {
			return file, "The file has fields this client does not know."
		}
		return file, mydataNotFile
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return file, mydataNotFile
	}
	if file.Format != mydataFormat {
		return file, mydataNotFile
	}
	if file.Version != mydataVersion {
		return file, fmt.Sprintf("This file uses format version %d; this client reads version %d.", file.Version, mydataVersion)
	}
	if message := mydataCheck(file); message != "" {
		return file, message
	}
	return file, ""
}

func mydataCheck(file mydataFile) string {
	switch {
	case file.Appearance != nil && !slices.Contains([]string{"system", "light", "dark"}, *file.Appearance):
		return "Appearance: unknown value."
	case file.ContentFilters != nil && !mydataLevelsValid(file.ContentFilters.Levels):
		return "Content filters: unknown kind or level."
	case file.Feedback != nil && !mydataFeedbackValid(*file.Feedback):
		return "Likes and dislikes: invalid entry."
	case file.Follows != nil && !mydataFollowsValid(*file.Follows):
		return "Follows: invalid key."
	case file.MediaOrigins != nil && !mydataOriginsValid(*file.MediaOrigins):
		return "Media origins: invalid origin."
	}
	return ""
}

func mydataLevelsValid(levels mydataLevels) bool {
	for name, level := range levels {
		index := slices.IndexFunc(dimensions, func(d dimension) bool { return d.Key == name })
		if index < 0 || level < 0 || level >= len(dimensions[index].Levels) {
			return false
		}
	}
	return true
}

func mydataFeedbackValid(entries []mydataVote) bool {
	if len(entries) > feedbackLimit {
		return false
	}
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		if e.Post == "" || len(e.Post) > 80 || strings.IndexFunc(e.Post, unicode.IsControl) >= 0 || (e.Vote != "up" && e.Vote != "down") || seen[e.Post] {
			return false
		}
		seen[e.Post] = true
	}
	return true
}

func mydataFollowsValid(ids []string) bool {
	if len(ids) > followingKeyLimit {
		return false
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if _, err := keys.ParseDID(id); err != nil || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func mydataOriginsValid(origins []string) bool {
	if len(origins) > mydataOriginLimit {
		return false
	}
	seen := make(map[string]bool, len(origins))
	for _, origin := range origins {
		u, err := url.Parse(origin)
		if err != nil || len(origin) > mydataOriginBytes || seen[origin] || u.Scheme != "https" || u.Host == "" || u.Port() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.String() != origin {
			return false
		}
		seen[origin] = true
	}
	return true
}

// mydataCanonical keeps the importable parts of a validated file, with every filter level named.
func mydataCanonical(file mydataFile) mydataFile {
	canonical := mydataFile{
		Format:       mydataFormat,
		Version:      mydataVersion,
		Appearance:   file.Appearance,
		Feedback:     file.Feedback,
		Follows:      file.Follows,
		MediaOrigins: file.MediaOrigins,
	}
	if file.ContentFilters != nil {
		canonical.ContentFilters = &mydataFilters{Rubric: file.ContentFilters.Rubric, Levels: mydataLevelsMap(mydataLevelsOf(file.ContentFilters.Levels))}
	}
	return canonical
}

// mydataChange is one importable part of a file, as the preview states it.
type mydataChange struct {
	Kind, Label string
	Lines       []string
	Changed     bool
}

// mydataPreview is the check-the-import page: what the file would change, and the data to apply.
type mydataPreview struct {
	Layout   Layout
	Exported string
	From     string
	Changes  []mydataChange
	Notes    []string
	Data     string
	Pending  bool
}

func mydataPreviewFor(layout Layout, s mydataState, file mydataFile) (mydataPreview, error) {
	preview := mydataPreview{Layout: layout, Exported: mydataExportedText(file.ExportedAt), From: mydataFromText(file.Profile), Notes: mydataIgnoredNotes(file)}
	if file.Appearance != nil {
		preview.Changes = append(preview.Changes, mydataChange{
			Kind:    "appearance",
			Label:   "Appearance: " + mydataThemeLabel(s.Theme) + " → " + mydataThemeLabel(*file.Appearance),
			Changed: s.Theme != *file.Appearance,
		})
	}
	if file.ContentFilters != nil {
		preview.Changes = append(preview.Changes, mydataFiltersChange(s, file.ContentFilters))
	}
	if file.Feedback != nil {
		next := *file.Feedback
		preview.Changes = append(preview.Changes, mydataChange{
			Kind:    "feedback",
			Label:   fmt.Sprintf("Likes and dislikes: replaces %d saved with %d from the file", len(s.Feedback), len(next)),
			Changed: !mydataSameFeedback(s.Feedback, next),
		})
	}
	if file.Follows != nil {
		next := *file.Follows
		added := mydataMissing(next, s.Follows)
		removed := mydataMissing(s.Follows, next)
		preview.Changes = append(preview.Changes, mydataChange{
			Kind:    "follows",
			Label:   fmt.Sprintf("Follows: replaces %d keys with %d (%d new, %d removed)", len(s.Follows), len(next), len(added), len(removed)),
			Changed: !slices.Equal(next, s.Follows),
		})
	}
	if file.MediaOrigins != nil {
		preview.Changes = append(preview.Changes, mydataMediaChange(s, *file.MediaOrigins))
	}
	for _, change := range preview.Changes {
		preview.Pending = preview.Pending || change.Changed
	}
	payload, err := json.Marshal(mydataCanonical(file))
	if err != nil {
		return preview, err
	}
	preview.Data = base64.RawURLEncoding.EncodeToString(payload)
	return preview, nil
}

func mydataFiltersChange(s mydataState, filters *mydataFilters) mydataChange {
	next := mydataLevelsOf(filters.Levels)
	current := filterRows(s.Levels)
	incoming := filterRows(next)
	change := mydataChange{Kind: "filters"}
	for i, d := range dimensions {
		if next[i] == s.Levels[i] {
			continue
		}
		change.Lines = append(change.Lines, d.Name+": "+current[i].Options[s.Levels[i]].Label+" → "+incoming[i].Options[next[i]].Label)
	}
	change.Changed = len(change.Lines) > 0
	change.Label = fmt.Sprintf("Content filters: %d of %d kinds change", len(change.Lines), len(dimensions))
	if filters.Rubric != "" && filters.Rubric != rank.RubricVersion {
		change.Lines = append(change.Lines, fmt.Sprintf("Filters were saved under rubric %s; this client uses %s. Levels are applied by kind name.", filters.Rubric, rank.RubricVersion))
	}
	return change
}

func mydataMediaChange(s mydataState, next []string) mydataChange {
	current := s.Settings.MediaOrigins
	added := mydataMissing(next, current)
	removed := mydataMissing(current, next)
	var parts []string
	if len(added) > 0 {
		parts = append(parts, "allows these origins to load media when shown: "+strings.Join(added, ", "))
	}
	if len(removed) > 0 {
		parts = append(parts, "stops: "+strings.Join(removed, ", "))
	}
	label := "Media origins: same as saved"
	if len(parts) > 0 {
		label = "Media origins: " + strings.Join(parts, "; ")
	}
	return mydataChange{Kind: "media", Label: label, Changed: !slices.Equal(next, current)}
}

// mydataSameFeedback compares the saved choices with a file's choices, in order.
func mydataSameFeedback(saved []feedbackEntry, next []mydataVote) bool {
	if len(saved) != len(next) {
		return false
	}
	for i := range saved {
		if saved[i].ID != next[i].Post || saved[i].Vote != next[i].Vote {
			return false
		}
	}
	return true
}

// mydataMissing lists the values of list that are not in from.
func mydataMissing(list, from []string) []string {
	var missing []string
	for _, value := range list {
		if !slices.Contains(from, value) {
			missing = append(missing, value)
		}
	}
	return missing
}

func mydataExportedText(exportedAt string) string {
	t, err := time.Parse(time.RFC3339, exportedAt)
	if err != nil {
		return "at an unknown time"
	}
	return "on " + t.UTC().Format("2006-01-02 15:04") + " UTC"
}

func mydataFromText(profile string) string {
	if profile == "" {
		return "an unknown profile"
	}
	if len(profile) > 8 {
		return "profile " + profile[:8] + "…"
	}
	return "profile " + profile
}

// mydataIgnoredNotes says which parts of a file are shown but not imported.
func mydataIgnoredNotes(file mydataFile) []string {
	var notes []string
	if file.Profile != "" || file.PublicKey != "" {
		notes = append(notes, "Profile ID and key from the file are shown for reference only; this browser keeps its own.")
	}
	if file.Network != nil {
		notes = append(notes, "Network choices are not imported. Add providers again on Network so their manifests are checked.")
	}
	if file.Acknowledgements != nil {
		notes = append(notes, "Confirmed warnings are not imported. You confirm them again on this browser.")
	}
	if file.Other != nil {
		notes = append(notes, "Display choices, feed order, saved posts, mutes, names and report receipts in the file are for reference only and are not imported.")
	}
	if file.SettingsFileError != "" {
		notes = append(notes, "The settings file was damaged when this file was exported, so its media, network and warning choices are not in it.")
	}
	return notes
}

func (a *App) mydataImportHandler(w http.ResponseWriter, r *http.Request) {
	if !requireSameOrigin(w, r) {
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "multipart/form-data" {
		http.Error(w, "unsupported form format", http.StatusUnsupportedMediaType)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, mydataUploadLimit)
	if err := r.ParseMultipartForm(mydataUploadLimit); err != nil {
		a.mydataRefused(w, r, mydataFormProblem(err))
		return
	}
	defer r.MultipartForm.RemoveAll()
	data, message := mydataReadUpload(r.MultipartForm)
	if message != "" {
		a.mydataRefused(w, r, message)
		return
	}
	file, message := mydataParse(data)
	if message != "" {
		a.mydataRefused(w, r, message)
		return
	}
	preview, err := mydataPreviewFor(a.layout(r, "settings", "Your data"), a.mydataReadState(r), file)
	if err != nil {
		a.fail(w, r, http.StatusInternalServerError, "The file could not be checked.")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	a.page(w, r, "mydata-preview", preview)
}

func (a *App) mydataRefused(w http.ResponseWriter, r *http.Request, message string) {
	a.renderStatus(w, r, "mydata-page", a.mydataPageView(r, a.mydataReadState(r), "", message), http.StatusBadRequest)
}

func mydataFormProblem(err error) string {
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		return "The file is larger than 256 KiB."
	}
	return "invalid form"
}

// mydataReadUpload returns the bytes of the one file part named file, or the reason it was refused.
func mydataReadUpload(form *multipart.Form) ([]byte, string) {
	count := 0
	var header *multipart.FileHeader
	for name, headers := range form.File {
		for _, h := range headers {
			if name != "file" {
				return nil, "invalid form"
			}
			count++
			header = h
		}
	}
	switch {
	case count > 1:
		return nil, "invalid form"
	case count == 0:
		return nil, "Choose a file to import."
	}
	opened, err := header.Open()
	if err != nil {
		return nil, "Choose a file to import."
	}
	defer opened.Close()
	data, err := io.ReadAll(io.LimitReader(opened, mydataFileLimit+1))
	if err != nil {
		return nil, "Choose a file to import."
	}
	if len(data) > mydataFileLimit {
		return nil, "The file is larger than 256 KiB."
	}
	if len(data) == 0 {
		return nil, "Choose a file to import."
	}
	return data, ""
}

func (a *App) mydataApplyHandler(w http.ResponseWriter, r *http.Request) {
	if !requireSameOrigin(w, r) {
		return
	}
	if !parseSmallForm(w, r, mydataApplyLimit) {
		return
	}
	if len(r.PostForm["data"]) != 1 {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return
	}
	raw, err := base64.RawURLEncoding.DecodeString(r.PostForm.Get("data"))
	if err != nil {
		http.Error(w, mydataNotFile, http.StatusBadRequest)
		return
	}
	file, message := mydataParse(raw)
	if message != "" {
		http.Error(w, mydataNotFile, http.StatusBadRequest)
		return
	}
	selected := make(map[string]bool, len(mydataImportable))
	for _, kind := range r.PostForm["kind"] {
		if !slices.Contains(mydataImportable, kind) || !mydataHas(file, kind) {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return
		}
		selected[kind] = true
	}
	applied := 0
	for _, kind := range mydataImportable {
		if !selected[kind] {
			continue
		}
		if err := a.mydataApply(w, r, kind, file); err != nil {
			a.fail(w, r, http.StatusInternalServerError, "Could not import. Try again.")
			return
		}
		applied++
	}
	w.Header().Set("Cache-Control", "no-store")
	http.Redirect(w, r, "/mydata?imported="+strconv.Itoa(applied)+"#mydata-kinds-title", http.StatusSeeOther)
}

func mydataHas(file mydataFile, kind string) bool {
	switch kind {
	case "appearance":
		return file.Appearance != nil
	case "filters":
		return file.ContentFilters != nil
	case "feedback":
		return file.Feedback != nil
	case "follows":
		return file.Follows != nil
	case "media":
		return file.MediaOrigins != nil
	}
	return false
}

// mydataApply writes one validated part of a file. Each part replaces what is saved for it.
func (a *App) mydataApply(w http.ResponseWriter, r *http.Request, kind string, file mydataFile) error {
	switch kind {
	case "appearance":
		if *file.Appearance == "system" {
			mydataExpireCookie(w, r, themeCookie, http.SameSiteLaxMode)
			return nil
		}
		http.SetCookie(w, &http.Cookie{Name: themeCookie, Value: *file.Appearance, Path: "/", MaxAge: 31536000, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteLaxMode})
	case "filters":
		digits := make([]byte, len(dimensions))
		for i, level := range mydataLevelsOf(file.ContentFilters.Levels) {
			digits[i] = byte('0' + level)
		}
		http.SetCookie(w, &http.Cookie{Name: filterCookie, Value: string(digits), Path: "/", MaxAge: 31536000, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
	case "feedback":
		mydataExpireFeedback(w, r)
		base := time.Now().UnixNano()
		for i, e := range *file.Feedback {
			http.SetCookie(w, &http.Cookie{Name: feedbackCookieName(e.Post), Value: e.Vote + "." + strconv.FormatInt(base+int64(i), 10), Path: "/", MaxAge: 31536000, HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
		}
	case "follows":
		id, err := a.profile(w, r)
		if err != nil {
			return err
		}
		now := a.config.Now()
		return a.followingUpdate(id, func(state *followingState) error {
			kept := make([]followingEntry, 0, len(*file.Follows))
			for _, did := range *file.Follows {
				entry := followingEntry{AuthorID: did, FollowedAt: now, Via: followingViaManual}
				if i := slices.IndexFunc(state.Follows, func(e followingEntry) bool { return e.AuthorID == did }); i >= 0 {
					entry = state.Follows[i]
				}
				kept = append(kept, entry)
			}
			state.Follows = kept
			return nil
		})
	case "media":
		id, err := a.profile(w, r)
		if err != nil {
			return err
		}
		origins := append([]string(nil), *file.MediaOrigins...)
		return a.updateSettings(id, func(settings *ProfileSettings) error {
			settings.MediaOrigins = origins
			return nil
		})
	}
	return nil
}
