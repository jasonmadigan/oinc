package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// VersionItem represents a version in the single-select picker.
type VersionItem struct {
	Version   string
	Hint      string
	IsDefault bool
}

// VersionPickerModel is a single-select list for choosing a version.
type VersionPickerModel struct {
	title   string
	items   []VersionItem
	cursor  int
	quitted bool
	aborted bool
}

func NewVersionPickerModel(title string, items []VersionItem) VersionPickerModel {
	cursor := 0
	for i, it := range items {
		if it.IsDefault {
			cursor = i
		}
	}
	return VersionPickerModel{
		title: title,
		items: items,
		cursor: cursor,
	}
}

func (m VersionPickerModel) Init() tea.Cmd { return nil }

func (m VersionPickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c", "esc":
			m.aborted = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < len(m.items)-1 {
				m.cursor++
			}
		case "enter":
			m.quitted = true
			return m, tea.Quit
		}
	}
	return m, nil
}

func (m VersionPickerModel) View() string {
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString(Pig(m.title))
	b.WriteString("\n")

	for i, item := range m.items {
		prefix := "  "
		if i == m.cursor {
			prefix = Pink.Render("> ")
		}

		dot := Dim.Render("●")
		if i == m.cursor {
			dot = Pink.Render("●")
		}

		name := item.Version
		if i == m.cursor {
			name = Bold.Render(name)
		}

		hint := ""
		if item.Hint != "" {
			hint = "  " + Dim.Render(item.Hint)
		}

		tag := ""
		if item.IsDefault {
			tag = "  " + Green.Render("[default]")
		}

		fmt.Fprintf(&b, "%s %s %s%s%s\n", prefix, dot, name, hint, tag)
	}

	b.WriteString("\n")
	b.WriteString(Dim.Render("  up/down navigate  enter select  q cancel"))
	b.WriteString("\n\n")

	return b.String()
}

// Aborted returns true if the user quit without selecting.
func (m VersionPickerModel) Aborted() bool { return m.aborted }

// Selected returns the version string at the cursor position.
func (m VersionPickerModel) Selected() string {
	if m.aborted || m.cursor >= len(m.items) {
		return ""
	}
	return m.items[m.cursor].Version
}
