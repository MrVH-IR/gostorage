package main

import (
	"context"
	"database/sql"
	"errors"
	_ "github.com/lib/pq"
	"gostorage/internal/config"
	coordinator "gostorage/internal/coordinator/handler"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	c, e := config.Load("coordinator")
	if e != nil {
		slog.Error("invalid configuration", "error", e)
		os.Exit(1)
	}
	db, e := sql.Open("postgres", c.DatabaseURL)
	if e != nil {
		slog.Error("database open failed", "error", e)
		os.Exit(1)
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(30 * time.Minute)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	pingCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	e = db.PingContext(pingCtx)
	cancel()
	if e != nil {
		slog.Error("database unavailable", "error", e)
		os.Exit(1)
	}
	defer db.Close()
	app := coordinator.New(db, c)
	cleanupDone := make(chan struct{})
	go func() { defer close(cleanupDone); app.RunCleanup(ctx, time.Minute) }()
	defer func() { stop(); <-cleanupDone }()
	srv := &http.Server{Addr: c.ListenAddr, Handler: app.Routes(), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: c.RequestTimeout, IdleTimeout: 60 * time.Second}
	go func() {
		<-ctx.Done()
		x, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(x)
	}()
	slog.Info("coordinator listening", "addr", c.ListenAddr)
	if e = srv.ListenAndServe(); e != nil && !errors.Is(e, http.ErrServerClosed) {
		slog.Error("server failed", "error", e)
		stop()
		<-cleanupDone
		_ = db.Close()
		os.Exit(1)
	}
}
