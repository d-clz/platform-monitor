// Package evaluator merges OCP and GitLab checker results, applies thresholds,
// and produces a unified health snapshot ready for the reporter.
package evaluator

import (
	"fmt"
	"sort"
	"time"

	"platform-monitor/internal/checker"
	"platform-monitor/internal/config"
)

// ---- Level ----

// Level represents a health severity level.
type Level string

const (
	LevelOK       Level = "ok"
	LevelWarning  Level = "warning"
	LevelCritical Level = "critical"
	LevelError    Level = "error"
)

// levelRank maps each level to a numeric rank for comparison.
var levelRank = map[Level]int{
	LevelOK:       0,
	LevelWarning:  1,
	LevelCritical: 2,
	LevelError:    3,
}

// maxLevel returns the more severe of two levels.
func maxLevel(a, b Level) Level {
	if levelRank[b] > levelRank[a] {
		return b
	}
	return a
}

// ---- Source ----

// Source describes which monitoring surfaces are active for an app.
type Source string

const (
	SourceOCPGitLab Source = "ocp_gitlab"
	SourceOCPOnly   Source = "ocp_only"
	SourceGitLabOnly Source = "gitlab_only"
)

// ---- Summary types ----

// TokenHealth is the evaluated health of a single token secret.
type TokenHealth struct {
	Present bool  `json:"present"`
	AgeDays int   `json:"ageDays"`
	Level   Level `json:"level"`
}

// OCPSummary is the evaluated OCP health for a single app.
type OCPSummary struct {
	ImageBuilderSAPresent bool        `json:"imageBuilderSAPresent"`
	DeployerSAPresent     bool        `json:"deployerSAPresent"`
	ImageBuilderToken     TokenHealth `json:"imageBuilderToken"`
	DeployerToken         TokenHealth `json:"deployerToken"`
	BindingNamespaces     []string    `json:"bindingNamespaces"` // union of image-builder + deployer namespaces
	Error                 string      `json:"error,omitempty"`
}

// GitLabSummary is the evaluated GitLab health for a single app.
type GitLabSummary struct {
	LastPipeline      *checker.PipelineInfo `json:"lastPipeline"`
	LastPipelineJobs  []checker.JobInfo     `json:"lastPipelineJobs"`
	FailedJobsByStage map[string]int         `json:"failedJobsByStage"`
	RunnerCount       int                    `json:"runnerCount"`
	StaleRunnerCount  int                    `json:"staleRunnerCount"`
	Error             string                 `json:"error,omitempty"`
}

// ---- Output types ----

// AppResult is the merged, evaluated health snapshot for a single app.
type AppResult struct {
	Name   string         `json:"name"`
	Source Source         `json:"source"`
	Level  Level          `json:"level"`
	Issues []string       `json:"issues"`
	OCP    *OCPSummary    `json:"ocp"`    // nil for gitlab_only apps
	GitLab *GitLabSummary `json:"gitlab"` // nil for ocp_only apps
}

// Results is the top-level output of an evaluation run.
type Results struct {
	Timestamp     time.Time   `json:"timestamp"`
	Apps          []AppResult `json:"apps"`
	TotalApps     int         `json:"totalApps"`
	OKCount       int         `json:"okCount"`
	WarningCount  int         `json:"warningCount"`
	CriticalCount int         `json:"criticalCount"`
	ErrorCount    int         `json:"errorCount"`
}

// ---- Evaluator ----

// Evaluator applies configured thresholds to checker output.
type Evaluator struct {
	Thresholds config.Thresholds

	// Now returns the current time. Defaults to time.Now if nil.
	Now func() time.Time
}

func (e *Evaluator) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// Evaluate merges OCP and GitLab results and produces a Results snapshot.
func (e *Evaluator) Evaluate(ocpStatuses []checker.OCPAppStatus, glStatuses []checker.GitLabAppStatus) Results {
	now := e.now()

	// Build lookup maps.
	ocpByName := make(map[string]checker.OCPAppStatus, len(ocpStatuses))
	for _, s := range ocpStatuses {
		ocpByName[s.Name] = s
	}
	glByName := make(map[string]checker.GitLabAppStatus, len(glStatuses))
	for _, s := range glStatuses {
		glByName[s.AppName] = s
	}

	// Union of all app names, sorted for deterministic output.
	nameSet := make(map[string]struct{})
	for n := range ocpByName {
		nameSet[n] = struct{}{}
	}
	for n := range glByName {
		nameSet[n] = struct{}{}
	}
	names := make([]string, 0, len(nameSet))
	for n := range nameSet {
		names = append(names, n)
	}
	sort.Strings(names)

	apps := make([]AppResult, 0, len(names))
	for _, name := range names {
		ocpStatus, hasOCP := ocpByName[name]
		glStatus, hasGL := glByName[name]

		result := e.evaluateApp(name, hasOCP, ocpStatus, hasGL, glStatus, now)
		apps = append(apps, result)
	}

	res := Results{
		Timestamp: now,
		Apps:      apps,
		TotalApps: len(apps),
	}
	for _, a := range apps {
		switch a.Level {
		case LevelOK:
			res.OKCount++
		case LevelWarning:
			res.WarningCount++
		case LevelCritical:
			res.CriticalCount++
		case LevelError:
			res.ErrorCount++
		}
	}
	return res
}

// evaluateApp produces a single AppResult by evaluating both surfaces.
func (e *Evaluator) evaluateApp(
	name string,
	hasOCP bool, ocp checker.OCPAppStatus,
	hasGL bool, gl checker.GitLabAppStatus,
	now time.Time,
) AppResult {
	result := AppResult{Name: name}

	switch {
	case hasOCP && hasGL:
		result.Source = SourceOCPGitLab
	case hasOCP:
		result.Source = SourceOCPOnly
	default:
		result.Source = SourceGitLabOnly
	}

	overall := LevelOK

	if hasOCP {
		summary, lvl, issues := e.evaluateOCP(ocp, now)
		result.OCP = summary
		overall = maxLevel(overall, lvl)
		result.Issues = append(result.Issues, issues...)
	}

	if hasGL {
		summary, lvl, issues := e.evaluateGitLab(gl)
		result.GitLab = summary
		overall = maxLevel(overall, lvl)
		result.Issues = append(result.Issues, issues...)
	}

	// GitLab-only apps are inherently a warning (OCP side is missing).
	if result.Source == SourceGitLabOnly {
		overall = maxLevel(overall, LevelWarning)
		result.Issues = append(result.Issues, "app has no OCP service accounts (gitlab-only)")
	}

	result.Level = overall
	return result
}

// evaluateOCP assesses the OCP side of an app and returns a summary, overall
// level, and a list of human-readable issue strings.
func (e *Evaluator) evaluateOCP(s checker.OCPAppStatus, now time.Time) (*OCPSummary, Level, []string) {
	summary := &OCPSummary{
		ImageBuilderSAPresent: s.ImageBuilderSA.Exists,
		DeployerSAPresent:     s.DeployerSA.Exists,
		BindingNamespaces:     unionNamespaces(s.Bindings),
	}

	// Propagate checker errors.
	// (OCPChecker does not carry a per-app error field; errors abort the whole
	//  run and are handled upstream. This field is reserved for future use.)

	overall := LevelOK
	var issues []string

	raise := func(lvl Level, msg string) {
		overall = maxLevel(overall, lvl)
		issues = append(issues, msg)
	}

	if !s.ImageBuilderSA.Exists {
		raise(LevelWarning, "image-builder service account missing")
	}
	if !s.DeployerSA.Exists {
		raise(LevelWarning, "deployer service account missing")
	}

	// Token health for image-builder.
	ibTH := e.evalToken(s.ImageBuilderToken, now)
	summary.ImageBuilderToken = ibTH
	if !ibTH.Present {
		raise(LevelWarning, "image-builder token secret missing")
	} else if ibTH.Level != LevelOK {
		raise(ibTH.Level, fmt.Sprintf("image-builder token age %d days (%s)", ibTH.AgeDays, ibTH.Level))
	}

	// Token health for deployer.
	depTH := e.evalToken(s.DeployerToken, now)
	summary.DeployerToken = depTH
	if !depTH.Present {
		raise(LevelWarning, "deployer token secret missing")
	} else if depTH.Level != LevelOK {
		raise(depTH.Level, fmt.Sprintf("deployer token age %d days (%s)", depTH.AgeDays, depTH.Level))
	}

	// Binding checks: only warn if the SA exists but has no bindings.
	if s.ImageBuilderSA.Exists && len(s.Bindings.ImageBuilderNamespaces) == 0 {
		raise(LevelWarning, "image-builder SA has no rolebindings")
	}
	if s.DeployerSA.Exists && len(s.Bindings.DeployerNamespaces) == 0 {
		raise(LevelWarning, "deployer SA has no rolebindings")
	}

	return summary, overall, issues
}

// evalToken converts a checker.TokenStatus into a TokenHealth with a level.
func (e *Evaluator) evalToken(ts checker.TokenStatus, now time.Time) TokenHealth {
	if !ts.Exists {
		return TokenHealth{Present: false, Level: LevelOK} // level applied by caller
	}

	ageDays := int(now.Sub(ts.CreatedAt).Hours() / 24)
	th := TokenHealth{Present: true, AgeDays: ageDays}

	switch {
	case ageDays > e.Thresholds.TokenAgeCriticalDays:
		th.Level = LevelCritical
	case ageDays > e.Thresholds.TokenAgeWarningDays:
		th.Level = LevelWarning
	default:
		th.Level = LevelOK
	}
	return th
}

// evaluateGitLab assesses the GitLab side of an app.
func (e *Evaluator) evaluateGitLab(s checker.GitLabAppStatus) (*GitLabSummary, Level, []string) {
	summary := &GitLabSummary{
		LastPipeline:      s.LastPipeline,
		LastPipelineJobs:  s.LastPipelineJobs,
		FailedJobsByStage: s.FailedJobsByStage,
		Error:             s.Error,
	}
	for _, r := range s.Runners {
		summary.RunnerCount++
		if r.Stale {
			summary.StaleRunnerCount++
		}
	}

	overall := LevelOK
	var issues []string

	raise := func(lvl Level, msg string) {
		overall = maxLevel(overall, lvl)
		issues = append(issues, msg)
	}

	if s.Error != "" {
		raise(LevelError, fmt.Sprintf("GitLab API error: %s", s.Error))
		return summary, overall, issues
	}

	// Pipeline checks.
	if s.LastPipeline == nil {
		raise(LevelWarning, "no pipelines found")
	} else {
		switch s.LastPipeline.Status {
		case "failed", "canceled":
			raise(LevelWarning, fmt.Sprintf("last pipeline status: %s", s.LastPipeline.Status))
		}
	}

	// Runner checks.
	if summary.RunnerCount == 0 {
		raise(LevelWarning, "no runners available")
	} else if summary.StaleRunnerCount == summary.RunnerCount {
		raise(LevelWarning, fmt.Sprintf("all %d runner(s) stale", summary.RunnerCount))
	}

	return summary, overall, issues
}

// unionNamespaces merges image-builder and deployer namespace lists,
// deduplicating entries that appear in both.
func unionNamespaces(b checker.BindingInfo) []string {
	seen := make(map[string]struct{})
	var result []string
	for _, ns := range append(b.ImageBuilderNamespaces, b.DeployerNamespaces...) {
		if _, ok := seen[ns]; !ok {
			seen[ns] = struct{}{}
			result = append(result, ns)
		}
	}
	return result
}
