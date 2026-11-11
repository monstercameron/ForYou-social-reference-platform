package server

import (
	"mime"
	"net/http"
	"net/url"
)

const actionFormLimit = 16384

// readActionForm parses the small same-origin form behind a post control and
// answers the client itself when the form is refused. Every named field must
// appear exactly once.
func readActionForm(w http.ResponseWriter, r *http.Request, fields ...string) (url.Values, bool) {
	if !sameOriginFeedback(r) {
		http.Error(w, "invalid origin", http.StatusForbidden)
		return nil, false
	}
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (mediaType != "application/x-www-form-urlencoded" && mediaType != "multipart/form-data") {
		http.Error(w, "invalid form format", http.StatusUnsupportedMediaType)
		return nil, false
	}
	r.Body = http.MaxBytesReader(w, r.Body, actionFormLimit)
	if mediaType == "multipart/form-data" {
		err = r.ParseMultipartForm(actionFormLimit)
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
		}
	} else {
		err = r.ParseForm()
	}
	valid := err == nil && (r.MultipartForm == nil || len(r.MultipartForm.File) == 0)
	for _, f := range fields {
		valid = valid && len(r.PostForm[f]) == 1
	}
	if !valid {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return nil, false
	}
	return r.PostForm, true
}
