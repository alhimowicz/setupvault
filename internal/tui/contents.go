package tui

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/bmatcuk/doublestar/v4"
)

func excluded(path string, patterns []string, dir bool) bool {
	for _, p := range patterns {
		candidate := path
		if !strings.Contains(p, "/") {
			candidate = filepath.Base(path)
		}
		if ok, _ := doublestar.PathMatch(p, candidate); ok {
			return true
		}
		if dir {
			if ok, _ := doublestar.PathMatch(p, candidate+"/"); ok {
				return true
			}
		}
	}
	return false
}

func (m Model) contentRows() []row {
	var rows []row
	var add func(string, int, bool)
	add = func(path string, depth int, enabled bool) {
		info, err := os.Lstat(path)
		dir := err == nil && info.IsDir()
		rows = append(rows, row{path: path, label: filepath.Base(path), depth: depth, dir: dir, excluded: !enabled || excluded(path, m.app.Exclusions(), dir)})
		if dir && m.expanded[path] {
			entries, err := os.ReadDir(path)
			if err != nil {
				return
			}
			for _, e := range entries {
				add(filepath.Join(path, e.Name()), depth+1, enabled)
			}
		}
	}
	for _, g := range m.app.Groups() {
		rows = append(rows, row{id: g.ID, label: g.Name, group: true})
		if m.expanded[g.ID] {
			for _, p := range g.Paths {
				add(p, 1, g.Selected)
			}
		}
	}
	for _, p := range m.app.Profile.CustomPaths {
		start := len(rows)
		add(p, 0, true)
		rows[start].custom = true
	}
	return rows
}

func (m *Model) removeCustomPath(r row) {
	if !r.custom {
		m.message = "Select a [custom] path to remove; Space excludes preset files and folders."
		return
	}
	previous := m.app.Profile.CustomPaths
	var paths []string
	for _, path := range previous {
		if path != r.path {
			paths = append(paths, path)
		}
	}
	m.app.Profile.CustomPaths = paths
	if err := m.app.Save(); err != nil {
		m.app.Profile.CustomPaths = previous
		m.message = err.Error()
		m.failed = true
		return
	}
	m.cursor = max(0, min(m.cursor, len(m.contentRows())-1))
	m.message = "Removed from backup selection: " + r.path
	m.failed = false
}
