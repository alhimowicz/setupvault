package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/alhimowicz/setupvault/internal/vault"
	"io"
	"strings"
)

func executeRecovery(ctx context.Context, a *vault.App, action string, o *options, seen map[string]bool, stderr io.Writer) (any, error) {
	var progress func(vault.Progress)
	if o.progress {
		progress = func(p vault.Progress) {
			if o.json {
				_ = json.NewEncoder(stderr).Encode(map[string]any{"cli_version": 1, "event": "progress", "data": p})
			} else {
				fmt.Fprintln(stderr, p.Phase)
			}
		}
	}
	switch action {
	case "plan-recovery":
		var groups []string
		if seen["select"] {
			groups = []string{}
			if o.selected != "" {
				groups = strings.Split(o.selected, ",")
			}
		}
		if o.job != "" {
			return a.SelectRecovery(ctx, o.job, groups, o.skipApps, o.skipFiles, o.approveSources)
		}
		j, err := a.PlanRecovery(ctx, o.snapshot, groups, progress)
		if err != nil {
			return j, err
		}
		if len(o.skipApps) > 0 || len(o.skipFiles) > 0 || len(o.approveSources) > 0 {
			return a.SelectRecovery(ctx, j.ID, nil, o.skipApps, o.skipFiles, o.approveSources)
		}
		return j, nil
	case "apply-recovery", "resume-recovery":
		var j *vault.RecoveryJob
		var err error
		if action == "resume-recovery" {
			j, err = a.ResumeRecovery(ctx, o.job, o.allowInteraction)
		} else {
			j, err = a.ApplyRecovery(ctx, o.job, o.allowInteraction)
		}
		if err != nil || !o.wait {
			return j, err
		}
		return a.WaitRecovery(ctx, o.job, progress)
	case "recovery-status":
		if o.job != "" {
			return a.RecoveryStatus(ctx, o.job)
		}
		return a.Recoveries()
	case "cancel-recovery":
		return a.CancelRecovery(o.job)
	case "undo-recovery-settings":
		return a.UndoRecoverySettings(ctx, o.job)
	}
	return nil, invalid("unknown recovery action")
}
