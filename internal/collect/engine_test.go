package collect

import (
	"context"
	"errors"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/YoanWai/runrack/internal/provider"
	"github.com/YoanWai/runrack/internal/scan"
	"github.com/YoanWai/runrack/internal/state"
	"github.com/YoanWai/runrack/internal/target"
)

const (
	testContainer = "runner-acme-1"
	testUnit      = "actions.runner.acme.zenbook-acme-1.service"
	testKey       = "github|incus:" + testContainer + "|" + testUnit
	testLog       = "/r/_diag/Worker_20261123-174638-utc.log"
)

var (
	testWorkerStart = time.Date(2026, 11, 23, 17, 46, 38, 0, time.UTC)
	testNow         = time.Date(2026, 11, 23, 17, 50, 0, 0, time.UTC)
)

// fakeTarget serves files from memory, the way an Incus container would through incus exec.
type fakeTarget struct {
	mu    sync.Mutex
	files map[string][]byte
}

func (f *fakeTarget) ID() string { return target.IncusID(testContainer) }

func (f *fakeTarget) set(name string, data []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.files[name] = data
}

func (f *fakeTarget) read(name string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	data, ok := f.files[name]
	if !ok {
		return nil, os.ErrNotExist
	}
	return data, nil
}

func (f *fakeTarget) Head(_ context.Context, name string, maxBytes int64) ([]byte, error) {
	data, err := f.read(name)
	return data[:min(int64(len(data)), maxBytes)], err
}

func (f *fakeTarget) List(_ context.Context, dir string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var names []string
	for name := range f.files {
		if path.Dir(name) == dir {
			names = append(names, path.Base(name))
		}
	}
	return names, nil
}

func (f *fakeTarget) Grep(_ context.Context, name string, fixed ...string) ([]string, error) {
	data, err := f.read(name)
	var lines []string
	for line := range strings.SplitSeq(string(data), "\n") {
		if slices.ContainsFunc(fixed, func(s string) bool { return strings.Contains(line, s) }) {
			lines = append(lines, line)
		}
	}
	return lines, err
}

func (f *fakeTarget) Units(context.Context) ([]target.Unit, error) { return nil, nil }

func (f *fakeTarget) WorkingDirectory(context.Context, string) (string, error) { return "/r", nil }

func workerLog(t *testing.T, finished bool) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("../../testdata/github", path.Base(testLog)))
	if err != nil {
		t.Fatal(err)
	}
	if finished {
		return data
	}
	var kept []string
	for line := range strings.SplitSeq(string(data), "\n") {
		if !strings.Contains(line, "Job result after all job steps finish") {
			kept = append(kept, line)
		}
	}
	return []byte(strings.Join(kept, "\n"))
}

func newTestEngine(t *testing.T, logFinished bool) (*Engine, *fakeTarget) {
	t.Helper()
	ft := &fakeTarget{files: map[string][]byte{testLog: workerLog(t, logFinished)}}
	e := New(Options{CgroupRoot: t.TempDir(), ContainerGlob: "*"})
	e.incus = false
	e.targets[ft.ID()] = ft
	return e, ft
}

func refWithWorker(pid int, started time.Time) provider.RunnerRef {
	return provider.RunnerRef{
		Presence: state.Presence{
			Key: testKey, Name: testUnit, Provider: "github", Target: target.IncusID(testContainer),
			Placement: scan.Placement{Kind: scan.KindIncus, Container: testContainer, Unit: testUnit},
			Service:   "active", Online: true, WorkerPID: pid, WorkerStart: started,
		},
		Root:        "/r",
		ListenerPID: 10,
	}
}

func (e *Engine) observe(ref provider.RunnerRef) map[string]state.Job {
	e.refs = map[string]provider.RunnerRef{ref.Key: ref}
	return e.red.Observe([]state.Presence{ref.Presence}, testNow)
}

func onlyRunner(t *testing.T, e *Engine) state.Runner {
	t.Helper()
	snap := e.red.Snapshot(testNow)
	if len(snap.Runners) != 1 {
		t.Fatalf("want 1 runner, got %+v", snap.Runners)
	}
	return snap.Runners[0]
}

func TestIndexFillsTheRunningJob(t *testing.T) {
	e, _ := newTestEngine(t, false)
	e.once = true
	e.observe(refWithWorker(100, testWorkerStart))
	e.scheduleIndex(t.Context(), testNow)
	e.barrier()
	if e.index[testKey].current != path.Base(testLog) {
		t.Fatalf("current = %q", e.index[testKey].current)
	}
	if job := onlyRunner(t, e).Job; job == nil || job.Repo == "" {
		t.Fatalf("job = %+v", job)
	}
}

func TestIndexOfEndedJobDoesNotLeakIntoTheNextOne(t *testing.T) {
	e, _ := newTestEngine(t, false)
	e.observe(refWithWorker(100, testWorkerStart))
	e.scheduleIndex(t.Context(), testNow)

	// the job ends and the next one starts before the index pass answers
	e.observe(refWithWorker(200, testWorkerStart.Add(3*time.Minute)))
	(<-e.applies)()

	if current := e.index[testKey].current; current != "" {
		t.Fatalf("stale current %q survived", current)
	}
	if job := onlyRunner(t, e).Job; job == nil || job.HasMetadata() {
		t.Fatalf("the new job got the old job's details: %+v", job)
	}
}

func TestUnreadableResultIsReplacedByTheRealOne(t *testing.T) {
	e, ft := newTestEngine(t, false)
	e.once = true
	e.observe(refWithWorker(100, testWorkerStart))
	e.scheduleIndex(t.Context(), testNow)
	e.barrier()

	ended := e.observe(refWithWorker(0, time.Time{}))
	e.scheduleJobs(t.Context(), testNow, ended)
	e.barrier()
	recent := e.red.Snapshot(testNow).Recent
	if len(recent) != 1 || recent[0].Conclusion != state.ConclusionUnknown || recent[0].ID == "" {
		t.Fatalf("before the log is flushed: %+v", recent)
	}

	ft.set(testLog, workerLog(t, true))
	e.scheduleIndex(t.Context(), testNow)
	e.barrier()
	recent = e.red.Snapshot(testNow).Recent
	if len(recent) != 1 || recent[0].Conclusion != state.ConclusionCancelled {
		t.Fatalf("after the log is flushed: %+v", recent)
	}
	if last := onlyRunner(t, e).Last; last == nil || last.Conclusion != state.ConclusionCancelled {
		t.Fatalf("last = %+v", last)
	}
}

func TestContainerStopKeepsRunnersOfflineAndRemovalClearsErrors(t *testing.T) {
	cgroups := t.TempDir()
	dir := filepath.Join(cgroups, "lxc.payload."+testContainer)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "cpu.stat"), []byte("usage_usec 1000\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "memory.current"), []byte("4096\n"), 0o644)
	e := New(Options{CgroupRoot: cgroups, ContainerGlob: "runner-*"})
	id := target.IncusID(testContainer)

	e.applyContainers([]target.Container{{Name: testContainer, Status: "Running"}, {Name: "other", Status: "Running"}})
	if len(e.targets) != 1 || e.targets[id] == nil {
		t.Fatalf("targets = %v", e.targets)
	}
	e.units[id] = []target.Unit{{Name: testUnit, Active: "active", Sub: "running"}}
	e.red.SetSource("units "+id, errors.New("Instance not found"), testNow)
	e.red.SetSource("index "+id+"/"+testUnit, errors.New("Instance not found"), testNow)

	e.applyContainers([]target.Container{{Name: testContainer, Status: "Stopped"}})
	if len(e.targets) != 0 {
		t.Fatalf("stopped container kept as target")
	}
	for _, s := range e.red.Snapshot(testNow).Sources {
		if !s.OK {
			t.Fatalf("error left behind: %+v", s)
		}
	}
	refs := e.provider.Claim(nil, e.units)
	if len(refs) != 1 || refs[0].Online || refs[0].Service != "container stopped" {
		t.Fatalf("refs = %+v", refs)
	}

	e.applyContainers(nil)
	if len(e.units) != 0 {
		t.Fatalf("deleted container kept its units: %v", e.units)
	}
}

func TestUnreadOnlySkipsFutureAndFinishedLogs(t *testing.T) {
	logs := []provider.LogFile{
		{Name: "old", Started: testNow.Add(-25 * time.Hour)},
		{Name: "done", Started: testNow.Add(-time.Hour)},
		{Name: "open", Started: testNow.Add(-time.Minute)},
		{Name: "future", Started: testNow.Add(time.Hour)},
	}
	got := unread(logs, map[string]time.Time{"done": logs[1].Started}, testNow)
	if len(got) != 1 || got[0].Name != "open" {
		t.Fatalf("unread = %+v", got)
	}
}
