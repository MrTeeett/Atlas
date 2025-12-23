package fs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MrTeeett/atlas/internal/auth"
)

type Config struct {
	RootDir      string
	SudoEnabled  bool
	SudoAny      bool
	SudoUsers    []string
	HelperBinary string
	SudoPassword func(user string) (string, bool, error)
}

type Service struct {
	root         string
	sudoEnabled  bool
	sudoAny      bool
	sudoUsers    map[string]bool
	selfUser     string
	helperPath   string
	sudoPath     string
	sudoPassword func(user string) (string, bool, error)
}

type Entry struct {
	Name    string `json:"name"`
	Path    string `json:"path"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	ModUnix int64  `json:"mod_unix"`
}

type listResponse struct {
	Path    string  `json:"path"`
	Entries []Entry `json:"entries"`
}

type renameRequest struct {
	From string `json:"from"`
	To   string `json:"to"`
}

type deleteRequest struct {
	Paths []string `json:"paths"`
}

type writeRequest struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func New(cfg Config) *Service {
	root := cfg.RootDir
	if root == "" {
		root = "/"
	}
	sudoUsers := map[string]bool{}
	for _, u := range cfg.SudoUsers {
		u = strings.TrimSpace(u)
		if u != "" {
			sudoUsers[u] = true
		}
	}
	helperPath := strings.TrimSpace(cfg.HelperBinary)
	if helperPath == "" {
		if exe, err := os.Executable(); err == nil {
			helperPath = exe
		}
	}
	sudoPath, _ := exec.LookPath("sudo")
	return &Service{
		root:         filepath.Clean(root),
		sudoEnabled:  cfg.SudoEnabled,
		sudoAny:      cfg.SudoAny,
		sudoUsers:    sudoUsers,
		selfUser:     lookupSelfUser(),
		helperPath:   helperPath,
		sudoPath:     sudoPath,
		sudoPassword: cfg.SudoPassword,
	}
}

func (s *Service) HandleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	clientPath := r.URL.Query().Get("path")
	as, err := s.identityFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	resp, err := s.listAs(r.Context(), as, clientPath)
	if err != nil {
		s.writeFSError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Service) HandleRead(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	clientPath := r.URL.Query().Get("path")
	as, err := s.identityFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	limit := int64(65536)
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil && n > 0 && n <= 1<<20 {
			limit = n
		}
	}

	buf, err := s.readAs(r.Context(), as, clientPath, limit)
	if err != nil {
		s.writeFSError(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(buf)
}

func (s *Service) HandleDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	clientPath := r.URL.Query().Get("path")
	as, err := s.identityFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if as == "self" {
		abs, err := s.resolve(clientPath)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		f, err := os.Open(abs)
		if err != nil {
			s.writeFSError(w, err)
			return
		}
		defer f.Close()

		info, err := f.Stat()
		if err != nil {
			s.writeFSError(w, err)
			return
		}
		if info.IsDir() {
			http.Error(w, "cannot download a directory", http.StatusBadRequest)
			return
		}
		name := filepath.Base(abs)
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, sanitizeFilename(name)))
		http.ServeContent(w, r, name, info.ModTime(), f)
		return
	}

	info, err := s.statAs(r.Context(), as, clientPath)
	if err != nil {
		s.writeFSError(w, err)
		return
	}
	if info.IsDir {
		http.Error(w, "cannot download a directory", http.StatusBadRequest)
		return
	}
	name := filepath.Base(info.Path)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, sanitizeFilename(name)))
	w.Header().Set("Content-Type", "application/octet-stream")
	if info.Size >= 0 {
		w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
	}
	w.Header().Set("Last-Modified", time.Unix(info.ModUnix, 0).UTC().Format(http.TimeFormat))

	cmd := s.sudoCmd(r.Context(), as, "cat", "--path", clientPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		s.writeFSError(w, err)
		return
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		s.writeFSError(w, err)
		return
	}
	_, _ = io.Copy(w, stdout)
	_ = stdout.Close()
	_ = cmd.Wait()
}

func (s *Service) HandleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	as, err := s.identityFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	clientPath := r.URL.Query().Get("path")
	dirAbs, err := s.resolve(clientPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if as == "self" {
		if st, err := os.Stat(dirAbs); err != nil || !st.IsDir() {
			http.Error(w, "target path must be a directory", http.StatusBadRequest)
			return
		}
	} else {
		info, err := s.statAs(r.Context(), as, clientPath)
		if err != nil {
			s.writeFSError(w, err)
			return
		}
		if !info.IsDir {
			http.Error(w, "target path must be a directory", http.StatusBadRequest)
			return
		}
	}

	r.Body = http.MaxBytesReader(w, r.Body, 512<<20)
	if err := r.ParseMultipartForm(512 << 20); err != nil {
		http.Error(w, "bad multipart form", http.StatusBadRequest)
		return
	}

	files := collectMultipartFiles(r, "file")
	if len(files) == 0 {
		http.Error(w, "file is required", http.StatusBadRequest)
		return
	}
	for _, fh := range files {
		if err := s.saveUploadedFileAs(r.Context(), as, dirAbs, fh); err != nil {
			s.writeFSError(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) HandleIdentities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	allowed := []string{"self"}
	if s.sudoEnabled && s.sudoPath != "" && s.helperPath != "" {
		if c, ok := auth.ClaimsFromContext(r.Context()); ok && c.FSSudo {
			if s.sudoAny && c.FSAny {
				allowed = append(allowed, "*")
			} else {
				// Intersection: global allowlist AND per-user allowlist.
				set := map[string]bool{}
				for u := range s.sudoUsers {
					set[u] = true
				}
				if s.sudoAny {
					set = map[string]bool{} // treat as allow-all from global
				}
				var users []string
				for _, u := range c.FSUsers {
					u = strings.TrimSpace(u)
					if u == "" {
						continue
					}
					if s.sudoAny || set[u] {
						users = append(users, u)
					}
				}
				sort.Strings(users)
				allowed = append(allowed, users...)
			}
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"self":         s.selfUser,
		"sudo_enabled": s.sudoEnabled && s.sudoPath != "",
		"allowed":      allowed,
	})
}

func (s *Service) HandleMkdir(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	as, err := s.identityFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	clientPath := r.URL.Query().Get("path")
	dirAbs, err := s.resolve(clientPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if as == "self" {
		if st, err := os.Stat(dirAbs); err != nil || !st.IsDir() {
			http.Error(w, "target path must be a directory", http.StatusBadRequest)
			return
		}
	} else {
		info, err := s.statAs(r.Context(), as, clientPath)
		if err != nil {
			s.writeFSError(w, err)
			return
		}
		if !info.IsDir {
			http.Error(w, "target path must be a directory", http.StatusBadRequest)
			return
		}
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if err := validateName(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.mkdirAs(r.Context(), as, s.clientPath(dirAbs), name); err != nil {
		s.writeFSError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) HandleTouch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	as, err := s.identityFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	clientPath := r.URL.Query().Get("path")
	dirAbs, err := s.resolve(clientPath)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if as == "self" {
		if st, err := os.Stat(dirAbs); err != nil || !st.IsDir() {
			http.Error(w, "target path must be a directory", http.StatusBadRequest)
			return
		}
	} else {
		info, err := s.statAs(r.Context(), as, clientPath)
		if err != nil {
			s.writeFSError(w, err)
			return
		}
		if !info.IsDir {
			http.Error(w, "target path must be a directory", http.StatusBadRequest)
			return
		}
	}
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if err := validateName(name); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.touchAs(r.Context(), as, s.clientPath(dirAbs), name); err != nil {
		s.writeFSError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) HandleWrite(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	as, err := s.identityFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2<<20)
	var req writeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	req.Path = strings.TrimSpace(req.Path)
	if req.Path == "" {
		http.Error(w, "path is required", http.StatusBadRequest)
		return
	}
	if err := s.writeFileAs(r.Context(), as, req.Path, []byte(req.Content)); err != nil {
		s.writeFSError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) HandleRename(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	as, err := s.identityFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	var req renameRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	toName := strings.TrimSpace(req.To)
	if err := validateName(toName); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.renameAs(r.Context(), as, req.From, toName); err != nil {
		s.writeFSError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) HandleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	as, err := s.identityFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	var req deleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if len(req.Paths) == 0 {
		http.Error(w, "paths required", http.StatusBadRequest)
		return
	}
	for _, p := range req.Paths {
		if err := s.deleteAs(r.Context(), as, p, true); err != nil {
			s.writeFSError(w, err)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Service) list(absDir string) ([]Entry, error) {
	entries, err := os.ReadDir(absDir)
	if err != nil {
		return nil, err
	}

	var out []Entry
	client := s.clientPath(absDir)
	if client != "/" {
		parentAbs, _ := s.resolve(filepath.Dir(client))
		out = append(out, Entry{Name: "..", Path: s.clientPath(parentAbs), IsDir: true})
	}

	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			continue
		}
		p := filepath.Join(absDir, e.Name())
		p, err = s.ensureWithinRoot(p)
		if err != nil {
			continue
		}
		out = append(out, Entry{
			Name:    e.Name(),
			Path:    s.clientPath(p),
			IsDir:   info.IsDir(),
			Size:    info.Size(),
			ModUnix: info.ModTime().Unix(),
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Name == ".." {
			return true
		}
		if out[j].Name == ".." {
			return false
		}
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})

	return out, nil
}

func (s *Service) resolve(clientPath string) (string, error) {
	if clientPath == "" {
		clientPath = "/"
	}
	if !strings.HasPrefix(clientPath, "/") {
		clientPath = "/" + clientPath
	}
	clean := filepath.Clean(clientPath)
	rel := strings.TrimPrefix(clean, string(filepath.Separator))
	abs := filepath.Join(s.root, rel)
	return s.ensureWithinRoot(abs)
}

func (s *Service) ensureWithinRoot(absPath string) (string, error) {
	root := filepath.Clean(s.root)
	p := filepath.Clean(absPath)
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path escapes root")
	}
	return p, nil
}

func (s *Service) clientPath(absPath string) string {
	root := filepath.Clean(s.root)
	p := filepath.Clean(absPath)
	if root == "/" {
		if p == "" {
			return "/"
		}
		if !strings.HasPrefix(p, "/") {
			return "/" + p
		}
		return p
	}
	rel, err := filepath.Rel(root, p)
	if err != nil || rel == "." {
		return "/"
	}
	return "/" + filepath.ToSlash(rel)
}

func sanitizeFilename(name string) string {
	name = strings.ReplaceAll(name, `"`, "")
	name = strings.ReplaceAll(name, "\n", "")
	name = strings.ReplaceAll(name, "\r", "")
	name = strings.TrimSpace(name)
	if name == "" {
		return "download"
	}
	return name
}

func validateName(name string) error {
	if name == "" || name == "." || name == ".." {
		return errors.New("bad name")
	}
	if strings.ContainsRune(name, filepath.Separator) {
		return errors.New("bad name")
	}
	if strings.Contains(name, "\x00") {
		return errors.New("bad name")
	}
	return nil
}

func collectMultipartFiles(r *http.Request, field string) []*multipart.FileHeader {
	var out []*multipart.FileHeader
	if r.MultipartForm == nil {
		return out
	}
	for k, fhs := range r.MultipartForm.File {
		if k != field {
			continue
		}
		for _, fh := range fhs {
			out = append(out, fh)
		}
	}
	return out
}

func (s *Service) saveUploadedFile(dirAbs string, fh *multipart.FileHeader) error {
	name := filepath.Base(fh.Filename)
	if err := validateName(name); err != nil {
		return err
	}
	dstPath := filepath.Join(dirAbs, name)
	dstPath, err := s.ensureWithinRoot(dstPath)
	if err != nil {
		return err
	}

	file, err := fh.Open()
	if err != nil {
		return err
	}
	defer file.Close()

	out, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, file)
	return err
}
