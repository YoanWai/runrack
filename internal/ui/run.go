package ui

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/YoanWai/runrack/internal/state"
)

const runKeyPrefix = "run:"

func runKey(res state.Result) string { return runKeyPrefix + res.ID }

func findRun(snap state.Snapshot, key string) (state.Result, bool) {
	for _, res := range snap.Recent {
		if runKey(res) == key {
			return res, true
		}
	}
	return state.Result{}, false
}

var eventWords = map[string]string{
	"push":              "push to",
	"workflow_dispatch": "manual on",
	"schedule":          "scheduled on",
	"workflow_run":      "after a run on",
	"merge_group":       "merge queue",
	"release":           "release",
}

// trigger says why the run exists: the PR and its title, or the event, the branch and the commit subject.
func trigger(job *state.Job, g Glyphs) string {
	if job.PR > 0 || strings.HasPrefix(job.Event, "pull_request") {
		pr := "PR"
		if job.PR > 0 {
			pr = fmt.Sprintf("PR #%d", job.PR)
		}
		if job.Title != "" {
			return pr + " " + job.Title
		}
		return strings.TrimSpace(pr + " " + job.Branch)
	}
	words, ok := eventWords[job.Event]
	if !ok {
		words = job.Event
	}
	text := strings.TrimSpace(words + " " + job.Branch)
	if job.Title != "" {
		text += " " + g.Dot + " " + job.Title
	}
	return text
}

// whyParts are the second line of a run: trigger, who started it and its run number.
func whyParts(job *state.Job, g Glyphs) []string {
	parts := []string{trigger(job, g)}
	if job.Actor != "" {
		parts = append(parts, "by "+job.Actor)
	}
	if job.RunNumber > 0 {
		parts = append(parts, runNumber(job))
	}
	return parts
}

func runWhy(v View, job *state.Job) string {
	t := v.Theme
	parts := whyParts(job, v.Glyphs)
	line := t.Text.Render(parts[0])
	for _, part := range parts[1:] {
		line += t.Dim.Render(" " + v.Glyphs.Dot + " " + part)
	}
	return line
}

func runNumber(job *state.Job) string {
	if job.Attempt > 1 {
		return fmt.Sprintf("#%d attempt %d", job.RunNumber, job.Attempt)
	}
	return fmt.Sprintf("#%d", job.RunNumber)
}

func workflowJob(v View, job *state.Job) string {
	t := v.Theme
	return t.Text.Render(job.Workflow) + t.Dim.Render(" "+v.Glyphs.Sep+" ") + t.Text.Render(job.Name)
}

// stepText is the current step with its place in the job, like "3/9 Run npm ci".
func stepText(v View, job *state.Job) string {
	t := v.Theme
	current := job.CurrentStep()
	if current == "" {
		return ""
	}
	text := t.Busy.Render(v.Glyphs.Step) + " "
	if done, total := job.StepProgress(); done > 0 && total > 0 {
		text += t.Dim.Render(fmt.Sprintf("%d/%d", done, total)) + " "
	}
	return text + t.Text.Render(shortRefs(current))
}

var fullSHA = regexp.MustCompile(`\b([0-9a-f]{7})[0-9a-f]{33}\b`)

// shortRefs turns actions pinned to a full commit, like checkout@3d3c42e5…, into the 7 characters people read.
func shortRefs(s string) string { return fullSHA.ReplaceAllString(s, "$1") }

func failedStep(job *state.Job) string {
	for _, step := range job.Steps {
		if step.Conclusion == state.ConclusionFailure {
			return shortRefs(step.Name)
		}
	}
	return ""
}

func resultBadge(v View, res state.Result) string {
	glyph, style := conclusionGlyph(v, res.Conclusion)
	return style.Render(glyph+" "+strings.ToUpper(res.Conclusion)) + "  " + v.Theme.Text.Render(clock(time.Duration(res.Duration)))
}

// RunDetail is the full picture of one finished run.
func RunDetail(v View, snap state.Snapshot, key string, now time.Time) string {
	t, g := v.Theme, v.Glyphs
	res, ok := findRun(snap, key)
	if !ok {
		return Board(v, snap, now)
	}
	right := t.Dim.Render("esc back   ") + t.Text.Render(now.In(v.Loc).Format("15:04:05")) + " "
	title := " " + t.Title.Render("runrack") + t.Dim.Render(" "+g.Sep+" ") + t.Text.Render(res.Repo) + t.Dim.Render(" "+g.Sep+" ") + workflowJob(v, &res.Job)
	lines := []string{spread(title, right, v.Width, g.More)}
	finished := t.Dim.Render("finished ") + t.Text.Render(res.FinishedAt.In(v.Loc).Format("Mon 2 Jan 15:04")) +
		t.Dim.Render(" ("+age(now.Sub(res.FinishedAt))+" ago)")
	if res.Runner != "" {
		finished += t.Dim.Render(" on ") + t.Text.Render(res.Runner)
	}
	lines = append(lines, jobBox(v, &res.Job, resultBadge(v, res), finished)...)
	lines = append(lines, stepsTitle(v, &res.Job))
	steps := stepLines(v, &res.Job, now, v.Width-14)
	if len(steps) == 0 {
		steps = []string{" " + t.Dim.Render("No steps were read for this run.")}
	}
	lines = append(lines, steps...)
	if v.Height > 0 && len(lines) > v.Height {
		lines = lines[:v.Height]
	}
	for i, line := range lines {
		lines[i] = fit(line, v.Width, g.More)
	}
	return strings.Join(lines, "\n")
}

func stepsTitle(v View, job *state.Job) string {
	title := " " + v.Theme.Title.Render("STEPS")
	if done, total := job.StepProgress(); total > 0 {
		title += v.Theme.Dim.Render(fmt.Sprintf("  %d of %d", done, total))
	}
	return title
}
