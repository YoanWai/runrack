package ui

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

func TestArrowsWalkFromRunnersIntoRunsAndEnterOpensOne(t *testing.T) {
	snap := demoSnapshot()
	view := View{Width: 120, Height: 50, Theme: NewTheme(colorprofile.Ascii), Glyphs: ASCIIGlyphs, Loc: time.UTC}
	model := Model{view: view, snap: snap, hasLayout: true, now: goldenNow, lastKey: goldenNow}
	press := func(key string) {
		next, _ := model.key(key)
		model = next.(Model)
	}
	model.ensureSelection()
	for range Order(snap, view.Width)[0] {
		press("down")
	}
	if got, want := model.view.Selected, runKey(snap.Recent[0]); got != want {
		t.Fatalf("after the last runner of the first card, down selects %q, want %q", got, want)
	}
	press("down")
	if got := model.view.Selected; got != runKey(snap.Recent[1]) {
		t.Fatalf("selected %q", got)
	}
	press("up")
	press("up")
	if got := model.view.Selected; !strings.HasPrefix(got, "github|") {
		t.Fatalf("up from the first run returns to a runner, got %q", got)
	}
	press("down")
	press("enter")
	out := ansi.Strip(model.View().Content)
	if !strings.Contains(out, "PR #14 fix: homelab runner labels") || !strings.Contains(out, "FAILURE") {
		t.Fatalf("run detail:\n%s", out)
	}
}
