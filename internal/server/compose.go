package server

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"unicode/utf8"
)

func init() { routeGroups = append(routeGroups, registerCompose) }

func registerCompose(a *App, mux *http.ServeMux) {
	mux.HandleFunc("GET /compose", a.composeHandler)
	mux.HandleFunc("GET /quote", a.quoteHandler)
	mux.HandleFunc("POST /quote", a.quoteHandler)
	mux.HandleFunc("GET /quote/fragment", a.quoteHandler)
	mux.HandleFunc("POST /quote/fragment", a.quoteHandler)
}

type quotePreview struct {
	Layout Layout
	Cost   postCost
	Bytes  int
	Text   string
	Token  string
}

func draftToken() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(id[:]), nil
}

func (a *App) composeHandler(w http.ResponseWriter, r *http.Request) {
	token, err := draftToken()
	if err != nil {
		http.Error(w, "unable to prepare draft", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	rememberTheme(w, r)
	a.page(w, r, "compose-page", quotePreview{Layout: a.layout(r, "compose", "New post"), Token: token})
}

func (a *App) quoteHandler(w http.ResponseWriter, r *http.Request) {
	text := r.URL.Query().Get("text")
	if r.Method == http.MethodPost {
		if !parseSmallForm(w, r, 50000) {
			return
		}
		text = r.PostForm.Get("text")
	}
	if !utf8.ValidString(text) || len(text) > 16000 || utf8.RuneCountInString(text) > 4000 {
		http.Error(w, "draft is too long or invalid", http.StatusBadRequest)
		return
	}
	cost, err := costOf(text)
	if err != nil {
		http.Error(w, "No fee quote is available.", http.StatusServiceUnavailable)
		return
	}
	preview := quotePreview{Layout: a.layout(r, "compose", "New post"), Cost: cost, Bytes: len(text), Text: text}
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
		a.page(w, r, "compose-page", preview)
		return
	}
	a.fragment(w, r, "compose-quote-response", preview)
}
