package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"mime"
	"net/http"
	"unicode/utf8"
)

type quotePreview struct {
	Bytes int
	Text  string
	Token string
	Theme string
}

func draftToken() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

func composeHandler(w http.ResponseWriter, r *http.Request) {
	token, err := draftToken()
	if err != nil {
		http.Error(w, "unable to prepare draft", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = home.ExecuteTemplate(w, "compose-page", quotePreview{Token: token, Theme: requestTheme(r)})
}

func quoteHandler(w http.ResponseWriter, r *http.Request) {
	text := r.URL.Query().Get("text")
	if r.Method == http.MethodPost {
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || (mediaType != "multipart/form-data" && mediaType != "application/x-www-form-urlencoded") {
			http.Error(w, "unsupported draft format", http.StatusUnsupportedMediaType)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 50000)
		if err := r.ParseMultipartForm(20000); err != nil && !errors.Is(err, http.ErrNotMultipart) {
			http.Error(w, "invalid draft", http.StatusBadRequest)
			return
		}
		if r.MultipartForm != nil {
			defer r.MultipartForm.RemoveAll()
			if len(r.MultipartForm.File) != 0 {
				http.Error(w, "attachments are not supported", http.StatusBadRequest)
				return
			}
		}
		text = r.PostForm.Get("text")
	}
	if !utf8.ValidString(text) || len(text) > 16000 || utf8.RuneCountInString(text) > 4000 {
		http.Error(w, "draft is too long or invalid", http.StatusBadRequest)
		return
	}
	preview := quotePreview{Bytes: len(text), Text: text, Theme: requestTheme(r)}
	if r.URL.Path == "/quote" {
		token, err := draftToken()
		if err != nil {
			http.Error(w, "unable to prepare draft", http.StatusServiceUnavailable)
			return
		}
		preview.Token = token
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.URL.Path == "/quote" {
		_ = home.ExecuteTemplate(w, "compose-page", preview)
		return
	}
	_ = home.ExecuteTemplate(w, "quote-response", preview)
}
