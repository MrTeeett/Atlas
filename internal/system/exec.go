package system

import (
	"context"
	"encoding/json"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

type ExecConfig struct {
	Enabled bool
}

type ExecService struct {
	cfg ExecConfig
}

func NewExecService(cfg ExecConfig) *ExecService {
	return &ExecService{cfg: cfg}
}

type execRequest struct {
	Command string `json:"command"`
}

type execResponse struct {
	Output string `json:"output"`
}

func (s *ExecService) HandleRun(w http.ResponseWriter, r *http.Request) {
	if !s.cfg.Enabled {
		http.Error(w, "exec is disabled (set ATLAS_ENABLE_EXEC=1)", http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	var req execRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.Command) == "" {
		http.Error(w, "command is required", http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "/bin/bash", "-lc", req.Command)
	out, _ := cmd.CombinedOutput()

	const max = 1 << 20
	sout := string(out)
	if len(sout) > max {
		sout = sout[:max] + "\n\n... output truncated ...\n"
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(execResponse{Output: sout})
}
