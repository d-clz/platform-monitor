package checker

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// ---- GetAppRepos ----

// TestGetAppRepos_success verifies that all projects in a group are returned as RepoInfo.
func TestGetAppRepos_success(t *testing.T) {
	srv := glMux(map[string]http.HandlerFunc{
		"/api/v4/groups/10/projects": func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("PRIVATE-TOKEN") != "test-token" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			writeJSON(w, []glProject{
				{ID: 101, Path: "pfm-api"},
				{ID: 102, Path: "pfm-service"},
			})
		},
	})
	defer srv.Close()

	c := newGitLabChecker(srv)
	repos, err := c.GetAppRepos(context.Background(), 10)
	if err != nil {
		t.Fatalf("GetAppRepos: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("expected 2 repos, got %d", len(repos))
	}
	if repos[0].Name != "pfm-api" || repos[0].ID != 101 {
		t.Errorf("repos[0]: expected pfm-api/101, got %+v", repos[0])
	}
	if repos[1].Name != "pfm-service" || repos[1].ID != 102 {
		t.Errorf("repos[1]: expected pfm-service/102, got %+v", repos[1])
	}
}

// TestGetAppRepos_pagination verifies that all pages are fetched when X-Next-Page is set.
func TestGetAppRepos_pagination(t *testing.T) {
	srv := glMux(map[string]http.HandlerFunc{
		"/api/v4/groups/10/projects": func(w http.ResponseWriter, r *http.Request) {
			page := r.URL.Query().Get("page")
			switch page {
			case "1", "":
				w.Header().Set("X-Next-Page", "2")
				writeJSON(w, []glProject{{ID: 101, Path: "pfm-api"}})
			case "2":
				writeJSON(w, []glProject{{ID: 102, Path: "pfm-service"}})
			default:
				http.Error(w, "unexpected page", http.StatusBadRequest)
			}
		},
	})
	defer srv.Close()

	c := newGitLabChecker(srv)
	repos, err := c.GetAppRepos(context.Background(), 10)
	if err != nil {
		t.Fatalf("GetAppRepos: %v", err)
	}
	if len(repos) != 2 {
		t.Fatalf("expected 2 repos across 2 pages, got %d", len(repos))
	}
}

// TestGetAppRepos_emptyGroup verifies that a group with no projects returns an empty slice.
func TestGetAppRepos_emptyGroup(t *testing.T) {
	srv := glMux(map[string]http.HandlerFunc{
		"/api/v4/groups/10/projects": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glProject{})
		},
	})
	defer srv.Close()

	c := newGitLabChecker(srv)
	repos, err := c.GetAppRepos(context.Background(), 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(repos) != 0 {
		t.Errorf("expected empty slice, got %v", repos)
	}
}

// TestGetAppRepos_apiError verifies that a non-200 response is returned as an error.
func TestGetAppRepos_apiError(t *testing.T) {
	srv := glMux(map[string]http.HandlerFunc{
		"/api/v4/groups/10/projects": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"message":"403 Forbidden"}`, http.StatusForbidden)
		},
	})
	defer srv.Close()

	c := newGitLabChecker(srv)
	_, err := c.GetAppRepos(context.Background(), 10)
	if err == nil {
		t.Fatal("expected error from API, got nil")
	}
}

// ---- MergeAppReposCache ----

func TestMergeAppReposCache_addsNewApp(t *testing.T) {
	cached := map[string][]RepoInfo{"pfm": {{Name: "pfm-api", ID: 10}}}
	fresh := map[string][]RepoInfo{
		"pfm": {{Name: "pfm-api", ID: 10}},
		"crm": {{Name: "crm-backend", ID: 20}}, // new
	}

	merged, changed := MergeAppReposCache(cached, fresh)
	if !changed {
		t.Error("expected changed=true when new app added")
	}
	if len(merged) != 2 {
		t.Errorf("expected 2 apps, got %d", len(merged))
	}
	if len(merged["crm"]) != 1 || merged["crm"][0].ID != 20 {
		t.Errorf("crm: unexpected repos %v", merged["crm"])
	}
}

func TestMergeAppReposCache_addsNewRepoToExistingApp(t *testing.T) {
	cached := map[string][]RepoInfo{
		"pfm": {{Name: "pfm-api", ID: 10}},
	}
	fresh := map[string][]RepoInfo{
		"pfm": {{Name: "pfm-api", ID: 10}, {Name: "pfm-service", ID: 11}}, // pfm-service is new
	}

	merged, changed := MergeAppReposCache(cached, fresh)
	if !changed {
		t.Error("expected changed=true when new repo added")
	}
	if len(merged["pfm"]) != 2 {
		t.Errorf("pfm: expected 2 repos, got %d", len(merged["pfm"]))
	}
}

func TestMergeAppReposCache_noChange(t *testing.T) {
	cached := map[string][]RepoInfo{"pfm": {{Name: "pfm-api", ID: 10}}}
	fresh := map[string][]RepoInfo{"pfm": {{Name: "pfm-api", ID: 10}}}

	_, changed := MergeAppReposCache(cached, fresh)
	if changed {
		t.Error("expected changed=false when nothing new")
	}
}

// TestMergeAppReposCache_neverRemovesApps verifies that an app or repo deleted
// from GitLab is retained in the cache (append-only / option B).
func TestMergeAppReposCache_neverRemovesApps(t *testing.T) {
	cached := map[string][]RepoInfo{
		"pfm":    {{Name: "pfm-api", ID: 10}},
		"legacy": {{Name: "legacy-svc", ID: 99}}, // gone from GitLab
	}
	fresh := map[string][]RepoInfo{
		"pfm": {{Name: "pfm-api", ID: 10}},
	}

	merged, changed := MergeAppReposCache(cached, fresh)
	if changed {
		t.Error("expected changed=false — removing an app is not a cache change")
	}
	if _, ok := merged["legacy"]; !ok {
		t.Error("legacy app should be retained in cache (append-only)")
	}
	if len(merged) != 2 {
		t.Errorf("expected 2 apps (nothing removed), got %d", len(merged))
	}
}

func TestMergeAppReposCache_doesNotMutateInputs(t *testing.T) {
	cached := map[string][]RepoInfo{"pfm": {{Name: "pfm-api", ID: 10}}}
	fresh := map[string][]RepoInfo{
		"pfm": {{Name: "pfm-api", ID: 10}},
		"crm": {{Name: "crm-backend", ID: 20}},
	}

	MergeAppReposCache(cached, fresh)

	if len(cached) != 1 {
		t.Errorf("cached was mutated: now has %d apps", len(cached))
	}
}

func TestMergeAppReposCache_bothEmpty(t *testing.T) {
	merged, changed := MergeAppReposCache(map[string][]RepoInfo{}, map[string][]RepoInfo{})
	if changed {
		t.Error("expected changed=false for two empty maps")
	}
	if len(merged) != 0 {
		t.Errorf("expected empty result, got %v", merged)
	}
}

// ---- LoadAppReposCache / SaveAppReposCache ----

func TestLoadAppReposCache_missingFile(t *testing.T) {
	m := LoadAppReposCache("/nonexistent/path/cache.json")
	if len(m) != 0 {
		t.Errorf("expected empty map for missing file, got %v", m)
	}
}

func TestLoadAppReposCache_corruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")
	os.WriteFile(path, []byte(`{not valid json`), 0o644)

	m := LoadAppReposCache(path)
	if len(m) != 0 {
		t.Errorf("expected empty map for corrupt file, got %v", m)
	}
}

func TestSaveAndLoadAppReposRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")

	cache := map[string][]RepoInfo{
		"pfm": {{Name: "pfm-api", ID: 10}, {Name: "pfm-service", ID: 11}},
		"crm": {{Name: "crm-backend", ID: 20}},
	}
	if err := SaveAppReposCache(path, cache); err != nil {
		t.Fatalf("SaveAppReposCache: %v", err)
	}

	loaded := LoadAppReposCache(path)
	if len(loaded) != 2 {
		t.Fatalf("expected 2 apps after roundtrip, got %d", len(loaded))
	}
	if len(loaded["pfm"]) != 2 {
		t.Errorf("pfm: expected 2 repos, got %d", len(loaded["pfm"]))
	}
	if loaded["pfm"][0].ID != 10 {
		t.Errorf("pfm[0]: expected ID=10, got %d", loaded["pfm"][0].ID)
	}
}

func TestSaveAppReposCache_writesValidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")

	if err := SaveAppReposCache(path, map[string][]RepoInfo{
		"pfm": {{Name: "pfm-api", ID: 10}},
	}); err != nil {
		t.Fatalf("SaveAppReposCache: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	var m map[string][]RepoInfo
	if err := json.Unmarshal(data, &m); err != nil {
		t.Errorf("written file is not valid JSON: %v", err)
	}
}
