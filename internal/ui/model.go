package ui

import (
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/YoanWai/runrack/internal/state"
)

// selectionTimeout hides the selection on an untouched TV.
const selectionTimeout = 60 * time.Second

type Controller interface {
	Refresh()
	Focus(key string)
}

type screen int

const (
	screenBoard screen = iota
	screenDetail
	screenHelp
)

type snapshotMsg state.Snapshot

type tickMsg time.Time

type Model struct {
	view      View
	snap      state.Snapshot
	snaps     <-chan state.Snapshot
	control   Controller
	screen    screen
	back      screen
	lastKey   time.Time
	now       time.Time
	hasLayout bool
	userMoved bool
}

func NewModel(view View, snaps <-chan state.Snapshot, control Controller) Model {
	now := time.Now()
	return Model{view: view, snaps: snaps, control: control, lastKey: now, now: now}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.waitSnapshot(), tick())
}

// tick keeps the clock and the "updated ago" warning moving when no snapshot arrives.
func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) waitSnapshot() tea.Cmd {
	return func() tea.Msg { return snapshotMsg(<-m.snaps) }
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.view.Width, m.view.Height, m.hasLayout = msg.Width, msg.Height, true
	case snapshotMsg:
		m.snap = state.Snapshot(msg)
		m.now = time.Now()
		m.ensureSelection()
		return m, m.waitSnapshot()
	case tickMsg:
		m.now = time.Time(msg)
		return m, tick()
	case tea.KeyPressMsg:
		m.lastKey = time.Now()
		return m.key(msg.String())
	}
	return m, nil
}

func (m *Model) ensureSelection() {
	_, isRunner := findRunner(m.snap, m.view.Selected)
	_, isRun := findRun(m.snap, m.view.Selected)
	if (!isRunner && !isRun || !m.userMoved) && len(m.snap.Runners) > 0 {
		m.view.Selected = Order(m.snap, m.view.Width)[0][0]
	}
}

func (m Model) key(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "q", "ctrl+c":
		return m, tea.Quit
	case "?":
		if m.screen == screenHelp {
			m.screen = m.back
		} else {
			m.back, m.screen = m.screen, screenHelp
		}
	case "esc", "backspace":
		if m.screen == screenHelp {
			m.screen = m.back
		} else {
			m.screen = screenBoard
			m.focus("")
		}
	case "enter":
		if m.screen == screenBoard && m.view.Selected != "" {
			m.screen = screenDetail
			if !strings.HasPrefix(m.view.Selected, runKeyPrefix) {
				m.focus(m.view.Selected)
			}
		}
	case "r":
		if m.control != nil {
			m.control.Refresh()
		}
	case "up", "k":
		m.move(0, -1)
	case "down", "j":
		m.move(0, 1)
	case "left", "h":
		m.move(-1, 0)
	case "right", "l":
		m.move(1, 0)
	}
	return m, nil
}

func (m *Model) focus(key string) {
	if m.control != nil {
		m.control.Focus(key)
	}
}

// move walks the runner columns and then, below the last runner, the finished runs.
func (m *Model) move(dx, dy int) {
	if m.screen != screenBoard {
		return
	}
	order := Order(m.snap, m.view.Width)
	runs := ShownRuns(m.view, m.snap)
	if i := slices.Index(runs, m.view.Selected); i >= 0 {
		m.userMoved = true
		switch {
		case dy < 0 && i == 0 && len(order) > 0:
			m.view.Selected = order[0][len(order[0])-1]
		case dy != 0:
			m.view.Selected = runs[min(max(i+dy, 0), len(runs)-1)]
		}
		return
	}
	col, row := 0, 0
	for c, keys := range order {
		for r, key := range keys {
			if key == m.view.Selected {
				col, row = c, r
			}
		}
	}
	if len(order) == 0 {
		return
	}
	m.userMoved = true
	if dy > 0 && row == len(order[col])-1 && len(runs) > 0 {
		m.view.Selected = runs[0]
		return
	}
	col = min(max(col+dx, 0), len(order)-1)
	row = min(max(row+dy, 0), len(order[col])-1)
	m.view.Selected = order[col][row]
}

func (m Model) View() tea.View {
	if !m.hasLayout {
		return tea.NewView("")
	}
	view := m.view
	view.ShowSelection = m.now.Sub(m.lastKey) < selectionTimeout
	content := ""
	switch m.screen {
	case screenDetail:
		content = Detail(view, m.snap, view.Selected, m.now)
		if strings.HasPrefix(view.Selected, runKeyPrefix) {
			content = RunDetail(view, m.snap, view.Selected, m.now)
		}
	case screenHelp:
		content = Help(view)
	default:
		content = Board(view, m.snap, m.now)
	}
	v := tea.NewView(content)
	v.AltScreen = true
	return v
}
