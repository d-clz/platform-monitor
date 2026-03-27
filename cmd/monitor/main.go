// Command monitor is the CronJob entrypoint for the Platform CI Monitor.
//
// It runs a single health-check cycle:
//  1. Load config from YAML
//  2. Discover and check OCP resources
//  3. Check GitLab pipelines and runners
//  4. Evaluate results against thresholds
//  5. Write results.json + history.json to the data directory
//  6. Send alert email if enabled and non-OK apps are found
//
// Environment variables:
//
//	CONFIG_PATH       path to config YAML          (default /etc/ci-monitor/config.yaml)
//	KUBE_API_URL      kube API base URL             (default https://kubernetes.default.svc)
//	KUBE_TOKEN_PATH   path to SA bearer token       (default /var/run/secrets/kubernetes.io/serviceaccount/token)
//	GITLAB_TOKEN      GitLab Personal Access Token  (required)
//	SMTP_USERNAME     SMTP auth username             (optional)
//	SMTP_PASSWORD     SMTP auth password             (optional)
//	DATA_DIR          directory for results files    (default /data)
package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"platform-monitor/internal/alerter"
	"platform-monitor/internal/checker"
	"platform-monitor/internal/config"
	"platform-monitor/internal/evaluator"
	"platform-monitor/internal/reporter"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("monitor: %v", err)
	}
}

func run() error {
	// ---- Configuration ----
	configPath := envOr("CONFIG_PATH", "/etc/ci-monitor/config.yaml")
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	kubeAPIURL := envOr("KUBE_API_URL", "https://kubernetes.default.svc")
	kubeTokenPath := envOr("KUBE_TOKEN_PATH", "/var/run/secrets/kubernetes.io/serviceaccount/token")
	gitlabToken := os.Getenv("GITLAB_TOKEN")
	smtpUsername := os.Getenv("SMTP_USERNAME")
	smtpPassword := os.Getenv("SMTP_PASSWORD")
	dataDir := envOr("DATA_DIR", "/data")

	if gitlabToken == "" {
		return fmt.Errorf("GITLAB_TOKEN environment variable is required")
	}

	kubeToken, err := readFile(kubeTokenPath)
	if err != nil {
		return fmt.Errorf("reading kube token: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	httpClient := &http.Client{Timeout: 30 * time.Second}

	// ---- OCP check ----
	ocpChecker := &checker.OCPChecker{
		Client:    httpClient,
		BaseURL:   kubeAPIURL,
		Token:     kubeToken,
		Namespace: "platform-cicd",
	}
	ocpStatuses, err := ocpChecker.Check(ctx)
	if err != nil {
		// Non-fatal: log and continue with empty OCP data so GitLab checks still run.
		log.Printf("WARNING: OCP check failed: %v", err)
		ocpStatuses = nil
	}
	log.Printf("OCP check complete: %d app(s) discovered", len(ocpStatuses))

	// ---- GitLab project discovery ----
	glChecker := &checker.GitLabChecker{
		Client:             httpClient,
		BaseURL:            cfg.GitLabBaseURL,
		Token:              gitlabToken,
		FailureWindow:      cfg.Thresholds.PipelineFailureWindow.Duration,
		RunnerStalenessMin: cfg.Thresholds.RunnerStalenessMin,
	}

	cachePath := filepath.Join(dataDir, "gitlab-projects-cache.json")
	cached := checker.LoadAppReposCache(cachePath)

	var appRepos []checker.AppRepos
	if cfg.GitLabGroupID != 0 {
		fresh, discErr := glChecker.DiscoverAppRepos(ctx, cfg.GitLabGroupID)
		if discErr != nil {
			// Non-fatal: fall back to the cached list so existing apps still run.
			log.Printf("WARNING: GitLab group discovery failed, using cached project list: %v", discErr)
			fresh = cached
		}

		merged, changed := checker.MergeAppReposCache(cached, fresh)
		if changed {
			if err := checker.SaveAppReposCache(cachePath, merged); err != nil {
				log.Printf("WARNING: saving project cache: %v", err)
			}
			log.Printf("GitLab project cache updated: %d app(s) total", len(merged))
		}

		appNames := make([]string, 0, len(merged))
		for name := range merged {
			appNames = append(appNames, name)
		}
		sort.Strings(appNames)
		for _, name := range appNames {
			appRepos = append(appRepos, checker.AppRepos{AppName: name, Repos: merged[name]})
		}
	}
	log.Printf("GitLab discovery complete: %d app(s) to check", len(appRepos))

	// ---- GitLab check ----
	glStatuses, err := glChecker.Check(ctx, appRepos)
	if err != nil {
		log.Printf("WARNING: GitLab check failed: %v", err)
		glStatuses = nil
	}
	log.Printf("GitLab check complete: %d app(s) checked", len(glStatuses))

	// ---- Evaluate ----
	eval := &evaluator.Evaluator{Thresholds: cfg.Thresholds}
	results := eval.Evaluate(ocpStatuses, glStatuses)
	log.Printf("Evaluation complete: %d total, %d ok, %d warning, %d critical, %d error",
		results.TotalApps, results.OKCount, results.WarningCount, results.CriticalCount, results.ErrorCount)

	// ---- Report ----
	rep := &reporter.Reporter{DataDir: dataDir}
	if err := rep.Write(results); err != nil {
		return fmt.Errorf("writing results: %w", err)
	}
	log.Printf("Results written to %s", dataDir)

	// ---- Alert ----
	al := &alerter.Alerter{
		Config:       cfg.Alerting,
		SMTPUsername: smtpUsername,
		SMTPPassword: smtpPassword,
	}
	if err := al.Send(results); err != nil {
		// Non-fatal: alert failure should not fail the whole run.
		log.Printf("WARNING: alert email failed: %v", err)
	}

	return nil
}

// envOr returns the value of the named environment variable, or fallback if unset.
func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// readFile reads a file and returns its trimmed contents as a string.
func readFile(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
