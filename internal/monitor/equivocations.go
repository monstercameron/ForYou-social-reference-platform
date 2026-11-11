package monitor

import (
	"net/http"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/committee"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

func init() {
	routeGroups = append(routeGroups, monitorRegisterEquivocationRoutes)
}

func monitorRegisterEquivocationRoutes(_ *Service, mux *http.ServeMux) {
	mux.HandleFunc("/v1/equivocations", monitorEquivocations)
}

func monitorEquivocations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		monitorMethodNotAllowed(w)
		return
	}
	query, ok := monitorParseQuery(r.URL.RawQuery, "provider_id")
	if !ok {
		apierr.Write(w, apierr.PublicReadError{Status: apierr.InvalidQuery})
		return
	}
	providerID, exists := query["provider_id"]
	if !exists {
		apierr.Write(w, apierr.PublicReadError{Status: apierr.InvalidQuery})
		return
	}
	if _, err := keys.ParseDID(providerID); err != nil {
		apierr.Write(w, apierr.PublicReadError{Status: apierr.InvalidQuery})
		return
	}
	member := MemberFrom(r)
	page := wire.EquivocationsPage{Proofs: []wire.EquivocationProof{}}
	if member == nil || member.Do(func(m *committee.Monitor) error {
		page.Proofs = m.EquivocationProofs(providerID)
		return nil
	}) != nil || len(page.Proofs) > 1000 {
		monitorUnavailable(w)
		return
	}
	monitorWritePage(w, r, "EquivocationsPage", page)
}
