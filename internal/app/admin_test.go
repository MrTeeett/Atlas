package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/MrTeeett/atlas/internal/config"
	"github.com/MrTeeett/atlas/internal/userdb"
)

func TestAdminUsersAndConfigEndpoints(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "atlas.json")

	// Minimal config file for /api/admin/config.
	fileCfg := config.Config{
		Listen:             "127.0.0.1:1234",
		Root:               "/",
		BasePath:           "/x",
		EnableAdminActions: true,
		ServiceName:        "atlas.service",
		MasterKeyFile:      filepath.Join(dir, "atlas.master.key"),
		UserDBPath:         filepath.Join(dir, "atlas.users.db"),
		FWDBPath:           filepath.Join(dir, "atlas.firewall.db"),
	}
	b, _ := json.MarshalIndent(fileCfg, "", "  ")
	if err := os.WriteFile(cfgPath, append(b, '\n'), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	masterKey, err := config.EnsureMasterKeyFile(fileCfg.MasterKeyFile)
	if err != nil {
		t.Fatalf("EnsureMasterKeyFile: %v", err)
	}
	store, err := userdb.Open(fileCfg.UserDBPath, masterKey)
	if err != nil {
		t.Fatalf("userdb.Open: %v", err)
	}
	if err := store.UpsertUser("admin", "pw"); err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}
	if err := store.SetPermissions("admin", "admin", true, true, true, true, true, nil); err != nil {
		t.Fatalf("SetPermissions: %v", err)
	}

	sessionSecret := sha256.Sum256(append(append([]byte{}, masterKey...), []byte("atlas:session:v1")...))
	srv, err := New(Config{
		RootDir:            fileCfg.Root,
		BasePath:           "/x",
		AuthStore:          store,
		Secret:             sessionSecret[:],
		ConfigPath:         cfgPath,
		ServiceName:        fileCfg.ServiceName,
		EnableAdminActions: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	h := srv.Handler()

	// Login.
	form := url.Values{}
	form.Set("user", "admin")
	form.Set("pass", "pw")
	r := httptest.NewRequest(http.MethodPost, "http://example/x/login", strings.NewReader(form.Encode()))
	r.Header.Set("content-type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	cookie := strings.Split(w.Header().Get("Set-Cookie"), ";")[0]
	if cookie == "" {
		t.Fatalf("expected session cookie")
	}

	// GET admin users.
	r = httptest.NewRequest(http.MethodGet, "http://example/x/api/admin/users", nil)
	r.Header.Set("Cookie", cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("users status=%d body=%q", w.Code, w.Body.String())
	}

	// Get CSRF token.
	r = httptest.NewRequest(http.MethodGet, "http://example/x/api/me", nil)
	r.Header.Set("Cookie", cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var me struct {
		CSRF string `json:"csrf"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &me)
	if me.CSRF == "" {
		t.Fatalf("expected csrf")
	}

	// POST admin users (create).
	payload := map[string]any{
		"user":      "alice",
		"pass":      "pw2",
		"role":      "user",
		"can_exec":  false,
		"can_procs": false,
		"can_fw":    false,
		"fs_sudo":   false,
		"fs_any":    false,
		"fs_users":  []string{},
	}
	body, _ := json.Marshal(payload)
	r = httptest.NewRequest(http.MethodPost, "http://example/x/api/admin/users", bytes.NewReader(body))
	r.Header.Set("content-type", "application/json")
	r.Header.Set("Cookie", cookie)
	r.Header.Set("X-Atlas-CSRF", me.CSRF)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusCreated {
		t.Fatalf("create user status=%d body=%q", w.Code, w.Body.String())
	}

	// GET admin config.
	r = httptest.NewRequest(http.MethodGet, "http://example/x/api/admin/config", nil)
	r.Header.Set("Cookie", cookie)
	w = httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("config status=%d body=%q", w.Code, w.Body.String())
	}
}
