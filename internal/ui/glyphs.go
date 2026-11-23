package ui

import "os"

type Glyphs struct {
	Busy, Idle, Offline                   string
	Success, Failure, Cancelled, Unknown  string
	Step, Done, Pending, Sep, Arrow, More string
	TopLeft, TopRight, BottomLeft         string
	BottomRight, Horizontal, Vertical     string
	Fill, Empty                           string
	Keys                                  string
}

var (
	UnicodeGlyphs = Glyphs{
		Busy: "●", Idle: "○", Offline: "✕",
		Success: "✓", Failure: "✗", Cancelled: "⊘", Unknown: "?",
		Step: "▸", Done: "✓", Pending: "·", Sep: "›", Arrow: "→", More: "…",
		TopLeft: "╭", TopRight: "╮", BottomLeft: "╰", BottomRight: "╯", Horizontal: "─", Vertical: "│",
		Fill: "█", Empty: "░", Keys: "←↑↓→",
	}
	// ConsoleGlyphs only uses runes in both the kernel's CP437 map and Terminus console fonts.
	ConsoleGlyphs = Glyphs{
		Busy: "■", Idle: "·", Offline: "x",
		Success: "√", Failure: "x", Cancelled: "-", Unknown: "?",
		Step: ">", Done: "√", Pending: "·", Sep: ">", Arrow: "→", More: "~",
		TopLeft: "┌", TopRight: "┐", BottomLeft: "└", BottomRight: "┘", Horizontal: "─", Vertical: "│",
		Fill: "█", Empty: "░", Keys: "←↑↓→",
	}
	ASCIIGlyphs = Glyphs{
		Busy: "*", Idle: "o", Offline: "x",
		Success: "+", Failure: "x", Cancelled: "-", Unknown: "?",
		Step: ">", Done: "+", Pending: ".", Sep: ">", Arrow: "->", More: "~",
		TopLeft: "+", TopRight: "+", BottomLeft: "+", BottomRight: "+", Horizontal: "-", Vertical: "|",
		Fill: "#", Empty: ".", Keys: "hjkl",
	}
)

// ParseGlyphs turns --glyphs into a set. "auto" picks console on the Linux console.
func ParseGlyphs(flag string) (Glyphs, bool) {
	switch flag {
	case "unicode":
		return UnicodeGlyphs, true
	case "console":
		return ConsoleGlyphs, true
	case "ascii":
		return ASCIIGlyphs, true
	case "auto":
		if os.Getenv("TERM") == "linux" {
			return ConsoleGlyphs, true
		}
		return UnicodeGlyphs, true
	}
	return Glyphs{}, false
}
