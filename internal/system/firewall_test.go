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

func TestFirewallApplyWhenEnabledButNoNft(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "fw.db")

	s := NewFirewallService(FirewallConfig{Enabled: true, DBPath: dbPath})
	s.nftPath = "" // ensure apply fails safely

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

func writeScript(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return path
}
