package provider

import (
	"net/http"

	"foryou.local/foryou/internal/discovery"
)

func init() {
	routeGroups = append(routeGroups, rdRegisterChanges)
}

func rdRegisterChanges(s *Service, mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/changes", func(w http.ResponseWriter, r *http.Request) {
		rdChanges(w, r, s)
	})
}

func rdChanges(w http.ResponseWriter, r *http.Request, s *Service) {
	head := r.Method == http.MethodHead
	if head {
		w = rdHeadWriter{ResponseWriter: w, head: true}
	}
	after, limit, err := discovery.ParseChangesQuery(r.URL.RawQuery)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_query")
		return
	}
	if !s.LimitRead(w, r, ReadListing) {
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
	body := discovery.BuildChanges(s.ProviderID(), after, limit, s.Records().MaxSequence(), candidates, len(records) < 1000)
	writeCanonical(w, http.StatusOK, body, "no-store")
}
