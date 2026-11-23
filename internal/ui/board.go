package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/YoanWai/runrack/internal/scan"
	"github.com/YoanWai/runrack/internal/state"
)

const (
	columnWidth  = 58
	maxColumns   = 4
	minRecent    = 3
	compactBelow = 80
)

// View holds everything a render needs besides the snapshot.
type View struct {
	Width, Height int // Height 0 means unbounded, for --once
	Theme         Theme
	Glyphs        Glyphs
	Loc           *time.Location
	Selected      string
	ShowSelection bool
}

type group struct {
	scope   string
	index   int
	runners []state.Runner
}

func groups(snap state.Snapshot) []group {
	var out []group
	for _, r := range snap.Runners {
		scope := r.Scope
		if scope == "" {
			scope = "identifying"
		}
		if len(out) == 0 || out[len(out)-1].scope != scope {
			out = append(out, group{scope: scope, index: len(out)})
		}
		out[len(out)-1].runners = append(out[len(out)-1].runners, r)
	}
	return out
}

// columns never leaves a column empty, so two groups on a wide TV get half the screen each.
func columns(w, groupCount int) int {
	return max(min(w/columnWidth, maxColumns, groupCount), 1)
}

// layout packs groups into columns, each group into the currently shortest column.
func layout(gs []group, cols int, compact bool) [][]group {
	packed := make([][]group, cols)
	heights := make([]int, cols)
	for _, g := range gs {
		target := 0
		for c := range cols {
			if heights[c] < heights[target] {
				target = c
			}
		}
		packed[target] = append(packed[target], g)
		heights[target] += groupHeight(g, compact)
	}
	return packed
}

func groupHeight(g group, compact bool) int {
	h := 2
	for _, r := range g.runners {
		h += runnerHeight(r, compact)
	}
	return h
}

func runnerHeight(r state.Runner, compact bool) int {
	switch {
	case r.Status != state.StatusBusy || r.Job == nil:
		return 1
	case !r.Job.HasMetadata() || compact:
		return 2
	}
	return 4
}

// Order lists runner keys as the board shows them, column by column, for keyboard movement.
func Order(snap state.Snapshot, w int) [][]string {
	gs := groups(snap)
	cols := columns(w, len(gs))
	if w < compactBelow {
		cols = 1
	}
	var order [][]string
	for _, col := range layout(gs, cols, false) {
		var keys []string
		for _, g := range col {
			for _, r := range g.runners {
				keys = append(keys, r.Key)
			}
		}
		if len(keys) > 0 {
			order = append(order, keys)
		}
	}
	return order
}

func Board(v View, snap state.Snapshot, now time.Time) string {
	lines := []string{header(v, snap, now), ""}
	board := boardLines(v, snap, now, false)
	fixed := 9 // header, blank, blank, recent title, blank, host title, 2 host lines, footer
	if v.Height > 0 && len(board) > v.Height-fixed-minRecent {
		board = boardLines(v, snap, now, true)
		board = board[:min(len(board), max(v.Height-fixed-minRecent, 0))]
	}
	lines = append(lines, board...)
	recentRows := len(snap.Recent)
	if v.Height > 0 {
		recentRows = min(recentRows, max(v.Height-fixed-len(board), minRecent))
	}
	lines = append(lines, "", sectionTitle(v, "RECENT"))
	lines = append(lines, recentLines(v, snap, recentRows)...)
	bottom := append([]string{"", sectionTitle(v, "HOST")}, hostLines(v, snap)...)
	bottom = append(bottom, footer(v, snap, now))
	if v.Height > 0 {
		for len(lines)+len(bottom) < v.Height {
			lines = append(lines, "")
		}
	}
	lines = append(lines, bottom...)
	if v.Height > 0 && len(lines) > v.Height {
		lines = lines[:v.Height]
	}
	for i, line := range lines {
		lines[i] = fit(line, v.Width, v.Glyphs.More)
	}
	return strings.Join(lines, "\n")
}

func header(v View, snap state.Snapshot, now time.Time) string {
	t, g := v.Theme, v.Glyphs
	busy, idle, offline := 0, 0, 0
	for _, r := range snap.Runners {
		switch r.Status {
		case state.StatusBusy:
			busy++
		case state.StatusOffline:
			offline++
		default:
			idle++
		}
	}
	left := " " + t.Title.Render("runrack") + "  " + t.Dim.Render(snap.Host.Hostname)
	counts := t.Busy.Render(fmt.Sprintf("%s %d busy", g.Busy, busy)) + "   " +
		t.Idle.Render(fmt.Sprintf("%s %d idle", g.Idle, idle)) + "   " +
		t.Offline.Render(fmt.Sprintf("%s %d offline", g.Offline, offline))
	right := t.Text.Render(now.In(v.Loc).Format("Mon 2 Jan  15:04:05")) + " "
	if v.Width < 100 {
		right = t.Text.Render(now.In(v.Loc).Format("15:04:05")) + " "
	}
	middleStart := max(width(left)+4, (v.Width-width(counts))/2)
	line := pad(left, middleStart) + counts
	return spread(line, right, v.Width, g.More)
}

func sectionTitle(v View, name string) string {
	rule := strings.Repeat(v.Glyphs.Horizontal, max(v.Width-len(name)-3, 0))
	return " " + v.Theme.Title.Render(name) + " " + v.Theme.Border.Render(rule)
}

func boardLines(v View, snap state.Snapshot, now time.Time, compact bool) []string {
	if snap.TakenAt.IsZero() {
		return []string{" " + v.Theme.Dim.Render("Looking for runners...")}
	}
	if len(snap.Runners) == 0 {
		return []string{" " + v.Theme.Dim.Render("No runners found on this machine. Try `runrack doctor`.")}
	}
	if v.Width < compactBelow {
		var lines []string
		for _, r := range snap.Runners {
			lines = append(lines, " "+listLine(v, r, v.Width-1, now))
		}
		return lines
	}
	gs := groups(snap)
	cols := columns(v.Width, len(gs))
	boxWidth := (v.Width - (cols - 1)) / cols
	var rendered [][]string
	height := 0
	for _, col := range layout(gs, cols, compact) {
		var lines []string
		for _, g := range col {
			lines = append(lines, groupBox(v, g, boxWidth, now, compact)...)
		}
		rendered = append(rendered, lines)
		height = max(height, len(lines))
	}
	out := make([]string, height)
	for row := range height {
		var parts []string
		for _, lines := range rendered {
			cell := ""
			if row < len(lines) {
				cell = lines[row]
			}
			parts = append(parts, pad(cell, boxWidth))
		}
		out[row] = strings.Join(parts, " ")
	}
	return out
}

func groupBox(v View, g group, w int, now time.Time, compact bool) []string {
	t, gl := v.Theme, v.Glyphs
	inner := w - 4
	var body []string
	for _, r := range g.runners {
		body = append(body, runnerLines(v, r, inner, now, compact)...)
	}
	count := fmt.Sprintf("%d runner", len(g.runners))
	if len(g.runners) != 1 {
		count += "s"
	}
	title := fit(g.scope, max(w-8-len(count), 4), gl.More)
	fill := w - 8 - width(title) - len(count)
	if fill < 1 {
		count, fill = "", w-6-width(title)
	}
	top := t.Border.Render(gl.TopLeft+gl.Horizontal) + " " + t.Accents[g.index%len(t.Accents)].Render(title) + " " +
		t.Border.Render(strings.Repeat(gl.Horizontal, max(fill, 0)))
	if count != "" {
		top += " " + t.Dim.Render(count) + " "
	}
	top += t.Border.Render(gl.Horizontal + gl.TopRight)
	lines := []string{top}
	for _, line := range body {
		lines = append(lines, t.Border.Render(gl.Vertical)+" "+pad(fit(line, inner, gl.More), inner)+" "+t.Border.Render(gl.Vertical))
	}
	bottom := t.Border.Render(gl.BottomLeft + strings.Repeat(gl.Horizontal, w-2) + gl.BottomRight)
	return append(lines, bottom)
}

func statusGlyph(v View, r state.Runner) string {
	switch r.Status {
	case state.StatusBusy:
		return v.Theme.Busy.Render(v.Glyphs.Busy)
	case state.StatusOffline:
		return v.Theme.Offline.Render(v.Glyphs.Offline)
	}
	return v.Theme.Idle.Render(v.Glyphs.Idle)
}

func conclusionGlyph(v View, conclusion string) (string, lipgloss.Style) {
	switch conclusion {
	case state.ConclusionSuccess:
		return v.Glyphs.Success, v.Theme.Success
	case state.ConclusionFailure:
		return v.Glyphs.Failure, v.Theme.Failure
	case state.ConclusionCancelled:
		return v.Glyphs.Cancelled, v.Theme.Cancelled
	}
	return v.Glyphs.Unknown, v.Theme.Dim
}

func (v View) selected(r state.Runner) bool { return v.ShowSelection && r.Key == v.Selected }

func nameRow(v View, r state.Runner, right string, w int) string {
	name := v.Theme.Text.Render(r.Name)
	if v.selected(r) {
		name = v.Theme.Text.Reverse(true).Bold(true).Render(r.Name)
	}
	return spread(statusGlyph(v, r)+" "+name, right, w, v.Glyphs.More)
}

func runnerLines(v View, r state.Runner, w int, now time.Time, compact bool) []string {
	t, g := v.Theme, v.Glyphs
	switch {
	case r.Status == state.StatusOffline:
		return []string{nameRow(v, r, t.Offline.Render("offline  "+r.Service), w)}
	case r.Status != state.StatusBusy || r.Job == nil:
		return []string{nameRow(v, r, idleRight(v, r, now), w)}
	}
	job := r.Job
	elapsed := now.Sub(job.StartedAt)
	lines := []string{nameRow(v, r, t.Busy.Render("BUSY  "+clock(elapsed)), w)}
	if !job.HasMetadata() {
		note := "starting"
		if r.Note != "" {
			note = r.Note
		}
		return append(lines, "  "+t.Dim.Render(note))
	}
	workflow := t.Text.Render(job.Workflow) + t.Dim.Render(" "+g.Sep+" ") + t.Text.Render(job.Name)
	step := ""
	if current := job.CurrentStep(); current != "" {
		step = t.Busy.Render(g.Step) + " " + t.Text.Render(current)
	}
	if compact {
		return append(lines, "  "+t.Dim.Render(job.Repo)+"  "+workflow+"  "+step)
	}
	lines = append(lines, "  "+t.Text.Render(job.Repo))
	lines = append(lines, "  "+pad(workflow, 14)+"   "+t.Dim.Render(branchLine(job, g)))
	progress := t.Text.Render(clock(elapsed))
	if job.Expected > 0 {
		cells := min(max(w/3, 8), 24)
		expected := time.Duration(job.Expected)
		progress = bar(float64(elapsed)/float64(expected), cells, g, t.Busy, t.Dim) + " " +
			t.Text.Render(clock(elapsed)) + t.Dim.Render(" / ~"+clock(expected))
	}
	return append(lines, "  "+progress+"   "+step)
}

func branchLine(job *state.Job, g Glyphs) string {
	switch {
	case job.PR > 0:
		return fmt.Sprintf("PR #%d  %s %s %s", job.PR, job.Branch, g.Arrow, job.BaseBranch)
	case job.BaseBranch != "":
		return fmt.Sprintf("%s %s %s", job.Branch, g.Arrow, job.BaseBranch)
	}
	return job.Branch + "  " + job.Event
}

func idleRight(v View, r state.Runner, now time.Time) string {
	right := v.Theme.Idle.Render("idle")
	if r.Last != nil {
		glyph, style := conclusionGlyph(v, r.Last.Conclusion)
		right += "   " + style.Render(glyph) + " " + v.Theme.Dim.Render(pad(fit(r.Last.Name, 12, v.Glyphs.More), 12)+fmt.Sprintf("%4s", age(now.Sub(r.Last.FinishedAt))))
	}
	return right
}

func listLine(v View, r state.Runner, w int, now time.Time) string {
	if r.Status == state.StatusBusy && r.Job != nil && r.Job.HasMetadata() {
		job := r.Job
		right := v.Theme.Busy.Render(clock(now.Sub(job.StartedAt))) + " " + v.Theme.Dim.Render(job.Repo+" "+v.Glyphs.Sep+" "+job.Name)
		return nameRow(v, r, right, w)
	}
	if r.Status == state.StatusBusy {
		return nameRow(v, r, v.Theme.Busy.Render("BUSY "+clock(now.Sub(r.Job.StartedAt))), w)
	}
	if r.Status == state.StatusOffline {
		return nameRow(v, r, v.Theme.Offline.Render("offline "+r.Service), w)
	}
	return nameRow(v, r, idleRight(v, r, now), w)
}

func recentLines(v View, snap state.Snapshot, rows int) []string {
	t, g := v.Theme, v.Glyphs
	if len(snap.Recent) == 0 {
		return []string{" " + t.Dim.Render("No finished jobs in the last 24 hours.")}
	}
	flexible := max(v.Width-1-5-2-12-6-2*5, 32)
	repoW, jobW, branchW := flexible*28/100, flexible*24/100, flexible*24/100
	runnerW := flexible - repoW - jobW - branchW
	var lines []string
	for _, res := range snap.Recent[:min(rows, len(snap.Recent))] {
		glyph, style := conclusionGlyph(v, res.Conclusion)
		cells := []string{
			t.Dim.Render(res.FinishedAt.In(v.Loc).Format("15:04")),
			pad(style.Render(glyph+" "+res.Conclusion), 12),
			pad(t.Text.Render(fit(res.Repo, repoW, g.More)), repoW),
			pad(t.Text.Render(fit(res.Workflow+" "+g.Sep+" "+res.Name, jobW, g.More)), jobW),
			pad(t.Dim.Render(fit(res.Branch, branchW, g.More)), branchW),
			t.Text.Render(fmt.Sprintf("%6s", short(time.Duration(res.Duration)))),
			t.Dim.Render(fit(res.Runner, runnerW, g.More)),
		}
		lines = append(lines, " "+strings.Join(cells, "  "))
	}
	return lines
}

func loadStyle(t Theme, pct float64) lipgloss.Style {
	switch {
	case pct >= 85:
		return t.Failure
	case pct >= 60:
		return t.Busy
	}
	return t.Idle
}

func hostLines(v View, snap state.Snapshot) []string {
	t, g, h := v.Theme, v.Glyphs, snap.Host
	memPct := 0.0
	if h.MemTotal > 0 {
		memPct = 100 * float64(h.MemUsed) / float64(h.MemTotal)
	}
	first := " " + t.Dim.Render("cpu ") + t.Text.Render(fmt.Sprintf("%3.0f%% ", h.CPUPct)) + bar(h.CPUPct/100, 10, g, loadStyle(t, h.CPUPct), t.Dim) +
		"   " + t.Dim.Render("mem ") + t.Text.Render(fmt.Sprintf("%.1f/%.1fG ", float64(h.MemUsed)/(1<<30), float64(h.MemTotal)/(1<<30))) + bar(memPct/100, 10, g, loadStyle(t, memPct), t.Dim) +
		"   " + t.Dim.Render("load ") + t.Text.Render(fmt.Sprintf("%.2f %.2f %.2f", h.Load1, h.Load5, h.Load15)) + t.Dim.Render(fmt.Sprintf(" /%d", h.CPUs)) +
		"   " + t.Dim.Render("up ") + t.Text.Render(uptime(time.Duration(h.Uptime)))
	var parts []string
	seen := map[string]bool{}
	for _, r := range snap.Runners {
		name := "host"
		if r.Placement.Kind == scan.KindIncus {
			name = strings.TrimPrefix(r.Placement.Container, "runner-")
		}
		if seen[name] || r.Container == nil {
			continue
		}
		seen[name] = true
		parts = append(parts, t.Dim.Render(name+" ")+t.Text.Render(fmt.Sprintf("%.0f%% %s", r.Container.CPUPct, gib(r.Container.MemBytes))))
	}
	return []string{first, " " + strings.Join(parts, "   ")}
}

func footer(v View, snap state.Snapshot, now time.Time) string {
	t, g := v.Theme, v.Glyphs
	keys := " " + t.Dim.Render(g.Keys+" select  enter detail  r refresh  ? help  q quit")
	mode := "local"
	if snap.API {
		mode = "local + api"
	}
	stale := now.Sub(snap.TakenAt)
	right := t.Dim.Render(fmt.Sprintf("%s   updated %ds ago ", mode, int(max(stale, 0).Seconds())))
	if stale >= time.Minute {
		right = t.Dim.Render(mode+"   updated ") + t.Busy.Render(age(stale)+" ago") + " "
	}
	if problem := firstProblem(snap); problem != "" {
		right = t.Failure.Render("! "+problem) + "   " + right
	}
	return spread(keys, right, v.Width, g.More)
}

func firstProblem(snap state.Snapshot) string {
	for _, s := range snap.Sources {
		if !s.OK {
			if strings.HasPrefix(s.LastError, "api: ") {
				return s.LastError
			}
			return s.Name + ": " + s.LastError
		}
	}
	return ""
}
