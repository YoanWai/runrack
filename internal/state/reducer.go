package state

import (
	"cmp"
	"slices"
	"time"

	"github.com/YoanWai/runrack/internal/scan"
)

const recentCap = 20

// Presence is what one discovery pass knows about a runner without reading any file.
type Presence struct {
	Key         string
	Provider    string
	Target      string
	Placement   scan.Placement
	Service     string
	Online      bool
	WorkerPID   int
	WorkerStart time.Time
}

type Info struct {
	Name    string
	Scope   string
	Version string
	Labels  []string
}

type APIJob struct {
	Name       string
	RunnerName string
	URL        string
	Steps      []Step
}

type entry struct {
	runner    Runner
	workerPID int
}

type recentItem struct {
	key    string
	result Result
}

// Reducer owns the world. Only one goroutine may call it.
type Reducer struct {
	runners    map[string]*entry
	recent     []recentItem
	seen       map[string]bool
	expected   map[string]Result
	host       HostStats
	sources    map[string]*SourceHealth
	containers map[string]ContainerStats
	apiRunners map[string]map[string]APIRunner
	apiJobs    map[int64][]APIJob
	runTitles  map[int64]string
	api        bool
}

func NewReducer() *Reducer {
	return &Reducer{
		runners:    map[string]*entry{},
		seen:       map[string]bool{},
		expected:   map[string]Result{},
		sources:    map[string]*SourceHealth{},
		containers: map[string]ContainerStats{},
		apiRunners: map[string]map[string]APIRunner{},
		apiJobs:    map[int64][]APIJob{},
		runTitles:  map[int64]string{},
	}
}

// Observe applies one discovery pass. It returns the jobs whose Worker just exited, by runner key.
func (r *Reducer) Observe(presences []Presence, now time.Time) map[string]Job {
	ended := map[string]Job{}
	present := map[string]bool{}
	for _, p := range presences {
		present[p.Key] = true
		e, ok := r.runners[p.Key]
		if !ok {
			e = &entry{runner: Runner{Key: p.Key, ID: p.Provider + "/" + targetOf(p.Placement) + "/" + p.Placement.Unit, Provider: p.Provider, Name: p.Placement.Unit}}
			r.runners[p.Key] = e
		}
		e.runner.Placement = p.Placement
		e.runner.Service = p.Service
		if p.WorkerPID != e.workerPID {
			if e.runner.Job != nil {
				ended[p.Key] = *e.runner.Job
			}
			e.runner.Job = nil
			if p.WorkerPID != 0 {
				e.runner.Job = &Job{StartedAt: p.WorkerStart.UTC(), Source: SourceNone}
			}
			e.workerPID = p.WorkerPID
		}
		switch {
		case !p.Online:
			e.runner.Status = StatusOffline
		case p.WorkerPID != 0:
			e.runner.Status = StatusBusy
		default:
			e.runner.Status = StatusIdle
		}
	}
	for key, e := range r.runners {
		if present[key] {
			continue
		}
		if e.runner.Job != nil {
			r.EndJob(key, *e.runner.Job, ConclusionUnknown, now)
		}
		delete(r.runners, key)
	}
	return ended
}

func (r *Reducer) SetInfo(key string, info Info) {
	e, ok := r.runners[key]
	if !ok {
		return
	}
	e.runner.Name = info.Name
	e.runner.Scope = info.Scope
	e.runner.Version = info.Version
	e.runner.Labels = info.Labels
	e.runner.ID = e.runner.Provider + "/" + targetOf(e.runner.Placement) + "/" + info.Name
}

func targetOf(p scan.Placement) string {
	if p.Kind == scan.KindIncus {
		return "incus:" + p.Container
	}
	return string(p.Kind)
}

func (r *Reducer) SetNote(key, note string) {
	if e, ok := r.runners[key]; ok {
		e.runner.Note = note
	}
}

// SetCurrent fills the busy job with what its Worker log says so far.
func (r *Reducer) SetCurrent(key string, job Job) {
	e, ok := r.runners[key]
	if !ok || e.runner.Job == nil {
		return
	}
	job.StartedAt = e.runner.Job.StartedAt
	job.Source = SourceDiag
	e.runner.Job = &job
}

// AddResults records finished jobs. Each job ID lands in Recent once.
func (r *Reducer) AddResults(key string, results []Result) {
	for _, result := range results {
		if result.Conclusion == "" || r.seen[result.ID] {
			continue
		}
		r.seen[result.ID] = true
		if e, ok := r.runners[key]; ok {
			result.RunnerID, result.Runner = e.runner.ID, e.runner.Name
		}
		r.recent = append(r.recent, recentItem{key: key, result: result})
		jobKey := expectedKey(result.Job)
		if previous, ok := r.expected[jobKey]; !ok || result.FinishedAt.After(previous.FinishedAt) {
			r.expected[jobKey] = result
		}
		if e, ok := r.runners[key]; ok && (e.runner.Last == nil || result.FinishedAt.After(e.runner.Last.FinishedAt)) {
			last := result
			e.runner.Last = &last
		}
	}
	slices.SortStableFunc(r.recent, func(a, b recentItem) int { return b.result.FinishedAt.Compare(a.result.FinishedAt) })
	r.recent = r.recent[:min(recentCap, len(r.recent))]
}

// EndJob closes a job whose result can no longer be read, for example when its container stopped.
func (r *Reducer) EndJob(key string, job Job, conclusion string, now time.Time) {
	if job.ID == "" {
		job.ID = key + "@" + job.StartedAt.Format(time.RFC3339)
	}
	r.AddResults(key, []Result{{Job: job, Conclusion: conclusion, FinishedAt: now.UTC(), Duration: Duration(now.Sub(job.StartedAt))}})
}

func expectedKey(j Job) string { return j.Repo + "\x00" + j.Workflow + "\x00" + j.Name }

func (r *Reducer) SetHost(h HostStats)                           { r.host = h }
func (r *Reducer) SetContainers(stats map[string]ContainerStats) { r.containers = stats }
func (r *Reducer) SetAPIEnabled(on bool)                         { r.api = on }
func (r *Reducer) SetAPIRunners(scope string, runners []APIRunner) {
	r.apiRunners[scope] = byName(runners)
}
func (r *Reducer) SetAPIJobs(runID int64, jobs []APIJob) { r.apiJobs[runID] = jobs }
func (r *Reducer) SetRunTitle(runID int64, title string) { r.runTitles[runID] = title }

func byName(runners []APIRunner) map[string]APIRunner {
	m := make(map[string]APIRunner, len(runners))
	for _, runner := range runners {
		m[runner.Name] = runner
	}
	return m
}

func (r *Reducer) SetSource(name string, err error, now time.Time) {
	s, ok := r.sources[name]
	if !ok {
		s = &SourceHealth{Name: name}
		r.sources[name] = s
	}
	s.OK = err == nil
	if err != nil {
		s.LastError = err.Error()
		return
	}
	s.LastError = ""
	s.LastOK = now.UTC()
}

// Busy lists the runners whose Worker is alive, for the job pollers.
func (r *Reducer) Busy() map[string]Job {
	busy := map[string]Job{}
	for key, e := range r.runners {
		if e.runner.Job != nil {
			busy[key] = *e.runner.Job
		}
	}
	return busy
}

func (r *Reducer) Snapshot(now time.Time) Snapshot {
	snap := Snapshot{Schema: Schema, Host: r.host, API: r.api, TakenAt: now.UTC(), Runners: []Runner{}, Recent: []Result{}, Sources: []SourceHealth{}}
	for _, e := range r.runners {
		snap.Runners = append(snap.Runners, r.view(e))
	}
	slices.SortFunc(snap.Runners, func(a, b Runner) int {
		return cmp.Or(cmp.Compare(a.Scope, b.Scope), cmp.Compare(rank(a), rank(b)), cmp.Compare(a.Name, b.Name))
	})
	for _, item := range r.recent {
		result := item.result
		if e, ok := r.runners[item.key]; ok {
			result.RunnerID, result.Runner = e.runner.ID, e.runner.Name
		}
		snap.Recent = append(snap.Recent, result)
	}
	for _, s := range r.sources {
		snap.Sources = append(snap.Sources, *s)
	}
	slices.SortFunc(snap.Sources, func(a, b SourceHealth) int { return cmp.Compare(a.Name, b.Name) })
	return snap
}

func rank(r Runner) int {
	if r.Status == StatusBusy {
		return 0
	}
	return 1
}

func (r *Reducer) view(e *entry) Runner {
	runner := e.runner
	if runner.Last != nil {
		last := *runner.Last
		last.RunnerID, last.Runner = runner.ID, runner.Name
		runner.Last = &last
	}
	if stats, ok := r.containers[runner.Placement.Container]; ok && runner.Placement.Kind == scan.KindIncus {
		runner.Container = &stats
	}
	if api, ok := r.apiRunners[runner.Scope][runner.Name]; ok {
		runner.API = &api
		runner.Labels = api.Labels
	}
	if runner.Job != nil {
		job := *runner.Job
		if previous, ok := r.expected[expectedKey(job)]; ok && job.HasMetadata() {
			job.Expected = previous.Duration
		}
		if title, ok := r.runTitles[job.RunID]; ok {
			job.Title = title
		}
		for _, apiJob := range r.apiJobs[job.RunID] {
			if apiJob.RunnerName == runner.Name && apiJob.Name == job.Name {
				job.URL = apiJob.URL
				job.Steps = apiJob.Steps
				job.Source = SourceAPI
			}
		}
		runner.Job = &job
	}
	return runner
}
