package tui

import (
	"os"
	"path/filepath"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/alhimowicz/setupvault/internal/vault"
)

func TestRemoveCustomPathKeepsFilesAndPersistsSelection(t *testing.T) {
	m := testModel(t)
	m.page = 2
	path := filepath.Join(t.TempDir(), "settings.txt")
	if err := os.WriteFile(path, []byte("keep this file"), 0600); err != nil {
		t.Fatal(err)
	}
	m.app.Profile.CustomPaths = []string{path}
	for i, r := range m.contentRows() {
		if r.custom && r.path == path {
			m.cursor = i
		}
	}
	next, _ := m.Update(tea.KeyPressMsg{Code: 'd', Text: "d"})
	m = next.(Model)
	if len(m.app.Profile.CustomPaths) != 0 {
		t.Fatal("custom path not removed")
	}
	a, err := vault.New(m.app.ConfigDir, m.app.StateDir)
	if err != nil || len(a.Profile.CustomPaths) != 0 {
		t.Fatal("removal not saved", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "keep this file" {
		t.Fatal("source file changed", err)
	}
	for _, r := range m.contentRows() {
		if r.custom && r.path == path {
			t.Fatal("removed path still displayed")
		}
	}
}

func TestRemoveCustomPathRollbackAndPresetProtection(t *testing.T) {
	m := testModel(t)
	path := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	m.app.Profile.CustomPaths = []string{path}
	m.removeCustomPath(row{path: path})
	if len(m.app.Profile.CustomPaths) != 1 {
		t.Fatal("preset removal changed custom paths")
	}
	m.app.ConfigDir = path
	m.removeCustomPath(row{path: path, custom: true})
	if !m.failed || len(m.app.Profile.CustomPaths) != 1 {
		t.Fatal("failed save did not restore selection")
	}
}
