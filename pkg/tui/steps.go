package tui

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"
)

// Step represents a named unit of work in a multi-step flow.
type Step struct {
	Name   string
	Run    func() error
	status string
	mu     sync.Mutex
}

// SetStatus updates the live sub-status shown next to the spinner.
func (s *Step) SetStatus(status string) {
	s.mu.Lock()
	s.status = status
	s.mu.Unlock()
}

func (s *Step) getStatus() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

type stepDoneMsg struct{ err error }
type summaryMsg struct{ text string }

// StepsModel is a bubbletea model that runs steps sequentially,
// showing a spinner on the active step and checkmarks on completed ones.
type StepsModel struct {
	steps     []*Step
	current   int
	err       error
	done      bool
	spinner   spinner.Model
	title     string
	summary   string
	summaryFn func() string
}

func NewStepsModel(title string, steps []*Step) StepsModel {
	s := spinner.New()
	s.Spinner = spinner.MiniDot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("#ff69b4"))
	return StepsModel{
		steps:   steps,
		title:   title,
		spinner: s,
	}
}

func (m StepsModel) Init() tea.Cmd {
	if len(m.steps) == 0 {
		m.done = true
		return tea.Quit
	}
	return tea.Batch(m.spinner.Tick, m.runCurrent())
}

func (m StepsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.err = fmt.Errorf("interrupted")
			return m, tea.Quit
		}
	case stepDoneMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, tea.Quit
		}
		m.current++
		if m.current >= len(m.steps) {
			m.done = true
			if m.summaryFn != nil {
				fn := m.summaryFn
				return m, func() tea.Msg { return summaryMsg{text: fn()} }
			}
			return m, tea.Quit
		}
		return m, m.runCurrent()
	case summaryMsg:
		m.summary = msg.text
		return m, tea.Quit
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m StepsModel) View() string {
	var b strings.Builder
	b.WriteString("\n")
	if m.title != "" {
		b.WriteString(Pig(m.title))
		b.WriteString("\n")
	}

	for i, step := range m.steps {
		switch {
		case i < m.current:
			b.WriteString(fmt.Sprintf("  %s %s\n", Green.Render("✓"), step.Name))
		case i == m.current && m.err != nil:
			b.WriteString(fmt.Sprintf("  %s %s\n", Red.Render("✗"), step.Name))
		case i == m.current:
			status := step.getStatus()
			if status != "" {
				b.WriteString(fmt.Sprintf("  %s %s %s\n", m.spinner.View(), step.Name, Dim.Render(status)))
			} else {
				b.WriteString(fmt.Sprintf("  %s %s\n", m.spinner.View(), step.Name))
			}
		default:
			b.WriteString(fmt.Sprintf("  %s %s\n", Dim.Render("○"), Dim.Render(step.Name)))
		}
	}

	if m.err != nil {
		b.WriteString(fmt.Sprintf("\n  %s\n", Red.Render(m.err.Error())))
	}
	if m.done && m.summary != "" {
		b.WriteString("\n" + m.summary)
	}
	b.WriteString("\n")
	return b.String()
}

func (m StepsModel) Err() error   { return m.err }
func (m StepsModel) Done() bool   { return m.done }

func (m StepsModel) runCurrent() tea.Cmd {
	if m.current >= len(m.steps) {
		return nil
	}
	step := m.steps[m.current]
	return func() tea.Msg {
		return stepDoneMsg{err: step.Run()}
	}
}

// StepsOpt configures RunSteps behaviour.
type StepsOpt func(*StepsModel)

// WithSummary provides a function called after all steps succeed.
// Its return value is rendered below the step list.
func WithSummary(fn func() string) StepsOpt {
	return func(m *StepsModel) { m.summaryFn = fn }
}

// RunSteps runs a step-based TUI if stdout is a terminal,
// otherwise falls back to plain text output.
func RunSteps(title string, steps []*Step, opts ...StepsOpt) error {
	if !IsTTY() {
		return runStepsPlain(title, steps)
	}
	m := NewStepsModel(title, steps)
	for _, opt := range opts {
		opt(&m)
	}
	p := tea.NewProgram(m)
	final, err := p.Run()
	if err != nil {
		return err
	}
	return final.(StepsModel).Err()
}

func runStepsPlain(title string, steps []*Step) error {
	if title != "" {
		fmt.Fprintf(os.Stderr, "%s\n", title)
	}
	for _, step := range steps {
		fmt.Fprintf(os.Stderr, "  %s...\n", step.Name)
		if err := step.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "  FAILED: %s\n", err)
			return err
		}
	}
	return nil
}

func IsTTY() bool {
	return term.IsTerminal(int(os.Stdout.Fd()))
}
