package cli

import "github.com/charmbracelet/lipgloss"

var (
	styleOK          = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	styleErr         = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	styleWarning     = lipgloss.NewStyle().Foreground(lipgloss.Color("11")).Bold(true)
	styleKey         = lipgloss.NewStyle().Foreground(lipgloss.Color("14"))
	styleSelected    = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Bold(true)
	styleUnsupported = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
	styleCursor      = lipgloss.NewStyle().Foreground(lipgloss.Color("12")).Bold(true)
	styleSubtext     = lipgloss.NewStyle().Foreground(lipgloss.Color("8"))
)
