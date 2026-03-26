package reporter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"platform-monitor/internal/evaluator"
)

// ---- helpers ----

var fixedTime = time.Date(2026, 3, 25, 12, 0, 0, 0, time.UTC)

func okResults() evaluator.Results {
	return evaluator.Results{
		Timestamp: fixedTime,
		Apps: []evaluator.AppResult{
			{Name: "pfm", Source: evaluator.SourceOCPGitLab, Level: evaluator.LevelOK},
		},
		TotalApps: 1,
		OKCount:   1,
	}
}

func warnResults() evaluator.Results {
	return evaluator.Results{
		Timestamp: fixedTime,
		Apps: []evaluator.AppResult{
			{
				Name:   "pfm",
				Source: evaluator.SourceOCPGitLab,
				Level:  evaluator.LevelWarning,
				Issues: []string{"deployer token age 70 days (warning)"},
			},
			{
				Name:   "crm",
				Source: evaluator.SourceOCPGitLab,
				Level:  evaluator.LevelOK,
			},
		},
		TotalApps:    2,
		OKCount:      1,
		WarningCount: 1,
	}
}

func readResults(t *testing.T, dir string) evaluator.Results {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "results.json"))
	if err != nil {
		t.Fatalf("reading results.json: %v", err)
	}
	var r evaluator.Results
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("parsing results.json: %v", err)
	}
	return r
}

func readHistory(t *testing.T, dir string) []HistoryEntry {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "history.json"))
	if err != nil {
		t.Fatalf("reading history.json: %v", err)
	}
	var h []HistoryEntry
	if err := json.Unmarshal(data, &h); err != nil {
		t.Fatalf("parsing history.json: %v", err)
	}
	return h
}

// ---- tests ----

// TestWrite_resultsJSON verifies that results.json is created with the correct content.
func TestWrite_resultsJSON(t *testing.T) {
	dir := t.TempDir()
	rep := &Reporter{DataDir: dir}

	input := okResults()
	if err := rep.Write(input); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	got := readResults(t, dir)
	if got.TotalApps != 1 {
		t.Errorf("expected TotalApps=1, got %d", got.TotalApps)
	}
	if got.OKCount != 1 {
		t.Errorf("expected OKCount=1, got %d", got.OKCount)
	}
	if len(got.Apps) != 1 || got.Apps[0].Name != "pfm" {
		t.Errorf("unexpected apps: %+v", got.Apps)
	}
}

// TestWrite_historyAppended verifies that a non-OK run writes a history entry.
func TestWrite_historyAppended(t *testing.T) {
	dir := t.TempDir()
	rep := &Reporter{DataDir: dir}

	if err := rep.Write(warnResults()); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	history := readHistory(t, dir)
	if len(history) != 1 {
		t.Fatalf("expected 1 history entry, got %d", len(history))
	}
	entry := history[0]
	if entry.WarningCount != 1 {
		t.Errorf("expected WarningCount=1, got %d", entry.WarningCount)
	}
	if len(entry.Alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(entry.Alerts))
	}
	if entry.Alerts[0].AppName != "pfm" {
		t.Errorf("expected alert for pfm, got %q", entry.Alerts[0].AppName)
	}
	if entry.Alerts[0].Level != evaluator.LevelWarning {
		t.Errorf("expected warning level, got %q", entry.Alerts[0].Level)
	}
}

// TestWrite_historyNotWrittenForAllOK verifies that an all-OK run does not
// create history.json.
func TestWrite_historyNotWrittenForAllOK(t *testing.T) {
	dir := t.TempDir()
	rep := &Reporter{DataDir: dir}

	if err := rep.Write(okResults()); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	histPath := filepath.Join(dir, "history.json")
	if _, err := os.Stat(histPath); !os.IsNotExist(err) {
		t.Errorf("history.json should not exist for an all-OK run")
	}
}

// TestWrite_historyAccumulates verifies that two non-OK runs produce two entries.
func TestWrite_historyAccumulates(t *testing.T) {
	dir := t.TempDir()
	rep := &Reporter{DataDir: dir}

	if err := rep.Write(warnResults()); err != nil {
		t.Fatalf("first Write failed: %v", err)
	}

	// Second run: different timestamp, critical level.
	second := evaluator.Results{
		Timestamp: fixedTime.Add(15 * time.Minute),
		Apps: []evaluator.AppResult{
			{Name: "crm", Level: evaluator.LevelCritical, Issues: []string{"deployer token age 95 days (critical)"}},
		},
		TotalApps:     1,
		CriticalCount: 1,
	}
	if err := rep.Write(second); err != nil {
		t.Fatalf("second Write failed: %v", err)
	}

	history := readHistory(t, dir)
	if len(history) != 2 {
		t.Fatalf("expected 2 history entries, got %d", len(history))
	}
	if history[0].WarningCount != 1 {
		t.Errorf("first entry: expected WarningCount=1, got %d", history[0].WarningCount)
	}
	if history[1].CriticalCount != 1 {
		t.Errorf("second entry: expected CriticalCount=1, got %d", history[1].CriticalCount)
	}
}

// TestWrite_existingHistoryPreserved verifies that pre-existing history entries
// are kept when a new entry is appended.
func TestWrite_existingHistoryPreserved(t *testing.T) {
	dir := t.TempDir()
	rep := &Reporter{DataDir: dir}

	// Seed history.json with one entry directly.
	seed := []HistoryEntry{{
		Timestamp:    "2026-03-24T12:00:00Z",
		WarningCount: 3,
		Alerts: []AlertEntry{
			{AppName: "legacy", Level: evaluator.LevelWarning, Issues: []string{"old issue"}},
		},
	}}
	seedData, _ := json.MarshalIndent(seed, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "history.json"), seedData, 0o644); err != nil {
		t.Fatalf("seeding history.json: %v", err)
	}

	if err := rep.Write(warnResults()); err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	history := readHistory(t, dir)
	if len(history) != 2 {
		t.Fatalf("expected 2 history entries, got %d", len(history))
	}
	if history[0].Alerts[0].AppName != "legacy" {
		t.Errorf("original entry should be first; got %q", history[0].Alerts[0].AppName)
	}
	if history[1].Alerts[0].AppName != "pfm" {
		t.Errorf("new entry should be second; got %q", history[1].Alerts[0].AppName)
	}
}

// TestWrite_historyTrimmed verifies that the history file is capped at HistoryLimit
// and the oldest entries are dropped when the limit is exceeded.
func TestWrite_historyTrimmed(t *testing.T) {
	dir := t.TempDir()
	rep := &Reporter{DataDir: dir, HistoryLimit: 3}

	for i := range 5 {
		r := evaluator.Results{
			Timestamp: fixedTime.Add(time.Duration(i) * 15 * time.Minute),
			Apps: []evaluator.AppResult{
				{Name: fmt.Sprintf("app%d", i), Level: evaluator.LevelWarning, Issues: []string{"issue"}},
			},
			TotalApps:    1,
			WarningCount: 1,
		}
		if err := rep.Write(r); err != nil {
			t.Fatalf("Write %d failed: %v", i, err)
		}
	}

	history := readHistory(t, dir)
	if len(history) != 3 {
		t.Fatalf("expected history capped at 3, got %d", len(history))
	}
	// Oldest two entries (app0, app1) should have been dropped.
	if history[0].Alerts[0].AppName != "app2" {
		t.Errorf("expected oldest kept entry to be app2, got %q", history[0].Alerts[0].AppName)
	}
	if history[2].Alerts[0].AppName != "app4" {
		t.Errorf("expected newest entry to be app4, got %q", history[2].Alerts[0].AppName)
	}
}

// TestWrite_resultsOverwritten verifies that a second Write replaces results.json
// rather than appending to it.
func TestWrite_resultsOverwritten(t *testing.T) {
	dir := t.TempDir()
	rep := &Reporter{DataDir: dir}

	if err := rep.Write(okResults()); err != nil {
		t.Fatalf("first Write failed: %v", err)
	}

	second := evaluator.Results{
		Timestamp: fixedTime.Add(15 * time.Minute),
		Apps: []evaluator.AppResult{
			{Name: "pfm", Level: evaluator.LevelOK},
			{Name: "crm", Level: evaluator.LevelOK},
		},
		TotalApps: 2,
		OKCount:   2,
	}
	if err := rep.Write(second); err != nil {
		t.Fatalf("second Write failed: %v", err)
	}

	got := readResults(t, dir)
	if got.TotalApps != 2 {
		t.Errorf("expected TotalApps=2 after overwrite, got %d", got.TotalApps)
	}
}
