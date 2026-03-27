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
    gitlabGroupID: 10
  - name: crm
    gitlabGroupID: 20
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
	if cfg.Apps[0].Name != "pfm" || cfg.Apps[0].GitLabGroupID != 10 {
		t.Errorf("Apps[0] = %+v, want {pfm 10}", cfg.Apps[0])
	}
	if cfg.Apps[1].Name != "crm" || cfg.Apps[1].GitLabGroupID != 20 {
		t.Errorf("Apps[1] = %+v, want {crm 20}", cfg.Apps[1])
	}
}

func TestParse_Defaults(t *testing.T) {
	// Minimal config — no apps, no GitLab URL needed, everything else gets defaults.
	minimal := `
thresholds:
  tokenAgeWarningDays: 0
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

// TestParse_NoAppsOCPOnly verifies that an empty apps list is valid (OCP-only mode).
func TestParse_NoAppsOCPOnly(t *testing.T) {
	input := `
apps: []
`
	cfg, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error for empty apps list: %v", err)
	}
	if len(cfg.Apps) != 0 {
		t.Errorf("expected empty apps, got %d", len(cfg.Apps))
	}
}

// TestParse_NoAppsNoGitLabURL verifies that gitlabBaseURL is not required when no apps are configured.
func TestParse_NoAppsNoGitLabURL(t *testing.T) {
	_, err := Parse([]byte(`{}`))
	if err != nil {
		t.Fatalf("unexpected error with no apps and no gitlabBaseURL: %v", err)
	}
}

func TestParse_MissingGitLabBaseURL(t *testing.T) {
	input := `
apps:
  - name: pfm
    gitlabGroupID: 10
`
	_, err := Parse([]byte(input))
	if err == nil {
		t.Fatal("expected error for missing gitlabBaseURL when apps are configured")
	}
	if !strings.Contains(err.Error(), "gitlabBaseURL is required") {
		t.Errorf("error = %q, want it to mention gitlabBaseURL", err)
	}
}

func TestParse_AppMissingName(t *testing.T) {
	input := `
gitlabBaseURL: "https://gitlab.example.com"
apps:
  - gitlabGroupID: 10
`
	_, err := Parse([]byte(input))
	if err == nil {
		t.Fatal("expected error for app with no name")
	}
	if !strings.Contains(err.Error(), "name is required") {
		t.Errorf("error = %q, want name error", err)
	}
}

func TestParse_AppMissingGroupID(t *testing.T) {
	input := `
gitlabBaseURL: "https://gitlab.example.com"
apps:
  - name: pfm
`
	_, err := Parse([]byte(input))
	if err == nil {
		t.Fatal("expected error for app with no gitlabGroupID")
	}
	if !strings.Contains(err.Error(), "gitlabGroupID must be a positive integer") {
		t.Errorf("error = %q, want groupID error", err)
	}
}

func TestParse_AppGroupIDZero(t *testing.T) {
	input := `
gitlabBaseURL: "https://gitlab.example.com"
apps:
  - name: pfm
    gitlabGroupID: 0
`
	_, err := Parse([]byte(input))
	if err == nil {
		t.Fatal("expected error for gitlabGroupID=0")
	}
}

func TestParse_WarningGTECritical(t *testing.T) {
	input := `
thresholds:
  tokenAgeWarningDays: 90
  tokenAgeCriticalDays: 90
`
	_, err := Parse([]byte(input))
	if err == nil {
		t.Fatal("expected error when warning >= critical")
	}
	if !strings.Contains(err.Error(), "must be less than") {
		t.Errorf("error = %q, want threshold comparison error", err)
	}
}

func TestParse_EmailEnabledMissingFields(t *testing.T) {
	input := `
alerting:
  enableEmail: true
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
alerting:
  enableEmail: false
`
	_, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParse_InvalidDuration(t *testing.T) {
	input := `
thresholds:
  pipelineFailureWindow: "not-a-duration"
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
