package github

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/YoanWai/runrack/internal/state"
)

func TestTokenSourceOrder(t *testing.T) {
	env := map[string]string{"GH_TOKEN": "b", "GITHUB_TOKEN": "c"}
	token, source := TokenSource(func(k string) string { return env[k] }, func() string { return "d" })
	if token != "b" || source != "GH_TOKEN" {
		t.Fatalf("got %q from %s", token, source)
	}
	token, source = TokenSource(func(string) string { return "" }, func() string { return "d" })
	if token != "d" || source != "gh auth token" {
		t.Fatalf("got %q from %s", token, source)
	}
	if token, _ := TokenSource(func(string) string { return "" }, func() string { return "" }); token != "" {
		t.Fatal("no token expected")
	}
}

func TestRunnersUsesETag(t *testing.T) {
	var hits, notModified atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/orgs/acme/actions/runners" || r.Header.Get("Authorization") != "Bearer t0ken" {
			t.Errorf("unexpected request %s %q", r.URL.Path, r.Header.Get("Authorization"))
		}
		if r.Header.Get("If-None-Match") == `W/"abc"` {
			notModified.Add(1)
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `W/"abc"`)
		w.Write(fixture(t, "api/org-runners.json"))
	}))
	defer server.Close()
	client := NewClient(server.URL, "t0ken")
	for range 2 {
		runners, err := client.Runners(t.Context(), "acme")
		if err != nil {
			t.Fatal(err)
		}
		if len(runners) != 3 || runners[0].Name != "zenbook-acme-1" || !runners[0].Busy || runners[0].Status != "online" {
			t.Fatalf("runners %+v", runners)
		}
		if strings.Join(runners[0].Labels, ",") != "self-hosted,Linux,X64,homelab" {
			t.Fatalf("labels %v", runners[0].Labels)
		}
	}
	if hits.Load() != 2 || notModified.Load() != 1 {
		t.Fatalf("hits %d, 304s %d", hits.Load(), notModified.Load())
	}
}

func TestForbiddenDisablesEndpointUntilReset(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusForbidden)
		w.Write(fixture(t, "api/403.json"))
	}))
	defer server.Close()
	client := NewClient(server.URL, "t")
	_, err := client.Runners(t.Context(), "acme")
	if !errors.Is(err, ErrDisabled) {
		t.Fatalf("err = %v", err)
	}
	const footer = "api: org runners acme 403 (needs admin:org or Self-hosted runners: read)"
	if !strings.Contains(err.Error(), footer) || client.Disabled()[0] != footer {
		t.Fatalf("err %q, disabled %v", err, client.Disabled())
	}
	client.Runners(t.Context(), "acme")
	if hits.Load() != 1 {
		t.Fatal("a disabled endpoint must not be called again")
	}
	client.Reset()
	client.Runners(t.Context(), "acme")
	if hits.Load() != 2 {
		t.Fatal("Reset must re-enable the endpoint")
	}
}

func TestRunJobsAndTitle(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/acme/widgets/actions/runs/45229190085/attempts/1/jobs":
			w.Write(fixture(t, "api/run-jobs.json"))
		case "/repos/acme/widgets/actions/runs/45229190085":
			w.Write(fixture(t, "api/run.json"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := NewClient(server.URL, "t")
	jobs, err := client.RunJobs(t.Context(), "acme/widgets", 45229190085, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 6 {
		t.Fatalf("got %d jobs", len(jobs))
	}
	var e2e state.APIJob
	for _, job := range jobs {
		if job.RunnerName == "zenbook-acme-1" && job.Name == "e2e" {
			e2e = job
		}
	}
	counts := map[string]int{}
	for _, step := range e2e.Steps {
		counts[step.Status]++
	}
	if counts[state.StepCompleted] != 4 || counts[state.StepInProgress] != 1 || counts[state.StepPending] != 8 {
		t.Fatalf("step statuses %v", counts)
	}
	if !strings.HasPrefix(e2e.URL, "https://github.com/acme/widgets/actions/runs/45229190085/job/") {
		t.Fatalf("url %q", e2e.URL)
	}
	title, err := client.RunTitle(t.Context(), "acme/widgets", 45229190085)
	if err != nil || title != "fix: homelab runner labels" {
		t.Fatalf("title %q, err %v", title, err)
	}
}
