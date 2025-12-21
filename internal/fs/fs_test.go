package fs

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MrTeeett/atlas/internal/auth"
)

func TestResolveAndRootEscape(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	s := New(Config{RootDir: root})

	got, err := s.resolve("/a/b/../c")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if !strings.HasPrefix(got, root) {
		t.Fatalf("expected under root, got %q", got)
	}
	outside := filepath.Join(filepath.Dir(root), "outside")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatalf("mkdir outside: %v", err)
	}
	if _, err := s.ensureWithinRoot(outside); err == nil {
		t.Fatalf("expected ensureWithinRoot escape error")
	}
}

func TestHandleWriteReadRenameDeleteSelf(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "d"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	s := New(Config{RootDir: root})

	// write
	body, _ := json.Marshal(map[string]any{"path": "/d/a.txt", "content": "hello"})
	req := httptest.NewRequest(http.MethodPost, "http://example/api/fs/write", bytes.NewReader(body))
	rr := httptest.NewRecorder()
	s.HandleWrite(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("write status=%d body=%q", rr.Code, rr.Body.String())
	}

	// read
	req = httptest.NewRequest(http.MethodGet, "http://example/api/fs/read?path=/d/a.txt&limit=10", nil)
	rr = httptest.NewRecorder()
	s.HandleRead(rr, req)
	if rr.Code != http.StatusOK || rr.Body.String() != "hello" {
		t.Fatalf("read status=%d body=%q", rr.Code, rr.Body.String())
	}

	// rename
	body, _ = json.Marshal(map[string]any{"from": "/d/a.txt", "to": "b.txt"})
	req = httptest.NewRequest(http.MethodPost, "http://example/api/fs/rename", bytes.NewReader(body))
	rr = httptest.NewRecorder()
	s.HandleRename(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("rename status=%d body=%q", rr.Code, rr.Body.String())
	}

	// delete
	body, _ = json.Marshal(map[string]any{"paths": []string{"/d/b.txt"}})
	req = httptest.NewRequest(http.MethodPost, "http://example/api/fs/delete", bytes.NewReader(body))
	rr = httptest.NewRecorder()
	s.HandleDelete(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%q", rr.Code, rr.Body.String())
	}
	if _, err := os.Stat(filepath.Join(root, "d", "b.txt")); !os.IsNotExist(err) {
		t.Fatalf("expected file deleted")
	}
}

func TestHandleListIncludesParent(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "d", "sub"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "d", "x.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	s := New(Config{RootDir: root})

	req := httptest.NewRequest(http.MethodGet, "http://example/api/fs/list?path=/d", nil)
	rr := httptest.NewRecorder()
	s.HandleList(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%q", rr.Code, rr.Body.String())
	}
	var resp struct {
		Path    string  `json:"path"`
		Entries []Entry `json:"entries"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	if resp.Path != "/d" {
		t.Fatalf("path=%q", resp.Path)
	}
	if len(resp.Entries) == 0 || resp.Entries[0].Name != ".." {
		t.Fatalf("expected .. entry first, got %#v", resp.Entries)
	}
}

func TestHandleDownloadSelf(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	s := New(Config{RootDir: root})

	req := httptest.NewRequest(http.MethodGet, "http://example/api/fs/download?path=/a.txt", nil)
	rr := httptest.NewRecorder()
	s.HandleDownload(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("download status=%d body=%q", rr.Code, rr.Body.String())
	}
	if rr.Body.String() != "hi" {
		t.Fatalf("download body=%q", rr.Body.String())
	}
	if cd := rr.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") {
		t.Fatalf("expected content-disposition, got %q", cd)
	}
}

func TestHandleUploadSelf(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "d"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	s := New(Config{RootDir: root})

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "a.txt")
	if err != nil {
		t.Fatalf("CreateFormFile: %v", err)
	}
	_, _ = fw.Write([]byte("payload"))
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost, "http://example/api/fs/upload?path=/d", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rr := httptest.NewRecorder()
	s.HandleUpload(rr, req)
	if rr.Code != http.StatusNoContent {
		t.Fatalf("upload status=%d body=%q", rr.Code, rr.Body.String())
	}
	b, err := os.ReadFile(filepath.Join(root, "d", "a.txt"))
	if err != nil || string(b) != "payload" {
		t.Fatalf("uploaded file mismatch err=%v body=%q", err, string(b))
	}
}

func TestHandleReadTruncates(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "big.txt"), []byte(strings.Repeat("a", 50)), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	s := New(Config{RootDir: root})

	req := httptest.NewRequest(http.MethodGet, "http://example/api/fs/read?path=/big.txt&limit=10", nil)
	rr := httptest.NewRecorder()
	s.HandleRead(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("read status=%d body=%q", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "file truncated") {
		t.Fatalf("expected truncated marker, got %q", rr.Body.String())
	}
}

func TestHandleMkdirBadName(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "d"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	s := New(Config{RootDir: root})

	req := httptest.NewRequest(http.MethodPost, "http://example/api/fs/mkdir?path=/d&name=..", nil)
	rr := httptest.NewRecorder()
	s.HandleMkdir(rr, req)
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d body=%q", rr.Code, rr.Body.String())
	}
}

func TestHandleIdentitiesSudoIntersection(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	s := New(Config{RootDir: root, SudoEnabled: true, SudoAny: false, SudoUsers: []string{"root", "daemon"}, HelperBinary: "/bin/true"})

	// Pretend sudo is available; HandleIdentities only checks presence of strings.
	s.sudoPath = "/bin/sudo"

	claims := auth.Claims{UserInfo: auth.UserInfo{FSSudo: true, FSAny: false, FSUsers: []string{"root", "nobody"}}}
	ctx := auth.WithClaims(context.Background(), claims)

	req := httptest.NewRequest(http.MethodGet, "http://example/api/fs/identities", nil).WithContext(ctx)
	rr := httptest.NewRecorder()
	s.HandleIdentities(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("identities status=%d body=%q", rr.Code, rr.Body.String())
	}
	var resp struct {
		Allowed []string `json:"allowed"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json: %v", err)
	}
	// root should be allowed, nobody should be filtered out.
	if len(resp.Allowed) < 2 || resp.Allowed[0] != "self" || resp.Allowed[1] != "root" {
		t.Fatalf("allowed=%#v", resp.Allowed)
	}
}

func TestIdentityFromRequestSudoDisabled(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	s := New(Config{RootDir: root, SudoEnabled: false})

	claims := auth.Claims{UserInfo: auth.UserInfo{FSSudo: true, FSAny: true}}
	ctx := auth.WithClaims(context.Background(), claims)

	req := httptest.NewRequest(http.MethodGet, "http://example/api/fs/list?path=/", nil).WithContext(ctx)
	req.Header.Set("X-Atlas-FS-User", "root")
	rr := httptest.NewRecorder()
	s.HandleList(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d body=%q", rr.Code, rr.Body.String())
	}
}

func TestHelpersNormalizeAndValidate(t *testing.T) {
	t.Parallel()

	if got := normalizeClientPath("a//b/"); got != "/a/b" {
		t.Fatalf("normalizeClientPath: %q", got)
	}
	if err := validateName(".."); err == nil {
		t.Fatalf("expected bad name")
	}
	if err := validateName("a/b"); err == nil {
		t.Fatalf("expected bad name with slash")
	}
	if got := sanitizeFilename("  \"x\"\n"); got != "x" {
		t.Fatalf("sanitizeFilename: %q", got)
	}
}

func TestWriteFSErrorMapping(t *testing.T) {
	t.Parallel()

	s := New(Config{RootDir: t.TempDir()})
	rr := httptest.NewRecorder()
	s.writeFSError(rr, os.ErrNotExist)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rr.Code)
	}

	rr = httptest.NewRecorder()
	s.writeFSError(rr, os.ErrPermission)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rr.Code)
	}

	rr = httptest.NewRecorder()
	s.writeFSError(rr, context.DeadlineExceeded)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d", rr.Code)
	}
}
