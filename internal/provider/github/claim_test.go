package github

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"strings"
	"testing"
	"time"

	"github.com/YoanWai/runrack/internal/provider"
	"github.com/YoanWai/runrack/internal/scan"
	"github.com/YoanWai/runrack/internal/state"
	"github.com/YoanWai/runrack/internal/target"
)

func TestClaim(t *testing.T) {
	procs, err := scan.Scan("../../../testdata/proc")
	if err != nil {
		t.Fatal(err)
	}
	units := map[string][]target.Unit{
		"incus:runner-acme-1": {{Name: "actions.runner.acme.zenbook-acme-1.service", Active: "active", Sub: "running"}},
		"incus:runner-acme-3": {{Name: "actions.runner.acme.zenbook-acme-3.service", Active: "failed", Sub: "failed"}},
	}
	refs := Claim(procs, units)
	byKey := map[string]provider.RunnerRef{}
	for _, ref := range refs {
		byKey[ref.Key] = ref
	}
	if len(refs) != 5 {
		t.Fatalf("want 4 Listeners (fake one excluded) + 1 offline unit, got %d: %+v", len(refs), refs)
	}
	busy := byKey["github|incus:runner-acme-1|actions.runner.acme.zenbook-acme-1.service"]
	if busy.ListenerPID != 28424 || busy.WorkerPID != 92911 || busy.Root != "/opt/actions-runner" || !busy.Online {
		t.Fatalf("busy runner = %+v", busy)
	}
	idle := byKey["github|incus:runner-acme-2|actions.runner.acme.zenbook-acme-2.service"]
	if idle.ListenerPID != 29645 || idle.WorkerPID != 0 {
		t.Fatalf("idle runner = %+v", idle)
	}
	host := byKey["github|host|actions.runner.acme.zenbook-host.service"]
	if host.Root != "/srv/runners/zenbook-host" || host.Placement.Kind != scan.KindHost {
		t.Fatalf("host runner = %+v", host)
	}
	offline := byKey["github|incus:runner-acme-3|actions.runner.acme.zenbook-acme-3.service"]
	if offline.Online || offline.Service != "failed" || offline.Placement.Container != "runner-acme-3" {
		t.Fatalf("offline runner = %+v", offline)
	}
	for _, ref := range refs {
		if ref.ListenerPID == 92950 {
			t.Fatal("a Runner.Listener started by a Worker is a job, not a runner")
		}
	}
}

// fakeTarget serves testdata files as if they lived under /opt/actions-runner.
type fakeTarget struct {
	files map[string]string
	units []target.Unit
}

func (f *fakeTarget) ID() string { return "incus:runner-acme-1" }

func (f *fakeTarget) Head(_ context.Context, p string, maxBytes int64) ([]byte, error) {
	if err := target.CheckPath(p); err != nil {
		return nil, err
	}
	src, ok := f.files[p]
	if !ok {
		return nil, fs.ErrNotExist
	}
	data, err := os.ReadFile(src)
	return data[:min(int64(len(data)), maxBytes)], err
}

func (f *fakeTarget) List(_ context.Context, dir string) ([]string, error) {
	var names []string
	for p := range f.files {
		if path.Dir(p) == dir {
			names = append(names, path.Base(p))
		}
	}
	if len(names) == 0 {
		return nil, fs.ErrPermission
	}
	return names, nil
}

func (f *fakeTarget) Grep(ctx context.Context, p string, fixed ...string) ([]string, error) {
	data, err := f.Head(ctx, p, 1<<30)
	if err != nil {
		return nil, err
	}
	var lines []string
	for line := range strings.SplitSeq(string(data), "\n") {
		for _, s := range fixed {
			if strings.Contains(line, s) {
				lines = append(lines, line)
				break
			}
		}
	}
	return lines, nil
}

func (f *fakeTarget) Units(context.Context) ([]target.Unit, error) { return f.units, nil }

func (f *fakeTarget) WorkingDirectory(context.Context, string) (string, error) {
	return "/opt/actions-runner", nil
}

func newFakeTarget() *fakeTarget {
	const td = "../../../testdata/github/"
	return &fakeTarget{files: map[string]string{
		"/opt/actions-runner/.runner":                                td + "runner.json",
		"/opt/actions-runner/_diag/Runner_20261123-173249-utc.log":   td + "Runner_20261123-173249-utc.log",
		"/opt/actions-runner/_diag/Worker_20261123-173750-utc.log":   td + "Worker_20261123-173750-utc.log",
		"/opt/actions-runner/_diag/Worker_20261123-173811-utc.log":   td + "Worker_20261123-173811-utc.log",
		"/opt/actions-runner/_diag/Worker_20261123-174638-utc.log":   td + "Worker_20261123-174638-utc.log",
		"/opt/actions-runner/.credentials":                           td + "runner.json",
		"/opt/actions-runner/_diag/pages/0b1f0a8e_7b3c.log":          td + "runner.json",
		"/opt/actions-runner/_diag/blocks/0b1f0a8e_7b3c.1":           td + "runner.json",
		"/opt/actions-runner/_diag/Worker_20261123-180000-utc.log.1": td + "runner.json",
	}}
}

func TestInspectOfflineRunnerFindsRootFromUnit(t *testing.T) {
	ref := provider.RunnerRef{}
	ref.Placement = scan.Placement{Kind: scan.KindIncus, Container: "runner-acme-1", Unit: "actions.runner.acme.zenbook-acme-1.service"}
	info, err := New().Inspect(t.Context(), newFakeTarget(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "zenbook-acme-1" || info.Scope != "acme" || info.Version != "2.338.2" || info.Root != "/opt/actions-runner" || info.DiagErr != nil {
		t.Fatalf("got %+v", info)
	}
	if strings.Join(info.Labels, ",") != "self-hosted,Linux,X64,homelab" {
		t.Fatalf("labels %v", info.Labels)
	}
}

func TestInspectReportsUnreadableDiag(t *testing.T) {
	fake := newFakeTarget()
	for p := range fake.files {
		if strings.Contains(p, "_diag") {
			delete(fake.files, p)
		}
	}
	ref := provider.RunnerRef{Root: "/opt/actions-runner"}
	info, err := New().Inspect(t.Context(), fake, ref)
	if err != nil || !errors.Is(info.DiagErr, fs.ErrPermission) || info.Name != "zenbook-acme-1" {
		t.Fatalf("info %+v, err %v", info, err)
	}
}

func TestWorkerLogsAndReadJob(t *testing.T) {
	p, fake := New(), newFakeTarget()
	ref := provider.RunnerRef{Root: "/opt/actions-runner"}
	logs, err := p.WorkerLogs(t.Context(), fake, ref)
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 3 || logs[0].Name != "Worker_20261123-173750-utc.log" {
		t.Fatalf("logs %+v", logs)
	}
	result, err := p.ReadJob(t.Context(), fake, ref, logs[1])
	if err != nil {
		t.Fatal(err)
	}
	if result.Conclusion != state.ConclusionFailure || result.Repo != "acme/widgets" || time.Duration(result.Duration) != 5*time.Minute+48*time.Second {
		t.Fatalf("result %+v", result)
	}
}

func TestClaimKeepsUnsupportedPlacementsApart(t *testing.T) {
	docker := scan.Placement{Kind: scan.KindUnknown}
	procs := []scan.Proc{
		{PID: 11, PPID: 1, Comm: scan.CommListener, Argv: []string{"/actions-runner/bin/Runner.Listener", "run"}, Placement: docker},
		{PID: 22, PPID: 1, Comm: scan.CommListener, Argv: []string{"/actions-runner/bin/Runner.Listener", "run"}, Placement: docker},
	}
	refs := Claim(procs, nil)
	if len(refs) != 2 || refs[0].Key == refs[1].Key {
		t.Fatalf("refs %+v", refs)
	}
	if refs[0].Name != "/actions-runner" || refs[0].Target != "" {
		t.Fatalf("name %q target %q", refs[0].Name, refs[0].Target)
	}
}

func TestClaimSkipsNamesWithControlCharacters(t *testing.T) {
	procs := []scan.Proc{
		{PID: 11, PPID: 1, Comm: scan.CommListener, Argv: []string{"/opt/\x1b]0;x\a/bin/Runner.Listener", "run"}, Placement: scan.Placement{Kind: scan.KindUnknown}},
		{PID: 22, PPID: 1, Comm: scan.CommListener, Argv: []string{"/opt/actions-runner/bin/Runner.Listener", "run"}, Placement: scan.Placement{Kind: scan.KindIncus, Container: "runner-acme-1", Unit: "evil\x1b[2J.service"}},
	}
	units := map[string][]target.Unit{"incus:runner-acme-2": {{Name: "actions.runner.\x1b[31m.service", Active: "failed"}}}
	if refs := Claim(procs, units); len(refs) != 0 {
		t.Fatalf("refs %+v", refs)
	}
}
