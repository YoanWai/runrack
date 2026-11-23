package scan

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	CommListener = "Runner.Listener"
	CommWorker   = "Runner.Worker"
)

type PlacementKind string

const (
	KindHost    PlacementKind = "host"
	KindIncus   PlacementKind = "incus"
	KindUnknown PlacementKind = "unknown"
)

type Placement struct {
	Kind      PlacementKind `json:"kind"`
	Project   string        `json:"project,omitempty"`
	Container string        `json:"container,omitempty"`
	Unit      string        `json:"unit,omitempty"`
}

type Proc struct {
	PID        int
	PPID       int
	Comm       string
	StartTicks uint64
	Started    time.Time
	Argv       []string
	Placement  Placement
}

// shortcut: USER_HZ fixed at 100 (true on Linux amd64/arm64), Go cannot call sysconf without cgo.
const userHZ = 100

func startTime(bootTime time.Time, ticks uint64) time.Time {
	return bootTime.Add(time.Duration(ticks) * time.Second / userHZ)
}

func IsRunnerComm(comm string) bool {
	return comm == CommListener || comm == CommWorker
}

// Scan walks procRoot once. Only runner processes get their cmdline and cgroup read.
func Scan(procRoot string) ([]Proc, error) {
	bootTime, err := BootTime(procRoot)
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(procRoot)
	if err != nil {
		return nil, err
	}
	var procs []Proc
	for _, entry := range entries {
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}
		dir := filepath.Join(procRoot, entry.Name())
		stat, err := os.ReadFile(filepath.Join(dir, "stat"))
		if err != nil {
			continue // process exited between ReadDir and ReadFile
		}
		proc, err := ParseStat(stat)
		if err != nil || !IsRunnerComm(proc.Comm) {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join(dir, "cmdline"))
		if err != nil {
			continue
		}
		proc.Argv = ParseCmdline(cmdline)
		cgroup, err := os.ReadFile(filepath.Join(dir, "cgroup"))
		if err != nil {
			continue
		}
		proc.Placement = ParseCgroup(cgroup)
		proc.Started = startTime(bootTime, proc.StartTicks)
		procs = append(procs, proc)
	}
	return procs, nil
}

// ParseStat reads pid, comm, ppid and starttime (field 22) from /proc/<pid>/stat.
func ParseStat(stat []byte) (Proc, error) {
	open := bytes.IndexByte(stat, '(')
	closeParen := bytes.LastIndexByte(stat, ')')
	if open < 0 || closeParen < open {
		return Proc{}, errors.New("stat: no comm")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(stat[:open])))
	if err != nil {
		return Proc{}, fmt.Errorf("stat pid: %w", err)
	}
	rest := strings.Fields(string(stat[closeParen+1:]))
	const starttimeIndex = 19 // field 22 minus pid, comm and state
	if len(rest) <= starttimeIndex {
		return Proc{}, errors.New("stat: too few fields")
	}
	ppid, err := strconv.Atoi(rest[1])
	if err != nil {
		return Proc{}, fmt.Errorf("stat ppid: %w", err)
	}
	start, err := strconv.ParseUint(rest[starttimeIndex], 10, 64)
	if err != nil {
		return Proc{}, fmt.Errorf("stat starttime: %w", err)
	}
	return Proc{PID: pid, PPID: ppid, Comm: string(stat[open+1 : closeParen]), StartTicks: start}, nil
}

func ParseCmdline(cmdline []byte) []string {
	trimmed := strings.TrimRight(string(cmdline), "\x00")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\x00")
}

// ParseCgroup maps a cgroup v2 path to where the process runs.
func ParseCgroup(cgroup []byte) Placement {
	for line := range strings.SplitSeq(string(cgroup), "\n") {
		path, ok := strings.CutPrefix(line, "0::")
		if ok {
			return placementFromPath(path)
		}
	}
	return Placement{Kind: KindUnknown}
}

func placementFromPath(path string) Placement {
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) == 2 && parts[0] == "system.slice" && strings.HasSuffix(parts[1], ".service") {
		return Placement{Kind: KindHost, Unit: parts[1]}
	}
	if len(parts) == 3 && parts[1] == "system.slice" && strings.HasSuffix(parts[2], ".service") {
		if container, ok := strings.CutPrefix(parts[0], "lxc.payload."); ok {
			placement := Placement{Kind: KindIncus, Container: container, Unit: parts[2]}
			// Incus names cannot contain "_", so the first one separates a non-default project.
			if project, name, found := strings.Cut(container, "_"); found {
				placement.Project, placement.Container = project, name
			}
			return placement
		}
	}
	return Placement{Kind: KindUnknown}
}

func BootTime(procRoot string) (time.Time, error) {
	stat, err := os.ReadFile(filepath.Join(procRoot, "stat"))
	if err != nil {
		return time.Time{}, err
	}
	for line := range strings.SplitSeq(string(stat), "\n") {
		if value, ok := strings.CutPrefix(line, "btime "); ok {
			seconds, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
			if err != nil {
				return time.Time{}, fmt.Errorf("btime: %w", err)
			}
			return time.Unix(seconds, 0), nil
		}
	}
	return time.Time{}, errors.New("btime not found in stat")
}

func CgroupV2(sysfsCgroup string) bool {
	_, err := os.Stat(filepath.Join(sysfsCgroup, "cgroup.controllers"))
	return err == nil
}
