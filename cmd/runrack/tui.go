package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/YoanWai/runrack/internal/demo"
	"github.com/YoanWai/runrack/internal/state"
	"github.com/YoanWai/runrack/internal/ui"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/term"
)

func newView(opts options) (ui.View, colorprofile.Profile) {
	profile, _ := ui.ParseColor(opts.color)
	glyphs, _ := ui.ParseGlyphs(opts.glyphs)
	return ui.View{Theme: ui.NewTheme(profile), Glyphs: glyphs, Loc: time.Local}, profile
}

func runTUI(opts options, stderr io.Writer) int {
	view, profile := newView(opts)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	snaps := make(chan state.Snapshot, 1)
	var control ui.Controller
	if opts.demo {
		go playDemo(ctx, snaps)
	} else {
		if _, err := os.Stat("/proc/stat"); err != nil {
			fmt.Fprintln(stderr, "runrack: no /proc: runrack discovers runners on Linux only (try --demo)")
			return 1
		}
		engine, _ := newEngine(opts)
		go engine.Run(ctx, snaps)
		control = engine
	}
	program := tea.NewProgram(ui.NewModel(view, snaps, control), tea.WithColorProfile(profile))
	if _, err := program.Run(); err != nil {
		fmt.Fprintln(stderr, "runrack:", err)
		return 1
	}
	return 0
}

func playDemo(ctx context.Context, snaps chan state.Snapshot) {
	start := time.Now()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case snaps <- demo.World(start, time.Now()):
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func printOnce(opts options, stdout, stderr io.Writer) int {
	snap, err := collectOnce(opts)
	if err != nil {
		fmt.Fprintln(stderr, "runrack:", err)
		return 1
	}
	view, profile := newView(opts)
	view.Width = opts.width
	if view.Width == 0 {
		view.Width = 120
		if w, _, err := term.GetSize(os.Stdout.Fd()); err == nil && w > 0 {
			view.Width = w
		}
	}
	out := &colorprofile.Writer{Forward: stdout, Profile: profile}
	fmt.Fprintln(out, ui.Board(view, snap, snap.TakenAt))
	return 0
}
