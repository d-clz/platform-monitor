package evaluator

import (
	"testing"
	"time"

	"platform-monitor/internal/checker"
	"platform-monitor/internal/config"
)

// ---- helpers ----

var fixedNow = time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)

func newEvaluator() *Evaluator {
	return &Evaluator{
		Thresholds: config.Thresholds{
			TokenAgeWarningDays:  60,
			TokenAgeCriticalDays: 90,
			RunnerStalenessMin:   10,
		},
		Now: func() time.Time { return fixedNow },
	}
}

// tokenStatus builds a checker.TokenStatus aged exactly ageDays days before fixedNow.
func tokenStatus(ageDays int) checker.TokenStatus {
	created := fixedNow.Add(-time.Duration(ageDays) * 24 * time.Hour)
	return checker.TokenStatus{
		Exists:    true,
		CreatedAt: created,
		Age:       fixedNow.Sub(created),
	}
}

func missingToken() checker.TokenStatus {
	return checker.TokenStatus{Exists: false}
}

func healthyOCP(name string) checker.OCPAppStatus {
	return checker.OCPAppStatus{
		Name:              name,
		ImageBuilderSA:    checker.SAStatus{Exists: true},
		DeployerSA:        checker.SAStatus{Exists: true},
		ImageBuilderToken: tokenStatus(10),
		DeployerToken:     tokenStatus(10),
		Bindings: checker.BindingInfo{
			ImageBuilderNamespaces: []string{"pfm-sit", "pfm-uat"},
			DeployerNamespaces:     []string{"pfm-sit", "pfm-uat"},
		},
	}
}

func healthyGL(name string, projectID int) checker.GitLabAppStatus {
	pipeline := &checker.PipelineInfo{
		ID:        1,
		Status:    "success",
		Ref:       "main",
		CreatedAt: fixedNow.Add(-1 * time.Hour),
		WebURL:    "https://gl/p/1",
	}
	return checker.GitLabAppStatus{
		AppName: name,
		Repos: []checker.RepoStatus{
			{
				RepoName:          name,
				ProjectID:         projectID,
				LastPipeline:      pipeline,
				FailedJobsByStage: map[string]int{},
			},
		},
	}
}

func hasIssue(issues []string, substr string) bool {
	for _, s := range issues {
		if len(s) >= len(substr) {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
		}
	}
	return false
}

// ---- tests ----

// TestEvaluate_fullHealthy verifies that a clean app with both OCP and GitLab
// data produces Level=ok with no issues.
func TestEvaluate_fullHealthy(t *testing.T) {
	e := newEvaluator()
	results := e.Evaluate(
		[]checker.OCPAppStatus{healthyOCP("pfm")},
		[]checker.GitLabAppStatus{healthyGL("pfm", 10)},
	)

	if results.TotalApps != 1 {
		t.Fatalf("expected 1 app, got %d", results.TotalApps)
	}
	app := results.Apps[0]
	if app.Level != LevelOK {
		t.Errorf("expected level=ok, got %q; issues: %v", app.Level, app.Issues)
	}
	if app.Source != SourceOCPGitLab {
		t.Errorf("expected source=ocp_gitlab, got %q", app.Source)
	}
	if results.OKCount != 1 || results.WarningCount != 0 {
		t.Errorf("unexpected counts: ok=%d warn=%d", results.OKCount, results.WarningCount)
	}
}

// TestEvaluate_tokenAgeWarning verifies that a token between warning and critical
// thresholds produces Level=warning.
func TestEvaluate_tokenAgeWarning(t *testing.T) {
	ocp := healthyOCP("pfm")
	ocp.ImageBuilderToken = tokenStatus(70) // 70 days > 60 warning, < 90 critical

	results := newEvaluator().Evaluate(
		[]checker.OCPAppStatus{ocp},
		[]checker.GitLabAppStatus{healthyGL("pfm", 10)},
	)
	app := results.Apps[0]
	if app.Level != LevelWarning {
		t.Errorf("expected warning, got %q", app.Level)
	}
	if !hasIssue(app.Issues, "image-builder token age 70 days") {
		t.Errorf("expected token age issue, got: %v", app.Issues)
	}
	if app.OCP.ImageBuilderToken.Level != LevelWarning {
		t.Errorf("expected token health=warning, got %q", app.OCP.ImageBuilderToken.Level)
	}
}

// TestEvaluate_tokenAgeCritical verifies that a token exceeding the critical
// threshold produces Level=critical.
func TestEvaluate_tokenAgeCritical(t *testing.T) {
	ocp := healthyOCP("pfm")
	ocp.DeployerToken = tokenStatus(95) // 95 days > 90 critical

	results := newEvaluator().Evaluate(
		[]checker.OCPAppStatus{ocp},
		[]checker.GitLabAppStatus{healthyGL("pfm", 10)},
	)
	app := results.Apps[0]
	if app.Level != LevelCritical {
		t.Errorf("expected critical, got %q", app.Level)
	}
	if !hasIssue(app.Issues, "deployer token age 95 days") {
		t.Errorf("expected deployer token age issue, got: %v", app.Issues)
	}
}

// TestEvaluate_missingSA verifies that a missing service account produces a warning.
func TestEvaluate_missingSA(t *testing.T) {
	ocp := healthyOCP("pfm")
	ocp.ImageBuilderSA = checker.SAStatus{Exists: false}

	results := newEvaluator().Evaluate(
		[]checker.OCPAppStatus{ocp},
		[]checker.GitLabAppStatus{healthyGL("pfm", 10)},
	)
	app := results.Apps[0]
	if app.Level != LevelWarning {
		t.Errorf("expected warning, got %q", app.Level)
	}
	if !hasIssue(app.Issues, "image-builder service account missing") {
		t.Errorf("expected SA missing issue, got: %v", app.Issues)
	}
}

// TestEvaluate_failedPipeline verifies that a failed pipeline produces a warning.
func TestEvaluate_failedPipeline(t *testing.T) {
	gl := healthyGL("pfm", 10)
	gl.Repos[0].LastPipeline.Status = "failed"

	results := newEvaluator().Evaluate(
		[]checker.OCPAppStatus{healthyOCP("pfm")},
		[]checker.GitLabAppStatus{gl},
	)
	app := results.Apps[0]
	if app.Level != LevelWarning {
		t.Errorf("expected warning, got %q", app.Level)
	}
	if !hasIssue(app.Issues, "last pipeline status: failed") {
		t.Errorf("expected pipeline issue, got: %v", app.Issues)
	}
}

// TestEvaluate_reposSummaryPopulated verifies that GitLabSummary.Repos is
// populated with one entry per repo after evaluation.
func TestEvaluate_reposSummaryPopulated(t *testing.T) {
	results := newEvaluator().Evaluate(
		[]checker.OCPAppStatus{healthyOCP("pfm")},
		[]checker.GitLabAppStatus{healthyGL("pfm", 10)},
	)
	app := results.Apps[0]
	if app.GitLab == nil {
		t.Fatal("expected non-nil GitLab summary")
	}
	if len(app.GitLab.Repos) != 1 {
		t.Fatalf("expected 1 repo in summary, got %d", len(app.GitLab.Repos))
	}
	if app.GitLab.Repos[0].RepoName != "pfm" {
		t.Errorf("expected RepoName=pfm, got %q", app.GitLab.Repos[0].RepoName)
	}
	if app.GitLab.Repos[0].LastPipeline == nil {
		t.Error("expected LastPipeline populated in repo summary")
	}
}

// TestEvaluate_ocpOnly verifies that an app with OCP data but no GitLab entry
// has source=ocp_only and nil GitLab summary.
func TestEvaluate_ocpOnly(t *testing.T) {
	results := newEvaluator().Evaluate(
		[]checker.OCPAppStatus{healthyOCP("pfm")},
		nil,
	)
	app := results.Apps[0]
	if app.Source != SourceOCPOnly {
		t.Errorf("expected source=ocp_only, got %q", app.Source)
	}
	if app.GitLab != nil {
		t.Errorf("expected nil GitLab summary")
	}
	if app.OCP == nil {
		t.Errorf("expected non-nil OCP summary")
	}
	if app.Level != LevelOK {
		t.Errorf("expected ok for healthy ocp-only app, got %q", app.Level)
	}
}

// TestEvaluate_gitlabOnly verifies that an app present only in GitLab is flagged
// with a warning and nil OCP summary.
func TestEvaluate_gitlabOnly(t *testing.T) {
	results := newEvaluator().Evaluate(
		nil,
		[]checker.GitLabAppStatus{healthyGL("pfm", 10)},
	)
	app := results.Apps[0]
	if app.Source != SourceGitLabOnly {
		t.Errorf("expected source=gitlab_only, got %q", app.Source)
	}
	if app.OCP != nil {
		t.Errorf("expected nil OCP summary")
	}
	if app.Level != LevelWarning {
		t.Errorf("expected warning for gitlab-only app, got %q", app.Level)
	}
	if !hasIssue(app.Issues, "no OCP service accounts") {
		t.Errorf("expected gitlab-only issue, got: %v", app.Issues)
	}
}

// TestEvaluate_gitlabAPIError verifies that a GitLab repo API error is surfaced
// as Level=error.
func TestEvaluate_gitlabAPIError(t *testing.T) {
	gl := healthyGL("pfm", 10)
	gl.Repos[0].Error = "API /api/v4/projects/10/pipelines returned 503: service unavailable"
	gl.Repos[0].LastPipeline = nil

	results := newEvaluator().Evaluate(
		[]checker.OCPAppStatus{healthyOCP("pfm")},
		[]checker.GitLabAppStatus{gl},
	)
	app := results.Apps[0]
	if app.Level != LevelError {
		t.Errorf("expected error, got %q", app.Level)
	}
	if !hasIssue(app.Issues, "GitLab API error") {
		t.Errorf("expected GitLab API error issue, got: %v", app.Issues)
	}
}

// TestEvaluate_aggregateCounts verifies that TotalApps and per-level counts are
// computed correctly across a mixed set of apps.
func TestEvaluate_aggregateCounts(t *testing.T) {
	ocpBad := healthyOCP("crm")
	ocpBad.ImageBuilderSA = checker.SAStatus{Exists: false}

	glError := healthyGL("svc", 30)
	glError.Repos[0].Error = "timeout"
	glError.Repos[0].LastPipeline = nil

	results := newEvaluator().Evaluate(
		[]checker.OCPAppStatus{
			healthyOCP("pfm"),
			ocpBad,
		},
		[]checker.GitLabAppStatus{
			healthyGL("pfm", 10),
			healthyGL("crm", 20),
			glError,
		},
	)

	if results.TotalApps != 3 {
		t.Errorf("expected 3 apps, got %d", results.TotalApps)
	}
	if results.OKCount != 1 {
		t.Errorf("expected OKCount=1, got %d", results.OKCount)
	}
	if results.WarningCount != 1 {
		t.Errorf("expected WarningCount=1, got %d", results.WarningCount)
	}
	if results.ErrorCount != 1 {
		t.Errorf("expected ErrorCount=1, got %d", results.ErrorCount)
	}
}

// TestEvaluate_sortedOutput verifies that apps are returned in alphabetical order
// regardless of input order.
func TestEvaluate_sortedOutput(t *testing.T) {
	results := newEvaluator().Evaluate(
		[]checker.OCPAppStatus{
			healthyOCP("zebra"),
			healthyOCP("alpha"),
			healthyOCP("middle"),
		},
		nil,
	)
	names := make([]string, len(results.Apps))
	for i, a := range results.Apps {
		names[i] = a.Name
	}
	want := []string{"alpha", "middle", "zebra"}
	for i, w := range want {
		if names[i] != w {
			t.Errorf("position %d: expected %q, got %q", i, w, names[i])
		}
	}
}

// TestEvaluate_missingTokens verifies that missing token secrets raise warnings.
func TestEvaluate_missingTokens(t *testing.T) {
	ocp := healthyOCP("pfm")
	ocp.ImageBuilderToken = missingToken()
	ocp.DeployerToken = missingToken()

	results := newEvaluator().Evaluate(
		[]checker.OCPAppStatus{ocp},
		[]checker.GitLabAppStatus{healthyGL("pfm", 10)},
	)
	app := results.Apps[0]
	if app.Level != LevelWarning {
		t.Errorf("expected warning, got %q", app.Level)
	}
	if !hasIssue(app.Issues, "image-builder token secret missing") {
		t.Errorf("missing image-builder token issue, got: %v", app.Issues)
	}
	if !hasIssue(app.Issues, "deployer token secret missing") {
		t.Errorf("missing deployer token issue, got: %v", app.Issues)
	}
}
