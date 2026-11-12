package monitor

import (
	"encoding/json"
	"net/http"
	"sort"
	"strconv"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/faux"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/store"
	"foryou.local/foryou/internal/wire"
)

const evidenceMaxPage = 1024

func init() {
	routeGroups = append(routeGroups, func(s *Service, mux *http.ServeMux) {
		mux.HandleFunc("GET /v1/quality-reports/{report_id}/methodology", func(w http.ResponseWriter, r *http.Request) {
			if evidence, ok := evidenceOfReport(w, r); ok {
				evidenceWrite(w, "text/plain; charset=utf-8", []byte(evidence.Methodology))
			}
		})
		mux.HandleFunc("GET /v1/quality-reports/{report_id}/observations", func(w http.ResponseWriter, r *http.Request) {
			evidence, ok := evidenceOfReport(w, r)
			if !ok {
				return
			}
			// The same canonical bytes the report's observations_digest was taken over.
			body, err := canon.Encode(evidence.Observations)
			if err != nil {
				monitorUnavailable(w)
				return
			}
			evidenceWrite(w, "application/json", body)
		})
		mux.HandleFunc("GET /v1/foundation-evidence", func(w http.ResponseWriter, r *http.Request) { foundationEvidenceRoute(s, w, r) })
	})
}

// evidenceOfReport returns the stored methodology and observation set of one of this member's reports.
func evidenceOfReport(w http.ResponseWriter, r *http.Request) (qualityEvidenceRecord, bool) {
	member := MemberFrom(r)
	if member == nil || r.URL.RawQuery != "" {
		apierr.Write(w, apierr.PublicReadError{Status: apierr.NotFound})
		return qualityEvidenceRecord{}, false
	}
	var evidence qualityEvidenceRecord
	var found bool
	err := member.DB().View(func(tx store.Tx) error {
		var err error
		evidence, found, err = qualityEvidence.Get(tx, r.PathValue("report_id"))
		return err
	})
	if err != nil {
		monitorUnavailable(w)
		return qualityEvidenceRecord{}, false
	}
	if !found {
		apierr.Write(w, apierr.PublicReadError{Status: apierr.NotFound})
		return qualityEvidenceRecord{}, false
	}
	return evidence, true
}

func evidenceWrite(w http.ResponseWriter, contentType string, body []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

// foundationEvidenceRoute publishes what the Foundation watch recorded, signed by
// the member that serves it. Only the three kinds the specification defines are
// published: they carry their own proof. A logged object that could not be fetched
// or did not match its entry stays in the monitor's log, because absence proves nothing.
func foundationEvidenceRoute(s *Service, w http.ResponseWriter, r *http.Request) {
	member := MemberFrom(r)
	if member == nil || r.URL.RawQuery != "" {
		apierr.Write(w, apierr.PublicReadError{Status: apierr.NotFound})
		return
	}
	recorded := s.FoundationEvidence()
	sort.SliceStable(recorded, func(i, j int) bool { return recorded[i].ObservedAt.Before(recorded[j].ObservedAt) })
	page := wire.FoundationEvidencePage{Evidence: make([]wire.FoundationEvidence, 0, len(recorded))}
	for _, item := range recorded {
		evidence, ok := evidencePublished(item)
		if !ok {
			continue
		}
		evidence.MonitorID = member.DID()
		evidence.ObservedAt = wire.FormatTime(item.ObservedAt)
		unsigned, err := canon.EncodeWithout(evidence, "monitor_signature")
		if err != nil {
			monitorUnavailable(w)
			return
		}
		if evidence.MonitorSignature, err = keys.Sign(member.key, "foundation-evidence", unsigned); err != nil {
			monitorUnavailable(w)
			return
		}
		page.Evidence = append(page.Evidence, evidence)
		if len(page.Evidence) == evidenceMaxPage {
			break
		}
	}
	monitorWritePage(w, r, "FoundationEvidencePage", page)
}

// evidencePublished maps one recorded item to the published object, without its monitor fields.
func evidencePublished(item Evidence) (wire.FoundationEvidence, bool) {
	evidence := wire.FoundationEvidence{Heads: []wire.FoundationLogHead{}}
	switch {
	case item.Kind == "rewritten_log" && len(item.Objects) == 2:
		var first, second wire.FoundationLogHead
		if json.Unmarshal(item.Objects[0], &first) != nil || json.Unmarshal(item.Objects[1], &second) != nil {
			return wire.FoundationEvidence{}, false
		}
		evidence.Kind, evidence.Heads = "log_rewrite", []wire.FoundationLogHead{first, second}
	case item.Kind == "head_after_closure" && len(item.Objects) == 2:
		var closure wire.FoundationKeyClosure
		var head wire.FoundationLogHead
		if json.Unmarshal(item.Objects[0], &closure) != nil || json.Unmarshal(item.Objects[1], &head) != nil {
			return wire.FoundationEvidence{}, false
		}
		evidence.Kind, evidence.Heads, evidence.Closure = "head_after_closure", []wire.FoundationLogHead{head}, &closure
	case item.Kind == "recipient_funded_deposit" && len(item.Objects) >= 1:
		var transfer faux.Transfer
		if json.Unmarshal(item.Objects[0], &transfer) != nil {
			return wire.FoundationEvidence{}, false
		}
		transaction := transfer.TxHash
		if transfer.OutputIndex != nil {
			transaction = transfer.TxID + ":" + strconv.FormatUint(uint64(*transfer.OutputIndex), 10)
		}
		if transaction == "" || transfer.From == "" {
			return wire.FoundationEvidence{}, false
		}
		evidence.Kind = "recipient_funded_deposit"
		evidence.Transfer = &wire.FoundationEvidenceTransfer{Network: transfer.Network, Transaction: transaction, Recipient: transfer.From}
	default:
		return wire.FoundationEvidence{}, false
	}
	return evidence, true
}
