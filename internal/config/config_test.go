package config

import (
	"strings"
	"testing"
	"time"
)

// Full valid config with all fields explicitly set.
const validYAML = `
thresholds:
  tokenAgeWarningDays: 60
  tokenAgeCriticalDays: 90
  runnerStalenessMinutes: 10
  pipelineFailureWindow: "24h"

alerting:
  enableEmail: false
  smtpHost: "smtp.example.com"
  smtpPort: 587
  senderAddress: "ci-monitor@example.com"
  recipientAddresses:
    - "ops@example.com"

gitlabBaseURL: "https://gitlab.example.com"

apps:
  - name: pfm
    gitlabProjectID: 123
  - name: crm
    gitlabProjectID: 456
`

func TestParse_ValidConfig(t *testing.T) {
	cfg, err := Parse([]byte(validYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Thresholds
	if cfg.Thresholds.TokenAgeWarningDays != 60 {
		t.Errorf("TokenAgeWarningDays = %d, want 60", cfg.Thresholds.TokenAgeWarningDays)
	}
	if cfg.Thresholds.TokenAgeCriticalDays != 90 {
		t.Errorf("TokenAgeCriticalDays = %d, want 90", cfg.Thresholds.TokenAgeCriticalDays)
	}
	if cfg.Thresholds.RunnerStalenessMin != 10 {
		t.Errorf("RunnerStalenessMin = %d, want 10", cfg.Thresholds.RunnerStalenessMin)
	}
	if cfg.Thresholds.PipelineFailureWindow.Duration != 24*time.Hour {
		t.Errorf("PipelineFailureWindow = %v, want 24h", cfg.Thresholds.PipelineFailureWindow.Duration)
	}

	// Alerting
	if cfg.Alerting.EnableEmail != false {
		t.Errorf("EnableEmail = %v, want false", cfg.Alerting.EnableEmail)
	}
	if cfg.Alerting.SMTPPort != 587 {
		t.Errorf("SMTPPort = %d, want 587", cfg.Alerting.SMTPPort)
	}

	// GitLab
	if cfg.GitLabBaseURL != "https://gitlab.example.com" {
		t.Errorf("GitLabBaseURL = %q, want https://gitlab.example.com", cfg.GitLabBaseURL)
	}

	// Apps
	if len(cfg.Apps) != 2 {
		t.Fatalf("len(Apps) = %d, want 2", len(cfg.Apps))
	}
	if cfg.Apps[0].Name != "pfm" || cfg.Apps[0].GitLabProjectID != 123 {
		t.Errorf("Apps[0] = %+v, want {pfm, 123}", cfg.Apps[0])
	}
	if cfg.Apps[1].Name != "crm" || cfg.Apps[1].GitLabProjectID != 456 {
		t.Errorf("Apps[1] = %+v, want {crm, 456}", cfg.Apps[1])
	}
}

func TestParse_Defaults(t *testing.T) {
	// Minimal config — only required fields, everything else should get defaults.
	minimal := `
gitlabBaseURL: "https://gitlab.example.com"
apps:
  - name: pfm
    gitlabProjectID: 100
`
	cfg, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Thresholds.TokenAgeWarningDays != 60 {
		t.Errorf("default TokenAgeWarningDays = %d, want 60", cfg.Thresholds.TokenAgeWarningDays)
	}
	if cfg.Thresholds.TokenAgeCriticalDays != 90 {
		t.Errorf("default TokenAgeCriticalDays = %d, want 90", cfg.Thresholds.TokenAgeCriticalDays)
	}
	if cfg.Thresholds.RunnerStalenessMin != 10 {
		t.Errorf("default RunnerStalenessMin = %d, want 10", cfg.Thresholds.RunnerStalenessMin)
	}
	if cfg.Thresholds.PipelineFailureWindow.Duration != 24*time.Hour {
		t.Errorf("default PipelineFailureWindow = %v, want 24h", cfg.Thresholds.PipelineFailureWindow.Duration)
	}
	if cfg.Alerting.SMTPPort != 587 {
		t.Errorf("default SMTPPort = %d, want 587", cfg.Alerting.SMTPPort)
	}
	if cfg.Alerting.EnableEmail != false {
		t.Errorf("default EnableEmail = %v, want false", cfg.Alerting.EnableEmail)
	}
}

func TestParse_MissingGitLabBaseURL(t *testing.T) {
	input := `
apps:
  - name: pfm
    gitlabProjectID: 100
`
	_, err := Parse([]byte(input))
	if err == nil {
		t.Fatal("expected error for missing gitlabBaseURL")
	}
	if !strings.Contains(err.Error(), "gitlabBaseURL is required") {
		t.Errorf("error = %q, want it to mention gitlabBaseURL", err)
	}
}

func TestParse_WarningGTECritical(t *testing.T) {
	input := `
gitlabBaseURL: "https://gitlab.example.com"
thresholds:
  tokenAgeWarningDays: 90
  tokenAgeCriticalDays: 90
apps:
  - name: pfm
    gitlabProjectID: 100
`
	_, err := Parse([]byte(input))
	if err == nil {
		t.Fatal("expected error when warning >= critical")
	}
	if !strings.Contains(err.Error(), "must be less than") {
		t.Errorf("error = %q, want threshold comparison error", err)
	}
}

func TestParse_DuplicateAppName(t *testing.T) {
	input := `
gitlabBaseURL: "https://gitlab.example.com"
apps:
  - name: pfm
    gitlabProjectID: 100
  - name: pfm
    gitlabProjectID: 200
`
	_, err := Parse([]byte(input))
	if err == nil {
		t.Fatal("expected error for duplicate app name")
	}
	if !strings.Contains(err.Error(), "duplicate app name") {
		t.Errorf("error = %q, want duplicate app error", err)
	}
}

func TestParse_InvalidProjectID(t *testing.T) {
	input := `
gitlabBaseURL: "https://gitlab.example.com"
apps:
  - name: pfm
    gitlabProjectID: 0
`
	_, err := Parse([]byte(input))
	if err == nil {
		t.Fatal("expected error for zero project ID")
	}
	if !strings.Contains(err.Error(), "gitlabProjectID must be > 0") {
		t.Errorf("error = %q, want project ID error", err)
	}
}

func TestParse_MissingAppName(t *testing.T) {
	input := `
gitlabBaseURL: "https://gitlab.example.com"
apps:
  - gitlabProjectID: 100
`
	_, err := Parse([]byte(input))
	if err == nil {
		t.Fatal("expected error for missing app name")
	}
	if !strings.Contains(err.Error(), "name is required") {
		t.Errorf("error = %q, want name required error", err)
	}
}

func TestParse_EmailEnabledMissingFields(t *testing.T) {
	input := `
gitlabBaseURL: "https://gitlab.example.com"
alerting:
  enableEmail: true
apps:
  - name: pfm
    gitlabProjectID: 100
`
	_, err := Parse([]byte(input))
	if err == nil {
		t.Fatal("expected error when email enabled without SMTP config")
	}
	errStr := err.Error()
	if !strings.Contains(errStr, "smtpHost is required") {
		t.Errorf("error = %q, want smtpHost error", errStr)
	}
	if !strings.Contains(errStr, "senderAddress is required") {
		t.Errorf("error = %q, want senderAddress error", errStr)
	}
	if !strings.Contains(errStr, "recipientAddresses must have at least one") {
		t.Errorf("error = %q, want recipientAddresses error", errStr)
	}
}

func TestParse_EmailDisabledNoSMTPValidation(t *testing.T) {
	input := `
gitlabBaseURL: "https://gitlab.example.com"
alerting:
  enableEmail: false
apps:
  - name: pfm
    gitlabProjectID: 100
`
	_, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParse_InvalidDuration(t *testing.T) {
	input := `
gitlabBaseURL: "https://gitlab.example.com"
thresholds:
  pipelineFailureWindow: "not-a-duration"
apps:
  - name: pfm
    gitlabProjectID: 100
`
	_, err := Parse([]byte(input))
	if err == nil {
		t.Fatal("expected error for invalid duration")
	}
	if !strings.Contains(err.Error(), "invalid duration") {
		t.Errorf("error = %q, want invalid duration error", err)
	}
}

func TestParse_InvalidYAML(t *testing.T) {
	_, err := Parse([]byte(`{{{not yaml`))
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}

func TestParse_EmptyApps(t *testing.T) {
	// No apps is valid — OCP-only monitoring with no GitLab mapping.
	input := `
gitlabBaseURL: "https://gitlab.example.com"
apps: []
`
	cfg, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Apps) != 0 {
		t.Errorf("len(Apps) = %d, want 0", len(cfg.Apps))
	}
}

func TestGitLabAppMap(t *testing.T) {
	cfg := &Config{
		Apps: []App{
			{Name: "pfm", GitLabProjectID: 123},
			{Name: "crm", GitLabProjectID: 456},
		},
	}
	m := cfg.GitLabAppMap()
	if len(m) != 2 {
		t.Fatalf("map size = %d, want 2", len(m))
	}
	if m["pfm"] != 123 {
		t.Errorf("m[pfm] = %d, want 123", m["pfm"])
	}
	if m["crm"] != 456 {
		t.Errorf("m[crm] = %d, want 456", m["crm"])
	}
}
