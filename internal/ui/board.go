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
	maxColumns   = 4
	minRecent    = 3
	compactBelow = 80
	narrowRecent = 100
	minCardWidth = 56
	// idleWidth covers " idle", the conclusion, the age of the last job and the gaps between them.
	idleWidth      = 26
	cardChrome     = 8
	busyBadgeWidth = len(" BUSY 0:00 ")
)

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

// short drops a repo owner that is also a group and the first word every runner name shares.
type short struct {
	owners map[string]bool
	prefix string
}

func shortener(snap state.Snapshot) short {
	s := short{owners: map[string]bool{}}
	var names []string
	for _, r := range snap.Runners {
		s.owners[r.Scope] = true
		names = append(names, r.Name)
	}
	s.prefix = sharedFirstWord(names)
	return s
}

func sharedFirstWord(names []string) string {
	if len(names) < 2 {
		return ""
	}
	cut := strings.Index(names[0], "-")
	if cut < 0 {
		return ""
	}
	prefix := names[0][:cut+1]
	for _, name := range names {
		if !strings.HasPrefix(name, prefix) || len(name) == len(prefix) {
			return ""
		}
	}
	return prefix
}

func (s short) repo(repo string) string {
	if owner, name, ok := strings.Cut(repo, "/"); ok && s.owners[owner] {
		return name
	}
	return repo
}

func (s short) runner(name string) string { return strings.TrimPrefix(name, s.prefix) }

type cards struct {
	names              short
	nameW, jobW, busyW int
	columns            int
}

// planCards puts groups side by side only while every card still shows each runner's last job in full.
func planCards(snap state.Snapshot, groupCount, w int) cards {
	c := cards{names: shortener(snap)}
	for _, r := range snap.Runners {
		c.nameW = max(c.nameW, width(c.names.runner(r.Name))+noteWidth(r))
		if r.Last != nil {
			c.jobW = max(c.jobW, width(jobLabel(r.Last.Workflow, r.Last.Name, ">")))
		}
		if r.Status == state.StatusBusy && r.Job != nil && r.Job.HasMetadata() {
			head := busyBadgeWidth + 2 + width(c.names.repo(r.Job.Repo)) + 2 + width(jobLabel(r.Job.Workflow, r.Job.Name, ">"))
			why := width(strings.Join(whyParts(r.Job, ASCIIGlyphs), " - "))
			c.busyW = max(c.busyW, head, why)
		}
	}
	need := max(cardChrome+c.nameW+max(idleWidth+c.jobW, c.busyW), minCardWidth)
	c.columns = max(min((w+1)/(need+1), maxColumns, groupCount), 1)
	if w < compactBelow {
		c.columns = 1
	}
	return c
}

func noteWidth(r state.Runner) int {
	if r.Note != "" {
		return 2
	}
	return 0
}

const noJobDetails = "job details not read"

// jobLabel is "workflow › job", or says the Worker log was gone before runrack read it.
func jobLabel(workflow, name, sep string) string {
	if workflow == "" && name == "" {
		return noJobDetails
	}
	return workflow + " " + sep + " " + name
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
	return 3
}

func Order(snap state.Snapshot, w int) [][]string {
	gs := groups(snap)
	var order [][]string
	for _, col := range layout(gs, planCards(snap, len(gs), w).columns, false) {
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

type boardParts struct {
	board, bottom []string
	recentRows    int
	wide          bool
}

func layoutBoard(v View, snap state.Snapshot, now time.Time) boardParts {
	wide := v.Width >= narrowRecent
	recentLines := minRecent
	if wide {
		recentLines *= 2
	}
	host := hostLines(v, snap, 0)
	board := boardLines(v, snap, now, false)
	room := func() int { return v.Height - 7 - len(host) - recentLines } // header, blanks, titles and footer
	if v.Height > 0 && len(board) > room() {
		board = boardLines(v, snap, now, true)
		if len(board) > room() {
			host = hostLines(v, snap, 1)
		}
		board = board[:min(len(board), max(room(), 0))]
	}
	bottom := append([]string{"", sectionTitle(v, "HOST", "")}, host...)
	bottom = append(bottom, footer(v, snap, now))
	parts := boardParts{board: board, bottom: bottom, recentRows: len(snap.Recent), wide: wide}
	if v.Height > 0 {
		room := v.Height - 4 - len(bottom) - len(board)
		used := 0
		for i, res := range snap.Recent {
			used += runHeight(res, parts.wide)
			if used > max(room, recentLines) {
				parts.recentRows = i
				break
			}
		}
	}
	return parts
}

func runHeight(res state.Result, wide bool) int {
	if wide && res.HasMetadata() {
		return 2
	}
	return 1
}

// ShownRuns are the keys of the finished runs the board has room for, top to bottom.
func ShownRuns(v View, snap state.Snapshot) []string {
	var keys []string
	for _, res := range snap.Recent[:layoutBoard(v, snap, snap.TakenAt).recentRows] {
		keys = append(keys, runKey(res))
	}
	return keys
}

func Board(v View, snap state.Snapshot, now time.Time) string {
	parts := layoutBoard(v, snap, now)
	lines := append([]string{header(v, snap, now), ""}, parts.board...)
	lines = append(lines, "", sectionTitle(v, "RECENT", recentTally(v, snap.Recent)))
	switch {
	case snap.TakenAt.IsZero():
		lines = append(lines, " "+v.Theme.Dim.Render("Loading history..."))
	case len(snap.Recent) == 0:
		lines = append(lines, " "+v.Theme.Dim.Render("No finished jobs in the last 24 hours."))
	default:
		lines = append(lines, recentLines(v, snap.Recent[:parts.recentRows], parts.wide, shortener(snap))...)
	}
	if v.Height > 0 {
		for len(lines)+len(parts.bottom) < v.Height {
			lines = append(lines, "")
		}
	}
	lines = append(lines, parts.bottom...)
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
	busy, idle, offline, starting := 0, 0, 0, 0
	for _, r := range snap.Runners {
		switch r.Status {
		case state.StatusBusy:
			busy++
		case state.StatusOffline:
			offline++
		case state.StatusStarting:
			starting++
		default:
			idle++
		}
	}
	left := " " + t.Title.Render("runrack") + "  " + t.Dim.Render(snap.Host.Hostname)
	busyStyle := t.Busy
	if busy == 0 {
		busyStyle = t.Dim
	}
	counts := busyStyle.Render(fmt.Sprintf("%s %d busy", g.Busy, busy)) + "   " +
		t.Idle.Render(fmt.Sprintf("%s %d idle", g.Idle, idle)) + "   " +
		t.Offline.Render(fmt.Sprintf("%s %d offline", g.Offline, offline))
	if starting > 0 {
		counts += "   " + t.Dim.Render(fmt.Sprintf("%s %d starting", g.Idle, starting))
	}
	right := t.Text.Render(now.In(v.Loc).Format("Mon 2 Jan  15:04:05")) + " "
	if v.Width < 100 {
		right = t.Text.Render(now.In(v.Loc).Format("15:04:05")) + " "
	}
	middleStart := max(width(left)+4, (v.Width-width(counts))/2)
	line := pad(left, middleStart) + counts
	return spread(line, right, v.Width, g.More)
}

func sectionTitle(v View, name, note string) string {
	head := " " + v.Theme.Title.Render(name) + " "
	if note != "" {
		head += " " + note + "  "
	}
	return head + v.Theme.Border.Render(strings.Repeat(v.Glyphs.Horizontal, max(v.Width-width(head)-1, 0)))
}

func recentTally(v View, recent []state.Result) string {
	var parts []string
	for _, conclusion := range []string{state.ConclusionSuccess, state.ConclusionFailure, state.ConclusionCancelled} {
		count := 0
		for _, res := range recent {
			if res.Conclusion == conclusion {
				count++
			}
		}
		if count > 0 {
			glyph, style := conclusionGlyph(v, conclusion)
			parts = append(parts, style.Render(fmt.Sprintf("%s %d", glyph, count)))
		}
	}
	return strings.Join(parts, "   ")
}

func boardLines(v View, snap state.Snapshot, now time.Time, compact bool) []string {
	if snap.TakenAt.IsZero() {
		return []string{" " + v.Theme.Dim.Render("Looking for runners...")}
	}
	if len(snap.Runners) == 0 {
		return []string{" " + v.Theme.Dim.Render("No runners found on this machine. Try `runrack doctor`.")}
	}
	gs := groups(snap)
	plan := planCards(snap, len(gs), v.Width)
	if v.Width < compactBelow {
		var lines []string
		for _, r := range snap.Runners {
			lines = append(lines, " "+listLine(v, r, v.Width-1, now, plan))
		}
		return lines
	}
	boxWidth := (v.Width - (plan.columns - 1)) / plan.columns
	var rendered [][]string
	height := 0
	for _, col := range layout(gs, plan.columns, compact) {
		var lines []string
		for _, g := range col {
			lines = append(lines, groupBox(v, g, boxWidth, now, compact, plan)...)
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

func groupBox(v View, g group, w int, now time.Time, compact bool, plan cards) []string {
	t, gl := v.Theme, v.Glyphs
	inner := w - 4
	var body []string
	for _, r := range g.runners {
		body = append(body, runnerLines(v, r, inner, now, compact, plan)...)
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
	case state.StatusStarting:
		return v.Theme.Dim.Render(v.Glyphs.Idle)
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

// nameRow keeps the status next to the name, so the eye does not travel across a wide TV.
func nameRow(v View, r state.Runner, right string, w int, plan cards) string {
	shown := plan.names.runner(r.Name)
	name := v.Theme.Title.Render(shown)
	if v.selected(r) {
		name = v.Theme.Text.Reverse(true).Bold(true).Render(shown)
	}
	left := statusGlyph(v, r) + " " + name
	if r.Note != "" {
		left += " " + v.Theme.Failure.Render("!")
	}
	right = fit(right, max(w-plan.nameW-4, w/2), v.Glyphs.More)
	left = fit(left, w-width(right)-2, v.Glyphs.More)
	return pad(left, min(plan.nameW+2, w-width(right)-2)) + "  " + right
}

func busyBadge(v View, elapsed time.Duration) string {
	return v.Theme.BusyBadge.Render(" BUSY " + clock(elapsed) + " ")
}

func runnerLines(v View, r state.Runner, w int, now time.Time, compact bool, plan cards) []string {
	t := v.Theme
	switch {
	case r.Status == state.StatusOffline:
		return []string{nameRow(v, r, t.Offline.Render("offline  "+r.Service), w, plan)}
	case r.Status == state.StatusStarting:
		return []string{nameRow(v, r, t.Dim.Render(" starting"), w, plan)}
	case r.Status != state.StatusBusy || r.Job == nil:
		return []string{nameRow(v, r, idleRight(v, r, now, plan, w), w, plan)}
	}
	job := r.Job
	indent := strings.Repeat(" ", plan.nameW+4)
	head := busyBadge(v, now.Sub(job.StartedAt))
	if !job.HasMetadata() {
		return []string{nameRow(v, r, head, w, plan), indent + busyStatus(v, r, now)}
	}
	head += "  " + t.Title.Render(plan.names.repo(job.Repo)) + "  " + workflowJob(v, job)
	lines := []string{nameRow(v, r, head, w, plan)}
	if compact {
		return append(lines, indent+stepText(v, job))
	}
	return append(lines, indent+runWhy(v, job), indent+cardProgress(v, job, w-plan.nameW-4, now))
}

func cardProgress(v View, job *state.Job, w int, now time.Time) string {
	t := v.Theme
	elapsed := now.Sub(job.StartedAt)
	progress := t.Text.Render(clock(elapsed))
	if job.Expected > 0 {
		expected := time.Duration(job.Expected)
		progress = bar(float64(elapsed)/float64(expected), min(max(w/4, 8), 20), v.Glyphs, t.Busy, t.Dim) + " " +
			t.Text.Render(clock(elapsed)) + t.Dim.Render(" / ~"+clock(expected))
	}
	if step := stepText(v, job); step != "" {
		progress += "   " + step
	}
	return progress
}

func branchLine(job *state.Job, g Glyphs) string {
	if job.BaseBranch != "" {
		return job.Branch + " " + g.Arrow + " " + job.BaseBranch
	}
	return job.Branch
}

// idleRight adds the repo of the last job only when it fits whole.
func idleRight(v View, r state.Runner, now time.Time, plan cards, w int) string {
	t := v.Theme
	right := " " + t.Idle.Render("idle") // lines up with the text of the busy badge
	if r.Last == nil {
		return right
	}
	last := r.Last
	glyph, style := conclusionGlyph(v, last.Conclusion)
	right += "  " + pad(style.Render(glyph+" "+last.Conclusion), 11) + "  " +
		t.Dim.Render(fmt.Sprintf("%4s", age(now.Sub(last.FinishedAt)))) + "  " +
		pad(t.Text.Render(jobLabel(last.Workflow, last.Name, v.Glyphs.Sep)), plan.jobW)
	if repo := plan.names.repo(last.Repo); plan.nameW+4+width(right)+3+width(repo) <= w {
		right += "   " + t.Dim.Render(repo)
	}
	return right
}

func listLine(v View, r state.Runner, w int, now time.Time, plan cards) string {
	if r.Status == state.StatusBusy && r.Job != nil && r.Job.HasMetadata() {
		job := r.Job
		right := busyBadge(v, now.Sub(job.StartedAt)) + " " + v.Theme.Dim.Render(plan.names.repo(job.Repo)+" "+v.Glyphs.Sep+" "+job.Name)
		return nameRow(v, r, right, w, plan)
	}
	if r.Status == state.StatusBusy {
		return nameRow(v, r, busyBadge(v, now.Sub(r.Job.StartedAt)), w, plan)
	}
	if r.Status == state.StatusOffline {
		return nameRow(v, r, v.Theme.Offline.Render("offline "+r.Service), w, plan)
	}
	if r.Status == state.StatusStarting {
		return nameRow(v, r, v.Theme.Dim.Render(" starting"), w, plan)
	}
	return nameRow(v, r, idleRight(v, r, now, plan, w), w, plan)
}

// recentLines sizes each column to its content and trims the widest first. Wide screens give every run a second line
// that says why it ran; narrow ones keep one line with the branch and drop the status word and the runner.
func recentLines(v View, results []state.Result, wide bool, names short) []string {
	t, g := v.Theme, v.Glyphs
	statusW, tookW := 1, 0
	texts := make([][4]string, len(results))
	var widths [4]int // repo, job, branch, runner
	for i, res := range results {
		texts[i] = [4]string{names.repo(res.Repo), jobLabel(res.Workflow, res.Name, g.Sep), res.Branch, names.runner(res.Runner)}
		for col, text := range texts[i] {
			widths[col] = max(widths[col], width(text))
		}
		tookW = max(tookW, len(clock(time.Duration(res.Duration))))
		glyph, _ := conclusionGlyph(v, res.Conclusion)
		statusW = max(statusW, width(glyph)+1+len(res.Conclusion))
	}
	if wide {
		widths[2] = 0
	} else {
		statusW, widths[3] = 1, 0
	}
	shrinkToFit(widths[:], v.Width-1-5-statusW-tookW-2*5)
	indent := strings.Repeat(" ", 1+5+2+statusW+2)
	var lines []string
	for i, res := range results {
		glyph, style := conclusionGlyph(v, res.Conclusion)
		status := glyph
		if wide {
			status += " " + res.Conclusion
		}
		cell := func(col int, style lipgloss.Style) string {
			return pad(style.Render(fit(texts[i][col], widths[col], g.More)), widths[col])
		}
		clockStyle := t.Dim
		if v.ShowSelection && v.Selected == runKey(res) {
			clockStyle = t.Text.Reverse(true).Bold(true)
		}
		cells := []string{clockStyle.Render(res.FinishedAt.In(v.Loc).Format("15:04")), pad(style.Render(status), statusW), cell(0, t.Text), cell(1, t.Text)}
		if !wide {
			cells = append(cells, cell(2, t.Dim))
		}
		cells = append(cells, t.Text.Render(fmt.Sprintf("%*s", tookW, clock(time.Duration(res.Duration)))))
		if wide {
			cells = append(cells, cell(3, t.Dim))
			if step := failedStep(&res.Job); step != "" {
				cells = append(cells, style.Render(g.Step+" "+step))
			}
		}
		lines = append(lines, strings.TrimRight(" "+strings.Join(cells, "  "), " "))
		if runHeight(res, wide) == 2 {
			lines = append(lines, indent+runWhy(v, &res.Job))
		}
	}
	return lines
}

func shrinkToFit(widths []int, room int) {
	for {
		widest, total := 0, 0
		for i, w := range widths {
			total += w
			if w > widths[widest] {
				widest = i
			}
		}
		if total <= room || widths[widest] <= 4 {
			return
		}
		widths[widest]--
	}
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

// hostLines puts the containers on at most maxLines lines, or as many as they need when maxLines is 0.
func hostLines(v View, snap state.Snapshot, maxLines int) []string {
	t, g, h := v.Theme, v.Glyphs, snap.Host
	if snap.TakenAt.IsZero() {
		return []string{" " + t.Dim.Render("Reading host load..."), ""}
	}
	memPct := 0.0
	if h.MemTotal > 0 {
		memPct = 100 * float64(h.MemUsed) / float64(h.MemTotal)
	}
	load := func(cells int) string {
		return " " + t.Dim.Render("cpu ") + t.Text.Render(fmt.Sprintf("%3.0f%% ", h.CPUPct)) + bar(h.CPUPct/100, cells, g, loadStyle(t, h.CPUPct), t.Dim) +
			"   " + t.Dim.Render("mem ") + t.Text.Render(fmt.Sprintf("%.1f/%.1fG ", float64(h.MemUsed)/(1<<30), float64(h.MemTotal)/(1<<30))) + bar(memPct/100, cells, g, loadStyle(t, memPct), t.Dim) +
			"   " + t.Dim.Render("load ") + t.Text.Render(fmt.Sprintf("%.2f %.2f %.2f", h.Load1, h.Load5, h.Load15)) + t.Dim.Render(fmt.Sprintf(" /%d", h.CPUs)) +
			"   " + t.Dim.Render("up ") + t.Text.Render(uptime(time.Duration(h.Uptime)))
	}
	first := load(10)
	if over := width(first) - (v.Width - 1); over > 0 {
		first = load(max(10-(over+1)/2, 3))
	}
	return append([]string{first}, containerLines(v, snap, maxLines)...)
}

// containerLines lays the containers out as a grid of equal cells, balanced over as few lines as fit.
func containerLines(v View, snap state.Snapshot, maxLines int) []string {
	t := v.Theme
	type usage struct {
		name  string
		stats *state.ContainerStats
	}
	var all []usage
	var containers []string
	seen := map[string]bool{}
	for _, r := range snap.Runners {
		name := "host"
		if r.Placement.Kind == scan.KindIncus {
			name = r.Placement.Container
			containers = append(containers, name)
		}
		if seen[name] || r.Container == nil {
			continue
		}
		seen[name] = true
		all = append(all, usage{name, r.Container})
	}
	if len(all) == 0 {
		return nil
	}
	prefix := sharedFirstWord(containers)
	nameW := 0
	for i := range all {
		all[i].name = strings.TrimPrefix(all[i].name, prefix)
		nameW = max(nameW, width(all[i].name))
	}
	const gap = 3
	cellW := nameW + len(" 100% 12.3G")
	fits := max((v.Width-1+gap)/(cellW+gap), 1)
	rows := (len(all) + fits - 1) / fits
	if maxLines > 0 {
		rows = min(rows, maxLines)
	}
	perLine := (len(all) + rows - 1) / rows
	var lines []string
	for start := 0; start < len(all); start += perLine {
		var cells []string
		for _, u := range all[start:min(start+perLine, len(all))] {
			cells = append(cells, t.Dim.Render(pad(u.name, nameW))+t.Text.Render(fmt.Sprintf(" %3.0f%% %5s", u.stats.CPUPct, gib(u.stats.MemBytes))))
		}
		lines = append(lines, " "+strings.Join(cells, strings.Repeat(" ", gap)))
	}
	return lines
}

func footer(v View, snap state.Snapshot, now time.Time) string {
	t, g := v.Theme, v.Glyphs
	keys := " " + t.Dim.Render(g.Keys+" select  enter detail  r refresh  ? help  q quit")
	if v.Width < narrowRecent {
		keys = " " + t.Dim.Render(g.Keys+" select  enter detail  ? help  q quit")
	}
	mode := "local"
	if snap.API {
		mode = "local + api"
	}
	stale := now.Sub(snap.TakenAt)
	right := t.Dim.Render(fmt.Sprintf("%s   updated %ds ago ", mode, int(max(stale, 0).Seconds())))
	switch {
	case snap.TakenAt.IsZero():
		right = t.Dim.Render("loading ")
	case stale >= time.Minute:
		right = t.Dim.Render(mode+"   updated ") + t.Busy.Render(age(stale)+" ago") + " "
	}
	if problem := firstProblem(snap); problem != "" {
		room := max(v.Width-width(keys)-width(right)-4, 16)
		right = t.Failure.Render(fit("! "+problem, room, g.More)) + "   " + right
	}
	return spread(keys, right, v.Width, g.More)
}

// firstProblem names a runner in words when it can, before the raw error of a source.
func firstProblem(snap state.Snapshot) string {
	for _, r := range snap.Runners {
		if r.Note != "" {
			return r.Name + ": " + r.Note
		}
	}
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
