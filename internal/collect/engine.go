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
	everyHost           = 2 * time.Second
	everyContainerStats = 5 * time.Second
	everyContainerList  = 60 * time.Second
	everyUnits          = 60 * time.Second
	everyUnitsEmpty     = 10 * time.Minute
	everyIndex          = 60 * time.Second
	everyJob            = 3 * time.Second
	everyJobFocused     = 1 * time.Second
	everyAPIRunners     = 30 * time.Second
	everyAPIJobs        = 10 * time.Second
	retryInspect        = 30 * time.Second
	maxBackoff          = 60 * time.Second
	historyWindow       = 24 * time.Hour
	historyPerRun       = 20
	deadLogAge          = time.Minute
	unreadableLogAge    = 10 * time.Minute
	clockSkew           = 5 * time.Minute
	warmupMax           = 10 * time.Second
	noteLogsUnreadable  = "logs unreadable"
)

type Options struct {
	ProcRoot      string
	CgroupRoot    string
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
	finished map[string]time.Time
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

	targets           map[string]target.Target
	units             map[string][]target.Unit
	refs              map[string]provider.RunnerRef
	roots             map[string]string
	inspected         map[string]int
	index             map[string]*logIndex
	cpuSamples        map[string]cpuSample
	procCounts        map[string]int
	listenerSig       string
	titled            map[int64]bool
	scopes            map[string]bool
	sched             map[string]*schedule
	focus             string
	lastHostRead      time.Time
	lastContainerRead time.Time
	warm              bool

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
		procCounts: map[string]int{},
		titled:     map[int64]bool{},
		scopes:     map[string]bool{},
		sched:      map[string]*schedule{},
		applies:    make(chan func(), 256),
		control:    make(chan func(), 8),
	}
}

func (e *Engine) Once(ctx context.Context) state.Snapshot {
	e.once = true
	e.sampleHost(time.Now())
	e.tick(ctx, time.Now())
	// second samples turn CPU time into a percentage
	e.lastHostRead, e.lastContainerRead = time.Time{}, time.Time{}
	e.sampleHost(time.Now())
	e.sampleContainers(time.Now())
	return e.red.Snapshot(time.Now())
}

// Run collects until ctx ends and sends a snapshot each second once discovery settles. Slow readers only see the newest one.
func (e *Engine) Run(ctx context.Context, out chan state.Snapshot) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	start := time.Now()
	e.tick(ctx, start)
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
			e.warm = e.warm || e.settled() || now.Sub(start) > warmupMax
			if e.warm {
				publish(out, e.red.Snapshot(now))
			}
		}
	}
}

// settled reports whether every runner found so far is identified and indexed, so the board never opens half built.
func (e *Engine) settled() bool {
	if e.incus && e.sched["incus list"] == nil {
		return false
	}
	for _, s := range e.sched {
		if s.inflight {
			return false
		}
	}
	for id := range e.targets {
		if e.sched["units "+id] == nil {
			return false
		}
	}
	for key, ref := range e.refs {
		if e.targetFor(ref) == nil {
			continue
		}
		if e.sched["inspect "+key] == nil || (ref.Root != "" && e.sched["index "+key] == nil) {
			return false
		}
	}
	return true
}

func publish(out chan state.Snapshot, snap state.Snapshot) {
	select {
	case <-out:
	default:
	}
	out <- snap
}

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

func (e *Engine) Focus(key string) {
	e.control <- func() { e.focus = key }
}

func (e *Engine) tick(ctx context.Context, now time.Time) {
	e.sampleHost(now)
	e.sampleContainers(now)
	if e.incus && e.due("incus list", now) {
		e.launch(ctx, "incus list", "incus list", everyContainerList, e.readContainers)
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
	e.prune(now)
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

func (e *Engine) sampleContainers(now time.Time) {
	if now.Sub(e.lastContainerRead) < everyContainerStats {
		return
	}
	e.lastContainerRead = now
	stats := map[string]state.ContainerStats{}
	var lastErr error
	for _, t := range e.targets {
		c, ok := t.(*target.IncusTarget)
		if !ok {
			continue
		}
		cpu, mem, err := target.CgroupUsage(e.opts.CgroupRoot, c.Container)
		if err != nil {
			lastErr = err
			e.makeDue("incus list") // the container most likely stopped
			continue
		}
		stat := state.ContainerStats{MemBytes: mem, Procs: e.procCounts[c.Container]}
		if previous, ok := e.cpuSamples[c.Container]; ok && now.After(previous.at) {
			stat.CPUPct = 100 * float64(cpu-previous.usage) / float64(now.Sub(previous.at))
		}
		e.cpuSamples[c.Container] = cpuSample{usage: cpu, at: now}
		stats[c.Container] = stat
	}
	e.red.SetSource("container stats", lastErr, now)
	e.red.SetContainers(stats)
}

func (e *Engine) discover(now time.Time) map[string]state.Job {
	procs, err := scan.Scan(e.opts.ProcRoot)
	e.red.SetSource("proc", err, now)
	if err != nil {
		return nil
	}
	refs := e.provider.Claim(procs, e.units)
	previous := e.refs
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
	for key := range previous {
		if _, ok := e.refs[key]; !ok {
			e.forgetRunner(key)
		}
	}
	return e.red.Observe(presences, now)
}

func (e *Engine) forgetRunner(key string) {
	delete(e.index, key)
	delete(e.roots, key)
	delete(e.inspected, key)
	for _, kind := range []string{"inspect ", "index ", "job ", "result "} {
		delete(e.sched, kind+key)
	}
	shown := displayKey(key)
	e.red.DropSources(func(name string) bool {
		return name == "inspect "+shown || name == "index "+shown || name == "diag "+shown
	})
}

func (e *Engine) forgetTarget(id string) {
	delete(e.targets, id)
	delete(e.sched, "units "+id)
	e.red.DropSources(func(name string) bool {
		return name == "units "+id || strings.HasPrefix(name, "inspect "+id+"/") ||
			strings.HasPrefix(name, "index "+id+"/") || strings.HasPrefix(name, "diag "+id+"/")
	})
}

// prune keeps an always-on board from growing: it forgets runs nobody is busy with and logs past the history window.
func (e *Engine) prune(now time.Time) {
	cutoff := now.Add(-historyWindow)
	e.red.Prune(cutoff.Add(-time.Hour))
	for _, idx := range e.index {
		maps.DeleteFunc(idx.finished, func(_ string, started time.Time) bool { return started.Before(cutoff) })
	}
	busyRuns, busyRepos := map[string]bool{}, map[string]bool{}
	for _, job := range e.red.Busy() {
		busyRuns[strconv.FormatInt(job.RunID, 10)] = true
		busyRepos[job.Repo] = true
	}
	for runID := range e.titled {
		if !busyRuns[strconv.FormatInt(runID, 10)] {
			delete(e.titled, runID)
		}
	}
	maps.DeleteFunc(e.sched, func(name string, s *schedule) bool {
		rest, ok := strings.CutPrefix(name, "api run ")
		if !ok {
			rest, ok = strings.CutPrefix(name, "api jobs ")
			rest, _, _ = strings.Cut(rest, "/")
		}
		return ok && !s.inflight && !busyRuns[rest]
	})
	e.red.DropSources(func(name string) bool {
		for _, prefix := range []string{"api jobs ", "api run "} {
			if repo, ok := strings.CutPrefix(name, prefix); ok {
				return !busyRepos[repo]
			}
		}
		return false
	})
	maps.DeleteFunc(e.cpuSamples, func(container string, _ cpuSample) bool {
		_, ok := e.targets[target.IncusID(container)]
		return !ok
	})
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
		idx = &logIndex{finished: map[string]time.Time{}}
		e.index[key] = idx
	}
	return idx
}

func (e *Engine) readContainers(ctx context.Context) (func(), error) {
	containers, err := target.ListContainers(ctx)
	if err != nil {
		return nil, err
	}
	return func() { e.applyContainers(containers) }, nil
}

// applyContainers keeps a target per running container. A stopped one keeps its units, marked stopped, so its runners show offline.
func (e *Engine) applyContainers(containers []target.Container) {
	watched := map[string]target.Container{}
	for _, c := range containers {
		// the glob was validated by path.Match when the flags were parsed
		if matched, _ := path.Match(e.opts.ContainerGlob, c.Name); matched && (c.Project == "default" || c.Project == "") {
			watched[c.Name] = c
		}
	}
	for id, t := range e.targets {
		if c, ok := t.(*target.IncusTarget); ok && watched[c.Container].Status != "Running" {
			e.forgetTarget(id)
		}
	}
	for id, units := range e.units {
		name, isContainer := strings.CutPrefix(id, "incus:")
		c, listed := watched[name]
		switch {
		case !isContainer:
		case !listed:
			delete(e.units, id)
		case c.Status != "Running":
			e.units[id] = stoppedUnits(units, c.Status)
		}
	}
	e.procCounts = map[string]int{}
	for name, c := range watched {
		e.procCounts[name] = c.Processes
		id := target.IncusID(name)
		if _, ok := e.targets[id]; !ok && c.Status == "Running" {
			e.targets[id] = target.NewIncus(name)
			e.makeDue("units " + id)
		}
	}
	e.lastContainerRead = time.Time{}
	e.sampleContainers(time.Now())
}

func stoppedUnits(units []target.Unit, containerStatus string) []target.Unit {
	stopped := make([]target.Unit, len(units))
	for i, unit := range units {
		stopped[i] = target.Unit{Name: unit.Name, Active: "container " + strings.ToLower(containerStatus), Sub: "dead"}
	}
	return stopped
}

func (e *Engine) scheduleUnits(ctx context.Context, now time.Time) {
	for id, t := range e.targetsWithHost() {
		name := "units " + id
		if !e.due(name, now) {
			continue
		}
		interval := everyUnits
		if id != target.HostID && len(e.units[id]) == 0 {
			interval = everyUnitsEmpty // a runner starting there shows up in /proc first, which makes this due again
		}
		e.launch(ctx, name, name, interval, func(ctx context.Context) (func(), error) {
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
				}
				e.red.SetSource("diag "+displayKey(key), info.DiagErr, time.Now())
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
		source := "index " + displayKey(key)
		e.launch(ctx, name, source, everyIndex, func(ctx context.Context) (func(), error) {
			logs, err := e.provider.WorkerLogs(ctx, t, ref)
			if err != nil {
				return func() { e.red.SetNote(key, noteLogsUnreadable) }, err
			}
			current := closestLog(logs, ref.WorkerStart, ref.WorkerPID != 0)
			var results []state.Result
			var running *state.Result
			var dead []provider.LogFile
			var unreadable error
			for _, log := range unread(logs, finished, now) {
				result, err := e.provider.ReadJob(ctx, t, ref, log)
				switch {
				case err != nil && log.Name != current && now.Sub(log.Started) > unreadableLogAge:
					dead, unreadable = append(dead, log), err
				case err != nil:
				case result.Conclusion != "":
					results = append(results, result)
				case log.Name == current:
					running = &result
				case now.Sub(log.Started) > deadLogAge:
					dead = append(dead, log)
				}
			}
			return func() {
				idx.logs = logs
				if e.refs[key].WorkerPID == ref.WorkerPID {
					idx.current = current
				}
				for _, result := range results {
					idx.finished[logName(result, logs)] = result.StartedAt
				}
				for _, log := range dead {
					idx.finished[log.Name] = log.Started
				}
				e.red.ClearNote(key, noteLogsUnreadable)
				if unreadable != nil {
					e.red.SetSource(source, unreadable, time.Now())
				}
				e.red.AddResults(key, results)
				if running != nil {
					e.red.SetCurrent(key, ref.WorkerPID, running.Job)
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

func unread(logs []provider.LogFile, finished map[string]time.Time, now time.Time) []provider.LogFile {
	var out []provider.LogFile
	for i := len(logs) - 1; i >= 0 && len(out) < historyPerRun; i-- {
		log := logs[i]
		_, done := finished[log.Name]
		if !done && now.Sub(log.Started) < historyWindow && log.Started.Before(now.Add(clockSkew)) {
			out = append(out, log)
		}
	}
	return out
}

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
					e.red.SetCurrent(key, ref.WorkerPID, result.Job)
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
					// shown as unknown until the index reads the real conclusion, which then replaces it
					if job.ID == "" && result.ID != "" {
						job = result.Job
					}
					e.red.EndJob(key, job, state.ConclusionUnknown, time.Now())
					e.makeDue("index " + key)
					return
				}
				e.indexOf(key).finished[log.Name] = log.Started
				e.red.AddResults(key, []state.Result{result})
			}, err
		})
	}
}

// readResult retries while the Worker flushes its last lines. Without a conclusion it returns what it read.
func (e *Engine) readResult(ctx context.Context, t target.Target, ref provider.RunnerRef, log provider.LogFile) (state.Result, error) {
	var result state.Result
	var err error
	for attempt := range 3 {
		if attempt > 0 && !e.once {
			time.Sleep(time.Second)
		}
		result, err = e.provider.ReadJob(ctx, t, ref, log)
		if err == nil && result.Conclusion != "" {
			return result, nil
		}
	}
	return result, err
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
				return func() { e.red.SetAPIRunners(scope, nil) }, err
			}
			return func() { e.red.SetAPIRunners(scope, runners) }, nil
		})
	}
	runs := map[state.RunAttempt]state.Job{}
	for _, job := range e.red.Busy() {
		if job.RunID != 0 && strings.HasPrefix(job.URL, "https://github.com/") {
			runs[state.RunAttempt{RunID: job.RunID, Attempt: job.Attempt}] = job
		}
	}
	for run, job := range runs {
		runID := strconv.FormatInt(run.RunID, 10)
		name := "api jobs " + runID + "/" + strconv.Itoa(run.Attempt)
		if e.due(name, now) {
			e.launch(ctx, name, "api jobs "+job.Repo, everyAPIJobs, func(ctx context.Context) (func(), error) {
				jobs, err := api.RunJobs(ctx, job.Repo, run.RunID, run.Attempt)
				if err != nil {
					return nil, err
				}
				return func() { e.red.SetAPIJobs(run, jobs) }, nil
			})
		}
		name = "api run " + runID
		if !e.titled[run.RunID] && e.due(name, now) {
			e.launch(ctx, name, "api run "+job.Repo, everyAPIJobs, func(ctx context.Context) (func(), error) {
				title, err := api.RunTitle(ctx, job.Repo, run.RunID)
				if err != nil {
					return nil, err
				}
				return func() {
					e.titled[run.RunID] = true
					e.red.SetRunTitle(run.RunID, title)
				}, nil
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
