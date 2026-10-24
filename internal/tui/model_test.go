package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/alhimowicz/setupvault/internal/vault"
)

func testModel(t *testing.T) Model {
	t.Helper()
	dir := t.TempDir()
	a, err := vault.New(filepath.Join(dir, "config"), filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	a.Profile.Home = filepath.Join(dir, "home")
	a.Profile.Excludes = vault.DefaultExclusions(a.Profile.Home)
	return New(a)
}

func TestSettingsShortcutAndPasswordPaste(t *testing.T) {
	m := testModel(t)
	m.page = 4
	next, _ := m.Update(tea.KeyPressMsg{Code: 'k', Text: "k"})
	m = next.(Model)
	if m.form == nil || m.form.kind != "keep" {
		t.Fatal("retention shortcut did not open")
	}
	m.form = nil
	next, _ = m.Update(tea.KeyPressMsg{Code: 'p', Text: "p"})
	m = next.(Model)
	const password = "a-pasted-test-password"
	next, _ = m.Update(tea.PasteMsg{Content: password})
	m = next.(Model)
	if m.form.inputs[0].Value() != password {
		t.Fatal("password paste lost")
	}
	if strings.Contains(m.View().Content, password) {
		t.Fatal("password visible")
	}
}

func TestContentsShowsExcludedAndDeselectedPaths(t *testing.T) {
	m := testModel(t)
	config := filepath.Join(m.app.Profile.Home, ".config/example")
	if err := os.MkdirAll(config, 0700); err != nil {
		t.Fatal(err)
	}
	m.app.Profile.Selected = nil
	m.expanded["settings"] = true
	for _, r := range m.contentRows() {
		if r.path == filepath.Dir(config) && !r.excluded {
			t.Fatal("disabled contents displayed as included")
		}
	}
	if !excluded(filepath.Join(config, "cache"), []string{"**/cache/**"}, true) {
		t.Fatal("cache folder not marked excluded")
	}
}

func TestFailureVisible(t *testing.T) {
	m := testModel(t)
	next, _ := m.Update(event{done: true, err: os.ErrPermission})
	m = next.(Model)
	if !m.failed || !strings.Contains(m.View().Content, "permission denied") {
		t.Fatal("failure hidden")
	}
}
