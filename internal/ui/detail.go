package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/YoanWai/runrack/internal/scan"
	"github.com/YoanWai/runrack/internal/state"
)

func Detail(v View, snap state.Snapshot, key string, now time.Time) string {
	t, g := v.Theme, v.Glyphs
	r, ok := findRunner(snap, key)
	if !ok {
		return Board(v, snap, now)
	}
	right := t.Dim.Render("esc back   ") + t.Text.Render(now.In(v.Loc).Format("15:04:05")) + " "
	lines := []string{spread(" "+t.Title.Render("runrack")+t.Dim.Render(" "+g.Sep+" ")+t.Text.Render(r.Name), right, v.Width, g.More)}
	if r.Job != nil {
		lines = append(lines, jobBox(v, r.Job, busyBadge(v, now.Sub(r.Job.StartedAt)), busyStatus(v, r, now))...)
	}
	left := max(v.Width/2-2, 30)
	steps := stepLines(v, r.Job, now, left-12)
	runner := runnerBlock(v, r)
	stepsHead := " " + t.Title.Render("STEPS")
	if r.Job != nil {
		stepsHead = stepsTitle(v, r.Job)
	}
	lines = append(lines, pad(stepsHead, left)+"  "+t.Title.Render("RUNNER"))
	if r.Job == nil {
		steps = []string{" " + t.Dim.Render("idle")}
		if r.Status == state.StatusOffline {
			steps = []string{" " + t.Offline.Render(g.Offline+" offline  "+r.Service)}
		}
	}
	for i := range max(len(steps), len(runner)) {
		var l, rt string
		if i < len(steps) {
			l = steps[i]
		}
		if i < len(runner) {
			rt = runner[i]
		}
		lines = append(lines, pad(fit(l, left, g.More), left)+"  "+rt)
	}
	lines = append(lines, "", " "+t.Title.Render("RECENT ON THIS RUNNER"))
	var mine []state.Result
	for _, res := range snap.Recent {
		if res.RunnerID == r.ID {
			mine = append(mine, res)
		}
	}
	if len(mine) == 0 {
		lines = append(lines, " "+t.Dim.Render("Nothing finished in the last 24 hours."))
	}
	lines = append(lines, recentLines(v, mine, v.Width >= narrowRecent, shortener(snap))...)
	if v.Height > 0 && len(lines) > v.Height {
		lines = lines[:v.Height]
	}
	for i, line := range lines {
		lines[i] = fit(line, v.Width, g.More)
	}
	return strings.Join(lines, "\n")
}

func findRunner(snap state.Snapshot, key string) (state.Runner, bool) {
	for _, r := range snap.Runners {
		if r.Key == key {
			return r, true
		}
	}
	return state.Runner{}, false
}

func jobBox(v View, job *state.Job, badge, status string) []string {
	t, g := v.Theme, v.Glyphs
	var body []string
	if !job.HasMetadata() {
		body = append(body, status)
	} else {
		meta := []string{}
		if job.RunNumber > 0 {
			meta = append(meta, "run "+runNumber(job))
		}
		meta = append(meta, job.Event)
		if job.Actor != "" {
			meta = append(meta, "by "+job.Actor)
		}
		body = append(body,
			t.Title.Render(job.Repo),
			pad(workflowJob(v, job), 30)+"  "+t.Dim.Render(strings.Join(meta, "  ")),
			t.Text.Render(trigger(job, g)),
			t.Dim.Render(strings.TrimSpace(branchLine(job, g)+"   "+shortSHA(job.SHA))),
			status,
		)
		if job.URL != "" {
			body = append(body, t.Dim.Render(job.URL))
		}
	}
	w := v.Width
	top := t.Border.Render(g.TopLeft+g.Horizontal) + " " + badge + " " + t.Border.Render(strings.Repeat(g.Horizontal, max(w-5-width(badge)-1, 0))+g.TopRight)
	lines := []string{top}
	for _, line := range body {
		lines = append(lines, t.Border.Render(g.Vertical)+" "+pad(fit(line, w-4, g.More), w-4)+" "+t.Border.Render(g.Vertical))
	}
	return append(lines, t.Border.Render(g.BottomLeft+strings.Repeat(g.Horizontal, w-2)+g.BottomRight))
}

// busyStatus is the progress line of a running job, or what it waits for before the job message arrives.
func busyStatus(v View, r state.Runner, now time.Time) string {
	t, g, job := v.Theme, v.Glyphs, r.Job
	if !job.HasMetadata() {
		note := "starting"
		if r.Note != "" {
			note = r.Note
		}
		return t.Dim.Render(note)
	}
	elapsed := now.Sub(job.StartedAt)
	progress := t.Text.Render(clock(elapsed))
	if job.Expected > 0 {
		expected := time.Duration(job.Expected)
		progress = bar(float64(elapsed)/float64(expected), 30, g, t.Busy, t.Dim) + " " + t.Text.Render(clock(elapsed)) + t.Dim.Render(" / ~"+clock(expected))
	}
	if step := stepText(v, job); step != "" {
		progress += "   " + step
	}
	return progress
}

func shortSHA(sha string) string { return sha[:min(7, len(sha))] }

func stepLines(v View, job *state.Job, now time.Time, maxName int) []string {
	if job == nil {
		return nil
	}
	t, g := v.Theme, v.Glyphs
	if len(job.Steps) == 0 {
		return []string{" " + t.Busy.Render(g.Step) + " " + t.Text.Render("Set up job")}
	}
	nameW := 0
	for _, step := range job.Steps {
		nameW = max(nameW, width(shortRefs(step.Name)))
	}
	nameW = min(nameW, maxName)
	var lines []string
	for _, step := range job.Steps {
		took := time.Duration(step.Duration)
		var glyph string
		switch {
		case step.Status == state.StepInProgress:
			glyph, took = t.Busy.Render(g.Step), now.Sub(step.StartedAt)
		case step.Status == state.StepPending:
			glyph = t.Dim.Render(g.Pending)
		case step.Conclusion == "failure":
			glyph = t.Failure.Render(g.Failure)
		case step.Conclusion == "skipped":
			glyph = t.Dim.Render(g.Pending)
		default:
			glyph = t.Success.Render(g.Done)
		}
		durationText := ""
		if step.Status != state.StepPending {
			durationText = clock(took)
		}
		lines = append(lines, " "+glyph+" "+pad(fit(t.Text.Render(shortRefs(step.Name)), nameW, g.More), nameW+2)+t.Dim.Render(durationText))
	}
	return lines
}

func runnerBlock(v View, r state.Runner) []string {
	t := v.Theme
	row := func(label, value string) string {
		return t.Dim.Render(fmt.Sprintf("%-10s ", label)) + t.Text.Render(value)
	}
	var lines []string
	if r.Placement.Unit != "" {
		lines = append(lines, row("unit", strings.TrimSuffix(r.Placement.Unit, ".service")))
	}
	switch r.Placement.Kind {
	case scan.KindIncus:
		lines = append(lines, row("where", "incus "+r.Placement.Container))
	case scan.KindHost:
		lines = append(lines, row("where", "host"))
	default:
		lines = append(lines, row("where", "placement not supported, busy and idle only"))
	}
	if r.Version != "" {
		lines = append(lines, row("version", r.Version))
	}
	if len(r.Labels) > 0 {
		lines = append(lines, row("labels", strings.Join(r.Labels, ", ")))
	}
	if c := r.Container; c != nil {
		lines = append(lines, row("container", fmt.Sprintf("cpu %.0f%%  mem %s  procs %d", c.CPUPct, gib(c.MemBytes), c.Procs)))
	}
	if a := r.API; a != nil {
		state := a.Status
		if a.Busy {
			state += ", busy"
		}
		lines = append(lines, row("github", state))
	}
	if r.Note != "" {
		lines = append(lines, row("note", r.Note))
	}
	return lines
}

func Help(v View) string {
	t, g := v.Theme, v.Glyphs
	move := g.Keys + " / hjkl"
	if g.Keys == "hjkl" {
		move = "hjkl"
	}
	rows := []string{
		t.Title.Render("Keys"),
		pad(move, 15) + "move selection",
		"enter          open detail",
		"esc            back",
		"r              refresh now, retry API",
		"?              this help",
		"q              quit",
		"",
		t.Title.Render("Glyphs"),
		t.Busy.Render(g.Busy) + " busy   " + t.Idle.Render(g.Idle) + " idle   " + t.Offline.Render(g.Offline) + " offline",
		t.Success.Render(g.Success) + " success   " + t.Failure.Render(g.Failure) + " failure   " + t.Cancelled.Render(g.Cancelled) + " cancelled",
		t.Busy.Render(g.Step) + " current step   " + t.Dim.Render(g.Pending) + " pending step",
	}
	inner := 0
	for _, row := range rows {
		inner = max(inner, width(row))
	}
	box := []string{t.Border.Render(g.TopLeft + strings.Repeat(g.Horizontal, inner+2) + g.TopRight)}
	for _, row := range rows {
		box = append(box, t.Border.Render(g.Vertical)+" "+pad(row, inner)+" "+t.Border.Render(g.Vertical))
	}
	box = append(box, t.Border.Render(g.BottomLeft+strings.Repeat(g.Horizontal, inner+2)+g.BottomRight))
	top := max((v.Height-len(box))/2, 0)
	left := strings.Repeat(" ", max((v.Width-inner-4)/2, 0))
	lines := make([]string, top)
	for _, line := range box {
		lines = append(lines, fit(left+line, v.Width, g.More))
	}
	return strings.Join(lines, "\n")
}
