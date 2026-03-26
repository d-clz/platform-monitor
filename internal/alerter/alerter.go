// Package alerter sends email notifications when non-OK apps are detected.
// It is a no-op unless config.Alerting.EnableEmail is true.
package alerter

import (
	"bytes"
	"fmt"
	"net/smtp"
	"strings"
	"time"

	"platform-monitor/internal/config"
	"platform-monitor/internal/evaluator"
)

// SendMailFunc matches the signature of net/smtp.SendMail and can be replaced
// in tests without spinning up a real SMTP server.
type SendMailFunc func(addr string, a smtp.Auth, from string, to []string, msg []byte) error

// Alerter sends email alerts for non-OK monitoring results.
type Alerter struct {
	Config       config.Alerting
	SMTPUsername string      // injected from secret at runtime (may be empty)
	SMTPPassword string      // injected from secret at runtime (may be empty)
	SendMail     SendMailFunc // defaults to smtp.SendMail if nil

	// Now returns the current time. Defaults to time.Now if nil.
	Now func() time.Time
}

func (a *Alerter) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Alerter) sendMail() SendMailFunc {
	if a.SendMail != nil {
		return a.SendMail
	}
	return smtp.SendMail
}

// Send dispatches an alert email if email is enabled and results contain at
// least one non-OK app. It is a no-op in all other cases.
func (a *Alerter) Send(results evaluator.Results) error {
	if !a.Config.EnableEmail {
		return nil
	}
	if results.WarningCount+results.CriticalCount+results.ErrorCount == 0 {
		return nil
	}

	subject := a.buildSubject(results)
	body := a.buildBody(results)
	msg := buildMessage(a.Config.SenderAddress, a.Config.RecipientAddresses, subject, body)

	addr := fmt.Sprintf("%s:%d", a.Config.SMTPHost, a.Config.SMTPPort)
	var auth smtp.Auth
	if a.SMTPUsername != "" {
		auth = smtp.PlainAuth("", a.SMTPUsername, a.SMTPPassword, a.Config.SMTPHost)
	}

	if err := a.sendMail()(addr, auth, a.Config.SenderAddress, a.Config.RecipientAddresses, msg); err != nil {
		return fmt.Errorf("sending alert email: %w", err)
	}
	return nil
}

// buildSubject produces a subject line summarising the severity counts.
func (a *Alerter) buildSubject(results evaluator.Results) string {
	ts := a.now().UTC().Format("2006-01-02 15:04 UTC")
	parts := []string{}
	if results.CriticalCount > 0 {
		parts = append(parts, fmt.Sprintf("%d critical", results.CriticalCount))
	}
	if results.WarningCount > 0 {
		parts = append(parts, fmt.Sprintf("%d warning(s)", results.WarningCount))
	}
	if results.ErrorCount > 0 {
		parts = append(parts, fmt.Sprintf("%d error(s)", results.ErrorCount))
	}
	return fmt.Sprintf("[CI Monitor] %s — %s", strings.Join(parts, ", "), ts)
}

// buildBody produces a plain-text email body listing each non-OK app and its issues.
func (a *Alerter) buildBody(results evaluator.Results) string {
	var buf bytes.Buffer

	fmt.Fprintf(&buf, "Platform CI Monitor Alert\n")
	fmt.Fprintf(&buf, "Run time: %s\n\n", a.now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&buf, "Summary: %d total app(s) — %d ok, %d warning, %d critical, %d error\n\n",
		results.TotalApps, results.OKCount, results.WarningCount, results.CriticalCount, results.ErrorCount)

	for _, app := range results.Apps {
		if app.Level == evaluator.LevelOK {
			continue
		}
		fmt.Fprintf(&buf, "  [%s] %s\n", strings.ToUpper(string(app.Level)), app.Name)
		for _, issue := range app.Issues {
			fmt.Fprintf(&buf, "    - %s\n", issue)
		}
	}

	fmt.Fprintf(&buf, "\n--\nPlatform CI Monitor\n")
	return buf.String()
}

// buildMessage formats a minimal RFC 2822 email message.
func buildMessage(from string, to []string, subject, body string) []byte {
	var buf bytes.Buffer
	fmt.Fprintf(&buf, "From: %s\r\n", from)
	fmt.Fprintf(&buf, "To: %s\r\n", strings.Join(to, ", "))
	fmt.Fprintf(&buf, "Subject: %s\r\n", subject)
	fmt.Fprintf(&buf, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&buf, "Content-Type: text/plain; charset=utf-8\r\n")
	fmt.Fprintf(&buf, "\r\n")
	// RFC 2822 requires CRLF line endings in the body.
	fmt.Fprintf(&buf, "%s", strings.ReplaceAll(body, "\n", "\r\n"))
	return buf.Bytes()
}
