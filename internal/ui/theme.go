package ui

import (
	"os"
	"strconv"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/colorprofile"
)

type Theme struct {
	Busy, Idle, Offline, Success, Failure, Cancelled lipgloss.Style
	Text, Dim, Border, Title                         lipgloss.Style
	Accents                                          []lipgloss.Style
}

// NewTheme maps tokens to colors per profile. The ANSI column only uses colors 1 to 7:
// a 512-glyph console font spends the bright bit on glyphs, leaving 8 colors.
func NewTheme(profile colorprofile.Profile) Theme {
	pick := lipgloss.Complete(profile)
	fg := func(ansi, ansi256 int, truecolor string) lipgloss.Style {
		return lipgloss.NewStyle().Foreground(pick(lipgloss.Color(strconv.Itoa(ansi)), lipgloss.Color(strconv.Itoa(ansi256)), lipgloss.Color(truecolor)))
	}
	theme := Theme{
		Busy:      fg(3, 214, "#F5B83D").Bold(true),
		Idle:      fg(2, 71, "#6BCB77"),
		Offline:   fg(7, 245, "#6C7086"),
		Success:   fg(2, 114, "#7EE787"),
		Failure:   fg(1, 203, "#FF6B6B").Bold(true),
		Cancelled: fg(5, 176, "#C792EA"),
		Text:      fg(7, 254, "#E6E6E6"),
		Dim:       fg(7, 245, "#8A8F98"),
		Border:    fg(6, 60, "#3B4261"),
		Title:     fg(7, 254, "#E6E6E6").Bold(true),
	}
	for i, truecolor := range []string{"#7DCFFF", "#82AAFF", "#C099FF", "#9ECE6A"} {
		theme.Accents = append(theme.Accents, fg(6, []int{117, 111, 141, 149}[i], truecolor).Bold(true))
	}
	return theme
}

// ParseColor turns --color into a profile. "auto" detects from the terminal.
func ParseColor(flag string) (colorprofile.Profile, bool) {
	switch flag {
	case "none":
		return colorprofile.Ascii, true
	case "16":
		return colorprofile.ANSI, true
	case "256":
		return colorprofile.ANSI256, true
	case "truecolor":
		return colorprofile.TrueColor, true
	case "auto":
		return colorprofile.Detect(os.Stdout, os.Environ()), true
	}
	return colorprofile.NoTTY, false
}
