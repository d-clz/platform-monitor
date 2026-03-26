package reporter

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"platform-monitor/internal/evaluator"
)

// ---- Types ----

// IncidentStatus indicates whether an incident is ongoing or has been resolved.
type IncidentStatus string

const (
	IncidentOpen     IncidentStatus = "open"
	IncidentResolved IncidentStatus = "resolved"
)

// IncidentNote is a user-authored annotation attached to an incident.
type IncidentNote struct {
	Timestamp string `json:"timestamp"`
	Content   string `json:"content"`
}

// Incident tracks the full lifecycle of a single app degradation event.
type Incident struct {
	ID              string         `json:"id"`
	AppName         string         `json:"appName"`
	OpenedAt        string         `json:"openedAt"`
	ClosedAt        *string        `json:"closedAt"`        // null while open
	Status          IncidentStatus `json:"status"`
	DurationMinutes *int           `json:"durationMinutes"` // null while open
	PeakLevel       evaluator.Level `json:"peakLevel"`
	Issues          []string       `json:"issues"`
	Notes           []IncidentNote `json:"notes"`
}

// ---- Reconcile ----

// reconcileIncidents opens new incidents for newly-degraded apps, refreshes
// existing open incidents with the latest issues and peak level, and closes
// incidents for apps that have recovered to OK.
func (r *Reporter) reconcileIncidents(results evaluator.Results, now time.Time) error {
	incPath := filepath.Join(r.DataDir, "incidents.json")
	incidents := loadIncidents(incPath)

	// Index open incidents by app name so we can mutate them in place.
	openByApp := make(map[string]*Incident)
	for i := range incidents {
		if incidents[i].Status == IncidentOpen {
			openByApp[incidents[i].AppName] = &incidents[i]
		}
	}

	for _, app := range results.Apps {
		nonOK := app.Level != evaluator.LevelOK
		open := openByApp[app.Name]

		switch {
		case nonOK && open == nil:
			// Newly degraded — open a new incident.
			incidents = append(incidents, makeIncident(app, now))

		case nonOK && open != nil:
			// Still degraded — refresh issues and escalate peak level if needed.
			open.Issues = app.Issues
			if incLevelRank(app.Level) > incLevelRank(open.PeakLevel) {
				open.PeakLevel = app.Level
			}

		case !nonOK && open != nil:
			// Recovered — close the incident and record duration.
			closedAt := now.UTC().Format(time.RFC3339)
			open.ClosedAt = &closedAt
			open.Status = IncidentResolved
			if t, err := time.Parse(time.RFC3339, open.OpenedAt); err == nil {
				dur := int(now.Sub(t).Minutes())
				open.DurationMinutes = &dur
			}
		}
	}

	data, err := json.MarshalIndent(incidents, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling incidents: %w", err)
	}
	return atomicWrite(incPath, data)
}

// readPreviousLevels reads the existing results.json (before it is overwritten)
// and returns a map of app name → level for transition detection.
// Returns nil if the file does not exist or cannot be parsed.
func (r *Reporter) readPreviousLevels() map[string]evaluator.Level {
	var snap struct {
		Apps []struct {
			Name  string          `json:"name"`
			Level evaluator.Level `json:"level"`
		} `json:"apps"`
	}
	data, err := os.ReadFile(filepath.Join(r.DataDir, "results.json"))
	if err != nil {
		return nil
	}
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil
	}
	m := make(map[string]evaluator.Level, len(snap.Apps))
	for _, a := range snap.Apps {
		m[a.Name] = a.Level
	}
	return m
}

// ---- Note API ----

// AddNote appends a user-authored note to the incident identified by incidentID.
// Returns (true, nil) on success, (false, nil) if the incident was not found.
func AddNote(incPath, incidentID, content string, now time.Time) (bool, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return false, fmt.Errorf("note content must not be empty")
	}

	incidents := loadIncidents(incPath)

	found := false
	for i := range incidents {
		if incidents[i].ID == incidentID {
			incidents[i].Notes = append(incidents[i].Notes, IncidentNote{
				Timestamp: now.UTC().Format(time.RFC3339),
				Content:   content,
			})
			found = true
			break
		}
	}
	if !found {
		return false, nil
	}

	data, err := json.MarshalIndent(incidents, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshalling incidents: %w", err)
	}
	return true, atomicWrite(incPath, data)
}

// UpdateNote replaces the content of a note identified by its zero-based index
// within the incident. Returns (false, nil) if the incident was not found.
func UpdateNote(incPath, incidentID string, noteIndex int, content string) (bool, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return false, fmt.Errorf("note content must not be empty")
	}

	incidents := loadIncidents(incPath)
	found := false
	for i := range incidents {
		if incidents[i].ID != incidentID {
			continue
		}
		if noteIndex < 0 || noteIndex >= len(incidents[i].Notes) {
			return false, fmt.Errorf("note index %d out of range", noteIndex)
		}
		incidents[i].Notes[noteIndex].Content = content
		found = true
		break
	}
	if !found {
		return false, nil
	}

	data, err := json.MarshalIndent(incidents, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshalling incidents: %w", err)
	}
	return true, atomicWrite(incPath, data)
}

// DeleteNote removes the note at the given zero-based index from the incident.
// Returns (false, nil) if the incident was not found.
func DeleteNote(incPath, incidentID string, noteIndex int) (bool, error) {
	incidents := loadIncidents(incPath)
	found := false
	for i := range incidents {
		if incidents[i].ID != incidentID {
			continue
		}
		if noteIndex < 0 || noteIndex >= len(incidents[i].Notes) {
			return false, fmt.Errorf("note index %d out of range", noteIndex)
		}
		incidents[i].Notes = append(
			incidents[i].Notes[:noteIndex],
			incidents[i].Notes[noteIndex+1:]...,
		)
		found = true
		break
	}
	if !found {
		return false, nil
	}

	data, err := json.MarshalIndent(incidents, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshalling incidents: %w", err)
	}
	return true, atomicWrite(incPath, data)
}

// ---- Helpers ----

func loadIncidents(path string) []Incident {
	data, err := os.ReadFile(path)
	if err != nil {
		return []Incident{}
	}
	var incidents []Incident
	if err := json.Unmarshal(data, &incidents); err != nil {
		return []Incident{}
	}
	return incidents
}

func makeIncident(app evaluator.AppResult, now time.Time) Incident {
	return Incident{
		ID:        fmt.Sprintf("%s-%s", app.Name, now.UTC().Format("20060102-1504")),
		AppName:   app.Name,
		OpenedAt:  now.UTC().Format(time.RFC3339),
		Status:    IncidentOpen,
		PeakLevel: app.Level,
		Issues:    app.Issues,
		Notes:     []IncidentNote{},
	}
}

func incLevelRank(l evaluator.Level) int {
	switch l {
	case evaluator.LevelWarning:
		return 1
	case evaluator.LevelCritical:
		return 2
	case evaluator.LevelError:
		return 3
	default:
		return 0
	}
}
