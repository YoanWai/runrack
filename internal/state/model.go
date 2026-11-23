package state

import (
	"strconv"
	"time"

	"github.com/YoanWai/runrack/internal/scan"
)

const Schema = "runrack.snapshot/v1"

const (
	StatusBusy    = "busy"
	StatusIdle    = "idle"
	StatusOffline = "offline"

	ConclusionSuccess   = "success"
	ConclusionFailure   = "failure"
	ConclusionCancelled = "cancelled"
	ConclusionUnknown   = "unknown"

	StepCompleted  = "completed"
	StepInProgress = "in_progress"
	StepPending    = "pending"

	SourceDiag = "diag"
	SourceAPI  = "api"
	SourceNone = "none"
)

// Duration marshals as seconds so --json stays language neutral.
type Duration time.Duration

func (d Duration) MarshalJSON() ([]byte, error) {
	return strconv.AppendFloat(nil, time.Duration(d).Round(time.Millisecond).Seconds(), 'f', -1, 64), nil
}

type Snapshot struct {
	Schema  string         `json:"schema"`
	Host    HostStats      `json:"host"`
	Runners []Runner       `json:"runners"`
	Recent  []Result       `json:"recent"`
	Sources []SourceHealth `json:"sources"`
	API     bool           `json:"api"`
	TakenAt time.Time      `json:"takenAt"`
}

type HostStats struct {
	Hostname string   `json:"hostname"`
	Load1    float64  `json:"load1"`
	Load5    float64  `json:"load5"`
	Load15   float64  `json:"load15"`
	CPUs     int      `json:"cpus"`
	CPUPct   float64  `json:"cpuPct"`
	MemUsed  int64    `json:"memUsed"`
	MemTotal int64    `json:"memTotal"`
	Uptime   Duration `json:"uptime"`
}

type SourceHealth struct {
	Name      string    `json:"name"`
	OK        bool      `json:"ok"`
	LastError string    `json:"lastError,omitempty"`
	LastOK    time.Time `json:"lastOK,omitzero"`
}

type Runner struct {
	Key       string          `json:"-"`
	ID        string          `json:"id"`
	Provider  string          `json:"provider"`
	Name      string          `json:"name"`
	Scope     string          `json:"scope"`
	Placement scan.Placement  `json:"placement"`
	Service   string          `json:"service"`
	Status    string          `json:"status"`
	Version   string          `json:"version,omitempty"`
	Labels    []string        `json:"labels,omitempty"`
	Note      string          `json:"note,omitempty"`
	Job       *Job            `json:"job,omitempty"`
	Last      *Result         `json:"last,omitempty"`
	API       *APIRunner      `json:"api,omitempty"`
	Container *ContainerStats `json:"container,omitempty"`
}

type APIRunner struct {
	ID     int64    `json:"id"`
	Name   string   `json:"name"`
	Status string   `json:"status"`
	Busy   bool     `json:"busy"`
	Labels []string `json:"labels"`
}

type ContainerStats struct {
	CPUPct   float64 `json:"cpuPct"`
	MemBytes int64   `json:"memBytes"`
	Procs    int     `json:"procs"`
}

type Job struct {
	ID         string    `json:"id,omitempty"`
	Name       string    `json:"name,omitempty"`
	Repo       string    `json:"repo,omitempty"`
	Workflow   string    `json:"workflow,omitempty"`
	Branch     string    `json:"branch,omitempty"`
	BaseBranch string    `json:"baseBranch,omitempty"`
	Event      string    `json:"event,omitempty"`
	Actor      string    `json:"actor,omitempty"`
	SHA        string    `json:"sha,omitempty"`
	Title      string    `json:"title,omitempty"`
	URL        string    `json:"url,omitempty"`
	RunID      int64     `json:"runId,omitempty"`
	RunNumber  int       `json:"runNumber,omitempty"`
	Attempt    int       `json:"attempt,omitempty"`
	PR         int       `json:"pr,omitempty"`
	StartedAt  time.Time `json:"startedAt,omitzero"`
	Steps      []Step    `json:"steps,omitempty"`
	Expected   Duration  `json:"expected,omitempty"`
	Source     string    `json:"source"`
}

type Step struct {
	Name       string    `json:"name"`
	Status     string    `json:"status"`
	Conclusion string    `json:"conclusion,omitempty"`
	StartedAt  time.Time `json:"startedAt,omitzero"`
	Duration   Duration  `json:"duration,omitempty"`
}

type Result struct {
	RunnerID string `json:"runnerId"`
	Runner   string `json:"runner"`
	Job
	Conclusion string    `json:"conclusion"`
	FinishedAt time.Time `json:"finishedAt,omitzero"`
	Duration   Duration  `json:"duration"`
}

// CurrentStep is the step the job is in now, or "Set up job" before the first step line.
func (j *Job) CurrentStep() string {
	for i := len(j.Steps) - 1; i >= 0; i-- {
		if j.Steps[i].Status == StepInProgress {
			return j.Steps[i].Name
		}
	}
	if len(j.Steps) == 0 {
		return "Set up job"
	}
	return ""
}

func (j *Job) HasMetadata() bool { return j.Repo != "" }
