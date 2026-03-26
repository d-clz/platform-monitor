package checker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"platform-monitor/internal/config"
)

// ---- test helpers ----

// fixedTime is a stable reference point for all GitLab tests.
var fixedTime = time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)

func newGitLabChecker(srv *httptest.Server) *GitLabChecker {
	return &GitLabChecker{
		Client:             srv.Client(),
		BaseURL:            srv.URL,
		Token:              "test-token",
		FailureWindow:      24 * time.Hour,
		RunnerStalenessMin: 10,
		Now:                func() time.Time { return fixedTime },
	}
}

// glMux builds a ServeMux that routes GitLab API paths to the provided handlers.
// Any unregistered path returns 404.
func glMux(routes map[string]http.HandlerFunc) *httptest.Server {
	mux := http.NewServeMux()
	for path, handler := range routes {
		mux.HandleFunc(path, handler)
	}
	return httptest.NewServer(mux)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// ---- helpers for building GL API responses ----

func pipelineResponse(id int, status, ref, createdAt, webURL string) []glPipeline {
	return []glPipeline{{ID: id, Status: status, Ref: ref, CreatedAt: createdAt, WebURL: webURL}}
}

func jobResponse(stage, createdAt string) glJob {
	return glJob{Status: "failed", Stage: stage, CreatedAt: createdAt}
}

func runnerResponse(id int, description string, active bool, contactedAt string) glRunner {
	return glRunner{ID: id, Description: description, Active: active, ContactedAt: contactedAt}
}

// ---- tests ----

// TestGitLabChecker_Check_success exercises the full happy path with two apps.
func TestGitLabChecker_Check_success(t *testing.T) {
	pipelineTS := fixedTime.Add(-2 * time.Hour).Format(time.RFC3339)
	jobTS := fixedTime.Add(-1 * time.Hour).Format(time.RFC3339)
	runnerTS := fixedTime.Add(-5 * time.Minute).Format(time.RFC3339)

	srv := glMux(map[string]http.HandlerFunc{
		"/api/v4/projects/10/pipelines": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, pipelineResponse(101, "success", "main", pipelineTS, "https://gl/p/101"))
		},
		"/api/v4/projects/10/jobs": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glJob{jobResponse("build", jobTS)})
		},
		"/api/v4/projects/10/runners": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glRunner{runnerResponse(1, "runner-a", true, runnerTS)})
		},
		"/api/v4/projects/20/pipelines": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, pipelineResponse(202, "failed", "main", pipelineTS, "https://gl/p/202"))
		},
		"/api/v4/projects/20/jobs": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glJob{jobResponse("test", jobTS), jobResponse("test", jobTS)})
		},
		"/api/v4/projects/20/runners": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glRunner{})
		},
	})
	defer srv.Close()

	checker := newGitLabChecker(srv)
	apps := []config.App{
		{Name: "pfm", GitLabProjectID: 10},
		{Name: "crm", GitLabProjectID: 20},
	}

	results, err := checker.Check(context.Background(), apps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	pfm := results[0]
	if pfm.AppName != "pfm" {
		t.Errorf("expected AppName=pfm, got %q", pfm.AppName)
	}
	if pfm.Error != "" {
		t.Errorf("unexpected error for pfm: %s", pfm.Error)
	}
	if pfm.LastPipeline == nil {
		t.Fatal("expected LastPipeline for pfm, got nil")
	}
	if pfm.LastPipeline.Status != "success" {
		t.Errorf("expected pipeline status=success, got %q", pfm.LastPipeline.Status)
	}
	if pfm.FailedJobsByStage["build"] != 1 {
		t.Errorf("expected 1 failed build job, got %d", pfm.FailedJobsByStage["build"])
	}
	if len(pfm.Runners) != 1 || pfm.Runners[0].Description != "runner-a" {
		t.Errorf("unexpected runners for pfm: %+v", pfm.Runners)
	}
	if pfm.Runners[0].Stale {
		t.Error("runner-a should not be stale (contacted 5m ago, threshold 10m)")
	}

	crm := results[1]
	if crm.LastPipeline.Status != "failed" {
		t.Errorf("expected crm pipeline status=failed, got %q", crm.LastPipeline.Status)
	}
	if crm.FailedJobsByStage["test"] != 2 {
		t.Errorf("expected 2 failed test jobs for crm, got %d", crm.FailedJobsByStage["test"])
	}
}

// TestGitLabChecker_Check_noPipelines verifies that LastPipeline is nil when
// the project has no pipelines yet.
func TestGitLabChecker_Check_noPipelines(t *testing.T) {
	srv := glMux(map[string]http.HandlerFunc{
		"/api/v4/projects/10/pipelines": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glPipeline{})
		},
		"/api/v4/projects/10/jobs": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glJob{})
		},
		"/api/v4/projects/10/runners": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glRunner{})
		},
	})
	defer srv.Close()

	results, err := newGitLabChecker(srv).Check(context.Background(), []config.App{
		{Name: "pfm", GitLabProjectID: 10},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results[0].LastPipeline != nil {
		t.Errorf("expected nil LastPipeline, got %+v", results[0].LastPipeline)
	}
}

// TestGitLabChecker_Check_failedJobsFilteredByWindow verifies that jobs older
// than FailureWindow are excluded from the failed-jobs count.
func TestGitLabChecker_Check_failedJobsFilteredByWindow(t *testing.T) {
	// One job within window (1h ago), one outside (25h ago).
	within := fixedTime.Add(-1 * time.Hour).Format(time.RFC3339)
	outside := fixedTime.Add(-25 * time.Hour).Format(time.RFC3339)

	srv := glMux(map[string]http.HandlerFunc{
		"/api/v4/projects/10/pipelines": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, pipelineResponse(1, "failed", "main", within, ""))
		},
		"/api/v4/projects/10/jobs": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glJob{
				jobResponse("build", within),
				jobResponse("build", outside),
			})
		},
		"/api/v4/projects/10/runners": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glRunner{})
		},
	})
	defer srv.Close()

	results, err := newGitLabChecker(srv).Check(context.Background(), []config.App{
		{Name: "pfm", GitLabProjectID: 10},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := results[0].FailedJobsByStage["build"]
	if got != 1 {
		t.Errorf("expected 1 in-window failed build job, got %d", got)
	}
}

// TestGitLabChecker_Check_staleRunner verifies that a runner whose contacted_at
// exceeds the staleness threshold is marked Stale.
func TestGitLabChecker_Check_staleRunner(t *testing.T) {
	pipelineTS := fixedTime.Add(-1 * time.Hour).Format(time.RFC3339)
	// contacted_at = 15 minutes ago; threshold = 10 minutes → stale.
	staleTS := fixedTime.Add(-15 * time.Minute).Format(time.RFC3339)
	freshTS := fixedTime.Add(-5 * time.Minute).Format(time.RFC3339)

	srv := glMux(map[string]http.HandlerFunc{
		"/api/v4/projects/10/pipelines": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, pipelineResponse(1, "success", "main", pipelineTS, ""))
		},
		"/api/v4/projects/10/jobs": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glJob{})
		},
		"/api/v4/projects/10/runners": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glRunner{
				runnerResponse(1, "stale-runner", true, staleTS),
				runnerResponse(2, "fresh-runner", true, freshTS),
			})
		},
	})
	defer srv.Close()

	results, err := newGitLabChecker(srv).Check(context.Background(), []config.App{
		{Name: "pfm", GitLabProjectID: 10},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	runners := results[0].Runners
	if len(runners) != 2 {
		t.Fatalf("expected 2 runners, got %d", len(runners))
	}

	// runners slice preserves order from the API response.
	if !runners[0].Stale {
		t.Errorf("stale-runner should be marked Stale")
	}
	if runners[1].Stale {
		t.Errorf("fresh-runner should not be marked Stale")
	}
}

// TestGitLabChecker_Check_apiErrorCapturedPerApp verifies that an API error for
// one app is captured in its Error field while the other app succeeds.
func TestGitLabChecker_Check_apiErrorCapturedPerApp(t *testing.T) {
	pipelineTS := fixedTime.Add(-1 * time.Hour).Format(time.RFC3339)

	srv := glMux(map[string]http.HandlerFunc{
		// Project 10 → 404 on pipelines.
		"/api/v4/projects/10/pipelines": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"message":"404 Project Not Found"}`, http.StatusNotFound)
		},
		// Project 20 → fully healthy.
		"/api/v4/projects/20/pipelines": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, pipelineResponse(202, "success", "main", pipelineTS, ""))
		},
		"/api/v4/projects/20/jobs": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glJob{})
		},
		"/api/v4/projects/20/runners": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glRunner{})
		},
	})
	defer srv.Close()

	results, err := newGitLabChecker(srv).Check(context.Background(), []config.App{
		{Name: "pfm", GitLabProjectID: 10},
		{Name: "crm", GitLabProjectID: 20},
	})
	if err != nil {
		t.Fatalf("unexpected top-level error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	pfm := results[0]
	if pfm.Error == "" {
		t.Error("expected Error for pfm (404), got none")
	}
	if !strings.Contains(pfm.Error, "404") {
		t.Errorf("expected 404 in pfm error, got: %s", pfm.Error)
	}

	crm := results[1]
	if crm.Error != "" {
		t.Errorf("expected no error for crm, got: %s", crm.Error)
	}
	if crm.LastPipeline == nil || crm.LastPipeline.Status != "success" {
		t.Errorf("expected crm pipeline=success, got %+v", crm.LastPipeline)
	}
}

// TestGitLabChecker_Check_emptyApps verifies that an empty app list returns
// an empty slice without error.
func TestGitLabChecker_Check_emptyApps(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()

	results, err := newGitLabChecker(srv).Check(context.Background(), nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

// TestGitLabChecker_Check_multipleFailedStages verifies that failed jobs are
// correctly grouped by stage when multiple stages have failures.
func TestGitLabChecker_Check_multipleFailedStages(t *testing.T) {
	pipelineTS := fixedTime.Add(-1 * time.Hour).Format(time.RFC3339)
	jobTS := fixedTime.Add(-30 * time.Minute).Format(time.RFC3339)

	srv := glMux(map[string]http.HandlerFunc{
		"/api/v4/projects/10/pipelines": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, pipelineResponse(1, "failed", "main", pipelineTS, ""))
		},
		"/api/v4/projects/10/jobs": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glJob{
				jobResponse("build", jobTS),
				jobResponse("build", jobTS),
				jobResponse("test", jobTS),
				jobResponse("deploy", jobTS),
				jobResponse("deploy", jobTS),
				jobResponse("deploy", jobTS),
			})
		},
		"/api/v4/projects/10/runners": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glRunner{})
		},
	})
	defer srv.Close()

	results, err := newGitLabChecker(srv).Check(context.Background(), []config.App{
		{Name: "pfm", GitLabProjectID: 10},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	byStage := results[0].FailedJobsByStage
	cases := map[string]int{"build": 2, "test": 1, "deploy": 3}
	for stage, want := range cases {
		if got := byStage[stage]; got != want {
			t.Errorf("stage %q: expected %d, got %d", stage, want, got)
		}
	}
}

// TestGitLabChecker_Check_authHeader verifies that the PRIVATE-TOKEN header is
// sent on every request.
func TestGitLabChecker_Check_authHeader(t *testing.T) {
	pipelineTS := fixedTime.Add(-1 * time.Hour).Format(time.RFC3339)
	var missing []string

	check := func(name string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("PRIVATE-TOKEN") != "test-token" {
				missing = append(missing, name)
			}
			switch name {
			case "pipelines":
				writeJSON(w, pipelineResponse(1, "success", "main", pipelineTS, ""))
			case "jobs":
				writeJSON(w, []glJob{})
			case "runners":
				writeJSON(w, []glRunner{})
			}
		}
	}

	srv := glMux(map[string]http.HandlerFunc{
		"/api/v4/projects/10/pipelines": check("pipelines"),
		"/api/v4/projects/10/jobs":      check("jobs"),
		"/api/v4/projects/10/runners":   check("runners"),
	})
	defer srv.Close()

	_, err := newGitLabChecker(srv).Check(context.Background(), []config.App{
		{Name: "pfm", GitLabProjectID: 10},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(missing) > 0 {
		t.Errorf("PRIVATE-TOKEN header missing on: %v", missing)
	}
}
