package provider

import (
	"context"
	"time"

	"github.com/YoanWai/runrack/internal/scan"
	"github.com/YoanWai/runrack/internal/state"
	"github.com/YoanWai/runrack/internal/target"
)

// RunnerRef is a claimed runner: where it lives and which processes belong to it.
type RunnerRef struct {
	state.Presence
	Root        string
	ListenerPID int
}

type RunnerInfo struct {
	state.Info
	Root      string
	GitHubURL string
	DiagErr   error
}

type LogFile struct {
	Name    string
	Started time.Time
}

type Provider interface {
	Name() string
	// Claim is pure: units are keyed by target ID.
	Claim(procs []scan.Proc, units map[string][]target.Unit) []RunnerRef
	Inspect(ctx context.Context, t target.Target, ref RunnerRef) (RunnerInfo, error)
	WorkerLogs(ctx context.Context, t target.Target, ref RunnerRef) ([]LogFile, error)
	// ReadJob returns the job a Worker log describes. Conclusion is empty while it runs.
	ReadJob(ctx context.Context, t target.Target, ref RunnerRef, log LogFile) (state.Result, error)
}

func TargetID(p scan.Placement) string {
	switch {
	case p.Kind == scan.KindHost:
		return target.HostID
	case p.Kind == scan.KindIncus && p.Project == "":
		return target.IncusID(p.Container)
	}
	return ""
}
