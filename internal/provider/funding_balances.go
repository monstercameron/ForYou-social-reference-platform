package provider

import (
	"net/http"

	"foryou.local/foryou/internal/fee"
	"foryou.local/foryou/internal/funding"
	"foryou.local/foryou/internal/wire"
)

func fdHandleBalance(s *Service) http.HandlerFunc {
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
		balance, err := s.Ledger.Balance(authorID, network)
		if err != nil {
			fdWriteLedgerError(w, err)
			return
		}
		denomination, err := fee.Denomination(network)
		if err != nil {
			writeError(w, http.StatusNotFound, "not_found")
			return
		}
		status, err := (funding.Signer{Key: s.Key()}).SignBalance(wire.BalanceStatus{ProviderID: s.ProviderID(), AuthorID: authorID, Network: network, Denomination: denomination, Available: fdAmount(balance.Available), Frozen: fdAmount(balance.Frozen), Reserved: fdAmount(balance.Reserved), Revision: int64(balance.Revision), UpdatedAt: wire.FormatTime(balance.UpdatedAt)})
		if err != nil {
			writeError(w, http.StatusServiceUnavailable, "unavailable")
			return
		}
		fdWriteJSON(w, http.StatusOK, status)
	}
}
