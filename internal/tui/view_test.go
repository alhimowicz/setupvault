package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/alhimowicz/setupvault/internal/vault"
	"github.com/charmbracelet/x/ansi"
)

func TestLongNoticesWrapWithoutDroppingText(t *testing.T) {
	for _, width := range []int{28, 50, 80} {
		for _, failed := range []bool{false, true} {
			m := testModel(t)
			m.width = width
			m.height = 100
			m.refreshing = false
			m.failed = failed
			m.message = "NOTICESTART A long message with a path /" + strings.Repeat("verylongsegment", 12) + " and Unicode 世界界界. NOTICEEND"
			view := ansi.Strip(m.View().Content)
			for _, line := range strings.Split(view, "\n") {
				if ansi.StringWidth(line) > width {
					t.Fatalf("line overflow at width %d: %q", width, line)
				}
			}
			start := strings.Index(view, "NOTICESTART")
			end := strings.Index(view, "NOTICEEND")
			if start < 0 || end < 0 {
				t.Fatal("notice was clipped", view)
			}
			stripSpace := func(s string) string {
				return strings.Map(func(r rune) rune {
					if unicode.IsSpace(r) {
						return -1
					}
					return r
				}, s)
			}
			if stripSpace(view[start:end+len("NOTICEEND")]) != stripSpace(m.message) {
				t.Fatal("wrapped notice lost text", view)
			}
		}
	}
}

func TestSavedFailureReasonVisibleAfterRestart(t *testing.T) {
	m := testModel(t)
	m.width = 50
	message := "Repository busy: " + strings.Repeat("long/path/", 12) + " OWNER-PID-1234"
	data, err := json.Marshal(vault.State{Repository: m.app.Profile.Repository, Status: "Verification failed", LastError: message})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(m.app.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.app.StateDir, "state.json"), data, 0600); err != nil {
		t.Fatal(err)
	}
	m = New(m.app)
	m.width = 50
	m.height = 50
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "Last issue:") || !strings.Contains(view, "OWNER-PID-1234") {
		t.Fatal("saved failure reason hidden after restart", view)
	}
	for _, line := range strings.Split(view, "\n") {
		if ansi.StringWidth(line) > m.width {
			t.Fatal("saved failure overflowed the terminal", line)
		}
	}
}

func TestSmallTerminalKeepsHelpVisibleAndCanScrollLongError(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 80, 24
	m.message = "ERRORSTART " + strings.Repeat("A long warning to read fully. ", 80) + " ERROREND"
	m.failed = true
	var sawStart, sawEnd bool
	for range 20 {
		view := ansi.Strip(m.View().Content)
		if !strings.Contains(view, "q quit") || !strings.Contains(view, "PgUp/PgDn") {
			t.Fatal("footer hidden in a small terminal", view)
		}
		if !strings.Contains(view, "SetupVault") || !strings.Contains(view, "Overview") {
			t.Fatal("header scrolled out of view", view)
		}
		if len(strings.Split(view, "\n")) > m.height {
			t.Fatal("view overflowed terminal height")
		}
		sawStart = sawStart || strings.Contains(view, "ERRORSTART")
		sawEnd = sawEnd || strings.Contains(view, "ERROREND")
		m.handleKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
	}
	if !sawStart || !sawEnd {
		t.Fatal("could not scroll through the complete error")
	}
	for range 20 {
		m.handleKey(tea.KeyPressMsg{Code: tea.KeyPgUp})
	}
	if m.scroll != 0 || !strings.Contains(ansi.Strip(m.View().Content), "SetupVault") {
		t.Fatal("could not scroll back to the top")
	}
}

func TestActivityWrapsLongLoggedError(t *testing.T) {
	m := testModel(t)
	m.page, m.width, m.height = 5, 50, 100
	if err := os.MkdirAll(m.app.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.app.StateDir, "activity.log"), []byte("LOGSTART "+strings.Repeat("long/path/", 30)+" LOGEND\n"), 0600); err != nil {
		t.Fatal(err)
	}
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "LOGSTART") || !strings.Contains(view, "LOGEND") {
		t.Fatal("logged error was clipped", view)
	}
}
