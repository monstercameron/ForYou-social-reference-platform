package provider

import (
	"net/http"
	"strconv"

	"foryou.local/foryou/internal/funding"
	"foryou.local/foryou/internal/wire"
)

func init() {
	routeGroups = append(routeGroups, func(s *Service, mux *http.ServeMux) {
		mux.HandleFunc("GET /v1/account-history/{network}/{after}", fdHandleHistory(s))
	})
}

// fdHandleHistory serves the author's own signed account history after the sequence
// named in the path. The page is recomputed from the ledger, so a retry gets the
// same entries.
func fdHandleHistory(s *Service) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		authorID, ok := fdAuthenticateEmpty(w, r, s)
		if !ok {
			return
		}
		network := r.PathValue("network")
		if network != wire.BitcoinMainnet && network != wire.EthereumMainnet {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		position := r.PathValue("after")
		after, err := strconv.ParseUint(position, 10, 53)
		if err != nil || strconv.FormatUint(after, 10) != position {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		entries, more, err := s.Ledger.History(authorID, network, after)
		if err != nil {
			fdWriteLedgerError(w, err)
			return
		}
		page := wire.AccountHistoryPage{ProviderID: s.ProviderID(), AuthorID: authorID, Network: network, Entries: make([]wire.AccountHistoryEntry, len(entries)), More: more}
		for i, entry := range entries {
			page.Entries[i] = wire.AccountHistoryEntry{Sequence: int64(entry.Sequence), At: wire.FormatTime(entry.At), Kind: string(entry.Kind), AmountBaseUnits: fdAmount(entry.Amount), Reference: entry.Reference}
		}
		signed, err := (funding.Signer{Key: s.Key()}).SignHistory(page)
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		fdWriteJSON(w, http.StatusOK, signed)
	}
}
