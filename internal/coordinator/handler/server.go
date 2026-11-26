package handler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"

	"gostorage/internal/chunk"
	"gostorage/internal/config"
	"gostorage/internal/coordinator/repository"
	"gostorage/internal/coordinator/service"
	"gostorage/internal/domain"
	"gostorage/internal/placement"
)

const internalOwnerID = "00000000-0000-4000-8000-000000000001"

var uuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)

type Server struct {
	DB          *sql.DB
	Repo        repository.Repository
	State       service.State
	Config      config.Config
	HTTP        *http.Client
	uploadSlots chan struct{}
}

func New(db *sql.DB, c config.Config) *Server {
	maxUploads := c.MaxConcurrentUploads
	if maxUploads < 1 {
		maxUploads = 4
	}
	timeout := c.RequestTimeout
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}
	return &Server{DB: db, Repo: repository.Repository{DB: db}, State: service.State{DB: db}, Config: c, HTTP: &http.Client{Timeout: timeout}, uploadSlots: make(chan struct{}, maxUploads)}
}
func (s *Server) Routes() http.Handler {
	m := http.NewServeMux()
	m.HandleFunc("/api/v1/files", s.files)
	m.HandleFunc("/api/v1/files/", s.file)
	m.HandleFunc("/internal/v1/nodes/heartbeat", s.heartbeat)
	m.HandleFunc("/internal/v1/metrics", s.metrics)
	return recoverer(m)
}
func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if x := recover(); x != nil {
				slog.Error("request panic", "panic", x)
				http.Error(w, "internal error", 500)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
func (s *Server) files(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.listFiles(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", 405)
		return
	}
	select {
	case s.uploadSlots <- struct{}{}:
		defer func() { <-s.uploadSlots }()
	default:
		http.Error(w, "upload capacity is busy", http.StatusServiceUnavailable)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, s.Config.MaxFileBytes+(1<<20))
	if e := r.ParseMultipartForm(8 << 20); e != nil {
		http.Error(w, "invalid or oversized multipart request", 413)
		return
	}
	defer r.MultipartForm.RemoveAll()
	f, h, e := r.FormFile("file")
	if e != nil {
		http.Error(w, "file field is required", 400)
		return
	}
	defer f.Close()
	if h.Size <= 0 {
		http.Error(w, "empty files are not supported", 400)
		return
	}
	if h.Size > s.Config.MaxFileBytes {
		http.Error(w, "file too large", 413)
		return
	}
	reader, ok := f.(io.ReaderAt)
	if !ok {
		http.Error(w, "unsupported multipart reader", 500)
		return
	}
	contentType := h.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	if mt, _, e := mime.ParseMediaType(contentType); e == nil {
		contentType = mt
	} else {
		contentType = "application/octet-stream"
	}
	if len(contentType) > 255 {
		http.Error(w, "content type is too long", 400)
		return
	}
	s.upload(w, r, reader, h, h.Size, contentType)
}

func (s *Server) listFiles(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 100 {
			http.Error(w, "limit must be between 1 and 100", http.StatusBadRequest)
			return
		}
		limit = n
	}
	rows, err := s.DB.QueryContext(r.Context(), `SELECT id,original_name,content_type,size_bytes,sha256,status,created_at FROM files WHERE owner_id=$1 AND status='active' ORDER BY created_at DESC,id DESC LIMIT $2`, internalOwnerID, limit)
	if err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	type listedFile struct {
		ID          string    `json:"id"`
		Name        string    `json:"name"`
		ContentType string    `json:"content_type"`
		SizeBytes   int64     `json:"size_bytes"`
		SHA256      string    `json:"sha256"`
		Status      string    `json:"status"`
		CreatedAt   time.Time `json:"created_at"`
	}
	files := make([]listedFile, 0)
	for rows.Next() {
		var f listedFile
		if err := rows.Scan(&f.ID, &f.Name, &f.ContentType, &f.SizeBytes, &f.SHA256, &f.Status, &f.CreatedAt); err != nil {
			http.Error(w, "database error", http.StatusInternalServerError)
			return
		}
		f.SHA256 = strings.TrimSpace(f.SHA256)
		files = append(files, f)
	}
	if err := rows.Err(); err != nil {
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"files": files, "count": len(files)})
}
func (s *Server) upload(w http.ResponseWriter, r *http.Request, source io.ReaderAt, h *multipart.FileHeader, size int64, contentType string) {
	ctx := r.Context()
	nodes, e := s.eligibleNodes(ctx, size)
	if e != nil {
		http.Error(w, "insufficient healthy storage nodes", 503)
		return
	}
	plans, e := chunk.PlanFile(size, len(nodes))
	if e != nil {
		http.Error(w, "file cannot be split across selected nodes", 400)
		return
	}
	fileID, e := newUUID()
	if e != nil {
		http.Error(w, "internal error", 500)
		return
	}
	uploadID, e := newUUID()
	if e != nil {
		http.Error(w, "internal error", 500)
		return
	}
	chunks := make([]domain.Chunk, len(plans))
	for i, p := range plans {
		id, x := newUUID()
		if x != nil {
			http.Error(w, "internal error", 500)
			return
		}
		chunks[i] = domain.Chunk{ID: id, UploadID: uploadID, StorageNodeID: nodes[i].ID, Index: p.Index, Offset: p.Offset, Size: p.Size, StorageKey: id, Status: domain.ChunkPending}
	}
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		http.Error(w, "database unavailable", 500)
		return
	}
	defer tx.Rollback()
	if _, e = tx.ExecContext(ctx, `INSERT INTO users(id,email) VALUES($1,$2) ON CONFLICT(id) DO NOTHING`, internalOwnerID, "internal@gostorage.local"); e == nil {
		_, e = tx.ExecContext(ctx, `INSERT INTO files(id,owner_id,original_name,content_type,size_bytes,sha256,status) VALUES($1,$2,$3,$4,$5,$6,'unavailable')`, fileID, internalOwnerID, safeName(h.Filename), contentType, size, strings.Repeat("0", 64))
	}
	if e == nil {
		_, e = tx.ExecContext(ctx, `INSERT INTO uploads(id,file_id,status,expected_size,expected_chunks,expires_at,started_at) VALUES($1,$2,'uploading',$3,$4,NOW()+INTERVAL '1 hour',NOW())`, uploadID, fileID, size, len(chunks))
	}
	for _, c := range chunks {
		if e != nil {
			break
		}
		_, e = tx.ExecContext(ctx, `INSERT INTO chunks(id,upload_id,storage_node_id,chunk_index,offset_bytes,size_bytes,status,storage_key) VALUES($1,$2,$3,$4,$5,$6,'pending',$7)`, c.ID, c.UploadID, c.StorageNodeID, c.Index, c.Offset, c.Size, c.StorageKey)
	}
	for _, n := range nodes {
		if e != nil {
			break
		}
		result, x := tx.ExecContext(ctx, `UPDATE storage_nodes SET used_bytes=used_bytes+$2,updated_at=NOW() WHERE id=$1 AND status='active' AND capacity_bytes-used_bytes >= $2`, n.ID, allocatedForNode(chunks, n.ID))
		if x != nil {
			e = x
			break
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			e = fmt.Errorf("node capacity changed during placement")
			break
		}
	}
	if e != nil {
		slog.Error("upload metadata reservation failed", "error", e)
		http.Error(w, "database error", 500)
		return
	}
	if e = tx.Commit(); e != nil {
		slog.Error("upload metadata commit failed", "error", e)
		http.Error(w, "database error", 500)
		return
	}
	fullHash := sha256.New()
	buffer := make([]byte, 64*1024)
	var wholeOffset int64
	for i := range chunks {
		c := &chunks[i]
		section := io.NewSectionReader(source, c.Offset, c.Size)
		chunkHash := sha256.New()
		n, x := io.CopyBuffer(chunkHash, section, buffer)
		if x != nil || n != c.Size {
			s.failUpload(ctx, uploadID, fileID, c.ID, "source read failed")
			http.Error(w, "upload read failed", 500)
			return
		}
		c.SHA256 = hex.EncodeToString(chunkHash.Sum(nil))
		section = io.NewSectionReader(source, c.Offset, c.Size)
		req, x := http.NewRequestWithContext(ctx, http.MethodPut, strings.TrimRight(nodes[i].BaseURL, "/")+"/internal/v1/chunks/"+c.ID, section)
		if x == nil {
			req.Header.Set("X-Internal-Token", s.Config.InternalToken)
			req.Header.Set("X-Storage-Key", c.StorageKey)
			req.Header.Set("X-Chunk-SHA256", c.SHA256)
			req.Header.Set("X-Chunk-Size", fmt.Sprint(c.Size))
			var resp *http.Response
			resp, x = s.HTTP.Do(req)
			if x == nil {
				io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
				resp.Body.Close()
				if resp.StatusCode < 200 || resp.StatusCode >= 300 {
					x = fmt.Errorf("storage node returned %d", resp.StatusCode)
				}
			}
		}
		if x != nil {
			s.failUpload(ctx, uploadID, fileID, c.ID, "chunk storage failed")
			http.Error(w, "storage node failed", 502)
			return
		}
		if x = s.State.MarkChunkStored(ctx, c.ID, c.SHA256); x != nil {
			slog.Error("chunk metadata update failed", "error", x)
			s.failUpload(ctx, uploadID, fileID, c.ID, "metadata update failed")
			http.Error(w, "database error", 500)
			return
		}
		if _, x = io.CopyBuffer(fullHash, io.NewSectionReader(source, c.Offset, c.Size), buffer); x != nil {
			s.failUpload(ctx, uploadID, fileID, c.ID, "source read failed")
			http.Error(w, "upload read failed", 500)
			return
		}
		wholeOffset += c.Size
	}
	if wholeOffset != size {
		s.failUpload(ctx, uploadID, fileID, "", "size mismatch")
		http.Error(w, "upload size mismatch", 500)
		return
	}
	digest := hex.EncodeToString(fullHash.Sum(nil))
	tx, e = s.DB.BeginTx(ctx, nil)
	if e == nil {
		e = s.State.CompleteUpload(ctx, tx, uploadID, fileID, digest)
		if e == nil {
			e = tx.Commit()
		} else {
			tx.Rollback()
		}
	}
	if e != nil {
		slog.Error("upload completion failed", "error", e)
		s.failUpload(ctx, uploadID, fileID, "", "completion failed")
		http.Error(w, "database error", 500)
		return
	}
	writeJSON(w, 201, map[string]any{"id": fileID, "name": safeName(h.Filename), "size_bytes": size, "chunks": len(chunks), "sha256": digest, "status": "active"})
}
func allocatedForNode(cs []domain.Chunk, id string) int64 {
	for _, c := range cs {
		if c.StorageNodeID == id {
			return c.Size
		}
	}
	return 0
}
func (s *Server) failUpload(ctx context.Context, uploadID, fileID, chunkID, msg string) {
	if err := s.State.FailUpload(ctx, uploadID, fileID, chunkID, msg); err != nil {
		slog.Error("failed to record upload failure", "error", err)
	}
}

func (s *Server) eligibleNodes(ctx context.Context, size int64) ([]domain.StorageNode, error) {
	maxNodes := s.Config.MaxNodes
	if maxNodes < 2 {
		maxNodes = 32
	}
	nodes, e := s.Repo.ListHealthyNodes(ctx, s.Config.HeartbeatTTL, maxNodes)
	if e != nil {
		return nil, e
	}
	chosen, _, e := placement.Select(nodes, size, maxNodes, time.Now(), s.Config.HeartbeatTTL)
	return chosen, e
}

func (s *Server) file(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/files/")
	parts := strings.Split(rest, "/")
	if !uuidPattern.MatchString(parts[0]) {
		http.NotFound(w, r)
		return
	}
	switch {
	case len(parts) == 1 && r.Method == http.MethodGet:
		s.getMetadata(w, r, parts[0])
	case len(parts) == 2 && parts[1] == "content" && r.Method == http.MethodGet:
		s.download(w, r, parts[0])
	case len(parts) == 1 && r.Method == http.MethodDelete:
		s.deleteFile(w, r, parts[0])
	default:
		http.NotFound(w, r)
	}
}
func (s *Server) getMetadata(w http.ResponseWriter, r *http.Request, id string) {
	var f domain.File
	e := s.DB.QueryRowContext(r.Context(), `SELECT id,original_name,content_type,size_bytes,sha256,status,created_at FROM files WHERE id=$1`, id).Scan(&f.ID, &f.Name, &f.ContentType, &f.SizeBytes, &f.SHA256, &f.Status, &f.CreatedAt)
	if errors.Is(e, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	if f.Status != domain.FileActive {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, 200, map[string]any{"id": f.ID, "name": f.Name, "content_type": f.ContentType, "size_bytes": f.SizeBytes, "sha256": strings.TrimSpace(f.SHA256), "status": f.Status, "created_at": f.CreatedAt})
}
func (s *Server) download(w http.ResponseWriter, r *http.Request, fileID string) {
	var status, contentType, name, expected string
	var size int64
	e := s.DB.QueryRowContext(r.Context(), `SELECT status,content_type,original_name,size_bytes,sha256 FROM files WHERE id=$1`, fileID).Scan(&status, &contentType, &name, &size, &expected)
	if errors.Is(e, sql.ErrNoRows) {
		http.NotFound(w, r)
		return
	}
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	if status != "active" {
		http.Error(w, "file unavailable", 409)
		return
	}
	rows, e := s.DB.QueryContext(r.Context(), `SELECT c.id,c.storage_key,c.sha256,c.offset_bytes,c.size_bytes,n.base_url FROM chunks c JOIN uploads u ON u.id=c.upload_id JOIN storage_nodes n ON n.id=c.storage_node_id WHERE u.file_id=$1 AND u.status='completed' AND c.status='stored' ORDER BY c.chunk_index`, fileID)
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	defer rows.Close()
	tmp, e := os.CreateTemp("", "gostorage-download-*.tmp")
	if e != nil {
		http.Error(w, "temporary storage unavailable", 500)
		return
	}
	defer func() { tmp.Close(); os.Remove(tmp.Name()) }()
	fileHash := sha256.New()
	buf := make([]byte, 64*1024)
	var total, expectedOffset int64
	for rows.Next() {
		var id, key, hash, base string
		var offset, chunkSize int64
		if e = rows.Scan(&id, &key, &hash, &offset, &chunkSize, &base); e != nil {
			http.Error(w, "database error", 500)
			return
		}
		if offset != expectedOffset || chunkSize <= 0 {
			http.Error(w, "stored file metadata is inconsistent", 502)
			return
		}
		req, x := http.NewRequestWithContext(r.Context(), http.MethodGet, strings.TrimRight(base, "/")+"/internal/v1/chunks/"+id, nil)
		if x != nil {
			http.Error(w, "storage request failed", 502)
			return
		}
		req.Header.Set("X-Internal-Token", s.Config.InternalToken)
		req.Header.Set("X-Storage-Key", key)
		resp, x := s.HTTP.Do(req)
		if x != nil {
			http.Error(w, "storage node unavailable", 502)
			return
		}
		chunkHash := sha256.New()
		n, x := io.CopyBuffer(io.MultiWriter(tmp, fileHash, chunkHash), io.LimitReader(resp.Body, chunkSize+1), buf)
		resp.Body.Close()
		if x != nil || resp.StatusCode != http.StatusOK || n != chunkSize || hex.EncodeToString(chunkHash.Sum(nil)) != strings.TrimSpace(hash) {
			http.Error(w, "stored chunk failed integrity check", 502)
			return
		}
		total += n
		expectedOffset += n
	}
	if rows.Err() != nil {
		http.Error(w, "database error", 500)
		return
	}
	if total != size || hex.EncodeToString(fileHash.Sum(nil)) != strings.TrimSpace(expected) {
		http.Error(w, "stored file failed integrity check", 502)
		return
	}
	if _, e = tmp.Seek(0, io.SeekStart); e != nil {
		http.Error(w, "temporary storage failed", 500)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": safeName(name)}))
	w.Header().Set("Content-Length", fmt.Sprint(size))
	if _, e = io.CopyBuffer(w, tmp, buf); e != nil {
		return
	}
}

func (s *Server) deleteFile(w http.ResponseWriter, r *http.Request, fileID string) {
	ctx := r.Context()
	tx, e := s.DB.BeginTx(ctx, nil)
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	var status string
	e = tx.QueryRowContext(ctx, `SELECT status FROM files WHERE id=$1 FOR UPDATE`, fileID).Scan(&status)
	if errors.Is(e, sql.ErrNoRows) {
		tx.Rollback()
		w.WriteHeader(204)
		return
	}
	if e != nil {
		tx.Rollback()
		http.Error(w, "database error", 500)
		return
	}
	if status == "deleted" {
		tx.Rollback()
		w.WriteHeader(204)
		return
	}
	_, e = tx.ExecContext(ctx, `UPDATE files SET status='deleting' WHERE id=$1`, fileID)
	if e == nil {
		_, e = tx.ExecContext(ctx, `UPDATE uploads SET status='deleting',updated_at=NOW() WHERE file_id=$1 AND status NOT IN ('deleted','deleting')`, fileID)
	}
	if e == nil {
		e = tx.Commit()
	} else {
		tx.Rollback()
	}
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	rows, e := s.DB.QueryContext(ctx, `SELECT c.id,c.storage_key,n.base_url FROM chunks c JOIN uploads u ON u.id=c.upload_id JOIN storage_nodes n ON n.id=c.storage_node_id WHERE u.file_id=$1 AND c.status<>'deleted'`, fileID)
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	type entry struct{ id, key, base string }
	var all []entry
	for rows.Next() {
		var x entry
		if e = rows.Scan(&x.id, &x.key, &x.base); e != nil {
			rows.Close()
			http.Error(w, "database error", 500)
			return
		}
		all = append(all, x)
	}
	rows.Close()
	for _, x := range all {
		req, e := http.NewRequestWithContext(ctx, http.MethodDelete, strings.TrimRight(x.base, "/")+"/internal/v1/chunks/"+x.id, nil)
		if e != nil {
			continue
		}
		req.Header.Set("X-Internal-Token", s.Config.InternalToken)
		req.Header.Set("X-Storage-Key", x.key)
		resp, e := s.HTTP.Do(req)
		if e == nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				continue
			}
		} else {
			continue
		}
		if e := s.State.MarkChunkDeleted(ctx, x.id); e != nil {
			slog.Error("failed to record chunk deletion", "error", e)
		}
	}
	var remaining int
	if e = s.DB.QueryRowContext(ctx, `SELECT COUNT(*) FROM chunks c JOIN uploads u ON u.id=c.upload_id WHERE u.file_id=$1 AND c.status<>'deleted'`, fileID).Scan(&remaining); e != nil || remaining > 0 {
		http.Error(w, "some chunks could not be deleted", 502)
		return
	}
	e = s.State.CompleteDelete(ctx, fileID)
	if e != nil {
		http.Error(w, "database error", 500)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) heartbeat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	if r.Header.Get("X-Internal-Token") != s.Config.InternalToken {
		http.Error(w, "unauthorized", 401)
		return
	}
	var body struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		URL      string `json:"base_url"`
		Capacity int64  `json:"capacity_bytes"`
	}
	if e := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body); e != nil || !uuidPattern.MatchString(body.ID) || body.Name == "" || body.URL == "" || body.Capacity <= 0 {
		http.Error(w, "invalid node registration", 400)
		return
	}
	parsed, e := url.ParseRequestURI(body.URL)
	if e != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || len(body.Name) > 100 {
		http.Error(w, "invalid node registration", 400)
		return
	}
	e = s.Repo.RegisterNode(r.Context(), domain.StorageNode{ID: body.ID, Name: body.Name, BaseURL: body.URL, CapacityBytes: body.Capacity})
	if e != nil {
		http.Error(w, "registration failed", 409)
		return
	}
	w.WriteHeader(204)
}
func (s *Server) metrics(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Internal-Token") != s.Config.InternalToken {
		http.Error(w, "unauthorized", 401)
		return
	}
	var files, uploads, nodes, failed, bytesUsed int64
	queries := []struct {
		query string
		dest  *int64
		args  []any
	}{{`SELECT COUNT(*) FROM files WHERE status='active'`, &files, nil}, {`SELECT COUNT(*) FROM uploads WHERE status='completed'`, &uploads, nil}, {`SELECT COUNT(*) FROM storage_nodes WHERE status='active' AND last_heartbeat_at>NOW()-$1::interval`, &nodes, []any{s.Config.HeartbeatTTL.String()}}, {`SELECT COUNT(*) FROM uploads WHERE status='failed'`, &failed, nil}, {`SELECT COALESCE(SUM(size_bytes),0) FROM files WHERE status='active'`, &bytesUsed, nil}}
	for _, q := range queries {
		if e := s.DB.QueryRowContext(r.Context(), q.query, q.args...).Scan(q.dest); e != nil {
			http.Error(w, "metrics unavailable", 500)
			return
		}
	}
	writeJSON(w, 200, map[string]int64{"active_files": files, "completed_uploads": uploads, "healthy_nodes": nodes, "failed_uploads": failed, "storage_bytes_used": bytesUsed})
}
func newUUID() (string, error) {
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		return "", e
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
func safeName(s string) string {
	n := strings.ToValidUTF8(filepath.Base(strings.ReplaceAll(s, "\\", "/")), "�")
	n = strings.TrimSpace(n)
	var clean strings.Builder
	count := 0
	for _, r := range n {
		if unicode.IsControl(r) {
			continue
		}
		if count >= 255 {
			break
		}
		clean.WriteRune(r)
		count++
	}
	n = clean.String()
	if n == "" || n == "." || n == "/" {
		return "file"
	}
	return n
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
