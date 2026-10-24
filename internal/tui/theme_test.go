package tui

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/alhimowicz/setupvault/internal/vault"
	"github.com/charmbracelet/x/ansi"
)

var sgr = regexp.MustCompile("\x1b\\[([0-9;:]*)m")

func checkThemeColors(t *testing.T, view string, monochrome bool) {
	t.Helper()
	for _, match := range sgr.FindAllStringSubmatch(view, -1) {
		for _, value := range strings.FieldsFunc(match[1], func(r rune) bool { return r == ';' || r == ':' }) {
			n, err := strconv.Atoi(value)
			if err != nil {
				t.Fatal(err)
			}
			if n == 38 || n == 48 || n == 58 {
				t.Fatalf("extended/RGB color bypasses terminal palette: %q", match[0])
			}
			if n >= 40 && n <= 47 || n >= 100 && n <= 107 {
				t.Fatalf("explicit background overrides the terminal: %q", match[0])
			}
			if monochrome && (n >= 30 && n <= 37 || n >= 90 && n <= 97) {
				t.Fatalf("color leaked into monochrome: %q", match[0])
			}
		}
	}
}

func TestThemeCoversScreensAndInputStates(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("NO_COLOR", "")
	for _, theme := range []string{"terminal", "monochrome"} {
		m := testModel(t)
		m.app.Profile.Theme = theme
		m.applyTheme()
		m.height = 80
		m.failed, m.busy = true, true
		m.message = "FIXTURE-ERROR"
		m.connectorError = "rclone is required"
		for page := range pages {
			m.page = page
			view := m.View().Content
			checkThemeColors(t, view, theme == "monochrome")
			plain := ansi.Strip(view)
			if !strings.Contains(plain, "["+pages[page]+"]") || !strings.Contains(plain, "Error: FIXTURE-ERROR") {
				t.Fatal("selection or error depends on color", plain)
			}
		}
		m.openForm("fixture", "Theme input", []string{"Focused", "Blurred"}, []string{"value", "value"}, false)
		m.form.inputs[0].Placeholder = "Placeholder"
		m.form.inputs[1].Placeholder = "Placeholder"
		for _, value := range []string{"value", ""} {
			for i := range m.form.inputs {
				m.form.inputs[i].SetValue(value)
			}
			checkThemeColors(t, m.View().Content, theme == "monochrome")
		}
	}
}

func TestNoColorUsesAnyNonemptyValue(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("CLICOLOR_FORCE", "1")
	for _, value := range []string{"1", "0", "false", "custom-value"} {
		t.Setenv("NO_COLOR", value)
		m := testModel(t)
		m.page = 4
		m.busy, m.failed = true, true
		m.message = "FAILED"
		checkThemeColors(t, m.View().Content, true)
		if !strings.Contains(m.themeLabel(), "NO_COLOR") {
			t.Fatal("environment override was hidden")
		}
		m.openForm("name", "Name", []string{"Name"}, []string{"example"}, false)
		checkThemeColors(t, m.View().Content, true)
	}
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "dumb")
	m := testModel(t)
	checkThemeColors(t, m.View().Content, true)
	if !strings.Contains(m.themeLabel(), "TERM=dumb") {
		t.Fatal("dumb-terminal override was hidden")
	}
}

func TestThemePreferencePersistsAndFailedSaveKeepsStyles(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	m := testModel(t)
	m.page = 4
	m.handleKey(tea.KeyPressMsg{Code: 't', Text: "t"})
	a, err := vault.New(m.app.ConfigDir, m.app.StateDir)
	if err != nil || a.Profile.Theme != "monochrome" {
		t.Fatal("theme selection was not saved", err)
	}
	m = New(a)
	checkThemeColors(t, m.View().Content, true)
	m.page = 4
	m.handleKey(tea.KeyPressMsg{Code: 't', Text: "t"})
	a, err = vault.New(m.app.ConfigDir, m.app.StateDir)
	if err != nil || a.Profile.Theme != "terminal" {
		t.Fatal("terminal theme could not be restored", err)
	}
	before := m.styles.heading.Render("heading")
	blocker := filepath.Join(t.TempDir(), "regular-file")
	if err = os.WriteFile(blocker, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	m.app.ConfigDir = blocker
	m.toggleTheme()
	if !m.failed || m.app.Profile.Theme != "terminal" || m.styles.heading.Render("heading") != before {
		t.Fatal("failed theme save changed the active preference")
	}
}

func TestProfileReloadReappliesThemeToOpenInputs(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "xterm-256color")
	m := testModel(t)
	m.openForm("name", "Name", []string{"Name"}, []string{"example"}, false)
	m.app.Profile.Theme = "monochrome"
	if err := m.app.Save(); err != nil {
		t.Fatal(err)
	}
	next, _ := m.Update(editorMsg{})
	m = next.(Model)
	checkThemeColors(t, m.View().Content, true)
}
