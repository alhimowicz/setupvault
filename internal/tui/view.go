package tui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

var pages = []string{"Overview", "Connections", "Contents", "Restore", "Settings", "Activity"}

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

func (m Model) content() string {
	var b strings.Builder
	b.WriteString(m.styles.heading.Render("SetupVault") + "  " + m.app.Profile.Name)
	if m.busy || m.refreshing || m.recovery != nil && m.recovery.Active() {
		b.WriteString("  " + m.spinner.View())
	}
	b.WriteString("\n")
	for i, p := range pages {
		label := fmt.Sprintf(" %s ", p)
		if i == m.page {
			label = m.styles.selected.Render("[" + p + "]")
		}
		b.WriteString(label)
	}
	b.WriteString("\n\n")
	if m.form != nil {
		b.WriteString(m.styles.heading.Render(m.form.title) + "\n\n")
		for i, in := range m.form.inputs {
			b.WriteString(m.form.labels[i] + "\n" + in.View() + "\n\n")
		}
		b.WriteString(m.styles.muted.Render("Enter next/save · Tab switch field · Esc cancel"))
	} else {
		s := m.app.State()
		switch m.page {
		case 0:
			if m.recovery != nil {
				b.WriteString("Recovery        " + m.recovery.Status + " · " + m.recovery.Phase + "\nOpen Restore for progress, review and settings Undo.\n\n")
			}
			status := m.styles.warning.Render(s.Status)
			if s.Status == "Healthy" {
				status = m.styles.healthy.Render(s.Status)
			}
			b.WriteString("Status          " + status + "\n")
			repo := m.app.Profile.Repository
			if repo == "" {
				repo = "Not configured — open Connections"
			}
			b.WriteString("Destination     " + repo + "\nConnection      " + stamp(s.LastConnection) + "\nLast backup     " + stamp(s.LastBackup) + "\nRestore checked " + stamp(s.LastVerified) + "\nNext backup     " + m.next + "\n")
			b.WriteString(fmt.Sprintf("Last snapshot   %s · %d files · %s\n", short(s.Snapshot), s.Files, size(s.Bytes)))
			if s.LastError != "" {
				b.WriteString(m.styles.failure.Render(ansi.Wrap("Last issue: "+s.LastError, max(1, m.width-4), "")) + "\n")
			}
			if m.quota != nil {
				b.WriteString(fmt.Sprintf("Cloud storage   %s used / %s total · %s free\n", size(m.quota.Used), size(m.quota.Total), size(m.quota.Free)))
			}
			b.WriteString("\n" + m.styles.heading.Render("Included") + "\n")
			for _, g := range m.app.Groups() {
				check := " "
				if g.Selected {
					check = "x"
				}
				b.WriteString("  [" + check + "] " + g.Name + "\n")
			}
			b.WriteString("\n[b] Back up now   [v] Verify backup   Tab: navigate")
		case 1:
			if m.connectorError != "" {
				b.WriteString(m.styles.warning.Render(ansi.Wrap(m.connectorError+"\n[i] Install rclone (administrator authentication)", max(1, m.width-4), "")) + "\nLocal-folder backups remain available.\n\n")
			}
			b.WriteString("[d] Connect Dropbox   [o] Connect OneDrive   [l] Local folder\n[t] Test current destination   Enter: use selected connection\n\n")
			if len(m.remotes) == 0 {
				if m.refreshing {
					b.WriteString("Loading connections…\n")
				} else {
					b.WriteString("No rclone connections yet. Choose a provider above.\n")
				}
			}
			for i, r := range m.remotes {
				cursor := "  "
				if i == min(m.cursor, len(m.remotes)-1) {
					cursor = "> "
				}
				b.WriteString(cursor + r.Type + " · " + r.Name + "\n")
			}
			b.WriteString("\nCurrent destination: " + m.app.Profile.Repository + "\n\n" + m.styles.muted.Render("Google Drive: use an existing rclone connection with your own OAuth client.\nCloud sign-in opens your browser; the connection test writes and reads a tiny probe."))
		case 2:
			b.WriteString("Space: toggle group/exclusion   Enter: expand tree\n[a] Add path   [d/Del] Remove custom path   [s] System configuration\n\n")
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
					if r.custom {
						label += "  [custom]"
					}
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
			b.WriteString("\n" + m.styles.muted.Render("App settings can live outside common config folders. Add explicit paths when needed.\nDocuments, browser profiles, caches and local mail are excluded by default."))
		case 3:
			if m.recovery != nil && !m.recoveryBrowse {
				b.WriteString(m.recoveryView())
				break
			}
			b.WriteString("[i] Import profile   [r] Load snapshots\nEnter: Restore my setup   [e] Extract files only\n\n")
			if m.recovery != nil {
				b.WriteString("[j] Return to recovery " + m.recovery.ID + "\n")
			}
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
			b.WriteString("\n" + m.styles.muted.Render("Restore my setup prepares a review before installing apps and applying settings.\nExtract files only writes into a separate empty folder.\nSystem configuration is staged for selective administrator review."))
		case 4:
			calendar := m.app.Profile.Schedule
			if calendar == "" {
				calendar = "Off"
			}
			b.WriteString("[t] Theme                " + m.themeLabel() + "\n")
			b.WriteString(fmt.Sprintf("[n] Name                 %s\n[p] Recovery password    Set/unlock in the desktop keyring\n[s] Schedule             %s\n[k] Keep snapshots       %d\n[x] Export encrypted recovery profile\n[i] Import recovery profile\n[e] Advanced profile     %s\n", m.app.Profile.Name, calendar, m.app.Profile.KeepLast, m.app.ProfilePath()))
			b.WriteString("\n" + m.styles.muted.Render("Save the recovery password in Bitwarden. Export/import uses that same password.\nImported profiles have scheduling disabled. Cloud sign-in may be needed again.\nScheduled backups need an unlocked desktop keyring and an active user session."))
		case 5:
			data, _ := os.ReadFile(filepath.Join(m.app.StateDir, "activity.log"))
			lines := strings.Split(strings.TrimSpace(string(data)), "\n")
			start := max(0, len(lines)-max(3, m.height-12))
			b.WriteString(ansi.Wrap(strings.Join(lines[start:], "\n"), max(1, m.width-4), ""))
		}
	}
	if m.busy {
		b.WriteString("\n\n" + m.spinner.View() + " " + m.styles.heading.Render(m.progress.Phase))
		if m.progress.TotalBytes > 0 {
			b.WriteString(fmt.Sprintf(" · %s / %s", size(m.progress.Bytes), size(m.progress.TotalBytes)))
		}
		b.WriteString("  Esc: cancel")
	} else if m.refreshing {
		b.WriteString("\n\n" + m.spinner.View() + " " + m.styles.muted.Render("Refreshing connections, storage and schedule…"))
	}
	message := m.message
	if m.failed {
		message = "Error: " + message
	}
	message = ansi.Wrap(message, max(1, m.width-4), "")
	if m.failed {
		message = m.styles.failure.Render(message)
	}
	b.WriteString("\n\n" + message)
	if m.confirmQuit {
		b.WriteString("\n" + m.styles.warning.Render(ansi.Wrap("A task is running. Cancel it and quit? y / any key to stay", max(1, m.width-4), "")))
	}
	return b.String()
}

func (m Model) layout() ([]string, string, string, int) {
	help := m.styles.muted.Render(ansi.Wrap("PgUp/PgDn scroll · Tab/Shift+Tab pages · ↑↓ select · q quit", max(1, m.width-4), ""))
	lines := strings.Split(m.content(), "\n")
	count := min(3, len(lines))
	header := strings.Join(lines[:count], "\n")
	room := max(1, m.height-2-lipgloss.Height(help)-count)
	return lines[count:], header, help, room
}

func (m Model) maxScroll() int {
	lines, _, _, room := m.layout()
	return max(0, len(lines)-room)
}

func (m Model) View() tea.View {
	lines, header, help, room := m.layout()
	start := max(0, min(m.scroll, len(lines)-room))
	end := min(len(lines), start+room)
	content := header + "\n" + strings.Join(lines[start:end], "\n")
	content += strings.Repeat("\n", max(1, room-(end-start)+1)) + help
	v := tea.NewView(lipgloss.NewStyle().Padding(1, 2).MaxWidth(max(1, m.width)).MaxHeight(max(1, m.height)).Render(content))
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
