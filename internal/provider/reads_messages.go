package provider

import (
	"fmt"
	"net/http"
	"strings"
)

func init() {
	routeGroups = append(routeGroups, rdRegisterMessages)
}

func rdRegisterMessages(s *Service, mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/messages/{message_id...}", func(w http.ResponseWriter, r *http.Request) {
		rdDirect(w, r, s)
	})
}

func rdDirect(w http.ResponseWriter, r *http.Request, s *Service) {
	head := r.Method == http.MethodHead
	w = rdHeadWriter{ResponseWriter: w, head: head}
	escaped := strings.TrimPrefix(r.URL.EscapedPath(), "/v1/messages/")
	if !rdValidMessagePath(escaped) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if !s.LimitRead(w, r, ReadDirect) {
		return
	}
	id := strings.Replace(escaped, "%3A", ":", 1)
	restriction, err := s.Restriction(id)
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", 5)
		return
	}
	switch restriction {
	case Concealed:
		writeError(w, http.StatusNotFound, "not_found")
		return
	case PolicyRestricted, LegalRestricted, Restricted:
		writeError(w, http.StatusForbidden, string(restriction))
		return
	case Unrestricted:
	default:
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", 5)
		return
	}
	body, state, err := s.Records().Lookup(id)
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", 5)
		return
	}
	switch state {
	case RecordRetained:
		cache := fmt.Sprintf("public, max-age=%d", int64(s.Config().DirectCacheMaxAge.Seconds()))
		if head {
			cache = "no-store"
		}
		writeCanonical(w, http.StatusOK, body, cache)
	case RecordExpired:
		writeError(w, http.StatusGone, "retention_expired")
	default:
		writeError(w, http.StatusNotFound, "not_found")
	}
}

func rdValidMessagePath(escaped string) bool {
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

type rdHeadWriter struct {
	http.ResponseWriter
	head bool
}

func (w rdHeadWriter) Write(b []byte) (int, error) {
	if w.head {
		return len(b), nil
	}
	return w.ResponseWriter.Write(b)
}
