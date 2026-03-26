package reporter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"platform-monitor/internal/evaluator"
)

// ---- helpers ----

func loadIncidentsFromDir(t *testing.T, dir string) []Incident {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "incidents.json"))
	if err != nil {
		t.Fatalf("reading incidents.json: %v", err)
	}
	var incidents []Incident
	if err := json.Unmarshal(data, &incidents); err != nil {
		t.Fatalf("parsing incidents.json: %v", err)
	}
	return incidents
}

func makeResults(apps ...evaluator.AppResult) evaluator.Results {
	r := evaluator.Results{
		Timestamp: fixedTime,
		Apps:      apps,
		TotalApps: len(apps),
	}
	for _, a := range apps {
		switch a.Level {
		case evaluator.LevelOK:
			r.OKCount++
		case evaluator.LevelWarning:
			r.WarningCount++
		case evaluator.LevelCritical:
			r.CriticalCount++
		case evaluator.LevelError:
			r.ErrorCount++
		}
	}
	return r
}

// ---- reconcileIncidents tests ----

// TestReconcileIncidents_opensNewIncident verifies that a degraded app with no
// existing open incident creates a new one.
func TestReconcileIncidents_opensNewIncident(t *testing.T) {
	dir := t.TempDir()
	rep := &Reporter{DataDir: dir}

	results := makeResults(evaluator.AppResult{
		Name:   "pfm",
		Level:  evaluator.LevelWarning,
		Issues: []string{"token expiring soon"},
	})

	if err := rep.reconcileIncidents(results, fixedTime); err != nil {
		t.Fatalf("reconcileIncidents: %v", err)
	}

	incidents := loadIncidentsFromDir(t, dir)
	if len(incidents) != 1 {
		t.Fatalf("expected 1 incident, got %d", len(incidents))
	}
	inc := incidents[0]
	if inc.AppName != "pfm" {
		t.Errorf("expected AppName=pfm, got %q", inc.AppName)
	}
	if inc.Status != IncidentOpen {
		t.Errorf("expected status=open, got %q", inc.Status)
	}
	if inc.PeakLevel != evaluator.LevelWarning {
		t.Errorf("expected PeakLevel=warning, got %q", inc.PeakLevel)
	}
	if inc.ClosedAt != nil {
		t.Errorf("expected ClosedAt=nil for open incident")
	}
	if !strings.HasPrefix(inc.ID, "pfm-") {
		t.Errorf("expected ID to start with pfm-, got %q", inc.ID)
	}
}

// TestReconcileIncidents_closesOnRecovery verifies that when an app returns to
// OK, its open incident is closed and duration is calculated.
func TestReconcileIncidents_closesOnRecovery(t *testing.T) {
	dir := t.TempDir()
	rep := &Reporter{DataDir: dir}

	openedAt := fixedTime
	degraded := makeResults(evaluator.AppResult{
		Name:   "pfm",
		Level:  evaluator.LevelWarning,
		Issues: []string{"token expiring soon"},
	})
	if err := rep.reconcileIncidents(degraded, openedAt); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	// 30 minutes later, app recovers.
	recoveredAt := openedAt.Add(30 * time.Minute)
	recovered := makeResults(evaluator.AppResult{
		Name:  "pfm",
		Level: evaluator.LevelOK,
	})
	recovered.Timestamp = recoveredAt
	if err := rep.reconcileIncidents(recovered, recoveredAt); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	incidents := loadIncidentsFromDir(t, dir)
	if len(incidents) != 1 {
		t.Fatalf("expected 1 incident, got %d", len(incidents))
	}
	inc := incidents[0]
	if inc.Status != IncidentResolved {
		t.Errorf("expected status=resolved, got %q", inc.Status)
	}
	if inc.ClosedAt == nil {
		t.Fatal("expected ClosedAt to be set")
	}
	if inc.DurationMinutes == nil {
		t.Fatal("expected DurationMinutes to be set")
	}
	if *inc.DurationMinutes != 30 {
		t.Errorf("expected DurationMinutes=30, got %d", *inc.DurationMinutes)
	}
}

// TestReconcileIncidents_escalatesPeakLevel verifies that a continuing incident
// escalates its PeakLevel if the current level is worse.
func TestReconcileIncidents_escalatesPeakLevel(t *testing.T) {
	dir := t.TempDir()
	rep := &Reporter{DataDir: dir}

	degraded := makeResults(evaluator.AppResult{
		Name:   "pfm",
		Level:  evaluator.LevelWarning,
		Issues: []string{"minor issue"},
	})
	if err := rep.reconcileIncidents(degraded, fixedTime); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	// Same app, worse level.
	worse := makeResults(evaluator.AppResult{
		Name:   "pfm",
		Level:  evaluator.LevelCritical,
		Issues: []string{"critical issue now"},
	})
	if err := rep.reconcileIncidents(worse, fixedTime.Add(15*time.Minute)); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	incidents := loadIncidentsFromDir(t, dir)
	if len(incidents) != 1 {
		t.Fatalf("expected 1 incident, got %d", len(incidents))
	}
	inc := incidents[0]
	if inc.PeakLevel != evaluator.LevelCritical {
		t.Errorf("expected PeakLevel=critical, got %q", inc.PeakLevel)
	}
	if inc.Status != IncidentOpen {
		t.Errorf("expected incident still open, got %q", inc.Status)
	}
	if len(inc.Issues) != 1 || inc.Issues[0] != "critical issue now" {
		t.Errorf("expected issues updated to critical issue, got %v", inc.Issues)
	}
}

// TestReconcileIncidents_doesNotDowngradePeakLevel verifies that if the current
// level improves, PeakLevel is NOT lowered.
func TestReconcileIncidents_doesNotDowngradePeakLevel(t *testing.T) {
	dir := t.TempDir()
	rep := &Reporter{DataDir: dir}

	if err := rep.reconcileIncidents(makeResults(evaluator.AppResult{
		Name:  "pfm",
		Level: evaluator.LevelCritical,
	}), fixedTime); err != nil {
		t.Fatalf("first reconcile: %v", err)
	}

	// Improves to warning — peak should stay critical.
	if err := rep.reconcileIncidents(makeResults(evaluator.AppResult{
		Name:  "pfm",
		Level: evaluator.LevelWarning,
	}), fixedTime.Add(15*time.Minute)); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}

	incidents := loadIncidentsFromDir(t, dir)
	if incidents[0].PeakLevel != evaluator.LevelCritical {
		t.Errorf("PeakLevel should stay critical, got %q", incidents[0].PeakLevel)
	}
}

// TestReconcileIncidents_reopensAfterRecovery verifies that a second degradation
// after recovery opens a new incident.
func TestReconcileIncidents_reopensAfterRecovery(t *testing.T) {
	dir := t.TempDir()
	rep := &Reporter{DataDir: dir}

	// First degradation → recovery.
	rep.reconcileIncidents(makeResults(evaluator.AppResult{Name: "pfm", Level: evaluator.LevelWarning}), fixedTime)
	rep.reconcileIncidents(makeResults(evaluator.AppResult{Name: "pfm", Level: evaluator.LevelOK}), fixedTime.Add(30*time.Minute))

	// Second degradation.
	if err := rep.reconcileIncidents(makeResults(evaluator.AppResult{Name: "pfm", Level: evaluator.LevelError}), fixedTime.Add(60*time.Minute)); err != nil {
		t.Fatalf("third reconcile: %v", err)
	}

	incidents := loadIncidentsFromDir(t, dir)
	if len(incidents) != 2 {
		t.Fatalf("expected 2 incidents after reopen, got %d", len(incidents))
	}
	if incidents[0].Status != IncidentResolved {
		t.Errorf("first incident should be resolved")
	}
	if incidents[1].Status != IncidentOpen {
		t.Errorf("second incident should be open")
	}
	if incidents[1].PeakLevel != evaluator.LevelError {
		t.Errorf("second incident PeakLevel=%q, want error", incidents[1].PeakLevel)
	}
}

// TestReconcileIncidents_allOKNoFile verifies that when all apps are OK and no
// incidents.json exists, the file is created with an empty array.
func TestReconcileIncidents_allOKNoFile(t *testing.T) {
	dir := t.TempDir()
	rep := &Reporter{DataDir: dir}

	if err := rep.reconcileIncidents(makeResults(evaluator.AppResult{Name: "pfm", Level: evaluator.LevelOK}), fixedTime); err != nil {
		t.Fatalf("reconcileIncidents: %v", err)
	}

	incidents := loadIncidentsFromDir(t, dir)
	if len(incidents) != 0 {
		t.Errorf("expected 0 incidents, got %d", len(incidents))
	}
}

// ---- readPreviousLevels tests ----

// TestReadPreviousLevels_missing verifies that a missing results.json returns nil.
func TestReadPreviousLevels_missing(t *testing.T) {
	rep := &Reporter{DataDir: t.TempDir()}
	if m := rep.readPreviousLevels(); m != nil {
		t.Errorf("expected nil for missing file, got %v", m)
	}
}

// TestReadPreviousLevels_parsesFile verifies that an existing results.json is
// parsed correctly into a level map.
func TestReadPreviousLevels_parsesFile(t *testing.T) {
	dir := t.TempDir()
	rep := &Reporter{DataDir: dir}

	snap := struct {
		Apps []struct {
			Name  string          `json:"name"`
			Level evaluator.Level `json:"level"`
		} `json:"apps"`
	}{
		Apps: []struct {
			Name  string          `json:"name"`
			Level evaluator.Level `json:"level"`
		}{
			{Name: "pfm", Level: evaluator.LevelOK},
			{Name: "crm", Level: evaluator.LevelWarning},
		},
	}
	data, _ := json.MarshalIndent(snap, "", "  ")
	os.WriteFile(filepath.Join(dir, "results.json"), data, 0o644)

	m := rep.readPreviousLevels()
	if m == nil {
		t.Fatal("expected map, got nil")
	}
	if m["pfm"] != evaluator.LevelOK {
		t.Errorf("pfm: expected ok, got %q", m["pfm"])
	}
	if m["crm"] != evaluator.LevelWarning {
		t.Errorf("crm: expected warning, got %q", m["crm"])
	}
}

// ---- incLevelRank tests ----

func TestIncLevelRank(t *testing.T) {
	cases := []struct {
		level evaluator.Level
		want  int
	}{
		{evaluator.LevelOK, 0},
		{evaluator.LevelWarning, 1},
		{evaluator.LevelCritical, 2},
		{evaluator.LevelError, 3},
		{"unknown", 0},
	}
	for _, tc := range cases {
		if got := incLevelRank(tc.level); got != tc.want {
			t.Errorf("incLevelRank(%q) = %d, want %d", tc.level, got, tc.want)
		}
	}
}

// ---- Note API tests ----

func seedIncidentsFile(t *testing.T, dir string, incidents []Incident) {
	t.Helper()
	data, err := json.MarshalIndent(incidents, "", "  ")
	if err != nil {
		t.Fatalf("seeding incidents: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "incidents.json"), data, 0o644); err != nil {
		t.Fatalf("writing incidents seed: %v", err)
	}
}

func incPath(dir string) string { return filepath.Join(dir, "incidents.json") }

// TestAddNote_success verifies a note is appended and persisted.
func TestAddNote_success(t *testing.T) {
	dir := t.TempDir()
	seedIncidentsFile(t, dir, []Incident{
		{ID: "pfm-20260325-1200", AppName: "pfm", Status: IncidentOpen, PeakLevel: evaluator.LevelWarning, Issues: []string{}, Notes: []IncidentNote{}},
	})

	ts := fixedTime.Add(5 * time.Minute)
	ok, err := AddNote(incPath(dir), "pfm-20260325-1200", "root cause identified", ts)
	if err != nil {
		t.Fatalf("AddNote: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}

	incidents := loadIncidentsFromDir(t, dir)
	if len(incidents[0].Notes) != 1 {
		t.Fatalf("expected 1 note, got %d", len(incidents[0].Notes))
	}
	if incidents[0].Notes[0].Content != "root cause identified" {
		t.Errorf("unexpected note content: %q", incidents[0].Notes[0].Content)
	}
	if incidents[0].Notes[0].Timestamp == "" {
		t.Error("expected non-empty Timestamp")
	}
}

// TestAddNote_notFound verifies that adding a note to a missing incident returns (false, nil).
func TestAddNote_notFound(t *testing.T) {
	dir := t.TempDir()
	seedIncidentsFile(t, dir, []Incident{})

	ok, err := AddNote(incPath(dir), "no-such-id", "note", fixedTime)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected ok=false for missing incident")
	}
}

// TestAddNote_emptyContent verifies that an empty note returns an error.
func TestAddNote_emptyContent(t *testing.T) {
	dir := t.TempDir()
	seedIncidentsFile(t, dir, []Incident{
		{ID: "pfm-1", AppName: "pfm", Status: IncidentOpen, Notes: []IncidentNote{}},
	})

	_, err := AddNote(incPath(dir), "pfm-1", "   ", fixedTime)
	if err == nil {
		t.Error("expected error for empty content")
	}
}

// TestAddNote_trimsWhitespace verifies that leading/trailing whitespace is stripped.
func TestAddNote_trimsWhitespace(t *testing.T) {
	dir := t.TempDir()
	seedIncidentsFile(t, dir, []Incident{
		{ID: "pfm-1", AppName: "pfm", Status: IncidentOpen, Notes: []IncidentNote{}},
	})

	AddNote(incPath(dir), "pfm-1", "  trimmed  ", fixedTime)

	incidents := loadIncidentsFromDir(t, dir)
	if incidents[0].Notes[0].Content != "trimmed" {
		t.Errorf("expected trimmed content, got %q", incidents[0].Notes[0].Content)
	}
}

// TestUpdateNote_success verifies a note's content is updated in place.
func TestUpdateNote_success(t *testing.T) {
	dir := t.TempDir()
	seedIncidentsFile(t, dir, []Incident{
		{
			ID: "pfm-1", AppName: "pfm", Status: IncidentOpen,
			Notes: []IncidentNote{{Timestamp: "2026-03-25T12:00:00Z", Content: "old content"}},
		},
	})

	ok, err := UpdateNote(incPath(dir), "pfm-1", 0, "new content")
	if err != nil {
		t.Fatalf("UpdateNote: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}

	incidents := loadIncidentsFromDir(t, dir)
	if incidents[0].Notes[0].Content != "new content" {
		t.Errorf("expected updated content, got %q", incidents[0].Notes[0].Content)
	}
}

// TestUpdateNote_outOfRange verifies that an out-of-range index returns an error.
func TestUpdateNote_outOfRange(t *testing.T) {
	dir := t.TempDir()
	seedIncidentsFile(t, dir, []Incident{
		{
			ID: "pfm-1", AppName: "pfm", Status: IncidentOpen,
			Notes: []IncidentNote{{Content: "note"}},
		},
	})

	_, err := UpdateNote(incPath(dir), "pfm-1", 5, "content")
	if err == nil {
		t.Error("expected error for out-of-range index")
	}
}

// TestUpdateNote_notFound verifies (false, nil) when incident doesn't exist.
func TestUpdateNote_notFound(t *testing.T) {
	dir := t.TempDir()
	seedIncidentsFile(t, dir, []Incident{})

	ok, err := UpdateNote(incPath(dir), "no-such", 0, "content")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected ok=false")
	}
}

// TestUpdateNote_emptyContent verifies an error is returned for blank content.
func TestUpdateNote_emptyContent(t *testing.T) {
	dir := t.TempDir()
	seedIncidentsFile(t, dir, []Incident{
		{ID: "pfm-1", Notes: []IncidentNote{{Content: "note"}}},
	})

	_, err := UpdateNote(incPath(dir), "pfm-1", 0, "")
	if err == nil {
		t.Error("expected error for empty content")
	}
}

// TestDeleteNote_success verifies a note is removed and the slice is preserved.
func TestDeleteNote_success(t *testing.T) {
	dir := t.TempDir()
	seedIncidentsFile(t, dir, []Incident{
		{
			ID: "pfm-1", AppName: "pfm", Status: IncidentOpen,
			Notes: []IncidentNote{
				{Content: "first"},
				{Content: "second"},
			},
		},
	})

	ok, err := DeleteNote(incPath(dir), "pfm-1", 0)
	if err != nil {
		t.Fatalf("DeleteNote: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}

	incidents := loadIncidentsFromDir(t, dir)
	if len(incidents[0].Notes) != 1 {
		t.Fatalf("expected 1 note remaining, got %d", len(incidents[0].Notes))
	}
	if incidents[0].Notes[0].Content != "second" {
		t.Errorf("expected remaining note to be 'second', got %q", incidents[0].Notes[0].Content)
	}
}

// TestDeleteNote_outOfRange verifies an error for an invalid index.
func TestDeleteNote_outOfRange(t *testing.T) {
	dir := t.TempDir()
	seedIncidentsFile(t, dir, []Incident{
		{ID: "pfm-1", Notes: []IncidentNote{{Content: "only"}},
		},
	})

	_, err := DeleteNote(incPath(dir), "pfm-1", 9)
	if err == nil {
		t.Error("expected error for out-of-range index")
	}
}

// TestDeleteNote_notFound verifies (false, nil) for a missing incident.
func TestDeleteNote_notFound(t *testing.T) {
	dir := t.TempDir()
	seedIncidentsFile(t, dir, []Incident{})

	ok, err := DeleteNote(incPath(dir), "no-such", 0)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected ok=false")
	}
}

// TestDeleteNote_negativeIndex verifies an error for negative index.
func TestDeleteNote_negativeIndex(t *testing.T) {
	dir := t.TempDir()
	seedIncidentsFile(t, dir, []Incident{
		{ID: "pfm-1", Notes: []IncidentNote{{Content: "note"}}},
	})

	_, err := DeleteNote(incPath(dir), "pfm-1", -1)
	if err == nil {
		t.Error("expected error for negative index")
	}
}
