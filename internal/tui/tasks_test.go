package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/alhimowicz/setupvault/internal/vault"
)

func TestLoadingAnimationForTaskAndRefresh(t *testing.T) {
	m := testModel(t)
	m.refreshing = false
	done := make(chan struct{})
	cmd := m.start("Testing connection", func(ctx context.Context, _ *vault.App, _ func(vault.Progress)) (event, error) {
		select {
		case <-done:
			return event{text: "Connected"}, nil
		case <-ctx.Done():
			return event{}, ctx.Err()
		}
	})
	t.Cleanup(func() {
		if m.cancel != nil {
			m.cancel()
		}
	})
	if !m.busy || cmd == nil || !strings.Contains(m.View().Content, "Testing connection") {
		t.Fatal("task did not show loading state")
	}
	before := m.spinner.View()
	next, tick := m.Update(m.spinner.Tick())
	m = next.(Model)
	if tick == nil || m.spinner.View() == before {
		t.Fatal("loading indicator did not animate")
	}
	close(done)
	finished := <-m.events
	next, cmd = m.Update(finished)
	m = next.(Model)
	if m.busy || m.message != "Connected" {
		t.Fatal("task did not finish")
	}
	next, cmd = m.Update(cmd())
	m = next.(Model)
	if !m.refreshing || cmd == nil {
		t.Fatal("status refresh not indicated")
	}
	next, cmd = m.Update(refreshRequestMsg{})
	m = next.(Model)
	if cmd != nil {
		t.Fatal("overlapping status refresh started")
	}
	next, _ = m.Update(refreshMsg{})
	m = next.(Model)
	next, cmd = m.Update(m.spinner.Tick())
	m = next.(Model)
	if m.refreshing || cmd != nil {
		t.Fatal("spinner kept ticking while idle")
	}
}

func TestQuitAndShutdownWaitForTaskCleanup(t *testing.T) {
	for _, quit := range []bool{false, true} {
		m := testModel(t)
		cleaning := make(chan struct{})
		release := make(chan struct{})
		t.Cleanup(func() {
			select {
			case <-release:
			default:
				close(release)
			}
			m.Shutdown()
		})
		m.start("Test task", func(ctx context.Context, _ *vault.App, progress func(vault.Progress)) (event, error) {
			for range 32 {
				progress(vault.Progress{Phase: "Working"})
			}
			<-ctx.Done()
			close(cleaning)
			<-release
			return event{}, ctx.Err()
		})
		if quit {
			m.handleKey(tea.KeyPressMsg{Code: 'q', Text: "q"})
			cmd := m.handleKey(tea.KeyPressMsg{Code: 'y', Text: "y"})
			if cmd != nil || !m.quitting || !m.busy || m.confirmQuit {
				t.Fatal("quit did not wait for task cleanup")
			}
			if cmd := m.handleKey(tea.KeyPressMsg{Code: 'q', Text: "q"}); cmd != nil {
				t.Fatal("repeated quit bypassed cleanup")
			}
		}
		stopped := make(chan struct{})
		go func() { m.Shutdown(); close(stopped) }()
		select {
		case <-cleaning:
		case <-time.After(time.Second):
			t.Fatal("worker was not canceled")
		}
		select {
		case <-stopped:
			t.Fatal("shutdown returned before worker cleanup")
		default:
		}
		close(release)
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Fatal("shutdown blocked on a full progress queue")
		}
		if quit {
			for {
				e := waitEvent(m.events)().(event)
				if !e.done {
					continue
				}
				_, cmd := m.Update(e)
				if cmd == nil {
					t.Fatal("completion did not finish quitting")
				}
				if _, ok := cmd().(tea.QuitMsg); !ok {
					t.Fatal("completion did not quit")
				}
				break
			}
		}
	}
}

func TestConnectionsLoadingAndStaleRefresh(t *testing.T) {
	m := testModel(t)
	m.page = 1
	if strings.Contains(m.View().Content, "No rclone connections") || !strings.Contains(m.View().Content, "Loading connections") {
		t.Fatal("empty state shown before loading completes")
	}
	m.app.Profile.Repository = "/new/repository"
	next, cmd := m.Update(refreshMsg{})
	m = next.(Model)
	if !m.refreshing || cmd == nil {
		t.Fatal("stale result stopped the current refresh")
	}
	next, _ = m.Update(refreshMsg{repository: m.app.Profile.Repository})
	m = next.(Model)
	if m.refreshing || !strings.Contains(m.View().Content, "No rclone connections") {
		t.Fatal("loading state not cleared")
	}
}
