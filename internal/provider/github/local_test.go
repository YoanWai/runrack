package github

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/YoanWai/runrack/internal/state"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("../../../testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// grepFixture does what `grep -a -h -F -e …` returns for a fixture file.
func grepFixture(t *testing.T, name string, patterns ...string) []string {
	var lines []string
	scanner := bufio.NewScanner(bytes.NewReader(fixture(t, name)))
	scanner.Buffer(nil, 1<<20)
	for scanner.Scan() {
		if slices.ContainsFunc(patterns, func(p string) bool { return strings.Contains(scanner.Text(), p) }) {
			lines = append(lines, scanner.Text())
		}
	}
	return lines
}

func TestParseRunnerFileStripsBOM(t *testing.T) {
	data := fixture(t, "github/runner.json")
	if !bytes.HasPrefix(data, []byte{0xEF, 0xBB, 0xBF}) {
		t.Fatal("fixture lost its BOM")
	}
	file, err := ParseRunnerFile(data)
	if err != nil {
		t.Fatal(err)
	}
	if file.AgentName != "zenbook-acme-1" || file.GitHubURL != "https://github.com/acme" {
		t.Fatalf("got %+v", file)
	}
}

func TestParseRunnerFileFromJITConfig(t *testing.T) {
	file, err := ParseRunnerFile(fixture(t, "github/runner-jit.json"))
	if err != nil {
		t.Fatal(err)
	}
	if file.AgentName != "zenbook-acme-1" || file.GitHubURL != "https://github.com/acme" {
		t.Fatalf("got %+v", file)
	}
}

func TestScope(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/acme":                "acme",
		"https://github.com/acme/widgets":        "acme/widgets",
		"https://github.com/enterprises/initech": "enterprises/initech",
	} {
		if got, host, err := Scope(in); err != nil || got != want || host != "github.com" {
			t.Errorf("Scope(%q) = %q %q %v", in, got, host, err)
		}
	}
}

func TestParseRunnerLog(t *testing.T) {
	lines := grepFixture(t, "github/Runner_20261123-173249-utc.log", patternVersion, patternLabels, patternArch)
	facts := ParseRunnerLog(lines)
	if facts.Version != "2.338.2" || facts.Arch != "X64" {
		t.Fatalf("got %+v", facts)
	}
	if got := strings.Join(facts.AllLabels(), ","); got != "self-hosted,Linux,X64,homelab" {
		t.Fatalf("labels = %s", got)
	}
}

func TestParseJobMessageKeepsOnlyWhitelist(t *testing.T) {
	head := fixture(t, "github/Worker_20261123-173811-utc.log")
	if !bytes.Contains(head, []byte(`"system.github.token"`)) || !bytes.Contains(head, []byte(`"isSecret": true`)) {
		t.Fatal("fixture must carry the masked token entry this test guards against")
	}
	job, err := ParseJobMessage(head)
	if err != nil {
		t.Fatal(err)
	}
	want := state.Job{
		ID: "377760b2-12b0-5774-8f85-8ee535fa5f51", Name: "e2e", Repo: "acme/widgets", Workflow: "CI",
		Branch: "ci/homelab-runner", BaseBranch: "main", Event: "pull_request", Actor: "octocat",
		SHA: "c0a9cbb51e2d4f8a9b7c6d5e4f3a2b1c0d9e8f7a", URL: "https://github.com/acme/widgets/actions/runs/45229190085",
		RunID: 45229190085, RunNumber: 16, Attempt: 1, PR: 14, Title: "fix: homelab runner labels", StepCount: 9, Source: state.SourceDiag,
	}
	gotJSON, _ := json.Marshal(job)
	wantJSON, _ := json.Marshal(want)
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("got  %s\nwant %s", gotJSON, wantJSON)
	}
	for _, leaked := range []string{"***", "isSecret", "AccessToken", "repository_owner_id", "actor_id", "CacheServerUrl", "billingOwnerId", "Long description"} {
		if bytes.Contains(gotJSON, []byte(leaked)) {
			t.Errorf("parsed job carries %q", leaked)
		}
	}
}

func TestParseJobMessageTitleFromPushedCommit(t *testing.T) {
	job, err := ParseJobMessage(fixture(t, "github/Worker_20261123-173750-utc.log"))
	if err != nil {
		t.Fatal(err)
	}
	if job.Event != "push" || job.Title != "chore: scan history for secrets" || job.StepCount != 3 {
		t.Fatalf("got event %q title %q steps %d", job.Event, job.Title, job.StepCount)
	}
}

func TestEventTitle(t *testing.T) {
	dict := func(pairs ...string) string {
		var d []string
		for i := 0; i < len(pairs); i += 2 {
			d = append(d, `{"k":"`+pairs[i]+`","v":`+pairs[i+1]+`}`)
		}
		return `{"t":2,"d":[` + strings.Join(d, ",") + `]}`
	}
	cases := map[string]string{
		dict("workflow_run", dict("display_title", `"Preview for #84"`)):       "Preview for #84",
		dict("inputs", "null", "ref", `"refs/heads/main"`):                     "",
		dict("head_commit", dict("message", `"  fix: tidy\u001b[2J\n\nbody"`)): "fix: tidy[2J",
		dict("pull_request", `"not a dictionary"`):                             "",
		`"not a dictionary"`: "",
	}
	for event, want := range cases {
		if got := eventTitle(json.RawMessage(event)); got != want {
			t.Errorf("eventTitle(%s) = %q, want %q", event, got, want)
		}
	}
}

func TestBranchAndPR(t *testing.T) {
	cases := []struct {
		event, ref, refName, headRef, wantBranch string
		wantPR                                   int
	}{
		{"pull_request", "refs/pull/14/merge", "14/merge", "ci/homelab-runner", "ci/homelab-runner", 14},
		{"pull_request_target", "refs/heads/main", "main", "feat/x", "feat/x", 0},
		{"push", "refs/heads/main", "main", "", "main", 0},
		{"workflow_dispatch", "refs/tags/v1.0.0", "v1.0.0", "", "v1.0.0", 0},
	}
	for _, tc := range cases {
		branch, pr := branchAndPR(tc.event, tc.ref, tc.refName, tc.headRef)
		if branch != tc.wantBranch || pr != tc.wantPR {
			t.Errorf("%s %s: got %s #%d", tc.event, tc.ref, branch, pr)
		}
	}
}

func TestParseSteps(t *testing.T) {
	cases := []struct {
		log, conclusion string
		steps           int
		finished        string
		took            time.Duration
	}{
		{"Worker_20261123-173811-utc.log", state.ConclusionFailure, 11, "2026-11-23T17:43:59Z", 5*time.Minute + 48*time.Second},
		{"Worker_20261123-174638-utc.log", state.ConclusionCancelled, 11, "2026-11-23T17:54:44Z", 8*time.Minute + 6*time.Second},
		{"Worker_20261123-173750-utc.log", state.ConclusionSuccess, 4, "2026-11-23T17:38:03Z", 13 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.log, func(t *testing.T) {
			got := ParseSteps(grepFixture(t, "github/"+tc.log, patternStep, patternStepResult, patternResult))
			if got.Conclusion != tc.conclusion || len(got.Steps) != tc.steps || got.FinishedAt.Format(time.RFC3339) != tc.finished {
				t.Fatalf("got %s, %d steps, finished %s", got.Conclusion, len(got.Steps), got.FinishedAt.Format(time.RFC3339))
			}
			started, _ := WorkerLogTime(tc.log)
			if took := got.FinishedAt.Sub(started); took != tc.took {
				t.Fatalf("took %s, want %s", took, tc.took)
			}
			for _, step := range got.Steps {
				if step.Status != state.StepCompleted {
					t.Fatalf("step %q still %s after the result line", step.Name, step.Status)
				}
			}
			if last := got.Steps[len(got.Steps)-1]; !strings.HasPrefix(last.Name, "Post Run ") {
				t.Fatalf("last step %q, want a Post Run step", last.Name)
			}
		})
	}
}

func TestParseStepsKeepsEachStepResult(t *testing.T) {
	got := ParseSteps(grepFixture(t, "github/Worker_20261123-173811-utc.log", patternStep, patternStepResult, patternResult))
	want := map[string]string{
		"Install browsers":               state.ConclusionSuccess,
		"Editor end to end":              state.ConclusionFailure,
		"Website":                        "skipped",
		"Post Run actions/setup-node@v7": "skipped",
	}
	for _, step := range got.Steps {
		if conclusion, ok := want[step.Name]; ok && step.Conclusion != conclusion {
			t.Errorf("%s: %q, want %q", step.Name, step.Conclusion, conclusion)
		}
	}
}

func TestParseStepsWhileRunning(t *testing.T) {
	lines := grepFixture(t, "github/Worker_20261123-173811-utc.log", patternStep)[:7]
	got := ParseSteps(lines)
	if got.Conclusion != "" {
		t.Fatalf("conclusion %q before the result line", got.Conclusion)
	}
	job := state.Job{Steps: got.Steps}
	if job.CurrentStep() != "Editor end to end" {
		t.Fatalf("current step %q", job.CurrentStep())
	}
	if d := time.Duration(got.Steps[5].Duration); d != 23*time.Second {
		t.Fatalf("Install browsers took %s, want 23s", d)
	}
}

func TestConclusionKeepsUnknownWords(t *testing.T) {
	if Conclusion("Abandoned") != "Abandoned" {
		t.Fatal("unknown results must stay raw")
	}
}

func TestWorkerLogTime(t *testing.T) {
	got, ok := WorkerLogTime("Worker_20261123-174638-utc.log")
	if !ok || !got.Equal(time.Date(2026, 11, 23, 17, 46, 38, 0, time.UTC)) {
		t.Fatalf("got %v %v", got, ok)
	}
	if _, ok := WorkerLogTime("Runner_20261123-174638-utc.log"); ok {
		t.Fatal("Runner logs are not Worker logs")
	}
}

func TestTerminalControlsAreStripped(t *testing.T) {
	const evil = "e2e\x1b]52;c;cHduZWQ=\x07\x1b[2J\x1b]0;owned\x07\u009b31m\r"
	quoted, _ := json.Marshal(evil)
	head := bytes.Replace(fixture(t, "github/Worker_20261123-173811-utc.log"), []byte(`"jobDisplayName": "e2e"`), append([]byte(`"jobDisplayName": `), quoted...), 1)
	job, err := ParseJobMessage(head)
	if err != nil {
		t.Fatal(err)
	}
	steps := ParseSteps([]string{"[2026-11-23 17:38:15Z INFO StepsRunner] Processing step: DisplayName='Run \x1b[2Jcheckout'"})
	facts := ParseRunnerLog([]string{"[2026-11-23 17:32:49Z INFO Listener] Version: 2.338.2\x1b]0;x\x07"})
	for _, got := range []string{job.Name, steps.Steps[0].Name, facts.Version} {
		if strings.ContainsFunc(got, unicode.IsControl) {
			t.Fatalf("control characters survived: %q", got)
		}
	}
	if !strings.HasPrefix(job.Name, "e2e]52;c;") {
		t.Fatalf("visible text must stay: %q", job.Name)
	}
}

func FuzzParseJobMessage(f *testing.F) {
	f.Add([]byte("Job message:\n{\"jobDisplayName\":\"x\",\"contextData\":{\"github\":{\"d\":[{\"k\":\"repository\",\"v\":\"a/b\"}]}}}"))
	f.Add([]byte("Job message: {"))
	f.Fuzz(func(t *testing.T, head []byte) {
		job, err := ParseJobMessage(head)
		if err != nil {
			return
		}
		for _, text := range []string{job.ID, job.Name, job.Repo, job.Workflow, job.Branch, job.Actor, job.URL} {
			if strings.ContainsFunc(text, unicode.IsControl) {
				t.Fatalf("control characters in %q", text)
			}
		}
	})
}

func TestParseJobMessageSurvivesAMaskThatAteAQuote(t *testing.T) {
	head := fixture(t, "github/Worker_20261123-173811-utc.log")
	broken := bytes.Replace(head, []byte(`"jobId":`), []byte(`"lit": "curl -H \"Authorization: ***" -X POST",`+"\n"+`  "jobId":`), 1)
	message := broken[bytes.Index(broken, []byte("Job message:")):]
	if json.NewDecoder(bytes.NewReader(message[bytes.IndexByte(message, '{'):])).Decode(new(any)) == nil {
		t.Fatal("fixture edit must break the JSON")
	}
	want, err := ParseJobMessage(head)
	if err != nil {
		t.Fatal(err)
	}
	got, err := ParseJobMessage(broken)
	if err != nil {
		t.Fatal(err)
	}
	gotJSON, _ := json.Marshal(got)
	wantJSON, _ := json.Marshal(want)
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("got  %s\nwant %s", gotJSON, wantJSON)
	}
}
