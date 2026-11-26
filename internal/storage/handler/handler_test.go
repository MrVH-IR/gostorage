package handler

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const testID = "10000000-0000-4000-8000-000000000001"

func invoke(h Handler, method, path, token, key, checksum string, data []byte) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, bytes.NewReader(data))
	req.Header.Set("X-Internal-Token", token)
	req.Header.Set("X-Storage-Key", key)
	req.Header.Set("X-Chunk-SHA256", checksum)
	req.Header.Set("X-Chunk-Size", itoa(len(data)))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}
func TestChunkLifecycleAndIntegrity(t *testing.T) {
	root := t.TempDir()
	h := Handler{Root: root, Token: "secret"}
	body := []byte("chunk payload")
	sum := sha256.Sum256(body)
	hash := hex.EncodeToString(sum[:])
	path := "/internal/v1/chunks/" + testID
	if got := invoke(h, http.MethodPut, path, "secret", testID, hash, body).Code; got != 201 {
		t.Fatalf("put: %d", got)
	}
	if got := invoke(h, http.MethodPut, path, "secret", testID, hash, body).Code; got != 201 {
		t.Fatalf("idempotent retry: %d", got)
	}
	otherHash := sha256.Sum256([]byte("altered data"))
	if got := invoke(h, http.MethodPut, path, "secret", testID, hex.EncodeToString(otherHash[:]), []byte("altered data")).Code; got != 409 {
		t.Fatalf("conflicting retry: %d", got)
	}
	if got := invoke(h, http.MethodGet, path, "wrong", testID, hash, nil).Code; got != 401 {
		t.Fatalf("unauthorized: %d", got)
	}
	got := invoke(h, http.MethodGet, path, "secret", testID, hash, nil)
	if got.Code != 200 || !bytes.Equal(got.Body.Bytes(), body) {
		t.Fatalf("read mismatch: %q", got.Body.Bytes())
	}
	if _, e := os.Stat(filepath.Join(root, testID+".chunk")); e != nil {
		t.Fatal(e)
	}
	if got := invoke(h, http.MethodDelete, path, "secret", testID, hash, nil).Code; got != 204 {
		t.Fatalf("delete: %d", got)
	}
	if got := invoke(h, http.MethodDelete, path, "secret", testID, hash, nil).Code; got != 204 {
		t.Fatalf("idempotent delete: %d", got)
	}
}
func TestChunkRejectsBadChecksumAndTraversal(t *testing.T) {
	h := Handler{Root: t.TempDir(), Token: "secret"}
	path := "/internal/v1/chunks/" + testID
	if got := invoke(h, http.MethodPut, path, "secret", testID, hex.EncodeToString(make([]byte, 32)), []byte("abc")).Code; got != 400 {
		t.Fatalf("bad checksum status %d", got)
	}
	sum := sha256.Sum256([]byte("abc"))
	if got := invoke(h, http.MethodPut, path, "secret", "../../etc/passwd", hex.EncodeToString(sum[:]), []byte("abc")).Code; got != 400 {
		t.Fatalf("traversal status %d", got)
	}
}
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	b := []byte{}
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}

func TestChunkRejectsEmptyAndIncompleteBodies(t *testing.T) {
	root := t.TempDir()
	h := Handler{Root: root, Token: "secret"}
	path := "/internal/v1/chunks/" + testID
	req := httptest.NewRequest(http.MethodPut, path, bytes.NewReader(nil))
	req.Header.Set("X-Internal-Token", "secret")
	req.Header.Set("X-Storage-Key", testID)
	req.Header.Set("X-Chunk-Size", "0")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("empty body status %d", w.Code)
	}
	req = httptest.NewRequest(http.MethodPut, path, bytes.NewReader([]byte("abc")))
	req.Header.Set("X-Internal-Token", "secret")
	req.Header.Set("X-Storage-Key", testID)
	req.Header.Set("X-Chunk-Size", "4")
	sum := sha256.Sum256([]byte("abcd"))
	req.Header.Set("X-Chunk-SHA256", hex.EncodeToString(sum[:]))
	w = httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if w.Code != 400 {
		t.Fatalf("incomplete body status %d", w.Code)
	}
	if _, e := os.Stat(filepath.Join(root, testID+".chunk")); !os.IsNotExist(e) {
		t.Fatalf("incomplete body was committed: %v", e)
	}
}
func TestLargeChunkRoundTrip(t *testing.T) {
	root := t.TempDir()
	h := Handler{Root: root, Token: "secret"}
	body := bytes.Repeat([]byte("large-stream-"), 150000)
	sum := sha256.Sum256(body)
	path := "/internal/v1/chunks/" + testID
	if got := invoke(h, http.MethodPut, path, "secret", testID, hex.EncodeToString(sum[:]), body).Code; got != 201 {
		t.Fatalf("large put status %d", got)
	}
	got := invoke(h, http.MethodGet, path, "secret", testID, "", nil)
	if got.Code != 200 || !bytes.Equal(got.Body.Bytes(), body) {
		t.Fatalf("large read mismatch: code=%d size=%d", got.Code, got.Body.Len())
	}
}

func TestCleanupTemporaryRemovesOnlyExpiredUploads(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, ".upload-old.tmp")
	fresh := filepath.Join(root, ".upload-new.tmp")
	other := filepath.Join(root, "keep.chunk")
	for _, p := range []string{old, fresh, other} {
		if e := os.WriteFile(p, []byte("x"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	past := time.Now().Add(-2 * time.Hour)
	if e := os.Chtimes(old, past, past); e != nil {
		t.Fatal(e)
	}
	if e := CleanupTemporary(root, time.Hour); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(old); !os.IsNotExist(e) {
		t.Fatalf("old temp file remains: %v", e)
	}
	for _, p := range []string{fresh, other} {
		if _, e := os.Stat(p); e != nil {
			t.Fatalf("unexpired or committed file was removed: %s", p)
		}
	}
}
