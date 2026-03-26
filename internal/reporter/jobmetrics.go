package reporter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"platform-monitor/internal/evaluator"
)

// jobMetricsWeekCap is the maximum number of ISO weeks retained in the hot file.
// 52 weeks = ~1 year. No cold rotation needed — the file stays small.
const jobMetricsWeekCap = 52

// JobMetricEntry is a weekly aggregate for one job within one app.
// Each distinct pipeline run is counted exactly once (deduped by pipeline ID).
type JobMetricEntry struct {
	Week          string  `json:"week"`          // "2026-W13" (ISO year-week)
	App           string  `json:"app"`
	Job           string  `json:"job"`
	Stage         string  `json:"stage"`
	Runs          int     `json:"runs"`          // distinct pipeline runs counted
	Failures      int     `json:"failures"`      // runs where job was failed or canceled
	TotalDuration float64 `json:"totalDuration"` // sum of durations in seconds
}

// jobMetricsState maps appName → last counted pipeline ID to prevent
// double-counting the same pipeline across multiple cron polls.
type jobMetricsState map[string]int

func isoWeek(t time.Time) string {
	year, week := t.ISOWeek()
	return fmt.Sprintf("%d-W%02d", year, week)
}

// appendJobMetrics updates weekly job aggregates from the current results.
// Only apps whose pipeline ID changed since the last run are processed.
func (r *Reporter) appendJobMetrics(results evaluator.Results) error {
	jobPath   := filepath.Join(r.DataDir, "job-metrics.json")
	statePath := filepath.Join(r.DataDir, "job-metrics-state.json")

	// Load existing weekly aggregates.
	var entries []JobMetricEntry
	if data, err := os.ReadFile(jobPath); err == nil {
		if err := json.Unmarshal(data, &entries); err != nil {
			return fmt.Errorf("parsing job-metrics.json: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("reading job-metrics.json: %w", err)
	}

	// Load last-seen pipeline IDs.
	state := make(jobMetricsState)
	if data, err := os.ReadFile(statePath); err == nil {
		_ = json.Unmarshal(data, &state)
	}

	week    := isoWeek(results.Timestamp)
	updated := false

	for _, app := range results.Apps {
		if app.GitLab == nil || app.GitLab.LastPipeline == nil {
			continue
		}
		pipelineID := app.GitLab.LastPipeline.ID
		if state[app.Name] == pipelineID {
			continue // already counted this pipeline run
		}

		// New pipeline — upsert each job into this week's aggregate.
		for _, job := range app.GitLab.LastPipelineJobs {
			entries = upsertJobEntry(entries, week, app.Name, job.Name, job.Stage, job.Status, job.Duration)
		}
		state[app.Name] = pipelineID
		updated = true
	}

	if !updated {
		return nil
	}

	// Trim to the most recent jobMetricsWeekCap distinct weeks.
	entries = capJobEntries(entries, jobMetricsWeekCap)

	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling job-metrics: %w", err)
	}
	if err := atomicWrite(jobPath, data); err != nil {
		return err
	}

	stateData, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling job-metrics state: %w", err)
	}
	return atomicWrite(statePath, stateData)
}

// upsertJobEntry finds the entry for (week, app, job) and increments its
// counters, or appends a new one if none exists.
func upsertJobEntry(entries []JobMetricEntry, week, app, job, stage, status string, duration float64) []JobMetricEntry {
	failed := status == "failed" || status == "canceled"
	for i, e := range entries {
		if e.Week == week && e.App == app && e.Job == job {
			entries[i].Runs++
			if failed {
				entries[i].Failures++
			}
			entries[i].TotalDuration += duration
			return entries
		}
	}
	e := JobMetricEntry{
		Week:          week,
		App:           app,
		Job:           job,
		Stage:         stage,
		Runs:          1,
		TotalDuration: duration,
	}
	if failed {
		e.Failures = 1
	}
	return append(entries, e)
}

// capJobEntries keeps only entries belonging to the most recent maxWeeks
// distinct ISO weeks, dropping the oldest when the cap is exceeded.
func capJobEntries(entries []JobMetricEntry, maxWeeks int) []JobMetricEntry {
	weekSet := make(map[string]struct{})
	for _, e := range entries {
		weekSet[e.Week] = struct{}{}
	}
	if len(weekSet) <= maxWeeks {
		return entries
	}

	weeks := make([]string, 0, len(weekSet))
	for w := range weekSet {
		weeks = append(weeks, w)
	}
	sort.Strings(weeks) // "YYYY-WNN" sorts lexicographically

	keep := make(map[string]struct{}, maxWeeks)
	for _, w := range weeks[len(weeks)-maxWeeks:] {
		keep[w] = struct{}{}
	}

	out := entries[:0]
	for _, e := range entries {
		if _, ok := keep[e.Week]; ok {
			out = append(out, e)
		}
	}
	return out
}
