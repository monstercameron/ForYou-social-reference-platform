package foundation

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

const (
	// pubStatementWindowQuarters bounds distribution history while exceeding the eight-quarter minimum.
	pubStatementWindowQuarters = 32
	// pubStatusWindowQuarters bounds status history while exceeding the eight-quarter minimum.
	pubStatusWindowQuarters = 64
)

type pubDistributionPage struct {
	Policies   []wire.DistributionPolicy `json:"policies"`
	Statements []json.RawMessage         `json:"statements"`
}

type pubVaultStatusPage struct {
	Statuses []json.RawMessage `json:"statuses"`
}

func init() {
	routeGroups = append(routeGroups, pubRegisterRoutes)
}

func pubRegisterRoutes(s *Service, mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/foundation/genesis", func(w http.ResponseWriter, r *http.Request) {
		if !pubNoQuery(w, r) {
			return
		}
		pubWrite(w, "GenesisManifest", s.cfg.Genesis)
	})
	mux.HandleFunc("GET /v1/foundation/certificates", func(w http.ResponseWriter, r *http.Request) {
		if !pubNoQuery(w, r) {
			return
		}
		issuedCertificates := certificatesAt(s.cfg, s.Clock())
		certificates := make([]wire.FoundationKeyCertificate, 0, len(issuedCertificates))
		seen := make(map[string]struct{}, len(issuedCertificates))
		for _, genesisCertificate := range s.cfg.Genesis.FoundationKeyCertificates {
			if _, exists := seen[genesisCertificate.CertificateID]; exists {
				continue
			}
			seen[genesisCertificate.CertificateID] = struct{}{}
			certificates = append(certificates, genesisCertificate)
		}
		otherCertificates := make([]wire.FoundationKeyCertificate, 0, len(issuedCertificates))
		for _, certificate := range issuedCertificates {
			if _, exists := seen[certificate.CertificateID]; exists {
				continue
			}
			seen[certificate.CertificateID] = struct{}{}
			otherCertificates = append(otherCertificates, certificate)
		}
		sort.SliceStable(otherCertificates, func(i, j int) bool { return otherCertificates[i].IssuedAt < otherCertificates[j].IssuedAt })
		certificates = append(certificates, otherCertificates...)
		closures := append([]wire.FoundationKeyClosure{}, s.cfg.Closures...)
		sort.Slice(closures, func(i, j int) bool { return closures[i].ClosedAt < closures[j].ClosedAt })
		updates := append([]wire.ReleaseKeySetUpdate{}, s.cfg.KeySetUpdates...)
		sort.Slice(updates, func(i, j int) bool { return updates[i].Sequence < updates[j].Sequence })
		if len(certificates) > 64 || len(closures) > 64 || len(updates) > 64 {
			pubUnavailable(w)
			return
		}
		pubWrite(w, "FoundationCertificatesPage", wire.FoundationCertificatesPage{Certificates: certificates, Closures: closures, KeySetUpdates: updates})
	})
	mux.HandleFunc("GET /v1/foundation/fee-schedules", func(w http.ResponseWriter, r *http.Request) {
		if !pubNoQuery(w, r) {
			return
		}
		schedules := s.FeeSchedules()
		sort.Slice(schedules, func(i, j int) bool {
			if schedules[i].Schedule.Network == schedules[j].Schedule.Network {
				return schedules[i].Schedule.Revision < schedules[j].Schedule.Revision
			}
			return schedules[i].Schedule.Network < schedules[j].Schedule.Network
		})
		if len(schedules) > 1000 {
			pubUnavailable(w)
			return
		}
		pubWrite(w, "FeeSchedulesPage", wire.FeeSchedulesPage{Schedules: schedules})
	})
	mux.HandleFunc("GET /v1/foundation/distribution", func(w http.ResponseWriter, r *http.Request) {
		if !pubNoQuery(w, r) {
			return
		}
		policies := s.Policies()
		sort.SliceStable(policies, func(i, j int) bool { return policies[i].Revision < policies[j].Revision })
		if len(policies) > 64 {
			pubUnavailable(w)
			return
		}
		page := pubDistributionPage{Policies: policies, Statements: make([]json.RawMessage, 0, pubStatementWindowQuarters*2)}
		periods := make([]string, 0, pubStatementWindowQuarters)
		start, _ := s.Calendar().Quarter(s.Clock())
		for i := 0; i < pubStatementWindowQuarters && !start.IsZero(); i++ {
			periods = append(periods, wire.FormatTime(start))
			start, _ = s.Calendar().Quarter(start.Add(-time.Second))
		}
		for i := len(periods) - 1; i >= 0; i-- {
			for _, network := range []string{wire.BitcoinMainnet, wire.EthereumMainnet} {
				if raw, ok := s.DistributionStatement(network, periods[i]); ok {
					page.Statements = append(page.Statements, json.RawMessage(raw))
				}
			}
		}
		if len(page.Statements) > 64 {
			pubUnavailable(w)
			return
		}
		pubWrite(w, "DistributionPage", page)
	})
	mux.HandleFunc("GET /v1/foundation/vault-status", func(w http.ResponseWriter, r *http.Request) {
		if !pubNoQuery(w, r) {
			return
		}
		page := pubVaultStatusPage{Statuses: make([]json.RawMessage, 0, pubStatusWindowQuarters)}
		start, _ := s.Calendar().Quarter(s.Clock())
		for i := 0; i < pubStatusWindowQuarters && !start.IsZero(); i++ {
			if raw, ok := s.VaultStatus(wire.FormatTime(start)); ok {
				page.Statuses = append(page.Statuses, json.RawMessage(raw))
			}
			start, _ = s.Calendar().Quarter(start.Add(-time.Second))
		}
		pubReverseRaw(page.Statuses)
		pubWrite(w, "VaultStatusPage", page)
	})
	mux.HandleFunc("GET /v1/foundation/quality-reports", func(w http.ResponseWriter, r *http.Request) {
		query, ok := pubParseQuery(w, r, "provider_id", "quarter_end")
		if !ok {
			return
		}
		providerID, hasProvider := query["provider_id"]
		quarterText, hasQuarter := query["quarter_end"]
		_, providerErr := keys.ParseDID(providerID)
		quarterEnd, timeErr := wire.ParseTime(quarterText)
		if !hasProvider || !hasQuarter || providerErr != nil || timeErr != nil || wire.FormatTime(quarterEnd) != quarterText || !s.Calendar().IsQuarterEnd(quarterEnd) {
			apierr.Write(w, apierr.PublicReadError{Status: apierr.InvalidQuery})
			return
		}
		reports := s.QualityReports(providerID, quarterText)
		if reports == nil {
			reports = []wire.QualityReport{}
		}
		if len(reports) > 4096 {
			pubUnavailable(w)
			return
		}
		pubWrite(w, "QualityReportsPage", wire.QualityReportsPage{Reports: reports})
	})
}

func pubNoQuery(w http.ResponseWriter, r *http.Request) bool {
	if r.URL.RawQuery != "" {
		apierr.Write(w, apierr.PublicReadError{Status: apierr.InvalidQuery})
		return false
	}
	return true
}

func pubParseQuery(w http.ResponseWriter, r *http.Request, allowed ...string) (map[string]string, bool) {
	raw := r.URL.RawQuery
	if len(raw) > 16384 || !pubIsASCII(raw) {
		apierr.Write(w, apierr.PublicReadError{Status: apierr.InvalidQuery})
		return nil, false
	}
	values := make(map[string]string, len(allowed))
	if raw == "" {
		return values, true
	}
	for _, component := range strings.Split(raw, "&") {
		name, encoded, found := strings.Cut(component, "=")
		if !found || name == "" || encoded == "" || !pubAllowedName(name, allowed) {
			apierr.Write(w, apierr.PublicReadError{Status: apierr.InvalidQuery})
			return nil, false
		}
		if _, exists := values[name]; exists {
			apierr.Write(w, apierr.PublicReadError{Status: apierr.InvalidQuery})
			return nil, false
		}
		value, err := pubDecodeValue(encoded)
		if err != nil || value == "" || pubEncodeValue(value) != encoded {
			apierr.Write(w, apierr.PublicReadError{Status: apierr.InvalidQuery})
			return nil, false
		}
		values[name] = value
	}
	return values, true
}

func pubAllowedName(name string, allowed []string) bool {
	for _, candidate := range allowed {
		if name == candidate {
			return true
		}
	}
	return false
}

func pubDecodeValue(encoded string) (string, error) {
	decoded := make([]byte, 0, len(encoded))
	for i := 0; i < len(encoded); i++ {
		if encoded[i] != '%' {
			decoded = append(decoded, encoded[i])
			continue
		}
		if i+2 >= len(encoded) {
			return "", pubErrInvalidQuery
		}
		pair := encoded[i+1 : i+3]
		b, err := hex.DecodeString(pair)
		if err != nil {
			return "", pubErrInvalidQuery
		}
		decoded = append(decoded, b[0])
		i += 2
	}
	if !pubIsASCII(string(decoded)) {
		return "", pubErrInvalidQuery
	}
	return string(decoded), nil
}

func pubEncodeValue(value string) string {
	const digits = "0123456789ABCDEF"
	var encoded strings.Builder
	for i := 0; i < len(value); i++ {
		b := value[i]
		if b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' || b >= '0' && b <= '9' || strings.ContainsRune("-._~", rune(b)) {
			encoded.WriteByte(b)
			continue
		}
		encoded.WriteByte('%')
		encoded.WriteByte(digits[b>>4])
		encoded.WriteByte(digits[b&15])
	}
	return encoded.String()
}

func pubWrite(w http.ResponseWriter, schema string, value any) {
	body, err := wire.Encode(schema, value)
	if err != nil {
		pubUnavailable(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func pubUnavailable(w http.ResponseWriter) {
	apierr.Write(w, apierr.PublicReadError{Status: apierr.Unavailable, RetryAfterSeconds: apierr.DefaultRetryAfterSeconds})
}

func pubReverseRaw(values []json.RawMessage) {
	for i, j := 0, len(values)-1; i < j; i, j = i+1, j-1 {
		values[i], values[j] = values[j], values[i]
	}
}

func pubIsASCII(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] > 0x7f {
			return false
		}
	}
	return true
}

var pubErrInvalidQuery = errors.New("invalid publication query")
