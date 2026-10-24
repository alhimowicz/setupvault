package tui

import (
	"context"
	"time"

	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/alhimowicz/setupvault/internal/vault"
)

type event struct {
	text             string
	err              error
	done             bool
	progress         vault.Progress
	remotes          []vault.Remote
	snapshots        []vault.Snapshot
	quota            *vault.Quota
	next             string
	connected        string
	repository       string
	recovery         *vault.RecoveryJob
	imported         bool
	connectorError   string
	recoveryKeepView bool
}

type refreshMsg event

type refreshRequestMsg struct{}

type tickMsg time.Time

type editorMsg struct{ err error }

type row struct {
	label, path, id              string
	depth                        int
	group, dir, excluded, custom bool
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
	scroll                      int
	expanded                    map[string]bool
	message                     string
	failed, busy, confirmQuit   bool
	quitting                    bool
	progress                    vault.Progress
	cancel                      context.CancelFunc
	taskDone                    chan struct{}
	events                      chan event
	remotes                     []vault.Remote
	snapshots                   []vault.Snapshot
	quota                       *vault.Quota
	next                        string
	form                        *form
	remote                      string
	spinner                     spinner.Model
	refreshing                  bool
	recovery                    *vault.RecoveryJob
	recoveryMode                string
	recoveryBrowse              bool
	recoveryJobs                []vault.RecoveryJob
	connectorError              string
	styles                      themeStyles
}

func New(a *vault.App) Model {
	m := Model{
		app:        a,
		width:      100,
		height:     30,
		next:       "Checking schedule…",
		expanded:   map[string]bool{},
		message:    "Choose a connection, review Contents, and set a recovery password to begin.",
		spinner:    spinner.New(spinner.WithSpinner(spinner.Points)),
		refreshing: true,
	}
	m.applyTheme()
	return m
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.fetchStatus(), tick(), m.spinner.Tick, m.fetchRecovery(), recoveryTick())
}

func tick() tea.Cmd { return tea.Tick(15*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }) }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case recoveryPollMsg:
		return m, tea.Batch(m.fetchRecovery(), recoveryTick())
	case recoveryStatusMsg:
		if msg.profile != m.app.Profile.ID {
			return m, nil
		}
		wasActive := m.recovery != nil && m.recovery.Active()
		m.recoveryJobs = msg.jobs
		if len(msg.jobs) > 0 {
			chosen := &msg.jobs[len(msg.jobs)-1]
			active := false
			for i := range msg.jobs {
				if msg.jobs[i].Active() {
					chosen = &msg.jobs[i]
					active = true
				}
			}
			if m.recovery != nil {
				for i := range msg.jobs {
					if msg.jobs[i].ID == m.recovery.ID && (!active || msg.jobs[i].Active()) {
						chosen = &msg.jobs[i]
						break
					}
				}
			}
			m.recovery = chosen
			if wasActive && !chosen.Active() {
				m.message = "Recovery " + chosen.Status + ": " + chosen.Phase
				if chosen.Error != "" {
					m.message += "\n" + chosen.Error
				}
				m.failed = chosen.Status == "Failed" || chosen.Status == "Partial"
				m.scroll = m.maxScroll()
			}
		}
		if !wasActive && m.recovery != nil && m.recovery.Active() && !m.busy && !m.refreshing {
			return m, m.spinner.Tick
		}
		return m, nil
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil
	case tickMsg:
		return m, tea.Batch(tick(), m.refresh())
	case refreshRequestMsg:
		if m.refreshing {
			return m, nil
		}
		m.refreshing = true
		cmd := m.fetchStatus()
		if !m.busy && (m.recovery == nil || !m.recovery.Active()) {
			cmd = tea.Batch(cmd, m.spinner.Tick)
		}
		return m, cmd
	case spinner.TickMsg:
		if !m.busy && !m.refreshing && (m.recovery == nil || !m.recovery.Active()) {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case refreshMsg:
		if msg.repository != m.app.Profile.Repository {
			return m, m.fetchStatus()
		}
		m.refreshing = false
		m.remotes = msg.remotes
		m.quota = msg.quota
		m.next = msg.next
		m.connectorError = msg.connectorError
		return m, nil
	case editorMsg:
		if msg.err != nil {
			m.message = msg.err.Error()
			m.failed = true
			m.scroll = m.maxScroll()
			return m, nil
		}
		a, err := vault.New(m.app.ConfigDir, m.app.StateDir)
		if err != nil {
			m.message = err.Error()
			m.failed = true
		} else {
			m.app = a
			m.applyTheme()
			m.message = "Profile reloaded"
			m.failed = false
		}
		m.scroll = m.maxScroll()
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
		if m.quitting {
			return m, tea.Quit
		}
		if msg.err != nil {
			m.message = msg.err.Error()
			m.failed = true
			m.scroll = m.maxScroll()
			return m, m.refresh()
		}
		m.message = msg.text
		m.failed = false
		m.scroll = m.maxScroll()
		if a, err := vault.New(m.app.ConfigDir, m.app.StateDir); err == nil {
			m.app = a
			m.applyTheme()
		}
		if msg.snapshots != nil {
			m.snapshots = msg.snapshots
			m.cursor = max(0, len(m.snapshots)-1)
		}
		if msg.recovery != nil {
			m.recovery = msg.recovery
			if !msg.recoveryKeepView {
				m.recoveryMode = "groups"
				m.cursor = 0
			}
			m.recoveryBrowse = false
			m.page = 3
			m.scroll = 0
		}
		if msg.imported {
			m.page = 3
			m.snapshots = nil
			m.recovery = nil
			m.recoveryBrowse = false
			m.message = "Profile imported. Press r to choose a backup and restore your setup; reconnect in Connections if needed."
		}
		if msg.connected != "" {
			m.remote = msg.connected
			return m, m.openForm("folder", "Folder in your cloud account", []string{"Dedicated backup folder"}, []string{"PC-Setup-Backups/" + m.app.Profile.Name}, false)
		}
		return m, m.refresh()
	case tea.KeyPressMsg:
		previous := m.message
		cmd := m.handleKey(msg)
		if m.message != previous {
			m.scroll = m.maxScroll()
		}
		return m, cmd

	}
	if m.form != nil {
		var cmd tea.Cmd
		m.form.inputs[m.form.focus], cmd = m.form.inputs[m.form.focus].Update(msg)
		return m, cmd
	}
	return m, nil
}
