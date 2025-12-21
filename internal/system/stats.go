package system

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type StatsService struct {
	mu   sync.Mutex
	prev statsSample
}

type statsSample struct {
	at       time.Time
	cpuTotal uint64
	cpuIdle  uint64
	netRx    uint64
	netTx    uint64
}

type Stats struct {
	TimeUnix int64 `json:"time_unix"`

	CPUCores    int     `json:"cpu_cores"`
	CPUUsagePct float64 `json:"cpu_usage_pct"`

	MemTotalBytes uint64  `json:"mem_total_bytes"`
	MemUsedBytes  uint64  `json:"mem_used_bytes"`
	MemUsedPct    float64 `json:"mem_used_pct"`

	DiskTotalBytes uint64  `json:"disk_total_bytes"`
	DiskUsedBytes  uint64  `json:"disk_used_bytes"`
	DiskUsedPct    float64 `json:"disk_used_pct"`

	NetRxBytesS float64 `json:"net_rx_bytes_s"`
	NetTxBytesS float64 `json:"net_tx_bytes_s"`
}

func NewStatsService() *StatsService {
	return &StatsService{}
}

func (s *StatsService) HandleStats(w http.ResponseWriter, r *http.Request) {
	st, err := s.collect()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(st)
}

func (s *StatsService) collect() (Stats, error) {
	now := time.Now()

	total, idle, cores, err := readCPUStat("/proc/stat")
	if err != nil {
		return Stats{}, err
	}

	memTotal, memAvail, err := readMemInfo("/proc/meminfo")
	if err != nil {
		return Stats{}, err
	}
	memUsed := memTotal - memAvail

	diskTotal, diskAvail, err := statFS("/")
	if err != nil {
		return Stats{}, err
	}
	diskUsed := diskTotal - diskAvail

	netRx, netTx, err := readNetDev("/proc/net/dev")
	if err != nil {
		return Stats{}, err
	}

	var cpuUsagePct float64
	var rxPerS float64
	var txPerS float64

	s.mu.Lock()
	prev := s.prev
	s.prev = statsSample{at: now, cpuTotal: total, cpuIdle: idle, netRx: netRx, netTx: netTx}
	s.mu.Unlock()

	if !prev.at.IsZero() {
		if dTotal := total - prev.cpuTotal; dTotal > 0 {
			dIdle := idle - prev.cpuIdle
			cpuUsagePct = (float64(dTotal-dIdle) / float64(dTotal)) * 100
		}
		secs := now.Sub(prev.at).Seconds()
		if secs > 0 {
			rxPerS = float64(netRx-prev.netRx) / secs
			txPerS = float64(netTx-prev.netTx) / secs
		}
	}

	memUsedPct := 0.0
	if memTotal > 0 {
		memUsedPct = (float64(memUsed) / float64(memTotal)) * 100
	}

	diskUsedPct := 0.0
	if diskTotal > 0 {
		diskUsedPct = (float64(diskUsed) / float64(diskTotal)) * 100
	}

	return Stats{
		TimeUnix: now.Unix(),

		CPUCores:    cores,
		CPUUsagePct: cpuUsagePct,

		MemTotalBytes: memTotal,
		MemUsedBytes:  memUsed,
		MemUsedPct:    memUsedPct,

		DiskTotalBytes: diskTotal,
		DiskUsedBytes:  diskUsed,
		DiskUsedPct:    diskUsedPct,

		NetRxBytesS: rxPerS,
		NetTxBytesS: txPerS,
	}, nil
}

func readCPUStat(path string) (total uint64, idle uint64, cores int, _ error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return 0, 0, 0, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "cpu ") {
			fields := strings.Fields(line)
			if len(fields) < 5 {
				return 0, 0, 0, errors.New("unexpected /proc/stat cpu line")
			}
			var nums []uint64
			for _, f := range fields[1:] {
				v, err := strconv.ParseUint(f, 10, 64)
				if err != nil {
					return 0, 0, 0, fmt.Errorf("parse /proc/stat: %w", err)
				}
				nums = append(nums, v)
			}
			for _, v := range nums {
				total += v
			}
			idle = nums[3]
			if len(nums) > 4 {
				idle += nums[4] // iowait
			}
			continue
		}
		if strings.HasPrefix(line, "cpu") && len(line) > 3 && line[3] >= '0' && line[3] <= '9' {
			cores++
		}
	}
	if err := sc.Err(); err != nil {
		return 0, 0, 0, err
	}
	if cores == 0 {
		cores = 1
	}
	return total, idle, cores, nil
}

func readMemInfo(path string) (totalBytes uint64, availableBytes uint64, _ error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "MemTotal:") {
			v, err := parseMeminfoKB(line)
			if err != nil {
				return 0, 0, err
			}
			totalBytes = v * 1024
		}
		if strings.HasPrefix(line, "MemAvailable:") {
			v, err := parseMeminfoKB(line)
			if err != nil {
				return 0, 0, err
			}
			availableBytes = v * 1024
		}
	}
	if err := sc.Err(); err != nil {
		return 0, 0, err
	}
	if totalBytes == 0 {
		return 0, 0, errors.New("MemTotal missing in /proc/meminfo")
	}
	return totalBytes, availableBytes, nil
}

func parseMeminfoKB(line string) (uint64, error) {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0, errors.New("unexpected meminfo line")
	}
	return strconv.ParseUint(fields[1], 10, 64)
}

func statFS(path string) (totalBytes uint64, availBytes uint64, _ error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	totalBytes = st.Blocks * uint64(st.Bsize)
	availBytes = st.Bavail * uint64(st.Bsize)
	return totalBytes, availBytes, nil
}

func readNetDev(path string) (rxBytes uint64, txBytes uint64, _ error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return 0, 0, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if !strings.Contains(line, ":") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		iface := strings.TrimSpace(parts[0])
		if iface == "lo" {
			continue
		}
		fields := strings.Fields(strings.TrimSpace(parts[1]))
		if len(fields) < 16 {
			continue
		}
		rx, err := strconv.ParseUint(fields[0], 10, 64)
		if err != nil {
			continue
		}
		tx, err := strconv.ParseUint(fields[8], 10, 64)
		if err != nil {
			continue
		}
		rxBytes += rx
		txBytes += tx
	}
	if err := sc.Err(); err != nil {
		return 0, 0, err
	}
	return rxBytes, txBytes, nil
}
