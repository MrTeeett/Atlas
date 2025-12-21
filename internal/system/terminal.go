package system

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/MrTeeett/atlas/internal/auth"
)

type TerminalConfig struct {
	Enabled bool

	// SudoEnabled enables "run as linux user" via sudo -u.
	SudoEnabled bool
	SudoAny     bool
	SudoUsers   []string

	// Limits
	TailBytes  int
	SessionTTL time.Duration
}

type TerminalService struct {
	cfg      TerminalConfig
	sudoPath string
	shell    string

	mu       sync.Mutex
	sessions map[string]*termSession

	cmdIdxMu  sync.Mutex
	cmdIdx    *cmdIndex
	cmdIdxExp time.Time

	reaperOnce sync.Once
}

type termSession struct {
	id  string
	as  string
	pty ptyPair
	cmd *exec.Cmd

	mu     sync.Mutex
	closed bool
	tail   []byte
	subs   map[chan []byte]struct{}

	lastActive time.Time
}

func NewTerminalService(cfg TerminalConfig) *TerminalService {
	if cfg.TailBytes <= 0 {
		cfg.TailBytes = 256 * 1024
	}
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = 30 * time.Minute
	}
	sudoPath, _ := exec.LookPath("sudo")
	shell := "/bin/bash"
	if p, err := exec.LookPath("bash"); err == nil {
		shell = p
	}
	return &TerminalService{
		cfg:      cfg,
		sudoPath: sudoPath,
		shell:    shell,
		sessions: map[string]*termSession{},
	}
}

type termIdentity struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type identitiesResponse struct {
	Identities []termIdentity `json:"identities"`
	AllowAny   bool           `json:"allow_any,omitempty"`
}

func (s *TerminalService) HandleIdentities(w http.ResponseWriter, r *http.Request) {
	ids := []termIdentity{{ID: "self", Label: s.selfLabel()}}
	if !s.cfg.SudoEnabled || s.sudoPath == "" {
		writeJSON(w, identitiesResponse{Identities: ids})
		return
	}
	c, ok := auth.ClaimsFromContext(r.Context())
	if !ok || !c.FSSudo {
		writeJSON(w, identitiesResponse{Identities: ids})
		return
	}

	allowed := s.allowedSudoUsers(c)
	for _, u := range allowed {
		ids = append(ids, termIdentity{ID: u, Label: u})
	}
	writeJSON(w, identitiesResponse{Identities: ids, AllowAny: s.cfg.SudoAny && c.FSAny})
}

func (s *TerminalService) selfLabel() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return fmt.Sprintf("self (%s)", u.Username)
	}
	if v := strings.TrimSpace(os.Getenv("USER")); v != "" {
		return fmt.Sprintf("self (%s)", v)
	}
	return "self"
}

func (s *TerminalService) allowedSudoUsers(c auth.Claims) []string {
	var out []string
	if s.cfg.SudoAny {
		// Cannot enumerate "any user" without external system lookups. If the web user has an explicit allowlist,
		// show it; otherwise keep the dropdown minimal (UI can provide a manual input).
		if !c.FSAny {
			for _, u := range c.FSUsers {
				u = strings.TrimSpace(u)
				if u != "" {
					out = append(out, u)
				}
			}
		}
		sort.Strings(out)
		return out
	}

	cfgAllowed := map[string]bool{}
	for _, u := range s.cfg.SudoUsers {
		u = strings.TrimSpace(u)
		if u != "" && u != "*" {
			cfgAllowed[u] = true
		}
	}
	if c.FSAny {
		for u := range cfgAllowed {
			out = append(out, u)
		}
		sort.Strings(out)
		return out
	}
	userAllowed := map[string]bool{}
	for _, u := range c.FSUsers {
		u = strings.TrimSpace(u)
		if u != "" {
			userAllowed[u] = true
		}
	}
	for u := range userAllowed {
		if cfgAllowed[u] {
			out = append(out, u)
		}
	}
	sort.Strings(out)
	return out
}

type createRequest struct {
	As   string `json:"as"`
	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
}

type createResponse struct {
	ID string `json:"id"`
	As string `json:"as"`
}

func (s *TerminalService) HandleCreate(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Enabled {
		http.Error(w, "terminal is disabled", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	as := strings.TrimSpace(req.As)
	if as == "" || as == "self" {
		as = "self"
	}
	if as != "self" {
		if err := s.validateAs(r, as); err != nil {
			http.Error(w, err.Error(), http.StatusForbidden)
			return
		}
		if s.sudoPath == "" {
			http.Error(w, "sudo is not available", http.StatusForbidden)
			return
		}
	}

	id, err := randomID(18)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	sess, err := s.startSession(id, as, req.Cols, req.Rows)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	s.mu.Lock()
	s.sessions[id] = sess
	s.mu.Unlock()

	s.reaperOnce.Do(func() { go s.reaperLoop() })

	writeJSON(w, createResponse{ID: id, As: as})
}

func (s *TerminalService) validateAs(r *http.Request, as string) error {
	as = strings.TrimSpace(as)
	if as == "" || as == "self" {
		return nil
	}
	if !s.cfg.SudoEnabled {
		return errors.New("sudo mode is disabled")
	}
	c, ok := auth.ClaimsFromContext(r.Context())
	if !ok || !c.FSSudo {
		return errors.New("sudo is not permitted")
	}
	if s.cfg.SudoAny {
		if !c.FSAny && len(c.FSUsers) > 0 {
			allowed := map[string]bool{}
			for _, u := range c.FSUsers {
				u = strings.TrimSpace(u)
				if u != "" {
					allowed[u] = true
				}
			}
			if !allowed[as] {
				return errors.New("user is not allowed")
			}
		}
		return nil
	}
	cfgAllowed := map[string]bool{}
	for _, u := range s.cfg.SudoUsers {
		u = strings.TrimSpace(u)
		if u != "" {
			cfgAllowed[u] = true
		}
	}
	if !cfgAllowed[as] {
		return errors.New("user is not allowed")
	}
	if c.FSAny {
		return nil
	}
	allowed := map[string]bool{}
	for _, u := range c.FSUsers {
		u = strings.TrimSpace(u)
		if u != "" {
			allowed[u] = true
		}
	}
	if !allowed[as] {
		return errors.New("user is not allowed")
	}
	return nil
}

func (s *TerminalService) startSession(id, as string, cols, rows int) (*termSession, error) {
	pty, err := openPTY(cols, rows)
	if err != nil {
		return nil, err
	}

	var cmd *exec.Cmd
	if as == "self" {
		cmd = exec.Command(s.shell, "-i")
	} else {
		cmd = exec.Command(s.sudoPath, "-u", as, "-H", "--", s.shell, "-i")
	}

	cmd.Env = append(os.Environ(),
		"TERM=xterm-256color",
		"COLORTERM=truecolor",
		"ATLAS=1",
	)

	// Attach to slave side.
	cmd.Stdin = pty.slave
	cmd.Stdout = pty.slave
	cmd.Stderr = pty.slave

	// Start a new session and make the PTY slave its controlling TTY.
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		// In the child process, exec.Cmd will dup our stdin to fd 0 before applying SysProcAttr.
		// Using Ctty=0 is the most reliable way to reference the PTY slave in the child.
		Ctty: 0,
	}

	if err := cmd.Start(); err != nil {
		_ = pty.master.Close()
		_ = pty.slave.Close()
		return nil, err
	}
	_ = pty.slave.Close() // only master is needed by us

	sess := &termSession{
		id:         id,
		as:         as,
		pty:        pty,
		cmd:        cmd,
		subs:       map[chan []byte]struct{}{},
		lastActive: time.Now(),
	}
	go sess.readLoop(s.cfg.TailBytes)
	return sess, nil
}

func (s *TerminalService) reaperLoop() {
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for range t.C {
		now := time.Now()
		var dead []string
		s.mu.Lock()
		for id, sess := range s.sessions {
			sess.mu.Lock()
			closed := sess.closed
			last := sess.lastActive
			sess.mu.Unlock()
			if closed || now.Sub(last) > s.cfg.SessionTTL {
				dead = append(dead, id)
			}
		}
		for _, id := range dead {
			if sess := s.sessions[id]; sess != nil {
				_ = sess.close()
			}
			delete(s.sessions, id)
		}
		s.mu.Unlock()
	}
}

func (t *termSession) readLoop(tailLimit int) {
	defer func() { _ = t.close() }()
	buf := make([]byte, 32*1024)
	for {
		n, err := t.pty.master.Read(buf)
		if n > 0 {
			chunk := append([]byte{}, buf[:n]...)
			t.mu.Lock()
			t.lastActive = time.Now()
			if tailLimit > 0 {
				if len(t.tail)+len(chunk) > tailLimit {
					drop := (len(t.tail) + len(chunk)) - tailLimit
					if drop >= len(t.tail) {
						t.tail = t.tail[:0]
					} else {
						t.tail = append([]byte{}, t.tail[drop:]...)
					}
				}
				t.tail = append(t.tail, chunk...)
			}
			for ch := range t.subs {
				select {
				case ch <- chunk:
				default:
				}
			}
			t.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

func (t *termSession) close() error {
	t.mu.Lock()
	if t.closed {
		t.mu.Unlock()
		return nil
	}
	t.closed = true
	for ch := range t.subs {
		close(ch)
		delete(t.subs, ch)
	}
	t.mu.Unlock()

	// Try to stop the whole process group.
	if t.cmd != nil && t.cmd.Process != nil {
		_ = syscall.Kill(-t.cmd.Process.Pid, syscall.SIGTERM)
		done := make(chan struct{})
		go func() {
			_ = t.cmd.Wait()
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(800 * time.Millisecond):
			_ = syscall.Kill(-t.cmd.Process.Pid, syscall.SIGKILL)
			_ = t.cmd.Wait()
		}
	}
	if t.pty.master != nil {
		_ = t.pty.master.Close()
	}
	return nil
}

func (s *TerminalService) HandleSession(w http.ResponseWriter, r *http.Request) {
	// /api/term/session/{id}/(stream|write|resize)
	path := strings.TrimPrefix(r.URL.Path, "/api/term/session/")
	path = strings.TrimPrefix(path, "/")
	parts := strings.Split(path, "/")
	if len(parts) < 1 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	id := parts[0]
	action := ""
	if len(parts) >= 2 {
		action = parts[1]
	}

	sess := s.getSession(id)
	if sess == nil {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	switch action {
	case "stream":
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		s.handleStream(w, r, sess)
	case "write":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		s.handleWrite(w, r, sess)
	case "resize":
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		s.handleResize(w, r, sess)
	default:
		// DELETE /api/term/session/{id}
		if r.Method == http.MethodDelete {
			s.handleClose(w, r, sess)
			return
		}
		if len(parts) == 1 && r.Method == http.MethodDelete {
			s.handleClose(w, r, sess)
			return
		}
		http.NotFound(w, r)
	}
}

func (s *TerminalService) getSession(id string) *termSession {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[id]
}

func (s *TerminalService) handleStream(w http.ResponseWriter, r *http.Request, sess *termSession) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	ch := make(chan []byte, 128)
	sess.mu.Lock()
	if sess.closed {
		sess.mu.Unlock()
		http.Error(w, "closed", http.StatusGone)
		return
	}
	sess.subs[ch] = struct{}{}
	tail := append([]byte{}, sess.tail...)
	sess.mu.Unlock()

	defer func() {
		sess.mu.Lock()
		delete(sess.subs, ch)
		sess.mu.Unlock()
	}()

	if len(tail) > 0 {
		_, _ = w.Write(tail)
		fl.Flush()
	}

	notify := r.Context().Done()
	for {
		select {
		case <-notify:
			return
		case b, ok := <-ch:
			if !ok {
				return
			}
			if len(b) == 0 {
				continue
			}
			_, err := w.Write(b)
			if err != nil {
				return
			}
			fl.Flush()
		}
	}
}

type writeRequest struct {
	DataB64 string `json:"data_b64"`
}

func (s *TerminalService) handleWrite(w http.ResponseWriter, r *http.Request, sess *termSession) {
	var req writeRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	raw, err := base64.RawStdEncoding.DecodeString(req.DataB64)
	if err != nil {
		http.Error(w, "bad base64", http.StatusBadRequest)
		return
	}
	if len(raw) == 0 {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	sess.mu.Lock()
	sess.lastActive = time.Now()
	sess.mu.Unlock()
	_, _ = sess.pty.master.Write(raw)
	w.WriteHeader(http.StatusNoContent)
}

type resizeRequest struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

func (s *TerminalService) handleResize(w http.ResponseWriter, r *http.Request, sess *termSession) {
	var req resizeRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if req.Cols <= 0 || req.Rows <= 0 {
		http.Error(w, "cols/rows required", http.StatusBadRequest)
		return
	}
	_ = setWinSize(sess.pty.master, req.Cols, req.Rows)
	sess.mu.Lock()
	sess.lastActive = time.Now()
	sess.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *TerminalService) handleClose(w http.ResponseWriter, r *http.Request, sess *termSession) {
	_ = sess.close()
	s.mu.Lock()
	delete(s.sessions, sess.id)
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

type completeResponse struct {
	Items []completeItem `json:"items"`
}

type completeItem struct {
	Label  string `json:"label"`
	Detail string `json:"detail,omitempty"`
}

func (s *TerminalService) HandleComplete(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		writeJSON(w, completeResponse{Items: nil})
		return
	}
	if len(q) > 128 {
		q = q[:128]
	}
	items := s.completeCommands(q)
	writeJSON(w, completeResponse{Items: items})
}

type cmdIndex struct {
	names []string
	where map[string]string
}

func (s *TerminalService) completeCommands(prefix string) []completeItem {
	idx := s.getCmdIndex()
	prefix = strings.ToLower(prefix)
	var out []completeItem
	for _, name := range idx.names {
		if !strings.HasPrefix(strings.ToLower(name), prefix) {
			continue
		}
		detail := idx.where[name]
		out = append(out, completeItem{Label: name, Detail: detail})
		if len(out) >= 60 {
			break
		}
	}
	return out
}

func (s *TerminalService) getCmdIndex() *cmdIndex {
	s.cmdIdxMu.Lock()
	defer s.cmdIdxMu.Unlock()
	if s.cmdIdx != nil && time.Now().Before(s.cmdIdxExp) {
		return s.cmdIdx
	}
	s.cmdIdx = buildCmdIndex()
	s.cmdIdxExp = time.Now().Add(60 * time.Second)
	return s.cmdIdx
}

func buildCmdIndex() *cmdIndex {
	where := map[string]string{}
	add := func(name, detail string) {
		if name == "" {
			return
		}
		if _, ok := where[name]; ok {
			return
		}
		where[name] = detail
	}
	for _, b := range []string{"cd", "exit", "clear", "export", "unset", "alias", "unalias", "history", "jobs", "fg", "bg", "kill", "sudo"} {
		add(b, "builtin")
	}
	path := os.Getenv("PATH")
	for _, dir := range strings.Split(path, ":") {
		dir = strings.TrimSpace(dir)
		if dir == "" {
			continue
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range ents {
			if e.IsDir() {
				continue
			}
			name := e.Name()
			info, err := e.Info()
			if err != nil {
				continue
			}
			if info.Mode()&0o111 == 0 {
				continue
			}
			add(name, dir)
		}
	}
	var names []string
	for n := range where {
		names = append(names, n)
	}
	sort.Strings(names)
	return &cmdIndex{names: names, where: where}
}

func randomID(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// stripANSI is used only for server-side errors; terminal output stays raw.
func stripANSI(b []byte) []byte {
	// Very small sanitizer: remove ESC[...] sequences.
	var out bytes.Buffer
	for i := 0; i < len(b); i++ {
		if b[i] != 0x1b {
			out.WriteByte(b[i])
			continue
		}
		// ESC [
		if i+1 < len(b) && b[i+1] == '[' {
			i += 2
			for i < len(b) {
				c := b[i]
				if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
					break
				}
				i++
			}
		}
	}
	return out.Bytes()
}
