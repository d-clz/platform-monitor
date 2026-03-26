package alerter

import (
	"net/smtp"
	"strings"
	"testing"
	"time"

	"platform-monitor/internal/config"
	"platform-monitor/internal/evaluator"
)

// ---- helpers ----

var fixedTime = time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)

type capturedCall struct {
	addr string
	from string
	to   []string
	msg  string
}

// capturingSendMail records the arguments passed to SendMail.
func capturingSendMail(cap *capturedCall) SendMailFunc {
	return func(addr string, _ smtp.Auth, from string, to []string, msg []byte) error {
		cap.addr = addr
		cap.from = from
		cap.to = to
		cap.msg = string(msg)
		return nil
	}
}

func enabledConfig() config.Alerting {
	return config.Alerting{
		EnableEmail:        true,
		SMTPHost:           "smtp.example.com",
		SMTPPort:           587,
		SenderAddress:      "ci-monitor@example.com",
		RecipientAddresses: []string{"team@example.com"},
	}
}

func warnResults() evaluator.Results {
	return evaluator.Results{
		Timestamp: fixedTime,
		Apps: []evaluator.AppResult{
			{
				Name:   "pfm",
				Level:  evaluator.LevelWarning,
				Issues: []string{"deployer token age 70 days (warning)", "image-builder SA has no rolebindings"},
			},
			{
				Name:  "crm",
				Level: evaluator.LevelOK,
			},
		},
		TotalApps:    2,
		OKCount:      1,
		WarningCount: 1,
	}
}

func newAlerter(cfg config.Alerting, cap *capturedCall) *Alerter {
	return &Alerter{
		Config:   cfg,
		SendMail: capturingSendMail(cap),
		Now:      func() time.Time { return fixedTime },
	}
}

// ---- tests ----

// TestSend_emailDisabled verifies that Send is a no-op when EnableEmail=false.
func TestSend_emailDisabled(t *testing.T) {
	var cap capturedCall
	cfg := enabledConfig()
	cfg.EnableEmail = false

	if err := newAlerter(cfg, &cap).Send(warnResults()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cap.addr != "" {
		t.Errorf("expected no SMTP call, but got addr=%q", cap.addr)
	}
}

// TestSend_noAlerts verifies that Send is a no-op when all apps are OK.
func TestSend_noAlerts(t *testing.T) {
	var cap capturedCall
	okResults := evaluator.Results{
		Timestamp: fixedTime,
		Apps:      []evaluator.AppResult{{Name: "pfm", Level: evaluator.LevelOK}},
		TotalApps: 1,
		OKCount:   1,
	}

	if err := newAlerter(enabledConfig(), &cap).Send(okResults); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cap.addr != "" {
		t.Errorf("expected no SMTP call for all-OK results")
	}
}

// TestSend_correctSMTPAddress verifies that Send uses host:port from config.
func TestSend_correctSMTPAddress(t *testing.T) {
	var cap capturedCall
	if err := newAlerter(enabledConfig(), &cap).Send(warnResults()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cap.addr != "smtp.example.com:587" {
		t.Errorf("expected addr=smtp.example.com:587, got %q", cap.addr)
	}
	if cap.from != "ci-monitor@example.com" {
		t.Errorf("expected from=ci-monitor@example.com, got %q", cap.from)
	}
	if len(cap.to) != 1 || cap.to[0] != "team@example.com" {
		t.Errorf("unexpected to: %v", cap.to)
	}
}

// TestSend_subjectContainsCounts verifies that the subject line includes
// the warning/critical/error counts.
func TestSend_subjectContainsCounts(t *testing.T) {
	var cap capturedCall
	if err := newAlerter(enabledConfig(), &cap).Send(warnResults()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(cap.msg, "Subject:") {
		t.Fatal("no Subject header in message")
	}
	if !strings.Contains(cap.msg, "1 warning(s)") {
		t.Errorf("subject missing warning count; msg:\n%s", cap.msg)
	}
	if !strings.Contains(cap.msg, "[CI Monitor]") {
		t.Errorf("subject missing [CI Monitor] prefix; msg:\n%s", cap.msg)
	}
}

// TestSend_bodyContainsAppIssues verifies that the email body lists each
// non-OK app along with its issues.
func TestSend_bodyContainsAppIssues(t *testing.T) {
	var cap capturedCall
	if err := newAlerter(enabledConfig(), &cap).Send(warnResults()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{
		"[WARNING] pfm",
		"deployer token age 70 days",
		"image-builder SA has no rolebindings",
	} {
		if !strings.Contains(cap.msg, want) {
			t.Errorf("body missing %q; msg:\n%s", want, cap.msg)
		}
	}
	// OK app should not appear in the body.
	if strings.Contains(cap.msg, "[OK] crm") {
		t.Errorf("OK app crm should not appear in alert body")
	}
}

// TestSend_criticalAndErrorCounts verifies subject handling when multiple
// severity levels are present simultaneously.
func TestSend_criticalAndErrorCounts(t *testing.T) {
	var cap capturedCall
	mixed := evaluator.Results{
		Timestamp: fixedTime,
		Apps: []evaluator.AppResult{
			{Name: "pfm", Level: evaluator.LevelCritical, Issues: []string{"token age 95 days"}},
			{Name: "crm", Level: evaluator.LevelError, Issues: []string{"GitLab API error: timeout"}},
		},
		TotalApps:     2,
		CriticalCount: 1,
		ErrorCount:    1,
	}

	if err := newAlerter(enabledConfig(), &cap).Send(mixed); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, want := range []string{"1 critical", "1 error(s)"} {
		if !strings.Contains(cap.msg, want) {
			t.Errorf("subject missing %q; msg:\n%s", want, cap.msg)
		}
	}
}
