package foundation

import (
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"foryou.local/foryou/internal/apierr"
	"foryou.local/foryou/internal/governance"
	"foryou.local/foryou/internal/wire"
)

const (
	logRoutePageLimit      = 1000
	logRouteMaxSafeInteger = int64(1<<53 - 1)
	// logRouteDefaultAfterSize is zero so omission returns signed heads from the first.
	logRouteDefaultAfterSize = int64(0)
)

func init() {
	routeGroups = append(routeGroups, logRouteRegisterRoutes)
}

func logRouteRegisterRoutes(s *Service, mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/foundation/log", func(w http.ResponseWriter, r *http.Request) {
		query, ok := pubParseQuery(w, r, "after")
		if !ok {
			return
		}
		after := int64(-1)
		if raw, exists := query["after"]; exists {
			parsed, valid := logRouteDecimal(raw, 0, logRouteMaxSafeInteger)
			if !valid {
				logRouteInvalidQuery(w)
				return
			}
			after = parsed
		}
		var head *wire.FoundationLogHead
		if latest, exists := s.LatestHead(); exists {
			head = &latest
		}
		pubWrite(w, "FoundationLogPage", wire.FoundationLogPage{Entries: s.LogEntries(after, logRoutePageLimit), Head: head})
	})
	mux.HandleFunc("GET /v1/foundation/log/heads", func(w http.ResponseWriter, r *http.Request) {
		query, ok := pubParseQuery(w, r, "after_size")
		if !ok {
			return
		}
		afterSize := logRouteDefaultAfterSize
		if raw, exists := query["after_size"]; exists {
			parsed, valid := logRouteDecimal(raw, 0, logRouteMaxSafeInteger)
			if !valid {
				logRouteInvalidQuery(w)
				return
			}
			afterSize = parsed
		}
		heads := make([]wire.FoundationLogHead, 0)
		for _, head := range s.SignedHeads() {
			if head.Size > afterSize && len(heads) < logRoutePageLimit {
				heads = append(heads, head)
			}
		}
		pubWrite(w, "FoundationLogHeadsPage", wire.FoundationLogHeadsPage{Heads: heads})
	})
	mux.HandleFunc("GET /v1/foundation/log/proof/{object_hash}", func(w http.ResponseWriter, r *http.Request) {
		query, ok := pubParseQuery(w, r, "size")
		if !ok {
			return
		}
		objectHash := r.PathValue("object_hash")
		rawSize, exists := query["size"]
		size, valid := logRouteDecimal(rawSize, 1, logRouteMaxSafeInteger)
		if !exists || !valid || !logRouteHash(objectHash) || size > s.log.Len() {
			logRouteInvalidQuery(w)
			return
		}
		proof, err := s.log.Proof(objectHash, size)
		if errors.Is(err, governance.ErrNotFound) {
			apierr.Write(w, apierr.PublicReadError{Status: apierr.NotFound})
			return
		}
		if err != nil {
			pubUnavailable(w)
			return
		}
		pubWrite(w, "FoundationLogProof", proof)
	})
	mux.HandleFunc("GET /v1/foundation/log/objects/{object_hash}", func(w http.ResponseWriter, r *http.Request) {
		if !pubNoQuery(w, r) {
			return
		}
		objectHash := r.PathValue("object_hash")
		object, exists := s.LogObject(objectHash)
		if !exists || !logRouteHash(objectHash) {
			apierr.Write(w, apierr.PublicReadError{Status: apierr.NotFound})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(object)
	})
}

func logRouteDecimal(value string, minimum, maximum int64) (int64, bool) {
	if value == "" || value[0] == '0' && len(value) != 1 {
		return 0, false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return 0, false
		}
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	return parsed, err == nil && parsed >= minimum && parsed <= maximum
}

func logRouteHash(value string) bool {
	if len(value) != 64 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func logRouteInvalidQuery(w http.ResponseWriter) {
	apierr.Write(w, apierr.PublicReadError{Status: apierr.InvalidQuery})
}
