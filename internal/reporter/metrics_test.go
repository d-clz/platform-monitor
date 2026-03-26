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

func readMetrics(t *testing.T, dir string) []MetricEntry {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "metrics.json"))
	if err != nil {
		t.Fatalf("reading metrics.json: %v", err)
	}
	var entries []MetricEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("parsing metrics.json: %v", err)
	}
	return entries
}

func readMetricsIndex(t *testing.T, dir string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "metrics-index.json"))
	if err != nil {
		t.Fatalf("reading metrics-index.json: %v", err)
	}
	var index []string
	if err := json.Unmarshal(data, &index); err != nil {
		t.Fatalf("parsing metrics-index.json: %v", err)
	}
	return index
}

func readColdMetrics(t *testing.T, dir, monthKey string) []MetricEntry {
	t.Helper()
	fname := "metrics-" + monthKey + ".json"
	data, err := os.ReadFile(filepath.Join(dir, fname))
	if err != nil {
		t.Fatalf("reading %s: %v", fname, err)
	}
	var entries []MetricEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		t.Fatalf("parsing %s: %v", fname, err)
	}
	return entries
}

func simpleResults(ts time.Time, apps ...string) evaluator.Results {
	r := evaluator.Results{Timestamp: ts, TotalApps: len(apps)}
	for _, name := range apps {
		r.Apps = append(r.Apps, evaluator.AppResult{Name: name, Level: evaluator.LevelOK})
		r.OKCount++
	}
	return r
}

// ---- appendMetrics tests ----

// TestAppendMetrics_createsHotFile verifies that the first call creates metrics.json
// with one entry per app.
func TestAppendMetrics_createsHotFile(t *testing.T) {
	dir := t.TempDir()
	rep := &Reporter{DataDir: dir}

	results := simpleResults(fixedTime, "pfm", "crm")
	if err := rep.appendMetrics(results); err != nil {
		t.Fatalf("appendMetrics: %v", err)
	}

	entries := readMetrics(t, dir)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	names := map[string]bool{}
	for _, e := range entries {
		names[e.App] = true
		if e.Level != "ok" {
			t.Errorf("expected level=ok for %s, got %q", e.App, e.Level)
		}
	}
	if !names["pfm"] || !names["crm"] {
		t.Errorf("missing expected apps in metrics: %v", names)
	}
}

// TestAppendMetrics_accumulates verifies that successive calls append new entries.
func TestAppendMetrics_accumulates(t *testing.T) {
	dir := t.TempDir()
	rep := &Reporter{DataDir: dir}

	rep.appendMetrics(simpleResults(fixedTime, "pfm"))
	rep.appendMetrics(simpleResults(fixedTime.Add(15*time.Minute), "pfm"))

	entries := readMetrics(t, dir)
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
}

// TestAppendMetrics_capturesGitLabFields verifies that pipeline status, duration,
// failed jobs, and runner counts are stored correctly.
func TestAppendMetrics_capturesGitLabFields(t *testing.T) {
	dir := t.TempDir()
	rep := &Reporter{DataDir: dir}

	results := evaluator.Results{
		Timestamp: fixedTime,
		Apps: []evaluator.AppResult{
			{
				Name:  "pfm",
				Level: evaluator.LevelWarning,
				GitLab: &evaluator.GitLabSummary{
					LastPipeline: &checker.PipelineInfo{
						ID:       101,
						Status:   "failed",
						Duration: 300,
					},
					FailedJobsByStage: map[string]int{"test": 2, "deploy": 1},
					RunnerCount:       5,
					StaleRunnerCount:  2,
				},
			},
		},
		TotalApps:    1,
		WarningCount: 1,
	}

	if err := rep.appendMetrics(results); err != nil {
		t.Fatalf("appendMetrics: %v", err)
	}

	entries := readMetrics(t, dir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.PipelineStatus != "failed" {
		t.Errorf("PipelineStatus=%q, want failed", e.PipelineStatus)
	}
	if e.PipelineDuration != 300 {
		t.Errorf("PipelineDuration=%d, want 300", e.PipelineDuration)
	}
	if e.FailedJobs != 3 {
		t.Errorf("FailedJobs=%d, want 3 (2+1)", e.FailedJobs)
	}
	if e.RunnersOnline != 3 {
		t.Errorf("RunnersOnline=%d, want 3 (5-2)", e.RunnersOnline)
	}
}

// TestAppendMetrics_rotatesOldEntriesToCold verifies that entries older than
// hotWindowDays are moved to a monthly cold file and the index is updated.
func TestAppendMetrics_rotatesOldEntriesToCold(t *testing.T) {
	dir := t.TempDir()
	rep := &Reporter{DataDir: dir}

	// Seed an entry that is 61 days old (outside the 60-day window).
	oldTs := fixedTime.AddDate(0, 0, -(hotWindowDays + 1))
	seed := []MetricEntry{
		{Ts: oldTs, App: "pfm", Level: "ok"},
	}
	seedData, _ := json.MarshalIndent(seed, "", "  ")
	os.WriteFile(filepath.Join(dir, "metrics.json"), seedData, 0o644)

	// New run with current timestamp.
	if err := rep.appendMetrics(simpleResults(fixedTime, "pfm")); err != nil {
		t.Fatalf("appendMetrics: %v", err)
	}

	// Hot file should only contain the new entry.
	hot := readMetrics(t, dir)
	if len(hot) != 1 {
		t.Fatalf("expected 1 hot entry, got %d", len(hot))
	}
	if !hot[0].Ts.Equal(fixedTime) {
		t.Errorf("hot entry ts=%v, want %v", hot[0].Ts, fixedTime)
	}

	// Cold file for the old month should exist.
	monthKey := oldTs.UTC().Format("2006-01")
	cold := readColdMetrics(t, dir, monthKey)
	if len(cold) != 1 {
		t.Fatalf("expected 1 cold entry, got %d", len(cold))
	}

	// Index should contain the cold file.
	index := readMetricsIndex(t, dir)
	if len(index) != 1 {
		t.Fatalf("expected 1 index entry, got %d", len(index))
	}
	if index[0] != "metrics-"+monthKey+".json" {
		t.Errorf("index entry=%q, want metrics-%s.json", index[0], monthKey)
	}
}

// TestAppendMetrics_coldRotationIdempotent verifies that rotating the same old
// entries twice does not create duplicates in the cold file.
func TestAppendMetrics_coldRotationIdempotent(t *testing.T) {
	dir := t.TempDir()
	rep := &Reporter{DataDir: dir}

	oldTs := fixedTime.AddDate(0, 0, -(hotWindowDays + 1))
	seed := []MetricEntry{
		{Ts: oldTs, App: "pfm", Level: "ok"},
	}
	seedData, _ := json.MarshalIndent(seed, "", "  ")
	monthKey := oldTs.UTC().Format("2006-01")

	// Pre-write the same entry into the cold file (simulates crash before hot rewrite).
	coldData, _ := json.MarshalIndent(seed, "", "  ")
	os.WriteFile(filepath.Join(dir, "metrics-"+monthKey+".json"), coldData, 0o644)

	os.WriteFile(filepath.Join(dir, "metrics.json"), seedData, 0o644)

	if err := rep.appendMetrics(simpleResults(fixedTime, "pfm")); err != nil {
		t.Fatalf("appendMetrics: %v", err)
	}

	cold := readColdMetrics(t, dir, monthKey)
	if len(cold) != 1 {
		t.Errorf("expected 1 cold entry (deduped), got %d", len(cold))
	}
}

// ---- dedupMetrics tests ----

func TestDedupMetrics_removsDuplicates(t *testing.T) {
	ts := fixedTime
	entries := []MetricEntry{
		{Ts: ts, App: "pfm", Level: "ok"},
		{Ts: ts, App: "pfm", Level: "warning"}, // duplicate ts+app, last wins
		{Ts: ts, App: "crm", Level: "ok"},
	}
	out := dedupMetrics(entries)
	if len(out) != 2 {
		t.Fatalf("expected 2 entries after dedup, got %d", len(out))
	}
	for _, e := range out {
		if e.App == "pfm" && e.Level != "warning" {
			t.Errorf("expected last pfm entry (warning) to win, got %q", e.Level)
		}
	}
}

func TestDedupMetrics_noOp(t *testing.T) {
	ts := fixedTime
	entries := []MetricEntry{
		{Ts: ts, App: "pfm", Level: "ok"},
		{Ts: ts.Add(time.Minute), App: "pfm", Level: "ok"},
	}
	out := dedupMetrics(entries)
	if len(out) != 2 {
		t.Errorf("expected 2 distinct entries, got %d", len(out))
	}
}

func TestDedupMetrics_empty(t *testing.T) {
	out := dedupMetrics(nil)
	if len(out) != 0 {
		t.Errorf("expected empty, got %d", len(out))
	}
}

// ---- buildMetricEntries tests ----

func TestBuildMetricEntries_noGitLab(t *testing.T) {
	results := evaluator.Results{
		Timestamp: fixedTime,
		Apps: []evaluator.AppResult{
			{Name: "pfm", Level: evaluator.LevelOK, GitLab: nil},
		},
	}
	entries := buildMetricEntries(results)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	e := entries[0]
	if e.PipelineStatus != "" {
		t.Errorf("expected empty PipelineStatus, got %q", e.PipelineStatus)
	}
	if e.FailedJobs != 0 {
		t.Errorf("expected FailedJobs=0, got %d", e.FailedJobs)
	}
	if e.RunnersOnline != 0 {
		t.Errorf("expected RunnersOnline=0, got %d", e.RunnersOnline)
	}
}

func TestBuildMetricEntries_gitLabNoPipeline(t *testing.T) {
	results := evaluator.Results{
		Timestamp: fixedTime,
		Apps: []evaluator.AppResult{
			{
				Name:  "pfm",
				Level: evaluator.LevelOK,
				GitLab: &evaluator.GitLabSummary{
					LastPipeline: nil, // no pipeline yet
					RunnerCount:  3,
				},
			},
		},
	}
	entries := buildMetricEntries(results)
	e := entries[0]
	if e.PipelineStatus != "" {
		t.Errorf("expected empty PipelineStatus when no pipeline, got %q", e.PipelineStatus)
	}
	if e.RunnersOnline != 3 {
		t.Errorf("expected RunnersOnline=3, got %d", e.RunnersOnline)
	}
}

// ---- writeColdMetrics tests ----

func TestWriteColdMetrics_createsFileAndIndex(t *testing.T) {
	dir := t.TempDir()
	rep := &Reporter{DataDir: dir}

	ts := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	coldByMonth := map[string][]MetricEntry{
		"2026-01": {{Ts: ts, App: "pfm", Level: "ok"}},
	}

	if err := rep.writeColdMetrics(coldByMonth); err != nil {
		t.Fatalf("writeColdMetrics: %v", err)
	}

	cold := readColdMetrics(t, dir, "2026-01")
	if len(cold) != 1 {
		t.Fatalf("expected 1 entry in cold file, got %d", len(cold))
	}

	index := readMetricsIndex(t, dir)
	if len(index) != 1 || index[0] != "metrics-2026-01.json" {
		t.Errorf("unexpected index: %v", index)
	}
}

func TestWriteColdMetrics_mergesWithExisting(t *testing.T) {
	dir := t.TempDir()
	rep := &Reporter{DataDir: dir}

	ts1 := time.Date(2026, 1, 10, 12, 0, 0, 0, time.UTC)
	ts2 := time.Date(2026, 1, 20, 12, 0, 0, 0, time.UTC)

	// Pre-write one entry.
	existing := []MetricEntry{{Ts: ts1, App: "pfm", Level: "ok"}}
	existingData, _ := json.MarshalIndent(existing, "", "  ")
	os.WriteFile(filepath.Join(dir, "metrics-2026-01.json"), existingData, 0o644)

	// Write a different entry for the same month.
	coldByMonth := map[string][]MetricEntry{
		"2026-01": {{Ts: ts2, App: "crm", Level: "warning"}},
	}
	if err := rep.writeColdMetrics(coldByMonth); err != nil {
		t.Fatalf("writeColdMetrics: %v", err)
	}

	cold := readColdMetrics(t, dir, "2026-01")
	if len(cold) != 2 {
		t.Fatalf("expected 2 merged entries, got %d", len(cold))
	}
	// Should be sorted by Ts.
	if !cold[0].Ts.Before(cold[1].Ts) {
		t.Errorf("expected entries sorted by ts, got %v, %v", cold[0].Ts, cold[1].Ts)
	}
}

func TestWriteColdMetrics_indexNotDuplicated(t *testing.T) {
	dir := t.TempDir()
	rep := &Reporter{DataDir: dir}

	// Pre-seed the index with the file already listed.
	index := []string{"metrics-2026-01.json"}
	indexData, _ := json.MarshalIndent(index, "", "  ")
	os.WriteFile(filepath.Join(dir, "metrics-index.json"), indexData, 0o644)

	ts := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	coldByMonth := map[string][]MetricEntry{
		"2026-01": {{Ts: ts, App: "pfm", Level: "ok"}},
	}
	if err := rep.writeColdMetrics(coldByMonth); err != nil {
		t.Fatalf("writeColdMetrics: %v", err)
	}

	got := readMetricsIndex(t, dir)
	if len(got) != 1 {
		t.Errorf("expected index to stay at 1 entry (no duplicate), got %d", len(got))
	}
}
