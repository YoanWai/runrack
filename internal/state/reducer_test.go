package state

import (
	"testing"
	"time"

	"github.com/YoanWai/runrack/internal/scan"
)

var t0 = time.Date(2026, 11, 23, 17, 46, 38, 0, time.UTC)

const key = "github|incus:runner-acme-1|actions.runner.acme.zenbook-acme-1.service"

func presence(worker int, online bool) Presence {
	return Presence{
		Key: key, Name: "actions.runner.acme.zenbook-acme-1.service", Provider: "github", Target: "incus:runner-acme-1", Service: "active", Online: online,
		Placement:   scan.Placement{Kind: scan.KindIncus, Container: "runner-acme-1", Unit: "actions.runner.acme.zenbook-acme-1.service"},
		WorkerPID:   worker,
		WorkerStart: t0,
	}
}

func finished(id, conclusion string, at time.Time, took time.Duration) Result {
	return Result{
		Job:        Job{ID: id, Name: "check", Repo: "acme/widgets", Workflow: "CI", StartedAt: at.Add(-took), Source: SourceDiag},
		Conclusion: conclusion, FinishedAt: at, Duration: Duration(took),
	}
}

func onlyRunner(t *testing.T, r *Reducer, now time.Time) Runner {
	t.Helper()
	snap := r.Snapshot(now)
	if len(snap.Runners) != 1 {
		t.Fatalf("want 1 runner, got %d", len(snap.Runners))
	}
	return snap.Runners[0]
}

func TestBusyIdleAndResultOnce(t *testing.T) {
	r := NewReducer()
	r.Observe([]Presence{presence(0, true)}, t0)
	r.SetInfo(key, Info{Name: "zenbook-acme-1", Scope: "acme", Version: "2.338.2"})
	if got := onlyRunner(t, r, t0); got.Status != StatusIdle || got.ID != "github/incus:runner-acme-1/zenbook-acme-1" {
		t.Fatalf("got %+v", got)
	}

	r.AddResults(key, []Result{finished("old", ConclusionSuccess, t0.Add(-time.Hour), 3*time.Minute)})
	ended := r.Observe([]Presence{presence(92911, true)}, t0.Add(time.Second))
	if len(ended) != 0 {
		t.Fatalf("nothing ended yet: %v", ended)
	}
	busy := onlyRunner(t, r, t0.Add(2*time.Second))
	if busy.Status != StatusBusy || busy.Job == nil || busy.Job.Source != SourceNone || !busy.Job.StartedAt.Equal(t0) {
		t.Fatalf("busy = %+v", busy)
	}

	r.SetCurrent(key, 92911, Job{ID: "new", Name: "check", Repo: "acme/widgets", Workflow: "CI"})
	busy = onlyRunner(t, r, t0.Add(3*time.Second))
	if busy.Job.Expected != Duration(3*time.Minute) || !busy.Job.StartedAt.Equal(t0) || busy.Job.Source != SourceDiag {
		t.Fatalf("expected duration from history, kept Worker start: %+v", busy.Job)
	}

	ended = r.Observe([]Presence{presence(0, true)}, t0.Add(4*time.Minute))
	if ended[key].ID != "new" {
		t.Fatalf("ended = %v", ended)
	}
	done := finished("new", ConclusionFailure, t0.Add(4*time.Minute), 4*time.Minute)
	r.AddResults(key, []Result{done})
	r.AddResults(key, []Result{done})
	snap := r.Snapshot(t0.Add(5 * time.Minute))
	if len(snap.Recent) != 2 || snap.Recent[0].ID != "new" || snap.Recent[0].Runner != "zenbook-acme-1" {
		t.Fatalf("recent = %+v", snap.Recent)
	}
	if got := snap.Runners[0]; got.Status != StatusIdle || got.Last.Conclusion != ConclusionFailure {
		t.Fatalf("runner after job = %+v", got)
	}
}

func TestListenerRestartKeepsRunner(t *testing.T) {
	r := NewReducer()
	r.Observe([]Presence{presence(0, true)}, t0)
	r.SetInfo(key, Info{Name: "zenbook-acme-1", Scope: "acme"})
	r.Observe([]Presence{presence(0, false)}, t0.Add(time.Second))
	if got := onlyRunner(t, r, t0); got.Status != StatusOffline || got.Name != "zenbook-acme-1" {
		t.Fatalf("got %+v", got)
	}
	r.Observe([]Presence{presence(0, true)}, t0.Add(2*time.Second))
	if got := onlyRunner(t, r, t0); got.Status != StatusIdle || got.ID != "github/incus:runner-acme-1/zenbook-acme-1" {
		t.Fatalf("got %+v", got)
	}
}

func TestContainerStoppingMidJobEndsUnknown(t *testing.T) {
	r := NewReducer()
	r.Observe([]Presence{presence(92911, true)}, t0)
	r.SetInfo(key, Info{Name: "zenbook-acme-1", Scope: "acme"})
	r.SetCurrent(key, 92911, Job{ID: "j1", Name: "check", Repo: "acme/widgets", Workflow: "CI"})
	r.Observe(nil, t0.Add(time.Minute))
	snap := r.Snapshot(t0.Add(time.Minute))
	if len(snap.Runners) != 0 || len(snap.Recent) != 1 {
		t.Fatalf("snap = %+v", snap)
	}
	if got := snap.Recent[0]; got.Conclusion != ConclusionUnknown || got.Duration != Duration(time.Minute) || got.Runner != "zenbook-acme-1" {
		t.Fatalf("result = %+v", got)
	}
}

func TestShortJobSeenOnlyThroughIndex(t *testing.T) {
	r := NewReducer()
	r.Observe([]Presence{presence(0, true)}, t0)
	r.AddResults(key, []Result{finished("blink", ConclusionSuccess, t0.Add(-2*time.Second), 400*time.Millisecond)})
	snap := r.Snapshot(t0)
	if len(snap.Recent) != 1 || snap.Recent[0].ID != "blink" || snap.Runners[0].Status != StatusIdle {
		t.Fatalf("snap = %+v", snap)
	}
}

func TestRecentCapAndOrder(t *testing.T) {
	r := NewReducer()
	r.Observe([]Presence{presence(0, true)}, t0)
	for i := range recentCap + 10 {
		r.AddResults(key, []Result{finished(string(rune('a'+i)), ConclusionSuccess, t0.Add(time.Duration(i)*time.Minute), time.Minute)})
	}
	snap := r.Snapshot(t0)
	if len(snap.Recent) != recentCap || snap.Recent[0].ID != string(rune('a'+recentCap+9)) {
		t.Fatalf("recent %d, first %s", len(snap.Recent), snap.Recent[0].ID)
	}
}

func TestAPIJobFillsStepsForMatchingRunnerAndJob(t *testing.T) {
	r := NewReducer()
	r.Observe([]Presence{presence(92911, true)}, t0)
	r.SetInfo(key, Info{Name: "zenbook-acme-1", Scope: "acme"})
	r.SetCurrent(key, 92911, Job{ID: "j1", Name: "e2e", Repo: "acme/widgets", RunID: 7})
	r.SetAPIJobs(RunAttempt{RunID: 7}, []APIJob{
		{Name: "postgres", RunnerName: "zenbook-acme-1", URL: "wrong", Steps: []Step{{Name: "x"}}},
		{Name: "e2e", RunnerName: "zenbook-acme-1", URL: "right", Steps: []Step{{Name: "a"}, {Name: "b", Status: StepPending}}},
	})
	r.SetRunTitle(7, "fix: homelab runner labels")
	r.SetAPIRunners("acme", []APIRunner{{Name: "zenbook-acme-1", Status: "online", Labels: []string{"self-hosted", "gpu"}}})
	got := onlyRunner(t, r, t0)
	if got.Job.URL != "right" || len(got.Job.Steps) != 2 || got.Job.Source != SourceAPI || got.Job.Title == "" {
		t.Fatalf("job = %+v", got.Job)
	}
	if got.API == nil || got.Labels[1] != "gpu" {
		t.Fatalf("api runner = %+v labels %v", got.API, got.Labels)
	}
}

func TestSetCurrentIgnoresAReplacedWorker(t *testing.T) {
	r := NewReducer()
	r.Observe([]Presence{presence(92911, true)}, t0)
	r.Observe([]Presence{presence(93001, true)}, t0.Add(time.Second))
	r.SetCurrent(key, 92911, Job{ID: "old", Repo: "acme/widgets"})
	if job := onlyRunner(t, r, t0).Job; job.ID != "" {
		t.Fatalf("job of the previous Worker landed on the new one: %+v", job)
	}
}

func TestUnknownIsReplacedByTheRealConclusion(t *testing.T) {
	r := NewReducer()
	r.Observe([]Presence{presence(0, true)}, t0)
	r.EndJob(key, Job{ID: "j1", Name: "check", Repo: "acme/widgets", Workflow: "CI", StartedAt: t0.Add(-time.Minute)}, ConclusionUnknown, t0)
	r.AddResults(key, []Result{finished("j1", ConclusionFailure, t0.Add(-time.Second), time.Minute)})
	snap := r.Snapshot(t0)
	if len(snap.Recent) != 1 || snap.Recent[0].Conclusion != ConclusionFailure || snap.Runners[0].Last.Conclusion != ConclusionFailure {
		t.Fatalf("recent %+v last %+v", snap.Recent, snap.Runners[0].Last)
	}
	r.AddResults(key, []Result{finished("j1", ConclusionSuccess, t0, time.Minute)})
	if got := r.Snapshot(t0).Recent; len(got) != 1 || got[0].Conclusion != ConclusionFailure {
		t.Fatalf("a real conclusion must not be replaced: %+v", got)
	}
}

func TestPruneForgetsOldIDsAndIdleRuns(t *testing.T) {
	r := NewReducer()
	r.Observe([]Presence{presence(92911, true)}, t0)
	r.SetCurrent(key, 92911, Job{ID: "j1", Repo: "acme/widgets", RunID: 7})
	r.AddResults(key, []Result{finished("ancient", ConclusionSuccess, t0.Add(-48*time.Hour), time.Minute)})
	r.SetAPIJobs(RunAttempt{RunID: 7}, nil)
	r.SetAPIJobs(RunAttempt{RunID: 6}, nil)
	r.SetRunTitle(7, "busy")
	r.SetRunTitle(6, "done")
	r.Prune(t0.Add(-25 * time.Hour))
	if _, ok := r.seen["ancient"]; ok || len(r.apiJobs) != 1 || len(r.runTitles) != 1 || r.runTitles[7] != "busy" {
		t.Fatalf("seen %v apiJobs %v titles %v", r.seen, r.apiJobs, r.runTitles)
	}
}
