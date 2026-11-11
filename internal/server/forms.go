package server

import (
	"net/http"
	"net/url"
)

const actionFormLimit = 16384

// readActionForm parses the small same-origin form behind a post control and
// answers the client itself when the form is refused. Every named field must
// appear exactly once.
func readActionForm(w http.ResponseWriter, r *http.Request, fields ...string) (url.Values, bool) {
	if !requireSameOrigin(w, r) {
		return nil, false
	}
	if !parseSmallForm(w, r, actionFormLimit) {
		return nil, false
	}
	valid := true
	for _, f := range fields {
		valid = valid && len(r.PostForm[f]) == 1
	}
	if !valid {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return nil, false
	}
	return r.PostForm, true
}
