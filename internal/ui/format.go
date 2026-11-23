package ui

import (
	"fmt"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func width(s string) int { return lipgloss.Width(s) }

func fit(s string, w int, tail string) string {
	if w <= 0 {
		return ""
	}
	if width(s) <= w {
		return s
	}
	if width(tail) >= w {
		tail = ""
	}
	return ansi.Truncate(s, w, tail)
}

func pad(s string, w int) string {
	if gap := w - width(s); gap > 0 {
		return s + strings.Repeat(" ", gap)
	}
	return s
}

// spread puts left and right on one line of w cells. Right wins when space runs out.
func spread(left, right string, w int, tail string) string {
	right = fit(right, w, tail)
	room := w - width(right)
	if width(right) > 0 {
		room--
	}
	left = fit(left, room, tail)
	return pad(left, w-width(right)) + right
}

// clock formats a duration as 0:55 or 1:02:31.
func clock(d time.Duration) string {
	d = max(d, 0).Round(time.Second)
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	if h > 0 {
		return fmt.Sprintf("%d:%02d:%02d", h, m, s)
	}
	return fmt.Sprintf("%d:%02d", m, s)
}

func age(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func uptime(d time.Duration) string {
	days, hours := int(d.Hours())/24, int(d.Hours())%24
	if days > 0 {
		return fmt.Sprintf("%dd %dh", days, hours)
	}
	return fmt.Sprintf("%dh %dm", hours, int(d.Minutes())%60)
}

func gib(b int64) string { return fmt.Sprintf("%.1fG", float64(b)/(1<<30)) }

func bar(ratio float64, cells int, g Glyphs, fill, empty lipgloss.Style) string {
	ratio = min(max(ratio, 0), 1)
	filled := int(ratio*float64(cells) + 0.5)
	return fill.Render(strings.Repeat(g.Fill, filled)) + empty.Render(strings.Repeat(g.Empty, cells-filled))
}
