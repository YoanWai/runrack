package github

import (
	"context"
	"errors"
	"fmt"
	"path"
	"slices"
	"sync"

	"github.com/YoanWai/runrack/internal/provider"
	"github.com/YoanWai/runrack/internal/scan"
	"github.com/YoanWai/runrack/internal/state"
	"github.com/YoanWai/runrack/internal/target"
)

const (
	runnerFileMax  = 64 << 10
	workerHeadMax  = 2 << 20
	runnerLogLimit = 20
)

type Provider struct {
	mu       sync.Mutex
	metadata map[string]state.Job
}

func New() *Provider { return &Provider{metadata: map[string]state.Job{}} }

func (p *Provider) Name() string { return providerName }

func (p *Provider) Claim(procs []scan.Proc, units map[string][]target.Unit) []provider.RunnerRef {
	return Claim(procs, units)
}

func (p *Provider) Inspect(ctx context.Context, t target.Target, ref provider.RunnerRef) (provider.RunnerInfo, error) {
	root := ref.Root
	if root == "" {
		dir, err := t.WorkingDirectory(ctx, ref.Placement.Unit)
		if err != nil {
			return provider.RunnerInfo{}, err
		}
		root = dir
	}
	if err := target.CheckPath(root); err != nil {
		return provider.RunnerInfo{}, err
	}
	data, err := t.Head(ctx, path.Join(root, ".runner"), runnerFileMax)
	if err != nil {
		return provider.RunnerInfo{}, err
	}
	file, err := ParseRunnerFile(data)
	if err != nil {
		return provider.RunnerInfo{}, err
	}
	scope, _, err := Scope(file.GitHubURL)
	if err != nil {
		return provider.RunnerInfo{}, err
	}
	info := provider.RunnerInfo{Root: root, GitHubURL: file.GitHubURL}
	info.Name, info.Scope = file.AgentName, scope
	facts, err := p.runnerLogFacts(ctx, t, root)
	info.DiagErr = err
	info.Version = facts.Version
	info.Labels = facts.AllLabels()
	return info, nil
}

func (p *Provider) runnerLogFacts(ctx context.Context, t target.Target, root string) (runnerLogFacts, error) {
	diag := path.Join(root, "_diag")
	names, err := t.List(ctx, diag)
	if err != nil {
		return runnerLogFacts{}, err
	}
	var logs []string
	for _, name := range names {
		if isRunnerLog(name) {
			logs = append(logs, name)
		}
	}
	slices.Sort(logs)
	var lines []string
	for _, name := range logs[max(0, len(logs)-runnerLogLimit):] {
		found, err := t.Grep(ctx, path.Join(diag, name), patternVersion, patternLabels, patternArch)
		if err != nil {
			return runnerLogFacts{}, err
		}
		lines = append(lines, found...)
	}
	return ParseRunnerLog(lines), nil
}

func (p *Provider) WorkerLogs(ctx context.Context, t target.Target, ref provider.RunnerRef) ([]provider.LogFile, error) {
	if ref.Root == "" {
		return nil, errors.New("runner root unknown")
	}
	names, err := t.List(ctx, path.Join(ref.Root, "_diag"))
	if err != nil {
		return nil, err
	}
	var logs []provider.LogFile
	for _, name := range names {
		if started, ok := WorkerLogTime(name); ok {
			logs = append(logs, provider.LogFile{Name: name, Started: started})
		}
	}
	slices.SortFunc(logs, func(a, b provider.LogFile) int { return a.Started.Compare(b.Started) })
	return logs, nil
}

func (p *Provider) ReadJob(ctx context.Context, t target.Target, ref provider.RunnerRef, log provider.LogFile) (state.Result, error) {
	logPath := path.Join(ref.Root, "_diag", log.Name)
	job, err := p.jobMetadata(ctx, t, logPath)
	if err != nil {
		return state.Result{}, err
	}
	lines, err := t.Grep(ctx, logPath, patternStep, patternResult)
	if err != nil {
		return state.Result{}, err
	}
	steps := ParseSteps(lines)
	job.Steps = steps.Steps
	job.StartedAt = log.Started
	result := state.Result{Job: job, Conclusion: steps.Conclusion, FinishedAt: steps.FinishedAt}
	if result.Conclusion != "" {
		result.Duration = state.Duration(steps.FinishedAt.Sub(log.Started))
	}
	return result, nil
}

func (p *Provider) jobMetadata(ctx context.Context, t target.Target, logPath string) (state.Job, error) {
	cacheKey := t.ID() + "|" + logPath
	p.mu.Lock()
	job, ok := p.metadata[cacheKey]
	p.mu.Unlock()
	if ok {
		return job, nil
	}
	head, err := t.Head(ctx, logPath, workerHeadMax)
	if err != nil {
		return state.Job{}, err
	}
	job, err = ParseJobMessage(head)
	if err != nil {
		return state.Job{}, fmt.Errorf("%s: %w", path.Base(logPath), err)
	}
	p.mu.Lock()
	p.metadata[cacheKey] = job
	p.mu.Unlock()
	return job, nil
}
