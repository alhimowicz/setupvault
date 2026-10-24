package tui

import (
	tea "charm.land/bubbletea/v2"
	"github.com/alhimowicz/setupvault/internal/vault"
	"github.com/charmbracelet/x/ansi"
	"strings"
	"testing"
)

func TestRecoveryQuitDetachesAndImportMovesToRestore(t *testing.T) {
	m := testModel(t)
	m.recovery = &vault.RecoveryJob{ID: strings.Repeat("a", 32), Status: "Running", Phase: "Installing apps"}
	next, cmd := m.Update(tea.KeyPressMsg{Code: 'q', Text: "q"})
	m = next.(Model)
	if cmd == nil || m.confirmQuit {
		t.Fatal("detached recovery quit required cancellation")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Fatal("quit did not detach")
	}
	next, _ = m.Update(event{done: true, imported: true, text: "imported"})
	m = next.(Model)
	if m.page != 3 || m.recovery != nil || !strings.Contains(m.message, "choose a backup") {
		t.Fatal("import did not lead to guided recovery")
	}
}

func TestRecoveryReviewAndDependencyNoticeFitTerminal(t *testing.T) {
	m := testModel(t)
	m.page = 3
	m.width = 80
	m.height = 24
	m.recovery = &vault.RecoveryJob{ID: strings.Repeat("b", 32), Status: "Ready", Phase: "Review changes", Groups: []string{"apps", "settings", "manual"}, Files: []vault.RecoveryFile{{Path: ".zshrc", Status: "Pending", Group: "settings", Before: "old", After: "new"}}}
	view := ansi.Strip(m.View().Content)
	if !strings.Contains(view, "Restore my setup") || !strings.Contains(view, "terminal and shell") || !strings.Contains(view, "q quit") {
		t.Fatal("recovery review lacks essential controls", view)
	}
	m.page = 1
	m.connectorError = "rclone is required for cloud connections"
	view = ansi.Strip(m.View().Content)
	if !strings.Contains(view, "Install rclone") || !strings.Contains(view, "Local-folder backups remain available") {
		t.Fatal("cloud dependency is not actionable", view)
	}
}
