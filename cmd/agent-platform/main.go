package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	_ "time/tzdata"

	"agent-platform/internal/app"
	"agent-platform/internal/config"
	"agent-platform/model"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintf(os.Stderr, "usage: %s <server|worker|migrate-rebuilt|seed>\n", os.Args[0])
		os.Exit(2)
	}

	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if os.Args[1] == "seed" {
		dsn := os.Getenv("DATABASE_DSN")
		if dsn == "" {
			log.Error("seed: DATABASE_DSN is required")
			os.Exit(1)
		}
		store, err := model.Open(ctx, dsn)
		if err != nil {
			log.Error("seed: open store", "err", err)
			os.Exit(1)
		}
		defer store.Close()
		if err := store.Init(ctx); err != nil {
			log.Error("seed: schema init", "err", err)
			os.Exit(1)
		}
		os.Exit(seedCommand(ctx, log, store))
	}

	cfg, err := config.Load()
	if err != nil {
		log.Error("config", "err", err)
		os.Exit(1)
	}

	switch os.Args[1] {
	case "migrate-rebuilt":
		if err := app.SchemaInit(ctx, log, cfg); err != nil {
			log.Error("rebuilt schema init failed", "err", err)
			os.Exit(1)
		}
	case "server":
		if err := app.RunServer(ctx, log, cfg); err != nil {
			log.Error("server stopped", "err", err)
			os.Exit(1)
		}
	case "worker":
		if err := app.RunWorker(ctx, log, cfg); err != nil {
			log.Error("worker stopped", "err", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n", os.Args[1])
		os.Exit(2)
	}
}
