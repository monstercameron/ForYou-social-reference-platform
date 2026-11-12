package monitor

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/committee"
	"foryou.local/foryou/internal/keys"
	"foryou.local/foryou/internal/wire"
)

const monitorMaxRawQueryBytes = 16384

func init() {
	routeGroups = append(routeGroups, monitorRegisterWitnessRoutes)
}

func monitorRegisterWitnessRoutes(_ *Service, mux *http.ServeMux) {
	mux.HandleFunc("/v1/witnesses", monitorWitnesses)
}

func monitorWitnesses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		monitorMethodNotAllowed(w)
		return
	}
	query, ok := monitorParseQuery(r.URL.RawQuery, "provider_id", "network", "after_sequence")
	if !ok {
		apierr.Write(w, apierr.PublicReadError{Status: apierr.InvalidQuery})
		return
	}
	providerID, hasProvider := query["provider_id"]
	network, hasNetwork := query["network"]
	if !hasProvider || !hasNetwork || (network != wire.BitcoinMainnet && network != wire.EthereumMainnet) {
		apierr.Write(w, apierr.PublicReadError{Status: apierr.InvalidQuery})
		return
	}
	if _, err := keys.ParseDID(providerID); err != nil {
		apierr.Write(w, apierr.PublicReadError{Status: apierr.InvalidQuery})
		return
	}
	afterSequence := int64(0)
	if raw, exists := query["after_sequence"]; exists {
		if raw != "0" && (raw == "" || raw[0] < '1' || raw[0] > '9' || !monitorDigits(raw)) {
			apierr.Write(w, apierr.PublicReadError{Status: apierr.InvalidQuery})
			return
		}
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value > 1<<53-1 {
			apierr.Write(w, apierr.PublicReadError{Status: apierr.InvalidQuery})
			return
		}
		afterSequence = value
	}
	member := MemberFrom(r)
	if member == nil {
		monitorUnavailable(w)
		return
	}
	var page wire.MonitorWitnessesPage
	member.read(func(m *committee.Monitor) {
		page = m.Page(providerID, network, afterSequence, 1000)
	})
	if len(page.Witnesses) > 1000 || len(page.Refusals) > 1000 || len(page.Holds) > 1000 || len(page.Claims) > 1000 {
		monitorUnavailable(w)
		return
	}
	monitorWritePage(w, r, "MonitorWitnessesPage", page)
}

func monitorParseQuery(raw string, allowed ...string) (map[string]string, bool) {
	if len(raw) > monitorMaxRawQueryBytes || !monitorASCII(raw) {
		return nil, false
	}
	values := make(map[string]string, len(allowed))
	if raw == "" {
		return values, true
	}
	for _, component := range strings.Split(raw, "&") {
		name, encoded, found := strings.Cut(component, "=")
		if !found || name == "" || encoded == "" || !monitorAllowedName(name, allowed) {
			return nil, false
		}
		if _, exists := values[name]; exists {
			return nil, false
		}
		value, err := url.QueryUnescape(strings.ReplaceAll(encoded, "+", "%2B"))
		if err != nil || !utf8.ValidString(value) || value == "" || monitorEncodeValue(value) != encoded {
			return nil, false
		}
		values[name] = value
	}
	return values, true
}

func monitorAllowedName(name string, allowed []string) bool {
	for _, candidate := range allowed {
		if name == candidate {
			return true
		}
	}
	return false
}

func monitorEncodeValue(value string) string {
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

func monitorASCII(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] > 0x7f {
			return false
		}
	}
	return true
}

func monitorDigits(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func monitorMethodNotAllowed(w http.ResponseWriter) {
	w.Header().Set("Allow", "GET, HEAD")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusMethodNotAllowed)
}

func monitorUnavailable(w http.ResponseWriter) {
	apierr.Write(w, apierr.PublicReadError{Status: apierr.Unavailable, RetryAfterSeconds: apierr.DefaultRetryAfterSeconds})
}

func monitorWritePage(w http.ResponseWriter, r *http.Request, schema string, value any) {
	body, err := wire.Encode(schema, value)
	if err != nil {
		monitorUnavailable(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(http.StatusOK)
	if r.Method == http.MethodGet {
		_, _ = w.Write(body)
	}
}
