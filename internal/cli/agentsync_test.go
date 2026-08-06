package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mvanhorn/agentcookie/internal/config"
)

func TestEnforceAgentSyncPolicy(t *testing.T) {
	tests := []struct {
		name      string
		required  string
		blocklist *config.Blocklist
		wantErr   string
	}{
		{
			name:      "default behavior remains sync all",
			blocklist: &config.Blocklist{Version: 1},
		},
		{
			name:      "missing policy fails closed when allowlist required",
			required:  "allowlist",
			blocklist: &config.Blocklist{Version: 1},
			wantErr:   "required cookie policy \"allowlist\" is not active",
		},
		{
			name:      "explicit allowlist passes",
			required:  "allowlist",
			blocklist: &config.Blocklist{Version: 1, Policy: config.CookiePolicyAllowlist},
		},
		{
			name:      "unsupported invariant is rejected",
			required:  "blocklist",
			blocklist: &config.Blocklist{Version: 1, Policy: config.CookiePolicyBlocklist},
			wantErr:   "unsupported --require-policy value",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := enforceAgentSyncPolicy(test.blocklist, test.required)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("enforceAgentSyncPolicy: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("enforceAgentSyncPolicy error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestLoadRequiredAgentSyncPolicyRechecksEveryCycle(t *testing.T) {
	dir := t.TempDir()
	withConfigDir(t, dir)

	if _, err := loadRequiredAgentSyncPolicy("allowlist"); err == nil {
		t.Fatal("missing blocklist.yaml should fail when allowlist is required")
	}

	writeCLIFile(t, filepath.Join(dir, "blocklist.yaml"), `
version: 1
policy: allowlist
domains:
  - pattern: "example.com"
`)
	if _, err := loadRequiredAgentSyncPolicy("allowlist"); err != nil {
		t.Fatalf("active allowlist should pass: %v", err)
	}

	writeCLIFile(t, filepath.Join(dir, "blocklist.yaml"), `
version: 1
policy: blocklist
domains: []
`)
	if _, err := loadRequiredAgentSyncPolicy("allowlist"); err == nil {
		t.Fatal("policy downgrade between cycles should fail closed")
	}
}

func TestWriteAgentSyncCapabilities(t *testing.T) {
	oldVersion := Version
	Version = "1.2.3-test"
	t.Cleanup(func() { Version = oldVersion })

	cfg := &config.SourceConfig{Browser: config.BrowserRef{Name: "brave"}}
	blocklist := &config.Blocklist{Version: 1, Policy: config.CookiePolicyAllowlist}
	var output bytes.Buffer
	if err := writeAgentSyncCapabilities(&output, agentSyncCmd, cfg, blocklist); err != nil {
		t.Fatalf("writeAgentSyncCapabilities: %v", err)
	}

	var got agentSyncCapabilities
	if err := json.Unmarshal(output.Bytes(), &got); err != nil {
		t.Fatalf("decode capabilities: %v\n%s", err, output.String())
	}
	if got.SchemaVersion != 1 {
		t.Errorf("schema_version = %d, want 1", got.SchemaVersion)
	}
	if got.EffectiveBrowserDefault != "brave" {
		t.Errorf("effective_browser_default = %q, want brave", got.EffectiveBrowserDefault)
	}
	if got.PolicyMode != "allowlist" {
		t.Errorf("policy_mode = %q, want allowlist", got.PolicyMode)
	}
	if got.BuildVersion != "1.2.3-test" {
		t.Errorf("build_version = %q, want 1.2.3-test", got.BuildVersion)
	}
	for _, want := range []string{"--browser", "--capabilities-json", "--require-policy"} {
		if !containsString(got.SupportedFlags, want) {
			t.Errorf("supported_flags missing %q: %v", want, got.SupportedFlags)
		}
	}
	if got.SigningSummary.CanonicalIdentityEnv != "AGENTCOOKIE_SIGN_IDENTITY" {
		t.Errorf("canonical signing env = %q", got.SigningSummary.CanonicalIdentityEnv)
	}
	if got.SigningSummary.ExternalWrapperMapping.From != "DEFAULT_SIGN_IDENTITY" || got.SigningSummary.ExternalWrapperMapping.To != "AGENTCOOKIE_SIGN_IDENTITY" {
		t.Errorf("wrapper mapping = %+v", got.SigningSummary.ExternalWrapperMapping)
	}
	if strings.Contains(output.String(), os.Getenv("HOME")) {
		t.Errorf("capability output should not expose machine-local paths: %s", output.String())
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
