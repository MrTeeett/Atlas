package system

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadOSReleasePrefersPrettyName(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	p := filepath.Join(dir, "os-release")
	data := "NAME=\"X\"\nPRETTY_NAME=\"Pretty X\"\n"
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	v, err := readOSRelease(p)
	if err != nil {
		t.Fatalf("readOSRelease: %v", err)
	}
	if v != "Pretty X" {
		t.Fatalf("got %q", v)
	}
}

func TestReadOSReleaseFallsBackToName(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	p := filepath.Join(dir, "os-release")
	data := "NAME=X\n"
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	v, err := readOSRelease(p)
	if err != nil {
		t.Fatalf("readOSRelease: %v", err)
	}
	if v != "X" {
		t.Fatalf("got %q", v)
	}
}

func TestReadUptimeAndLoadAvg(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	up := filepath.Join(dir, "uptime")
	la := filepath.Join(dir, "loadavg")
	if err := os.WriteFile(up, []byte("123.45 0.00\n"), 0o600); err != nil {
		t.Fatalf("WriteFile uptime: %v", err)
	}
	if err := os.WriteFile(la, []byte("1.00 5.00 15.00 1/1 1\n"), 0o600); err != nil {
		t.Fatalf("WriteFile loadavg: %v", err)
	}
	u, err := readUptimeSeconds(up)
	if err != nil || u != 123.45 {
		t.Fatalf("uptime=%v err=%v", u, err)
	}
	l1, l5, l15, err := readLoadAvg(la)
	if err != nil || l1 != 1.0 || l5 != 5.0 || l15 != 15.0 {
		t.Fatalf("load=%v %v %v err=%v", l1, l5, l15, err)
	}
}

func TestCharsToStringStopsAtZero(t *testing.T) {
	t.Parallel()
	in := []int8{'a', 'b', 0, 'c'}
	if got := charsToString(in); got != "ab" {
		t.Fatalf("got %q", got)
	}
}
