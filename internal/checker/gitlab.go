package checker

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"platform-monitor/internal/config"
)

// ---- Public types ----

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
type RunnerStatus struct {
	ID          int
	Description string
	Active      bool
	ContactedAt time.Time
	Stale       bool // true when ContactedAt is older than RunnerStalenessMin
}

// GitLabAppStatus is the health snapshot for a single app from GitLab's perspective.
type GitLabAppStatus struct {
	AppName           string
	ProjectID         int
	LastPipeline      *PipelineInfo  // nil if no pipelines exist
	LastPipelineJobs  []JobInfo      // jobs from the most recent pipeline
	FailedJobsByStage map[string]int // stage → count within FailureWindow
	Runners           []RunnerStatus
	Error             string // non-empty when any API call for this app failed
}

// ---- Checker ----

// GitLabChecker queries a self-hosted GitLab REST API for CI health data.
type GitLabChecker struct {
	Client             HTTPClient
	BaseURL            string // e.g. "https://gitlab.example.com"
	Token              string // GitLab Personal Access Token
	FailureWindow      time.Duration
	RunnerStalenessMin int // minutes before a runner is considered stale

	// Now returns the current time. Defaults to time.Now if nil.
	Now func() time.Time
}

// Check queries GitLab for each app and returns a per-app status slice.
// Individual app errors are captured in GitLabAppStatus.Error; the method
// itself only returns an error for programming mistakes (nil receiver, etc.).
func (c *GitLabChecker) Check(ctx context.Context, apps []config.App) ([]GitLabAppStatus, error) {
	now := c.now()
	results := make([]GitLabAppStatus, 0, len(apps))

	for _, app := range apps {
		status := GitLabAppStatus{
			AppName:   app.Name,
			ProjectID: app.GitLabProjectID,
		}

		pipeline, err := c.getLastPipeline(ctx, app.GitLabProjectID)
		if err != nil {
			status.Error = err.Error()
			results = append(results, status)
			continue
		}
		status.LastPipeline = pipeline

		if pipeline != nil {
			jobs, err := c.getLastPipelineJobs(ctx, app.GitLabProjectID, pipeline.ID)
			if err != nil {
				status.Error = err.Error()
				results = append(results, status)
				continue
			}
			status.LastPipelineJobs = jobs
		}

		since := now.Add(-c.FailureWindow)
		failedByStage, err := c.getFailedJobsByStage(ctx, app.GitLabProjectID, since)
		if err != nil {
			status.Error = err.Error()
			results = append(results, status)
			continue
		}
		status.FailedJobsByStage = failedByStage

		runners, err := c.getRunners(ctx, app.GitLabProjectID, now)
		if err != nil {
			status.Error = err.Error()
			results = append(results, status)
			continue
		}
		status.Runners = runners

		results = append(results, status)
	}

	return results, nil
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
