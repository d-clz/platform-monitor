package reporter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"platform-monitor/internal/checker"
	"platform-monitor/internal/evaluator"
)

// ---- helpers ----

func readJobMetrics(t *testing.T, dir string) []JobMetricEntry {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "job-metrics.json"))
	if err != nil {
		t.Fatalf("reading job-metrics.json: %v", err)
	}
	var entries []JobMetricEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("parsing job-metrics.json: %v", err)
	}
	return entries
}

func readJobMetricsState(t *testing.T, dir string) jobMetricsState {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "job-metrics-state.json"))
	if err != nil {
		t.Fatalf("reading job-metrics-state.json: %v", err)
	}
	var state jobMetricsState
	if err := json.Unmarshal(data, &state); err != nil {
		t.Fatalf("parsing job-metrics-state.json: %v", err)
	}
	return state
}

func resultsWithPipeline(ts time.Time, appName string, pipelineID int, jobs ...checker.JobInfo) evaluator.Results {
	return evaluator.Results{
		Timestamp: ts,
		Apps: []evaluator.AppResult{
			{
				Name:  appName,
				Level: evaluator.LevelOK,
				GitLab: &evaluator.GitLabSummary{
					Repos: []evaluator.RepoSummary{
						{
							RepoName: appName,
							LastPipeline: &checker.PipelineInfo{
								ID:     pipelineID,
								Status: "success",
							},
							LastPipelineJobs: jobs,
						},
					},
				},
			},
		},
		TotalApps: 1,
		OKCount:   1,
	}
}

// ---- appendJobMetrics tests ----

// TestAppendJobMetrics_createsFilesForNewPipeline verifies that a new pipeline
// creates job-metrics.json and job-metrics-state.json.
func TestAppendJobMetrics_createsFilesForNewPipeline(t *testing.T) {
	dir := t.TempDir()
	rep := &Reporter{DataDir: dir}

	results := resultsWithPipeline(fixedTime, "pfm", 101,
		checker.JobInfo{Name: "lint", Stage: "lint", Status: "success", Duration: 15.0},
		checker.JobInfo{Name: "test", Stage: "test", Status: "success", Duration: 120.0},
	)

	if err := rep.appendJobMetrics(results); err != nil {
		t.Fatalf("appendJobMetrics: %v", err)
	}

	entries := readJobMetrics(t, dir)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}

	state := readJobMetricsState(t, dir)
	if state["pfm/pfm"] != 101 {
		t.Errorf("expected state[pfm/pfm]=101, got %d", state["pfm/pfm"])
	}
}

// TestAppendJobMetrics_skipsSamePipeline verifies that if the pipeline ID has not
// changed since the last run, no new entries are added.
func TestAppendJobMetrics_skipsSamePipeline(t *testing.T) {
	dir := t.TempDir()
	rep := &Reporter{DataDir: dir}

	results := resultsWithPipeline(fixedTime, "pfm", 101,
		checker.JobInfo{Name: "lint", Stage: "lint", Status: "success", Duration: 15.0},
	)

	// First run — new pipeline, should write.
	rep.appendJobMetrics(results)

	// Second run — same pipeline ID, should be a no-op.
	results.Timestamp = fixedTime.Add(15 * time.Minute)
	if err := rep.appendJobMetrics(results); err != nil {
		t.Fatalf("second appendJobMetrics: %v", err)
	}

	entries := readJobMetrics(t, dir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry (no double-count), got %d", len(entries))
	}
	if entries[0].Runs != 1 {
		t.Errorf("expected Runs=1, got %d", entries[0].Runs)
	}
}

// TestAppendJobMetrics_upsertsSameWeek verifies that two different pipelines in the
// same week accumulate into a single (week, app, job) aggregate.
func TestAppendJobMetrics_upsertsSameWeek(t *testing.T) {
	dir := t.TempDir()
	rep := &Reporter{DataDir: dir}

	// Pipeline 101 in week 13.
	ts1 := time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC) // Wednesday W13
	rep.appendJobMetrics(resultsWithPipeline(ts1, "pfm", 101,
		checker.JobInfo{Name: "lint", Stage: "lint", Status: "success", Duration: 10.0},
	))

	// Pipeline 102 in the same week.
	ts2 := ts1.Add(4 * time.Hour)
	rep.appendJobMetrics(resultsWithPipeline(ts2, "pfm", 102,
		checker.JobInfo{Name: "lint", Stage: "lint", Status: "failed", Duration: 8.0},
	))

	entries := readJobMetrics(t, dir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 aggregated entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Runs != 2 {
		t.Errorf("expected Runs=2, got %d", e.Runs)
	}
	if e.Failures != 1 {
		t.Errorf("expected Failures=1, got %d", e.Failures)
	}
	if e.TotalDuration != 18.0 {
		t.Errorf("expected TotalDuration=18.0, got %f", e.TotalDuration)
	}
}

// TestAppendJobMetrics_canceledCountsAsFailure verifies that "canceled" status
// increments Failures.
func TestAppendJobMetrics_canceledCountsAsFailure(t *testing.T) {
	dir := t.TempDir()
	rep := &Reporter{DataDir: dir}

	rep.appendJobMetrics(resultsWithPipeline(fixedTime, "pfm", 101,
		checker.JobInfo{Name: "deploy", Stage: "deploy", Status: "canceled", Duration: 5.0},
	))

	entries := readJobMetrics(t, dir)
	if entries[0].Failures != 1 {
		t.Errorf("expected Failures=1 for canceled, got %d", entries[0].Failures)
	}
}

// TestAppendJobMetrics_noGitLab verifies that apps without GitLab data are skipped.
func TestAppendJobMetrics_noGitLab(t *testing.T) {
	dir := t.TempDir()
	rep := &Reporter{DataDir: dir}

	results := evaluator.Results{
		Timestamp: fixedTime,
		Apps: []evaluator.AppResult{
			{Name: "pfm", Level: evaluator.LevelOK, GitLab: nil},
		},
		TotalApps: 1,
		OKCount:   1,
	}

	if err := rep.appendJobMetrics(results); err != nil {
		t.Fatalf("appendJobMetrics: %v", err)
	}

	// No files should be created (updated=false → early return).
	if _, err := os.Stat(filepath.Join(dir, "job-metrics.json")); !os.IsNotExist(err) {
		t.Error("job-metrics.json should not exist when no GitLab data")
	}
}

// TestAppendJobMetrics_multipleApps verifies that two apps each contribute their
// own entries and state is tracked independently per app/repo key.
func TestAppendJobMetrics_multipleApps(t *testing.T) {
	dir := t.TempDir()
	rep := &Reporter{DataDir: dir}

	results := evaluator.Results{
		Timestamp: fixedTime,
		Apps: []evaluator.AppResult{
			{
				Name:  "pfm",
				Level: evaluator.LevelOK,
				GitLab: &evaluator.GitLabSummary{
					Repos: []evaluator.RepoSummary{
						{
							RepoName:         "pfm",
							LastPipeline:     &checker.PipelineInfo{ID: 101, Status: "success"},
							LastPipelineJobs: []checker.JobInfo{{Name: "lint", Stage: "lint", Status: "success", Duration: 10}},
						},
					},
				},
			},
			{
				Name:  "crm",
				Level: evaluator.LevelOK,
				GitLab: &evaluator.GitLabSummary{
					Repos: []evaluator.RepoSummary{
						{
							RepoName:         "crm",
							LastPipeline:     &checker.PipelineInfo{ID: 201, Status: "success"},
							LastPipelineJobs: []checker.JobInfo{{Name: "test", Stage: "test", Status: "failed", Duration: 30}},
						},
					},
				},
			},
		},
		TotalApps: 2,
		OKCount:   2,
	}

	if err := rep.appendJobMetrics(results); err != nil {
		t.Fatalf("appendJobMetrics: %v", err)
	}

	entries := readJobMetrics(t, dir)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (one per app/repo), got %d", len(entries))
	}

	state := readJobMetricsState(t, dir)
	if state["pfm/pfm"] != 101 || state["crm/crm"] != 201 {
		t.Errorf("unexpected state: %v", state)
	}
}

// ---- upsertJobEntry tests ----

func TestUpsertJobEntry_newEntry(t *testing.T) {
	entries := upsertJobEntry(nil, "2026-W13", "pfm", "pfm", "lint", "lint", "success", 12.5)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Runs != 1 || e.Failures != 0 || e.TotalDuration != 12.5 {
		t.Errorf("unexpected entry: %+v", e)
	}
	if e.Week != "2026-W13" || e.App != "pfm" || e.Repo != "pfm" || e.Job != "lint" || e.Stage != "lint" {
		t.Errorf("unexpected fields: %+v", e)
	}
}

func TestUpsertJobEntry_existingEntry(t *testing.T) {
	existing := []JobMetricEntry{
		{Week: "2026-W13", App: "pfm", Repo: "pfm", Job: "lint", Stage: "lint", Runs: 1, Failures: 0, TotalDuration: 10.0},
	}
	entries := upsertJobEntry(existing, "2026-W13", "pfm", "pfm", "lint", "lint", "failed", 8.0)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Runs != 2 {
		t.Errorf("expected Runs=2, got %d", e.Runs)
	}
	if e.Failures != 1 {
		t.Errorf("expected Failures=1, got %d", e.Failures)
	}
	if e.TotalDuration != 18.0 {
		t.Errorf("expected TotalDuration=18.0, got %f", e.TotalDuration)
	}
}

func TestUpsertJobEntry_differentWeekCreatesNew(t *testing.T) {
	existing := []JobMetricEntry{
		{Week: "2026-W12", App: "pfm", Repo: "pfm", Job: "lint", Stage: "lint", Runs: 1, TotalDuration: 10.0},
	}
	entries := upsertJobEntry(existing, "2026-W13", "pfm", "pfm", "lint", "lint", "success", 12.0)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries for different weeks, got %d", len(entries))
	}
}

func TestUpsertJobEntry_canceledIsFailure(t *testing.T) {
	entries := upsertJobEntry(nil, "2026-W13", "pfm", "pfm", "deploy", "deploy", "canceled", 5.0)
	if entries[0].Failures != 1 {
		t.Errorf("expected Failures=1 for canceled, got %d", entries[0].Failures)
	}
}

func TestUpsertJobEntry_successIsNotFailure(t *testing.T) {
	entries := upsertJobEntry(nil, "2026-W13", "pfm", "pfm", "lint", "lint", "success", 10.0)
	if entries[0].Failures != 0 {
		t.Errorf("expected Failures=0 for success, got %d", entries[0].Failures)
	}
}

// ---- capJobEntries tests ----

func makeJobEntries(weeks ...string) []JobMetricEntry {
	var entries []JobMetricEntry
	for _, w := range weeks {
		entries = append(entries, JobMetricEntry{Week: w, App: "pfm", Job: "lint"})
	}
	return entries
}

func TestCapJobEntries_belowCap(t *testing.T) {
	entries := makeJobEntries("2026-W11", "2026-W12", "2026-W13")
	out := capJobEntries(entries, 5)
	if len(out) != 3 {
		t.Errorf("expected all 3 entries kept, got %d", len(out))
	}
}

func TestCapJobEntries_exactCap(t *testing.T) {
	entries := makeJobEntries("2026-W11", "2026-W12", "2026-W13")
	out := capJobEntries(entries, 3)
	if len(out) != 3 {
		t.Errorf("expected 3 entries, got %d", len(out))
	}
}

func TestCapJobEntries_dropsOldestWeeks(t *testing.T) {
	entries := makeJobEntries("2026-W10", "2026-W11", "2026-W12", "2026-W13")
	out := capJobEntries(entries, 2)

	weeksSeen := map[string]bool{}
	for _, e := range out {
		weeksSeen[e.Week] = true
	}
	if weeksSeen["2026-W10"] || weeksSeen["2026-W11"] {
		t.Errorf("old weeks should have been dropped, got: %v", weeksSeen)
	}
	if !weeksSeen["2026-W12"] || !weeksSeen["2026-W13"] {
		t.Errorf("recent weeks should be kept, got: %v", weeksSeen)
	}
}

func TestCapJobEntries_multipleEntriesPerWeek(t *testing.T) {
	// Two jobs in W12, one job in W13 — cap at 1 week → only W13 kept.
	entries := []JobMetricEntry{
		{Week: "2026-W12", App: "pfm", Job: "lint"},
		{Week: "2026-W12", App: "pfm", Job: "test"},
		{Week: "2026-W13", App: "pfm", Job: "lint"},
	}
	out := capJobEntries(entries, 1)
	if len(out) != 1 {
		t.Fatalf("expected 1 entry (only W13/lint), got %d", len(out))
	}
	if out[0].Week != "2026-W13" {
		t.Errorf("expected W13, got %q", out[0].Week)
	}
}

// ---- isoWeek tests ----

func TestIsoWeek(t *testing.T) {
	cases := []struct {
		t    time.Time
		want string
	}{
		{time.Date(2026, 3, 25, 0, 0, 0, 0, time.UTC), "2026-W13"}, // Wednesday week 13
		{time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), "2026-W01"},  // New Year's Day 2026
		{time.Date(2026, 12, 28, 0, 0, 0, 0, time.UTC), "2026-W53"}, // last week of 2026
	}
	for _, tc := range cases {
		if got := isoWeek(tc.t); got != tc.want {
			t.Errorf("isoWeek(%v) = %q, want %q", tc.t, got, tc.want)
		}
	}
}
