package system

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MrTeeett/atlas/internal/auth"
)

type FirewallConfig struct {
	Enabled      bool
	DBPath       string
	SudoPassword func(user string) (string, bool, error)
}

type FirewallService struct {
	cfg FirewallConfig

	mu sync.Mutex
	db fwDB

	nftPath       string
	ssPath        string
	sudoPath      string
	ufwPath       string
	fwCmdPath     string
	systemctlPath string
	sudoPassword  func(user string) (string, bool, error)
}

type fwDB struct {
	Version int       `json:"version"`
	Enabled bool      `json:"enabled"`
	Rules   []FWRule  `json:"rules"`
	Updated time.Time `json:"updated_utc,omitempty"`
}

type FWRule struct {
	ID      string `json:"id"`
	Enabled bool   `json:"enabled"`
	Type    string `json:"type"` // allow|deny|redirect
	Proto   string `json:"proto"`

	PortFrom int `json:"port_from"`
	PortTo   int `json:"port_to,omitempty"`

	ToPort int `json:"to_port,omitempty"` // redirect

	Service string    `json:"service,omitempty"`
	Comment string    `json:"comment,omitempty"`
	Created time.Time `json:"created_utc,omitempty"`
}

// UFWRule is a read-only representation of a ufw rule.
type UFWRule struct {
	To     string `json:"to"`
	Action string `json:"action"`
	From   string `json:"from"`
	V6     bool   `json:"v6,omitempty"`
	Raw    string `json:"raw,omitempty"`
}

type fwStatus struct {
	ConfigEnabled  bool   `json:"config_enabled"`
	DBEnabled      bool   `json:"db_enabled"`
	Tool           string `json:"tool"`
	Active         bool   `json:"active"`
	Error          string `json:"error,omitempty"`
	ExternalTool   string `json:"external_tool,omitempty"`
	ExternalActive bool   `json:"external_active,omitempty"`
	ExternalError  string `json:"external_error,omitempty"`
	EUID           int    `json:"euid"`
	HasSudo        bool   `json:"has_sudo"`
	DBPath         string `json:"db_path,omitempty"`
}

func NewFirewallService(cfg FirewallConfig) *FirewallService {
	nft, _ := exec.LookPath("nft")
	ss, _ := exec.LookPath("ss")
	sudo, _ := exec.LookPath("sudo")
	ufw, _ := exec.LookPath("ufw")
	fwcmd, _ := exec.LookPath("firewall-cmd")
	systemctl, _ := exec.LookPath("systemctl")
	s := &FirewallService{
		cfg:           cfg,
		nftPath:       nft,
		ssPath:        ss,
		sudoPath:      sudo,
		ufwPath:       ufw,
		fwCmdPath:     fwcmd,
		systemctlPath: systemctl,
		sudoPassword:  cfg.SudoPassword,
		db: fwDB{
			Version: 1,
			Enabled: false,
			Rules:   nil,
		},
	}
	_ = s.load()
	return s
}

func (s *FirewallService) HandleStatus(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	enabled := s.db.Enabled
	dbPath := s.cfg.DBPath
	s.mu.Unlock()

	backend, berr := s.backend()
	tool := backendToolName(backend)
	st := fwStatus{
		ConfigEnabled: s.cfg.Enabled,
		DBEnabled:     enabled,
		Tool:          tool,
		EUID:          os.Geteuid(),
		HasSudo:       s.sudoPath != "",
		DBPath:        dbPath,
	}
	if berr != nil {
		st.Error = berr.Error()
	}
	if !s.cfg.Enabled {
		writeJSON(w, st)
		return
	}
	active, err := s.backendActive(r.Context(), backend)
	st.Active = active
	if err != nil {
		st.Error = err.Error()
	}
	if backend != "nft" {
		st.DBEnabled = active
	}
	writeJSON(w, st)
}

type setEnabledRequest struct {
	Enabled bool `json:"enabled"`
}

func (s *FirewallService) HandleEnabled(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !s.cfg.Enabled {
		http.Error(w, "firewall is disabled by config", http.StatusForbidden)
		return
	}
	var req setEnabledRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
	defer cancel()

	backend, berr := s.backend()
	if berr != nil {
		http.Error(w, berr.Error(), http.StatusInternalServerError)
		return
	}
	if backend != "nft" {
		if err := s.setSystemFirewallEnabled(ctx, backend, req.Enabled); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.mu.Lock()
		s.db.Enabled = req.Enabled
		s.db.Updated = time.Now().UTC()
		_ = s.saveLocked()
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return
	}

	s.mu.Lock()
	prev := s.db
	s.db.Enabled = req.Enabled
	s.db.Updated = time.Now().UTC()
	if err := s.saveLocked(); err != nil {
		s.db = prev
		s.mu.Unlock()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	err := s.applyLocked(ctx)
	if err != nil {
		// rollback
		s.db = prev
		_ = s.saveLocked()
		s.mu.Unlock()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func (s *FirewallService) HandleApply(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if !s.cfg.Enabled {
		http.Error(w, "firewall is disabled by config", http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	backend, berr := s.backend()
	if berr != nil {
		http.Error(w, berr.Error(), http.StatusInternalServerError)
		return
	}
	if backend != "nft" {
		if err := s.applySystem(ctx, backend); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
		return
	}

	s.mu.Lock()
	err := s.applyLocked(ctx)
	s.mu.Unlock()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type rulesResponse struct {
	Enabled        bool      `json:"enabled"`
	Rules          []FWRule  `json:"rules"`
	ExternalTool   string    `json:"external_tool,omitempty"`
	ExternalActive bool      `json:"external_active,omitempty"`
	ExternalRules  []UFWRule `json:"external_rules,omitempty"`
	ExternalError  string    `json:"external_error,omitempty"`
}

type createRuleRequest struct {
	Enabled  bool   `json:"enabled"`
	Type     string `json:"type"`
	Proto    string `json:"proto"`
	Ports    string `json:"ports"`   // "80" or "1000-2000"
	ToPort   int    `json:"to_port"` // redirect
	Service  string `json:"service,omitempty"`
	Comment  string `json:"comment"`  // optional
	Position int    `json:"position"` // optional insert at index; -1 append
}

func (s *FirewallService) HandleRules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		backend, berr := s.backend()
		if berr != nil {
			http.Error(w, berr.Error(), http.StatusInternalServerError)
			return
		}
		if backend != "nft" {
			ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
			defer cancel()
			s.mu.Lock()
			if len(s.db.Rules) == 0 {
				_ = s.importSystemRulesLocked(ctx, backend)
			}
			active, _ := s.backendActive(ctx, backend)
			resp := rulesResponse{Enabled: active, Rules: append([]FWRule{}, s.db.Rules...)}
			s.mu.Unlock()
			writeJSON(w, resp)
			return
		}
		s.mu.Lock()
		resp := rulesResponse{Enabled: s.db.Enabled, Rules: append([]FWRule{}, s.db.Rules...)}
		s.mu.Unlock()
		writeJSON(w, resp)
		return

	case http.MethodPost:
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if !s.cfg.Enabled {
		http.Error(w, "firewall is disabled by config", http.StatusForbidden)
		return
	}

	var req createRuleRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}
	rule, err := s.ruleFromCreate(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	backend, berr := s.backend()
	if berr != nil {
		http.Error(w, berr.Error(), http.StatusInternalServerError)
		return
	}

	s.mu.Lock()
	prev := s.db
	pos := req.Position
	if pos < 0 || pos > len(s.db.Rules) {
		pos = len(s.db.Rules)
	}
	s.db.Rules = append(s.db.Rules[:pos], append([]FWRule{rule}, s.db.Rules[pos:]...)...)
	s.db.Updated = time.Now().UTC()
	if err := s.saveLocked(); err != nil {
		s.db = prev
		s.mu.Unlock()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if backend == "nft" {
		err = s.applyLocked(ctx)
		if err != nil {
			s.db = prev
			_ = s.saveLocked()
			s.mu.Unlock()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	} else if rule.Enabled {
		if err := s.applyRuleSystem(ctx, backend, rule, true); err != nil {
			s.db = prev
			_ = s.saveLocked()
			s.mu.Unlock()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
	}
	s.mu.Unlock()
	writeJSON(w, rule)
}

type toggleRuleRequest struct {
	Enabled bool `json:"enabled"`
}

type updateRuleRequest struct {
	Type    string `json:"type"`
	Proto   string `json:"proto"`
	Ports   string `json:"ports"`
	ToPort  int    `json:"to_port"`
	Service string `json:"service,omitempty"`
	Comment string `json:"comment"`
}

func (s *FirewallService) HandleRuleID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/firewall/rules/")
	path = strings.TrimPrefix(path, "/")
	parts := strings.Split(path, "/")
	if len(parts) < 1 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	id := parts[0]
	action := ""
	if len(parts) >= 2 {
		action = parts[1]
	}

	if !s.cfg.Enabled {
		http.Error(w, "firewall is disabled by config", http.StatusForbidden)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()

	backend, berr := s.backend()
	if berr != nil {
		http.Error(w, berr.Error(), http.StatusInternalServerError)
		return
	}

	switch {
	case action == "toggle" && r.Method == http.MethodPost:
		var req toggleRuleRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		prev := s.db
		found := false
		for i := range s.db.Rules {
			if s.db.Rules[i].ID == id {
				s.db.Rules[i].Enabled = req.Enabled
				found = true
				break
			}
		}
		if !found {
			s.mu.Unlock()
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		s.db.Updated = time.Now().UTC()
		if err := s.saveLocked(); err != nil {
			s.db = prev
			s.mu.Unlock()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if backend == "nft" {
			err := s.applyLocked(ctx)
			if err != nil {
				s.db = prev
				_ = s.saveLocked()
				s.mu.Unlock()
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		} else if found {
			var rule FWRule
			for _, rr := range s.db.Rules {
				if rr.ID == id {
					rule = rr
					break
				}
			}
			if rule.ID != "" {
				if err := s.applyRuleSystem(ctx, backend, rule, req.Enabled); err != nil {
					s.db = prev
					_ = s.saveLocked()
					s.mu.Unlock()
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
			}
		}
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return

	case action == "" && r.Method == http.MethodDelete:
		s.mu.Lock()
		prev := s.db
		var out []FWRule
		found := false
		var removed FWRule
		for _, rr := range s.db.Rules {
			if rr.ID == id {
				found = true
				removed = rr
				continue
			}
			out = append(out, rr)
		}
		if !found {
			s.mu.Unlock()
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		s.db.Rules = out
		s.db.Updated = time.Now().UTC()
		if err := s.saveLocked(); err != nil {
			s.db = prev
			s.mu.Unlock()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if backend == "nft" {
			err := s.applyLocked(ctx)
			if err != nil {
				s.db = prev
				_ = s.saveLocked()
				s.mu.Unlock()
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		} else if found && removed.ID != "" && removed.Enabled {
			if err := s.applyRuleSystem(ctx, backend, removed, false); err != nil {
				s.db = prev
				_ = s.saveLocked()
				s.mu.Unlock()
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return

	case action == "" && r.Method == http.MethodPut:
		var req updateRuleRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		update, err := s.ruleFromUpdate(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if backend == "nft" && update.Service != "" {
			http.Error(w, "service rules are not supported with nft backend", http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		prev := s.db
		found := false
		var prevRule FWRule
		for i := range s.db.Rules {
			if s.db.Rules[i].ID == id {
				prevRule = s.db.Rules[i]
				update.ID = id
				update.Enabled = s.db.Rules[i].Enabled
				update.Created = s.db.Rules[i].Created
				s.db.Rules[i] = update
				found = true
				break
			}
		}
		if !found {
			s.mu.Unlock()
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		s.db.Updated = time.Now().UTC()
		if err := s.saveLocked(); err != nil {
			s.db = prev
			s.mu.Unlock()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if backend == "nft" {
			err = s.applyLocked(ctx)
			if err != nil {
				s.db = prev
				_ = s.saveLocked()
				s.mu.Unlock()
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		} else if found {
			if prevRule.ID != "" && prevRule.Enabled {
				if err := s.applyRuleSystem(ctx, backend, prevRule, false); err != nil {
					s.db = prev
					_ = s.saveLocked()
					s.mu.Unlock()
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
			}
			if update.Enabled {
				if err := s.applyRuleSystem(ctx, backend, update, true); err != nil {
					s.db = prev
					_ = s.saveLocked()
					s.mu.Unlock()
					http.Error(w, err.Error(), http.StatusInternalServerError)
					return
				}
			}
		}
		s.mu.Unlock()
		writeJSON(w, update)
		return
	}

	http.NotFound(w, r)
}

type portUsage struct {
	Proto   string `json:"proto"`
	Local   string `json:"local"`
	Process string `json:"process,omitempty"`
	PID     int    `json:"pid,omitempty"`
}

type portUsageResponse struct {
	Port  int         `json:"port"`
	Proto string      `json:"proto"`
	Items []portUsage `json:"items"`
	Error string      `json:"error,omitempty"`
}

// Example ss output:
// users:(("sshd",pid=123,fd=3))
var reUsers = regexp.MustCompile(`users:\(\("([^"]+)".*pid=([0-9]+)`)

func (s *FirewallService) HandlePortUsage(w http.ResponseWriter, r *http.Request) {
	port, _ := strconv.Atoi(strings.TrimSpace(r.URL.Query().Get("port")))
	if port <= 0 || port > 65535 {
		http.Error(w, "bad port", http.StatusBadRequest)
		return
	}
	proto := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("proto")))
	if proto == "" || proto == "tcp" || proto == "udp" || proto == "any" {
		proto = "any"
	} else {
		http.Error(w, "bad proto", http.StatusBadRequest)
		return
	}
	if s.ssPath == "" {
		writeJSON(w, portUsageResponse{Port: port, Proto: proto, Error: "ss not available"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
	defer cancel()
	args := []string{"-H", "-l", "-n", "-p"}
	if proto == "tcp" {
		args = append(args, "-t")
	} else if proto == "udp" {
		args = append(args, "-u")
	} else {
		args = append(args, "-t", "-u")
	}

	out, err := s.run(ctx, s.ssPath, args...)
	resp := portUsageResponse{Port: port, Proto: proto}
	if err != nil {
		resp.Error = err.Error()
		writeJSON(w, resp)
		return
	}
	lines := strings.Split(out, "\n")
	var items []portUsage
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		fields := strings.Fields(ln)
		if len(fields) < 6 {
			continue
		}
		netid := fields[0]
		local := fields[4]
		p := parsePort(local)
		if p != port {
			continue
		}
		it := portUsage{Proto: netid, Local: local}
		if m := reUsers.FindStringSubmatch(ln); len(m) == 3 {
			it.Process = m[1]
			pid, _ := strconv.Atoi(m[2])
			it.PID = pid
		}
		items = append(items, it)
	}
	resp.Items = items
	writeJSON(w, resp)
}

func parsePort(local string) int {
	local = strings.TrimSpace(local)
	if local == "" {
		return 0
	}
	if i := strings.LastIndex(local, "]:"); i >= 0 {
		p, _ := strconv.Atoi(local[i+2:])
		return p
	}
	i := strings.LastIndex(local, ":")
	if i < 0 {
		return 0
	}
	p, _ := strconv.Atoi(local[i+1:])
	return p
}

func (s *FirewallService) ruleFromCreate(req createRuleRequest) (FWRule, error) {
	id, err := randID(10)
	if err != nil {
		return FWRule{}, err
	}
	rule := FWRule{
		ID:      id,
		Enabled: req.Enabled,
		Type:    strings.ToLower(strings.TrimSpace(req.Type)),
		Proto:   strings.ToLower(strings.TrimSpace(req.Proto)),
		ToPort:  req.ToPort,
		Service: strings.TrimSpace(req.Service),
		Comment: strings.TrimSpace(req.Comment),
		Created: time.Now().UTC(),
	}
	if rule.Proto == "" {
		if rule.Service != "" {
			rule.Proto = "any"
		} else {
			rule.Proto = "tcp"
		}
	}
	if rule.Type == "" {
		rule.Type = "allow"
	}
	if rule.Service == "" {
		if err := parsePortsInto(&rule, req.Ports); err != nil {
			return FWRule{}, err
		}
	}
	if err := validateRule(rule); err != nil {
		return FWRule{}, err
	}
	return rule, nil
}

func (s *FirewallService) ruleFromUpdate(req updateRuleRequest) (FWRule, error) {
	rule := FWRule{
		Type:    strings.ToLower(strings.TrimSpace(req.Type)),
		Proto:   strings.ToLower(strings.TrimSpace(req.Proto)),
		ToPort:  req.ToPort,
		Service: strings.TrimSpace(req.Service),
		Comment: strings.TrimSpace(req.Comment),
	}
	if rule.Proto == "" {
		if rule.Service != "" {
			rule.Proto = "any"
		} else {
			rule.Proto = "tcp"
		}
	}
	if rule.Type == "" {
		rule.Type = "allow"
	}
	if rule.Service == "" {
		if err := parsePortsInto(&rule, req.Ports); err != nil {
			return FWRule{}, err
		}
	}
	if err := validateRule(rule); err != nil {
		return FWRule{}, err
	}
	return rule, nil
}

func parsePortsInto(rule *FWRule, ports string) error {
	ports = strings.TrimSpace(ports)
	if ports == "" {
		return errors.New("ports is required")
	}
	if strings.Contains(ports, "-") {
		parts := strings.SplitN(ports, "-", 2)
		a, _ := strconv.Atoi(strings.TrimSpace(parts[0]))
		b, _ := strconv.Atoi(strings.TrimSpace(parts[1]))
		if a <= 0 || b <= 0 {
			return errors.New("bad ports range")
		}
		if b < a {
			a, b = b, a
		}
		rule.PortFrom = a
		rule.PortTo = b
		return nil
	}
	p, _ := strconv.Atoi(ports)
	if p <= 0 {
		return errors.New("bad port")
	}
	rule.PortFrom = p
	rule.PortTo = p
	return nil
}

func validateRule(r FWRule) error {
	switch r.Type {
	case "allow", "deny", "redirect":
	default:
		return errors.New("type must be allow, deny or redirect")
	}
	if r.Service != "" {
		if r.Type == "redirect" {
			return errors.New("service does not support redirect")
		}
		return nil
	}
	if r.Proto != "tcp" && r.Proto != "udp" {
		return errors.New("proto must be tcp or udp")
	}
	if r.PortFrom <= 0 || r.PortFrom > 65535 || r.PortTo <= 0 || r.PortTo > 65535 {
		return errors.New("port must be 1..65535")
	}
	if r.PortTo < r.PortFrom {
		return errors.New("port_to must be >= port_from")
	}
	if r.Type == "redirect" {
		if r.PortFrom != r.PortTo {
			return errors.New("redirect supports single port only")
		}
		if r.ToPort <= 0 || r.ToPort > 65535 {
			return errors.New("to_port must be 1..65535")
		}
	}
	return nil
}

func (s *FirewallService) backend() (string, error) {
	if s.fwCmdPath != "" {
		return "firewalld", nil
	}
	if s.ufwPath != "" {
		return "ufw", nil
	}
	if s.nftPath != "" {
		return "nft", nil
	}
	return "", errors.New("no firewall tool available")
}

func backendToolName(backend string) string {
	switch backend {
	case "firewalld":
		return "firewall-cmd"
	case "ufw":
		return "ufw"
	case "nft":
		return "nft"
	default:
		return "unknown"
	}
}

func (s *FirewallService) backendActive(ctx context.Context, backend string) (bool, error) {
	switch backend {
	case "nft":
		return s.isActive(ctx)
	case "firewalld":
		active, _, err := s.firewalldStatus(ctx)
		return active, err
	case "ufw":
		active, _, err := s.ufwStatus(ctx)
		return active, err
	default:
		return false, errors.New("unknown firewall backend")
	}
}

func (s *FirewallService) setSystemFirewallEnabled(ctx context.Context, backend string, enabled bool) error {
	switch backend {
	case "firewalld":
		if s.systemctlPath == "" {
			return errors.New("systemctl not found")
		}
		action := "stop"
		if enabled {
			action = "start"
		}
		_, err := s.systemctl(ctx, action, "firewalld")
		return err
	case "ufw":
		if enabled {
			_, err := s.ufw(ctx, "--force", "enable")
			return err
		}
		_, err := s.ufw(ctx, "--force", "disable")
		return err
	default:
		return errors.New("unsupported firewall backend")
	}
}

func (s *FirewallService) applySystem(ctx context.Context, backend string) error {
	switch backend {
	case "firewalld":
		_, err := s.firewalld(ctx, "--reload")
		return err
	case "ufw":
		_, err := s.ufw(ctx, "reload")
		return err
	default:
		return errors.New("unsupported firewall backend")
	}
}

func (s *FirewallService) applyRuleSystem(ctx context.Context, backend string, rule FWRule, enable bool) error {
	switch backend {
	case "firewalld":
		return s.applyFirewalldRule(ctx, rule, enable)
	case "ufw":
		return s.applyUfwRule(ctx, rule, enable)
	default:
		return errors.New("unsupported firewall backend")
	}
}

func (s *FirewallService) importSystemRulesLocked(ctx context.Context, backend string) error {
	var (
		rules []FWRule
		err   error
	)
	switch backend {
	case "firewalld":
		rules, err = s.readFirewalldRules(ctx)
	case "ufw":
		rules, err = s.readUfwRules(ctx)
	default:
		return nil
	}
	if err != nil {
		return err
	}
	s.db.Rules = rules
	s.db.Updated = time.Now().UTC()
	return s.saveLocked()
}

func (s *FirewallService) isActive(ctx context.Context) (bool, error) {
	if s.nftPath == "" {
		return false, errors.New("nft is not available")
	}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	_, err := s.nft(ctx, "list", "table", "inet", "atlas")
	if err != nil {
		return false, nil
	}
	return true, nil
}

var (
	ufwSplitRe = regexp.MustCompile(`\s{2,}`)
	ufwIdxRe   = regexp.MustCompile(`^\[\s*\d+\]\s*`)
)

func (s *FirewallService) ufwStatus(ctx context.Context) (bool, []UFWRule, error) {
	if s.ufwPath == "" {
		return false, nil, errors.New("ufw not found")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := s.ufw(ctx, "status")
	if err != nil {
		return false, nil, err
	}
	active, rules := parseUFWStatus(out)
	return active, rules, nil
}

func parseUFWStatus(out string) (bool, []UFWRule) {
	var active bool
	var rules []UFWRule
	lines := strings.Split(out, "\n")
	inRules := false
	for _, line := range lines {
		l := strings.TrimSpace(line)
		if l == "" {
			continue
		}
		lo := strings.ToLower(l)
		if strings.HasPrefix(lo, "status:") {
			active = strings.Contains(lo, "active")
			continue
		}
		if strings.HasPrefix(l, "To ") && strings.Contains(l, "Action") && strings.Contains(l, "From") {
			inRules = true
			continue
		}
		if strings.HasPrefix(l, "--") {
			continue
		}
		if !inRules {
			continue
		}
		l = ufwIdxRe.ReplaceAllString(l, "")
		parts := ufwSplitRe.Split(l, -1)
		if len(parts) < 3 {
			continue
		}
		to := strings.TrimSpace(parts[0])
		action := strings.TrimSpace(parts[1])
		from := strings.TrimSpace(parts[2])
		v6 := strings.Contains(to, "(v6)") || strings.Contains(from, "(v6)")
		to = strings.TrimSpace(strings.ReplaceAll(to, "(v6)", ""))
		from = strings.TrimSpace(strings.ReplaceAll(from, "(v6)", ""))
		rules = append(rules, UFWRule{
			To:     to,
			Action: action,
			From:   from,
			V6:     v6,
			Raw:    l,
		})
		if len(rules) >= 300 {
			break
		}
	}
	return active, rules
}

func (s *FirewallService) firewalldStatus(ctx context.Context) (bool, []UFWRule, error) {
	if s.fwCmdPath == "" {
		return false, nil, errors.New("firewall-cmd not found")
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	stateOut, err := s.firewalld(ctx, "--state")
	if err != nil {
		return false, nil, err
	}
	active := strings.TrimSpace(stateOut) == "running"
	if !active {
		return false, nil, nil
	}

	zone := s.firewalldZone(ctx)
	if zone == "" {
		zone = "public"
	}
	zoneLabel := "zone:" + zone

	var rules []UFWRule
	if portsOut, err := s.firewalld(ctx, "--zone", zone, "--list-ports"); err == nil {
		for _, p := range strings.Fields(portsOut) {
			rules = append(rules, UFWRule{To: p, Action: "allow", From: zoneLabel})
		}
	}
	if svcOut, err := s.firewalld(ctx, "--zone", zone, "--list-services"); err == nil {
		for _, s := range strings.Fields(svcOut) {
			rules = append(rules, UFWRule{To: "service:" + s, Action: "allow", From: zoneLabel})
		}
	}
	if richOut, err := s.firewalld(ctx, "--zone", zone, "--list-rich-rules"); err == nil {
		for _, line := range strings.Split(richOut, "\n") {
			l := strings.TrimSpace(line)
			if l == "" {
				continue
			}
			action := "allow"
			lo := strings.ToLower(l)
			if strings.Contains(lo, "reject") {
				action = "reject"
			} else if strings.Contains(lo, "drop") {
				action = "drop"
			} else if strings.Contains(lo, "accept") {
				action = "allow"
			}
			rules = append(rules, UFWRule{To: "rich rule", Action: action, From: zoneLabel, Raw: l})
		}
	}

	return active, rules, nil
}

func (s *FirewallService) firewalldZone(ctx context.Context) string {
	out, err := s.firewalld(ctx, "--get-default-zone")
	if err == nil {
		zone := strings.TrimSpace(out)
		if zone != "" {
			return zone
		}
	}
	out, err = s.firewalld(ctx, "--get-active-zones")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) > 0 {
			return fields[0]
		}
	}
	return ""
}

func (s *FirewallService) applyLocked(ctx context.Context) error {
	if s.nftPath == "" {
		return errors.New("nft is not available")
	}
	if !s.db.Enabled {
		// Disable: remove our tables.
		_, _ = s.nft(ctx, "delete", "table", "inet", "atlas")
		_, _ = s.nft(ctx, "delete", "table", "ip", "atlas_nat")
		return nil
	}

	if err := s.ensureFilter(ctx); err != nil {
		return err
	}
	if err := s.ensureNAT(ctx); err != nil {
		return err
	}

	// Flush (our tables only).
	_, _ = s.nft(ctx, "flush", "chain", "inet", "atlas", "input")
	_, _ = s.nft(ctx, "flush", "chain", "ip", "atlas_nat", "prerouting")

	// Apply rules in order.
	for _, r := range s.db.Rules {
		if !r.Enabled {
			continue
		}
		if err := s.applyRule(ctx, r); err != nil {
			return fmt.Errorf("apply rule %s: %w", r.ID, err)
		}
	}
	return nil
}

func (s *FirewallService) ensureFilter(ctx context.Context) error {
	if _, err := s.nft(ctx, "list", "table", "inet", "atlas"); err != nil {
		if _, err := s.nft(ctx, "add", "table", "inet", "atlas"); err != nil {
			return err
		}
	}
	if _, err := s.nft(ctx, "list", "chain", "inet", "atlas", "input"); err != nil {
		args := []string{"add", "chain", "inet", "atlas", "input", "{", "type", "filter", "hook", "input", "priority", "0", ";", "policy", "accept", ";", "}"}
		if _, err := s.nft(ctx, args...); err != nil {
			return err
		}
	}
	return nil
}

func (s *FirewallService) ensureNAT(ctx context.Context) error {
	// NAT is in ip family for broad compatibility.
	if _, err := s.nft(ctx, "list", "table", "ip", "atlas_nat"); err != nil {
		if _, err := s.nft(ctx, "add", "table", "ip", "atlas_nat"); err != nil {
			return err
		}
	}
	if _, err := s.nft(ctx, "list", "chain", "ip", "atlas_nat", "prerouting"); err != nil {
		args := []string{"add", "chain", "ip", "atlas_nat", "prerouting", "{", "type", "nat", "hook", "prerouting", "priority", "-100", ";", "}"}
		if _, err := s.nft(ctx, args...); err != nil {
			return err
		}
	}
	return nil
}

func (s *FirewallService) applyRule(ctx context.Context, r FWRule) error {
	comment := nftString("atlas:" + r.ID)
	switch r.Type {
	case "allow":
		return s.addFilterRule(ctx, r, "accept", comment)
	case "deny":
		return s.addFilterRule(ctx, r, "drop", comment)
	case "redirect":
		return s.addRedirectRule(ctx, r, comment)
	default:
		return errors.New("unsupported rule type")
	}
}

func (s *FirewallService) addFilterRule(ctx context.Context, r FWRule, verdict string, comment string) error {
	dport := fmt.Sprintf("%d", r.PortFrom)
	if r.PortTo != r.PortFrom {
		dport = fmt.Sprintf("%d-%d", r.PortFrom, r.PortTo)
	}
	args := []string{"add", "rule", "inet", "atlas", "input", r.Proto, "dport", dport, verdict, "comment", comment}
	_, err := s.nft(ctx, args...)
	return err
}

func (s *FirewallService) addRedirectRule(ctx context.Context, r FWRule, comment string) error {
	args := []string{"add", "rule", "ip", "atlas_nat", "prerouting", r.Proto, "dport", fmt.Sprintf("%d", r.PortFrom), "redirect", "to", fmt.Sprintf(":%d", r.ToPort), "comment", comment}
	_, err := s.nft(ctx, args...)
	return err
}

func nftString(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\"", "\\\"")
	return `"` + s + `"`
}

func (s *FirewallService) nft(ctx context.Context, args ...string) (string, error) {
	if s.nftPath == "" {
		return "", errors.New("nft not found")
	}
	// If not root, try sudo -n.
	if os.Geteuid() != 0 && s.sudoPath != "" {
		if pass, ok, err := s.sudoPassFor(ctx); err != nil {
			return "", err
		} else if ok && pass != "" {
			return s.runSudoPassword(ctx, pass, s.nftPath, args...)
		}
		all := append([]string{"-n", "--", s.nftPath}, args...)
		return s.run(ctx, s.sudoPath, all...)
	}
	return s.run(ctx, s.nftPath, args...)
}

func (s *FirewallService) ufw(ctx context.Context, args ...string) (string, error) {
	if s.ufwPath == "" {
		return "", errors.New("ufw not found")
	}
	// If not root, try sudo -n.
	if os.Geteuid() != 0 && s.sudoPath != "" {
		if pass, ok, err := s.sudoPassFor(ctx); err != nil {
			return "", err
		} else if ok && pass != "" {
			return s.runSudoPassword(ctx, pass, s.ufwPath, args...)
		}
		all := append([]string{"-n", "--", s.ufwPath}, args...)
		return s.run(ctx, s.sudoPath, all...)
	}
	return s.run(ctx, s.ufwPath, args...)
}

func (s *FirewallService) firewalld(ctx context.Context, args ...string) (string, error) {
	if s.fwCmdPath == "" {
		return "", errors.New("firewall-cmd not found")
	}
	if os.Geteuid() != 0 && s.sudoPath != "" {
		if pass, ok, err := s.sudoPassFor(ctx); err != nil {
			return "", err
		} else if ok && pass != "" {
			return s.runSudoPassword(ctx, pass, s.fwCmdPath, args...)
		}
		all := append([]string{"-n", "--", s.fwCmdPath}, args...)
		return s.run(ctx, s.sudoPath, all...)
	}
	return s.run(ctx, s.fwCmdPath, args...)
}

func (s *FirewallService) systemctl(ctx context.Context, args ...string) (string, error) {
	if s.systemctlPath == "" {
		return "", errors.New("systemctl not found")
	}
	if os.Geteuid() != 0 && s.sudoPath != "" {
		if pass, ok, err := s.sudoPassFor(ctx); err != nil {
			return "", err
		} else if ok && pass != "" {
			return s.runSudoPassword(ctx, pass, s.systemctlPath, args...)
		}
		all := append([]string{"-n", "--", s.systemctlPath}, args...)
		return s.run(ctx, s.sudoPath, all...)
	}
	return s.run(ctx, s.systemctlPath, args...)
}

func (s *FirewallService) sudoPassFor(ctx context.Context) (string, bool, error) {
	if s.sudoPassword == nil {
		return "", false, nil
	}
	c, ok := auth.ClaimsFromContext(ctx)
	if !ok || strings.TrimSpace(c.User) == "" {
		return "", false, nil
	}
	return s.sudoPassword(c.User)
}

func (s *FirewallService) runSudoPassword(ctx context.Context, pass string, bin string, args ...string) (string, error) {
	pass = strings.TrimSpace(pass)
	if pass == "" {
		return "", errors.New("sudo password is empty")
	}
	all := append([]string{"-S", "-p", "", "--", bin}, args...)
	cmd := exec.CommandContext(ctx, s.sudoPath, all...)
	cmd.Stdin = bytes.NewBufferString(pass + "\n")
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return string(out), nil
}

func (s *FirewallService) applyFirewalldRule(ctx context.Context, rule FWRule, enable bool) error {
	zone := s.firewalldZone(ctx)
	if zone == "" {
		zone = "public"
	}
	op := "add"
	if !enable {
		op = "remove"
	}

	if rule.Type == "redirect" {
		if rule.Service != "" {
			return errors.New("redirect with service is not supported")
		}
		proto := strings.ToLower(strings.TrimSpace(rule.Proto))
		if proto == "" {
			proto = "tcp"
		}
		spec := fmt.Sprintf("port=%d:proto=%s:toport=%d", rule.PortFrom, proto, rule.ToPort)
		flag := fmt.Sprintf("--%s-forward-port=%s", op, spec)
		return s.firewalldChange(ctx, zone, flag)
	}

	if rule.Service != "" {
		switch rule.Type {
		case "allow":
			flag := fmt.Sprintf("--%s-service=%s", op, rule.Service)
			return s.firewalldChange(ctx, zone, flag)
		case "deny":
			rich := fmt.Sprintf("rule service name=\"%s\" drop", rule.Service)
			flag := fmt.Sprintf("--%s-rich-rule=%s", op, rich)
			return s.firewalldChange(ctx, zone, flag)
		default:
			return errors.New("unsupported rule type")
		}
	}

	portSpec := fmt.Sprintf("%d", rule.PortFrom)
	if rule.PortTo != rule.PortFrom {
		portSpec = fmt.Sprintf("%d-%d", rule.PortFrom, rule.PortTo)
	}
	proto := strings.ToLower(strings.TrimSpace(rule.Proto))
	if proto == "" {
		proto = "tcp"
	}
	switch rule.Type {
	case "allow":
		flag := fmt.Sprintf("--%s-port=%s/%s", op, portSpec, proto)
		return s.firewalldChange(ctx, zone, flag)
	case "deny":
		rich := fmt.Sprintf("rule port port=\"%s\" protocol=\"%s\" drop", portSpec, proto)
		flag := fmt.Sprintf("--%s-rich-rule=%s", op, rich)
		return s.firewalldChange(ctx, zone, flag)
	default:
		return errors.New("unsupported rule type")
	}
}

func (s *FirewallService) firewalldChange(ctx context.Context, zone string, opFlag string) error {
	args := []string{"--zone", zone, opFlag}
	if _, err := s.firewalld(ctx, args...); err != nil {
		return err
	}
	permanent := append([]string{"--permanent"}, args...)
	if _, err := s.firewalld(ctx, permanent...); err != nil {
		return err
	}
	return nil
}

func (s *FirewallService) applyUfwRule(ctx context.Context, rule FWRule, enable bool) error {
	if rule.Type == "redirect" {
		return errors.New("redirect is not supported by ufw")
	}
	if rule.Type != "allow" && rule.Type != "deny" {
		return errors.New("unsupported rule type")
	}
	target, err := ufwTarget(rule)
	if err != nil {
		return err
	}
	var args []string
	if enable {
		args = []string{rule.Type, target}
	} else {
		args = []string{"--force", "delete", rule.Type, target}
	}
	_, err = s.ufw(ctx, args...)
	return err
}

func ufwTarget(rule FWRule) (string, error) {
	if rule.Service != "" {
		return rule.Service, nil
	}
	if rule.PortFrom <= 0 {
		return "", errors.New("port is required")
	}
	target := fmt.Sprintf("%d", rule.PortFrom)
	if rule.PortTo != rule.PortFrom {
		target = fmt.Sprintf("%d:%d", rule.PortFrom, rule.PortTo)
	}
	proto := strings.ToLower(strings.TrimSpace(rule.Proto))
	if proto == "" || proto == "any" {
		return target, nil
	}
	if proto != "tcp" && proto != "udp" {
		return "", errors.New("proto must be tcp or udp")
	}
	return target + "/" + proto, nil
}

func (s *FirewallService) readFirewalldRules(ctx context.Context) ([]FWRule, error) {
	zone := s.firewalldZone(ctx)
	if zone == "" {
		zone = "public"
	}
	var out []FWRule
	seen := make(map[string]struct{})

	appendRule := func(r FWRule) {
		key := fmt.Sprintf("%s|%s|%d|%d|%d|%s", r.Type, r.Proto, r.PortFrom, r.PortTo, r.ToPort, r.Service)
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		id, err := randID(10)
		if err != nil {
			return
		}
		r.ID = id
		r.Enabled = true
		r.Created = time.Now().UTC()
		out = append(out, r)
	}

	if portsOut, err := s.firewalld(ctx, "--zone", zone, "--list-ports"); err == nil {
		for _, tok := range strings.Fields(portsOut) {
			from, to, proto, ok := parseFirewalldPort(tok)
			if !ok {
				continue
			}
			appendRule(FWRule{
				Type:     "allow",
				Proto:    proto,
				PortFrom: from,
				PortTo:   to,
			})
		}
	}
	if svcOut, err := s.firewalld(ctx, "--zone", zone, "--list-services"); err == nil {
		for _, tok := range strings.Fields(svcOut) {
			appendRule(FWRule{
				Type:    "allow",
				Proto:   "any",
				Service: tok,
			})
		}
	}
	if fwdOut, err := s.firewalld(ctx, "--zone", zone, "--list-forward-ports"); err == nil {
		for _, tok := range strings.Fields(fwdOut) {
			from, proto, toPort, ok := parseFirewalldForward(tok)
			if !ok {
				continue
			}
			appendRule(FWRule{
				Type:     "redirect",
				Proto:    proto,
				PortFrom: from,
				PortTo:   from,
				ToPort:   toPort,
			})
		}
	}
	if richOut, err := s.firewalld(ctx, "--zone", zone, "--list-rich-rules"); err == nil {
		for _, line := range strings.Split(richOut, "\n") {
			line = strings.TrimSpace(line)
			if line == "" {
				continue
			}
			lo := strings.ToLower(line)
			if strings.Contains(lo, "source ") || strings.Contains(lo, "destination ") || strings.Contains(lo, "forward-port") {
				continue
			}
			action := "allow"
			if strings.Contains(lo, " drop") || strings.HasSuffix(lo, "drop") {
				action = "deny"
			} else if strings.Contains(lo, " reject") || strings.HasSuffix(lo, "reject") {
				action = "deny"
			} else if strings.Contains(lo, " accept") || strings.HasSuffix(lo, "accept") {
				action = "allow"
			}
			if m := firewalldRichPortRe.FindStringSubmatch(line); len(m) == 3 {
				from, to, ok := parsePortRange(m[1])
				if !ok {
					continue
				}
				appendRule(FWRule{
					Type:     action,
					Proto:    strings.ToLower(m[2]),
					PortFrom: from,
					PortTo:   to,
				})
				continue
			}
			if m := firewalldRichServiceRe.FindStringSubmatch(line); len(m) == 2 {
				appendRule(FWRule{
					Type:    action,
					Proto:   "any",
					Service: m[1],
				})
			}
		}
	}
	return out, nil
}

func (s *FirewallService) readUfwRules(ctx context.Context) ([]FWRule, error) {
	active, rules, err := s.ufwStatus(ctx)
	if err != nil && !active {
		return nil, err
	}
	var out []FWRule
	for _, r := range rules {
		rule, ok := ufwRuleToFWRule(r)
		if !ok {
			continue
		}
		id, err := randID(10)
		if err != nil {
			continue
		}
		rule.ID = id
		rule.Enabled = true
		rule.Created = time.Now().UTC()
		out = append(out, rule)
	}
	return out, nil
}

func ufwRuleToFWRule(r UFWRule) (FWRule, bool) {
	action := strings.ToLower(strings.TrimSpace(r.Action))
	ruleType := ""
	if strings.Contains(action, "allow") {
		ruleType = "allow"
	} else if strings.Contains(action, "deny") || strings.Contains(action, "reject") {
		ruleType = "deny"
	}
	if ruleType == "" {
		return FWRule{}, false
	}

	to := strings.TrimSpace(r.To)
	if to == "" {
		return FWRule{}, false
	}
	loTo := strings.ToLower(to)
	if loTo == "anywhere" || loTo == "any" {
		return FWRule{}, false
	}

	if to[0] < '0' || to[0] > '9' {
		return FWRule{Type: ruleType, Proto: "any", Service: to}, true
	}

	proto := ""
	base := to
	if parts := strings.SplitN(to, "/", 2); len(parts) == 2 {
		base = parts[0]
		proto = parts[1]
	}

	from, toPort, ok := parseUfwRange(base)
	if !ok {
		return FWRule{}, false
	}
	if proto == "" {
		proto = "tcp"
	}
	return FWRule{
		Type:     ruleType,
		Proto:    strings.ToLower(proto),
		PortFrom: from,
		PortTo:   toPort,
	}, true
}

func parseUfwRange(base string) (int, int, bool) {
	if strings.Contains(base, ":") {
		parts := strings.SplitN(base, ":", 2)
		return parsePortRange(parts[0] + "-" + parts[1])
	}
	return parsePortRange(base)
}

func parsePortRange(s string) (int, int, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, 0, false
	}
	if strings.Contains(s, "-") {
		parts := strings.SplitN(s, "-", 2)
		a, errA := strconv.Atoi(strings.TrimSpace(parts[0]))
		b, errB := strconv.Atoi(strings.TrimSpace(parts[1]))
		if errA != nil || errB != nil || a <= 0 || b <= 0 {
			return 0, 0, false
		}
		if b < a {
			a, b = b, a
		}
		return a, b, true
	}
	p, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || p <= 0 {
		return 0, 0, false
	}
	return p, p, true
}

func parseFirewalldPort(spec string) (int, int, string, bool) {
	parts := strings.SplitN(strings.TrimSpace(spec), "/", 2)
	if len(parts) != 2 {
		return 0, 0, "", false
	}
	from, to, ok := parsePortRange(parts[0])
	if !ok {
		return 0, 0, "", false
	}
	proto := strings.ToLower(strings.TrimSpace(parts[1]))
	if proto == "" {
		proto = "tcp"
	}
	return from, to, proto, true
}

func parseFirewalldForward(spec string) (int, string, int, bool) {
	fields := strings.Split(spec, ":")
	var port, toPort int
	proto := ""
	for _, f := range fields {
		parts := strings.SplitN(strings.TrimSpace(f), "=", 2)
		if len(parts) != 2 {
			continue
		}
		switch parts[0] {
		case "port":
			port, _ = strconv.Atoi(parts[1])
		case "proto":
			proto = strings.ToLower(strings.TrimSpace(parts[1]))
		case "toport":
			toPort, _ = strconv.Atoi(parts[1])
		}
	}
	if port <= 0 || toPort <= 0 {
		return 0, "", 0, false
	}
	if proto == "" {
		proto = "tcp"
	}
	return port, proto, toPort, true
}

var (
	firewalldRichPortRe    = regexp.MustCompile(`port port="([^"]+)"\s+protocol="([^"]+)"`)
	firewalldRichServiceRe = regexp.MustCompile(`service name="([^"]+)"`)
)

func (s *FirewallService) run(ctx context.Context, bin string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return "", ctx.Err()
	}
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return string(out), nil
}

func (s *FirewallService) load() error {
	path := strings.TrimSpace(s.cfg.DBPath)
	if path == "" {
		return nil
	}
	path = filepath.Clean(path)
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var db fwDB
	if err := json.Unmarshal(b, &db); err != nil {
		return err
	}
	if db.Version == 0 {
		db.Version = 1
	}
	s.mu.Lock()
	s.db = db
	s.mu.Unlock()
	return nil
}

func (s *FirewallService) saveLocked() error {
	path := strings.TrimSpace(s.cfg.DBPath)
	if path == "" {
		return errors.New("firewall db path is not configured")
	}
	path = filepath.Clean(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	b, err := json.MarshalIndent(s.db, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func randID(nBytes int) (string, error) {
	b := make([]byte, nBytes)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
