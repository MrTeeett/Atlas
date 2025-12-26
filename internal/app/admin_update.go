package app

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/MrTeeett/atlas/internal/buildinfo"
	"github.com/MrTeeett/atlas/internal/config"
	"github.com/MrTeeett/atlas/internal/logging"
)

type updateStatus struct {
	Running   bool      `json:"running"`
	LastAt    time.Time `json:"last_at,omitempty"`
	LastError string    `json:"last_error,omitempty"`
	TargetTag string    `json:"target_tag,omitempty"`
}

type adminUpdateResponse struct {
	ActionsEnabled bool         `json:"actions_enabled"`
	Repo           string       `json:"repo"`
	Channel        string       `json:"channel"`  // auto|stable|dev
	Resolved       string       `json:"resolved"` // stable|dev
	CurrentVersion string       `json:"version"`  // buildinfo.Version
	CurrentChannel string       `json:"build_ch"` // buildinfo.Channel
	CurrentCommit  string       `json:"commit"`   // buildinfo.Commit
	CurrentBuiltAt string       `json:"built_at"` // buildinfo.BuiltAt
	Status         updateStatus `json:"status"`
}

type adminUpdateRequest struct {
	Channel string `json:"channel"` // auto|stable|dev
}

var (
	updateMu     sync.Mutex
	updateState  updateStatus
	updateCancel context.CancelFunc
)

func (s *Server) HandleAdminUpdate(w http.ResponseWriter, r *http.Request) {
	if s.cfg.ConfigPath == "" {
		slog.Error("update: config path is not configured")
		http.Error(w, "config path is not configured", http.StatusInternalServerError)
		return
	}
	cfg, err := config.Load(s.cfg.ConfigPath)
	if err != nil {
		slog.Error("update: failed to load config", "err", err)
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	repo := strings.TrimSpace(cfg.UpdateRepo)
	channel := strings.TrimSpace(cfg.UpdateChannel)
	resolved := resolveUpdateChannel(channel)

	updateMu.Lock()
	st := updateState
	updateMu.Unlock()

	if r.Method == http.MethodGet {
		writeJSON(w, adminUpdateResponse{
			ActionsEnabled: s.cfg.EnableAdminActions,
			Repo:           repo,
			Channel:        channel,
			Resolved:       resolved,
			CurrentVersion: strings.TrimSpace(buildinfo.Version),
			CurrentChannel: strings.TrimSpace(buildinfo.Channel),
			CurrentCommit:  strings.TrimSpace(buildinfo.Commit),
			CurrentBuiltAt: strings.TrimSpace(buildinfo.BuiltAt),
			Status:         st,
		})
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !s.cfg.EnableAdminActions {
		slog.Warn("update: admin actions are disabled")
		http.Error(w, "admin actions are disabled (enable_admin_actions=false)", http.StatusForbidden)
		return
	}

	var req adminUpdateRequest
	_ = json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req)
	reqCh := strings.TrimSpace(req.Channel)
	if reqCh == "" {
		reqCh = channel
	}
	resolved = resolveUpdateChannel(reqCh)
	if resolved != "dev" && resolved != "stable" {
		slog.Warn("update: bad channel", "channel", reqCh)
		http.Error(w, "bad channel (use auto/stable/dev)", http.StatusBadRequest)
		return
	}

	updateMu.Lock()
	if updateState.Running {
		updateMu.Unlock()
		slog.Warn("update: already running")
		http.Error(w, "update already running", http.StatusConflict)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	updateCancel = cancel
	updateState = updateStatus{Running: true, LastAt: time.Now(), TargetTag: ""}
	updateMu.Unlock()

	go func() {
		defer cancel()
		defer func() {
			updateMu.Lock()
			updateState.Running = false
			updateMu.Unlock()
		}()

		started := time.Now()
		tag, err := resolveTargetTag(ctx, repo, resolved)
		if err != nil {
			slog.Error("update: resolve target tag", "err", err)
			setUpdateErr(err)
			return
		}
		updateMu.Lock()
		updateState.TargetTag = tag
		updateMu.Unlock()

		logging.InfoOrDebug("update: starting", "repo", repo, "channel", resolved, "tag", tag)
		if err := s.applyUpdate(ctx, repo, tag); err != nil {
			setUpdateErr(err)
			slog.Error("update: failed", "err", err)
			return
		}
		logging.InfoOrDebug("update: installed, restarting service", "elapsed", time.Since(started).String())
		if err := s.restartService(context.Background()); err != nil {
			slog.Warn("update: restart failed", "err", err)
		}
	}()

	logging.InfoOrDebug("update: requested", "repo", repo, "channel", resolved)
	writeJSON(w, adminActionResponse{Ok: true, Message: "update started; check Admin → Logs"})
}

func resolveUpdateChannel(cfgVal string) string {
	v := strings.TrimSpace(strings.ToLower(cfgVal))
	switch v {
	case "", "auto":
		// Auto: use buildinfo when available.
		ch := strings.TrimSpace(strings.ToLower(buildinfo.Channel))
		ver := strings.TrimSpace(strings.ToLower(buildinfo.Version))
		if ch == "dev" || ver == "dev" || strings.Contains(ver, "dev") {
			return "dev"
		}
		return "stable"
	case "stable", "release":
		return "stable"
	case "dev", "nightly":
		return "dev"
	default:
		// Keep invalid value; caller will validate if needed.
		return v
	}
}

func resolveTargetTag(ctx context.Context, repo, channel string) (string, error) {
	if channel == "dev" {
		return "dev", nil
	}
	tag, err := githubLatestTag(ctx, repo)
	if err != nil {
		return "", err
	}
	return tag, nil
}

func githubLatestTag(ctx context.Context, repo string) (string, error) {
	repo = strings.TrimSpace(repo)
	if !isValidRepo(repo) {
		return "", errors.New("bad update_repo (expected owner/name)")
	}
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Atlas")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return "", fmt.Errorf("github latest: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var out struct {
		Tag string `json:"tag_name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); err != nil {
		return "", err
	}
	tag := strings.TrimSpace(out.Tag)
	if tag == "" {
		return "", errors.New("github latest: missing tag_name")
	}
	return tag, nil
}

func isValidRepo(repo string) bool {
	if repo == "" {
		return false
	}
	if strings.Count(repo, "/") != 1 {
		return false
	}
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return false
	}
	for _, r := range repo {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.' || r == '/' {
			continue
		}
		return false
	}
	return true
}

func (s *Server) applyUpdate(ctx context.Context, repo, tag string) error {
	arch := runtime.GOARCH
	if arch != "amd64" && arch != "arm64" {
		return fmt.Errorf("unsupported arch: %s", arch)
	}
	asset := fmt.Sprintf("atlas_%s_linux_%s.tar.gz", tag, arch)
	base := fmt.Sprintf("https://github.com/%s/releases/download/%s", repo, tag)
	assetURL := base + "/" + asset
	sumsURL := base + "/SHA256SUMS.txt"

	tmpDir, err := os.MkdirTemp("", "atlas-update-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(tmpDir) }()
	slog.Debug("update: temp dir", "path", tmpDir)

	sumsPath := filepath.Join(tmpDir, "SHA256SUMS.txt")
	logging.InfoOrDebug("update: download checksums", "url", sumsURL)
	if err := downloadToFile(ctx, sumsURL, sumsPath); err != nil {
		return err
	}
	want, err := checksumForFile(sumsPath, asset)
	if err != nil {
		return err
	}

	assetPath := filepath.Join(tmpDir, asset)
	logging.InfoOrDebug("update: download asset", "url", assetURL)
	got, err := downloadWithSHA256(ctx, assetURL, assetPath)
	if err != nil {
		return err
	}
	if !strings.EqualFold(want, got) {
		return fmt.Errorf("checksum mismatch: want %s got %s", want, got)
	}
	slog.Debug("update: checksum ok", "asset", asset)

	binPath := filepath.Join(tmpDir, "atlas.new")
	logging.InfoOrDebug("update: extracting binary", "asset", asset)
	if err := extractTarGzFile(assetPath, "atlas", binPath); err != nil {
		return err
	}

	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe = filepath.Clean(exe)
	if real, err := filepath.EvalSymlinks(exe); err == nil && strings.TrimSpace(real) != "" {
		exe = real
	}
	if strings.TrimSpace(exe) == "" {
		return errors.New("cannot determine executable path")
	}

	backup := exe + ".bak"
	if err := s.runRoot(ctx, "cp", "-f", exe, backup); err != nil {
		slog.Warn("update: backup failed", "err", err, "path", backup)
	}
	slog.Debug("update: installing binary", "from", binPath, "to", exe)
	if err := s.runRoot(ctx, "install", "-m", "0755", binPath, exe); err != nil {
		return err
	}
	return nil
}

func (s *Server) restartService(ctx context.Context) error {
	name := strings.TrimSpace(s.cfg.ServiceName)
	if name == "" {
		slog.Debug("update: no service name configured; skip restart")
		return nil
	}
	if !strings.HasSuffix(name, ".service") {
		name += ".service"
	}
	// Best effort: do not fail update if restart isn't available.
	if err := s.runRoot(ctx, "systemctl", "daemon-reload"); err != nil {
		slog.Warn("update: systemctl daemon-reload failed", "err", err)
	}
	if err := s.runRoot(ctx, "systemctl", "restart", name); err != nil {
		slog.Warn("update: systemctl restart failed", "err", err, "service", name)
	}
	return nil
}

func setUpdateErr(err error) {
	updateMu.Lock()
	updateState.LastError = err.Error()
	updateMu.Unlock()
}

func downloadToFile(ctx context.Context, url, outPath string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Atlas")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return fmt.Errorf("download: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	_, err = io.Copy(f, resp.Body)
	return err
}

func downloadWithSHA256(ctx context.Context, url, outPath string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "Atlas")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return "", fmt.Errorf("download: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	f, err := os.Create(outPath)
	if err != nil {
		return "", err
	}
	defer func() { _ = f.Close() }()

	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(f, h), resp.Body)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func checksumForFile(sumsPath, filename string) (string, error) {
	b, err := os.ReadFile(sumsPath)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Format: "<sha>  <file>"
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[len(fields)-1] == filename {
			sum := strings.TrimSpace(fields[0])
			if len(sum) != 64 {
				return "", errors.New("bad checksum format")
			}
			return sum, nil
		}
	}
	return "", errors.New("checksum not found for asset")
}

func extractTarGzFile(archivePath string, wantBase string, outPath string) error {
	f, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer f.Close()

	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close()

	tr := tar.NewReader(gz)
	tmp := outPath + ".tmp"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()

	found := false
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if h == nil {
			continue
		}
		if h.Typeflag != tar.TypeReg {
			continue
		}
		if filepath.Base(h.Name) != wantBase {
			continue
		}
		if _, err := io.Copy(out, tr); err != nil {
			return err
		}
		found = true
		break
	}
	if !found {
		return errors.New("binary not found in archive")
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, outPath)
}
