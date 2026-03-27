package checker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func pipelineJobResponse(name, stage, status string, duration float64) glJob {
	return glJob{Name: name, Stage: stage, Status: status, Duration: duration}
}

// appRepos builds a single-repo AppRepos for convenience in tests.
func appRepos(name string, projectID int) AppRepos {
	return AppRepos{AppName: name, Repos: []RepoInfo{{Name: name, ID: projectID}}}
}

// ---- tests ----

// TestGitLabChecker_Check_success exercises the full happy path with two apps
// (each with a single repo).
func TestGitLabChecker_Check_success(t *testing.T) {
	pipelineTS := fixedTime.Add(-2 * time.Hour).Format(time.RFC3339)
	jobTS := fixedTime.Add(-1 * time.Hour).Format(time.RFC3339)

	srv := glMux(map[string]http.HandlerFunc{
		"/api/v4/projects/10/pipelines": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, pipelineResponse(101, "success", "main", pipelineTS, "https://gl/p/101"))
		},
		"/api/v4/projects/10/pipelines/101/jobs": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glJob{
				pipelineJobResponse("test-unit", "test", "success", 45.2),
				pipelineJobResponse("build-image", "build", "success", 78.5),
			})
		},
		"/api/v4/projects/10/jobs": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glJob{jobResponse("build", jobTS)})
		},
		"/api/v4/projects/20/pipelines": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, pipelineResponse(202, "failed", "main", pipelineTS, "https://gl/p/202"))
		},
		"/api/v4/projects/20/pipelines/202/jobs": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glJob{
				pipelineJobResponse("test-unit", "test", "failed", 12.1),
			})
		},
		"/api/v4/projects/20/jobs": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glJob{jobResponse("test", jobTS), jobResponse("test", jobTS)})
		},
	})
	defer srv.Close()

	c := newGitLabChecker(srv)
	apps := []AppRepos{
		appRepos("pfm", 10),
		appRepos("crm", 20),
	}

	results, err := c.Check(context.Background(), apps)
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
		t.Errorf("unexpected app-level error for pfm: %s", pfm.Error)
	}
	if len(pfm.Repos) != 1 {
		t.Fatalf("expected 1 repo for pfm, got %d", len(pfm.Repos))
	}
	repo := pfm.Repos[0]
	if repo.Error != "" {
		t.Errorf("unexpected repo error for pfm: %s", repo.Error)
	}
	if repo.LastPipeline == nil {
		t.Fatal("expected LastPipeline for pfm, got nil")
	}
	if repo.LastPipeline.Status != "success" {
		t.Errorf("expected pipeline status=success, got %q", repo.LastPipeline.Status)
	}
	if repo.FailedJobsByStage["build"] != 1 {
		t.Errorf("expected 1 failed build job, got %d", repo.FailedJobsByStage["build"])
	}
	if len(repo.LastPipelineJobs) != 2 {
		t.Errorf("expected 2 pipeline jobs for pfm, got %d", len(repo.LastPipelineJobs))
	} else if repo.LastPipelineJobs[1].Name != "build-image" {
		t.Errorf("expected second job name=build-image, got %q", repo.LastPipelineJobs[1].Name)
	}

	crm := results[1]
	if len(crm.Repos) != 1 {
		t.Fatalf("expected 1 repo for crm, got %d", len(crm.Repos))
	}
	crmRepo := crm.Repos[0]
	if crmRepo.LastPipeline.Status != "failed" {
		t.Errorf("expected crm pipeline status=failed, got %q", crmRepo.LastPipeline.Status)
	}
	if crmRepo.FailedJobsByStage["test"] != 2 {
		t.Errorf("expected 2 failed test jobs for crm, got %d", crmRepo.FailedJobsByStage["test"])
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
	})
	defer srv.Close()

	results, err := newGitLabChecker(srv).Check(context.Background(), []AppRepos{appRepos("pfm", 10)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if results[0].Repos[0].LastPipeline != nil {
		t.Errorf("expected nil LastPipeline, got %+v", results[0].Repos[0].LastPipeline)
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
		"/api/v4/projects/10/pipelines/1/jobs": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glJob{})
		},
		"/api/v4/projects/10/jobs": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glJob{
				jobResponse("build", within),
				jobResponse("build", outside),
			})
		},
	})
	defer srv.Close()

	results, err := newGitLabChecker(srv).Check(context.Background(), []AppRepos{appRepos("pfm", 10)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := results[0].Repos[0].FailedJobsByStage["build"]
	if got != 1 {
		t.Errorf("expected 1 in-window failed build job, got %d", got)
	}
}

// TestGitLabChecker_Check_apiErrorCapturedPerRepo verifies that an API error for
// one repo is captured in its Error field while the next app succeeds.
func TestGitLabChecker_Check_apiErrorCapturedPerRepo(t *testing.T) {
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
		"/api/v4/projects/20/pipelines/202/jobs": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glJob{})
		},
		"/api/v4/projects/20/jobs": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glJob{})
		},
	})
	defer srv.Close()

	results, err := newGitLabChecker(srv).Check(context.Background(), []AppRepos{
		appRepos("pfm", 10),
		appRepos("crm", 20),
	})
	if err != nil {
		t.Fatalf("unexpected top-level error: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}

	// pfm's repo should have the error.
	pfmRepo := results[0].Repos[0]
	if pfmRepo.Error == "" {
		t.Error("expected Error for pfm repo (404), got none")
	}
	if !strings.Contains(pfmRepo.Error, "404") {
		t.Errorf("expected 404 in pfm repo error, got: %s", pfmRepo.Error)
	}

	// App-level error should be empty (only repo-level errors for pipeline failures).
	if results[0].Error != "" {
		t.Errorf("expected no app-level error for pfm, got: %s", results[0].Error)
	}

	crm := results[1]
	if crm.Repos[0].Error != "" {
		t.Errorf("expected no error for crm repo, got: %s", crm.Repos[0].Error)
	}
	if crm.Repos[0].LastPipeline == nil || crm.Repos[0].LastPipeline.Status != "success" {
		t.Errorf("expected crm pipeline=success, got %+v", crm.Repos[0].LastPipeline)
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
		"/api/v4/projects/10/pipelines/1/jobs": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glJob{})
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
	})
	defer srv.Close()

	results, err := newGitLabChecker(srv).Check(context.Background(), []AppRepos{appRepos("pfm", 10)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	byStage := results[0].Repos[0].FailedJobsByStage
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
			case "pipeline-jobs", "jobs":
				writeJSON(w, []glJob{})
			}
		}
	}

	srv := glMux(map[string]http.HandlerFunc{
		"/api/v4/projects/10/pipelines":       check("pipelines"),
		"/api/v4/projects/10/pipelines/1/jobs": check("pipeline-jobs"),
		"/api/v4/projects/10/jobs":             check("jobs"),
	})
	defer srv.Close()

	_, err := newGitLabChecker(srv).Check(context.Background(), []AppRepos{appRepos("pfm", 10)})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(missing) > 0 {
		t.Errorf("PRIVATE-TOKEN header missing on: %v", missing)
	}
}

// TestGitLabChecker_Check_multipleReposPerApp verifies that two repos in the
// same app are each checked independently.
func TestGitLabChecker_Check_multipleReposPerApp(t *testing.T) {
	pipelineTS := fixedTime.Add(-1 * time.Hour).Format(time.RFC3339)

	srv := glMux(map[string]http.HandlerFunc{
		"/api/v4/projects/10/pipelines": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, pipelineResponse(101, "success", "main", pipelineTS, ""))
		},
		"/api/v4/projects/10/pipelines/101/jobs": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glJob{pipelineJobResponse("lint", "lint", "success", 5.0)})
		},
		"/api/v4/projects/10/jobs": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glJob{})
		},
		"/api/v4/projects/20/pipelines": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, pipelineResponse(201, "failed", "main", pipelineTS, ""))
		},
		"/api/v4/projects/20/pipelines/201/jobs": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glJob{pipelineJobResponse("test", "test", "failed", 30.0)})
		},
		"/api/v4/projects/20/jobs": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glJob{})
		},
	})
	defer srv.Close()

	apps := []AppRepos{
		{
			AppName: "pfm",
			Repos: []RepoInfo{
				{Name: "pfm-api", ID: 10},
				{Name: "pfm-service", ID: 20},
			},
		},
	}

	results, err := newGitLabChecker(srv).Check(context.Background(), apps)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 app result, got %d", len(results))
	}
	if len(results[0].Repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(results[0].Repos))
	}

	api := results[0].Repos[0]
	if api.RepoName != "pfm-api" {
		t.Errorf("expected RepoName=pfm-api, got %q", api.RepoName)
	}
	if api.LastPipeline.Status != "success" {
		t.Errorf("expected pfm-api pipeline=success, got %q", api.LastPipeline.Status)
	}

	svc := results[0].Repos[1]
	if svc.RepoName != "pfm-service" {
		t.Errorf("expected RepoName=pfm-service, got %q", svc.RepoName)
	}
	if svc.LastPipeline.Status != "failed" {
		t.Errorf("expected pfm-service pipeline=failed, got %q", svc.LastPipeline.Status)
	}
}
