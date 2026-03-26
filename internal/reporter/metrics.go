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

const hotWindowDays = 60

// MetricEntry is one data point captured per app per cron run.
type MetricEntry struct {
	Ts               time.Time `json:"ts"`
	App              string    `json:"app"`
	Level            string    `json:"level"`
	PipelineStatus   string    `json:"pipelineStatus"`   // "success", "failed", etc.; empty if no pipeline
	PipelineDuration int       `json:"pipelineDuration"` // seconds; 0 if not available
	FailedJobs       int       `json:"failedJobs"`       // total failed jobs within the failure window
	RunnersOnline    int       `json:"runnersOnline"`    // non-stale runner count
}

// appendMetrics builds one MetricEntry per app from results, appends them to
// the hot file (metrics.json), rotates entries older than hotWindowDays into
// monthly cold files (metrics-YYYY-MM.json), and keeps metrics-index.json
// up to date. Called on every cron run regardless of health status.
func (r *Reporter) appendMetrics(results evaluator.Results) error {
	hotPath := filepath.Join(r.DataDir, "metrics.json")

	// Load existing hot entries.
	var hot []MetricEntry
	if data, err := os.ReadFile(hotPath); err == nil {
		if err := json.Unmarshal(data, &hot); err != nil {
			return fmt.Errorf("parsing metrics.json: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("reading metrics.json: %w", err)
	}

	// Append new entries for this run.
	hot = append(hot, buildMetricEntries(results)...)

	// Partition: hot (within window) vs cold (by calendar month).
	cutoff := results.Timestamp.AddDate(0, 0, -hotWindowDays)
	var hotEntries []MetricEntry
	coldByMonth := make(map[string][]MetricEntry) // "2006-01" → entries

	for _, e := range hot {
		if e.Ts.After(cutoff) {
			hotEntries = append(hotEntries, e)
		} else {
			key := e.Ts.UTC().Format("2006-01")
			coldByMonth[key] = append(coldByMonth[key], e)
		}
	}

	// Write cold files and update the index.
	if len(coldByMonth) > 0 {
		if err := r.writeColdMetrics(coldByMonth); err != nil {
			return err
		}
	}

	// Rewrite the hot file with only recent entries.
	data, err := json.MarshalIndent(hotEntries, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling metrics: %w", err)
	}
	return atomicWrite(hotPath, data)
}

// buildMetricEntries converts an evaluator.Results into one MetricEntry per app.
func buildMetricEntries(results evaluator.Results) []MetricEntry {
	entries := make([]MetricEntry, 0, len(results.Apps))
	for _, app := range results.Apps {
		e := MetricEntry{
			Ts:    results.Timestamp,
			App:   app.Name,
			Level: string(app.Level),
		}
		if app.GitLab != nil {
			if app.GitLab.LastPipeline != nil {
				e.PipelineStatus = app.GitLab.LastPipeline.Status
				e.PipelineDuration = app.GitLab.LastPipeline.Duration
			}
			for _, count := range app.GitLab.FailedJobsByStage {
				e.FailedJobs += count
			}
			e.RunnersOnline = app.GitLab.RunnerCount - app.GitLab.StaleRunnerCount
		}
		entries = append(entries, e)
	}
	return entries
}

// writeColdMetrics merges new cold entries into their monthly files and updates
// metrics-index.json. Entries are deduplicated by (ts, app) so rotation is
// idempotent in the event of a crash before the hot file is rewritten.
func (r *Reporter) writeColdMetrics(coldByMonth map[string][]MetricEntry) error {
	indexPath := filepath.Join(r.DataDir, "metrics-index.json")

	// Load existing index.
	var index []string
	if data, err := os.ReadFile(indexPath); err == nil {
		_ = json.Unmarshal(data, &index)
	}
	indexSet := make(map[string]struct{}, len(index))
	for _, name := range index {
		indexSet[name] = struct{}{}
	}

	for monthKey, newEntries := range coldByMonth {
		fname := "metrics-" + monthKey + ".json"
		coldPath := filepath.Join(r.DataDir, fname)

		// Merge with any existing cold entries for the same month.
		var existing []MetricEntry
		if data, err := os.ReadFile(coldPath); err == nil {
			_ = json.Unmarshal(data, &existing)
		}

		merged := dedupMetrics(append(existing, newEntries...))
		sort.Slice(merged, func(i, j int) bool {
			return merged[i].Ts.Before(merged[j].Ts)
		})

		data, err := json.MarshalIndent(merged, "", "  ")
		if err != nil {
			return fmt.Errorf("marshalling cold metrics for %s: %w", monthKey, err)
		}
		if err := atomicWrite(coldPath, data); err != nil {
			return fmt.Errorf("writing cold metrics for %s: %w", monthKey, err)
		}

		if _, ok := indexSet[fname]; !ok {
			index = append(index, fname)
			indexSet[fname] = struct{}{}
		}
	}

	sort.Strings(index)
	data, err := json.MarshalIndent(index, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling metrics index: %w", err)
	}
	return atomicWrite(indexPath, data)
}

// dedupMetrics returns entries with duplicates (same ts+app) removed; the last
// occurrence wins, preserving the most recent write for a given sample point.
func dedupMetrics(entries []MetricEntry) []MetricEntry {
	seen := make(map[string]int, len(entries)) // key → index in result
	result := make([]MetricEntry, 0, len(entries))
	for _, e := range entries {
		key := e.Ts.UTC().Format(time.RFC3339) + "|" + e.App
		if idx, ok := seen[key]; ok {
			result[idx] = e
		} else {
			seen[key] = len(result)
			result = append(result, e)
		}
	}
	return result
}
