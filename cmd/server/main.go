package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/RickDB/HikFootageServer/internal/auth"
	"github.com/RickDB/HikFootageServer/internal/compliance"
	"github.com/RickDB/HikFootageServer/internal/config"
	"github.com/RickDB/HikFootageServer/internal/jobs"
	"github.com/RickDB/HikFootageServer/internal/monitor"
	"github.com/RickDB/HikFootageServer/internal/store"
	webapp "github.com/RickDB/HikFootageServer/internal/web"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		panic(err)
	}
	level := slog.LevelInfo
	if cfg.LogLevel == "debug" {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	db, err := store.Open(cfg.DatabasePath, cfg.EncryptionKey)
	if err != nil {
		log.Error("open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	if cfg.EncryptionKey == "" {
		log.Warn("NVR passwords are not encrypted; set APP_ENCRYPTION_KEY")
	}
	authManager := auth.New(db, cfg)
	if err := authManager.BootstrapAdmin(context.Background(), cfg.AdminUsername, cfg.AdminPassword); err != nil {
		log.Error("bootstrap administrator", "error", err)
		os.Exit(1)
	}

	manager := jobs.New(db, cfg, log)
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	manager.Start(ctx)
	compliance.New(db, cfg, log).Start(ctx)
	monitor.New(db, log).Start(ctx)

	server := webapp.New(cfg, db, manager, authManager, log)
	go func() {
		<-ctx.Done()
		shutdown, done := context.WithTimeout(context.Background(), 10*time.Second)
		defer done()
		_ = server.Shutdown(shutdown)
	}()
	log.Info("server started", "address", cfg.ListenAddr, "database", cfg.DatabasePath, "exports", cfg.ExportRoot)
	if err := server.ListenAndServe(); err != nil && err.Error() != "http: Server closed" {
		log.Error("server stopped", "error", err)
		os.Exit(1)
	}
}
