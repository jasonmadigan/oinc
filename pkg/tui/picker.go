package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// PickerItem represents a selectable item in the picker.
type PickerItem struct {
	Name      string
	Hint      string // dim text after name (e.g. dependency info)
	Installed bool
	Selected  bool
}

// PickerModel is a multi-select list with keyboard navigation.
type PickerModel struct {
	title   string
	items   []PickerItem
	cursor  int
	quitted bool
	aborted bool
}

func NewPickerModel(title string, items []PickerItem) PickerModel {
	return PickerModel{
		title: title,
		items: items,
	}
}

func (m PickerModel) Init() tea.Cmd { return nil }

func (m PickerModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		case " ", "x":
			if !m.items[m.cursor].Installed {
				m.items[m.cursor].Selected = !m.items[m.cursor].Selected
			}
		case "enter":
			m.quitted = true
			return m, tea.Quit
		case "a":
			// toggle all uninstalled
			allSelected := true
			for _, it := range m.items {
				if !it.Installed && !it.Selected {
					allSelected = false
					break
				}
			}
			for i := range m.items {
				if !m.items[i].Installed {
					m.items[i].Selected = !allSelected
				}
			}
		}
	}
	return m, nil
}

func (m PickerModel) View() string {
	var b strings.Builder

	b.WriteString("\n")
	b.WriteString(Pig(m.title))
	b.WriteString("\n")

	installed := lipgloss.NewStyle().Foreground(lipgloss.Color("#22c55e"))
	cursor := lipgloss.NewStyle().Foreground(lipgloss.Color("#ff69b4"))

	for i, item := range m.items {
		prefix := "  "
		if i == m.cursor {
			prefix = cursor.Render("> ")
		}

		var check string
		switch {
		case item.Installed:
			check = installed.Render("[✓]")
		case item.Selected:
			check = Pink.Render("[x]")
		default:
			check = Dim.Render("[ ]")
		}

		name := item.Name
		if item.Installed {
			name = installed.Render(item.Name) + " " + Dim.Render("installed")
		}

		hint := ""
		if item.Hint != "" && !item.Installed {
			hint = " " + Dim.Render(item.Hint)
		}

		b.WriteString(fmt.Sprintf("%s %s %s%s\n", prefix, check, name, hint))
	}

	b.WriteString("\n")
	b.WriteString(Dim.Render("  space select  a all  enter confirm  q cancel"))
	b.WriteString("\n\n")

	return b.String()
}

// Aborted returns true if the user quit without confirming.
func (m PickerModel) Aborted() bool { return m.aborted }

// Selected returns the names of items the user selected (not already installed).
func (m PickerModel) Selected() []string {
	var names []string
	for _, item := range m.items {
		if item.Selected && !item.Installed {
			names = append(names, item.Name)
		}
	}
	return names
}
