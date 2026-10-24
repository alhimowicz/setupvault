package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/alhimowicz/setupvault/internal/vault"
	"github.com/charmbracelet/x/ansi"
)

type recoveryPollMsg struct{}
type recoveryStatusMsg struct {
	jobs    []vault.RecoveryJob
	profile string
}

func recoveryTick() tea.Cmd {
	return tea.Tick(2*time.Second, func(time.Time) tea.Msg { return recoveryPollMsg{} })
}

func (m Model) fetchRecovery() tea.Cmd {
	a := *m.app
	return func() tea.Msg {
		jobs, _ := a.Recoveries()
		sort.Slice(jobs, func(i, j int) bool { return jobs[i].Updated.Before(jobs[j].Updated) })
		ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
		defer cancel()
		for i := range jobs {
			if jobs[i].Active() {
				if j, err := a.RecoveryStatus(ctx, jobs[i].ID); err == nil {
					jobs[i] = *j
				}
			}
		}
		return recoveryStatusMsg{jobs: jobs, profile: a.Profile.ID}
	}
}

var recoveryGroups = []string{"apps", "settings", "gnome", "manual", "custom"}
var recoveryLabels = map[string]string{"apps": "Reinstall RPM / Flatpak apps", "settings": "App, terminal and shell settings", "gnome": "GNOME, extensions, fonts and themes", "manual": "AppImages, manual apps and plugins", "custom": "Captured custom home paths"}

func (m Model) recoveryView() string {
	j := m.recovery
	var b strings.Builder
	b.WriteString(m.styles.heading.Render("Restore my setup") + "\nJob " + j.ID + " · " + j.Status + "\n")
	b.WriteString(ansi.Wrap(j.Phase, max(1, m.width-4), "") + "\n")
	if j.Active() {
		b.WriteString(m.spinner.View() + " Running in the background. q leaves it running.\n")
	}
	if j.Error != "" {
		b.WriteString(m.styles.failure.Render(ansi.Wrap(j.Error, max(1, m.width-4), "")) + "\n")
	}
	if m.recoveryMode == "undo" {
		b.WriteString("\nUndo settings changes?\nPrevious user files and desktop keys will be restored.\nNewer edits will be preserved. Installed packages remain installed.\n\nEnter: Undo settings   Esc: Back\n")
		return b.String()
	}
	switch m.recoveryMode {
	case "sources":
		b.WriteString("\nSpace: approve/deselect source   [g] Categories\n")
		sources := recoverySources(j)
		for i, p := range sources {
			prefix := "  "
			if i == min(m.cursor, len(sources)-1) {
				prefix = "> "
			}
			mark := " "
			for _, id := range j.ApprovedSources {
				if id == p.SourceID() {
					mark = "x"
				}
			}
			b.WriteString(ansi.Wrap(prefix+"["+mark+"] "+p.Scope+":"+p.Origin+"\n    "+p.URL+"\n    Signing identity: "+p.Key, max(1, m.width-4), "") + "\n")
		}
	case "apps":
		b.WriteString("\nSpace: select/deselect installation   [s] Sources   [g] Categories\n")
		cursor := min(m.cursor, max(0, len(j.Packages)-1))
		start := max(0, cursor-max(3, m.height-14)+1)
		for i := start; i < min(len(j.Packages), start+max(3, m.height-14)); i++ {
			p := j.Packages[i]
			mark := " "
			if p.Selected {
				mark = "x"
			}
			prefix := "  "
			if i == cursor {
				prefix = "> "
			}
			b.WriteString(ansi.Wrap(fmt.Sprintf("%s[%s] %s %s [%s] %s", prefix, mark, p.Kind, p.Name, p.Status, p.Reason), max(1, m.width-4), "") + "\n")
		}
	case "files":
		b.WriteString("\nSpace: select/deselect file   [g] Categories\n")
		cursor := min(m.cursor, max(0, len(j.Files)-1))
		start := max(0, cursor-max(3, m.height-14)+1)
		for i := start; i < min(len(j.Files), start+max(3, m.height-14)); i++ {
			f := j.Files[i]
			prefix := "  "
			if i == cursor {
				prefix = "> "
			}
			b.WriteString(ansi.Wrap(fmt.Sprintf("%s%s [%s] %s", prefix, f.Path, f.Status, f.Reason), max(1, m.width-4), "") + "\n")
		}
	case "details":
		dir := filepath.Join(m.app.StateDir, "recovery", j.ID)
		b.WriteString("\nVerified staging: " + filepath.Join(dir, "stage") + "\nSettings rollback: " + filepath.Join(dir, "rollback") + "\nReport: " + filepath.Join(dir, "job.json") + "\n")
		var transaction struct {
			RPMs []struct {
				NEVRA, Action string
				Repo          string `json:"repo_id"`
			} `json:"rpms"`
		}
		if json.Unmarshal(j.DNF, &transaction) == nil && len(transaction.RPMs) > 0 {
			b.WriteString("\nReviewed RPM transaction (including dependencies):\n")
			for _, p := range transaction.RPMs {
				b.WriteString(ansi.Wrap(p.Action+" "+p.NEVRA+" ["+p.Repo+"]", max(1, m.width-4), "") + "\n")
			}
		}
		for _, notice := range j.Notices {
			b.WriteString(ansi.Wrap("• "+notice, max(1, m.width-4), "") + "\n")
		}
		for _, k := range j.Keys {
			if k.Reason != "" {
				b.WriteString(ansi.Wrap(k.Path+": "+k.Reason, max(1, m.width-4), "") + "\n")
			}
		}
	default:
		b.WriteString("\n")
		for i, g := range recoveryGroups {
			mark := " "
			if j.Selected(g) {
				mark = "x"
			}
			prefix := "  "
			if i == min(m.cursor, len(recoveryGroups)-1) {
				prefix = "> "
			}
			b.WriteString(prefix + "[" + mark + "] " + recoveryLabels[g] + "\n")
		}
		var installs, present, replaced, restored, blocked int
		for _, p := range j.Packages {
			if p.Status == "Installed" {
				present++
			} else if p.Selected && j.Selected("apps") {
				installs++
				if p.Status == "Blocked" || p.Status == "Needs source approval" {
					blocked++
				}
			}
		}
		for _, f := range j.Files {
			if !j.Selected(f.Group) {
				continue
			}
			if f.Before != "missing" && f.Before != f.After {
				replaced++
			}
			if f.Status == "Restored" {
				restored++
			}
			if f.Status == "Blocked" {
				blocked++
			}
		}
		b.WriteString(fmt.Sprintf("\nApps: %d selected to install · %d present\nFiles: %d restored · %d replacements with Undo copies\nNeeds attention: %d\n", installs, present, restored, replaced, blocked))
		b.WriteString("Verified staging data: " + size(j.StagedBytes) + "\n")
		b.WriteString("System archive: staged for separate review.\n")
	}
	if j.NeedsLogout {
		b.WriteString("Log out and back in to finish desktop changes.\n")
	}
	if j.Active() {
		b.WriteString("\n[c] Stop after current transaction   [p] Apps   [f] Files   [d] Details")
	} else if j.Status == "Ready" {
		b.WriteString("\n[a] Start recovery   Space: toggle category\n[p] Apps   [f] Files   [d] Details   [r] Choose backup")
	} else {
		b.WriteString("\n[a] Resume   [u] Undo settings   [p] Apps   [f] Files\n[d] Details   [r] Choose backup")
	}
	return b.String()
}

func toggleRecoveryList(list []string, value string) []string {
	next := []string{}
	found := false
	for _, v := range list {
		if v == value {
			found = true
			continue
		}
		next = append(next, v)
	}
	if !found {
		next = append(next, value)
	}
	return next
}

func (m *Model) recoveryKey(key string) tea.Cmd {
	j := m.recovery
	if m.recoveryMode == "undo" {
		if key == "esc" {
			m.recoveryMode = "groups"
			return nil
		}
		if key == "enter" {
			return m.start("Undoing recovered settings", func(ctx context.Context, a *vault.App, _ func(vault.Progress)) (event, error) {
				job, err := a.UndoRecoverySettings(ctx, j.ID)
				return event{text: "Settings undo finished; newer edits are preserved", recovery: job}, err
			})
		}
		return nil
	}
	switch key {
	case "p":
		m.recoveryMode = "apps"
		m.cursor = 0
	case "s":
		m.recoveryMode = "sources"
		m.cursor = 0
	case "f":
		m.recoveryMode = "files"
		m.cursor = 0
	case "d":
		m.recoveryMode = "details"
		m.cursor = 0
	case "g", "esc":
		m.recoveryMode = "groups"
		m.cursor = 0
	case "r":
		m.recoveryBrowse = true
		m.recoveryMode = ""
		return m.loadSnapshots()
	case "a":
		if j.Active() {
			return nil
		}
		return m.start("Starting background recovery; authorize selected system packages", func(ctx context.Context, a *vault.App, _ func(vault.Progress)) (event, error) {
			var job *vault.RecoveryJob
			var err error
			if j.Status == "Ready" {
				job, err = a.ApplyRecovery(ctx, j.ID, true)
			} else {
				job, err = a.ResumeRecovery(ctx, j.ID, true)
			}
			return event{text: "Recovery started in the background. You may leave the TUI running or reopen it later.", recovery: job}, err
		})
	case "c":
		return m.start("Requesting a safe recovery stop", func(_ context.Context, a *vault.App, _ func(vault.Progress)) (event, error) {
			job, err := a.CancelRecovery(j.ID)
			return event{text: "Stop requested; the current transaction may need to finish", recovery: job}, err
		})
	case "u":
		if !j.Active() {
			m.recoveryMode = "undo"
		}
	case "space", " ":
		if j.Status != "Ready" {
			m.message = "Only a ready plan can be edited; running work keeps its approved selection"
			return nil
		}
		groups := append([]string{}, j.Groups...)
		approved := append([]string{}, j.ApprovedSources...)
		skipApps := append([]string{}, j.SkipApps...)
		skipFiles := append([]string{}, j.SkipFiles...)
		switch m.recoveryMode {
		case "sources":
			sources := recoverySources(j)
			if len(sources) == 0 {
				return nil
			}
			p := sources[min(m.cursor, len(sources)-1)]
			approved = toggleRecoveryList(approved, p.SourceID())
		case "apps":
			if len(j.Packages) == 0 {
				return nil
			}
			p := j.Packages[min(m.cursor, len(j.Packages)-1)]
			if p.Status == "Installed" || p.Status == "Skipped" {
				m.message = p.Reason
				return nil
			}
			skipApps = toggleRecoveryList(skipApps, p.ID)
		case "files":
			if len(j.Files) == 0 {
				return nil
			}
			f := j.Files[min(m.cursor, len(j.Files)-1)]
			skipFiles = toggleRecoveryList(skipFiles, f.Path)
		default:
			groups = toggleRecoveryList(groups, recoveryGroups[min(m.cursor, len(recoveryGroups)-1)])
		}
		return m.start("Updating recovery selection", func(ctx context.Context, a *vault.App, _ func(vault.Progress)) (event, error) {
			job, err := a.SelectRecovery(ctx, j.ID, groups, skipApps, skipFiles, approved)
			return event{text: "Recovery plan updated", recovery: job, recoveryKeepView: true}, err
		})
	}
	return nil
}

func recoverySources(j *vault.RecoveryJob) []vault.RecoveryPackage {
	var result []vault.RecoveryPackage
	seen := map[string]bool{}
	for _, p := range j.Packages {
		key := p.Scope + ":" + p.Origin
		if p.AddRemote && !seen[key] {
			seen[key] = true
			result = append(result, p)
		}
	}
	return result
}

func (m *Model) openRecoveryImport() tea.Cmd {
	cmd := m.openForm("import", "Import recovery profile", []string{"Recovery file", "Password"}, []string{filepath.Join(m.app.Profile.Home, "setupvault-recovery.age"), ""}, false)
	m.form.inputs[1].EchoMode = textinput.EchoPassword
	return cmd
}
