package faux

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
)

// NewHandler returns the open dev-only HTTP API for a faux ledger.
func NewHandler(l *Ledger) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /faux/v1/transfers", func(w http.ResponseWriter, r *http.Request) {
		var req SubmitRequest
		if !decodeBody(w, r, &req) {
			return
		}
		transfer, replay, err := l.submit(r.Context(), req)
		if err != nil {
			writeError(w, err)
			return
		}
		status := http.StatusCreated
		if replay {
			status = http.StatusOK
		}
		writeJSON(w, status, transfer)
	})
	mux.HandleFunc("GET /faux/v1/transfers", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		limit := 100
		if query.Has("limit") {
			raw := query.Get("limit")
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed < 1 || parsed > 1000 {
				writeError(w, ErrInvalid)
				return
			}
			limit = parsed
		}
		after := uint64(0)
		if query.Has("after") {
			raw := query.Get("after")
			parsed, err := strconv.ParseUint(raw, 10, 64)
			if err != nil {
				writeError(w, ErrInvalid)
				return
			}
			after = parsed
		}
		items, err := l.transfersAfter(r.Context(), Query{Network: query.Get("network"), Recipient: query.Get("recipient"), Data: query.Get("data"), After: after, Limit: limit + 1}, true)
		if err != nil {
			writeError(w, err)
			return
		}
		var next any
		if len(items) > limit {
			items = items[:limit]
			next = items[len(items)-1].Sequence
		}
		writeJSON(w, http.StatusOK, map[string]any{"transfers": items, "next_after": next})
	})
	mux.HandleFunc("GET /faux/v1/transfers/{network}/{id}", func(w http.ResponseWriter, r *http.Request) {
		transfer, err := l.Transfer(r.Context(), r.PathValue("network"), r.PathValue("id"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, transfer)
	})
	mux.HandleFunc("POST /faux/v1/blocks", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Network string `json:"network"`
			Count   int    `json:"count"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		tip, err := l.Mine(r.Context(), body.Network, body.Count)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, tip)
	})
	mux.HandleFunc("GET /faux/v1/chains/{network}", func(w http.ResponseWriter, r *http.Request) {
		tip, err := l.Tip(r.Context(), r.PathValue("network"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, tip)
	})
	mux.HandleFunc("POST /faux/v1/faucet", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Network   string `json:"network"`
			Recipient string `json:"recipient"`
			Amount    string `json:"amount_chain_units,omitempty"`
		}
		if !decodeBody(w, r, &body) {
			return
		}
		transfer, err := l.Faucet(r.Context(), body.Network, body.Recipient, body.Amount)
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusCreated, transfer)
	})
	mux.HandleFunc("GET /faux/v1/addresses/{network}/{address}", func(w http.ResponseWriter, r *http.Request) {
		totals, err := l.Address(r.Context(), r.PathValue("network"), r.PathValue("address"))
		if err != nil {
			writeError(w, err)
			return
		}
		writeJSON(w, http.StatusOK, totals)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, ErrNotFound)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("ForYou-Faux", "dev-only")
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, 4096)
		}
		mux.ServeHTTP(w, r)
	})
}

func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		writeError(w, err)
		return false
	}
	if decoder.Decode(new(any)) != io.EOF {
		writeError(w, ErrInvalid)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, err error) {
	status, code := http.StatusBadRequest, "bad_request"
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		status, code = http.StatusRequestEntityTooLarge, "too_large"
	case errors.Is(err, ErrNotFound):
		status, code = http.StatusNotFound, "not_found"
	case errors.Is(err, ErrConflict):
		status, code = http.StatusConflict, "conflict"
	case errors.Is(err, ErrFull):
		status, code = http.StatusInsufficientStorage, "full"
	case errors.Is(err, ErrUnavailable):
		status, code = http.StatusServiceUnavailable, "unavailable"
	}
	writeJSON(w, status, map[string]string{"error": code})
}
