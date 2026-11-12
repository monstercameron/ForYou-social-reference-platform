package replica

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"foryou.local/foryou/internal/discovery"
)

func init() {
	routeGroups = append(routeGroups, readRegisterRoutes)
}

func readRegisterRoutes(s *Service, mux *http.ServeMux) {
	mux.HandleFunc("/v1/messages/", s.readDirect)
	mux.HandleFunc("/v1/messages", s.readListing(discovery.Messages))
	mux.HandleFunc("/v1/author-messages", s.readListing(discovery.AuthorMessages))
	mux.HandleFunc("/v1/replies/{message_id}", s.readIndex(discovery.Replies))
	mux.HandleFunc("/v1/references/{target}", s.readIndex(discovery.References))
	mux.HandleFunc("/v1/versions/{message_id}", s.readIndex(discovery.Versions))
	mux.HandleFunc("/v1/changes", s.readChanges)
}

func (s *Service) readDirect(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		readMethodNotAllowed(w, "GET, HEAD")
		return
	}
	escaped := strings.TrimPrefix(r.URL.EscapedPath(), "/v1/messages/")
	if !readValidMessagePath(escaped) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if retry, ok := s.AllowRead(ReadDirect, r); !ok {
		writeRetry(w, http.StatusTooManyRequests, "rate_limited", retry)
		return
	}
	id := strings.Replace(escaped, "%3A", ":", 1)
	body, found := s.Records().Lookup(id)
	if !found {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Length", strconv.Itoa(len(body)))
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		return
	}
	cache := fmt.Sprintf("public, max-age=%d", int64(s.DirectCacheMaxAge().Seconds()))
	writeCanonical(w, http.StatusOK, body, cache)
}

func readValidMessagePath(escaped string) bool {
	if strings.HasPrefix(escaped, "fy1%3A") {
		escaped = "fy1:" + escaped[len("fy1%3A"):]
	}
	if len(escaped) != 68 || !strings.HasPrefix(escaped, "fy1:") {
		return false
	}
	for i := 4; i < len(escaped); i++ {
		if !(escaped[i] >= '0' && escaped[i] <= '9' || escaped[i] >= 'a' && escaped[i] <= 'f') {
			return false
		}
	}
	return true
}

func readMethodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusMethodNotAllowed)
}

func (s *Service) readListing(route discovery.Route) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			readMethodNotAllowed(w, "GET")
			return
		}
		s.readListPage(w, r, route, "")
	}
}

func (s *Service) readIndex(route discovery.Route) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			readMethodNotAllowed(w, "GET")
			return
		}
		escapedPath := r.URL.EscapedPath()
		lastSlash := strings.LastIndexByte(escapedPath, '/')
		if lastSlash < 0 {
			writeError(w, http.StatusBadRequest, "invalid_query")
			return
		}
		target, err := discovery.ParseTarget(route, escapedPath[lastSlash+1:])
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_query")
			return
		}
		s.readListPage(w, r, route, target)
	}
}

func (s *Service) readListPage(w http.ResponseWriter, r *http.Request, route discovery.Route, target string) {
	query, cursor, err := discovery.ParseListQuery(route, target, r.URL.RawQuery)
	if err != nil {
		var parseErr *discovery.Error
		if errors.As(err, &parseErr) {
			writeError(w, http.StatusBadRequest, parseErr.Status)
			return
		}
		writeError(w, http.StatusBadRequest, "invalid_query")
		return
	}
	if retry, ok := s.AllowRead(ReadListing, r); !ok {
		writeRetry(w, http.StatusTooManyRequests, "rate_limited", retry)
		return
	}
	var body []byte
	if cursor == "" {
		body, err = s.Snapshots().StartBudgeted(query.Binding(), query.Limit, s.Visibility(), func(budget int64) ([]discovery.Entry, error) {
			records, err := s.Records().SelectWithin(query, budget)
			if err != nil {
				return nil, err
			}
			entries := make([]discovery.Entry, 0, len(records))
			for _, record := range records {
				entries = append(entries, discovery.Entry{MessageID: record.MessageID, Bytes: record.Bytes})
			}
			return entries, nil
		})
	} else {
		body, err = s.Snapshots().Continue(query.Binding(), cursor, query.Limit, s.Visibility())
	}
	if err != nil {
		readWriteSnapshotError(w, err)
		return
	}
	writeCanonical(w, http.StatusOK, body, "no-store")
}

func readWriteSnapshotError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, discovery.ErrUnknownCursor):
		writeError(w, http.StatusBadRequest, "invalid_cursor")
	case errors.Is(err, discovery.ErrBusy):
		writeRetry(w, http.StatusTooManyRequests, "rate_limited", 1)
	default:
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", 5)
	}
}

func (s *Service) readChanges(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		readMethodNotAllowed(w, "GET")
		return
	}
	after, limit, err := discovery.ParseChangesQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query")
		return
	}
	if retry, ok := s.AllowRead(ReadListing, r); !ok {
		writeRetry(w, http.StatusTooManyRequests, "rate_limited", retry)
		return
	}
	records := s.Records().After(after, 1000)
	candidates := make([]discovery.Candidate, 0, len(records))
	for _, record := range records {
		visible, _, visibilityErr := s.Visibility().Visible(record.MessageID)
		if visibilityErr != nil {
			writeRetry(w, http.StatusServiceUnavailable, "unavailable", 5)
			return
		}
		candidates = append(candidates, discovery.Candidate{Sequence: record.Sequence, Bytes: record.Bytes, Visible: visible})
	}
	body := discovery.BuildChanges(s.ReplicaID(), after, limit, s.Records().MaxSequence(), candidates, len(records) < 1000)
	writeCanonical(w, http.StatusOK, body, "no-store")
}
