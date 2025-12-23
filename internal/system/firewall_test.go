package system

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestFirewallDisabledByConfig(t *testing.T) {
	t.Parallel()

	s := NewFirewallService(FirewallConfig{Enabled: false, DBPath: "/tmp/fw.db"})
	req := httptest.NewRequest(http.MethodPost, "http://example/api/firewall/enabled", bytes.NewReader([]byte(`{"enabled":true}`)))
	rr := httptest.NewRecorder()
	s.HandleEnabled(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rr.Code)
	}

	req = httptest.NewRequest(http.MethodPost, "http://example/api/firewall/apply", nil)
	rr = httptest.NewRecorder()
	s.HandleApply(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d", rr.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "http://example/api/firewall/status", nil)
	rr = httptest.NewRecorder()
	s.HandleStatus(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rr.Code)
	}
	var st fwStatus
	if err := json.Unmarshal(rr.Body.Bytes(), &st); err != nil {
		t.Fatalf("json: %v", err)
	}
	if st.ConfigEnabled {
		t.Fatalf("expected config disabled")
	}
}

func TestFirewallRuleValidation(t *testing.T) {
	t.Parallel()

	var r FWRule
	if err := parsePortsInto(&r, ""); err == nil {
		t.Fatalf("expected ports required error")
	}
	if err := parsePortsInto(&r, "0"); err == nil {
		t.Fatalf("expected bad port")
	}
	if err := parsePortsInto(&r, "10-5"); err != nil {
		t.Fatalf("expected range ok (swap), got %v", err)
	}
	if r.PortFrom != 5 || r.PortTo != 10 {
		t.Fatalf("range parsed wrong: %#v", r)
	}

	okRule := FWRule{Type: "allow", Proto: "tcp", PortFrom: 22, PortTo: 22}
	if err := validateRule(okRule); err != nil {
		t.Fatalf("expected ok rule, got %v", err)
	}
	badProto := okRule
	badProto.Proto = "icmp"
	if err := validateRule(badProto); err == nil {
		t.Fatalf("expected bad proto")
	}
	badRedirect := FWRule{Type: "redirect", Proto: "tcp", PortFrom: 80, PortTo: 81, ToPort: 8080}
	if err := validateRule(badRedirect); err == nil {
		t.Fatalf("expected redirect single port only")
	}
}

func TestFirewallIsActiveNoNft(t *testing.T) {
	t.Parallel()

	s := NewFirewallService(FirewallConfig{Enabled: true})
	s.nftPath = "" // force no nft
	_, err := s.isActive(context.Background())
	if err == nil {
		t.Fatalf("expected error when nft missing")
	}
}

func TestFirewallRulesHandlers(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "fw.db")

	s := NewFirewallService(FirewallConfig{Enabled: false, DBPath: dbPath})

	// GET rules always works
	req := httptest.NewRequest(http.MethodGet, "http://example/api/firewall/rules", nil)
	rr := httptest.NewRecorder()
	s.HandleRules(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("rules get status=%d body=%q", rr.Code, rr.Body.String())
	}

	// POST when config disabled is forbidden
	req = httptest.NewRequest(http.MethodPost, "http://example/api/firewall/rules", bytes.NewReader([]byte(`{"enabled":true,"type":"allow","proto":"tcp","ports":"22","position":-1}`)))
	rr = httptest.NewRecorder()
	s.HandleRules(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("rules post status=%d body=%q", rr.Code, rr.Body.String())
	}
}

func TestFirewallApplyNoBackend(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "fw.db")

	s := NewFirewallService(FirewallConfig{Enabled: true, DBPath: dbPath})
	s.nftPath = ""
	s.ufwPath = ""
	s.fwCmdPath = ""

	req := httptest.NewRequest(http.MethodPost, "http://example/api/firewall/apply", nil)
	rr := httptest.NewRecorder()
	s.HandleApply(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d body=%q", rr.Code, rr.Body.String())
	}
}

func TestFirewallApplyWithFakeNft(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("needs shell script")
	}

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "fw.db")
	logPath := filepath.Join(dir, "nft.log")
	nftPath := writeScript(t, dir, "nft.sh", `#!/bin/sh
echo "$@" >> "`+logPath+`"
# Fail on list commands to force create paths.
if [ "$1" = "list" ]; then exit 1; fi
exit 0
`)

	s := NewFirewallService(FirewallConfig{Enabled: true, DBPath: dbPath})
	s.nftPath = nftPath
	s.sudoPath = "" // don't try sudo

	s.mu.Lock()
	s.db.Enabled = true
	s.db.Rules = []FWRule{
		{ID: "a", Enabled: true, Type: "allow", Proto: "tcp", PortFrom: 22, PortTo: 22},
		{ID: "b", Enabled: true, Type: "redirect", Proto: "tcp", PortFrom: 80, PortTo: 80, ToPort: 8080},
	}
	err := s.applyLocked(context.Background())
	s.mu.Unlock()
	if err != nil {
		t.Fatalf("applyLocked: %v", err)
	}

	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if len(bytes.TrimSpace(b)) == 0 {
		t.Fatalf("expected nft to be invoked")
	}
}

func TestParseUFWStatus(t *testing.T) {
	t.Parallel()

	out := `Status: active
To                         Action      From
--                         ------      ----
22/tcp                     ALLOW       Anywhere
1000:1002/udp              DENY        1.2.3.4
OpenSSH                    ALLOW       Anywhere (v6)
`
	active, rules := parseUFWStatus(out)
	if !active {
		t.Fatalf("expected active")
	}
	if len(rules) != 3 {
		t.Fatalf("expected 3 rules, got %d", len(rules))
	}
	if !rules[2].V6 {
		t.Fatalf("expected v6 rule")
	}
}

func TestUfwRuleToFWRule(t *testing.T) {
	t.Parallel()

	rule, ok := ufwRuleToFWRule(UFWRule{To: "22/tcp", Action: "ALLOW"})
	if !ok || rule.Type != "allow" || rule.Proto != "tcp" || rule.PortFrom != 22 || rule.PortTo != 22 {
		t.Fatalf("unexpected rule: ok=%v rule=%#v", ok, rule)
	}
	rule, ok = ufwRuleToFWRule(UFWRule{To: "1000:1002/udp", Action: "DENY"})
	if !ok || rule.Type != "deny" || rule.Proto != "udp" || rule.PortFrom != 1000 || rule.PortTo != 1002 {
		t.Fatalf("unexpected range rule: ok=%v rule=%#v", ok, rule)
	}
	rule, ok = ufwRuleToFWRule(UFWRule{To: "OpenSSH", Action: "ALLOW"})
	if !ok || rule.Service != "OpenSSH" || rule.Type != "allow" {
		t.Fatalf("unexpected service rule: ok=%v rule=%#v", ok, rule)
	}
	if _, ok := ufwRuleToFWRule(UFWRule{To: "Anywhere", Action: "ALLOW"}); ok {
		t.Fatalf("expected Anywhere to be ignored")
	}
}

func TestParseFirewalldSpecs(t *testing.T) {
	t.Parallel()

	from, to, proto, ok := parseFirewalldPort("1000-1002/udp")
	if !ok || from != 1000 || to != 1002 || proto != "udp" {
		t.Fatalf("unexpected port parse: ok=%v %d-%d %s", ok, from, to, proto)
	}
	port, proto, toPort, ok := parseFirewalldForward("port=80:proto=tcp:toport=8080:toaddr=1.2.3.4")
	if !ok || port != 80 || proto != "tcp" || toPort != 8080 {
		t.Fatalf("unexpected forward parse: ok=%v port=%d proto=%s to=%d", ok, port, proto, toPort)
	}
}

func TestRuleFromCreateService(t *testing.T) {
	t.Parallel()

	s := NewFirewallService(FirewallConfig{Enabled: true})
	rule, err := s.ruleFromCreate(createRuleRequest{Enabled: true, Type: "allow", Service: "ssh"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if rule.Service != "ssh" || rule.Proto != "any" {
		t.Fatalf("unexpected service rule: %#v", rule)
	}
	_, err = s.ruleFromCreate(createRuleRequest{Enabled: true, Type: "redirect", Service: "ssh"})
	if err == nil {
		t.Fatalf("expected error for redirect service")
	}
}

func TestApplyUfwRuleArgs(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("needs shell script")
	}

	dir := t.TempDir()
	logPath := filepath.Join(dir, "ufw.log")
	ufwPath := writeScript(t, dir, "ufw.sh", `#!/bin/sh
echo "$@" >> "`+logPath+`"
exit 0
`)
	s := NewFirewallService(FirewallConfig{Enabled: true})
	s.ufwPath = ufwPath
	s.sudoPath = ""

	rule := FWRule{Type: "allow", Proto: "tcp", PortFrom: 22, PortTo: 22}
	if err := s.applyUfwRule(context.Background(), rule, true); err != nil {
		t.Fatalf("applyUfwRule enable: %v", err)
	}
	if err := s.applyUfwRule(context.Background(), rule, false); err != nil {
		t.Fatalf("applyUfwRule disable: %v", err)
	}

	b, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	log := string(b)
	if !bytes.Contains(b, []byte("allow 22/tcp")) {
		t.Fatalf("expected allow line, got %q", log)
	}
	if !bytes.Contains(b, []byte("--force delete allow 22/tcp")) {
		t.Fatalf("expected delete line, got %q", log)
	}
}

func TestReadFirewalldRules(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("needs shell script")
	}

	dir := t.TempDir()
	fwPath := writeScript(t, dir, "firewall-cmd.sh", `#!/bin/sh
case "$*" in
  *"--get-default-zone"*) echo "public";;
  *"--get-active-zones"*) echo "public";;
  *"--list-ports"*) echo "22/tcp 1000-1002/udp";;
  *"--list-services"*) echo "ssh http";;
  *"--list-forward-ports"*) echo "port=80:proto=tcp:toport=8080";;
  *"--list-rich-rules"*) echo "rule port port=\"53\" protocol=\"udp\" drop";;
  *) ;;
esac
exit 0
`)
	s := NewFirewallService(FirewallConfig{Enabled: true})
	s.fwCmdPath = fwPath
	s.sudoPath = ""

	rules, err := s.readFirewalldRules(context.Background())
	if err != nil {
		t.Fatalf("readFirewalldRules: %v", err)
	}
	if !hasRule(rules, FWRule{Type: "allow", Proto: "tcp", PortFrom: 22, PortTo: 22}) {
		t.Fatalf("missing allow 22/tcp rule: %#v", rules)
	}
	if !hasRule(rules, FWRule{Type: "allow", Proto: "udp", PortFrom: 1000, PortTo: 1002}) {
		t.Fatalf("missing allow 1000-1002/udp rule: %#v", rules)
	}
	if !hasRule(rules, FWRule{Type: "allow", Proto: "any", Service: "ssh"}) {
		t.Fatalf("missing service ssh rule: %#v", rules)
	}
	if !hasRule(rules, FWRule{Type: "redirect", Proto: "tcp", PortFrom: 80, PortTo: 80, ToPort: 8080}) {
		t.Fatalf("missing redirect rule: %#v", rules)
	}
	if !hasRule(rules, FWRule{Type: "deny", Proto: "udp", PortFrom: 53, PortTo: 53}) {
		t.Fatalf("missing deny rule: %#v", rules)
	}
}

func TestPortUsageWithFakeSS(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("needs shell script")
	}

	dir := t.TempDir()
	ssPath := writeScript(t, dir, "ss.sh", `#!/bin/sh
cat <<'OUT'
tcp   LISTEN 0 4096 127.0.0.1:12345 0.0.0.0:* users:(("sshd",pid=99,fd=3))
udp   UNCONN 0 0    127.0.0.1:55555 0.0.0.0:* users:(("dns",pid=10,fd=1))
OUT
`)
	s := NewFirewallService(FirewallConfig{Enabled: true})
	s.ssPath = ssPath

	req := httptest.NewRequest(http.MethodGet, "http://example/api/ports/usage?port=12345&proto=tcp", nil)
	rr := httptest.NewRecorder()
	s.HandlePortUsage(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status=%d body=%q", rr.Code, rr.Body.String())
	}
	var resp portUsageResponse
	_ = json.Unmarshal(rr.Body.Bytes(), &resp)
	if resp.Error != "" || len(resp.Items) != 1 || resp.Items[0].PID != 99 || resp.Items[0].Process != "sshd" {
		t.Fatalf("resp=%#v", resp)
	}
}

func hasRule(rules []FWRule, want FWRule) bool {
	for _, r := range rules {
		if r.Type != want.Type {
			continue
		}
		if strings.ToLower(r.Proto) != strings.ToLower(want.Proto) {
			continue
		}
		if r.PortFrom != want.PortFrom || r.PortTo != want.PortTo || r.ToPort != want.ToPort {
			continue
		}
		if strings.TrimSpace(r.Service) != strings.TrimSpace(want.Service) {
			continue
		}
		return true
	}
	return false
}

func writeScript(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}
