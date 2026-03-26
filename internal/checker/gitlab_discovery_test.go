package checker

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

// ---- DiscoverGroupProjects ----

// TestDiscoverGroupProjects_success verifies that all projects on a single page
// are returned with the correct path→ID mapping.
func TestDiscoverGroupProjects_success(t *testing.T) {
	srv := glMux(map[string]http.HandlerFunc{
		"/api/v4/groups/5/projects": func(w http.ResponseWriter, r *http.Request) {
			// Verify expected query params.
			q := r.URL.Query()
			if q.Get("include_subgroups") != "true" {
				t.Errorf("expected include_subgroups=true, got %q", q.Get("include_subgroups"))
			}
			if q.Get("per_page") != "100" {
				t.Errorf("expected per_page=100, got %q", q.Get("per_page"))
			}
			// No X-Next-Page → single page.
			writeJSON(w, []glProject{
				{ID: 10, Path: "pfm"},
				{ID: 20, Path: "crm"},
				{ID: 30, Path: "hrm"},
			})
		},
	})
	defer srv.Close()

	c := newGitLabChecker(srv)
	projects, err := c.DiscoverGroupProjects(context.Background(), 5)
	if err != nil {
		t.Fatalf("DiscoverGroupProjects: %v", err)
	}
	if len(projects) != 3 {
		t.Fatalf("expected 3 projects, got %d", len(projects))
	}
	if projects["pfm"] != 10 {
		t.Errorf("pfm: expected ID=10, got %d", projects["pfm"])
	}
	if projects["crm"] != 20 {
		t.Errorf("crm: expected ID=20, got %d", projects["crm"])
	}
	if projects["hrm"] != 30 {
		t.Errorf("hrm: expected ID=30, got %d", projects["hrm"])
	}
}

// TestDiscoverGroupProjects_pagination verifies that multiple pages are fetched
// and merged into a single result map.
func TestDiscoverGroupProjects_pagination(t *testing.T) {
	srv := glMux(map[string]http.HandlerFunc{
		"/api/v4/groups/5/projects": func(w http.ResponseWriter, r *http.Request) {
			page := r.URL.Query().Get("page")
			switch page {
			case "1", "": // page 1
				w.Header().Set("X-Next-Page", "2")
				writeJSON(w, []glProject{{ID: 10, Path: "pfm"}})
			case "2": // page 2 — last page, no X-Next-Page
				writeJSON(w, []glProject{{ID: 20, Path: "crm"}})
			default:
				http.Error(w, "unexpected page", http.StatusBadRequest)
			}
		},
	})
	defer srv.Close()

	c := newGitLabChecker(srv)
	projects, err := c.DiscoverGroupProjects(context.Background(), 5)
	if err != nil {
		t.Fatalf("DiscoverGroupProjects: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("expected 2 projects across 2 pages, got %d", len(projects))
	}
	if projects["pfm"] != 10 || projects["crm"] != 20 {
		t.Errorf("unexpected project map: %v", projects)
	}
}

// TestDiscoverGroupProjects_emptyGroup verifies that an empty group returns an
// empty map without error.
func TestDiscoverGroupProjects_emptyGroup(t *testing.T) {
	srv := glMux(map[string]http.HandlerFunc{
		"/api/v4/groups/5/projects": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glProject{})
		},
	})
	defer srv.Close()

	c := newGitLabChecker(srv)
	projects, err := c.DiscoverGroupProjects(context.Background(), 5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(projects) != 0 {
		t.Errorf("expected empty map, got %v", projects)
	}
}

// TestDiscoverGroupProjects_apiError verifies that an API error is returned as-is.
func TestDiscoverGroupProjects_apiError(t *testing.T) {
	srv := glMux(map[string]http.HandlerFunc{
		"/api/v4/groups/5/projects": func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, `{"message":"403 Forbidden"}`, http.StatusForbidden)
		},
	})
	defer srv.Close()

	c := newGitLabChecker(srv)
	_, err := c.DiscoverGroupProjects(context.Background(), 5)
	if err == nil {
		t.Fatal("expected error from API, got nil")
	}
}

// TestDiscoverGroupProjects_authHeader verifies the PAT is sent on discovery requests.
func TestDiscoverGroupProjects_authHeader(t *testing.T) {
	srv := glMux(map[string]http.HandlerFunc{
		"/api/v4/groups/5/projects": func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("PRIVATE-TOKEN") != "test-token" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			writeJSON(w, []glProject{{ID: 10, Path: "pfm"}})
		},
	})
	defer srv.Close()

	c := newGitLabChecker(srv)
	projects, err := c.DiscoverGroupProjects(context.Background(), 5)
	if err != nil {
		t.Fatalf("DiscoverGroupProjects: %v", err)
	}
	if len(projects) == 0 {
		t.Error("expected projects to be returned when auth header is correct")
	}
}

// ---- MergeProjectCache ----

func TestMergeProjectCache_addsNewProjects(t *testing.T) {
	cached := map[string]int{"pfm": 10, "crm": 20}
	fresh := map[string]int{"pfm": 10, "crm": 20, "hrm": 30} // hrm is new

	merged, changed := MergeProjectCache(cached, fresh)
	if !changed {
		t.Error("expected changed=true when new project added")
	}
	if len(merged) != 3 {
		t.Errorf("expected 3 entries, got %d", len(merged))
	}
	if merged["hrm"] != 30 {
		t.Errorf("hrm: expected ID=30, got %d", merged["hrm"])
	}
}

func TestMergeProjectCache_noChange(t *testing.T) {
	cached := map[string]int{"pfm": 10, "crm": 20}
	fresh := map[string]int{"pfm": 10, "crm": 20}

	_, changed := MergeProjectCache(cached, fresh)
	if changed {
		t.Error("expected changed=false when nothing new")
	}
}

// TestMergeProjectCache_neverRemoves verifies option B: a project deleted from the
// group is retained in the cache to preserve long-term report integrity.
func TestMergeProjectCache_neverRemoves(t *testing.T) {
	cached := map[string]int{"pfm": 10, "crm": 20, "legacy": 99}
	fresh := map[string]int{"pfm": 10, "crm": 20} // "legacy" gone from GitLab

	merged, changed := MergeProjectCache(cached, fresh)
	if changed {
		t.Error("expected changed=false — removing a project from GitLab is not a cache change")
	}
	if _, ok := merged["legacy"]; !ok {
		t.Error("legacy project should be retained in merged cache (option B)")
	}
	if len(merged) != 3 {
		t.Errorf("expected 3 entries (nothing removed), got %d", len(merged))
	}
}

// TestMergeProjectCache_doesNotMutateInputs verifies that cached and fresh are
// not modified by the merge operation.
func TestMergeProjectCache_doesNotMutateInputs(t *testing.T) {
	cached := map[string]int{"pfm": 10}
	fresh := map[string]int{"pfm": 10, "crm": 20}

	MergeProjectCache(cached, fresh)

	if len(cached) != 1 {
		t.Errorf("cached was mutated: now has %d entries", len(cached))
	}
}

// TestMergeProjectCache_bothEmpty verifies merging two empty maps is a no-op.
func TestMergeProjectCache_bothEmpty(t *testing.T) {
	merged, changed := MergeProjectCache(map[string]int{}, map[string]int{})
	if changed {
		t.Error("expected changed=false for two empty maps")
	}
	if len(merged) != 0 {
		t.Errorf("expected empty result, got %v", merged)
	}
}

// ---- LoadProjectCache / SaveProjectCache ----

func TestLoadProjectCache_missingFile(t *testing.T) {
	m := LoadProjectCache("/nonexistent/path/cache.json")
	if len(m) != 0 {
		t.Errorf("expected empty map for missing file, got %v", m)
	}
}

func TestLoadProjectCache_corruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")
	os.WriteFile(path, []byte(`{not valid json`), 0o644)

	m := LoadProjectCache(path)
	if len(m) != 0 {
		t.Errorf("expected empty map for corrupt file, got %v", m)
	}
}

func TestSaveAndLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")

	projects := map[string]int{"pfm": 10, "crm": 20, "hrm": 30}
	if err := SaveProjectCache(path, projects); err != nil {
		t.Fatalf("SaveProjectCache: %v", err)
	}

	loaded := LoadProjectCache(path)
	if len(loaded) != 3 {
		t.Fatalf("expected 3 entries after roundtrip, got %d", len(loaded))
	}
	for name, id := range projects {
		if loaded[name] != id {
			t.Errorf("%s: expected %d, got %d", name, id, loaded[name])
		}
	}
}

func TestSaveProjectCache_writesValidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cache.json")

	if err := SaveProjectCache(path, map[string]int{"pfm": 10}); err != nil {
		t.Fatalf("SaveProjectCache: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading file: %v", err)
	}
	var m map[string]int
	if err := json.Unmarshal(data, &m); err != nil {
		t.Errorf("written file is not valid JSON: %v", err)
	}
}

// TestLoadProjectCache_fullIntegrationWithDiscover verifies that the output of
// DiscoverGroupProjects can be saved and loaded without loss.
func TestLoadProjectCache_fullIntegrationWithDiscover(t *testing.T) {
	srv := glMux(map[string]http.HandlerFunc{
		"/api/v4/groups/5/projects": func(w http.ResponseWriter, r *http.Request) {
			writeJSON(w, []glProject{
				{ID: 10, Path: "pfm"},
				{ID: 20, Path: "crm"},
			})
		},
	})
	defer srv.Close()

	c := newGitLabChecker(srv)
	fresh, err := c.DiscoverGroupProjects(context.Background(), 5)
	if err != nil {
		t.Fatalf("DiscoverGroupProjects: %v", err)
	}

	dir := t.TempDir()
	cachePath := filepath.Join(dir, "gitlab-projects-cache.json")

	cached := LoadProjectCache(cachePath) // empty — first run
	merged, changed := MergeProjectCache(cached, fresh)
	if !changed {
		t.Error("expected changed=true on first run (cache was empty)")
	}
	if err := SaveProjectCache(cachePath, merged); err != nil {
		t.Fatalf("SaveProjectCache: %v", err)
	}

	// Second run: same group, nothing new.
	reloaded := LoadProjectCache(cachePath)
	_, changed2 := MergeProjectCache(reloaded, fresh)
	if changed2 {
		t.Error("expected changed=false on second run with same projects")
	}

	// Third run: new project appears.
	freshWithNew := map[string]int{"pfm": 10, "crm": 20, "hrm": 30}
	merged3, changed3 := MergeProjectCache(reloaded, freshWithNew)
	if !changed3 {
		t.Error("expected changed=true when new project appears")
	}
	if merged3["hrm"] != 30 {
		t.Errorf("hrm should be in merged cache after new project detected")
	}
	// Legacy project still present even if dropped from group.
	if len(merged3) != 3 {
		t.Errorf("expected 3 entries, got %d: %v", len(merged3), merged3)
	}
}
