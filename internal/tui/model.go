package tui

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/alhimowicz/setupvault/internal/vault"
	"github.com/bmatcuk/doublestar/v4"
)

var pages = []string{"Overview", "Connections", "Contents", "Restore", "Settings", "Activity"}
var heading = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("81"))
var muted = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
var warning = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
var healthy = lipgloss.NewStyle().Foreground(lipgloss.Color("78"))

type event struct {
	text      string
	err       error
	done      bool
	progress  vault.Progress
	remotes   []vault.Remote
	snapshots []vault.Snapshot
	quota     *vault.Quota
	next      string
	connected string
}
type refreshMsg event
type tickMsg time.Time
type editorMsg struct{ err error }
type row struct {
	label, path, id      string
	depth                int
	group, dir, excluded bool
}
type form struct {
	kind, title string
	labels      []string
	inputs      []textinput.Model
	focus       int
}

type Model struct {
	app                         *vault.App
	page, cursor, width, height int
	expanded                    map[string]bool
	message                     string
	failed, busy, confirmQuit   bool
	progress                    vault.Progress
	cancel                      context.CancelFunc
	events                      chan event
	remotes                     []vault.Remote
	snapshots                   []vault.Snapshot
	quota                       *vault.Quota
	next                        string
	form                        *form
	remote                      string
}

func New(a *vault.App) Model {
	return Model{app: a, width: 100, height: 30, next: "Checking schedule…", expanded: map[string]bool{}, message: "Choose a connection, review Contents, and set a recovery password to begin."}
}
func (m Model) Init() tea.Cmd { return tea.Batch(m.refresh(), tick()) }
func tick() tea.Cmd           { return tea.Tick(15*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }) }

func (m Model) refresh() tea.Cmd {
	a := *m.app
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		e := event{next: a.NextRun(ctx)}
		e.remotes, _ = a.Remotes(ctx)
		if q, err := a.Quota(ctx); err == nil {
			e.quota = &q
		}
		return refreshMsg(e)
	}
}

func waitEvent(ch <-chan event) tea.Cmd {
	return func() tea.Msg {
		e, ok := <-ch
		if !ok {
			return event{done: true}
		}
		return e
	}
}

func (m *Model) start(label string, fn func(context.Context, *vault.App, func(vault.Progress)) (event, error)) tea.Cmd {
	m.busy = true
	m.failed = false
	m.message = label
	m.progress = vault.Progress{Phase: label}
	m.events = make(chan event, 16)
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	worker := *m.app
	ch := m.events
	go func() {
		e, err := fn(ctx, &worker, func(p vault.Progress) {
			select {
			case ch <- event{progress: p}:
			default:
			}
		})
		e.done = true
		e.err = err
		ch <- e
		close(ch)
	}()
	return waitEvent(ch)
}

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

func (m *Model) connectionTest() tea.Cmd {
	return m.start("Testing destination write and read", func(ctx context.Context, a *vault.App, _ func(vault.Progress)) (event, error) {
		return event{text: "Destination write/read test passed. Set a password in Settings before the first backup."}, a.TestConnection(ctx)
	})
}
func (m *Model) loadSnapshots() tea.Cmd {
	return m.start("Reading encrypted backup history", func(ctx context.Context, a *vault.App, _ func(vault.Progress)) (event, error) {
		s, err := a.Snapshots(ctx)
		return event{text: fmt.Sprintf("%d snapshots available; choose one to restore", len(s)), snapshots: s}, err
	})
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tickMsg:
		return m, tea.Batch(tick(), m.refresh())
	case refreshMsg:
		m.remotes = msg.remotes
		m.quota = msg.quota
		m.next = msg.next
		return m, nil
	case editorMsg:
		if msg.err != nil {
			m.message = msg.err.Error()
			m.failed = true
			return m, nil
		}
		a, err := vault.New(m.app.ConfigDir, m.app.StateDir)
		if err != nil {
			m.message = err.Error()
			m.failed = true
		} else {
			m.app = a
			m.message = "Profile reloaded"
		}
		return m, m.refresh()
	case event:
		if !msg.done {
			m.progress = msg.progress
			return m, waitEvent(m.events)
		}
		m.busy = false
		m.progress = vault.Progress{}
		if m.cancel != nil {
			m.cancel()
			m.cancel = nil
		}
		if msg.err != nil {
			m.message = msg.err.Error()
			m.failed = true
			return m, m.refresh()
		}
		m.message = msg.text
		m.failed = false
		if a, err := vault.New(m.app.ConfigDir, m.app.StateDir); err == nil {
			m.app = a
		}
		if msg.snapshots != nil {
			m.snapshots = msg.snapshots
			m.cursor = max(0, len(m.snapshots)-1)
		}
		if msg.connected != "" {
			m.remote = msg.connected
			return m, m.openForm("folder", "Folder in your cloud account", []string{"Dedicated backup folder"}, []string{"PC-Setup-Backups/" + m.app.Profile.Name}, false)
		}
		return m, m.refresh()
	case tea.KeyPressMsg:
		key := msg.String()
		if m.confirmQuit {
			if key == "y" {
				if m.cancel != nil {
					m.cancel()
				}
				return m, tea.Quit
			}
			m.confirmQuit = false
			return m, nil
		}
		if key == "ctrl+c" {
			if m.busy {
				m.confirmQuit = true
				return m, nil
			}
			return m, tea.Quit
		}
		if m.form != nil {
			if key == "esc" {
				m.form = nil
				return m, nil
			}
			if key == "enter" {
				if m.form.focus < len(m.form.inputs)-1 {
					m.form.inputs[m.form.focus].Blur()
					m.form.focus++
					return m, m.form.inputs[m.form.focus].Focus()
				}
				return m, m.submit()
			}
			if key == "tab" || key == "shift+tab" {
				m.form.inputs[m.form.focus].Blur()
				step := 1
				if key == "shift+tab" {
					step = -1
				}
				m.form.focus = (m.form.focus + step + len(m.form.inputs)) % len(m.form.inputs)
				return m, m.form.inputs[m.form.focus].Focus()
			}
			var cmd tea.Cmd
			m.form.inputs[m.form.focus], cmd = m.form.inputs[m.form.focus].Update(msg)
			return m, cmd
		}
		if key == "q" {
			if m.busy {
				m.confirmQuit = true
				return m, nil
			}
			return m, tea.Quit
		}
		if key == "tab" || key == "shift+tab" {
			step := 1
			if key == "shift+tab" {
				step = -1
			}
			m.page = (m.page + step + len(pages)) % len(pages)
			m.cursor = 0
			return m, nil
		}
		if key == "esc" && m.busy {
			if m.cancel != nil {
				m.cancel()
			}
			m.message = "Canceling; waiting for the backup engine to stop"
			return m, nil
		}
		if m.busy {
			return m, nil
		}
		if key == "up" {
			m.cursor = max(0, m.cursor-1)
			return m, nil
		}
		if key == "down" {
			m.cursor++
			return m, nil
		}
		if key == "b" {
			return m, m.start("Preparing encrypted backup", func(ctx context.Context, a *vault.App, p func(vault.Progress)) (event, error) {
				return event{text: "Backup committed and sample restore verified"}, a.Backup(ctx, p)
			})
		}
		if key == "v" {
			return m, m.start("Checking repository and sample restore", func(ctx context.Context, a *vault.App, _ func(vault.Progress)) (event, error) {
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
				return m, m.start("Authorize "+provider+" in the browser that opens", func(ctx context.Context, a *vault.App, _ func(vault.Progress)) (event, error) {
					remote, err := a.Connect(ctx, provider)
					return event{connected: remote}, err
				})
			case "enter":
				if len(m.remotes) > 0 {
					m.cursor = min(m.cursor, len(m.remotes)-1)
					m.remote = m.remotes[m.cursor].Name
					return m, m.openForm("folder", "Folder in "+m.remotes[m.cursor].Type, []string{"Dedicated backup folder"}, []string{"PC-Setup-Backups/" + m.app.Profile.Name}, false)
				}
			case "l":
				return m, m.openForm("local", "Local backup destination", []string{"Absolute folder path"}, []string{filepath.Join(m.app.Profile.Home, "SetupVault-Backups")}, false)
			case "t":
				return m, m.connectionTest()
			}
		case 2:
			rows := m.contentRows()
			if len(rows) > 0 {
				m.cursor = min(m.cursor, len(rows)-1)
			}
			if key == "a" {
				return m, m.openForm("path", "Include a custom file or folder", []string{"Absolute path"}, []string{m.app.Profile.Home + "/"}, false)
			}
			if key == "s" {
				return m, m.start("Enable protected system configuration access", func(ctx context.Context, a *vault.App, _ func(vault.Progress)) (event, error) {
					return event{text: "System configuration export enabled; it runs as part of each backup."}, a.EnableSystem(ctx)
				})
			}
			if len(rows) > 0 {
				r := rows[m.cursor]
				if key == "enter" || key == "right" {
					if r.group {
						m.expanded[r.id] = !m.expanded[r.id]
					} else if r.dir {
						m.expanded[r.path] = !m.expanded[r.path]
					}
					return m, nil
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
					return m, nil
				}
			}
		case 3:
			if key == "r" {
				return m, m.loadSnapshots()
			}
			if key == "enter" && len(m.snapshots) > 0 {
				m.cursor = min(m.cursor, len(m.snapshots)-1)
				return m, m.openForm("restore", "Restore into a new, empty folder", []string{"Restore folder"}, []string{filepath.Join(m.app.Profile.Home, "SetupVault-Restore", time.Now().Format("20060102-150405"))}, false)
			}
		case 4:
			switch key {
			case "n":
				return m, m.openForm("name", "Computer/profile name", []string{"Name"}, []string{m.app.Profile.Name}, false)
			case "p":
				return m, m.openForm("password", "Recovery password — save it in Bitwarden", []string{"Password (12+ characters)", "Confirm password"}, []string{"", ""}, true)
			case "s":
				value := m.app.Profile.Schedule
				if value == "" {
					value = "daily"
				}
				return m, m.openForm("schedule", "Schedule (systemd calendar, or 'off')", []string{"Calendar expression"}, []string{value}, false)
			case "k":
				return m, m.openForm("keep", "Snapshot retention", []string{"Keep the latest N snapshots"}, []string{strconv.Itoa(m.app.Profile.KeepLast)}, false)
			case "x":
				return m, m.openForm("export", "Export an encrypted recovery profile", []string{"New recovery file"}, []string{filepath.Join(m.app.Profile.Home, "setupvault-recovery.age")}, false)
			case "i":
				cmd := m.openForm("import", "Import recovery profile (existing profile will not be overwritten)", []string{"Recovery file", "Password"}, []string{filepath.Join(m.app.Profile.Home, "setupvault-recovery.age"), ""}, false)
				m.form.inputs[1].EchoMode = textinput.EchoPassword
				return m, cmd
			case "e":
				if err := m.app.Save(); err != nil {
					m.message = err.Error()
					return m, nil
				}
				editor := strings.Fields(os.Getenv("EDITOR"))
				if len(editor) == 0 {
					editor = []string{"vi"}
				}
				args := append(editor[1:], m.app.ProfilePath())
				return m, tea.ExecProcess(exec.Command(editor[0], args...), func(err error) tea.Msg { return editorMsg{err} })
			}
		}
	}
	if m.form != nil {
		var cmd tea.Cmd
		m.form.inputs[m.form.focus], cmd = m.form.inputs[m.form.focus].Update(msg)
		return m, cmd
	}
	return m, nil
}

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
		add(p, 0, true)
	}
	return rows
}

func stamp(t time.Time) string {
	if t.IsZero() {
		return "Never"
	}
	return t.Local().Format("02 Jan 15:04")
}
func size(n uint64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	v := float64(n)
	i := 0
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

func (m Model) View() tea.View {
	var b strings.Builder
	b.WriteString(heading.Render("SetupVault") + "  " + m.app.Profile.Name + "\n")
	for i, p := range pages {
		label := fmt.Sprintf(" %s ", p)
		if i == m.page {
			label = lipgloss.NewStyle().Bold(true).Background(lipgloss.Color("24")).Foreground(lipgloss.Color("255")).Render(label)
		}
		b.WriteString(label)
	}
	b.WriteString("\n\n")
	if m.form != nil {
		b.WriteString(heading.Render(m.form.title) + "\n\n")
		for i, in := range m.form.inputs {
			b.WriteString(m.form.labels[i] + "\n" + in.View() + "\n\n")
		}
		b.WriteString(muted.Render("Enter next/save · Tab switch field · Esc cancel"))
	} else {
		s := m.app.State()
		switch m.page {
		case 0:
			status := warning.Render(s.Status)
			if s.Status == "Healthy" {
				status = healthy.Render(s.Status)
			}
			b.WriteString("Status          " + status + "\n")
			repo := m.app.Profile.Repository
			if repo == "" {
				repo = "Not configured — open Connections"
			}
			b.WriteString("Destination     " + repo + "\nConnection      " + stamp(s.LastConnection) + "\nLast backup     " + stamp(s.LastBackup) + "\nRestore checked " + stamp(s.LastVerified) + "\nNext backup     " + m.next + "\n")
			b.WriteString(fmt.Sprintf("Last snapshot   %s · %d files · %s\n", short(s.Snapshot), s.Files, size(s.Bytes)))
			if m.quota != nil {
				b.WriteString(fmt.Sprintf("Cloud storage   %s used / %s total · %s free\n", size(m.quota.Used), size(m.quota.Total), size(m.quota.Free)))
			}
			b.WriteString("\n" + heading.Render("Included") + "\n")
			for _, g := range m.app.Groups() {
				check := " "
				if g.Selected {
					check = "x"
				}
				b.WriteString("  [" + check + "] " + g.Name + "\n")
			}
			b.WriteString("\n[b] Back up now   [v] Verify backup   Tab: navigate")
		case 1:
			b.WriteString("[d] Connect Dropbox   [o] Connect OneDrive   [l] Local folder\n[t] Test current destination   Enter: use selected connection\n\n")
			if len(m.remotes) == 0 {
				b.WriteString("No rclone connections yet. Choose a provider above.\n")
			}
			for i, r := range m.remotes {
				cursor := "  "
				if i == min(m.cursor, len(m.remotes)-1) {
					cursor = "> "
				}
				b.WriteString(cursor + r.Type + " · " + r.Name + "\n")
			}
			b.WriteString("\nCurrent destination: " + m.app.Profile.Repository + "\n\n" + muted.Render("Google Drive: use an existing rclone connection with your own OAuth client.\nCloud sign-in opens your browser; the connection test writes and reads a tiny probe."))
		case 2:
			b.WriteString("Space: toggle group/exclusion   Enter: expand tree\n[a] Add path   [s] Enable protected system configuration\n\n")
			rows := m.contentRows()
			cursor := min(m.cursor, max(0, len(rows)-1))
			room := max(3, m.height-13)
			start := max(0, cursor-room+1)
			groups := m.app.Groups()
			selected := map[string]bool{}
			for _, g := range groups {
				selected[g.ID] = g.Selected
			}
			for i := start; i < min(len(rows), start+room); i++ {
				r := rows[i]
				prefix := "  "
				if i == cursor {
					prefix = "> "
				}
				prefix += strings.Repeat("  ", r.depth)
				label := r.label
				if r.group {
					mark := " "
					if selected[r.id] {
						mark = "x"
					}
					label = "[" + mark + "] " + label
				} else {
					label = r.path
					if r.excluded {
						label += "  [excluded]"
					}
				}
				if r.group || r.dir {
					if m.expanded[r.id] || m.expanded[r.path] {
						label = "▾ " + label
					} else {
						label = "▸ " + label
					}
				}
				b.WriteString(prefix + label + "\n")
			}
			b.WriteString("\n" + muted.Render("App settings can live outside common config folders. Add explicit paths when needed.\nDocuments, browser profiles, caches and local mail are excluded by default."))
		case 3:
			b.WriteString("[r] Load snapshots   Enter: restore selected snapshot to a new folder\n\n")
			if len(m.snapshots) == 0 {
				b.WriteString("Load backup history to choose a dated snapshot.\n")
			}
			for i, s := range m.snapshots {
				prefix := "  "
				if i == min(m.cursor, len(m.snapshots)-1) {
					prefix = "> "
				}
				b.WriteString(prefix + stamp(s.Time) + "  " + short(s.ID) + "  " + s.Hostname + "\n")
			}
			b.WriteString("\n" + muted.Render("Restores never overwrite your home or /. Review restored settings first.\nThe restored inventory contains RPM/Flatpak lists for reinstalling packaged apps.\nSystem configuration is archived separately for selective administrator restore."))
		case 4:
			calendar := m.app.Profile.Schedule
			if calendar == "" {
				calendar = "Off"
			}
			b.WriteString(fmt.Sprintf("[n] Name                 %s\n[p] Recovery password    Set/unlock in the desktop keyring\n[s] Schedule             %s\n[k] Keep snapshots       %d\n[x] Export encrypted recovery profile\n[i] Import recovery profile\n[e] Advanced profile     %s\n", m.app.Profile.Name, calendar, m.app.Profile.KeepLast, m.app.ProfilePath()))
			b.WriteString("\n" + muted.Render("Save the recovery password in Bitwarden. Export/import uses that same password.\nImported profiles have scheduling disabled. Cloud sign-in may be needed again.\nScheduled backups need an unlocked desktop keyring and an active user session."))
		case 5:
			data, _ := os.ReadFile(filepath.Join(m.app.StateDir, "activity.log"))
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			start := max(0, len(lines)-max(3, m.height-12))
			b.WriteString(strings.Join(lines[start:], "\n"))
		}
	}
	if m.busy {
		b.WriteString("\n\n" + heading.Render(m.progress.Phase))
		if m.progress.TotalBytes > 0 {
			b.WriteString(fmt.Sprintf(" · %s / %s", size(m.progress.Bytes), size(m.progress.TotalBytes)))
		}
		b.WriteString("  Esc: cancel")
	}
	message := m.message
	if m.failed {
		message = warning.Render(message)
	}
	b.WriteString("\n\n" + message + "\n" + muted.Render("Tab/Shift+Tab navigate · ↑↓ select · q quit"))
	if m.confirmQuit {
		b.WriteString("\n" + warning.Render("A task is running. Cancel it and quit? y / any key to stay"))
	}
	v := tea.NewView(lipgloss.NewStyle().Padding(1, 2).MaxWidth(max(30, m.width)).Render(b.String()))
	v.AltScreen = true
	return v
}

func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	if s == "" {
		return "None"
	}
	return s
}
