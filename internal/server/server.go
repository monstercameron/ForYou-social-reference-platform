// Package server provides the client web application and demo service roles.
package server

import (
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

//go:embed templates static
var templates embed.FS

// Client is the HTTP service for the browser client.
type Client struct {
	app     *App
	handler http.Handler
}

type notFoundPage struct {
	Layout Layout
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

// NewClient creates the client web service and its private local data directory.
func NewClient(cfg Config) (*Client, error) {
	if cfg.DataDir == "" {
		return nil, fmt.Errorf("client data directory is required")
	}
	if err := os.MkdirAll(cfg.DataDir, 0700); err != nil {
		return nil, fmt.Errorf("create client data directory: %w", err)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	parsed, err := template.New("root").ParseFS(templates, "templates/*/*.html")
	if err != nil {
		return nil, fmt.Errorf("parse client templates: %w", err)
	}
	app := &App{config: cfg, templates: parsed, profiles: &profileStore{root: filepath.Join(cfg.DataDir, "profiles")}, store: &demoStore{}}
	mux := http.NewServeMux()
	for _, register := range routeGroups {
		register(app, mux)
	}
	registerLandingPages(app, mux)
	assets, err := fs.Sub(templates, "static")
	if err != nil {
		return nil, err
	}
	notFoundRequest, _ := http.NewRequest(http.MethodGet, "/__not_found__", nil)
	_, notFoundPattern := mux.Handler(notFoundRequest)
	if notFoundPattern != "GET /" {
		mux.HandleFunc("GET /", func(w http.ResponseWriter, r *http.Request) {
			app.renderStatus(w, r, "layout-error-page", notFoundPage{Layout: app.layout(r, "", "Page not found")}, http.StatusNotFound)
		})
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"role": "client", "status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if appPing := app.readiness; appPing != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if err := appPing(ctx); err != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"role": "client", "database": "unavailable"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"role": "client", "database": "ready"})
			return
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"role": "client", "database": "not_configured"})
	})
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(assets))))
	return &Client{app: app, handler: compress(mux)}, nil
}

// Handler returns the client's HTTP handler.
func (c *Client) Handler() http.Handler { return c.handler }

// Run returns immediately because the client service has no background work yet.
func (c *Client) Run(context.Context) error { return nil }

// RunOnce performs one service maintenance cycle.
func (c *Client) RunOnce(context.Context) error { return nil }

// Close releases resources owned by the client service.
func (c *Client) Close() error { return nil }

// NewRole constructs the selected demo role, using PostgreSQL only for readiness.
func NewRole(role string, db *sql.DB) http.Handler {
	var ping func(context.Context) error
	if db != nil {
		ping = db.PingContext
	}
	if role == "client" {
		client, err := NewClient(Config{DataDir: filepath.Join(".local", "data", "client"), Dev: os.Getenv("FORYOU_DEV") == "1", PublicOrigin: os.Getenv("FORYOU_PUBLIC_ORIGIN")})
		if err != nil {
			panic(fmt.Errorf("create client role: %w", err))
		}
		client.app.readiness = ping
		return client.Handler()
	}
	return newHandler(role, ping)
}

func newHandler(role string, ping func(context.Context) error) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"role": role, "service": "foryou", "status": "demo"})
	})
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
	return compress(mux)
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
