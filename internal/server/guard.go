package server

import (
	"mime"
	"net/http"
	"net/url"
)

func requireSameOrigin(w http.ResponseWriter, r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil || u.Host != r.Host || (u.Scheme != "http" && u.Scheme != "https") {
			http.Error(w, "invalid origin", http.StatusForbidden)
			return false
		}
	}
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		http.Error(w, "invalid origin", http.StatusForbidden)
		return false
	}
	return true
}

func parseSmallForm(w http.ResponseWriter, r *http.Request, limit int64) bool {
	mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || (mediaType != "application/x-www-form-urlencoded" && mediaType != "multipart/form-data") {
		http.Error(w, "unsupported form format", http.StatusUnsupportedMediaType)
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, limit)
	if mediaType == "multipart/form-data" {
		if err := r.ParseMultipartForm(limit); err != nil {
			http.Error(w, "invalid form", http.StatusBadRequest)
			return false
		}
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
			if len(r.MultipartForm.File) != 0 {
				http.Error(w, "invalid form", http.StatusBadRequest)
				return false
			}
		}
	} else if err := r.ParseForm(); err != nil {
		http.Error(w, "invalid form", http.StatusBadRequest)
		return false
	}
	return true
}
