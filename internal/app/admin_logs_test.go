package app

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTailLines(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "atlas.log")
	var b strings.Builder
	for i := 0; i < 50; i++ {
		b.WriteString("line ")
		b.WriteString(string(rune('A' + (i % 26))))
		b.WriteString("\n")
	}
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	lines, _, truncated, err := tailLines(p, 5, 1<<20)
	if err != nil {
		t.Fatalf("tailLines: %v", err)
	}
	if truncated {
		t.Fatalf("expected not truncated")
	}
	if len(lines) != 5 {
		t.Fatalf("expected 5 lines, got %d", len(lines))
	}
	if !strings.HasPrefix(lines[0], "line ") {
		t.Fatalf("unexpected line: %q", lines[0])
	}
}

func TestHandleAdminLogsDisabled(t *testing.T) {
	s := &Server{cfg: Config{LogPath: "/tmp/nope.log", LogLevel: "off"}}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/admin/logs?n=10", nil)
	s.HandleAdminLogs(rr, req)
	if rr.Code != 200 {
		t.Fatalf("status: %d body=%s", rr.Code, rr.Body.String())
	}
	var out struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil {
		t.Fatalf("json: %v", err)
	}
	if out.Enabled {
		t.Fatalf("expected disabled")
	}
}

