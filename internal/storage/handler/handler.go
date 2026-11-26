package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

var uuidRE = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[1-8][0-9a-fA-F]{3}-[89abAB][0-9a-fA-F]{3}-[0-9a-fA-F]{12}$`)
var commitMu sync.Mutex

type Handler struct {
	Root, Token   string
	MaxChunkBytes int64
}

func CleanupTemporary(root string, olderThan time.Duration) error {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-olderThan)
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, ".upload-") || !strings.HasSuffix(name, ".tmp") {
			continue
		}
		info, e := entry.Info()
		if e != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			if e = os.Remove(filepath.Join(root, name)); e != nil && !errors.Is(e, os.ErrNotExist) {
				return e
			}
		}
	}
	return nil
}

func (h Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/internal/v1/health" && r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"status":"ok"}`)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/internal/v1/chunks/") {
		h.chunk(w, r)
		return
	}
	http.NotFound(w, r)
}
func (h Handler) authorized(r *http.Request) bool {
	return h.Token != "" && r.Header.Get("X-Internal-Token") == h.Token
}
func (h Handler) chunk(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 4 || parts[0] != "internal" || parts[1] != "v1" || parts[2] != "chunks" || !uuidRE.MatchString(parts[3]) {
		http.NotFound(w, r)
		return
	}
	key := r.Header.Get("X-Storage-Key")
	if !uuidRE.MatchString(key) {
		http.Error(w, "invalid storage key", 400)
		return
	}
	path := filepath.Join(h.Root, key+".chunk")
	switch r.Method {
	case http.MethodPut:
		h.put(w, r, path)
	case http.MethodGet:
		f, e := os.Open(path)
		if errors.Is(e, os.ErrNotExist) {
			http.NotFound(w, r)
			return
		}
		if e != nil {
			http.Error(w, "internal error", 500)
			return
		}
		defer f.Close()
		io.Copy(w, f)
	case http.MethodDelete:
		e := os.Remove(path)
		if e != nil && !errors.Is(e, os.ErrNotExist) {
			http.Error(w, "internal error", 500)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.Header().Set("Allow", "GET, PUT, DELETE")
		http.Error(w, "method not allowed", 405)
	}
}
func (h Handler) put(w http.ResponseWriter, r *http.Request, path string) {
	size, e := strconv.ParseInt(r.Header.Get("X-Chunk-Size"), 10, 64)
	if e != nil || size <= 0 {
		http.Error(w, "invalid chunk size", 400)
		return
	}
	if h.MaxChunkBytes > 0 && size > h.MaxChunkBytes {
		http.Error(w, "chunk too large", http.StatusRequestEntityTooLarge)
		return
	}
	expected := strings.ToLower(r.Header.Get("X-Chunk-SHA256"))
	if len(expected) != 64 {
		http.Error(w, "invalid checksum", 400)
		return
	}
	if _, e = hex.DecodeString(expected); e != nil {
		http.Error(w, "invalid checksum", 400)
		return
	}
	if e = os.MkdirAll(h.Root, 0700); e != nil {
		http.Error(w, "internal error", 500)
		return
	}
	tmp, e := os.CreateTemp(h.Root, ".upload-*.tmp")
	if e != nil {
		http.Error(w, "internal error", 500)
		return
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	hash := sha256.New()
	n, e := io.CopyN(io.MultiWriter(tmp, hash), r.Body, size)
	if e != nil || n != size {
		tmp.Close()
		http.Error(w, "incomplete chunk", 400)
		return
	}
	var extra [1]byte
	if n, _ := r.Body.Read(extra[:]); n > 0 {
		tmp.Close()
		http.Error(w, "chunk too large", 400)
		return
	}
	actual := hex.EncodeToString(hash.Sum(nil))
	if actual != expected {
		tmp.Close()
		http.Error(w, "checksum mismatch", 400)
		return
	}
	if e = tmp.Sync(); e == nil {
		e = tmp.Close()
	}
	if e != nil {
		http.Error(w, "internal error", 500)
		return
	}
	commitMu.Lock()
	defer commitMu.Unlock()
	if existing, openErr := os.Open(path); openErr == nil {
		st, statErr := existing.Stat()
		h := sha256.New()
		var copied int64
		if statErr == nil {
			copied, statErr = io.Copy(h, existing)
		}
		existing.Close()
		if statErr == nil && st.Size() == size && copied == size && hex.EncodeToString(h.Sum(nil)) == actual {
			w.WriteHeader(http.StatusCreated)
			return
		}
		http.Error(w, "chunk already committed with different content", http.StatusConflict)
		return
	} else if !errors.Is(openErr, os.ErrNotExist) {
		http.Error(w, "internal error", 500)
		return
	}
	if e = os.Rename(tmpName, path); e != nil {
		http.Error(w, "internal error", 500)
		return
	}
	w.WriteHeader(http.StatusCreated)
}
