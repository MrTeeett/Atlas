package system

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadCPUStatParsesTotalsAndCores(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	p := filepath.Join(dir, "stat")
	// cpu line: user nice system idle iowait irq softirq steal guest guest_nice
	data := "cpu  1 2 3 4 5 6 7 8 9 10\ncpu0 0 0 0 0 0 0 0 0 0 0\ncpu1 0 0 0 0 0 0 0 0 0 0\n"
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	total, idle, cores, err := readCPUStat(p)
	if err != nil {
		t.Fatalf("readCPUStat: %v", err)
	}
	if cores != 2 {
		t.Fatalf("cores=%d", cores)
	}
	// total = 1..10 sum = 55
	if total != 55 {
		t.Fatalf("total=%d", total)
	}
	// idle = idle(4)+iowait(5)=9
	if idle != 9 {
		t.Fatalf("idle=%d", idle)
	}
}

func TestReadMemInfoParsesTotalAndAvailable(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	p := filepath.Join(dir, "meminfo")
	data := "MemTotal:       2048 kB\nMemAvailable:   1024 kB\n"
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	total, avail, err := readMemInfo(p)
	if err != nil {
		t.Fatalf("readMemInfo: %v", err)
	}
	if total != 2048*1024 || avail != 1024*1024 {
		t.Fatalf("total=%d avail=%d", total, avail)
	}
}

func TestReadNetDevSkipsLoAndSums(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	p := filepath.Join(dir, "netdev")
	data := `Inter-|   Receive                                                |  Transmit
 face |bytes    packets errs drop fifo frame compressed multicast|bytes    packets errs drop fifo colls carrier compressed
  lo: 1 0 0 0 0 0 0 0 2 0 0 0 0 0 0 0
eth0: 10 0 0 0 0 0 0 0 20 0 0 0 0 0 0 0
wlan0: 5 0 0 0 0 0 0 0 7 0 0 0 0 0 0 0
`
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	rx, tx, err := readNetDev(p)
	if err != nil {
		t.Fatalf("readNetDev: %v", err)
	}
	if rx != 15 || tx != 27 {
		t.Fatalf("rx=%d tx=%d", rx, tx)
	}
}
