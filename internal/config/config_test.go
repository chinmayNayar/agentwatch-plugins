package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sandbox(t *testing.T) {
	t.Helper()
	h := t.TempDir()
	t.Setenv("AGENTWATCH_HOME", filepath.Join(h, ".agentwatch"))
	t.Setenv("AGENTWATCH_USER_HOME", h)
	t.Setenv("AGENTWATCH_NO_KEYCHAIN", "1")
	for _, k := range []string{"AGENTWATCH_API_URL", "AGENTWATCH_MODE", "AGENTWATCH_FAIL_MODE", "AGENTWATCH_API_KEY",
		"AGENTWATCH_TOKEN", "CLAUDE_PLUGIN_OPTION_MODE", "CLAUDE_PLUGIN_OPTION_API_KEY", "CLAUDE_PLUGIN_OPTION_API_URL"} {
		t.Setenv(k, "")
	}
}

func TestDefaultsAreDryRunFailOpen(t *testing.T) {
	sandbox(t)
	c := Load()
	if c.Mode != ModeDryRun || c.FailMode != FailOpen || c.APIURL != DefaultAPIURL || c.PreflightTimeoutMS != 800 {
		t.Fatalf("%+v", c)
	}
	if !c.SelfProtectOn() {
		t.Fatal("self-protection must default on")
	}
}

func TestPrecedencePluginOptionOverEnvOverFile(t *testing.T) {
	sandbox(t)
	c := &Config{Mode: ModeEnforce, APIURL: "https://file.example"}
	if err := c.Save(); err != nil {
		t.Fatal(err)
	}
	if got := Load(); got.Mode != ModeEnforce || got.APIURL != "https://file.example" {
		t.Fatalf("file: %+v", got)
	}
	t.Setenv("AGENTWATCH_MODE", "dry_run")
	if got := Load(); got.Mode != ModeDryRun {
		t.Fatal("env must override the file")
	}
	t.Setenv("CLAUDE_PLUGIN_OPTION_MODE", "enforce")
	if got := Load(); got.Mode != ModeEnforce || got.Sources["mode"] != "CLAUDE_PLUGIN_OPTION_MODE" {
		t.Fatalf("plugin option must win: %+v", got.Sources)
	}
}

func TestUnknownModeIsDryRun(t *testing.T) {
	sandbox(t)
	t.Setenv("AGENTWATCH_MODE", "ENFORCE!!")
	if Load().Mode != ModeDryRun {
		t.Fatal("garbage mode must not enforce")
	}
}

func TestConfigFileIs0600(t *testing.T) {
	sandbox(t)
	_ = (&Config{Mode: ModeDryRun}).Save()
	info, err := os.Stat(Path())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o077 != 0 && os.PathSeparator == '/' {
		t.Fatalf("config readable by others: %v", info.Mode())
	}
}

func TestCredentialFallbackFileIs0600AndNeverInErrors(t *testing.T) {
	sandbox(t)
	tok := "awdv_" + strings.Repeat("a", 48)
	where, err := SaveCredential(&Credential{Token: tok, Kind: "device"})
	if err != nil || where != "file" {
		t.Fatalf("%s %v", where, err)
	}
	info, _ := os.Stat(CredsPath())
	if os.PathSeparator == '/' && info.Mode().Perm() != 0o600 {
		t.Fatalf("creds mode %v", info.Mode())
	}
	c, err := LoadCredential()
	if err != nil || c.Token != tok || c.Source != "file" {
		t.Fatalf("%+v %v", c, err)
	}
	_ = os.WriteFile(CredsPath(), []byte(tok+"{corrupt"), 0o600)
	if _, err := LoadCredential(); err == nil || strings.Contains(err.Error(), tok) {
		t.Fatalf("corrupt creds error leaks or passes: %v", err)
	}
	DeleteCredential()
	if _, err := LoadCredential(); err != ErrNoCredential {
		t.Fatalf("delete: %v", err)
	}
}

func TestPluginAPIKeyOptionIsACredential(t *testing.T) {
	sandbox(t)
	t.Setenv("CLAUDE_PLUGIN_OPTION_API_KEY", "aw_x")
	c, err := LoadCredential()
	if err != nil || c.Source != "plugin option" || c.Kind != "api_key" {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestAPIURLMustBeHTTPSExceptLoopback(t *testing.T) {
	for u, ok := range map[string]bool{
		"https://api.agentwatch.in": true, "http://localhost:8000": true, "http://127.0.0.1:8000": true,
		"http://api.agentwatch.in": false, "ftp://x": false, "not a url": false,
	} {
		if (ValidAPIURL(u) == nil) != ok {
			t.Errorf("%s: want ok=%v", u, ok)
		}
	}
}

func TestAgentNameAndOverride(t *testing.T) {
	sandbox(t)
	t.Setenv("USER", "Ada.Lovelace!")
	c := Load()
	if got := c.AgentName("cursor"); got != "cursor@ada.lovelace" {
		t.Fatal(got)
	}
	c.AgentNames = map[string]string{"cursor": "cursor"}
	if c.AgentName("cursor") != "cursor" {
		t.Fatal("override ignored")
	}
}

func TestEnvOverridesForAgentNameAndCapture(t *testing.T) {
	sandbox(t)
	t.Setenv("AGENTWATCH_AGENT_NAME", "cursor")
	t.Setenv("AGENTWATCH_CONTENT_CAPTURE", "1")
	t.Setenv("AGENTWATCH_APPROVER", "owner")
	c := Load()
	if c.AgentName("cursor") != "cursor" || !c.ContentCapture || c.Approver != "owner" {
		t.Fatalf("%+v", c)
	}
}
