package apierr

import "net/http"

// Routed serves mux and answers what it does not route the way the API catalog
// defines: an unknown path is 404 not_found in the public error body, and a
// method the route does not define is 405 with an Allow header and no body.
// Both carry Cache-Control: no-store.
func Routed(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler, pattern := mux.Handler(r)
		if pattern != "" {
			mux.ServeHTTP(w, r)
			return
		}
		unrouted := &unroutedResponse{header: http.Header{}}
		handler.ServeHTTP(unrouted, r)
		if allow := unrouted.header.Get("Allow"); unrouted.status == http.StatusMethodNotAllowed && allow != "" {
			w.Header().Set("Allow", allow)
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		Write(w, PublicReadError{Status: NotFound})
	})
}

// unroutedResponse records what the mux's own fallback handler would have sent.
type unroutedResponse struct {
	header http.Header
	status int
}

func (u *unroutedResponse) Header() http.Header { return u.header }

func (u *unroutedResponse) WriteHeader(status int) {
	if u.status == 0 {
		u.status = status
	}
}

func (u *unroutedResponse) Write(body []byte) (int, error) {
	if u.status == 0 {
		u.status = http.StatusOK
	}
	return len(body), nil
}
