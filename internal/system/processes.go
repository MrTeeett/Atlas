package system

import (
	"bufio"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type ProcessService struct {
	mu          sync.Mutex
	passwdAt    time.Time
	uidToUser   map[uint32]string
	passwdError error

	prevAt      time.Time
	prevTotal   uint64
	prevPerProc map[int]uint64
}

type Process struct {
	PID         int     `json:"pid"`
	User        string  `json:"user"`
	Command     string  `json:"command"`
	RSSBytes    uint64  `json:"rss_bytes"`
	State       string  `json:"state"`
	CPUUsagePct float64 `json:"cpu_usage_pct"`
}

type processListResponse struct {
	Processes []Process `json:"processes"`
}

func NewProcessService() *ProcessService {
	return &ProcessService{}
}

func (s *ProcessService) HandleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	ps, err := s.list()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(processListResponse{Processes: ps})
}

type signalRequest struct {
	PID    int    `json:"pid"`
	PIDs   []int  `json:"pids"`
	Signal string `json:"signal"`
}

func (s *ProcessService) HandleSignal(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var req signalRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	var pids []int
	if req.PID > 0 {
		pids = append(pids, req.PID)
	}
	for _, pid := range req.PIDs {
		if pid > 0 {
			pids = append(pids, pid)
		}
	}
	if len(pids) == 0 {
		http.Error(w, "pid(s) are required", http.StatusBadRequest)
		return
	}
	sig, ok := parseSignal(req.Signal)
	if !ok {
		http.Error(w, "unknown signal", http.StatusBadRequest)
		return
	}

	seen := map[int]bool{}
	for _, pid := range pids {
		if seen[pid] {
			continue
		}
		seen[pid] = true
		if err := syscall.Kill(pid, sig); err != nil {
			http.Error(w, "kill pid "+strconv.Itoa(pid)+": "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *ProcessService) list() ([]Process, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}

	users := s.loadPasswd()

	totalNow, err := readTotalCPUJiffies("/proc/stat")
	if err != nil {
		return nil, err
	}
	now := time.Now()

	var out []Process
	perProcNow := map[int]uint64{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		p, err := readProc(pid, users)
		if err != nil {
			continue
		}
		if ticks, err := readProcCPUJiffies(pid); err == nil {
			perProcNow[pid] = ticks
		}
		out = append(out, p)
	}

	s.mu.Lock()
	prevAt := s.prevAt
	prevTotal := s.prevTotal
	prevPerProc := s.prevPerProc
	s.prevAt = now
	s.prevTotal = totalNow
	s.prevPerProc = perProcNow
	s.mu.Unlock()

	if !prevAt.IsZero() && prevTotal > 0 && totalNow > prevTotal {
		dTotal := totalNow - prevTotal
		for i := range out {
			prevTicks := uint64(0)
			if prevPerProc != nil {
				prevTicks = prevPerProc[out[i].PID]
			}
			nowTicks := perProcNow[out[i].PID]
			if nowTicks > prevTicks {
				out[i].CPUUsagePct = (float64(nowTicks-prevTicks) / float64(dTotal)) * 100
			}
		}
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].RSSBytes == out[j].RSSBytes {
			return out[i].PID < out[j].PID
		}
		return out[i].RSSBytes > out[j].RSSBytes
	})
	if len(out) > 300 {
		out = out[:300]
	}
	return out, nil
}

func readProc(pid int, uidToUser map[uint32]string) (Process, error) {
	statusPath := filepath.Join("/proc", strconv.Itoa(pid), "status")
	name, state, uid, rss, err := parseProcStatus(statusPath)
	if err != nil {
		return Process{}, err
	}

	cmdlinePath := filepath.Join("/proc", strconv.Itoa(pid), "cmdline")
	cmdline, _ := os.ReadFile(cmdlinePath)
	command := strings.TrimSpace(strings.ReplaceAll(string(cmdline), "\x00", " "))
	if command == "" {
		command = name
	}

	user := uidToUser[uid]

	return Process{
		PID:      pid,
		User:     user,
		Command:  command,
		RSSBytes: rss,
		State:    state,
	}, nil
}

func readTotalCPUJiffies(path string) (uint64, error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return 0, err
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "cpu ") {
			fields := strings.Fields(line)
			var total uint64
			for _, f := range fields[1:] {
				v, err := strconv.ParseUint(f, 10, 64)
				if err != nil {
					return 0, err
				}
				total += v
			}
			return total, nil
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return 0, errors.New("missing cpu line in /proc/stat")
}

func readProcCPUJiffies(pid int) (uint64, error) {
	// /proc/<pid>/stat: field 14=utime, 15=stime
	b, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return 0, err
	}
	line := string(b)
	// comm is in parentheses and may contain spaces. Find the last ') '.
	rp := strings.LastIndex(line, ") ")
	if rp < 0 {
		return 0, errors.New("bad stat format")
	}
	rest := strings.Fields(line[rp+2:])
	if len(rest) < 15 {
		return 0, errors.New("bad stat fields")
	}
	utime, err := strconv.ParseUint(rest[11], 10, 64)
	if err != nil {
		return 0, err
	}
	stime, err := strconv.ParseUint(rest[12], 10, 64)
	if err != nil {
		return 0, err
	}
	return utime + stime, nil
}

func parseSignal(s string) (syscall.Signal, bool) {
	s = strings.TrimSpace(strings.ToUpper(s))
	s = strings.TrimPrefix(s, "SIG")
	switch s {
	case "HUP":
		return syscall.SIGHUP, true
	case "INT":
		return syscall.SIGINT, true
	case "TERM":
		return syscall.SIGTERM, true
	case "KILL":
		return syscall.SIGKILL, true
	case "STOP":
		return syscall.SIGSTOP, true
	case "CONT":
		return syscall.SIGCONT, true
	case "USR1":
		return syscall.SIGUSR1, true
	case "USR2":
		return syscall.SIGUSR2, true
	default:
		// allow numbers
		if n, err := strconv.Atoi(s); err == nil && n > 0 && n < 128 {
			return syscall.Signal(n), true
		}
		return 0, false
	}
}

func parseProcStatus(path string) (name string, state string, uid uint32, rssBytes uint64, _ error) {
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return "", "", 0, 0, err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "Name:"):
			name = strings.TrimSpace(strings.TrimPrefix(line, "Name:"))
		case strings.HasPrefix(line, "State:"):
			state = strings.TrimSpace(strings.TrimPrefix(line, "State:"))
		case strings.HasPrefix(line, "Uid:"):
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				v, err := strconv.ParseUint(fields[1], 10, 32)
				if err == nil {
					uid = uint32(v)
				}
			}
		case strings.HasPrefix(line, "VmRSS:"):
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				v, err := strconv.ParseUint(fields[1], 10, 64)
				if err == nil {
					rssBytes = v * 1024
				}
			}
		}
	}
	if err := sc.Err(); err != nil {
		return "", "", 0, 0, err
	}
	if name == "" {
		return "", "", 0, 0, errors.New("missing Name in status")
	}
	return name, state, uid, rssBytes, nil
}

func (s *ProcessService) loadPasswd() map[uint32]string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if time.Since(s.passwdAt) < 5*time.Minute && s.uidToUser != nil {
		return s.uidToUser
	}

	m := map[uint32]string{}
	b, err := os.ReadFile("/etc/passwd")
	if err == nil {
		for _, line := range strings.Split(string(b), "\n") {
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			parts := strings.Split(line, ":")
			if len(parts) < 3 {
				continue
			}
			uid64, err := strconv.ParseUint(parts[2], 10, 32)
			if err != nil {
				continue
			}
			m[uint32(uid64)] = parts[0]
		}
	}
	s.passwdAt = time.Now()
	s.uidToUser = m
	s.passwdError = err
	return s.uidToUser
}
