package tui

import (
	"os"
	"testing"
)

func TestDetectHyperlinkSupport(t *testing.T) {
	tests := []struct {
		name        string
		termProgram string
		term        string
		wtSession   string
		want        bool
	}{
		{
			name:        "iTerm2",
			termProgram: "iTerm.app",
			want:        true,
		},
		{
			name:        "Ghostty",
			termProgram: "ghostty",
			want:        true,
		},
		{
			name:        "VS Code",
			termProgram: "vscode",
			want:        true,
		},
		{
			name:        "WezTerm",
			termProgram: "WezTerm",
			want:        true,
		},
		{
			name:      "Windows Terminal",
			wtSession: "abc123",
			want:      true,
		},
		{
			name: "Kitty",
			term: "xterm-kitty",
			want: true,
		},
		{
			name: "Alacritty",
			term: "alacritty",
			want: true,
		},
		{
			name:        "Terminal.app",
			termProgram: "Apple_Terminal",
			term:        "xterm-256color",
			want:        false,
		},
		{
			name: "xterm",
			term: "xterm",
			want: false,
		},
		{
			name: "default",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save original env
			origTermProgram := os.Getenv("TERM_PROGRAM")
			origTerm := os.Getenv("TERM")
			origWTSession := os.Getenv("WT_SESSION")

			// Set test env
			os.Setenv("TERM_PROGRAM", tt.termProgram)
			os.Setenv("TERM", tt.term)
			os.Setenv("WT_SESSION", tt.wtSession)

			// Test
			got := detectHyperlinkSupport()

			// Restore env
			os.Setenv("TERM_PROGRAM", origTermProgram)
			os.Setenv("TERM", origTerm)
			os.Setenv("WT_SESSION", origWTSession)

			if got != tt.want {
				t.Errorf("detectHyperlinkSupport() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMakeHyperlink(t *testing.T) {
	url := "https://example.com"
	text := "Example Link"

	// Test with hyperlink support
	supportsHyperlinks = true
	got := makeHyperlink(url, text)
	want := "\x1b]8;;https://example.com\x1b\\Example Link\x1b]8;;\x1b\\"
	if got != want {
		t.Errorf("makeHyperlink() with support = %q, want %q", got, want)
	}

	// Test without hyperlink support
	supportsHyperlinks = false
	got = makeHyperlink(url, text)
	want = "Example Link (https://example.com)"
	if got != want {
		t.Errorf("makeHyperlink() without support = %q, want %q", got, want)
	}
}
