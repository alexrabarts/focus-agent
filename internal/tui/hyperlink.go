package tui

import "fmt"

// makeHyperlink creates a clickable hyperlink using OSC 8 escape sequences.
// This works in terminals that support OSC 8 (iTerm2, Ghostty, VS Code, Windows Terminal, etc.).
// In terminals that don't support it, the text will be displayed without the link.
func makeHyperlink(url, text string) string {
	return fmt.Sprintf("\x1b]8;;%s\x1b\\%s\x1b]8;;\x1b\\", url, text)
}
