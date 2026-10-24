package tui

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/alhimowicz/setupvault/internal/vault"
)

func (m *Model) openForm(kind, title string, labels, values []string, password bool) tea.Cmd {
	f := &form{kind: kind, title: title, labels: labels}
	for i, value := range values {
		in := textinput.New()
		in.SetWidth(max(20, min(m.width-12, 88)))
		in.CharLimit = 4096
		in.SetValue(value)
		if password {
			in.EchoMode = textinput.EchoPassword
		}
		if i == 0 {
			in.Focus()
		}
		f.inputs = append(f.inputs, in)
	}
	m.form = f
	return textinput.Blink
}

func (m *Model) submit() tea.Cmd {
	f := m.form
	values := make([]string, len(f.inputs))
	for i := range f.inputs {
		values[i] = f.inputs[i].Value()
	}
	m.form = nil
	previous := m.app.Profile
	save := func() {
		if err := m.app.Save(); err != nil {
			m.app.Profile = previous
			m.message = err.Error()
			m.failed = true
		} else {
			m.message = "Saved"
			m.failed = false
		}
	}
	switch f.kind {
	case "password":
		if values[0] != values[1] {
			m.message = "Passwords do not match"
			m.failed = true
			return nil
		}
		password := values[0]
		return m.start("Saving password in the desktop keyring", func(ctx context.Context, a *vault.App, _ func(vault.Progress)) (event, error) {
			err := a.StorePassword(ctx, password)
			if err == nil {
				err = a.Save()
			}
			return event{text: "Recovery password saved in the desktop keyring. Save it in Bitwarden too."}, err
		})
	case "folder":
		folder := strings.Trim(values[0], "/")
		if folder == "" || strings.Contains(folder, "..") || strings.ContainsAny(folder, "\r\n\x00") {
			m.message = "Choose a dedicated cloud folder without '..'"
			m.failed = true
			return nil
		}
		m.app.Profile.Repository = "rclone:" + m.remote + ":" + folder
		save()
		if m.failed {
			return nil
		}
		return m.connectionTest()
	case "local":
		path := expand(values[0])
		if !filepath.IsAbs(path) {
			m.message = "Choose an absolute local folder"
			m.failed = true
			return nil
		}
		m.app.Profile.Repository = path
		save()
		if m.failed {
			return nil
		}
		return m.connectionTest()
	case "name":
		m.app.Profile.Name = values[0]
		save()
	case "keep":
		n, err := strconv.Atoi(values[0])
		if err != nil || n < 1 || n > 10000 {
			m.message = "Keep between 1 and 10000 snapshots"
			m.failed = true
			return nil
		}
		m.app.Profile.KeepLast = n
		save()
	case "schedule":
		calendar := values[0]
		if strings.EqualFold(calendar, "off") {
			calendar = ""
		}
		return m.start("Updating the schedule", func(ctx context.Context, a *vault.App, _ func(vault.Progress)) (event, error) {
			return event{text: "Schedule updated"}, a.SetSchedule(ctx, calendar)
		})
	case "path":
		path := expand(values[0])
		if !filepath.IsAbs(path) {
			m.message = "Custom paths must be absolute"
			m.failed = true
			return nil
		}
		if _, err := os.Lstat(path); err != nil {
			m.message = err.Error()
			m.failed = true
			return nil
		}
		m.app.Profile.CustomPaths = append(m.app.Profile.CustomPaths, path)
		save()
	case "export":
		path := expand(values[0])
		return m.start("Encrypting recovery profile", func(ctx context.Context, a *vault.App, _ func(vault.Progress)) (event, error) {
			return event{text: "Encrypted recovery profile saved to " + path}, a.Export(ctx, path)
		})
	case "import":
		path, password := expand(values[0]), values[1]
		return m.start("Unlocking recovery profile", func(ctx context.Context, a *vault.App, _ func(vault.Progress)) (event, error) {
			return event{text: "Recovery profile imported. Review Contents; scheduling remains off. Reconnect your cloud account if needed."}, a.Import(ctx, path, password)
		})
	case "restore":
		path := expand(values[0])
		if m.cursor >= len(m.snapshots) {
			return nil
		}
		snapshot := m.snapshots[m.cursor].ID
		return m.start("Restoring files to "+path, func(ctx context.Context, a *vault.App, _ func(vault.Progress)) (event, error) {
			return event{text: "Restored to " + path + ". Review settings before applying them to a different PC."}, a.Restore(ctx, snapshot, path)
		})
	}
	return m.refresh()
}

func expand(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		h, _ := os.UserHomeDir()
		return filepath.Join(h, strings.TrimPrefix(path, "~/"))
	}
	return filepath.Clean(path)
}
