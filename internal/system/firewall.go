package system

import (
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
)

type FirewallConfig struct {
	Enabled bool
	DBPath  string
}

type FirewallService struct {
	cfg FirewallConfig

	mu sync.Mutex
	db fwDB

	nftPath  string
	ssPath   string
	sudoPath string
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

	Comment string    `json:"comment,omitempty"`
	Created time.Time `json:"created_utc,omitempty"`
}

type fwStatus struct {
	ConfigEnabled bool   `json:"config_enabled"`
	DBEnabled     bool   `json:"db_enabled"`
	Tool          string `json:"tool"`
	Active        bool   `json:"active"`
	Error         string `json:"error,omitempty"`
	EUID          int    `json:"euid"`
	HasSudo       bool   `json:"has_sudo"`
	DBPath        string `json:"db_path,omitempty"`
}

func NewFirewallService(cfg FirewallConfig) *FirewallService {
	nft, _ := exec.LookPath("nft")
	ss, _ := exec.LookPath("ss")
	sudo, _ := exec.LookPath("sudo")
	s := &FirewallService{
		cfg:      cfg,
		nftPath:  nft,
		ssPath:   ss,
		sudoPath: sudo,
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

	tool := "none"
	if s.nftPath != "" {
		tool = "nft"
	}

	st := fwStatus{
		ConfigEnabled: s.cfg.Enabled,
		DBEnabled:     enabled,
		Tool:          tool,
		EUID:          os.Geteuid(),
		HasSudo:       s.sudoPath != "",
		DBPath:        dbPath,
	}
	if !s.cfg.Enabled {
		writeJSON(w, st)
		return
	}
	active, err := s.isActive(r.Context())
	st.Active = active
	if err != nil {
		st.Error = err.Error()
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
	Enabled bool     `json:"enabled"`
	Rules   []FWRule `json:"rules"`
}

type createRuleRequest struct {
	Enabled  bool   `json:"enabled"`
	Type     string `json:"type"`
	Proto    string `json:"proto"`
	Ports    string `json:"ports"`    // "80" or "1000-2000"
	ToPort   int    `json:"to_port"`  // redirect
	Comment  string `json:"comment"`  // optional
	Position int    `json:"position"` // optional insert at index; -1 append
}

func (s *FirewallService) HandleRules(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
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
	err = s.applyLocked(ctx)
	if err != nil {
		s.db = prev
		_ = s.saveLocked()
		s.mu.Unlock()
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
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
		err := s.applyLocked(ctx)
		if err != nil {
			s.db = prev
			_ = s.saveLocked()
			s.mu.Unlock()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		s.mu.Unlock()
		w.WriteHeader(http.StatusNoContent)
		return

	case action == "" && r.Method == http.MethodDelete:
		s.mu.Lock()
		prev := s.db
		var out []FWRule
		found := false
		for _, rr := range s.db.Rules {
			if rr.ID == id {
				found = true
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
		err := s.applyLocked(ctx)
		if err != nil {
			s.db = prev
			_ = s.saveLocked()
			s.mu.Unlock()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
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
		s.mu.Lock()
		prev := s.db
		found := false
		for i := range s.db.Rules {
			if s.db.Rules[i].ID == id {
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
		err = s.applyLocked(ctx)
		if err != nil {
			s.db = prev
			_ = s.saveLocked()
			s.mu.Unlock()
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
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
	if proto == "" {
		proto = "tcp"
	}
	if proto != "tcp" && proto != "udp" && proto != "any" {
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
		Comment: strings.TrimSpace(req.Comment),
		Created: time.Now().UTC(),
	}
	if rule.Proto == "" {
		rule.Proto = "tcp"
	}
	if rule.Type == "" {
		rule.Type = "allow"
	}
	if err := parsePortsInto(&rule, req.Ports); err != nil {
		return FWRule{}, err
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
		Comment: strings.TrimSpace(req.Comment),
	}
	if rule.Proto == "" {
		rule.Proto = "tcp"
	}
	if rule.Type == "" {
		rule.Type = "allow"
	}
	if err := parsePortsInto(&rule, req.Ports); err != nil {
		return FWRule{}, err
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
	if r.Proto != "tcp" && r.Proto != "udp" {
		return errors.New("proto must be tcp or udp")
	}
	switch r.Type {
	case "allow", "deny", "redirect":
	default:
		return errors.New("type must be allow, deny or redirect")
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
		all := append([]string{"-n", "--", s.nftPath}, args...)
		return s.run(ctx, s.sudoPath, all...)
	}
	return s.run(ctx, s.nftPath, args...)
}

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
