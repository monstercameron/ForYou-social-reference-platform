package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"time"

	"foryou.local/foryou/internal/server"
	_ "github.com/jackc/pgx/v5/stdlib"
)

type demoService struct {
	handler http.Handler
	db      *sql.DB
}

func demoBuilders() map[string]builder {
	return map[string]builder{
		"client": buildDemo, "foundation": buildDemo, "provider": buildDemo,
		"replica": buildDemo, "monitor": buildDemo, "faux": buildDemo,
	}
}

func buildDemo(config roleConfig) (service, error) {
	var db *sql.DB
	if config.DatabaseURL != "" {
		var err error
		db, err = sql.Open("pgx", config.DatabaseURL)
		if err != nil {
			return nil, fmt.Errorf("configure database: %w", err)
		}
		db.SetMaxOpenConns(5)
		db.SetConnMaxLifetime(30 * time.Minute)
	}
	return &demoService{handler: server.NewRole(config.Role, db), db: db}, nil
}

func (s *demoService) Handler() http.Handler         { return s.handler }
func (s *demoService) Run(context.Context) error     { return nil }
func (s *demoService) RunOnce(context.Context) error { return nil }
func (s *demoService) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}
