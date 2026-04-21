package cli

import (
	"fmt"
	"io"

	"github.com/charmbracelet/lipgloss"
)

var (
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#3DD68C"))
	subtleStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
	successStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#3DD68C"))
	errorStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FF6B6B"))
	codeStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#FFB454"))
	bulletStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("#3DD68C"))
)

func bullet(s string) string {
	return bulletStyle.Render("✓ ") + s
}

// printErr writes an error line to w in the canonical CLI format:
// red ✗ glyph + the message + newline. Mirrors bullet() for the
// success path. Only for standalone "✗ ..." lines — per-row glyphs
// composed into a wider string (see renderApplyResults / renderCheckRows)
// stay inline because they're not a full line.
func printErr(w io.Writer, msg string) {
	fmt.Fprintln(w, errorStyle.Render("✗ "+msg))
}
