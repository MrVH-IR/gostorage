package integration

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	_ "github.com/lib/pq"
	"gostorage/internal/config"
	coordinatorhandler "gostorage/internal/coordinator/handler"
	storagehandler "gostorage/internal/storage/handler"
)

const token = "integration-test-token"

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	db, e := sql.Open("postgres", dsn)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if e = db.PingContext(ctx); e != nil {
		db.Close()
		t.Fatal(e)
	}
	return db
}
func uid(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, e := rand.Read(b[:]); e != nil {
		t.Fatal(e)
	}
	b[6] = (b[6] & 15) | 64
	b[8] = (b[8] & 63) | 128
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x", b[:4], b[4:6], b[6:8], b[8:10], b[10:])
}

type testNode struct {
	id     string
	root   string
	server *httptest.Server
}

func setup(t *testing.T, db *sql.DB, brokenSecond bool) (*httptest.Server, []testNode, func()) {
	t.Helper()
	started := time.Now().Add(-time.Second)
	nodes := make([]testNode, 2)
	for i := range nodes {
		id := uid(t)
		id = fmt.Sprintf("%08x%s", 0x10000000+i, id[8:])
		root := t.TempDir()
		hs := storagehandler.Handler{Root: root, Token: token}
		nodes[i] = testNode{id: id, root: root, server: httptest.NewServer(hs)}
	}
	if brokenSecond {
		nodes[1].server.Close()
	}
	ctx := context.Background()
	for i, n := range nodes {
		_, e := db.ExecContext(ctx, `INSERT INTO storage_nodes(id,name,base_url,status,capacity_bytes,used_bytes,last_heartbeat_at) VALUES($1,$2,$3,'active',10485760,0,NOW())`, n.id, fmt.Sprintf("it-%s-%d", strings.ReplaceAll(n.id, "-", ""), i), n.server.URL)
		if e != nil {
			t.Fatal(e)
		}
	}
	c := config.Config{InternalToken: token, MaxFileBytes: 4 << 20, MaxNodes: 32, HeartbeatTTL: time.Minute, RequestTimeout: time.Second * 5}
	app := coordinatorhandler.New(db, c)
	api := httptest.NewServer(app.Routes())
	cleanup := func() {
		api.Close()
		for _, n := range nodes {
			n.server.Close()
		}
		_, _ = db.Exec(`DELETE FROM chunks WHERE upload_id IN (SELECT id FROM uploads WHERE created_at >= $1)`, started)
		_, _ = db.Exec(`DELETE FROM uploads WHERE created_at >= $1`, started)
		_, _ = db.Exec(`DELETE FROM files WHERE created_at >= $1`, started)
		for _, n := range nodes {
			_, _ = db.Exec(`DELETE FROM storage_nodes WHERE id=$1`, n.id)
		}
	}
	return api, nodes, cleanup
}
func upload(t *testing.T, url string, data []byte) (int, []byte) {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	part, e := mw.CreateFormFile("file", "demo.bin")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = part.Write(data); e != nil {
		t.Fatal(e)
	}
	if e = mw.Close(); e != nil {
		t.Fatal(e)
	}
	req, e := http.NewRequest(http.MethodPost, url+"/api/v1/files", &body)
	if e != nil {
		t.Fatal(e)
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, b
}
func TestCoordinatorRoundTripWithTwoNodes(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	api, nodes, cleanup := setup(t, db, false)
	defer cleanup()
	data := bytes.Repeat([]byte("distributed-storage-"), 32768)
	status, response := upload(t, api.URL, data)
	if status != http.StatusCreated {
		t.Fatalf("upload status %d: %s", status, response)
	}
	var created struct {
		ID     string `json:"id"`
		Chunks int    `json:"chunks"`
		Status string `json:"status"`
	}
	if e := json.Unmarshal(response, &created); e != nil {
		t.Fatal(e)
	}
	if created.Chunks != 2 || created.Status != "active" {
		t.Fatalf("unexpected upload response: %+v", created)
	}
	var distinct, chunkCount int
	if e := db.QueryRow(`SELECT COUNT(DISTINCT storage_node_id),COUNT(*) FROM chunks c JOIN uploads u ON u.id=c.upload_id WHERE u.file_id=$1`, created.ID).Scan(&distinct, &chunkCount); e != nil {
		t.Fatal(e)
	}
	if distinct != 2 || chunkCount != 2 {
		t.Fatalf("placement used %d nodes for %d chunks", distinct, chunkCount)
	}
	listResp, e := http.Get(api.URL + "/api/v1/files?limit=10")
	if e != nil {
		t.Fatal(e)
	}
	var listed struct {
		Files []struct {
			ID string `json:"id"`
		} `json:"files"`
		Count int `json:"count"`
	}
	if e = json.NewDecoder(listResp.Body).Decode(&listed); e != nil {
		listResp.Body.Close()
		t.Fatal(e)
	}
	listResp.Body.Close()
	if listResp.StatusCode != http.StatusOK || listed.Count < 1 {
		t.Fatalf("list response: status=%d count=%d", listResp.StatusCode, listed.Count)
	}
	found := false
	for _, item := range listed.Files {
		if item.ID == created.ID {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("uploaded file %s not found in list", created.ID)
	}
	metaResp, e := http.Get(api.URL + "/api/v1/files/" + created.ID)
	if e != nil {
		t.Fatal(e)
	}
	var meta map[string]any
	if e = json.NewDecoder(metaResp.Body).Decode(&meta); e != nil {
		metaResp.Body.Close()
		t.Fatal(e)
	}
	metaResp.Body.Close()
	if metaResp.StatusCode != 200 || meta["status"] != "active" {
		t.Fatalf("metadata response: status=%d body=%v", metaResp.StatusCode, meta)
	}
	metricsReq, _ := http.NewRequest(http.MethodGet, api.URL+"/internal/v1/metrics", nil)
	metricsReq.Header.Set("X-Internal-Token", token)
	metricsResp, e := http.DefaultClient.Do(metricsReq)
	if e != nil {
		t.Fatal(e)
	}
	metricsResp.Body.Close()
	if metricsResp.StatusCode != 200 {
		t.Fatalf("metrics status %d", metricsResp.StatusCode)
	}
	resp, e := http.Get(api.URL + "/api/v1/files/" + created.ID + "/content")
	if e != nil {
		t.Fatal(e)
	}
	got, e := io.ReadAll(resp.Body)
	resp.Body.Close()
	if e != nil {
		t.Fatal(e)
	}
	if resp.StatusCode != 200 || !bytes.Equal(got, data) {
		t.Fatalf("download mismatch: status=%d bytes=%d", resp.StatusCode, len(got))
	}
	req, _ := http.NewRequest(http.MethodDelete, api.URL+"/api/v1/files/"+created.ID, nil)
	resp, e = http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatalf("delete status %d", resp.StatusCode)
	}
	req, _ = http.NewRequest(http.MethodDelete, api.URL+"/api/v1/files/"+created.ID, nil)
	resp, e = http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	resp.Body.Close()
	if resp.StatusCode != 204 {
		t.Fatalf("repeated delete status %d", resp.StatusCode)
	}
	_ = nodes
}
func TestCoordinatorNodeFailureLeavesUploadFailed(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	api, _, cleanup := setup(t, db, true)
	defer cleanup()
	data := []byte("0123456789abcdef")
	status, response := upload(t, api.URL, data)
	if status != http.StatusBadGateway {
		t.Fatalf("expected storage failure, got %d: %s", status, response)
	}
	var state string
	e := db.QueryRow(`SELECT status FROM uploads ORDER BY created_at DESC LIMIT 1`).Scan(&state)
	if e != nil {
		t.Fatal(e)
	}
	if state != "failed" {
		t.Fatalf("failed upload has state %q", state)
	}
	var stored, failed int
	if e := db.QueryRow(`SELECT COUNT(*) FILTER (WHERE c.status='stored'),COUNT(*) FILTER (WHERE c.status='failed') FROM chunks c JOIN uploads u ON u.id=c.upload_id WHERE u.status='failed' AND u.created_at>NOW()-INTERVAL '1 minute'`).Scan(&stored, &failed); e != nil {
		t.Fatal(e)
	}
	if stored != 1 || failed != 1 {
		t.Fatalf("expected one successful and one failed chunk, got stored=%d failed=%d", stored, failed)
	}
}

func TestDownloadRejectsCorruptChunkBeforeWriting(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	api, nodes, cleanup := setup(t, db, false)
	defer cleanup()
	data := []byte("0123456789abcdef")
	status, response := upload(t, api.URL, data)
	if status != 201 {
		t.Fatalf("upload status %d: %s", status, response)
	}
	var created struct {
		ID string `json:"id"`
	}
	if e := json.Unmarshal(response, &created); e != nil {
		t.Fatal(e)
	}
	var key, nodeID string
	if e := db.QueryRow(`SELECT c.storage_key,c.storage_node_id FROM chunks c JOIN uploads u ON u.id=c.upload_id WHERE u.file_id=$1 ORDER BY c.chunk_index LIMIT 1`, created.ID).Scan(&key, &nodeID); e != nil {
		t.Fatal(e)
	}
	var root string
	for _, node := range nodes {
		if node.id == nodeID {
			root = node.root
		}
	}
	if root == "" {
		t.Fatal("placed node was not found")
	}
	if e := os.WriteFile(filepath.Join(root, key+".chunk"), []byte("corrupt-content"), 0600); e != nil {
		t.Fatal(e)
	}
	metaResp, e := http.Get(api.URL + "/api/v1/files/" + created.ID)
	if e != nil {
		t.Fatal(e)
	}
	var meta map[string]any
	if e = json.NewDecoder(metaResp.Body).Decode(&meta); e != nil {
		metaResp.Body.Close()
		t.Fatal(e)
	}
	metaResp.Body.Close()
	if metaResp.StatusCode != 200 || meta["status"] != "active" {
		t.Fatalf("metadata response: status=%d body=%v", metaResp.StatusCode, meta)
	}
	metricsReq, _ := http.NewRequest(http.MethodGet, api.URL+"/internal/v1/metrics", nil)
	metricsReq.Header.Set("X-Internal-Token", token)
	metricsResp, e := http.DefaultClient.Do(metricsReq)
	if e != nil {
		t.Fatal(e)
	}
	metricsResp.Body.Close()
	if metricsResp.StatusCode != 200 {
		t.Fatalf("metrics status %d", metricsResp.StatusCode)
	}
	resp, e := http.Get(api.URL + "/api/v1/files/" + created.ID + "/content")
	if e != nil {
		t.Fatal(e)
	}
	got, e := io.ReadAll(resp.Body)
	resp.Body.Close()
	if e != nil {
		t.Fatal(e)
	}
	if resp.StatusCode != http.StatusBadGateway || bytes.Contains(got, data) {
		t.Fatalf("corrupt content response status=%d body=%q", resp.StatusCode, got)
	}
	if e := os.Remove(filepath.Join(root, key+".chunk")); e != nil {
		t.Fatal(e)
	}
	resp, e = http.Get(api.URL + "/api/v1/files/" + created.ID + "/content")
	if e != nil {
		t.Fatal(e)
	}
	got, e = io.ReadAll(resp.Body)
	resp.Body.Close()
	if e != nil {
		t.Fatal(e)
	}
	if resp.StatusCode != http.StatusBadGateway || bytes.Contains(got, data) {
		t.Fatalf("missing chunk response status=%d body=%q", resp.StatusCode, got)
	}
}

func TestDownloadClientCancellationStopsNodeRead(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	startedAt := time.Now().Add(-time.Second)
	tokenValue := token
	var blocked atomic.Bool
	started := make(chan struct{}, 1)
	root1, root2 := t.TempDir(), t.TempDir()
	base1 := storagehandler.Handler{Root: root1, Token: tokenValue}
	base2 := storagehandler.Handler{Root: root2, Token: tokenValue}
	wrapped := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if blocked.Load() && r.Method == http.MethodGet {
			select {
			case started <- struct{}{}:
			default:
			}
			<-r.Context().Done()
			return
		}
		base1.ServeHTTP(w, r)
	})
	n1 := httptest.NewServer(wrapped)
	n2 := httptest.NewServer(base2)
	ids := []string{"10000000-0000-4000-8000-000000000001", "20000000-0000-4000-8000-000000000002"}
	for i, n := range []*httptest.Server{n1, n2} {
		_, e := db.Exec(`INSERT INTO storage_nodes(id,name,base_url,status,capacity_bytes,used_bytes,last_heartbeat_at) VALUES($1,$2,$3,'active',10485760,0,NOW())`, ids[i], "cancel-"+ids[i], n.URL)
		if e != nil {
			t.Fatal(e)
		}
	}
	c := config.Config{InternalToken: tokenValue, MaxFileBytes: 1 << 20, MaxNodes: 2, MaxConcurrentUploads: 2, HeartbeatTTL: time.Minute, RequestTimeout: 5 * time.Second}
	api := httptest.NewServer(coordinatorhandler.New(db, c).Routes())
	defer func() {
		api.Close()
		n1.Close()
		n2.Close()
		_, _ = db.Exec(`DELETE FROM chunks WHERE upload_id IN (SELECT id FROM uploads WHERE created_at >= $1)`, startedAt)
		_, _ = db.Exec(`DELETE FROM uploads WHERE created_at >= $1`, startedAt)
		_, _ = db.Exec(`DELETE FROM files WHERE created_at >= $1`, startedAt)
		for _, id := range ids {
			_, _ = db.Exec(`DELETE FROM storage_nodes WHERE id=$1`, id)
		}
	}()
	code, response := upload(t, api.URL, []byte("client-cancel-check"))
	if code != 201 {
		t.Fatalf("upload status %d: %s", code, response)
	}
	var file struct {
		ID string `json:"id"`
	}
	if e := json.Unmarshal(response, &file); e != nil {
		t.Fatal(e)
	}
	blocked.Store(true)
	ctx, cancel := context.WithCancel(context.Background())
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, api.URL+"/api/v1/files/"+file.ID+"/content", nil)
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() {
		resp, e := http.DefaultClient.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
		done <- e
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("Coordinator did not request a Storage Node")
	}
	cancel()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("expected canceled client request to fail")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client cancellation did not stop the request")
	}
}

func TestExpiredUploadCleanupIsIdempotent(t *testing.T) {
	db := testDB(t)
	defer db.Close()
	_, nodes, cleanup := setup(t, db, false)
	defer cleanup()
	owner := "00000000-0000-4000-8000-000000000001"
	fileID, uploadID, chunk1, chunk2 := uid(t), uid(t), uid(t), uid(t)
	payload := []byte("abcdefgh")
	sum := sha256.Sum256(payload)
	if _, e := db.Exec(`INSERT INTO users(id,email) VALUES($1,$2) ON CONFLICT(id) DO NOTHING`, owner, "internal@gostorage.local"); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`INSERT INTO files(id,owner_id,original_name,content_type,size_bytes,sha256,status) VALUES($1,$2,'cleanup.bin','application/octet-stream',16,$3,'unavailable')`, fileID, owner, strings.Repeat("0", 64)); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`INSERT INTO uploads(id,file_id,status,expected_size,expected_chunks,expires_at) VALUES($1,$2,'failed',16,2,NOW()-INTERVAL '1 hour')`, uploadID, fileID); e != nil {
		t.Fatal(e)
	}
	for i, c := range []string{chunk1, chunk2} {
		hash := ""
		status := "pending"
		if i == 0 {
			hash = hex.EncodeToString(sum[:])
			status = "stored"
			if e := os.WriteFile(filepath.Join(nodes[i].root, c+".chunk"), payload, 0600); e != nil {
				t.Fatal(e)
			}
		}
		if _, e := db.Exec(`INSERT INTO chunks(id,upload_id,storage_node_id,chunk_index,offset_bytes,size_bytes,sha256,status,storage_key) VALUES($1,$2,$3,$4,$5,8,NULLIF($6,''),$7,$8)`, c, uploadID, nodes[i].id, i, int64(i*8), hash, status, c); e != nil {
			t.Fatal(e)
		}
		if _, e := db.Exec(`UPDATE storage_nodes SET used_bytes=used_bytes+8 WHERE id=$1`, nodes[i].id); e != nil {
			t.Fatal(e)
		}
	}
	app := coordinatorhandler.New(db, config.Config{InternalToken: token, HeartbeatTTL: time.Minute, RequestTimeout: 5 * time.Second})
	if e := app.CleanupExpired(context.Background()); e != nil {
		t.Fatal(e)
	}
	if e := app.CleanupExpired(context.Background()); e != nil {
		t.Fatal(e)
	}
	var fileStatus, uploadStatus string
	if e := db.QueryRow(`SELECT status FROM files WHERE id=$1`, fileID).Scan(&fileStatus); e != nil {
		t.Fatal(e)
	}
	if e := db.QueryRow(`SELECT status FROM uploads WHERE id=$1`, uploadID).Scan(&uploadStatus); e != nil {
		t.Fatal(e)
	}
	if fileStatus != "deleted" || uploadStatus != "deleted" {
		t.Fatalf("cleanup states file=%s upload=%s", fileStatus, uploadStatus)
	}
	var used int64
	if e := db.QueryRow(`SELECT SUM(used_bytes) FROM storage_nodes WHERE id IN ($1,$2)`, nodes[0].id, nodes[1].id).Scan(&used); e != nil {
		t.Fatal(e)
	}
	if used != 0 {
		t.Fatalf("cleanup left %d bytes reserved", used)
	}
	if _, e := os.Stat(filepath.Join(nodes[0].root, chunk1+".chunk")); !os.IsNotExist(e) {
		t.Fatalf("stored file remained after cleanup: %v", e)
	}
}
