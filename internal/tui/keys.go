package tui

import (
	"context"
	"path/filepath"
	"strconv"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/alhimowicz/setupvault/internal/vault"
)

func (m *Model) handleKey(msg tea.KeyPressMsg) tea.Cmd {
	key := msg.String()
	if m.confirmQuit {
		if key == "y" {
			if m.cancel != nil {
				m.cancel()
			}
			return tea.Quit
		}
		m.confirmQuit = false
		return nil
	}
	if key == "ctrl+c" {
		if m.busy {
			m.confirmQuit = true
			return nil
		}
		return tea.Quit
	}
	if m.form != nil {
		if key == "esc" {
			m.form = nil
			return nil
		}
		if key == "enter" {
			if m.form.focus < len(m.form.inputs)-1 {
				m.form.inputs[m.form.focus].Blur()
				m.form.focus++
				return m.form.inputs[m.form.focus].Focus()
			}
			return m.submit()
		}
		if key == "tab" || key == "shift+tab" {
			m.form.inputs[m.form.focus].Blur()
			step := 1
			if key == "shift+tab" {
				step = -1
			}
			m.form.focus = (m.form.focus + step + len(m.form.inputs)) % len(m.form.inputs)
			return m.form.inputs[m.form.focus].Focus()
		}
		var cmd tea.Cmd
		m.form.inputs[m.form.focus], cmd = m.form.inputs[m.form.focus].Update(msg)
		return cmd
	}
	if key == "q" {
		if m.busy {
			m.confirmQuit = true
			return nil
		}
		return tea.Quit
	}
	if key == "tab" || key == "shift+tab" {
		step := 1
		if key == "shift+tab" {
			step = -1
		}
		m.page = (m.page + step + len(pages)) % len(pages)
		m.cursor = 0
		return nil
	}
	if key == "esc" && m.busy {
		if m.cancel != nil {
			m.cancel()
		}
		m.message = "Canceling; waiting for the backup engine to stop"
		return nil
	}
	if m.busy {
		return nil
	}
	if key == "up" {
		m.cursor = max(0, m.cursor-1)
		return nil
	}
	if key == "down" {
		m.cursor++
		return nil
	}
	if key == "b" {
		return m.start("Preparing encrypted backup", func(ctx context.Context, a *vault.App, p func(vault.Progress)) (event, error) {
			return event{text: "Backup committed and sample restore verified"}, a.Backup(ctx, p)
		})
	}
	if key == "v" {
		return m.start("Checking repository and sample restore", func(ctx context.Context, a *vault.App, _ func(vault.Progress)) (event, error) {
			return event{text: "Repository and sample restore verified"}, a.Verify(ctx)
		})
	}
	switch m.page {
	case 1:
		switch key {
		case "d", "o":
			provider := "dropbox"
			if key == "o" {
				provider = "onedrive"
			}
			return m.start("Authorize "+provider+" in the browser that opens", func(ctx context.Context, a *vault.App, _ func(vault.Progress)) (event, error) {
				remote, err := a.Connect(ctx, provider)
				return event{connected: remote}, err
			})
		case "enter":
			if len(m.remotes) > 0 {
				m.cursor = min(m.cursor, len(m.remotes)-1)
				m.remote = m.remotes[m.cursor].Name
				return m.openForm("folder", "Folder in "+m.remotes[m.cursor].Type, []string{"Dedicated backup folder"}, []string{"PC-Setup-Backups/" + m.app.Profile.Name}, false)
			}
		case "l":
			return m.openForm("local", "Local backup destination", []string{"Absolute folder path"}, []string{filepath.Join(m.app.Profile.Home, "SetupVault-Backups")}, false)
		case "t":
			return m.connectionTest()
		}
	case 2:
		rows := m.contentRows()
		if len(rows) > 0 {
			m.cursor = min(m.cursor, len(rows)-1)
		}
		if key == "a" {
			return m.openForm("path", "Include a custom file or folder", []string{"Absolute path"}, []string{m.app.Profile.Home + "/"}, false)
		}
		if key == "s" {
			return m.start("Enable protected system configuration access", func(ctx context.Context, a *vault.App, _ func(vault.Progress)) (event, error) {
				return event{text: "System configuration export enabled; it runs as part of each backup."}, a.EnableSystem(ctx)
			})
		}
		if len(rows) > 0 {
			r := rows[m.cursor]
			if key == "delete" || key == "backspace" || key == "d" {
				m.removeCustomPath(r)
				return nil
			}
			if key == "enter" || key == "right" {
				if r.group {
					m.expanded[r.id] = !m.expanded[r.id]
				} else if r.dir {
					m.expanded[r.path] = !m.expanded[r.path]
				}
				return nil
			}
			if key == "space" || key == " " {
				if r.group {
					m.app.Toggle(r.id)
				} else {
					pattern := r.path
					if r.dir {
						pattern += "/**"
					}
					found := -1
					for i, p := range m.app.Profile.Excludes {
						if p == pattern {
							found = i
							break
						}
					}
					if found >= 0 {
						m.app.Profile.Excludes = append(m.app.Profile.Excludes[:found], m.app.Profile.Excludes[found+1:]...)
					} else {
						m.app.Profile.Excludes = append(m.app.Profile.Excludes, pattern)
					}
				}
				if err := m.app.Save(); err != nil {
					m.message = err.Error()
					m.failed = true
				} else {
					m.message = "Contents saved. Default exclusions remain visible in the expanded tree."
				}
				return nil
			}
		}
	case 3:
		if key == "r" {
			return m.loadSnapshots()
		}
		if key == "enter" && len(m.snapshots) > 0 {
			m.cursor = min(m.cursor, len(m.snapshots)-1)
			return m.openForm("restore", "Restore into a new, empty folder", []string{"Restore folder"}, []string{filepath.Join(m.app.Profile.Home, "SetupVault-Restore", time.Now().Format("20060102-150405"))}, false)
		}
	case 4:
		switch key {
		case "n":
			return m.openForm("name", "Computer/profile name", []string{"Name"}, []string{m.app.Profile.Name}, false)
		case "p":
			return m.openForm("password", "Recovery password — save it in Bitwarden", []string{"Password (12+ characters)", "Confirm password"}, []string{"", ""}, true)
		case "s":
			value := m.app.Profile.Schedule
			if value == "" {
				value = "daily"
			}
			return m.openForm("schedule", "Schedule (systemd calendar, or 'off')", []string{"Calendar expression"}, []string{value}, false)
		case "k":
			return m.openForm("keep", "Snapshot retention", []string{"Keep the latest N snapshots"}, []string{strconv.Itoa(m.app.Profile.KeepLast)}, false)
		case "x":
			return m.openForm("export", "Export an encrypted recovery profile", []string{"New recovery file"}, []string{filepath.Join(m.app.Profile.Home, "setupvault-recovery.age")}, false)
		case "i":
			cmd := m.openForm("import", "Import recovery profile (existing profile will not be overwritten)", []string{"Recovery file", "Password"}, []string{filepath.Join(m.app.Profile.Home, "setupvault-recovery.age"), ""}, false)
			m.form.inputs[1].EchoMode = textinput.EchoPassword
			return cmd
		case "e":
			return m.editProfile()
		}
	}
	return nil
}
