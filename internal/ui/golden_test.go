package ui

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/YoanWai/runrack/internal/demo"
	"github.com/YoanWai/runrack/internal/state"
	"github.com/charmbracelet/colorprofile"
	"github.com/charmbracelet/x/ansi"
)

var update = flag.Bool("update", false, "rewrite golden files")

var (
	goldenNow = time.Date(2026, 11, 23, 17, 42, 7, 0, time.UTC)
	sizes     = [][2]int{{80, 24}, {120, 33}, {160, 45}, {240, 75}}
	combos    = []struct {
		name    string
		profile colorprofile.Profile
		glyphs  Glyphs
	}{
		{"ascii", colorprofile.Ascii, ASCIIGlyphs},
		{"console", colorprofile.ANSI, ConsoleGlyphs},
		{"truecolor", colorprofile.TrueColor, UnicodeGlyphs},
	}
)

func demoSnapshot() state.Snapshot { return demo.World(goldenNow, goldenNow) }

func TestBoardGolden(t *testing.T) {
	snap := demoSnapshot()
	for _, combo := range combos {
		for _, size := range sizes {
			name := fmt.Sprintf("board_%s_%dx%d", combo.name, size[0], size[1])
			t.Run(name, func(t *testing.T) {
				view := View{Width: size[0], Height: size[1], Theme: NewTheme(combo.profile), Glyphs: combo.glyphs, Loc: time.UTC,
					Selected: snap.Runners[0].Key, ShowSelection: true}
				out := Board(view, snap, goldenNow)
				checkFrame(t, out, size[0], size[1], combo.name == "console")
				golden(t, name, out)
			})
		}
	}
}

func TestDetailAndHelpGolden(t *testing.T) {
	snap := demoSnapshot()
	for _, combo := range combos {
		view := View{Width: 120, Height: 33, Theme: NewTheme(combo.profile), Glyphs: combo.glyphs, Loc: time.UTC}
		for _, screen := range []struct {
			name string
			out  string
		}{
			{"detail_busy", Detail(view, snap, snap.Runners[0].Key, goldenNow)},
			{"detail_idle", Detail(view, snap, snap.Runners[1].Key, goldenNow)},
			{"help", Help(view)},
		} {
			t.Run(screen.name+"_"+combo.name, func(t *testing.T) {
				checkFrame(t, screen.out, 120, 33, combo.name == "console")
				golden(t, screen.name+"_"+combo.name, screen.out)
			})
		}
	}
}

func TestBoardWithoutRunners(t *testing.T) {
	view := View{Width: 80, Height: 24, Theme: NewTheme(colorprofile.Ascii), Glyphs: ASCIIGlyphs, Loc: time.UTC}
	out := ansi.Strip(Board(view, state.Snapshot{TakenAt: goldenNow}, goldenNow))
	if !strings.Contains(out, "No runners found") || !strings.Contains(out, "No finished jobs") {
		t.Fatalf("empty board:\n%s", out)
	}
}

func checkFrame(t *testing.T, out string, w, h int, console bool) {
	t.Helper()
	lines := strings.Split(out, "\n")
	if len(lines) > h {
		t.Errorf("%d lines, screen has %d", len(lines), h)
	}
	for i, line := range lines {
		if got := width(line); got > w {
			t.Errorf("line %d is %d cells wide, max %d: %q", i, got, w, ansi.Strip(line))
		}
	}
	if console {
		allowed := consoleRunes(t)
		for _, r := range ansi.Strip(out) {
			if r != '\n' && (r < 0x20 || r > 0x7e) && !strings.ContainsRune(allowed, r) {
				t.Errorf("rune %q (U+%04X) is not in the console font", r, r)
			}
		}
	}
}

// consoleRunes is the non-ASCII set both the kernel CP437 map and Uni2-TerminusBold24x12 can draw,
// parsed from that font's PSF2 unicode table.
func consoleRunes(t *testing.T) string {
	data, err := os.ReadFile("testdata/console-runes.txt")
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

func golden(t *testing.T, name, got string) {
	t.Helper()
	path := filepath.Join("testdata", name+".golden")
	if *update {
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run go test ./internal/ui -update)", err)
	}
	if !bytes.Equal(want, []byte(got)) {
		t.Errorf("%s differs from golden. Plain render:\n%s", name, ansi.Strip(got))
	}
}

func TestBoardKeepsLongNamesWholeOnTV(t *testing.T) {
	snap := demoSnapshot()
	for i := range snap.Runners {
		if last := snap.Runners[i].Last; last != nil {
			long := *last
			long.Workflow, long.Name = "Preview deploy", "deploy to staging"
			snap.Runners[i].Last = &long
		}
	}
	snap.Recent[0].Branch = "fix/ci-preview-comment-old-gh"
	view := View{Width: 120, Height: 66, Theme: NewTheme(colorprofile.ANSI), Glyphs: ConsoleGlyphs, Loc: time.UTC}
	out := Board(view, snap, goldenNow)
	checkFrame(t, out, 120, 66, true)
	plain := ansi.Strip(out)
	for _, want := range []string{"Preview deploy > deploy to staging", "fix/ci-preview-comment-old-gh", "globex-2", "acme-1 "} {
		if !strings.Contains(plain, want) {
			t.Errorf("board lacks %q:\n%s", want, plain)
		}
	}
	if cut := regexp.MustCompile(`[^ ]~`).FindString(plain); cut != "" {
		t.Errorf("board truncates %q:\n%s", cut, plain)
	}
}

func TestSharedFirstWord(t *testing.T) {
	for _, tc := range []struct {
		names []string
		want  string
	}{
		{[]string{"zenbook-acme-1", "zenbook-globex-1"}, "zenbook-"},
		{[]string{"zenbook-acme-1", "laptop-acme-2"}, ""},
		{[]string{"zenbook-acme-1"}, ""},
		{[]string{"zenbook-", "zenbook-acme-1"}, ""},
		{[]string{"acme", "acme"}, ""},
	} {
		if got := sharedFirstWord(tc.names); got != tc.want {
			t.Errorf("sharedFirstWord(%q) = %q, want %q", tc.names, got, tc.want)
		}
	}
}
