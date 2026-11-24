package collect

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/YoanWai/runrack/internal/state"
)

func TestHistoryPath(t *testing.T) {
	env := func(vars map[string]string) func(string) string { return func(k string) string { return vars[k] } }
	cases := []struct {
		vars map[string]string
		want string
	}{
		{map[string]string{"XDG_STATE_HOME": "/state", "HOME": "/home/octocat"}, "/state/runrack/history.json"},
		{map[string]string{"XDG_STATE_HOME": "relative", "HOME": "/home/octocat"}, "/home/octocat/.local/state/runrack/history.json"},
		{map[string]string{}, ""},
	}
	for _, tc := range cases {
		if got := HistoryPath(env(tc.vars)); got != tc.want {
			t.Errorf("HistoryPath(%v) = %q, want %q", tc.vars, got, tc.want)
		}
	}
}

func TestHistoryRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runrack", "history.json")
	if got, err := loadHistory(path); err != nil || got != nil {
		t.Fatalf("missing file: %v %v", got, err)
	}
	finished := time.Date(2026, 11, 23, 17, 43, 59, 0, time.UTC)
	want := []state.Result{{
		Runner: "zenbook-acme-1", Scope: "acme", Conclusion: state.ConclusionFailure, FinishedAt: finished, Duration: state.Duration(348 * time.Second),
		Job: state.Job{ID: "j1", Repo: "acme/widgets", Workflow: "CI", Name: "e2e", Title: "fix: homelab runner labels", StepCount: 9, RunNumber: 16, Source: state.SourceDiag},
	}}
	if err := saveHistory(path, want); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(filepath.Dir(path)); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("history dir must be private: %v %v", info.Mode(), err)
	}
	got, err := loadHistory(path)
	if err != nil || len(got) != 1 || got[0].Title != want[0].Title || got[0].Duration != want[0].Duration || !got[0].FinishedAt.Equal(finished) || got[0].Scope != "acme" {
		t.Fatalf("got %+v %v", got, err)
	}
	if err := os.WriteFile(path, []byte(`{"schema":"other"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadHistory(path); err == nil {
		t.Fatal("a file with another schema must be refused")
	}
}
