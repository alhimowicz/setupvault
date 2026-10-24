package tui

import (
	"os"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type themeStyles struct {
	heading, muted, warning, failure, healthy, selected lipgloss.Style
	input                                               textinput.Styles
}

func newTheme(name string) themeStyles {
	plain := lipgloss.NewStyle()
	bold := plain.Bold(true)
	s := themeStyles{
		heading: bold, muted: plain, warning: bold, failure: bold, healthy: bold,
		selected: bold.Reverse(true),
	}
	if name != "monochrome" && os.Getenv("NO_COLOR") == "" && os.Getenv("TERM") != "dumb" {
		// Basic ANSI slots retain the terminal's configured palette.
		s.heading = bold.Foreground(lipgloss.Color("6"))
		s.warning = bold.Foreground(lipgloss.Color("3"))
		s.failure = bold.Foreground(lipgloss.Color("1"))
		s.healthy = bold.Foreground(lipgloss.Color("2"))
	}
	s.input = textinput.Styles{
		Focused: textinput.StyleState{Text: plain, Prompt: s.heading, Placeholder: plain, Suggestion: plain},
		Blurred: textinput.StyleState{Text: plain, Prompt: plain, Placeholder: plain, Suggestion: plain},
		Cursor:  textinput.CursorStyle{Shape: tea.CursorBlock, Blink: true},
	}
	return s
}

func (m *Model) applyTheme() {
	m.styles = newTheme(m.app.Profile.Theme)
	m.spinner.Style = m.styles.heading
	if m.form != nil {
		for i := range m.form.inputs {
			m.form.inputs[i].SetStyles(m.styles.input)
		}
	}
}

func (m Model) themeLabel() string {
	if m.app.Profile.Theme == "monochrome" {
		return "Monochrome"
	}
	if os.Getenv("NO_COLOR") != "" {
		return "Terminal (NO_COLOR: monochrome)"
	}
	if os.Getenv("TERM") == "dumb" {
		return "Terminal (TERM=dumb: monochrome)"
	}
	return "Terminal"
}

func (m *Model) toggleTheme() {
	previous := m.app.Profile.Theme
	m.app.Profile.Theme = "monochrome"
	if previous == "monochrome" {
		m.app.Profile.Theme = "terminal"
	}
	if err := m.app.Save(); err != nil {
		m.app.Profile.Theme = previous
		m.message, m.failed = err.Error(), true
		return
	}
	m.applyTheme()
	m.message, m.failed = "Theme saved: "+m.themeLabel(), false
}
