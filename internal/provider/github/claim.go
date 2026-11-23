package github

import (
	"cmp"
	"path"
	"strconv"
	"strings"

	"github.com/YoanWai/runrack/internal/provider"
	"github.com/YoanWai/runrack/internal/scan"
	"github.com/YoanWai/runrack/internal/target"
)

const providerName = "github"

// Claim finds GitHub runners: Listeners started with `run` whose parent is not a runner process,
// plus runner units with no Listener (offline).
func Claim(procs []scan.Proc, units map[string][]target.Unit) []provider.RunnerRef {
	commByPID := make(map[int]string, len(procs))
	for _, p := range procs {
		commByPID[p.PID] = p.Comm
	}
	var refs []provider.RunnerRef
	claimedUnits := map[string]bool{}
	for _, p := range procs {
		if p.Comm != scan.CommListener || len(p.Argv) < 2 || p.Argv[1] != "run" || scan.IsRunnerComm(commByPID[p.PPID]) {
			continue
		}
		root, ok := runnerRoot(p.Argv[0])
		if !ok {
			continue
		}
		targetID := provider.TargetID(p.Placement)
		ref := provider.RunnerRef{Root: root, ListenerPID: p.PID}
		ref.Key = refKey(targetID, p.Placement.Unit, root)
		if targetID == "" {
			ref.Key += "|" + strconv.Itoa(p.PID) // runners in Docker or a user session share roots like /actions-runner
		}
		ref.Name = cmp.Or(p.Placement.Unit, root)
		ref.Provider = providerName
		ref.Target = targetID
		ref.Placement = p.Placement
		ref.Service = "active"
		ref.Online = true
		for _, w := range procs {
			if w.Comm == scan.CommWorker && w.PPID == p.PID {
				ref.WorkerPID, ref.WorkerStart = w.PID, w.Started
			}
		}
		claimedUnits[targetID+"|"+p.Placement.Unit] = true
		refs = append(refs, ref)
	}
	for targetID, list := range units {
		for _, unit := range list {
			if claimedUnits[targetID+"|"+unit.Name] {
				continue
			}
			ref := provider.RunnerRef{}
			ref.Key = refKey(targetID, unit.Name, "")
			ref.Name = unit.Name
			ref.Provider = providerName
			ref.Target = targetID
			ref.Placement = placementOf(targetID, unit.Name)
			ref.Service = unit.Active
			refs = append(refs, ref)
		}
	}
	return refs
}

// runnerRoot is the parent of the directory holding Runner.Listener (…/bin/Runner.Listener).
func runnerRoot(argv0 string) (string, bool) {
	if !path.IsAbs(argv0) || path.Clean(argv0) != argv0 {
		return "", false
	}
	root := path.Dir(path.Dir(argv0))
	return root, root != "/"
}

func refKey(targetID, unit, root string) string {
	if unit != "" {
		return providerName + "|" + targetID + "|" + unit
	}
	return providerName + "|" + targetID + "|" + root
}

func placementOf(targetID, unit string) scan.Placement {
	if container, ok := strings.CutPrefix(targetID, "incus:"); ok {
		return scan.Placement{Kind: scan.KindIncus, Container: container, Unit: unit}
	}
	return scan.Placement{Kind: scan.KindHost, Unit: unit}
}
