// Package reporter writes the evaluator output to disk for the dashboard to read.
//
// Two files are maintained under DataDir:
//   - results.json  — full snapshot of the latest run, overwritten each time
//   - history.json  — append-only log of runs that had at least one non-OK app
package reporter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"platform-monitor/internal/evaluator"
)

// AlertEntry captures the health of a single non-OK app in the history log.
type AlertEntry struct {
	AppName string          `json:"appName"`
	Level   evaluator.Level `json:"level"`
	Issues  []string        `json:"issues"`
}

// HistoryEntry is one run's worth of alert data written to history.json.
type HistoryEntry struct {
	Timestamp     string       `json:"timestamp"` // RFC3339
	WarningCount  int          `json:"warningCount"`
	CriticalCount int          `json:"criticalCount"`
	ErrorCount    int          `json:"errorCount"`
	Alerts        []AlertEntry `json:"alerts"`
}

const defaultHistoryLimit = 200

// Reporter writes monitoring results to a directory (typically a PVC mount).
type Reporter struct {
	DataDir      string
	HistoryLimit int // max entries kept in history.json; 0 uses defaultHistoryLimit
}

// Write reconciles incidents, atomically overwrites results.json with the
// latest snapshot, and appends to history.json when non-OK apps are present.
func (r *Reporter) Write(results evaluator.Results) error {
	// Reconcile incidents before overwriting results.json so we can read the
	// previous snapshot to detect ok→degraded and degraded→ok transitions.
	if err := r.reconcileIncidents(results, results.Timestamp); err != nil {
		return fmt.Errorf("reconciling incidents: %w", err)
	}

	if err := r.writeResults(results); err != nil {
		return fmt.Errorf("writing results.json: %w", err)
	}

	if results.WarningCount+results.CriticalCount+results.ErrorCount > 0 {
		if err := r.appendHistory(results); err != nil {
			return fmt.Errorf("appending history.json: %w", err)
		}
	}
	return nil
}

// writeResults marshals results to a temp file then renames it into place so
// the dashboard never reads a partial file.
func (r *Reporter) writeResults(results evaluator.Results) error {
	data, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling results: %w", err)
	}
	return atomicWrite(filepath.Join(r.DataDir, "results.json"), data)
}

// appendHistory reads history.json (if it exists), appends a new HistoryEntry
// for non-OK apps, then rewrites the file atomically.
func (r *Reporter) appendHistory(results evaluator.Results) error {
	histPath := filepath.Join(r.DataDir, "history.json")

	// Read existing history (empty slice if file doesn't exist yet).
	var history []HistoryEntry
	if data, err := os.ReadFile(histPath); err == nil {
		if err := json.Unmarshal(data, &history); err != nil {
			return fmt.Errorf("parsing existing history.json: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("reading history.json: %w", err)
	}

	// Build the new entry from non-OK apps only.
	entry := HistoryEntry{
		Timestamp:     results.Timestamp.Format("2006-01-02T15:04:05Z07:00"),
		WarningCount:  results.WarningCount,
		CriticalCount: results.CriticalCount,
		ErrorCount:    results.ErrorCount,
	}
	for _, app := range results.Apps {
		if app.Level == evaluator.LevelOK {
			continue
		}
		entry.Alerts = append(entry.Alerts, AlertEntry{
			AppName: app.Name,
			Level:   app.Level,
			Issues:  app.Issues,
		})
	}

	history = append(history, entry)

	// Trim oldest entries so the file doesn't grow unboundedly.
	limit := r.HistoryLimit
	if limit <= 0 {
		limit = defaultHistoryLimit
	}
	if len(history) > limit {
		history = history[len(history)-limit:]
	}

	data, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling history: %w", err)
	}
	return atomicWrite(histPath, data)
}

// atomicWrite writes data to a temp file in the same directory as dst, then
// renames it to dst. This ensures readers never see a partial write.
func atomicWrite(dst string, data []byte) error {
	dir := filepath.Dir(dst)
	tmp, err := os.CreateTemp(dir, ".tmp-")
	if err != nil {
		return fmt.Errorf("creating temp file: %w", err)
	}
	tmpName := tmp.Name()

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("writing temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("closing temp file: %w", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("renaming temp file to %s: %w", dst, err)
	}
	return nil
}
