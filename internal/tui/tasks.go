package tui

import (
	"context"
	"fmt"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/alhimowicz/setupvault/internal/vault"
)

func (m Model) refresh() tea.Cmd {
	return func() tea.Msg { return refreshRequestMsg{} }
}

func (m Model) fetchStatus() tea.Cmd {
	a := *m.app
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		e := event{next: a.NextRun(ctx), repository: a.Profile.Repository}
		e.remotes, _ = a.Remotes(ctx)
		if err := vault.CheckDependency("rclone"); err != nil {
			e.connectorError = err.Error()
		}
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
			return event{done: true, err: context.Canceled}
		}
		return e
	}
}

func (m *Model) start(label string, fn func(context.Context, *vault.App, func(vault.Progress)) (event, error)) tea.Cmd {
	animate := !m.busy && !m.refreshing && (m.recovery == nil || !m.recovery.Active())
	m.busy = true
	m.failed = false
	m.message = label
	m.progress = vault.Progress{Phase: label}
	m.events = make(chan event, 16)
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.taskDone = make(chan struct{})
	done := m.taskDone
	worker := *m.app
	ch := m.events
	go func() {
		defer close(done)
		defer close(ch)
		e, err := fn(ctx, &worker, func(p vault.Progress) {
			select {
			case ch <- event{progress: p}:
			default:
			}
		})
		e.done = true
		e.err = err
		select {
		case ch <- e:
		case <-ctx.Done():
		}
	}()
	if animate {
		return tea.Batch(waitEvent(ch), m.spinner.Tick)
	}
	return waitEvent(ch)
}

// Shutdown waits for the worker to release repository locks when the terminal closes.
func (m Model) Shutdown() {
	if m.cancel != nil {
		m.cancel()
	}
	if m.taskDone != nil {
		<-m.taskDone
	}
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
