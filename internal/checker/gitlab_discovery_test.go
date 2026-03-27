package checker

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// ---- DiscoverAppRepos ----

// TestDiscoverAppRepos_success verifies that sub-groups are fetched and then
// their projects are fetched to build the app→repos map.
func TestDiscoverAppRepos_success(t *testing.T) {
	srv := glMux(map[string]http.HandlerFunc{
		"/api/v4/groups/5/subgroups": func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("PRIVATE-TOKEN") != "test-token" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			writeJSON(w, []glSubgroup{
				{ID: 10, Path: "pfm"},
				{ID: 20, Path: "crm"},
			})
		},
		"/api/v4/groups/10/projects": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glProject{
				{ID: 101, Path: "pfm-api"},
				{ID: 102, Path: "pfm-service"},
			})
		},
		"/api/v4/groups/20/projects": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glProject{
				{ID: 201, Path: "crm-backend"},
			})
		},
	})
	defer srv.Close()

	c := newGitLabChecker(srv)
	result, err := c.DiscoverAppRepos(context.Background(), 5)
	if err != nil {
		t.Fatalf("DiscoverAppRepos: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 apps, got %d", len(result))
	}
	if len(result["pfm"]) != 2 {
		t.Errorf("pfm: expected 2 repos, got %d", len(result["pfm"]))
	}
	if len(result["crm"]) != 1 {
		t.Errorf("crm: expected 1 repo, got %d", len(result["crm"]))
	}
	if result["pfm"][0].Name != "pfm-api" || result["pfm"][0].ID != 101 {
		t.Errorf("pfm[0]: expected pfm-api/101, got %+v", result["pfm"][0])
	}
	if result["crm"][0].Name != "crm-backend" || result["crm"][0].ID != 201 {
		t.Errorf("crm[0]: expected crm-backend/201, got %+v", result["crm"][0])
	}
}

// TestDiscoverAppRepos_pagination verifies that both the subgroup and project
// pages are fetched when X-Next-Page is set.
func TestDiscoverAppRepos_pagination(t *testing.T) {
	srv := glMux(map[string]http.HandlerFunc{
		"/api/v4/groups/5/subgroups": func(w http.ResponseWriter, r *http.Request) {
			page := r.URL.Query().Get("page")
			switch page {
			case "1", "":
				w.Header().Set("X-Next-Page", "2")
				writeJSON(w, []glSubgroup{{ID: 10, Path: "pfm"}})
			case "2":
				writeJSON(w, []glSubgroup{{ID: 20, Path: "crm"}})
			default:
				http.Error(w, "unexpected page", http.StatusBadRequest)
			}
		},
		"/api/v4/groups/10/projects": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glProject{{ID: 101, Path: "pfm-api"}})
		},
		"/api/v4/groups/20/projects": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glProject{{ID: 201, Path: "crm-backend"}})
		},
	})
	defer srv.Close()

	c := newGitLabChecker(srv)
	result, err := c.DiscoverAppRepos(context.Background(), 5)
	if err != nil {
		t.Fatalf("DiscoverAppRepos: %v", err)
	}
	if len(result) != 2 {
		t.Fatalf("expected 2 apps across 2 subgroup pages, got %d", len(result))
	}
}

// TestDiscoverAppRepos_emptyGroup verifies that a group with no sub-groups
// returns an empty map without error.
func TestDiscoverAppRepos_emptyGroup(t *testing.T) {
	srv := glMux(map[string]http.HandlerFunc{
		"/api/v4/groups/5/subgroups": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glSubgroup{})
		},
	})
	defer srv.Close()

	c := newGitLabChecker(srv)
	result, err := c.DiscoverAppRepos(context.Background(), 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result) != 0 {
		t.Errorf("expected empty map, got %v", result)
	}
}

// TestDiscoverAppRepos_apiError verifies that a subgroup API error is returned.
func TestDiscoverAppRepos_apiError(t *testing.T) {
	srv := glMux(map[string]http.HandlerFunc{
		"/api/v4/groups/5/subgroups": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"message":"403 Forbidden"}`, http.StatusForbidden)
		},
	})
	defer srv.Close()

	c := newGitLabChecker(srv)
	_, err := c.DiscoverAppRepos(context.Background(), 5)
	if err == nil {
		t.Fatal("expected error from API, got nil")
	}
}

// TestDiscoverAppRepos_projectsApiError verifies that a project API error
// within a sub-group is propagated.
func TestDiscoverAppRepos_projectsApiError(t *testing.T) {
	srv := glMux(map[string]http.HandlerFunc{
		"/api/v4/groups/5/subgroups": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glSubgroup{{ID: 10, Path: "pfm"}})
		},
		"/api/v4/groups/10/projects": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"message":"503 Service Unavailable"}`, http.StatusServiceUnavailable)
		},
	})
	defer srv.Close()

	c := newGitLabChecker(srv)
	_, err := c.DiscoverAppRepos(context.Background(), 5)
	if err == nil {
		t.Fatal("expected error from projects API, got nil")
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

// TestMergeAppReposCache_neverRemovesApps verifies option B: an app or repo
// deleted from GitLab is retained in the cache.
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
		t.Error("legacy app should be retained in cache (option B)")
	}
	if len(merged) != 2 {
		t.Errorf("expected 2 apps (nothing removed), got %d", len(merged))
	}
}

// TestMergeAppReposCache_doesNotMutateInputs verifies cached and fresh are not
// modified.
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

// TestDiscoverAndCacheIntegration verifies the full discover→merge→save→reload
// workflow across three simulated runs.
func TestDiscoverAndCacheIntegration(t *testing.T) {
	srv := glMux(map[string]http.HandlerFunc{
		"/api/v4/groups/5/subgroups": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glSubgroup{
				{ID: 10, Path: "pfm"},
				{ID: 20, Path: "crm"},
			})
		},
		"/api/v4/groups/10/projects": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glProject{{ID: 101, Path: "pfm-api"}})
		},
		"/api/v4/groups/20/projects": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glProject{{ID: 201, Path: "crm-backend"}})
		},
	})
	defer srv.Close()

	c := newGitLabChecker(srv)
	dir := t.TempDir()
	cachePath := filepath.Join(dir, "gitlab-projects-cache.json")

	// First run: cache is empty.
	cached := LoadAppReposCache(cachePath)
	fresh, err := c.DiscoverAppRepos(context.Background(), 5)
	if err != nil {
		t.Fatalf("DiscoverAppRepos: %v", err)
	}
	merged, changed := MergeAppReposCache(cached, fresh)
	if !changed {
		t.Error("expected changed=true on first run (cache was empty)")
	}
	if err := SaveAppReposCache(cachePath, merged); err != nil {
		t.Fatalf("SaveAppReposCache: %v", err)
	}

	// Second run: same group, nothing new.
	reloaded := LoadAppReposCache(cachePath)
	_, changed2 := MergeAppReposCache(reloaded, fresh)
	if changed2 {
		t.Error("expected changed=false on second run with same apps")
	}

	// Third run: new repo appears in pfm.
	freshWithNew := map[string][]RepoInfo{
		"pfm": {{Name: "pfm-api", ID: 101}, {Name: "pfm-service", ID: 102}},
		"crm": {{Name: "crm-backend", ID: 201}},
	}
	merged3, changed3 := MergeAppReposCache(reloaded, freshWithNew)
	if !changed3 {
		t.Error("expected changed=true when new repo appears")
	}
	if len(merged3["pfm"]) != 2 {
		t.Errorf("pfm: expected 2 repos after new repo added, got %d", len(merged3["pfm"]))
	}
	// Legacy app/repo not deleted (option B).
	if len(merged3) < 2 {
		t.Errorf("expected at least 2 apps retained, got %d", len(merged3))
	}
}
