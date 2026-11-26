package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// CleanupExpired deletes chunks belonging to uploads that passed their deadline.
// It is safe to repeat after a partial node failure.
func (s *Server) CleanupExpired(ctx context.Context) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT id,file_id FROM uploads WHERE (status IN ('pending','uploading','failed') AND expires_at<NOW()) OR (status='deleting' AND updated_at<NOW()-INTERVAL '2 minutes') ORDER BY expires_at LIMIT 100`)
	if err != nil {
		return err
	}
	type expired struct{ upload, file string }
	var items []expired
	for rows.Next() {
		var x expired
		if err = rows.Scan(&x.upload, &x.file); err != nil {
			rows.Close()
			return err
		}
		items = append(items, x)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	var failures []error
	for _, item := range items {
		fileID, claimed, e := s.State.ClaimCleanup(ctx, item.upload)
		if e != nil {
			failures = append(failures, e)
			continue
		}
		if !claimed {
			continue
		}
		if err = s.cleanupUpload(ctx, item.upload, fileID); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}
func (s *Server) cleanupUpload(ctx context.Context, uploadID, fileID string) error {
	rows, err := s.DB.QueryContext(ctx, `SELECT c.id,c.storage_key,c.storage_node_id,n.base_url,c.size_bytes FROM chunks c JOIN storage_nodes n ON n.id=c.storage_node_id WHERE c.upload_id=$1 AND c.status<>'deleted' ORDER BY c.chunk_index`, uploadID)
	if err != nil {
		return err
	}
	type item struct {
		id, key, node, url string
		size               int64
	}
	var chunks []item
	for rows.Next() {
		var c item
		if err = rows.Scan(&c.id, &c.key, &c.node, &c.url, &c.size); err != nil {
			rows.Close()
			return err
		}
		chunks = append(chunks, c)
	}
	if err = rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	var failures []error
	for _, c := range chunks {
		req, e := http.NewRequestWithContext(ctx, http.MethodDelete, strings.TrimRight(c.url, "/")+"/internal/v1/chunks/"+c.id, nil)
		if e == nil {
			req.Header.Set("X-Internal-Token", s.Config.InternalToken)
			req.Header.Set("X-Storage-Key", c.key)
			var resp *http.Response
			resp, e = s.HTTP.Do(req)
			if e == nil {
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
				if resp.StatusCode < 200 || resp.StatusCode >= 300 {
					e = fmt.Errorf("node delete returned %d", resp.StatusCode)
				}
			}
		}
		if e != nil {
			failures = append(failures, e)
			continue
		}
		if e := s.State.MarkChunkDeleted(ctx, c.id); e != nil {
			failures = append(failures, e)
		}
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}
	return s.State.CompleteCleanup(ctx, uploadID, fileID)
}
func (s *Server) RunCleanup(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			err := s.CleanupExpired(runCtx)
			cancel()
			if err != nil {
				slog.Error("expired upload cleanup failed", "error", err)
			}
		}
	}
}
