package scan

import (
	"testing"
	"time"
)

func TestParseCgroup(t *testing.T) {
	cases := []struct {
		name, cgroup string
		want         Placement
	}{
		{"incus default project", "0::/lxc.payload.runner-acme-1/system.slice/actions.runner.acme.zenbook-acme-1.service\n",
			Placement{Kind: KindIncus, Container: "runner-acme-1", Unit: "actions.runner.acme.zenbook-acme-1.service"}},
		{"incus other project", "0::/lxc.payload.lab_runner-globex-1/system.slice/actions.runner.globex.zenbook-globex-1.service\n",
			Placement{Kind: KindIncus, Project: "lab", Container: "runner-globex-1", Unit: "actions.runner.globex.zenbook-globex-1.service"}},
		{"host unit", "0::/system.slice/actions.runner.acme.zenbook-host.service\n",
			Placement{Kind: KindHost, Unit: "actions.runner.acme.zenbook-host.service"}},
		{"user session", "0::/user.slice/user-1000.slice/session-3.scope\n", Placement{Kind: KindUnknown}},
		{"cgroup v1", "12:pids:/system.slice/x.service\n1:name=systemd:/system.slice/x.service\n", Placement{Kind: KindUnknown}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseCgroup([]byte(tc.cgroup)); got != tc.want {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestParseStatCommWithSpacesAndParens(t *testing.T) {
	proc, err := ParseStat([]byte("4242 (odd (name) x) S 7 7 7 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 316247 0 0"))
	if err != nil {
		t.Fatal(err)
	}
	if proc.PID != 4242 || proc.Comm != "odd (name) x" || proc.PPID != 7 || proc.StartTicks != 316247 {
		t.Fatalf("got %+v", proc)
	}
}

func TestScanReadsOnlyRunnerProcesses(t *testing.T) {
	procs, err := Scan("../../testdata/proc")
	if err != nil {
		t.Fatal(err)
	}
	byPID := map[int]Proc{}
	for _, p := range procs {
		byPID[p.PID] = p
	}
	if _, ok := byPID[50000]; ok {
		t.Fatal("bash decoy whose cmdline mentions Runner.Worker must not match: comm decides")
	}
	worker, ok := byPID[92911]
	if !ok {
		t.Fatal("worker not found")
	}
	if worker.PPID != 28424 || worker.Argv[1] != "spawnclient" {
		t.Fatalf("worker = %+v", worker)
	}
	wantStart := time.Unix(1795452835, 0).Add(3162470 * time.Millisecond)
	if !worker.Started.Equal(wantStart) {
		t.Fatalf("started %v, want %v", worker.Started.UTC(), wantStart.UTC())
	}
	if len(procs) != 6 {
		t.Fatalf("want 6 runner processes, got %d", len(procs))
	}
}

func FuzzParseStat(f *testing.F) {
	f.Add([]byte("4242 (odd (name) x) S 7 7 7 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 316247 0 0"))
	f.Add([]byte("1 () S"))
	f.Fuzz(func(t *testing.T, stat []byte) {
		if proc, err := ParseStat(stat); err == nil && proc.PID <= 0 {
			t.Fatalf("parsed nonsense %+v", proc)
		}
	})
}

func FuzzParseCgroup(f *testing.F) {
	f.Add([]byte("0::/lxc.payload.lab_runner-globex-1/system.slice/actions.runner.globex.zenbook-globex-1.service\n"))
	f.Add([]byte("0::/system.slice/docker-0123abcd.scope\n"))
	f.Fuzz(func(t *testing.T, cgroup []byte) {
		p := ParseCgroup(cgroup)
		if p.Kind != KindHost && p.Kind != KindIncus && p.Kind != KindUnknown {
			t.Fatalf("kind %q", p.Kind)
		}
		if p.Kind == KindIncus && p.Container == "" {
			t.Fatalf("incus placement without a container: %+v", p)
		}
	})
}
