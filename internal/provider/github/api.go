package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/YoanWai/runrack/internal/state"
)

const DefaultAPI = "https://api.github.com"

const (
	disabledFor  = 15 * time.Minute
	cacheEntries = 32
	cacheBodyMax = 1 << 20
)

// ErrDisabled means the endpoint answered 401, 403 or 404 and stays off for disabledFor or until Reset.
var ErrDisabled = errors.New("endpoint disabled")

var slugPart = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// validSlug checks owner, owner/repo or enterprises/slug before it goes into an API path.
func validSlug(s string, minParts int) bool {
	parts := strings.Split(s, "/")
	if len(parts) < minParts || len(parts) > 2 {
		return false
	}
	for _, part := range parts {
		if !slugPart.MatchString(part) || part == "." || part == ".." {
			return false
		}
	}
	return true
}

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

type disabled struct {
	reason string
	until  time.Time
}

type Client struct {
	base  string
	token string
	http  *http.Client

	mu       sync.Mutex
	cache    map[string]cached
	disabled map[string]disabled
}

func NewClient(base, token string) *Client {
	return &Client{
		base:     strings.TrimRight(base, "/"),
		token:    token,
		http:     &http.Client{Timeout: 10 * time.Second},
		cache:    map[string]cached{},
		disabled: map[string]disabled{},
	}
}

func (c *Client) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.disabled = map[string]disabled{}
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
	if !validSlug(scope, 1) {
		return nil, fmt.Errorf("api: scope %q is not an owner or owner/repo", scope)
	}
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
		runner := state.APIRunner{ID: r.ID, Name: clean(r.Name), Status: clean(r.Status), Busy: r.Busy}
		for _, label := range r.Labels {
			runner.Labels = append(runner.Labels, clean(label.Name))
		}
		runners = append(runners, runner)
	}
	return runners, nil
}

func (c *Client) RunJobs(ctx context.Context, repo string, runID int64, attempt int) ([]state.APIJob, error) {
	if err := checkRepo(repo); err != nil {
		return nil, err
	}
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
		job := state.APIJob{Name: clean(j.Name), RunnerName: clean(j.RunnerName), URL: clean(j.HTMLURL)}
		for _, s := range j.Steps {
			step := state.Step{Name: clean(s.Name), Status: clean(s.Status)}
			if s.Status == "queued" || s.Status == "waiting" {
				step.Status = state.StepPending
			}
			if s.Conclusion != nil {
				step.Conclusion = clean(*s.Conclusion)
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
	if err := checkRepo(repo); err != nil {
		return "", err
	}
	var body struct {
		DisplayTitle string `json:"display_title"`
	}
	path := fmt.Sprintf("/repos/%s/actions/runs/%d", repo, runID)
	if err := c.get(ctx, "run "+repo, "needs repo or Actions: read", path, &body); err != nil {
		return "", err
	}
	return clean(body.DisplayTitle), nil
}

func checkRepo(repo string) error {
	if !validSlug(repo, 2) {
		return fmt.Errorf("api: repo %q is not owner/name", repo)
	}
	return nil
}

func (c *Client) get(ctx context.Context, endpoint, hint, path string, out any) error {
	c.mu.Lock()
	off, isOff := c.disabled[endpoint]
	previous := c.cache[path]
	c.mu.Unlock()
	if isOff && time.Now().Before(off.until) {
		return fmt.Errorf("%w: %s", ErrDisabled, off.reason)
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
	case rateLimited(resp):
		return fmt.Errorf("api: %s rate limited (%d)", endpoint, resp.StatusCode)
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusNotFound:
		reason := fmt.Sprintf("api: %s %d (%s)", endpoint, resp.StatusCode, hint)
		c.mu.Lock()
		c.disabled[endpoint] = disabled{reason: reason, until: time.Now().Add(disabledFor)}
		c.mu.Unlock()
		return fmt.Errorf("%w: %s", ErrDisabled, reason)
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("api: %s %d", endpoint, resp.StatusCode)
	default:
		c.mu.Lock()
		if len(c.cache) >= cacheEntries {
			c.cache = map[string]cached{} // finished runs are never asked again, so start over instead of tracking them
		}
		if len(body) <= cacheBodyMax {
			c.cache[path] = cached{etag: resp.Header.Get("ETag"), body: body}
		}
		c.mu.Unlock()
	}
	return json.Unmarshal(body, out)
}

// rateLimited follows GitHub's docs: a 429, or a 403 that carries Retry-After or an exhausted quota.
func rateLimited(resp *http.Response) bool {
	if resp.StatusCode == http.StatusTooManyRequests {
		return true
	}
	return resp.StatusCode == http.StatusForbidden &&
		(resp.Header.Get("Retry-After") != "" || resp.Header.Get("X-RateLimit-Remaining") == "0")
}
