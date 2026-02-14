package system

import (
	"bufio"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/MrTeeett/atlas/internal/buildinfo"
)

type InfoService struct{}

type SystemInfo struct {
	TimeUnix int64 `json:"time_unix"`

	AtlasVersion string `json:"atlas_version"`
	AtlasChannel string `json:"atlas_channel"`
	AtlasCommit  string `json:"atlas_commit"`
	AtlasBuiltAt string `json:"atlas_built_at"`

	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Kernel   string `json:"kernel"`

	UptimeSeconds float64 `json:"uptime_seconds"`
	Load1         float64 `json:"load1"`
	Load5         float64 `json:"load5"`
	Load15        float64 `json:"load15"`
}

func NewInfoService() *InfoService { return &InfoService{} }

func (s *InfoService) HandleInfo(w http.ResponseWriter, r *http.Request) {
	info, err := collectSystemInfo()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(info)
}

func (s *InfoService) Collect() (SystemInfo, error) {
	return collectSystemInfo()
}

func collectSystemInfo() (SystemInfo, error) {
	host, _ := os.Hostname()
	osName, _ := readOSRelease("/etc/os-release")
	kernel, _ := readKernel()
	uptime, _ := readUptimeSeconds("/proc/uptime")
	l1, l5, l15, _ := readLoadAvg("/proc/loadavg")

	return SystemInfo{
		TimeUnix: time.Now().Unix(),

		AtlasVersion: strings.TrimSpace(buildinfo.Version),
		AtlasChannel: strings.TrimSpace(buildinfo.Channel),
		AtlasCommit:  strings.TrimSpace(buildinfo.Commit),
		AtlasBuiltAt: strings.TrimSpace(buildinfo.BuiltAt),

		Hostname: host,
		OS:       osName,
		Kernel:   kernel,

		UptimeSeconds: uptime,
		Load1:         l1,
		Load5:         l5,
		Load15:        l15,
	}, nil
}

func readOSRelease(path string) (string, error) {
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return "", err
	}
	m := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		m[k] = v
	}
	if v := m["PRETTY_NAME"]; v != "" {
		return v, nil
	}
	if v := m["NAME"]; v != "" {
		return v, nil
	}
	return "", errors.New("os-release missing NAME")
}

func readKernel() (string, error) {
	var u syscall.Utsname
	if err := syscall.Uname(&u); err != nil {
		return "", err
	}
	return strings.TrimSpace(charsToString(u.Sysname[:]) + " " + charsToString(u.Release[:])), nil
}

func charsToString(in []int8) string {
	b := make([]byte, 0, len(in))
	for _, c := range in {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b)
}

func readUptimeSeconds(path string) (float64, error) {
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return 0, err
	}
	fields := strings.Fields(string(b))
	if len(fields) < 1 {
		return 0, errors.New("bad /proc/uptime")
	}
	return strconv.ParseFloat(fields[0], 64)
}

func readLoadAvg(path string) (float64, float64, float64, error) {
	b, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return 0, 0, 0, err
	}
	fields := strings.Fields(string(b))
	if len(fields) < 3 {
		return 0, 0, 0, errors.New("bad /proc/loadavg")
	}
	l1, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, 0, 0, err
	}
	l5, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return 0, 0, 0, err
	}
	l15, err := strconv.ParseFloat(fields[2], 64)
	if err != nil {
		return 0, 0, 0, err
	}
	return l1, l5, l15, nil
}
