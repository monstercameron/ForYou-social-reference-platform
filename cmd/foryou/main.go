package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"foryou.local/foryou/internal/transport"
	"github.com/joho/godotenv"
)

func main() {
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		log.Printf("load .env: %v", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Getenv, func(role, address string) { log.Printf("%s listening on %s", role, address) }); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, getenv func(string) string, onListen func(role, url string)) error {
	flags := flag.NewFlagSet("foryou", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	role := flags.String("role", "client", "server role or whole-process command")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if command, ok := commands[*role]; ok {
		return command(ctx, getenv)
	}
	valid := append(append([]string(nil), roleOrder...), commandNames()...)
	if _, ok := rolePorts[*role]; !ok {
		return fmt.Errorf("invalid role %q; valid names: %s", *role, strings.Join(valid, ", "))
	}
	config, transportConfig, err := loadRoleConfig(*role, getenv)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(config.DataDir, 0700); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	svc, err := builders[*role](config)
	if err != nil {
		return fmt.Errorf("build %s: %w", *role, err)
	}
	listener, err := transport.Listen(transportConfig)
	if err != nil {
		_ = svc.Close()
		return err
	}
	defer listener.Close()
	if onListen != nil {
		onListen(*role, listener.URL())
	}
	if config.Dev {
		log.Printf("%s serves plain HTTP on loopback in dev mode; this is not for production", *role)
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	serveErrors := make(chan error, 1)
	runErrors := make(chan error, 1)
	go func() { serveErrors <- listener.Serve(runCtx, svc.Handler()) }()
	go func() { runErrors <- svc.Run(runCtx) }()
	var serveErr, runErr error
	runFinished := false
	for {
		select {
		case serveErr = <-serveErrors:
			cancel()
			if !runFinished {
				runErr = <-runErrors
			}
			return finishService(svc, serveErr, runErr)
		case runErr = <-runErrors:
			runFinished = true
			if runErr != nil {
				cancel()
				serveErr = <-serveErrors
				return finishService(svc, serveErr, runErr)
			}
		case <-ctx.Done():
			cancel()
			serveErr = <-serveErrors
			if !runFinished {
				runErr = <-runErrors
			}
			return finishService(svc, serveErr, runErr)
		}
	}
}

func finishService(svc service, serveErr, runErr error) error {
	closeErr := svc.Close()
	if serveErr != nil && !errors.Is(serveErr, context.Canceled) {
		return serveErr
	}
	if runErr != nil && !errors.Is(runErr, context.Canceled) {
		return runErr
	}
	if closeErr != nil {
		return closeErr
	}
	return nil
}

func commandNames() []string {
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
