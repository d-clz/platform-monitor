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
//	OCP_TOKEN         bearer token for OCP API      (takes priority over KUBE_TOKEN_PATH)
//	KUBE_TOKEN_PATH   path to SA bearer token       (default /var/run/secrets/kubernetes.io/serviceaccount/token)
//	KUBE_NAMESPACE    SA home namespace              (default platform-cicd)
//	OCP_SKIP_TLS      skip TLS verification          (default false, set "true" for self-signed certs)
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
	kubeNamespace := envOr("KUBE_NAMESPACE", "platform-cicd")
	gitlabToken := os.Getenv("GITLAB_TOKEN")
	smtpUsername := os.Getenv("SMTP_USERNAME")
	smtpPassword := os.Getenv("SMTP_PASSWORD")
	dataDir := envOr("DATA_DIR", "/data")

	if gitlabToken == "" {
		return fmt.Errorf("GITLAB_TOKEN environment variable is required")
	}

	// OCP_TOKEN takes priority; falls back to reading from KUBE_TOKEN_PATH.
	kubeToken, err := resolveKubeToken()
	if err != nil {
		return fmt.Errorf("resolving kube token: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	// ---- OCP check ----
	// NewOCPChecker wires TLS config from OCP_SKIP_TLS env var.
	// Set OCP_SKIP_TLS="true" in your ConfigMap for clusters with self-signed certs.
	ocpChecker := checker.NewOCPChecker(kubeAPIURL, kubeToken, kubeNamespace)
	ocpStatuses, err := ocpChecker.Check(ctx)
	if err != nil {
		// Non-fatal: log and continue with empty OCP data so GitLab checks still run.
		log.Printf("WARNING: OCP check failed: %v", err)
		ocpStatuses = nil
	}
	log.Printf("OCP check complete: %d app(s) discovered", len(ocpStatuses))

	// ---- GitLab project discovery ----
	// GitLab uses its own plain HTTP client — TLS skip is OCP-specific.
	httpClient := &http.Client{Timeout: 30 * time.Second}

	glChecker := &checker.GitLabChecker{
		Client:             httpClient,
		BaseURL:            cfg.GitLabBaseURL,
		Token:              gitlabToken,
		FailureWindow:      cfg.Thresholds.PipelineFailureWindow.Duration,
		RunnerStalenessMin: cfg.Thresholds.RunnerStalenessMin,
	}

	// ---- GitLab per-app repo discovery ----
	// For each app in config, fetch its repos from the declared sub-group,
	// merge with the on-disk cache (append-only), then check all repos.
	cachePath := filepath.Join(dataDir, "gitlab-projects-cache.json")
	cache := checker.LoadAppReposCache(cachePath)

	var appRepos []checker.AppRepos
	cacheChanged := false

	for _, app := range cfg.Apps {
		fresh, discErr := glChecker.GetAppRepos(ctx, app.GitLabGroupID)
		if discErr != nil {
			// Non-fatal: use cached repos so the app still runs against stale data.
			log.Printf("WARNING: repo discovery for %q (group %d) failed, using cache: %v",
				app.Name, app.GitLabGroupID, discErr)
			fresh = cache[app.Name]
		}

		merged, changed := checker.MergeAppReposCache(
			map[string][]checker.RepoInfo{app.Name: cache[app.Name]},
			map[string][]checker.RepoInfo{app.Name: fresh},
		)
		if changed {
			cache[app.Name] = merged[app.Name]
			cacheChanged = true
		}

		appRepos = append(appRepos, checker.AppRepos{AppName: app.Name, Repos: cache[app.Name]})
	}

	if cacheChanged {
		if err := checker.SaveAppReposCache(cachePath, cache); err != nil {
			log.Printf("WARNING: saving project cache: %v", err)
		}
		log.Printf("GitLab project cache updated: %d app(s)", len(cache))
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

// resolveKubeToken returns the OCP bearer token.
// OCP_TOKEN env var takes priority; falls back to reading from KUBE_TOKEN_PATH.
func resolveKubeToken() (string, error) {
	if token := os.Getenv("OCP_TOKEN"); token != "" {
		log.Printf("using OCP token from OCP_TOKEN env var")
		return token, nil
	}

	tokenPath := envOr("KUBE_TOKEN_PATH", "/var/run/secrets/kubernetes.io/serviceaccount/token")
	log.Printf("OCP_TOKEN not set, reading token from %s", tokenPath)

	token, err := readFile(tokenPath)
	if err != nil {
		return "", fmt.Errorf("reading token from %s: %w", tokenPath, err)
	}
	if token == "" {
		return "", fmt.Errorf("token file %s is empty", tokenPath)
	}
	return token, nil
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
