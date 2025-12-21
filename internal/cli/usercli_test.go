package cli

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/MrTeeett/atlas/internal/config"
	"github.com/MrTeeett/atlas/internal/userdb"
)

func TestRunUserCLIFlow(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "atlas.json")

	cfg := config.Config{
		Listen:        "127.0.0.1:1",
		Root:          "/",
		BasePath:      "/x",
		MasterKeyFile: filepath.Join(dir, "atlas.master.key"),
		UserDBPath:    filepath.Join(dir, "atlas.users.db"),
		FWDBPath:      filepath.Join(dir, "atlas.firewall.db"),
	}
	b, _ := json.MarshalIndent(cfg, "", "  ")
	if err := os.WriteFile(cfgPath, append(b, '\n'), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// add
	code, err := RunUserCLI(cfgPath, []string{"add", "-user", "admin", "-pass", "pw", "-role", "admin", "-exec", "true", "-procs", "true", "-fw", "true", "-fs-sudo", "true", "-fs-any", "true"})
	if err != nil || code != 0 {
		t.Fatalf("add: code=%d err=%v", code, err)
	}

	// list
	code, err = RunUserCLI(cfgPath, []string{"list"})
	if err != nil || code != 0 {
		t.Fatalf("list: code=%d err=%v", code, err)
	}

	// set
	code, err = RunUserCLI(cfgPath, []string{"set", "-user", "admin", "-exec", "false", "-fs-users", "root,daemon"})
	if err != nil || code != 0 {
		t.Fatalf("set: code=%d err=%v", code, err)
	}

	// validate db updated
	mk, err := config.EnsureMasterKeyFile(cfg.MasterKeyFile)
	if err != nil {
		t.Fatalf("EnsureMasterKeyFile: %v", err)
	}
	st, err := userdb.Open(cfg.UserDBPath, mk)
	if err != nil {
		t.Fatalf("Open userdb: %v", err)
	}
	info, ok, err := st.GetUser("admin")
	if err != nil || !ok {
		t.Fatalf("GetUser: ok=%v err=%v", ok, err)
	}
	if info.CanExec {
		t.Fatalf("expected CanExec=false after set")
	}
	if info.FSAny {
		t.Fatalf("expected FSAny=false after fs-users set")
	}
	if len(info.FSUsers) != 2 || info.FSUsers[0] != "root" || info.FSUsers[1] != "daemon" {
		t.Fatalf("expected FSUsers [root daemon], got %#v", info.FSUsers)
	}

	// del
	code, err = RunUserCLI(cfgPath, []string{"del", "-user", "admin"})
	if err != nil || code != 0 {
		t.Fatalf("del: code=%d err=%v", code, err)
	}
}

func TestParseOptBool(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   string
		val  bool
		ok   bool
		want bool
	}{
		{"", false, false, true},
		{"true", true, true, true},
		{"1", true, true, true},
		{"off", false, true, true},
		{"no", false, true, true},
	}
	for _, tt := range tests {
		val, ok, err := parseOptBool(tt.in)
		if err != nil {
			t.Fatalf("parseOptBool(%q) err=%v", tt.in, err)
		}
		if ok != tt.ok || val != tt.val {
			t.Fatalf("parseOptBool(%q) got val=%v ok=%v", tt.in, val, ok)
		}
	}
	if _, _, err := parseOptBool("wat"); err == nil {
		t.Fatalf("expected error for bad bool")
	}
}
