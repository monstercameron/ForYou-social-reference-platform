package main

import (
	"database/sql"
	"flag"
	"log"
	"os"
	"strings"
	"time"

	"foryou.local/foryou/internal/server"
	"foryou.local/foryou/internal/transport"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/joho/godotenv"
)

func main() {
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		log.Fatal("load .env: ", err)
	}
	role := flag.String("role", "client", "server role: foundation, provider, replica, or client")
	flag.Parse()
	addr, ok := server.DefaultAddress(*role)
	if !ok {
		log.Fatal("invalid role: choose foundation, provider, replica, or client")
	}
	rolePrefix := "FORYOU_" + strings.ToUpper(*role) + "_"
	if value := os.Getenv(rolePrefix + "ADDR"); value != "" {
		addr = value
	}
	certFile, keyFile := os.Getenv("FORYOU_TLS_CERT"), os.Getenv("FORYOU_TLS_KEY")
	if certFile == "" || keyFile == "" {
		log.Fatal("set FORYOU_TLS_CERT and FORYOU_TLS_KEY to a certificate and private key")
	}
	var db *sql.DB
	if url := os.Getenv(rolePrefix + "DATABASE_URL"); url != "" {
		var err error
		db, err = sql.Open("pgx", url)
		if err != nil {
			log.Fatal("configure database: ", err)
		}
		defer db.Close()
		db.SetMaxOpenConns(5)
		db.SetConnMaxLifetime(30 * time.Minute)
	}
	handler := server.NewRole(*role, db)
	log.Printf("%s listening on https://%s (HTTP/3 when available; HTTP/1.1 fallback)", *role, addr)
	if err := transport.ListenAndServe(addr, certFile, keyFile, handler); err != nil {
		log.Fatal(err)
	}
}
