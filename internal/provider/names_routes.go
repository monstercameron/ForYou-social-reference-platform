package provider

import (
	"mime"
	"net/http"
	"net/url"
	"strings"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/names"
	"foryou.local/foryou/internal/store"
)

func init() {
	routeGroups = append(routeGroups, namesRegister)
}

func namesRegister(service *Service, mux *http.ServeMux) {
	snapshots := &namesSnapshotSet{entries: make(map[*namesSnapshot]struct{}), cursors: make(map[string]namesCursorRef)}
	mux.HandleFunc("POST /v1/names", service.namesPublishRoute)
	mux.HandleFunc("GET /v1/names/{author_id}", service.namesReadRoute)
	mux.HandleFunc("GET /v1/names", func(w http.ResponseWriter, r *http.Request) {
		service.namesLookupRoute(snapshots, w, r)
	})
}

func (s *Service) namesPublishRoute(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.LimitRead(w, r, ReadOther) {
		return
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_message")
		return
	}
	body, err := decodeJSON(r, names.MaxRecordBytes, new(any))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_message")
		return
	}
	record, err := names.Parse(body)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_message")
		return
	}
	if err := s.CheckClock(); err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	if names.Verify(record, s.Now()) != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_message")
		return
	}
	encoded, err := names.Encode(record)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_message")
		return
	}
	result := s.namesPublish(record, encoded, s.Now())
	if result.signal != "" {
		if result.status == http.StatusTooManyRequests || result.status == http.StatusServiceUnavailable {
			writeRetry(w, result.status, result.signal, result.retry)
		} else {
			writeError(w, result.status, result.signal)
		}
		return
	}
	writeCanonical(w, result.status, result.body, "no-store")
}

// Name records may be withheld by policy or law; current compliance controls apply to messages only.
func (s *Service) namesReadRoute(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.LimitRead(w, r, ReadDirect) {
		return
	}
	authorID, ok := namesPathAuthor(r.URL.EscapedPath())
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_query")
		return
	}
	var body []byte
	var found bool
	err := s.Store().View(func(tx store.Tx) error {
		var err error
		_, body, found, err = namesLoadCurrent(tx, authorID)
		return err
	})
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	writeCanonical(w, http.StatusOK, body, "no-store")
}

func namesPathAuthor(escapedPath string) (string, bool) {
	const prefix = "/v1/names/"
	if !strings.HasPrefix(escapedPath, prefix) {
		return "", false
	}
	escaped := strings.TrimPrefix(escapedPath, prefix)
	decoded, err := url.PathUnescape(escaped)
	if err != nil || strings.Contains(decoded, "/") {
		return "", false
	}
	if _, err := keys.ParseDID(decoded); err != nil || namesEscapeDID(decoded) != escaped {
		return "", false
	}
	return decoded, true
}

func namesEscapeDID(did string) string {
	return strings.ReplaceAll(did, ":", "%3A")
}
