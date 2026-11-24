package demo

import (
	"fmt"
	"strings"
	"time"

	"github.com/YoanWai/runrack/internal/scan"
	"github.com/YoanWai/runrack/internal/state"
)

const gib = 1 << 30

// World pins jobs to start, so elapsed times grow as now moves.
func World(start, now time.Time) state.Snapshot {
	start, now = start.UTC(), now.UTC()
	at := func(offset time.Duration) time.Time { return start.Add(offset) }

	acmeJob := &state.Job{
		ID: "6f1c2a3e-0d4b-5c6d-8e9f-0a1b2c3d4e5f", Name: "check", Repo: "acme/widgets", Workflow: "CI",
		Branch: "ci/homelab-runner", BaseBranch: "main", Event: "pull_request", Actor: "octocat",
		SHA: "c0a9cbb51e2d4f8a9b7c6d5e4f3a2b1c0d9e8f7a", Title: "fix: homelab runner labels",
		URL: "https://github.com/acme/widgets/actions/runs/45229190085", RunID: 45229190085, RunNumber: 16,
		Attempt: 1, PR: 14, StartedAt: at(-151 * time.Second), Expected: state.Duration(190 * time.Second), Source: state.SourceAPI,
		Steps: []state.Step{
			{Name: "Set up job", Status: state.StepCompleted, Conclusion: "success", StartedAt: at(-151 * time.Second), Duration: state.Duration(3 * time.Second)},
			{Name: "Run actions/checkout@v5", Status: state.StepCompleted, Conclusion: "success", StartedAt: at(-148 * time.Second), Duration: state.Duration(2 * time.Second)},
			{Name: "Run actions/setup-node@v5", Status: state.StepCompleted, Conclusion: "success", StartedAt: at(-146 * time.Second), Duration: state.Duration(9 * time.Second)},
			{Name: "Run npm ci", Status: state.StepInProgress, StartedAt: at(-137 * time.Second)},
			{Name: "Run npm test", Status: state.StepPending},
		},
	}
	globexJob := &state.Job{
		ID: "9a8b7c6d-5e4f-4a3b-2c1d-0e9f8a7b6c5d", Name: "check", Repo: "globex/site", Workflow: "CI",
		Branch: "main", Event: "push", Actor: "octocat", SHA: "4e5f6a7b8c9d0e1f2a3b4c5d6e7f8a9b0c1d2e3f",
		Title: "feat: dark mode for the status page",
		URL:   "https://github.com/globex/site/actions/runs/45229199120", RunID: 45229199120, RunNumber: 88, Attempt: 1,
		StartedAt: at(-12 * time.Second), StepCount: 6, Source: state.SourceDiag,
	}
	failedSteps := []state.Step{
		{Name: "Run actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1", Status: state.StepCompleted, Conclusion: state.ConclusionSuccess, Duration: state.Duration(2 * time.Second)},
		{Name: "Run actions/setup-node@v5", Status: state.StepCompleted, Conclusion: state.ConclusionSuccess, Duration: state.Duration(9 * time.Second)},
		{Name: "Run npm ci", Status: state.StepCompleted, Conclusion: state.ConclusionSuccess, Duration: state.Duration(21 * time.Second)},
		{Name: "Run npm test", Status: state.StepCompleted, Conclusion: state.ConclusionFailure, Duration: state.Duration(22 * time.Second)},
		{Name: "Post Run actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1", Status: state.StepCompleted, Conclusion: state.ConclusionSuccess},
	}
	recent := []state.Result{
		result("zenbook-acme-3", "acme/widgets", "check", state.ConclusionFailure, at(-7*time.Minute), 55*time.Second,
			state.Job{Event: "pull_request", Branch: "ci/homelab-runner", BaseBranch: "main", PR: 14, Title: "fix: homelab runner labels", RunNumber: 15, Steps: failedSteps, StepCount: 4}),
		result("zenbook-globex-1", "globex/site", "check", state.ConclusionSuccess, at(-8*time.Minute), 96*time.Second,
			state.Job{Event: "push", Branch: "main", Title: "docs: tidy the install guide", RunNumber: 87}),
		result("zenbook-acme-1", "acme/widgets", "check", state.ConclusionSuccess, at(-30*time.Minute), 118*time.Second,
			state.Job{Event: "push", Branch: "main", Title: "chore: bump node to 24", RunNumber: 13}),
		result("zenbook-acme-2", "acme/widgets", "check", state.ConclusionSuccess, at(-36*time.Minute), 121*time.Second,
			state.Job{Event: "schedule", Branch: "main", RunNumber: 12}),
		result("zenbook-globex-2", "globex/site", "build", state.ConclusionCancelled, at(-51*time.Minute), 130*time.Second,
			state.Job{Event: "pull_request", Branch: "feat/tv-layout", BaseBranch: "main", PR: 31, Title: "feat: TV layout", RunNumber: 86}),
		result("zenbook-globex-2", "globex/site", "build", state.ConclusionFailure, at(-62*time.Minute), 141*time.Second,
			state.Job{Event: "workflow_dispatch", Branch: "main", RunNumber: 85, Attempt: 2}),
	}
	lastOf := func(runner string) *state.Result {
		for i := range recent {
			if recent[i].Runner == runner {
				return &recent[i]
			}
		}
		return nil
	}
	labels := []string{"self-hosted", "Linux", "X64", "homelab"}
	runner := func(scope string, n string, status string, job *state.Job, cpu float64, mem float64, procs int) state.Runner {
		name := "zenbook-" + scope + "-" + n
		container := "runner-" + scope + "-" + n
		return state.Runner{
			Key: "github|incus:" + container, ID: "github/incus:" + container + "/" + name, Provider: "github",
			Name: name, Scope: scope, Service: "active", Status: status, Version: "2.338.2", Labels: labels,
			Placement: scan.Placement{Kind: scan.KindIncus, Container: container, Unit: "actions.runner." + scope + "." + name + ".service"},
			Job:       job, Last: lastOf(name),
			API:       &state.APIRunner{Name: name, Status: "online", Busy: status == state.StatusBusy, Labels: labels},
			Container: &state.ContainerStats{CPUPct: cpu, MemBytes: int64(mem * float64(gib)), Procs: procs},
		}
	}
	healthy := func(name string) state.SourceHealth { return state.SourceHealth{Name: name, OK: true, LastOK: now} }
	return state.Snapshot{
		Schema: state.Schema,
		Host: state.HostStats{
			Hostname: "homelab", Load1: 3.21, Load5: 2.80, Load15: 2.10, CPUs: 16, CPUPct: 23,
			MemUsed: 61 * gib / 10, MemTotal: 15 * gib, Uptime: state.Duration(98 * time.Hour),
		},
		Runners: []state.Runner{
			runner("acme", "1", state.StatusBusy, acmeJob, 61, 1.9, 212),
			runner("acme", "2", state.StatusIdle, nil, 1, 0.4, 64),
			runner("acme", "3", state.StatusIdle, nil, 2, 0.5, 66),
			runner("globex", "3", state.StatusBusy, globexJob, 34, 1.2, 140),
			runner("globex", "1", state.StatusIdle, nil, 1, 0.4, 61),
			runner("globex", "2", state.StatusIdle, nil, 0, 0.4, 60),
		},
		Recent:  recent,
		Sources: []state.SourceHealth{healthy("api runners acme"), healthy("api runners globex"), healthy("incus list"), healthy("proc")},
		API:     true,
		TakenAt: now,
	}
}

// result fills in what every demo run shares around the trigger fields of why.
func result(runner, repo, job, conclusion string, finished time.Time, took time.Duration, why state.Job) state.Result {
	scope, _, _ := strings.Cut(repo, "/")
	why.ID, why.Name, why.Repo, why.Workflow = runner+"-"+finished.Format("150405"), job, repo, "CI"
	why.Actor, why.StartedAt, why.Source = "octocat", finished.Add(-took), state.SourceDiag
	why.Attempt = max(why.Attempt, 1)
	why.URL = fmt.Sprintf("https://github.com/%s/actions/runs/%d", repo, 45229100000+why.RunNumber)
	return state.Result{
		RunnerID:   "github/incus:runner-" + scope + "-" + runner[len(runner)-1:] + "/" + runner,
		Runner:     runner,
		Scope:      scope,
		Job:        why,
		Conclusion: conclusion,
		FinishedAt: finished,
		Duration:   state.Duration(took),
	}
}
