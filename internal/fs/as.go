package fs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/MrTeeett/atlas/internal/auth"
)

type fileInfo struct {
	Path    string `json:"path"`
	IsDir   bool   `json:"is_dir"`
	Size    int64  `json:"size"`
	ModUnix int64  `json:"mod_unix"`
}

func (s *Service) identityFromRequest(r *http.Request) (string, error) {
	as := strings.TrimSpace(r.Header.Get("X-Atlas-FS-User"))
	if as == "" {
		as = strings.TrimSpace(r.URL.Query().Get("as"))
	}
	if as == "" || as == "self" {
		return "self", nil
	}

	// Per-web-user authorization.
	if c, ok := auth.ClaimsFromContext(r.Context()); ok {
		if !c.FSSudo {
			return "", errors.New("fs sudo is not permitted")
		}
		if !c.FSAny {
			allowed := map[string]bool{}
			for _, u := range c.FSUsers {
				u = strings.TrimSpace(u)
				if u != "" {
					allowed[u] = true
				}
			}
			if !allowed[as] {
				return "", errors.New("fs user is not allowed")
			}
		}
	}

	if !s.sudoEnabled {
		return "", errors.New("fs sudo is disabled")
	}
	if s.sudoPath == "" || s.helperPath == "" {
		return "", errors.New("sudo mode is not available")
	}
	if s.sudoAny {
		return as, nil
	}
	if s.sudoUsers[as] {
		return as, nil
	}
	return "", errors.New("fs user is not allowed")
}

func (s *Service) writeFSError(w http.ResponseWriter, err error) {
	if err == nil {
		return
	}
	if strings.Contains(err.Error(), "path escapes root") {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "sudo:") || strings.Contains(lower, "a password is required") || strings.Contains(lower, "not in the sudoers file") {
		http.Error(w, "sudo is not configured", http.StatusForbidden)
		return
	}
	if isPermission(err) {
		http.Error(w, "permission denied", http.StatusForbidden)
		return
	}
	if errors.Is(err, os.ErrNotExist) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	http.Error(w, err.Error(), http.StatusInternalServerError)
}

func isPermission(err error) bool {
	if err == nil {
		return false
	}
	if os.IsPermission(err) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "permission denied") || strings.Contains(msg, "operation not permitted")
}

func (s *Service) listAs(ctx context.Context, as string, clientPath string) (listResponse, error) {
	if as == "self" {
		abs, err := s.resolve(clientPath)
		if err != nil {
			return listResponse{}, err
		}
		entries, err := s.list(abs)
		if err != nil {
			return listResponse{}, err
		}
		return listResponse{Path: s.clientPath(abs), Entries: entries}, nil
	}

	var stdout bytes.Buffer
	if err := s.runHelper(ctx, as, &stdout, nil, "list", "--path", clientPath); err != nil {
		return listResponse{}, err
	}
	var resp listResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return listResponse{}, err
	}
	resp.Path = normalizeClientPath(resp.Path)
	return resp, nil
}

func (s *Service) readAs(ctx context.Context, as string, clientPath string, limit int64) ([]byte, error) {
	if as == "self" {
		abs, err := s.resolve(clientPath)
		if err != nil {
			return nil, err
		}
		f, err := os.Open(abs)
		if err != nil {
			return nil, err
		}
		defer f.Close()

		buf, err := io.ReadAll(io.LimitReader(f, limit+1))
		if err != nil {
			return nil, err
		}
		if int64(len(buf)) > limit {
			buf = append(buf[:limit], []byte("\n\n... file truncated ...\n")...)
		}
		return buf, nil
	}

	var stdout bytes.Buffer
	if err := s.runHelper(ctx, as, &stdout, nil, "read", "--path", clientPath, "--limit", strconv.FormatInt(limit, 10)); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

func (s *Service) statAs(ctx context.Context, as string, clientPath string) (fileInfo, error) {
	if as == "self" {
		abs, err := s.resolve(clientPath)
		if err != nil {
			return fileInfo{}, err
		}
		st, err := os.Stat(abs)
		if err != nil {
			return fileInfo{}, err
		}
		return fileInfo{Path: s.clientPath(abs), IsDir: st.IsDir(), Size: st.Size(), ModUnix: st.ModTime().Unix()}, nil
	}

	var stdout bytes.Buffer
	if err := s.runHelper(ctx, as, &stdout, nil, "stat", "--path", clientPath); err != nil {
		return fileInfo{}, err
	}
	var info fileInfo
	if err := json.Unmarshal(stdout.Bytes(), &info); err != nil {
		return fileInfo{}, err
	}
	info.Path = normalizeClientPath(info.Path)
	return info, nil
}

type downloadReader struct {
	rc  io.ReadCloser
	cmd *exec.Cmd
}

func (d *downloadReader) Read(p []byte) (int, error) { return d.rc.Read(p) }

func (d *downloadReader) Close() error {
	_ = d.rc.Close()
	return d.cmd.Wait()
}

func (s *Service) openForDownloadAs(ctx context.Context, as string, clientPath string) (io.ReadCloser, time.Time, error) {
	if as == "self" {
		abs, err := s.resolve(clientPath)
		if err != nil {
			return nil, time.Time{}, err
		}
		f, err := os.Open(abs)
		if err != nil {
			return nil, time.Time{}, err
		}
		st, err := f.Stat()
		if err != nil {
			_ = f.Close()
			return nil, time.Time{}, err
		}
		if st.IsDir() {
			_ = f.Close()
			return nil, time.Time{}, errors.New("cannot download a directory")
		}
		return f, st.ModTime(), nil
	}

	info, err := s.statAs(ctx, as, clientPath)
	if err != nil {
		return nil, time.Time{}, err
	}

	cmd := s.sudoCmd(ctx, as, "cat", "--path", clientPath)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, time.Time{}, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, time.Time{}, err
	}
	return &downloadReader{rc: stdout, cmd: cmd}, time.Unix(info.ModUnix, 0), nil
}

func (s *Service) saveUploadedFileAs(ctx context.Context, as string, dirAbs string, fh *multipart.FileHeader) error {
	if as == "self" {
		return s.saveUploadedFile(dirAbs, fh)
	}

	name := filepath.Base(fh.Filename)
	if err := validateName(name); err != nil {
		return err
	}
	file, err := fh.Open()
	if err != nil {
		return err
	}
	defer file.Close()

	cmd := s.sudoCmd(ctx, as, "write", "--dir", s.clientPath(dirAbs), "--name", name)
	cmd.Stdin = file
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return errors.New(strings.TrimSpace(stderr.String()))
		}
		return err
	}
	return nil
}

func (s *Service) mkdirAs(ctx context.Context, as string, clientDir string, name string) error {
	if as == "self" {
		dirAbs, err := s.resolve(clientDir)
		if err != nil {
			return err
		}
		dst := filepath.Join(dirAbs, name)
		dst, err = s.ensureWithinRoot(dst)
		if err != nil {
			return err
		}
		return os.Mkdir(dst, 0o755)
	}
	return s.runHelper(ctx, as, nil, nil, "mkdir", "--path", clientDir, "--name", name)
}

func (s *Service) touchAs(ctx context.Context, as string, clientDir string, name string) error {
	if as == "self" {
		dirAbs, err := s.resolve(clientDir)
		if err != nil {
			return err
		}
		dst := filepath.Join(dirAbs, name)
		dst, err = s.ensureWithinRoot(dst)
		if err != nil {
			return err
		}
		f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		return f.Close()
	}
	return s.runHelper(ctx, as, nil, nil, "touch", "--path", clientDir, "--name", name)
}

func (s *Service) renameAs(ctx context.Context, as string, fromClient string, toName string) error {
	if as == "self" {
		fromAbs, err := s.resolve(fromClient)
		if err != nil {
			return err
		}
		if s.clientPath(fromAbs) == "/" {
			return errors.New("cannot rename root")
		}
		dstAbs := filepath.Join(filepath.Dir(fromAbs), toName)
		dstAbs, err = s.ensureWithinRoot(dstAbs)
		if err != nil {
			return err
		}
		return os.Rename(fromAbs, dstAbs)
	}
	return s.runHelper(ctx, as, nil, nil, "rename", "--from", fromClient, "--to", toName)
}

func (s *Service) deleteAs(ctx context.Context, as string, clientPath string, recursive bool) error {
	if as == "self" {
		abs, err := s.resolve(clientPath)
		if err != nil {
			return err
		}
		if s.clientPath(abs) == "/" {
			return errors.New("cannot delete root")
		}
		if recursive {
			return os.RemoveAll(abs)
		}
		return os.Remove(abs)
	}
	args := []string{"--path", clientPath}
	if recursive {
		args = append(args, "--recursive", "1")
	}
	return s.runHelper(ctx, as, nil, nil, "delete", args...)
}

func (s *Service) writeFileAs(ctx context.Context, as string, clientPath string, content []byte) error {
	if as == "self" {
		abs, err := s.resolve(clientPath)
		if err != nil {
			return err
		}
		if s.clientPath(abs) == "/" {
			return errors.New("cannot write root")
		}
		if st, err := os.Stat(abs); err == nil && st.IsDir() {
			return errors.New("path is a directory")
		}
		f, err := os.OpenFile(abs, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.Write(content)
		return err
	}
	return s.runHelper(ctx, as, nil, bytes.NewReader(content), "writefile", "--path", clientPath)
}

func (s *Service) sudoCmd(ctx context.Context, as string, op string, args ...string) *exec.Cmd {
	cmdArgs := []string{"-n", "-u", as, s.helperPath, "fs-helper", "--root", s.root, op}
	cmdArgs = append(cmdArgs, args...)
	return exec.CommandContext(ctx, s.sudoPath, cmdArgs...)
}

func (s *Service) runHelper(ctx context.Context, as string, stdout io.Writer, stdin io.Reader, op string, args ...string) error {
	cmd := s.sudoCmd(ctx, as, op, args...)
	if stdout != nil {
		cmd.Stdout = stdout
	}
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return errors.New(strings.TrimSpace(stderr.String()))
		}
		return err
	}
	return nil
}

func normalizeClientPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	p = strings.ReplaceAll(p, "\\", "/")
	for strings.Contains(p, "//") {
		p = strings.ReplaceAll(p, "//", "/")
	}
	if len(p) > 1 && strings.HasSuffix(p, "/") {
		p = strings.TrimSuffix(p, "/")
	}
	return p
}

func lookupSelfUser() string {
	uid := os.Geteuid()
	b, err := os.ReadFile("/etc/passwd")
	if err != nil {
		return "self"
	}
	for _, line := range strings.Split(string(b), "\n") {
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.Split(line, ":")
		if len(parts) < 3 {
			continue
		}
		if parts[2] == strconv.Itoa(uid) {
			return parts[0]
		}
	}
	return "self"
}
