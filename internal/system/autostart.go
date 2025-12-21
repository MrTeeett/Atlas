package system

import (
	"bufio"
	"context"
	"net/http"
	"os/exec"
	"strings"
	"time"
)

type AutostartService struct{}

func NewAutostartService() *AutostartService { return &AutostartService{} }

type AutostartItem struct {
	Unit        string `json:"unit"`
	Enabled     bool   `json:"enabled"`
	ActiveState string `json:"active_state,omitempty"`
	SubState    string `json:"sub_state,omitempty"`
	Description string `json:"description,omitempty"`
}

type AutostartResponse struct {
	Supported bool            `json:"supported"`
	Items     []AutostartItem `json:"items,omitempty"`
	Message   string          `json:"message,omitempty"`
}

func (s *AutostartService) HandleAutostart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	systemctl, err := exec.LookPath("systemctl")
	if err != nil {
		writeJSON(w, AutostartResponse{Supported: false, Message: "systemctl not found"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	units, msg := listEnabledServices(ctx, systemctl)
	if len(units) == 0 {
		writeJSON(w, AutostartResponse{Supported: true, Items: nil, Message: msg})
		return
	}

	// systemctl show does not handle template units like getty@.service. Keep them as-is.
	var templateItems []AutostartItem
	var showable []string
	for _, u := range units {
		if isTemplateUnit(u) {
			templateItems = append(templateItems, AutostartItem{
				Unit:        u,
				Enabled:     true,
				Description: "(template)",
			})
			continue
		}
		showable = append(showable, u)
	}

	items, showMsg := showUnits(ctx, systemctl, showable)
	items = append(items, templateItems...)
	if msg == "" {
		msg = showMsg
	} else if showMsg != "" {
		msg = strings.TrimSpace(msg + "; " + showMsg)
	}
	writeJSON(w, AutostartResponse{Supported: true, Items: items, Message: msg})
}

func listEnabledServices(ctx context.Context, systemctl string) ([]string, string) {
	cmd := exec.CommandContext(ctx, systemctl,
		"list-unit-files",
		"--type=service",
		"--state=enabled",
		"--no-legend",
		"--no-pager",
	)
	out, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(out))
	if err != nil {
		if s == "" {
			s = err.Error()
		}
		// Still return message; keep empty list.
		return nil, s
	}

	var units []string
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 1 {
			continue
		}
		u := fields[0]
		if !strings.HasSuffix(u, ".service") {
			continue
		}
		units = append(units, u)
		if len(units) >= 200 {
			break
		}
	}
	return units, ""
}

func isTemplateUnit(unit string) bool {
	unit = strings.TrimSpace(unit)
	// Template unit files look like "getty@.service" and can't be queried via `systemctl show`.
	return strings.Contains(unit, "@.service")
}

func showUnits(ctx context.Context, systemctl string, units []string) ([]AutostartItem, string) {
	var out []AutostartItem
	var msg string
	// Chunk to avoid huge argv.
	const chunkSize = 80
	for i := 0; i < len(units); i += chunkSize {
		j := i + chunkSize
		if j > len(units) {
			j = len(units)
		}
		items, chunkMsg := showUnitsChunk(ctx, systemctl, units[i:j])
		out = append(out, items...)
		if msg == "" {
			msg = chunkMsg
		} else if chunkMsg != "" {
			msg = strings.TrimSpace(msg + "; " + chunkMsg)
		}
	}
	return out, msg
}

func showUnitsChunk(ctx context.Context, systemctl string, units []string) ([]AutostartItem, string) {
	args := []string{"show", "-p", "Id", "-p", "ActiveState", "-p", "SubState", "-p", "Description"}
	args = append(args, units...)
	cmd := exec.CommandContext(ctx, systemctl, args...)
	raw, err := cmd.CombinedOutput()
	s := strings.TrimSpace(string(raw))

	items := parseSystemctlShow(s)

	if err == nil {
		return items, ""
	}

	if s == "" {
		return items, err.Error()
	}
	return items, firstShowErrorLine(s)
}

func parseSystemctlShow(out string) []AutostartItem {
	var items []AutostartItem
	var cur AutostartItem
	have := false

	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "Id=") {
			if have {
				items = append(items, cur)
			}
			cur = AutostartItem{Unit: strings.TrimPrefix(line, "Id="), Enabled: true}
			have = true
			continue
		}
		if !have {
			continue
		}
		if strings.HasPrefix(line, "ActiveState=") {
			cur.ActiveState = strings.TrimPrefix(line, "ActiveState=")
		} else if strings.HasPrefix(line, "SubState=") {
			cur.SubState = strings.TrimPrefix(line, "SubState=")
		} else if strings.HasPrefix(line, "Description=") {
			cur.Description = strings.TrimPrefix(line, "Description=")
		}
	}
	if have {
		items = append(items, cur)
	}
	return items
}

func firstShowErrorLine(out string) string {
	sc := bufio.NewScanner(strings.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "Failed to") || strings.HasPrefix(line, "Unit ") {
			return line
		}
	}
	// Fallback: first line only.
	if i := strings.IndexByte(out, '\n'); i >= 0 {
		return strings.TrimSpace(out[:i])
	}
	return strings.TrimSpace(out)
}
