package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"gostorage/internal/config"
	"gostorage/internal/storage/handler"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

func main() {
	c, e := config.Load("storage")
	if e != nil {
		slog.Error("config", "error", e)
		os.Exit(1)
	}
	if e = handler.CleanupTemporary(c.StorageDir, time.Hour); e != nil {
		slog.Error("temporary file cleanup failed", "error", e)
	}
	srv := &http.Server{Addr: c.ListenAddr, Handler: handler.Handler{Root: c.StorageDir, Token: c.InternalToken, MaxChunkBytes: c.MaxFileBytes}, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: c.RequestTimeout, WriteTimeout: 0, IdleTimeout: 60 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	var background sync.WaitGroup
	defer func() { stop(); background.Wait() }()
	client := &http.Client{Timeout: 5 * time.Second}
	background.Add(1)
	go func() {
		defer background.Done()
		register := func() {
			body, _ := json.Marshal(map[string]any{"id": c.NodeID, "name": c.NodeName, "base_url": c.NodeAddr, "capacity_bytes": c.NodeCapacityBytes})
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.CoordinatorURL, "/")+"/internal/v1/nodes/heartbeat", bytes.NewReader(body))
			if err != nil {
				return
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Internal-Token", c.InternalToken)
			resp, err := client.Do(req)
			if err == nil {
				resp.Body.Close()
			}
		}
		register()
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				register()
			}
		}
	}()
	background.Add(1)
	go func() {
		defer background.Done()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if e := handler.CleanupTemporary(c.StorageDir, time.Hour); e != nil {
					slog.Error("temporary file cleanup failed", "error", e)
				}
			}
		}
	}()
	go func() {
		<-ctx.Done()
		x, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		srv.Shutdown(x)
	}()
	slog.Info("storage node listening", "addr", c.ListenAddr)
	if e = srv.ListenAndServe(); e != nil && !errors.Is(e, http.ErrServerClosed) {
		slog.Error("server", "error", e)
		stop()
		background.Wait()
		os.Exit(1)
	}
}
