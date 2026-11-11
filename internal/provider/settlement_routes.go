package provider

import (
	"encoding/base64"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/canon"
	"foryou.local/foryou/internal/discovery"
	"foryou.local/foryou/internal/settlement"
	"foryou.local/foryou/internal/wire"
)

const settleMaxQueryBytes = 16384

func init() {
	routeGroups = append(routeGroups, settleRegisterRoutes)
}

func settleRegisterRoutes(s *Service, mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/settlement-claims", func(w http.ResponseWriter, r *http.Request) {
		settleClaimsPage(w, r, s)
	})
	mux.HandleFunc("GET /v1/settlement-claims/{claim_id}/leaves", func(w http.ResponseWriter, r *http.Request) {
		settleLeavesPage(w, r, s)
	})
	mux.HandleFunc("GET /v1/settlement-claims/{claim_id}/witnesses", func(w http.ResponseWriter, r *http.Request) {
		settleWitnessesPage(w, r, s)
	})
	mux.HandleFunc("POST /v1/settlement-witnesses", func(w http.ResponseWriter, r *http.Request) {
		settlePostWitness(w, r, s)
	})
	settleRegisterProof(s, mux)
}

func settleClaimsPage(w http.ResponseWriter, r *http.Request, s *Service) {
	w = settleHead(w, r)
	query, ok := settleRawQuery(r.URL.RawQuery, "network", "after_sequence")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_query")
		return
	}
	network, ok := query["network"]
	if !ok || network != wire.BitcoinMainnet && network != wire.EthereumMainnet {
		writeError(w, http.StatusBadRequest, "invalid_query")
		return
	}
	after := int64(0)
	if raw, present := query["after_sequence"]; present {
		value, valid := settleCanonicalNumber(raw)
		if !valid {
			writeError(w, http.StatusBadRequest, "invalid_query")
			return
		}
		after = int64(value)
	}
	if !s.LimitRead(w, r, ReadListing) {
		return
	}
	claims, err := s.Settlement().Claims(network, after, 100)
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	if claims == nil {
		claims = []wire.SettlementClaim{}
	}
	body, err := wire.Encode("SettlementClaimsPage", wire.SettlementClaimsPage{Claims: claims})
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	writeCanonical(w, http.StatusOK, body, "no-store")
}

func settleLeavesPage(w http.ResponseWriter, r *http.Request, s *Service) {
	w = settleHead(w, r)
	claimID := r.PathValue("claim_id")
	if !settleClaimID(claimID) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	query, ok := settleRawQuery(r.URL.RawQuery, "offset")
	if !ok {
		writeError(w, http.StatusBadRequest, "invalid_query")
		return
	}
	offset := uint64(0)
	if raw, present := query["offset"]; present {
		var valid bool
		offset, valid = settleCanonicalNumber(raw)
		if !valid {
			writeError(w, http.StatusBadRequest, "invalid_query")
			return
		}
	}
	claim, exists, err := s.Settlement().Claim(claimID)
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if !s.LimitRead(w, r, ReadListing) {
		return
	}
	leaves := []wire.SettlementLeaf{}
	if offset < uint64(claim.ReceiptCount) {
		leaves, err = s.Settlement().Leaves(claimID, int(offset), 2000)
		if err != nil {
			writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
			return
		}
	}
	body, err := wire.Encode("SettlementLeavesPage", wire.SettlementLeavesPage{ClaimID: claimID, Offset: int64(offset), Leaves: leaves})
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	writeCanonical(w, http.StatusOK, body, "no-store")
}

func settleWitnessesPage(w http.ResponseWriter, r *http.Request, s *Service) {
	w = settleHead(w, r)
	claimID := r.PathValue("claim_id")
	if !settleClaimID(claimID) {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if _, exists, err := s.Settlement().Claim(claimID); err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	} else if !exists {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if !s.LimitRead(w, r, ReadListing) {
		return
	}
	witnesses, err := s.Settlement().Witnesses(claimID)
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	if witnesses == nil {
		witnesses = []wire.SettlementWitness{}
	}
	if len(witnesses) > settleWitnessLimit {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	body, err := wire.Encode("SettlementWitnessesPage", wire.SettlementWitnessesPage{Witnesses: witnesses})
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	writeCanonical(w, http.StatusOK, body, "no-store")
}

func settlePostWitness(w http.ResponseWriter, r *http.Request, s *Service) {
	mediaType, _, typeErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if typeErr != nil || mediaType != "application/json" {
		writeError(w, http.StatusUnprocessableEntity, "invalid_message")
		return
	}
	body, err := decodeJSON(r, 16<<10, new(wire.SettlementWitness))
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_message")
		return
	}
	var witness wire.SettlementWitness
	if err := wire.DecodeLimits(body, canon.Limits{MaxBytes: 16 << 10, MaxDepth: 32}, "SettlementWitness", &witness); err != nil {
		writeError(w, http.StatusUnprocessableEntity, "invalid_message")
		return
	}
	claim, exists, err := s.Settlement().Claim(witness.ClaimID)
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	if !exists {
		writeError(w, http.StatusNotFound, "not_found")
		return
	}
	if settlement.VerifyWitness(witness) != nil || !settlement.WitnessMatches(witness, claim) || !settleKnownMonitor(s, witness.WitnessID) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_message")
		return
	}
	held, err := s.Settlement().Witnesses(claim.ClaimID)
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	if len(held) >= settleWitnessLimit && !settleHasWitness(held, witness.WitnessID) {
		writeError(w, http.StatusUnprocessableEntity, "invalid_message")
		return
	}
	created, err := s.Settlement().AddWitness(witness)
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	if !created {
		witnesses, err := s.Settlement().Witnesses(claim.ClaimID)
		if err != nil {
			writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
			return
		}
		for _, held := range witnesses {
			if held.WitnessID == witness.WitnessID {
				witness = held
				break
			}
		}
	}
	response, err := wire.Encode("SettlementWitness", witness)
	if err != nil {
		writeRetry(w, http.StatusServiceUnavailable, "unavailable", apierr.DefaultRetryAfterSeconds)
		return
	}
	status := http.StatusCreated
	if !created {
		status = http.StatusOK
	}
	writeCanonical(w, status, response, "no-store")
}

// settleKnownMonitor admits only monitors named by distribution policies already known to this provider.
// The specification does not require this; it keeps the bounded witness page to committee witnesses,
// so that keys outside every committee cannot fill a claim's 512 witness slots.
func settleKnownMonitor(s *Service, witnessID string) bool {
	for _, policy := range s.Policies() {
		for _, monitorID := range policy.Monitors {
			if witnessID == monitorID {
				return true
			}
		}
	}
	return false
}

func settleHasWitness(witnesses []wire.SettlementWitness, witnessID string) bool {
	for _, witness := range witnesses {
		if witness.WitnessID == witnessID {
			return true
		}
	}
	return false
}

func settleRawQuery(raw string, supported ...string) (map[string]string, bool) {
	if len(raw) > settleMaxQueryBytes {
		return nil, false
	}
	result := make(map[string]string, len(supported))
	if raw == "" {
		return result, true
	}
	for _, part := range strings.Split(raw, "&") {
		if part == "" || strings.Count(part, "=") != 1 {
			return nil, false
		}
		name, value, _ := strings.Cut(part, "=")
		if name == "" || value == "" || !settleSupportedParam(name, supported) {
			return nil, false
		}
		if _, duplicate := result[name]; duplicate || strings.ContainsAny(name+value, "+%\t\r\n ") {
			return nil, false
		}
		result[name] = value
	}
	return result, true
}

func settleSupportedParam(name string, supported []string) bool {
	for _, candidate := range supported {
		if name == candidate {
			return true
		}
	}
	return false
}

func settleCanonicalNumber(raw string) (uint64, bool) {
	value, err := strconv.ParseUint(raw, 10, 53)
	return value, err == nil && strconv.FormatUint(value, 10) == raw && value <= discovery.MaxAfter
}

func settleClaimID(id string) bool {
	if len(id) != 70 || !strings.HasPrefix(id, "fysc2:") {
		return false
	}
	for _, char := range id[6:] {
		if !(char >= '0' && char <= '9' || char >= 'a' && char <= 'f') {
			return false
		}
	}
	return true
}

func settleReceiptID(id string) bool {
	if len(id) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(id)
	return err == nil && len(decoded) == 32 && base64.RawURLEncoding.EncodeToString(decoded) == id
}

func settleHead(w http.ResponseWriter, r *http.Request) http.ResponseWriter {
	return rdHeadWriter{ResponseWriter: w, head: r.Method == http.MethodHead}
}
