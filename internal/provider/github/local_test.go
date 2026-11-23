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
	if file.AgentName != "zenbook-acme-1" || file.AgentID != 234 || file.GitHubURL != "https://github.com/acme" {
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
		RunID: 45229190085, RunNumber: 16, Attempt: 1, PR: 14, Source: state.SourceDiag,
	}
	gotJSON, _ := json.Marshal(job)
	wantJSON, _ := json.Marshal(want)
	if !bytes.Equal(gotJSON, wantJSON) {
		t.Fatalf("got  %s\nwant %s", gotJSON, wantJSON)
	}
	for _, leaked := range []string{"***", "isSecret", "AccessToken", "repository_owner_id", "actor_id", "CacheServerUrl", "billingOwnerId"} {
		if bytes.Contains(gotJSON, []byte(leaked)) {
			t.Errorf("parsed job carries %q", leaked)
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
			got := ParseSteps(grepFixture(t, "github/"+tc.log, patternStep, patternResult))
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
