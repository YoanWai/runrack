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
	"unicode"

	"github.com/YoanWai/runrack/internal/state"
)

const (
	patternStep   = "StepsRunner] Processing step: DisplayName="
	patternResult = "Job result after all job steps finish: "
	// patternStepResult ends both "Update job result with" and "No need for updating job result with" in StepsRunner.cs.
	patternStepResult = "with current step result '"
	patternVersion    = "Listener] Version: "
	patternLabels     = "Adding option 'labels': "
	patternArch       = "Runner is built for Linux ("
)

type runnerFile struct {
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
	file.AgentName, file.GitHubURL = clean(file.AgentName), clean(file.GitHubURL)
	return file, nil
}

// clean drops control characters, so text a workflow author controls cannot drive the terminal.
func clean(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}

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
			facts.Version = clean(strings.TrimSpace(rest))
		}
		if _, rest, ok := strings.Cut(line, patternArch); ok {
			arch, _, _ := strings.Cut(rest, ")")
			facts.Arch = clean(arch)
		}
		if _, rest, ok := strings.Cut(line, patternLabels); ok {
			facts.Labels = nil
			for label := range strings.SplitSeq(strings.Trim(strings.TrimSpace(rest), "'"), ",") {
				if label = clean(strings.TrimSpace(label)); label != "" {
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
	JobDisplayName string     `json:"jobDisplayName"`
	JobID          string     `json:"jobId"`
	Steps          []struct{} `json:"steps"`
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
	body := head[marker+start:]
	var msg jobMessage
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&msg); err != nil {
		salvaged, ok := salvageJobMessage(body)
		if !ok {
			return state.Job{}, fmt.Errorf("worker log: Job message: %w", err)
		}
		msg = salvaged
	}
	gh := map[string]string{}
	title := ""
	for _, kv := range msg.ContextData.GitHub.D {
		var value string
		if githubContextKeys[kv.K] && json.Unmarshal(kv.V, &value) == nil {
			gh[kv.K] = clean(value)
		}
		if kv.K == "event" {
			title = eventTitle(kv.V)
		}
	}
	job := state.Job{
		ID:         clean(msg.JobID),
		Name:       clean(msg.JobDisplayName),
		Repo:       gh["repository"],
		Workflow:   gh["workflow"],
		Event:      gh["event_name"],
		Actor:      gh["actor"],
		SHA:        gh["sha"],
		BaseBranch: gh["base_ref"],
		Title:      title,
		StepCount:  len(msg.Steps),
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

// eventTitle is what GitHub shows as the run title: the PR title, the pushed commit's subject, or the title of the run that triggered this one.
func eventTitle(event json.RawMessage) string {
	for _, path := range [][]string{{"pull_request", "title"}, {"head_commit", "message"}, {"workflow_run", "display_title"}} {
		if text := contextString(event, path...); text != "" {
			subject, _, _ := strings.Cut(text, "\n")
			return clean(strings.TrimSpace(subject))
		}
	}
	return ""
}

// contextString walks a runner context dictionary ({"t":2,"d":[{"k":…,"v":…}]}) down keys to a string.
func contextString(raw json.RawMessage, keys ...string) string {
	for _, key := range keys {
		var dict struct {
			D []struct {
				K string          `json:"k"`
				V json.RawMessage `json:"v"`
			} `json:"d"`
		}
		if json.Unmarshal(raw, &dict) != nil {
			return ""
		}
		raw = nil
		for _, kv := range dict.D {
			if kv.K == key {
				raw = kv.V
				break
			}
		}
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return ""
	}
	return text
}

// salvageJobMessage decodes only the kept fields: the runner masks secrets after writing, and a mask can eat a closing quote.
func salvageJobMessage(body []byte) (jobMessage, bool) {
	var msg jobMessage
	found := decodeAfter(body, `"jobDisplayName":`, &msg.JobDisplayName)
	decodeAfter(body, `"jobId":`, &msg.JobID)
	decodeAfter(body, `"steps":`, &msg.Steps)
	if i := bytes.Index(body, []byte(`"contextData":`)); i >= 0 {
		decodeAfter(body[i:], `"github":`, &msg.ContextData.GitHub)
	}
	return msg, found && len(msg.ContextData.GitHub.D) > 0
}

func decodeAfter(body []byte, key string, out any) bool {
	i := bytes.Index(body, []byte(key))
	return i >= 0 && json.NewDecoder(bytes.NewReader(body[i+len(key):])).Decode(out) == nil
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

func ParseSteps(lines []string) stepsAndResult {
	var out stepsAndResult
	for _, line := range lines {
		at, message, ok := lineTime(line)
		if !ok {
			continue
		}
		if _, name, found := strings.Cut(message, "Processing step: DisplayName="); found {
			out.closeLastStep(at)
			out.Steps = append(out.Steps, state.Step{Name: clean(strings.Trim(name, "'")), Status: state.StepInProgress, StartedAt: at})
			continue
		}
		if _, raw, found := strings.Cut(message, patternStepResult); found && len(out.Steps) > 0 {
			raw, _, _ = strings.Cut(raw, "'")
			out.Steps[len(out.Steps)-1].Conclusion = Conclusion(clean(raw))
			continue
		}
		if _, raw, found := strings.Cut(message, patternResult); found {
			out.closeLastStep(at)
			out.Conclusion = Conclusion(clean(strings.TrimSpace(raw)))
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
	case "Skipped":
		return "skipped"
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
