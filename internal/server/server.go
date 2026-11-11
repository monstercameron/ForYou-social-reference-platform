// Package server provides the HTTP handlers shared by the demo roles.
package server

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"html/template"
	"io/fs"
	"net/http"
	"time"
)

//go:embed templates/*.html static
var templates embed.FS

var home = template.Must(template.ParseFS(templates, "templates/*.html"))

type notFoundPage struct {
	Theme   string
	Section string
}

// DefaultAddress returns a loopback address for each independent demo process.
func DefaultAddress(role string) (string, bool) {
	switch role {
	case "client":
		return "127.0.0.1:8443", true
	case "foundation":
		return "127.0.0.1:8444", true
	case "provider":
		return "127.0.0.1:8445", true
	case "replica":
		return "127.0.0.1:8446", true
	default:
		return "", false
	}
}

// NewRole uses a PostgreSQL connection for readiness, when configured.
func NewRole(role string, db *sql.DB) http.Handler {
	var ping func(context.Context) error
	if db != nil {
		ping = db.PingContext
	}
	return newHandler(role, ping)
}

func newHandler(role string, ping func(context.Context) error) http.Handler {
	mux := http.NewServeMux()
	store := &demoStore{}
	if role == "client" {
		assets, err := fs.Sub(templates, "static")
		if err != nil {
			panic(err)
		}
		mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(assets))))
		mux.HandleFunc("GET /feed", feedHandler)
		mux.HandleFunc("GET /feed/search", searchHandler)
		mux.HandleFunc("GET /feed/more", moreHandler)
		mux.HandleFunc("GET /explore", exploreHandler)
		mux.HandleFunc("GET /explore/more", exploreMoreHandler)
		mux.HandleFunc("GET /settings", settingsHandler)
		mux.HandleFunc("POST /feedback", feedbackHandler)
		mux.HandleFunc("POST /feedback/fragment", feedbackHandler)
		mux.HandleFunc("POST /feedback/clear", feedbackClearHandler)
		mux.HandleFunc("GET /thread", threadPageHandler)
		mux.HandleFunc("GET /thread/fragment", threadFragmentHandler)
		mux.HandleFunc("GET /posts/{id}", postPageHandler)
		mux.HandleFunc("GET /embed/{id}", embedPageHandler)
		mux.HandleFunc("GET /share", sharePageHandler)
		mux.HandleFunc("GET /share/fragment", shareFragmentHandler)
		mux.HandleFunc("GET /gallery", galleryPageHandler)
		mux.HandleFunc("GET /gallery/fragment", galleryFragmentHandler)
		mux.HandleFunc("GET /compose", composeHandler)
		mux.HandleFunc("POST /compose", publishDemo)
		mux.HandleFunc("POST /compose/fragment", publishDemo)
		mux.HandleFunc("GET /quote", quoteHandler)
		mux.HandleFunc("POST /quote", quoteHandler)
		mux.HandleFunc("GET /quote/fragment", quoteHandler)
		mux.HandleFunc("POST /quote/fragment", quoteHandler)
	}
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		if role != "client" {
			writeJSON(w, http.StatusOK, map[string]string{"role": role, "service": "foryou", "status": "demo"})
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		page, ok := readFeedRequest(r, false)
		if !ok {
			http.Error(w, "invalid feed filter or search", http.StatusBadRequest)
			return
		}
		rememberTheme(w, r)
		w.Header().Set("Cache-Control", "private, no-store")
		if err := home.ExecuteTemplate(w, "home.html", page); err != nil {
			return
		}
	})
	if role == "client" {
		mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Header().Set("Cache-Control", "no-store")
			w.WriteHeader(http.StatusNotFound)
			_ = home.ExecuteTemplate(w, "not-found-page", notFoundPage{Theme: requestTheme(r)})
		})
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"role": role, "status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if ping == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"role": role, "database": "not_configured"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := ping(ctx); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"role": role, "database": "unavailable"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"role": role, "database": "ready"})
	})
	return compress(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), demoStoreKey{}, store)))
	}))
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
