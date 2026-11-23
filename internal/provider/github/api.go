package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/YoanWai/runrack/internal/state"
)

const DefaultAPI = "https://api.github.com"

// ErrDisabled means the endpoint answered 401, 403 or 404 and stays off until Reset.
var ErrDisabled = errors.New("endpoint disabled")

func TokenSource(getenv func(string) string, ghToken func() string) (token, source string) {
	for _, name := range []string{"RUNRACK_GITHUB_TOKEN", "GH_TOKEN", "GITHUB_TOKEN"} {
		if value := strings.TrimSpace(getenv(name)); value != "" {
			return value, name
		}
	}
	if value := ghToken(); value != "" {
		return value, "gh auth token"
	}
	return "", ""
}

type cached struct {
	etag string
	body []byte
}

type Client struct {
	base  string
	token string
	http  *http.Client

	mu       sync.Mutex
	cache    map[string]cached
	disabled map[string]string
}

func NewClient(base, token string) *Client {
	return &Client{
		base:     strings.TrimRight(base, "/"),
		token:    token,
		http:     &http.Client{Timeout: 10 * time.Second},
		cache:    map[string]cached{},
		disabled: map[string]string{},
	}
}

func (c *Client) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disabled = map[string]string{}
}

func runnersPath(scope string) (path, label, hint string) {
	switch {
	case strings.HasPrefix(scope, "enterprises/"):
		return "/" + scope + "/actions/runners?per_page=100", "enterprise runners", "needs manage_runners:enterprise"
	case strings.Contains(scope, "/"):
		return "/repos/" + scope + "/actions/runners?per_page=100", "repo runners", "needs repo or Administration: read"
	}
	return "/orgs/" + scope + "/actions/runners?per_page=100", "org runners", "needs admin:org or Self-hosted runners: read"
}

func (c *Client) Runners(ctx context.Context, scope string) ([]state.APIRunner, error) {
	path, label, hint := runnersPath(scope)
	var body struct {
		Runners []struct {
			ID     int64  `json:"id"`
			Name   string `json:"name"`
			Status string `json:"status"`
			Busy   bool   `json:"busy"`
			Labels []struct {
				Name string `json:"name"`
			} `json:"labels"`
		} `json:"runners"`
	}
	if err := c.get(ctx, label+" "+scope, hint, path, &body); err != nil {
		return nil, err
	}
	runners := make([]state.APIRunner, 0, len(body.Runners))
	for _, r := range body.Runners {
		runner := state.APIRunner{ID: r.ID, Name: r.Name, Status: r.Status, Busy: r.Busy}
		for _, label := range r.Labels {
			runner.Labels = append(runner.Labels, label.Name)
		}
		runners = append(runners, runner)
	}
	return runners, nil
}

func (c *Client) RunJobs(ctx context.Context, repo string, runID int64, attempt int) ([]state.APIJob, error) {
	var body struct {
		Jobs []struct {
			Name       string `json:"name"`
			RunnerName string `json:"runner_name"`
			HTMLURL    string `json:"html_url"`
			Steps      []struct {
				Name        string     `json:"name"`
				Status      string     `json:"status"`
				Conclusion  *string    `json:"conclusion"`
				StartedAt   *time.Time `json:"started_at"`
				CompletedAt *time.Time `json:"completed_at"`
			} `json:"steps"`
		} `json:"jobs"`
	}
	path := fmt.Sprintf("/repos/%s/actions/runs/%d/attempts/%d/jobs?per_page=100", repo, runID, max(attempt, 1))
	if err := c.get(ctx, "run jobs "+repo, "needs repo or Actions: read", path, &body); err != nil {
		return nil, err
	}
	jobs := make([]state.APIJob, 0, len(body.Jobs))
	for _, j := range body.Jobs {
		job := state.APIJob{Name: j.Name, RunnerName: j.RunnerName, URL: j.HTMLURL}
		for _, s := range j.Steps {
			step := state.Step{Name: s.Name, Status: s.Status}
			if s.Status == "queued" || s.Status == "waiting" {
				step.Status = state.StepPending
			}
			if s.Conclusion != nil {
				step.Conclusion = *s.Conclusion
			}
			if s.StartedAt != nil {
				step.StartedAt = s.StartedAt.UTC()
				if s.CompletedAt != nil {
					step.Duration = state.Duration(s.CompletedAt.Sub(*s.StartedAt))
				}
			}
			job.Steps = append(job.Steps, step)
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func (c *Client) RunTitle(ctx context.Context, repo string, runID int64) (string, error) {
	var body struct {
		DisplayTitle string `json:"display_title"`
	}
	path := fmt.Sprintf("/repos/%s/actions/runs/%d", repo, runID)
	if err := c.get(ctx, "run "+repo, "needs repo or Actions: read", path, &body); err != nil {
		return "", err
	}
	return body.DisplayTitle, nil
}

func (c *Client) get(ctx context.Context, endpoint, hint, path string, out any) error {
	c.mu.Lock()
	reason, off := c.disabled[endpoint]
	previous := c.cache[path]
	c.mu.Unlock()
	if off {
		return fmt.Errorf("%w: %s", ErrDisabled, reason)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if previous.etag != "" {
		req.Header.Set("If-None-Match", previous.etag)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("api: %s: %w", endpoint, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return fmt.Errorf("api: %s: %w", endpoint, err)
	}
	switch {
	case resp.StatusCode == http.StatusNotModified:
		body = previous.body
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound:
		reason := fmt.Sprintf("api: %s %d (%s)", endpoint, resp.StatusCode, hint)
		c.mu.Lock()
		c.disabled[endpoint] = reason
		c.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrDisabled, reason)
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("api: %s %d", endpoint, resp.StatusCode)
	default:
		c.mu.Lock()
		if len(c.cache) > 200 {
			c.cache = map[string]cached{} // finished runs are never asked again, so start over instead of tracking them
		}
		c.cache[path] = cached{etag: resp.Header.Get("ETag"), body: body}
		c.mu.Unlock()
	}
	return json.Unmarshal(body, out)
}
