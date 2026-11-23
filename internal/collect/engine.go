package collect

import (
	"context"
	"maps"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/YoanWai/runrack/internal/hoststat"
	"github.com/YoanWai/runrack/internal/provider"
	"github.com/YoanWai/runrack/internal/provider/github"
	"github.com/YoanWai/runrack/internal/scan"
	"github.com/YoanWai/runrack/internal/state"
	"github.com/YoanWai/runrack/internal/target"
)

const (
	everyHost       = 2 * time.Second
	everyContainers = 5 * time.Second
	everyUnits      = 60 * time.Second
	everyIndex      = 60 * time.Second
	everyJob        = 3 * time.Second
	everyJobFocused = 1 * time.Second
	everyAPIRunners = 30 * time.Second
	everyAPIJobs    = 10 * time.Second
	retryInspect    = 30 * time.Second
	maxBackoff      = 60 * time.Second
	historyWindow   = 24 * time.Hour
	historyPerRun   = 20
	deadLogAge      = time.Minute
)

type Options struct {
	ProcRoot      string
	ContainerGlob string
	API           *github.Client
}

type schedule struct {
	next     time.Time
	fails    int
	inflight bool
}

type logIndex struct {
	logs     []provider.LogFile
	finished map[string]bool
	current  string
}

// Engine runs the collectors. Its loop goroutine is the only one that touches the reducer.
type Engine struct {
	opts     Options
	provider *github.Provider
	red      *state.Reducer
	host     *target.HostTarget
	sampler  *hoststat.Sampler
	incus    bool

	targets      map[string]target.Target
	units        map[string][]target.Unit
	refs         map[string]provider.RunnerRef
	roots        map[string]string
	inspected    map[string]int
	index        map[string]*logIndex
	cpuSamples   map[string]cpuSample
	listenerSig  string
	titled       map[int64]bool
	scopes       map[string]bool
	sched        map[string]*schedule
	focus        string
	lastHostRead time.Time

	once    bool
	pending sync.WaitGroup
	applies chan func()
	control chan func()
}

type cpuSample struct {
	usage time.Duration
	at    time.Time
}

func New(opts Options) *Engine {
	red := state.NewReducer()
	red.SetAPIEnabled(opts.API != nil)
	return &Engine{
		opts:       opts,
		provider:   github.New(),
		red:        red,
		host:       target.NewHost(),
		sampler:    hoststat.NewSampler(opts.ProcRoot),
		incus:      target.IncusAvailable(),
		targets:    map[string]target.Target{},
		units:      map[string][]target.Unit{},
		refs:       map[string]provider.RunnerRef{},
		roots:      map[string]string{},
		inspected:  map[string]int{},
		index:      map[string]*logIndex{},
		cpuSamples: map[string]cpuSample{},
		titled:     map[int64]bool{},
		scopes:     map[string]bool{},
		sched:      map[string]*schedule{},
		applies:    make(chan func(), 256),
		control:    make(chan func(), 8),
	}
}

// Once runs every collector to completion and returns one snapshot.
func (e *Engine) Once(ctx context.Context) state.Snapshot {
	e.once = true
	e.sampleHost(time.Now())
	e.tick(ctx, time.Now())
	if e.incus {
		if apply, err := e.readContainers(ctx); err == nil {
			apply() // a second sample turns container CPU time into a percentage
		}
	}
	e.lastHostRead = time.Time{}
	e.sampleHost(time.Now())
	return e.red.Snapshot(time.Now())
}

// Run collects until ctx ends and sends a snapshot after every change. Slow readers only see the newest one.
func (e *Engine) Run(ctx context.Context, out chan state.Snapshot) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	e.tick(ctx, time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case apply := <-e.applies:
			apply()
		case change := <-e.control:
			change()
			e.tick(ctx, time.Now())
		case now := <-ticker.C:
			e.tick(ctx, now)
		}
		publish(out, e.red.Snapshot(time.Now()))
	}
}

func publish(out chan state.Snapshot, snap state.Snapshot) {
	select {
	case <-out:
	default:
	}
	out <- snap
}

// Refresh makes every source due now and re-enables disabled API endpoints.
func (e *Engine) Refresh() {
	e.control <- func() {
		for _, s := range e.sched {
			s.next, s.fails = time.Time{}, 0
		}
		if e.opts.API != nil {
			e.opts.API.Reset()
		}
	}
}

// Focus polls the given runner's job every second while its detail view is open.
func (e *Engine) Focus(key string) {
	e.control <- func() { e.focus = key }
}

func (e *Engine) tick(ctx context.Context, now time.Time) {
	e.sampleHost(now)
	if e.incus && e.due("incus list", now) {
		e.launch(ctx, "incus list", "incus list", everyContainers, e.readContainers)
	}
	e.barrier()
	e.scheduleUnits(ctx, now)
	e.barrier()
	ended := e.discover(now)
	e.scheduleInspect(ctx, now)
	e.barrier()
	e.scheduleIndex(ctx, now)
	e.barrier()
	e.scheduleJobs(ctx, now, ended)
	e.barrier()
	e.scheduleAPI(ctx, now)
	e.barrier()
}

func (e *Engine) sampleHost(now time.Time) {
	if now.Sub(e.lastHostRead) < everyHost {
		return
	}
	e.lastHostRead = now
	stats, err := e.sampler.Sample()
	e.red.SetSource("host stats", err, now)
	if err == nil {
		e.red.SetHost(stats)
	}
}

func (e *Engine) discover(now time.Time) map[string]state.Job {
	procs, err := scan.Scan(e.opts.ProcRoot)
	e.red.SetSource("proc", err, now)
	if err != nil {
		return nil
	}
	refs := e.provider.Claim(procs, e.units)
	e.refs = make(map[string]provider.RunnerRef, len(refs))
	presences := make([]state.Presence, 0, len(refs))
	var listeners []string
	for _, ref := range refs {
		if ref.Root == "" {
			ref.Root = e.roots[ref.Key]
		}
		e.refs[ref.Key] = ref
		presences = append(presences, ref.Presence)
		listeners = append(listeners, strconv.Itoa(ref.ListenerPID))
		if ref.WorkerPID != 0 && e.indexOf(ref.Key).current == "" {
			e.makeDue("index " + ref.Key)
		}
	}
	slices.Sort(listeners)
	if sig := strings.Join(listeners, ","); sig != e.listenerSig {
		e.listenerSig = sig
		for id := range e.targetsWithHost() {
			e.makeDue("units " + id)
		}
	}
	return e.red.Observe(presences, now)
}

func (e *Engine) targetsWithHost() map[string]target.Target {
	all := map[string]target.Target{target.HostID: e.host}
	for id, t := range e.targets {
		all[id] = t
	}
	return all
}

func (e *Engine) targetFor(ref provider.RunnerRef) target.Target {
	return e.targetsWithHost()[ref.Target]
}

func (e *Engine) indexOf(key string) *logIndex {
	idx, ok := e.index[key]
	if !ok {
		idx = &logIndex{finished: map[string]bool{}}
		e.index[key] = idx
	}
	return idx
}

func (e *Engine) readContainers(ctx context.Context) (func(), error) {
	containers, err := target.ListContainers(ctx)
	if err != nil {
		return nil, err
	}
	return func() { e.applyContainers(containers, time.Now()) }, nil
}

func (e *Engine) applyContainers(containers []target.Container, now time.Time) {
	running := map[string]bool{}
	stats := map[string]state.ContainerStats{}
	for _, c := range containers {
		if c.Project != "default" && c.Project != "" {
			continue
		}
		if ok, _ := path.Match(e.opts.ContainerGlob, c.Name); !ok || c.Status != "Running" {
			continue
		}
		running[c.Name] = true
		id := target.IncusID(c.Name)
		if _, ok := e.targets[id]; !ok {
			e.targets[id] = target.NewIncus(c.Name)
			e.makeDue("units " + id)
		}
		stat := state.ContainerStats{MemBytes: c.MemBytes, Procs: c.Processes}
		if previous, ok := e.cpuSamples[c.Name]; ok && now.After(previous.at) {
			stat.CPUPct = 100 * float64(c.CPUUsage-previous.usage) / float64(now.Sub(previous.at))
		}
		e.cpuSamples[c.Name] = cpuSample{usage: c.CPUUsage, at: now}
		stats[c.Name] = stat
	}
	for id, t := range e.targets {
		if c, ok := t.(*target.IncusTarget); ok && !running[c.Container] {
			delete(e.targets, id)
			delete(e.units, id)
		}
	}
	e.red.SetContainers(stats)
}

func (e *Engine) scheduleUnits(ctx context.Context, now time.Time) {
	for id, t := range e.targetsWithHost() {
		name := "units " + id
		if !e.due(name, now) {
			continue
		}
		e.launch(ctx, name, name, everyUnits, func(ctx context.Context) (func(), error) {
			units, err := t.Units(ctx)
			if err != nil {
				return nil, err
			}
			return func() {
				if _, alive := e.targetsWithHost()[id]; alive {
					e.units[id] = units
				}
			}, nil
		})
	}
}

func (e *Engine) scheduleInspect(ctx context.Context, now time.Time) {
	for key, ref := range e.refs {
		if pid, ok := e.inspected[key]; ok && pid == ref.ListenerPID {
			continue
		}
		t := e.targetFor(ref)
		name := "inspect " + key
		if t == nil || !e.due(name, now) {
			continue
		}
		e.launch(ctx, name, "inspect "+displayKey(key), retryInspect, func(ctx context.Context) (func(), error) {
			info, err := e.provider.Inspect(ctx, t, ref)
			if err != nil {
				return func() { e.red.SetNote(key, "no access to runner files") }, err
			}
			return func() {
				e.inspected[key] = ref.ListenerPID
				e.roots[key] = info.Root
				if current, ok := e.refs[key]; ok {
					current.Root = info.Root
					e.refs[key] = current
				}
				e.red.SetInfo(key, info.Info)
				e.red.SetNote(key, "")
				if info.DiagErr != nil {
					e.red.SetNote(key, "no access to _diag")
					e.red.SetSource("diag "+info.Name, info.DiagErr, time.Now())
				}
				if strings.HasPrefix(info.GitHubURL, "https://github.com/") {
					e.scopes[info.Scope] = true
				}
			}, nil
		})
	}
}

func (e *Engine) scheduleIndex(ctx context.Context, now time.Time) {
	for key, ref := range e.refs {
		t := e.targetFor(ref)
		name := "index " + key
		if t == nil || ref.Root == "" || !e.due(name, now) {
			continue
		}
		idx := e.indexOf(key)
		finished := maps.Clone(idx.finished)
		e.launch(ctx, name, "index "+displayKey(key), everyIndex, func(ctx context.Context) (func(), error) {
			logs, err := e.provider.WorkerLogs(ctx, t, ref)
			if err != nil {
				return nil, err
			}
			current := closestLog(logs, ref.WorkerStart, ref.WorkerPID != 0)
			var results []state.Result
			var running *state.Result
			var dead []string
			for _, log := range unread(logs, finished, now) {
				result, err := e.provider.ReadJob(ctx, t, ref, log)
				if err != nil {
					continue
				}
				switch {
				case result.Conclusion != "":
					results = append(results, result)
				case log.Name == current:
					running = &result
				case now.Sub(log.Started) > deadLogAge:
					dead = append(dead, log.Name)
				}
			}
			return func() {
				idx.logs, idx.current = logs, current
				for _, result := range results {
					idx.finished[logName(result, logs)] = true
				}
				for _, name := range dead {
					idx.finished[name] = true
				}
				e.red.AddResults(key, results)
				if running != nil {
					e.red.SetCurrent(key, running.Job)
				}
			}, nil
		})
	}
}

func logName(result state.Result, logs []provider.LogFile) string {
	for _, log := range logs {
		if log.Started.Equal(result.StartedAt) {
			return log.Name
		}
	}
	return ""
}

// unread picks the Worker logs worth parsing: unseen, inside the history window, newest first.
func unread(logs []provider.LogFile, finished map[string]bool, now time.Time) []provider.LogFile {
	var out []provider.LogFile
	for i := len(logs) - 1; i >= 0 && len(out) < historyPerRun; i-- {
		log := logs[i]
		if !finished[log.Name] && now.Sub(log.Started) < historyWindow {
			out = append(out, log)
		}
	}
	return out
}

// closestLog matches a running Worker to the log whose filename time is nearest its start.
func closestLog(logs []provider.LogFile, workerStart time.Time, busy bool) string {
	if !busy {
		return ""
	}
	best, bestGap := "", 2*time.Minute
	for _, log := range logs {
		gap := log.Started.Sub(workerStart).Abs()
		if gap < bestGap {
			best, bestGap = log.Name, gap
		}
	}
	return best
}

func (e *Engine) scheduleJobs(ctx context.Context, now time.Time, ended map[string]state.Job) {
	for key := range e.red.Busy() {
		ref, idx := e.refs[key], e.indexOf(key)
		t := e.targetFor(ref)
		log, ok := e.logByName(idx, idx.current)
		interval := everyJob
		if key == e.focus {
			interval = everyJobFocused
		}
		name := "job " + key
		if t == nil || !ok || !e.due(name, now) {
			continue
		}
		e.launch(ctx, name, "", interval, func(ctx context.Context) (func(), error) {
			result, err := e.provider.ReadJob(ctx, t, ref, log)
			if err != nil {
				return nil, err
			}
			return func() {
				if result.Conclusion == "" {
					e.red.SetCurrent(key, result.Job)
				}
			}, nil
		})
	}
	for key, job := range ended {
		ref, idx := e.refs[key], e.indexOf(key)
		t := e.targetFor(ref)
		log, ok := e.logByName(idx, idx.current)
		idx.current = ""
		if t == nil {
			e.red.EndJob(key, job, state.ConclusionUnknown, now)
			continue
		}
		if !ok {
			e.makeDue("index " + key) // a job shorter than one index pass: the index will find its result
			continue
		}
		e.launch(ctx, "result "+key, "", 0, func(ctx context.Context) (func(), error) {
			result, err := e.readResult(ctx, t, ref, log)
			return func() {
				if result.Conclusion == "" {
					e.red.EndJob(key, job, state.ConclusionUnknown, time.Now())
					return
				}
				e.indexOf(key).finished[log.Name] = true
				e.red.AddResults(key, []state.Result{result})
			}, err
		})
	}
}

func (e *Engine) readResult(ctx context.Context, t target.Target, ref provider.RunnerRef, log provider.LogFile) (state.Result, error) {
	var lastErr error
	for attempt := range 3 {
		if attempt > 0 && !e.once {
			time.Sleep(time.Second)
		}
		result, err := e.provider.ReadJob(ctx, t, ref, log)
		if err == nil && result.Conclusion != "" {
			return result, nil
		}
		lastErr = err
	}
	return state.Result{}, lastErr
}

func (e *Engine) logByName(idx *logIndex, name string) (provider.LogFile, bool) {
	for _, log := range idx.logs {
		if log.Name == name {
			return log, true
		}
	}
	return provider.LogFile{}, false
}

func (e *Engine) scheduleAPI(ctx context.Context, now time.Time) {
	api := e.opts.API
	if api == nil {
		return
	}
	for scope := range e.scopes {
		name := "api runners " + scope
		if !e.due(name, now) {
			continue
		}
		e.launch(ctx, name, name, everyAPIRunners, func(ctx context.Context) (func(), error) {
			runners, err := api.Runners(ctx, scope)
			if err != nil {
				return nil, err
			}
			return func() { e.red.SetAPIRunners(scope, runners) }, nil
		})
	}
	runs := map[int64]state.Job{}
	for _, job := range e.red.Busy() {
		if job.RunID != 0 && strings.HasPrefix(job.URL, "https://github.com/") {
			runs[job.RunID] = job
		}
	}
	for runID, job := range runs {
		name := "api jobs " + strconv.FormatInt(runID, 10)
		if e.due(name, now) {
			e.launch(ctx, name, "api jobs "+job.Repo, everyAPIJobs, func(ctx context.Context) (func(), error) {
				jobs, err := api.RunJobs(ctx, job.Repo, runID, job.Attempt)
				if err != nil {
					return nil, err
				}
				return func() { e.red.SetAPIJobs(runID, jobs) }, nil
			})
		}
		if !e.titled[runID] {
			e.titled[runID] = true
			e.launch(ctx, "api run "+strconv.FormatInt(runID, 10), "", 0, func(ctx context.Context) (func(), error) {
				title, err := api.RunTitle(ctx, job.Repo, runID)
				if err != nil {
					return nil, err
				}
				return func() { e.red.SetRunTitle(runID, title) }, nil
			})
		}
	}
}

func (e *Engine) due(name string, now time.Time) bool {
	s, ok := e.sched[name]
	if !ok {
		return true
	}
	return !s.inflight && !now.Before(s.next)
}

func (e *Engine) makeDue(name string) {
	if s, ok := e.sched[name]; ok {
		s.next = time.Time{}
	}
}

// launch runs slow work off the loop. A failing source backs off 2x per failure, up to a minute.
// source names the health entry the result updates; empty for one-shot work.
func (e *Engine) launch(ctx context.Context, name, source string, interval time.Duration, work func(context.Context) (func(), error)) {
	s, ok := e.sched[name]
	if !ok {
		s = &schedule{}
		e.sched[name] = s
	}
	s.inflight = true
	e.pending.Add(1)
	go func() {
		defer e.pending.Done()
		apply, err := work(ctx)
		e.applies <- func() {
			now := time.Now()
			s.inflight = false
			if err != nil {
				s.fails++
				s.next = now.Add(min(max(interval, time.Second)<<s.fails, maxBackoff))
			} else {
				s.fails = 0
				s.next = now.Add(interval)
			}
			if source != "" {
				e.red.SetSource(source, err, now)
			}
			if apply != nil {
				apply()
			}
		}
	}()
}

// displayKey turns "github|incus:runner-acme-1|unit" into "incus:runner-acme-1/unit".
func displayKey(key string) string {
	_, rest, _ := strings.Cut(key, "|")
	return strings.Replace(rest, "|", "/", 1)
}

// barrier makes Once deterministic: each phase finishes before the next one reads its results.
func (e *Engine) barrier() {
	if !e.once {
		return
	}
	done := make(chan struct{})
	go func() {
		e.pending.Wait()
		close(done)
	}()
	for {
		select {
		case apply := <-e.applies:
			apply()
		case <-done:
			for {
				select {
				case apply := <-e.applies:
					apply()
				default:
					return
				}
			}
		}
	}
}

// Targets lists the targets the engine knows about, for doctor.
func (e *Engine) Targets() []string {
	var ids []string
	for id := range e.targetsWithHost() {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids
}
