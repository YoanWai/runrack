package main

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/YoanWai/runrack/internal/scan"
	"github.com/YoanWai/runrack/internal/target"
)

func doctor(opts options, stdout io.Writer) int {
	report := func(ok bool, what, detail string) {
		mark := "ok  "
		if !ok {
			mark = "FAIL"
		}
		fmt.Fprintf(stdout, "%s  %-34s %s\n", mark, what, detail)
	}
	note := func(what, detail string) { fmt.Fprintf(stdout, "--    %-34s %s\n", what, detail) }

	if _, err := os.Stat("/proc/stat"); err != nil {
		report(false, "proc", "no /proc: runrack discovers runners on Linux only")
		return 1
	}
	procs, err := scan.Scan("/proc")
	report(err == nil, "proc", fmt.Sprintf("%d runner processes (comm Runner.Listener or Runner.Worker)", len(procs)))
	report(scan.CgroupV2("/sys/fs/cgroup"), "cgroup v2", "placement of each runner comes from its cgroup path")
	if !target.IncusAvailable() {
		note("incus", "not installed, only host runners are watched")
	}
	_, source := apiClient(opts)
	switch {
	case opts.noAPI:
		note("github token", "--no-api: local data only")
	case source == "":
		note("github token", "none: local data only. Set RUNRACK_GITHUB_TOKEN or run `gh auth login` for API data")
	default:
		report(true, "github token", "from "+source+" (the token itself is never shown)")
	}

	snap, err := collectOnce(opts)
	if err != nil {
		report(false, "collect", err.Error())
		return 1
	}
	for _, s := range snap.Sources {
		detail := "answered"
		if !s.OK {
			detail = s.LastError
		}
		report(s.OK, s.Name, detail)
	}
	if len(snap.Runners) == 0 {
		note("runners", "none found: no Runner.Listener process and no actions.runner.* unit")
	}
	for _, r := range snap.Runners {
		where := string(r.Placement.Kind)
		if r.Placement.Container != "" {
			where += " " + r.Placement.Container
		}
		detail := fmt.Sprintf("%s, %s, version %s", where, r.Status, orDash(r.Version))
		ok := r.Note == ""
		if !ok {
			detail += ": " + r.Note + accessHint(r.Placement.Kind)
		}
		report(ok, "runner "+r.Name, detail)
	}
	return 0
}

func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "-"
	}
	return s
}

func accessHint(kind scan.PlacementKind) string {
	if kind == scan.KindIncus {
		return " (needs membership in incus-admin)"
	}
	return " (the runner's user owns _diag: add yourself to its group or run as that user)"
}
