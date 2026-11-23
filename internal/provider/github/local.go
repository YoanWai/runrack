package github

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/YoanWai/runrack/internal/state"
)

const (
	patternStep    = "StepsRunner] Processing step: DisplayName="
	patternResult  = "Job result after all job steps finish: "
	patternVersion = "Listener] Version: "
	patternLabels  = "Adding option 'labels': "
	patternArch    = "Runner is built for Linux ("
)

type runnerFile struct {
	AgentID   int64  `json:"agentId"`
	AgentName string `json:"agentName"`
	GitHubURL string `json:"gitHubUrl"`
}

func ParseRunnerFile(data []byte) (runnerFile, error) {
	var file runnerFile
	if err := json.Unmarshal(bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF")), &file); err != nil {
		return runnerFile{}, fmt.Errorf(".runner: %w", err)
	}
	if file.AgentName == "" {
		return runnerFile{}, errors.New(".runner: no agentName")
	}
	return file, nil
}

// Scope turns gitHubUrl into org, owner/repo or enterprises/x. host is the server.
func Scope(gitHubURL string) (scope, host string, err error) {
	u, err := url.Parse(gitHubURL)
	if err != nil {
		return "", "", err
	}
	return strings.Trim(u.Path, "/"), u.Host, nil
}

var logLine = regexp.MustCompile(`^\[(\d{4}-\d\d-\d\d \d\d:\d\d:\d\d)Z [A-Z]+ [^\]]*\] (.*)$`)

func lineTime(line string) (time.Time, string, bool) {
	m := logLine.FindStringSubmatch(line)
	if m == nil {
		return time.Time{}, "", false
	}
	t, err := time.Parse(time.DateTime, m[1])
	if err != nil {
		return time.Time{}, "", false
	}
	return t.UTC(), m[2], true
}

type runnerLogFacts struct {
	Version string
	Arch    string
	Labels  []string
}

func ParseRunnerLog(lines []string) runnerLogFacts {
	var facts runnerLogFacts
	for _, line := range lines {
		if _, rest, ok := strings.Cut(line, patternVersion); ok {
			facts.Version = strings.TrimSpace(rest)
		}
		if _, rest, ok := strings.Cut(line, patternArch); ok {
			facts.Arch, _, _ = strings.Cut(rest, ")")
		}
		if _, rest, ok := strings.Cut(line, patternLabels); ok {
			facts.Labels = nil
			for label := range strings.SplitSeq(strings.Trim(strings.TrimSpace(rest), "'"), ",") {
				if label = strings.TrimSpace(label); label != "" {
					facts.Labels = append(facts.Labels, label)
				}
			}
		}
	}
	return facts
}

func (f runnerLogFacts) AllLabels() []string {
	labels := []string{"self-hosted", "Linux"}
	if f.Arch != "" {
		labels = append(labels, f.Arch)
	}
	return append(labels, f.Labels...)
}

var githubContextKeys = map[string]bool{
	"repository": true, "workflow": true, "run_id": true, "run_number": true, "run_attempt": true,
	"ref": true, "ref_name": true, "head_ref": true, "base_ref": true, "event_name": true,
	"actor": true, "sha": true, "server_url": true,
}

// jobMessage holds only the whitelisted fields. Everything else in the message,
// secrets included, is skipped by the decoder and never kept.
type jobMessage struct {
	JobDisplayName string `json:"jobDisplayName"`
	JobID          string `json:"jobId"`
	ContextData    struct {
		GitHub struct {
			D []struct {
				K string          `json:"k"`
				V json.RawMessage `json:"v"`
			} `json:"d"`
		} `json:"github"`
	} `json:"contextData"`
}

func ParseJobMessage(head []byte) (state.Job, error) {
	marker := bytes.Index(head, []byte("Job message:"))
	if marker < 0 {
		return state.Job{}, errors.New("worker log: no Job message yet")
	}
	start := bytes.IndexByte(head[marker:], '{')
	if start < 0 {
		return state.Job{}, errors.New("worker log: Job message has no body")
	}
	var msg jobMessage
	if err := json.NewDecoder(bytes.NewReader(head[marker+start:])).Decode(&msg); err != nil {
		return state.Job{}, fmt.Errorf("worker log: Job message: %w", err)
	}
	gh := map[string]string{}
	for _, kv := range msg.ContextData.GitHub.D {
		var value string
		if githubContextKeys[kv.K] && json.Unmarshal(kv.V, &value) == nil {
			gh[kv.K] = value
		}
	}
	job := state.Job{
		ID:         msg.JobID,
		Name:       msg.JobDisplayName,
		Repo:       gh["repository"],
		Workflow:   gh["workflow"],
		Event:      gh["event_name"],
		Actor:      gh["actor"],
		SHA:        gh["sha"],
		BaseBranch: gh["base_ref"],
		Source:     state.SourceDiag,
	}
	job.RunID, _ = strconv.ParseInt(gh["run_id"], 10, 64)
	job.RunNumber, _ = strconv.Atoi(gh["run_number"])
	job.Attempt, _ = strconv.Atoi(gh["run_attempt"])
	job.Branch, job.PR = branchAndPR(job.Event, gh["ref"], gh["ref_name"], gh["head_ref"])
	if gh["server_url"] != "" && job.Repo != "" && job.RunID != 0 {
		job.URL = fmt.Sprintf("%s/%s/actions/runs/%d", gh["server_url"], job.Repo, job.RunID)
	}
	return job, nil
}

var pullRef = regexp.MustCompile(`^refs/pull/(\d+)/`)

func branchAndPR(event, ref, refName, headRef string) (string, int) {
	pr := 0
	if m := pullRef.FindStringSubmatch(ref); m != nil {
		pr, _ = strconv.Atoi(m[1])
	}
	if strings.HasPrefix(event, "pull_request") && headRef != "" {
		return headRef, pr
	}
	return refName, pr
}

type stepsAndResult struct {
	Steps      []state.Step
	Conclusion string
	FinishedAt time.Time
}

// ParseSteps reads the grep output of one Worker log: step starts and the final result line.
func ParseSteps(lines []string) stepsAndResult {
	var out stepsAndResult
	for _, line := range lines {
		at, message, ok := lineTime(line)
		if !ok {
			continue
		}
		if _, name, found := strings.Cut(message, "Processing step: DisplayName="); found {
			out.closeLastStep(at)
			out.Steps = append(out.Steps, state.Step{Name: strings.Trim(name, "'"), Status: state.StepInProgress, StartedAt: at})
			continue
		}
		if _, raw, found := strings.Cut(message, patternResult); found {
			out.closeLastStep(at)
			out.Conclusion = Conclusion(strings.TrimSpace(raw))
			out.FinishedAt = at
		}
	}
	return out
}

func (s *stepsAndResult) closeLastStep(at time.Time) {
	if n := len(s.Steps); n > 0 && s.Steps[n-1].Status == state.StepInProgress {
		s.Steps[n-1].Status = state.StepCompleted
		s.Steps[n-1].Duration = state.Duration(at.Sub(s.Steps[n-1].StartedAt))
	}
}

func Conclusion(raw string) string {
	switch raw {
	case "Succeeded":
		return state.ConclusionSuccess
	case "Failed":
		return state.ConclusionFailure
	case "Canceled":
		return state.ConclusionCancelled
	}
	return raw
}

var workerLogName = regexp.MustCompile(`^Worker_(\d{8}-\d{6})-utc\.log$`)

func WorkerLogTime(name string) (time.Time, bool) {
	m := workerLogName.FindStringSubmatch(name)
	if m == nil {
		return time.Time{}, false
	}
	t, err := time.Parse("20060102-150405", m[1])
	return t.UTC(), err == nil
}

func isRunnerLog(name string) bool {
	return strings.HasPrefix(name, "Runner_") && strings.HasSuffix(name, ".log")
}
