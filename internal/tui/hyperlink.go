package tui

import (
	"fmt"
	"os"
	"strings"
)

var supportsHyperlinks bool

func init() {
	supportsHyperlinks = detectHyperlinkSupport()
}

// detectHyperlinkSupport checks if the terminal supports OSC 8 hyperlinks
func detectHyperlinkSupport() bool {
	// Check TERM_PROGRAM (set by many modern terminals)
	termProgram := os.Getenv("TERM_PROGRAM")
	switch termProgram {
	case "iTerm.app", "vscode", "Hyper", "WezTerm", "ghostty":
		return true
	}

	// Check for Windows Terminal
	if os.Getenv("WT_SESSION") != "" {
		return true
	}

	// Check TERM variable for specific terminals
	term := os.Getenv("TERM")
	if strings.Contains(term, "kitty") || strings.Contains(term, "alacritty") {
		return true
	}

	// Default to false for compatibility (Terminal.app, xterm, etc.)
	return false
}

// makeHyperlink creates a clickable hyperlink using OSC 8 escape sequences.
// If the terminal doesn't support OSC 8, it falls back to displaying the URL.
func makeHyperlink(url, text string) string {
	if supportsHyperlinks {
		return fmt.Sprintf("\x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\", url, text)
	}
	// Fallback: show text and URL
	return fmt.Sprintf("%s (%s)", text, url)
}
