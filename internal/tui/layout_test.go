package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/alhimowicz/setupvault/internal/vault"
	"github.com/charmbracelet/x/ansi"
)

func withoutSpace(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return -1
		}
		return r
	}, ansi.Strip(s))
}

func assertFits(t *testing.T, m Model) string {
	t.Helper()
	view := ansi.Strip(m.View().Content)
	for _, line := range strings.Split(view, "\n") {
		if ansi.StringWidth(line) > m.width {
			t.Fatalf("%dx%d line overflow: %q", m.width, m.height, line)
		}
	}
	if len(strings.Split(view, "\n")) > m.height {
		t.Fatalf("%dx%d height overflow:\n%s", m.width, m.height, view)
	}
	return view
}

func longLayoutFixture(t *testing.T) Model {
	t.Helper()
	m := testModel(t)
	m.app.Profile.Name = "PROFILE-START-" + strings.Repeat("界🦉é", 20) + "-PROFILE-END"
	m.app.Profile.Repository = "rclone:fixture:" + strings.Repeat("nested/", 30) + "REPOSITORY-END"
	m.app.Profile.CustomPaths = []string{filepath.Join(m.app.Profile.Home, strings.Repeat("directory", 20), "CUSTOM-END")}
	m.remotes = []vault.Remote{{Type: "dropbox", Name: strings.Repeat("connection", 20) + "-REMOTE-END"}}
	m.snapshots = []vault.Snapshot{{ID: strings.Repeat("a", 64), Hostname: strings.Repeat("hostname", 20) + "-HOST-END"}}
	m.next = strings.Repeat("next scheduled run ", 10) + "NEXT-END"
	m.message = "MESSAGE-START " + strings.Repeat("unbrokentoken", 20) + " MESSAGE-END"
	m.connectorError = "DEPENDENCY-START " + strings.Repeat("explanation ", 20) + "DEPENDENCY-END"
	m.progress.Phase = "PROGRESS-START " + strings.Repeat("loading details ", 20) + "PROGRESS-END"
	if err := os.MkdirAll(m.app.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(m.app.StateDir, "activity.log"), []byte("LOG-START\t"+strings.Repeat("path/", 40)+"\tLOG-END\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAllPagesWrapEveryTextBlockWithoutCropping(t *testing.T) {
	for _, width := range []int{20, 28, 40, 60, 80, 120} {
		m := longLayoutFixture(t)
		m.width, m.height = width, 500
		for page := range pages {
			m.page = page
			view := assertFits(t, m)
			expected := []string{m.app.Profile.Name, m.message}
			switch page {
			case 0:
				expected = append(expected, m.app.Profile.Repository, m.next)
			case 1:
				expected = append(expected, m.remotes[0].Name, m.connectorError, "Cloud sign-in opens your browser; the connection test writes and reads a tiny probe.")
			case 2:
				expected = append(expected, m.app.Profile.CustomPaths[0], "App settings can live outside common config folders. Add explicit paths when needed.", "Documents, browser profiles, caches and local mail are excluded by default.")
			case 3:
				expected = append(expected, m.snapshots[0].Hostname, "System configuration is staged for selective administrator review.")
			case 4:
				expected = append(expected, m.app.ProfilePath(), "Scheduled backups need an unlocked desktop keyring and an active user session.")
			case 5:
				expected = append(expected, "LOG-START", strings.Repeat("path/", 40), "LOG-END")
			}
			for _, text := range expected {
				if !strings.Contains(withoutSpace(view), withoutSpace(text)) {
					t.Fatalf("%s lost text at width %d: %q\n%s", pages[page], width, text, view)
				}
			}
		}
		m.busy, m.confirmQuit = true, true
		view := assertFits(t, m)
		for _, text := range []string{m.progress.Phase, "A task is running. Cancel it and quit? y / Esc to stay"} {
			if !strings.Contains(withoutSpace(view), withoutSpace(text)) {
				t.Fatal("progress or confirmation was cropped", width)
			}
		}
	}
}

func TestRecoverySubpagesWrapLongDetails(t *testing.T) {
	for _, width := range []int{20, 28, 50, 80} {
		m := longLayoutFixture(t)
		m.width, m.height, m.page = width, 500, 3
		long := strings.Repeat("long-value-界/", 30) + "DETAIL-END"
		m.recovery = &vault.RecoveryJob{
			ID: strings.Repeat("b", 32), Status: "Ready", Phase: long, Error: long,
			Groups: recoveryGroups, Notices: []string{long},
			Packages: []vault.RecoveryPackage{{Name: long, Scope: "user", Origin: "fixture", URL: "https://example.com/" + long, Key: long, AddRemote: true, Selected: true, Reason: long}},
			Files:    []vault.RecoveryFile{{Path: long, Group: "settings", Reason: long}},
			Keys:     []vault.RecoveryKey{{Path: long, Reason: long}},
		}
		for _, mode := range []string{"groups", "apps", "files", "sources", "details", "undo"} {
			m.recoveryMode = mode
			view := assertFits(t, m)
			if !strings.Contains(withoutSpace(view), withoutSpace(long)) {
				t.Fatal("recovery text was cropped", width, mode)
			}
			if mode == "sources" && !strings.Contains(withoutSpace(view), withoutSpace(m.recovery.Packages[0].URL)) {
				t.Fatal("source URL was cropped", width)
			}
			if mode == "details" && !strings.Contains(withoutSpace(view), withoutSpace(filepath.Join(m.app.StateDir, "recovery", m.recovery.ID, "job.json"))) {
				t.Fatal("report path was cropped", width)
			}
		}
	}
}

func TestSmallWindowsCanPageThroughAllContentIncludingForms(t *testing.T) {
	marker := regexp.MustCompile(`ROW\d{3}_END`)
	for _, size := range [][2]int{{20, 6}, {24, 8}, {32, 10}, {40, 12}, {60, 16}, {80, 24}} {
		for _, form := range []bool{false, true} {
			m := testModel(t)
			m.page, m.width, m.height = 2, size[0], size[1]
			if form {
				m.openForm("fixture", "Scrollable form", []string{"Field"}, []string{"unchanged"}, false)
			}
			var text strings.Builder
			for i := range 80 {
				fmt.Fprintf(&text, "ROW%03d_END\n", i)
			}
			m.message = text.String()
			seen := map[string]bool{}
			for range 200 {
				view := assertFits(t, m)
				if !strings.Contains(view, "q quit") || !strings.Contains(view, "Contents") {
					t.Fatal("small window lost header or navigation", size, view)
				}
				for _, value := range marker.FindAllString(view, -1) {
					seen[value] = true
				}
				before := m.scroll
				m.handleKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
				if before == m.scroll {
					break
				}
			}
			if len(seen) != 80 {
				t.Fatal("some text cannot be reached by scrolling", size, form, len(seen))
			}
			if form && m.form.inputs[0].Value() != "unchanged" {
				t.Fatal("paging changed the field")
			}
		}
	}
}

func TestResizingOpenFormKeepsValuesAndFocusedFieldVisible(t *testing.T) {
	m := testModel(t)
	value := strings.Repeat("secret-value-界", 30)
	m.openForm("password", "Password settings", []string{"Password", "Confirm password"}, []string{value, value}, true)
	for _, size := range [][2]int{{80, 24}, {24, 10}, {20, 8}, {60, 16}} {
		next, _ := m.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		m = next.(Model)
		next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyTab})
		m = next.(Model)
		view := assertFits(t, m)
		if !strings.Contains(view, "*") || strings.Contains(view, "secret-value") {
			t.Fatal("focused password field is hidden or exposed", size, view)
		}
		for _, in := range m.form.inputs {
			if in.Value() != value || in.Width() > m.contentWidth()-3 {
				t.Fatal("resize lost the value or left the old field width", size)
			}
		}
	}
}

func TestNarrowListSelectionRemainsVisible(t *testing.T) {
	m := testModel(t)
	m.width, m.height, m.page = 32, 10, 2
	for i := range 30 {
		m.app.Profile.CustomPaths = append(m.app.Profile.CustomPaths, filepath.Join(m.app.Profile.Home, fmt.Sprintf("custom-%02d", i), strings.Repeat("long-path/", 8)))
	}
	for range len(m.contentRows()) - 1 {
		next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
		m = next.(Model)
		if !strings.Contains(assertFits(t, m), "> ") {
			t.Fatalf("selected wrapped row scrolled out of view: cursor=%d scroll=%d\n%s", m.cursor, m.scroll, ansi.Strip(m.View().Content))
		}
	}
}

func TestQuitConfirmationCanScrollBeforeAnswering(t *testing.T) {
	m := testModel(t)
	m.width, m.height = 20, 8
	m.busy, m.confirmQuit = true, true
	m.message = strings.Repeat("Details of the current operation. ", 20)
	m.scroll = m.maxScroll()
	before := m.scroll
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyPgUp})
	if !m.confirmQuit || m.scroll >= before || m.quitting {
		t.Fatal("scrolling dismissed or answered the quit confirmation")
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyPgDown})
	if !m.confirmQuit || m.scroll != before || m.quitting {
		t.Fatal("confirmation did not remain scrollable")
	}
	m.handleKey(tea.KeyPressMsg{Code: tea.KeyEscape})
	if m.confirmQuit || m.quitting || !m.busy {
		t.Fatal("Escape did not keep the operation running")
	}
}
