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
gitlabGroupID: 42
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
	if cfg.GitLabGroupID != 42 {
		t.Errorf("GitLabGroupID = %d, want 42", cfg.GitLabGroupID)
	}
}

func TestParse_Defaults(t *testing.T) {
	// Minimal config — only required fields, everything else should get defaults.
	minimal := `
gitlabBaseURL: "https://gitlab.example.com"
gitlabGroupID: 10
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
gitlabGroupID: 10
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
gitlabGroupID: 10
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
gitlabBaseURL: "https://gitlab.example.com"
gitlabGroupID: 10
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
gitlabBaseURL: "https://gitlab.example.com"
gitlabGroupID: 10
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
gitlabBaseURL: "https://gitlab.example.com"
gitlabGroupID: 10
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

// TestParse_GroupIDZero verifies that gitlabGroupID=0 (omitted) is accepted —
// the monitor can run OCP-only with no GitLab group configured.
func TestParse_GroupIDZero(t *testing.T) {
	input := `
gitlabBaseURL: "https://gitlab.example.com"
`
	cfg, err := Parse([]byte(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.GitLabGroupID != 0 {
		t.Errorf("GitLabGroupID = %d, want 0", cfg.GitLabGroupID)
	}
}
