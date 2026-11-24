package ui

import (
	"testing"

	"github.com/YoanWai/runrack/internal/state"
)

func TestRunsThatShareACauseShareOneHeading(t *testing.T) {
	pr := state.Job{Repo: "acme/widgets", Event: "pull_request", PR: 14, Title: "fix labels", Actor: "octocat"}
	first, retry, push := pr, pr, state.Job{Repo: "acme/widgets", Event: "push", Branch: "main", Actor: "octocat"}
	first.RunNumber, retry.RunNumber = 16, 15
	results := []state.Result{{Job: first}, {Job: retry}, {Job: push}, {}}

	var got []int
	for i := range results {
		got = append(got, runHeight(results, i, true))
	}
	want := []int{2, 1, 2, 1}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("heights on a wide board = %v, want %v", got, want)
		}
	}
	if runHeight(results, 0, false) != 1 {
		t.Fatal("a narrow board keeps one line per run")
	}
}
