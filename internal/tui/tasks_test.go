package tui

import (
	"context"
	"strings"
	"testing"

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
