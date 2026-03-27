package checker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"
)

// ---- Public types ----

// RepoInfo identifies a single GitLab repository within an app sub-group.
type RepoInfo struct {
	Name string `json:"name"`
	ID   int    `json:"id"`
}

// AppRepos groups a set of repos that belong to a single logical application.
type AppRepos struct {
	AppName string
	Repos   []RepoInfo
}

// PipelineInfo holds the key fields of the most recent GitLab pipeline run.
type PipelineInfo struct {
	ID        int       `json:"id"`
	Status    string    `json:"status"`
	Ref       string    `json:"ref"`
	CreatedAt time.Time `json:"createdAt"`
	WebURL    string    `json:"webURL"`
	Duration  int       `json:"duration"` // seconds; 0 when pipeline is still running
}

// JobInfo holds the key fields of a single job within a pipeline.
type JobInfo struct {
	Name     string  `json:"name"`
	Stage    string  `json:"stage"`
	Status   string  `json:"status"`
	Duration float64 `json:"duration"` // seconds; 0 when job has not yet finished
}

// RunnerStatus is the health snapshot for a single GitLab runner.
// Kept for future global-runner tracking; not used in per-app checks.
type RunnerStatus struct {
	ID          int
	Description string
	Active      bool
	ContactedAt time.Time
	Stale       bool // true when ContactedAt is older than RunnerStalenessMin
}

// RepoStatus is the per-repo health snapshot within a GitLab app sub-group.
type RepoStatus struct {
	RepoName          string
	ProjectID         int
	LastPipeline      *PipelineInfo  // nil if no pipelines exist
	LastPipelineJobs  []JobInfo      // jobs from the most recent pipeline
	FailedJobsByStage map[string]int // stage → count within FailureWindow
	Error             string         // non-empty when any API call for this repo failed
}

// GitLabAppStatus is the health snapshot for a single app from GitLab's perspective.
type GitLabAppStatus struct {
	AppName string
	Repos   []RepoStatus
	Error   string // non-empty for group/discovery-level errors
}

// ---- Checker ----

// GitLabChecker queries a self-hosted GitLab REST API for CI health data.
type GitLabChecker struct {
	Client             HTTPClient
	BaseURL            string // e.g. "https://gitlab.example.com"
	Token              string // GitLab Personal Access Token
	FailureWindow      time.Duration
	RunnerStalenessMin int // minutes before a runner is considered stale (future use)

	// Now returns the current time. Defaults to time.Now if nil.
	Now func() time.Time
}

// Check queries GitLab for each app's repos and returns a per-app status slice.
// Per-repo errors are captured in RepoStatus.Error; the method itself only
// returns an error for programming mistakes (nil receiver, etc.).
func (c *GitLabChecker) Check(ctx context.Context, apps []AppRepos) ([]GitLabAppStatus, error) {
	now := c.now()
	results := make([]GitLabAppStatus, 0, len(apps))

	for _, app := range apps {
		status := GitLabAppStatus{AppName: app.AppName}
		status.Repos = make([]RepoStatus, 0, len(app.Repos))
		for _, repo := range app.Repos {
			status.Repos = append(status.Repos, c.checkRepo(ctx, repo, now))
		}
		results = append(results, status)
	}

	return results, nil
}

// checkRepo fetches pipeline, job, and failure data for a single repo.
func (c *GitLabChecker) checkRepo(ctx context.Context, repo RepoInfo, now time.Time) RepoStatus {
	rs := RepoStatus{
		RepoName:          repo.Name,
		ProjectID:         repo.ID,
		FailedJobsByStage: make(map[string]int),
	}

	pipeline, err := c.getLastPipeline(ctx, repo.ID)
	if err != nil {
		rs.Error = err.Error()
		return rs
	}
	rs.LastPipeline = pipeline

	if pipeline != nil {
		jobs, err := c.getLastPipelineJobs(ctx, repo.ID, pipeline.ID)
		if err != nil {
			rs.Error = err.Error()
			return rs
		}
		rs.LastPipelineJobs = jobs
	}

	since := now.Add(-c.FailureWindow)
	failedByStage, err := c.getFailedJobsByStage(ctx, repo.ID, since)
	if err != nil {
		rs.Error = err.Error()
		return rs
	}
	rs.FailedJobsByStage = failedByStage

	return rs
}

func (c *GitLabChecker) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// ---- GitLab API response types (minimal) ----

type glPipeline struct {
	ID        int    `json:"id"`
	Status    string `json:"status"`
	Ref       string `json:"ref"`
	CreatedAt string `json:"created_at"`
	WebURL    string `json:"web_url"`
	Duration  int    `json:"duration"` // seconds; 0 when still running
}

type glJob struct {
	Name      string  `json:"name"`
	Status    string  `json:"status"`
	Stage     string  `json:"stage"`
	Duration  float64 `json:"duration"` // seconds; null decodes as 0
	CreatedAt string  `json:"created_at"`
}

type glRunner struct {
	ID          int    `json:"id"`
	Description string `json:"description"`
	Active      bool   `json:"active"`
	ContactedAt string `json:"contacted_at"`
}

type glProject struct {
	ID   int    `json:"id"`
	Path string `json:"path"` // slug only (e.g. "pfm"), not the full namespace path
}

type glSubgroup struct {
	ID   int    `json:"id"`
	Path string `json:"path"` // slug, becomes the app name
}

// ---- API call helpers ----

func (c *GitLabChecker) getLastPipeline(ctx context.Context, projectID int) (*PipelineInfo, error) {
	path := fmt.Sprintf("/api/v4/projects/%d/pipelines", projectID)
	params := url.Values{
		"per_page": {"1"},
		"order_by": {"id"},
		"sort":     {"desc"},
	}

	var pipelines []glPipeline
	if err := c.get(ctx, path, params, &pipelines); err != nil {
		return nil, err
	}
	if len(pipelines) == 0 {
		return nil, nil
	}

	p := pipelines[0]
	createdAt, err := time.Parse(time.RFC3339, p.CreatedAt)
	if err != nil {
		return nil, fmt.Errorf("parsing pipeline created_at %q: %w", p.CreatedAt, err)
	}

	return &PipelineInfo{
		ID:        p.ID,
		Status:    p.Status,
		Ref:       p.Ref,
		CreatedAt: createdAt,
		WebURL:    p.WebURL,
		Duration:  p.Duration,
	}, nil
}

func (c *GitLabChecker) getLastPipelineJobs(ctx context.Context, projectID, pipelineID int) ([]JobInfo, error) {
	path := fmt.Sprintf("/api/v4/projects/%d/pipelines/%d/jobs", projectID, pipelineID)
	params := url.Values{"per_page": {"100"}}

	var raw []glJob
	if err := c.get(ctx, path, params, &raw); err != nil {
		return nil, err
	}

	jobs := make([]JobInfo, 0, len(raw))
	for _, j := range raw {
		jobs = append(jobs, JobInfo{
			Name:     j.Name,
			Stage:    j.Stage,
			Status:   j.Status,
			Duration: j.Duration,
		})
	}
	return jobs, nil
}

func (c *GitLabChecker) getFailedJobsByStage(ctx context.Context, projectID int, since time.Time) (map[string]int, error) {
	path := fmt.Sprintf("/api/v4/projects/%d/jobs", projectID)
	params := url.Values{
		"per_page": {"100"},
		"scope[]":  {"failed"},
	}

	var jobs []glJob
	if err := c.get(ctx, path, params, &jobs); err != nil {
		return nil, err
	}

	result := make(map[string]int)
	for _, j := range jobs {
		createdAt, err := time.Parse(time.RFC3339, j.CreatedAt)
		if err != nil {
			continue
		}
		if createdAt.Before(since) {
			continue
		}
		result[j.Stage]++
	}
	return result, nil
}

// getRunners fetches project-level runners. Kept for future global-runner tracking.
func (c *GitLabChecker) getRunners(ctx context.Context, projectID int, now time.Time) ([]RunnerStatus, error) {
	path := fmt.Sprintf("/api/v4/projects/%d/runners", projectID)
	params := url.Values{
		"per_page": {"100"},
	}

	var runners []glRunner
	if err := c.get(ctx, path, params, &runners); err != nil {
		return nil, err
	}

	stalenessThreshold := time.Duration(c.RunnerStalenessMin) * time.Minute
	result := make([]RunnerStatus, 0, len(runners))
	for _, r := range runners {
		rs := RunnerStatus{
			ID:          r.ID,
			Description: r.Description,
			Active:      r.Active,
		}
		if r.ContactedAt != "" {
			if contactedAt, err := time.Parse(time.RFC3339, r.ContactedAt); err == nil {
				rs.ContactedAt = contactedAt
				rs.Stale = now.Sub(contactedAt) > stalenessThreshold
			}
		}
		result = append(result, rs)
	}
	return result, nil
}

// ---- Sub-group / project discovery ----

// DiscoverAppRepos fetches sub-groups of the root group (one per app) and the
// projects within each sub-group (one per repo). It returns a map of
// appName → []RepoInfo for building or refreshing the project cache.
func (c *GitLabChecker) DiscoverAppRepos(ctx context.Context, groupID int) (map[string][]RepoInfo, error) {
	var subgroups []glSubgroup
	for page := 1; ; {
		batch, nextPage, err := c.getSubgroupsPage(ctx, groupID, page)
		if err != nil {
			return nil, err
		}
		subgroups = append(subgroups, batch...)
		if nextPage == 0 {
			break
		}
		page = nextPage
	}

	result := make(map[string][]RepoInfo, len(subgroups))
	for _, sg := range subgroups {
		var projects []glProject
		for page := 1; ; {
			batch, nextPage, err := c.getGroupProjectsPage(ctx, sg.ID, page)
			if err != nil {
				return nil, fmt.Errorf("listing projects for sub-group %q: %w", sg.Path, err)
			}
			projects = append(projects, batch...)
			if nextPage == 0 {
				break
			}
			page = nextPage
		}
		repos := make([]RepoInfo, 0, len(projects))
		for _, p := range projects {
			repos = append(repos, RepoInfo{Name: p.Path, ID: p.ID})
		}
		result[sg.Path] = repos
	}
	return result, nil
}

// getSubgroupsPage fetches one page of sub-groups from a group.
func (c *GitLabChecker) getSubgroupsPage(ctx context.Context, groupID, page int) ([]glSubgroup, int, error) {
	rawURL := fmt.Sprintf("%s/api/v4/groups/%d/subgroups?per_page=100&page=%d",
		c.BaseURL, groupID, page)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", c.Token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("fetching subgroups for group %d: %w", groupID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, 0, fmt.Errorf("API groups/%d/subgroups returned %d: %s", groupID, resp.StatusCode, string(body))
	}

	var sgs []glSubgroup
	if err := json.NewDecoder(resp.Body).Decode(&sgs); err != nil {
		return nil, 0, fmt.Errorf("decoding subgroups response: %w", err)
	}

	nextPage := 0
	if next := resp.Header.Get("X-Next-Page"); next != "" {
		nextPage, _ = strconv.Atoi(next)
	}

	return sgs, nextPage, nil
}

// getGroupProjectsPage fetches one page of projects directly in a group (not recursive).
func (c *GitLabChecker) getGroupProjectsPage(ctx context.Context, groupID, page int) ([]glProject, int, error) {
	rawURL := fmt.Sprintf("%s/api/v4/groups/%d/projects?per_page=100&page=%d",
		c.BaseURL, groupID, page)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", c.Token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("fetching projects for group %d: %w", groupID, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, 0, fmt.Errorf("API groups/%d/projects returned %d: %s", groupID, resp.StatusCode, string(body))
	}

	var projects []glProject
	if err := json.NewDecoder(resp.Body).Decode(&projects); err != nil {
		return nil, 0, fmt.Errorf("decoding projects response: %w", err)
	}

	nextPage := 0
	if next := resp.Header.Get("X-Next-Page"); next != "" {
		nextPage, _ = strconv.Atoi(next)
	}

	return projects, nextPage, nil
}

// ---- App repos cache ----

// LoadAppReposCache reads the cached app→repos map from disk.
// Returns an empty map if the file is missing or unreadable.
func LoadAppReposCache(path string) map[string][]RepoInfo {
	data, err := os.ReadFile(path)
	if err != nil {
		return make(map[string][]RepoInfo)
	}
	var m map[string][]RepoInfo
	if err := json.Unmarshal(data, &m); err != nil {
		return make(map[string][]RepoInfo)
	}
	return m
}

// MergeAppReposCache merges fresh discoveries into the cached map.
// Option B: existing apps and repos are never removed to preserve long-term
// report integrity. Returns the merged map and changed=true when anything new
// was added.
func MergeAppReposCache(cached, fresh map[string][]RepoInfo) (map[string][]RepoInfo, bool) {
	merged := make(map[string][]RepoInfo, len(cached))
	for k, v := range cached {
		cp := make([]RepoInfo, len(v))
		copy(cp, v)
		merged[k] = cp
	}

	changed := false
	for appName, freshRepos := range fresh {
		existing, ok := merged[appName]
		if !ok {
			// Brand-new app.
			cp := make([]RepoInfo, len(freshRepos))
			copy(cp, freshRepos)
			merged[appName] = cp
			changed = true
			continue
		}
		// Add repos that aren't already cached (match by ID).
		existingIDs := make(map[int]struct{}, len(existing))
		for _, r := range existing {
			existingIDs[r.ID] = struct{}{}
		}
		for _, r := range freshRepos {
			if _, found := existingIDs[r.ID]; !found {
				existing = append(existing, r)
				changed = true
			}
		}
		merged[appName] = existing
	}
	return merged, changed
}

// SaveAppReposCache writes the app→repos map to disk as JSON.
func SaveAppReposCache(path string, cache map[string][]RepoInfo) error {
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return fmt.Errorf("marshalling app repos cache: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("writing app repos cache: %w", err)
	}
	return nil
}

// ---- HTTP helper ----

func (c *GitLabChecker) get(ctx context.Context, path string, params url.Values, out any) error {
	rawURL := c.BaseURL + path
	if len(params) > 0 {
		rawURL += "?" + params.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return fmt.Errorf("creating request: %w", err)
	}
	req.Header.Set("PRIVATE-TOKEN", c.Token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.Client.Do(req)
	if err != nil {
		return fmt.Errorf("executing request to %s: %w", path, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("API %s returned %d: %s", path, resp.StatusCode, string(body))
	}

	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decoding response from %s: %w", path, err)
	}
	return nil
}
