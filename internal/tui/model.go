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
	text       string
	err        error
	done       bool
	progress   vault.Progress
	remotes    []vault.Remote
	snapshots  []vault.Snapshot
	quota      *vault.Quota
	next       string
	connected  string
	repository string
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
}

func New(a *vault.App) Model {
	return Model{
		app:        a,
		width:      100,
		height:     30,
		next:       "Checking schedule…",
		expanded:   map[string]bool{},
		message:    "Choose a connection, review Contents, and set a recovery password to begin.",
		spinner:    spinner.New(spinner.WithSpinner(spinner.Points), spinner.WithStyle(heading)),
		refreshing: true,
	}
}

func (m Model) Init() tea.Cmd { return tea.Batch(m.fetchStatus(), tick(), m.spinner.Tick) }

func tick() tea.Cmd { return tea.Tick(15*time.Second, func(t time.Time) tea.Msg { return tickMsg(t) }) }

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
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
		if !m.busy {
			cmd = tea.Batch(cmd, m.spinner.Tick)
		}
		return m, cmd
	case spinner.TickMsg:
		if !m.busy && !m.refreshing {
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
		if m.quitting {
			return m, tea.Quit
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
		cmd := m.handleKey(msg)
		return m, cmd

	}
	if m.form != nil {
		var cmd tea.Cmd
		m.form.inputs[m.form.focus], cmd = m.form.inputs[m.form.focus].Update(msg)
		return m, cmd
	}
	return m, nil
}
