package cli

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/theomorin/dbpilot/internal/detect"
)

type selectorModel struct {
	instances []detect.DetectedInstance
	checked   []bool
	cursor    int
	nameWidth int
}

func newSelectorModel(instances []detect.DetectedInstance) selectorModel {
	checked := make([]bool, len(instances))
	maxLen := 0
	for i, inst := range instances {
		checked[i] = inst.Engine == detect.EnginePostgres && inst.Confidence == detect.ConfidenceHigh
		if len(inst.DisplayName) > maxLen {
			maxLen = len(inst.DisplayName)
		}
	}
	return selectorModel{instances: instances, checked: checked, nameWidth: maxLen}
}

func (m selectorModel) Init() tea.Cmd { return nil }

func (m selectorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			for i := range m.checked {
				m.checked[i] = false
			}
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.instances)-1 {
				m.cursor++
			}
		case " ":
			if m.instances[m.cursor].Engine == detect.EnginePostgres {
				m.checked[m.cursor] = !m.checked[m.cursor]
			}
		case "enter":
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m selectorModel) View() string {
	var b strings.Builder
	b.WriteString("Select instances to back up:\n")
	b.WriteString(styleSubtext.Render("  ↑/↓ navigate   space toggle   enter confirm") + "\n\n")

	for i, inst := range m.instances {
		supported := inst.Engine == detect.EnginePostgres

		cursorChar := "  "
		if i == m.cursor {
			cursorChar = "> "
		}

		checkbox := "[ ]"
		if m.checked[i] {
			checkbox = styleSelected.Render("[✓]")
		} else if !supported {
			checkbox = styleUnsupported.Render("[ ]")
		}

		// Pad name to consistent width (no ANSI codes here, so padding is reliable)
		name := inst.DisplayName + strings.Repeat(" ", m.nameWidth-len(inst.DisplayName))
		engine := fmt.Sprintf("%-10s", inst.Engine)

		var info string
		if supported {
			info = fmt.Sprintf("%d (%s)", inst.Score, inst.Confidence)
		} else {
			info = "(not supported yet)"
		}

		line := fmt.Sprintf("%s  %s  %s", name, engine, info)
		if !supported {
			line = styleUnsupported.Render(line)
		}

		row := fmt.Sprintf("%s%s %s", cursorChar, checkbox, line)
		if i == m.cursor {
			row = styleCursor.Render(cursorChar) + checkbox + " " + line
		}
		b.WriteString(row + "\n")
	}
	return b.String()
}

// RunSelector displays an interactive checkbox list and returns the selected instances.
func RunSelector(instances []detect.DetectedInstance) ([]detect.DetectedInstance, error) {
	m := newSelectorModel(instances)
	result, err := tea.NewProgram(m).Run()
	if err != nil {
		return nil, err
	}
	final := result.(selectorModel)

	var selected []detect.DetectedInstance
	for i, inst := range final.instances {
		if final.checked[i] {
			selected = append(selected, inst)
		}
	}
	return selected, nil
}
