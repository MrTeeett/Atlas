package system

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSignal(t *testing.T) {
	t.Parallel()
	if sig, ok := parseSignal("TERM"); !ok || sig == 0 {
		t.Fatalf("expected TERM ok")
	}
	if sig, ok := parseSignal("SIGKILL"); !ok || sig == 0 {
		t.Fatalf("expected SIGKILL ok")
	}
	if sig, ok := parseSignal("9"); !ok || sig != 9 {
		t.Fatalf("expected numeric ok, got sig=%v ok=%v", sig, ok)
	}
	if _, ok := parseSignal("NOPE"); ok {
		t.Fatalf("expected NOPE to be invalid")
	}
}

func TestParseProcStatus(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	p := filepath.Join(dir, "status")
	data := "Name:\ttest\nState:\tR (running)\nUid:\t1000\t1000\t1000\t1000\nVmRSS:\t42 kB\n"
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	name, state, uid, rss, err := parseProcStatus(p)
	if err != nil {
		t.Fatalf("parseProcStatus: %v", err)
	}
	if name != "test" || uid != 1000 || rss != 42*1024 {
		t.Fatalf("got name=%q uid=%d rss=%d state=%q", name, uid, rss, state)
	}
}

func TestReadTotalCPUJiffies(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	p := filepath.Join(dir, "stat")
	if err := os.WriteFile(p, []byte("cpu 1 2 3\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	total, err := readTotalCPUJiffies(p)
	if err != nil {
		t.Fatalf("readTotalCPUJiffies: %v", err)
	}
	if total != 6 {
		t.Fatalf("total=%d", total)
	}
}
