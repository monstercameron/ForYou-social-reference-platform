package monitor

import (
	"net/http"
	"time"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

const qualityMaxReportsPerPage = 4096

func init() {
	routeGroups = append(routeGroups, func(s *Service, mux *http.ServeMux) {
		mux.HandleFunc("/v1/quality-reports", func(w http.ResponseWriter, r *http.Request) { qualityReportsRoute(s, w, r) })
	})
}

// qualityReportsRoute serves this member's reports about one provider whose periods lie inside the quarter ending at quarter_end.
func qualityReportsRoute(s *Service, w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		monitorMethodNotAllowed(w)
		return
	}
	query, ok := monitorParseQuery(r.URL.RawQuery, "provider_id", "quarter_end")
	providerID, hasProvider := query["provider_id"]
	quarterValue, hasQuarter := query["quarter_end"]
	if !ok || !hasProvider || !hasQuarter {
		apierr.Write(w, apierr.PublicReadError{Status: apierr.InvalidQuery})
		return
	}
	if _, err := keys.ParseDID(providerID); err != nil {
		apierr.Write(w, apierr.PublicReadError{Status: apierr.InvalidQuery})
		return
	}
	quarterEnd, err := wire.ParseTime(quarterValue)
	if err != nil || wire.FormatTime(quarterEnd) != quarterValue || !s.Calendar().IsQuarterEnd(quarterEnd) {
		apierr.Write(w, apierr.PublicReadError{Status: apierr.InvalidQuery})
		return
	}
	member := MemberFrom(r)
	if member == nil {
		apierr.Write(w, apierr.PublicReadError{Status: apierr.NotFound})
		return
	}
	quarterStart, _ := s.Calendar().Quarter(quarterEnd.Add(-time.Second))
	reports := make([]wire.QualityReport, 0)
	prefix := providerID + "\x00"
	err = member.DB().View(func(tx store.Tx) error {
		return qualityReports.Scan(tx, store.Range{Start: prefix + wire.FormatTime(quarterStart), End: prefix + wire.FormatTime(quarterEnd)}, func(_ string, report wire.QualityReport) (bool, error) {
			end, err := wire.ParseTime(report.PeriodEnd)
			if err == nil && !end.After(quarterEnd) {
				reports = append(reports, report)
			}
			return len(reports) < qualityMaxReportsPerPage, nil
		})
	})
	if err != nil {
		monitorUnavailable(w)
		return
	}
	monitorWritePage(w, r, "QualityReportsPage", wire.QualityReportsPage{Reports: reports})
}
