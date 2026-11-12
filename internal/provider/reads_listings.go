package provider

import (
	"errors"
	"net/http"
	"strings"

	"foryou.local/foryou/internal/discovery"
)

func init() {
	routeGroups = append(routeGroups, rdRegisterListings)
}

func rdRegisterListings(s *Service, mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/messages", func(w http.ResponseWriter, r *http.Request) {
		rdList(w, r, s, discovery.Messages, "")
	})
	mux.HandleFunc("GET /v1/author-messages", func(w http.ResponseWriter, r *http.Request) {
		rdList(w, r, s, discovery.AuthorMessages, "")
	})
	mux.HandleFunc("GET /v1/replies/{message_id}", func(w http.ResponseWriter, r *http.Request) {
		rdIndex(w, r, s, discovery.Replies)
	})
	mux.HandleFunc("GET /v1/references/{target}", func(w http.ResponseWriter, r *http.Request) {
		rdIndex(w, r, s, discovery.References)
	})
	mux.HandleFunc("GET /v1/versions/{message_id}", func(w http.ResponseWriter, r *http.Request) {
		rdIndex(w, r, s, discovery.Versions)
	})
}

func rdIndex(w http.ResponseWriter, r *http.Request, s *Service, route discovery.Route) {
	escaped := r.URL.EscapedPath()
	lastSlash := strings.LastIndexByte(escaped, '/')
	if lastSlash < 0 {
		writeError(w, http.StatusBadRequest, "invalid_query")
		return
	}
	target, err := discovery.ParseTarget(route, escaped[lastSlash+1:])
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query")
		return
	}
	rdList(w, r, s, route, target)
}

func rdList(w http.ResponseWriter, r *http.Request, s *Service, route discovery.Route, target string) {
	head := r.Method == http.MethodHead
	if head {
		w = rdHeadWriter{ResponseWriter: w, head: true}
	}
	query, cursor, err := discovery.ParseListQuery(route, target, r.URL.RawQuery)
	if err != nil {
		var parseError *discovery.Error
		if errors.As(err, &parseError) {
			writeError(w, http.StatusBadRequest, parseError.Status)
		} else {
			writeError(w, http.StatusBadRequest, "invalid_query")
		}
		return
	}
	if !s.LimitRead(w, r, ReadListing) {
		return
	}
	var body []byte
	if cursor == "" {
		body, err = s.Snapshots().StartBudgeted(query.Binding(), query.Limit, s.Visibility(), func(budget int64) ([]discovery.Entry, error) {
			records, err := s.Records().index.SelectWithin(query, budget)
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
		rdSnapshotError(w, err)
		return
	}
	writeCanonical(w, http.StatusOK, body, "no-store")
}

func rdSnapshotError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, discovery.ErrUnknownCursor):
		writeError(w, http.StatusBadRequest, "invalid_cursor")
	case errors.Is(err, discovery.ErrBusy):
		writeRetry(w, http.StatusTooManyRequests, "rate_limited", 1)
	default:
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", 5)
	}
}
