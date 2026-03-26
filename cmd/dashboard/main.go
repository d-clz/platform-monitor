// Command dashboard serves the static web dashboard and handles note submissions.
//
// Environment variables:
//
//	WEB_DIR   directory containing index.html and app.js  (default /web)
//	DATA_DIR  directory containing results.json            (default /data)
//	HOST      interface to bind to                         (default 0.0.0.0)
//	PORT      TCP port to listen on                        (default 8080)
//	LOG_FILE  path to log file; logs go to both file and stdout (default: stdout only)
package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"platform-monitor/internal/reporter"
)

func main() {
	webDir  := envOr("WEB_DIR",  "/web")
	dataDir := envOr("DATA_DIR", "/data")
	host    := envOr("HOST",     "0.0.0.0")
	port    := envOr("PORT",     "8080")
	logFile := envOr("LOG_FILE", "")

	setupLogger(logFile)

	mux := http.NewServeMux()

	// POST /notes — append a user note to an incident.
	mux.HandleFunc("POST /notes", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			IncidentID string `json:"incidentId"`
			Content    string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		if req.IncidentID == "" || strings.TrimSpace(req.Content) == "" {
			http.Error(w, "incidentId and content are required", http.StatusBadRequest)
			return
		}

		incPath := filepath.Join(dataDir, "incidents.json")
		found, err := reporter.AddNote(incPath, req.IncidentID, req.Content, time.Now())
		if err != nil {
			log.Printf("ERROR saving note for incident %s: %v", req.IncidentID, err)
			http.Error(w, "failed to save note", http.StatusInternalServerError)
			return
		}
		if !found {
			log.Printf("WARN  note POST: incident %q not found", req.IncidentID)
			http.Error(w, "incident not found", http.StatusNotFound)
			return
		}
		log.Printf("INFO  note saved for incident %s", req.IncidentID)
		w.WriteHeader(http.StatusNoContent)
	})

	// PUT /notes — update the content of an existing note.
	mux.HandleFunc("PUT /notes", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			IncidentID string `json:"incidentId"`
			NoteIndex  int    `json:"noteIndex"`
			Content    string `json:"content"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		if req.IncidentID == "" || strings.TrimSpace(req.Content) == "" {
			http.Error(w, "incidentId and content are required", http.StatusBadRequest)
			return
		}
		incPath := filepath.Join(dataDir, "incidents.json")
		found, err := reporter.UpdateNote(incPath, req.IncidentID, req.NoteIndex, req.Content)
		if err != nil {
			log.Printf("ERROR updating note: %v", err)
			http.Error(w, "failed to update note", http.StatusInternalServerError)
			return
		}
		if !found {
			http.Error(w, "incident not found", http.StatusNotFound)
			return
		}
		log.Printf("INFO  note %d updated for incident %s", req.NoteIndex, req.IncidentID)
		w.WriteHeader(http.StatusNoContent)
	})

	// DELETE /notes — remove a note from an incident.
	mux.HandleFunc("DELETE /notes", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			IncidentID string `json:"incidentId"`
			NoteIndex  int    `json:"noteIndex"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		if req.IncidentID == "" {
			http.Error(w, "incidentId is required", http.StatusBadRequest)
			return
		}
		incPath := filepath.Join(dataDir, "incidents.json")
		found, err := reporter.DeleteNote(incPath, req.IncidentID, req.NoteIndex)
		if err != nil {
			log.Printf("ERROR deleting note: %v", err)
			http.Error(w, "failed to delete note", http.StatusInternalServerError)
			return
		}
		if !found {
			http.Error(w, "incident not found", http.StatusNotFound)
			return
		}
		log.Printf("INFO  note %d deleted from incident %s", req.NoteIndex, req.IncidentID)
		w.WriteHeader(http.StatusNoContent)
	})

	// GET /data/* — serve results.json, incidents.json, metrics*.json from the data directory.
	mux.Handle("/data/", http.StripPrefix("/data/", http.FileServer(http.Dir(dataDir))))

	// GET /report — serve the long-term report SPA.
	mux.HandleFunc("GET /report", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, filepath.Join(webDir, "report.html"))
	})

	// GET /* — serve static dashboard assets.
	mux.Handle("/", http.FileServer(http.Dir(webDir)))

	addr := host + ":" + port
	log.Printf("INFO  dashboard listening on %s (web=%s, data=%s)", addr, webDir, dataDir)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("FATAL dashboard: %v", err)
	}
}

// setupLogger configures the default logger to write to stdout and, if path is
// non-empty, to a log file simultaneously. The file is opened in append mode so
// restarts do not truncate previous entries.
func setupLogger(path string) {
	log.SetFlags(log.Ldate | log.Ltime | log.LUTC)
	log.SetPrefix("")

	if path == "" {
		log.SetOutput(os.Stdout)
		return
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		// Fall back to stdout only and warn — don't crash on a log config issue.
		log.SetOutput(os.Stdout)
		log.Printf("WARN  could not open log file %q: %v — logging to stdout only", path, err)
		return
	}

	log.SetOutput(io.MultiWriter(os.Stdout, f))
	log.Printf("INFO  logging to stdout and %s", path)
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
