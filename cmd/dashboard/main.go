// Command dashboard serves the static web dashboard from a directory.
//
// Environment variables:
//
//	WEB_DIR   directory containing index.html and app.js  (default /web)
//	DATA_DIR  directory containing results.json            (default /data)
//	PORT      TCP port to listen on                        (default 8080)
package main

import (
	"log"
	"net/http"
	"os"
)

func main() {
	webDir := envOr("WEB_DIR", "/web")
	dataDir := envOr("DATA_DIR", "/data")
	port := envOr("PORT", "8080")

	mux := http.NewServeMux()

	// Serve results.json and history.json from the data PVC.
	mux.Handle("/data/", http.StripPrefix("/data/", http.FileServer(http.Dir(dataDir))))

	// Serve static dashboard assets.
	mux.Handle("/", http.FileServer(http.Dir(webDir)))

	addr := ":" + port
	log.Printf("dashboard: listening on %s (web=%s, data=%s)", addr, webDir, dataDir)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("dashboard: %v", err)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
