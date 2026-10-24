package vault

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var ErrRecoveryInteraction = errors.New("administrator authorization is required; use --allow-interaction from a desktop session")

func (j *RecoveryJob) NeedsAdministrator() bool {
	if !j.Selected("apps") {
		return false
	}
	for _, p := range j.Packages {
		if p.Selected && p.Status != "Installed" && (p.Kind == "rpm" || p.Scope != "user") {
			return true
		}
	}
	return false
}

func recoveryUnit(id string) string { return fmt.Sprintf("setupvault-recovery-%d-%s", os.Getuid(), id) }

func (a *App) ApplyRecovery(ctx context.Context, id string, allowInteraction bool) (*RecoveryJob, error) {
	unlock, err := a.lockOperation()
	if err != nil {
		return nil, err
	}
	defer unlock()
	j, err := a.Recovery(id)
	if err != nil {
		return nil, err
	}
	if j.Status != "Ready" && j.Status != "Canceled" && j.Status != "Interrupted" && j.Status != "Partial" && j.Status != "Failed" {
		return j, errors.New("recovery must be ready or stopped before it can start")
	}
	for _, p := range j.Packages {
		if j.Selected("apps") && p.Selected && p.Status == "Blocked" {
			return j, fmt.Errorf("%s: %s; resolve it or deselect the app", p.Name, p.Reason)
		}
	}
	for _, f := range j.Files {
		if j.Selected(f.Group) && f.Status == "Blocked" {
			return j, fmt.Errorf("%s: %s; prepare a new plan or deselect its category", f.Path, f.Reason)
		}
	}
	for _, k := range j.Keys {
		if j.Selected("gnome") && k.Status == "Blocked" {
			return j, fmt.Errorf("%s: %s", k.Path, k.Reason)
		}
	}
	if j.NeedsAdministrator() && !allowInteraction {
		return j, ErrRecoveryInteraction
	}
	destination, err := os.OpenRoot(j.TargetHome)
	if err != nil {
		return j, err
	}
	var space uint64
	for _, f := range j.Files {
		if !j.Selected(f.Group) || f.Status == "Skipped" || f.Status == "Unchanged" || f.Status == "Restored" {
			continue
		}
		current, e := entryHash(destination, f.Path)
		if e != nil {
			destination.Close()
			return j, e
		}
		if current != f.Before && (!f.Saved || current != f.After) {
			destination.Close()
			return j, fmt.Errorf("destination %s changed since review; prepare a new plan", f.Path)
		}
		if f.Bytes > 0 {
			space += uint64(f.Bytes)
		}
		if f.Before != "missing" {
			if info, e := destination.Lstat(f.Path); e == nil && info.Size() > 0 {
				space += uint64(info.Size())
			}
		}
	}
	destination.Close()
	if err = checkRecoverySpace(j.TargetHome, space); err != nil {
		return j, err
	}
	if a.Profile.Schedule != "" {
		if err = a.SetSchedule(ctx, ""); err != nil {
			return j, err
		}
	}
	dir, _ := a.recoveryDir(id)
	_ = os.Remove(filepath.Join(dir, "cancel"))
	if j.NeedsAdministrator() {
		j.Phase = "Authorizing the selected package transaction"
		if err = a.authorizeRecovery(ctx, j); err != nil {
			return j, err
		}
	}
	executable, err := os.Executable()
	if err != nil {
		return j, err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return j, err
	}
	j.Status = "Queued"
	j.Phase = "Recovery queued; closing the TUI leaves it running"
	j.Error = ""
	j.Unit = recoveryUnit(j.ID)
	if err = a.saveRecovery(j); err != nil {
		return j, err
	}
	args := []string{"--user", "--quiet", "--collect", "--service-type=exec", "--unit=" + j.Unit, "--property=UMask=0077", "--property=KillMode=mixed", "--property=TimeoutStopSec=45s", executable, "internal-recovery-worker", a.ConfigDir, a.StateDir, j.ID}
	if _, err = commandOutput(ctx, "systemd-run", args...); err != nil {
		j.Status = "Interrupted"
		j.Error = err.Error()
		_ = a.saveRecovery(j)
		return j, err
	}
	return j, nil
}

func (a *App) RecoveryStatus(ctx context.Context, id string) (*RecoveryJob, error) {
	j, err := a.Recovery(id)
	if err != nil {
		return nil, err
	}
	if j.Active() && time.Since(j.Updated) > 10*time.Second {
		probe, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		out, err := commandOutput(probe, "systemctl", "--user", "is-active", recoveryUnit(id))
		if probe.Err() != nil {
			return j, nil
		}
		if err != nil && strings.TrimSpace(string(out)) != "activating" {
			if j.Privileged {
				rootState, _ := packageJobDir(strconv.Itoa(os.Getuid()), j.ID)
				var r packageResult
				if readLimitedJSON(filepath.Join(rootState, "result.json"), &r, 1<<20) == nil && (r.Status == "Running" || r.Status == "Queued") {
					rootOut, _ := commandOutput(probe, "systemctl", "is-active", packageUnit(strconv.Itoa(os.Getuid()), j.ID))
					if strings.TrimSpace(string(rootOut)) == "active" {
						j.Status = "Finishing transaction"
						j.Phase = "System package transaction is still running"
						return j, nil
					}
				}
			}
			j.Status = "Interrupted"
			j.Error = "Recovery worker stopped. Resume reconciles completed work before continuing."
		}
	}
	if j.Active() {
		if dir, e := a.recoveryDir(j.ID); e == nil {
			if _, e = os.Stat(filepath.Join(dir, "cancel")); e == nil {
				j.Status = "Finishing transaction"
				j.Phase = "Stop requested; the current transaction finishes before stopping"
			}
		}
	}
	return j, nil
}

func (a *App) CancelRecovery(id string) (*RecoveryJob, error) {
	j, err := a.RecoveryStatus(context.Background(), id)
	if err != nil {
		return nil, err
	}
	dir, _ := a.recoveryDir(id)
	if err = writePrivate(filepath.Join(dir, "cancel"), []byte("stop after current transaction\n")); err != nil {
		return j, err
	}
	if !j.Active() {
		j.Status = "Canceled"
		j.Phase = "Recovery canceled"
		err = a.saveRecovery(j)
	} else {
		j.Phase = "Stop requested; any active package commit finishes first"
	}
	return j, err
}

func (a *App) WaitRecovery(ctx context.Context, id string, progress func(Progress)) (*RecoveryJob, error) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		j, err := a.RecoveryStatus(ctx, id)
		if err != nil {
			return nil, err
		}
		if progress != nil {
			progress(Progress{Phase: j.Phase})
		}
		if !j.Active() {
			switch j.Status {
			case "Completed":
				return j, nil
			case "Canceled":
				return j, context.Canceled
			default:
				return j, fmt.Errorf("recovery %s: %s", j.Status, j.Error)
			}
		}
		select {
		case <-ctx.Done():
			return j, ctx.Err()
		case <-ticker.C:
		}
	}
}

func RunRecoveryWorker(ctx context.Context, config, state, id string) error {
	if os.Geteuid() == 0 {
		return errors.New("run user-settings recovery as the desktop user, not root")
	}
	a, err := New(config, state)
	if err != nil {
		return err
	}
	return a.runRecoveryJob(ctx, id)
}

func (a *App) runRecoveryJob(ctx context.Context, id string) (result error) {
	var unlock func()
	var err error
	for attempt := 0; attempt < 100; attempt++ {
		unlock, err = a.lockProfile(id)
		if err == nil {
			break
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		time.Sleep(100 * time.Millisecond)
	}
	if err != nil {
		return err
	}
	defer unlock()
	j, err := a.Recovery(id)
	if err != nil {
		return err
	}
	dir, _ := a.recoveryDir(id)
	canceled := func() bool { _, err := os.Stat(filepath.Join(dir, "cancel")); return err == nil || ctx.Err() != nil }
	j.Status = "Running"
	j.Error = ""
	j.Phase = "Installing selected applications"
	if err = a.saveRecovery(j); err != nil {
		return err
	}
	defer func() {
		if result != nil {
			j.Status = "Failed"
			j.Error = result.Error()
			if errors.Is(result, context.Canceled) {
				j.Status = "Canceled"
				j.Phase = "Stopped at a safe boundary; completed work is retained"
			}
		} else {
			j.Status = "Completed"
			j.Phase = "Recovery completed"
			for _, f := range j.Files {
				if j.Selected(f.Group) && f.Status == "Blocked" {
					j.Status = "Partial"
					j.Error = "Some files need attention; see per-item reasons"
				}
			}
			for _, k := range j.Keys {
				if j.Selected("gnome") && k.Status == "Blocked" {
					j.Status = "Partial"
					j.Error = "Some GNOME settings need attention; see per-item reasons"
				}
			}
			for _, p := range j.Packages {
				if j.Selected("apps") && p.Selected && p.Status != "Installed" {
					j.Status = "Partial"
					j.Error = "Some applications need attention; see per-item reasons"
				}
			}
		}
		if err := a.saveRecovery(j); result == nil && err != nil {
			result = err
		}
		a.Log("Recovery " + j.ID + ": " + j.Status)
	}()
	if canceled() {
		return context.Canceled
	}
	if j.Privileged && j.Selected("apps") {
		rootdir, _ := packageJobDir(strconv.Itoa(os.Getuid()), j.ID)
		for {
			var r packageResult
			if err = readLimitedJSON(filepath.Join(rootdir, "result.json"), &r, 1<<20); err != nil {
				return fmt.Errorf("read authorized installer result: %w", err)
			}
			j.Phase = r.Phase
			if canceled() {
				j.Status = "Finishing transaction"
				j.Phase = "Finishing the current package transaction before stopping"
			}
			if err = a.saveRecovery(j); err != nil {
				return err
			}
			if r.Status == "Failed" {
				return errors.New(r.Error)
			}
			if r.Status == "Completed" {
				break
			}
			out, probeErr := commandOutput(context.Background(), "systemctl", "is-active", packageUnit(strconv.Itoa(os.Getuid()), j.ID))
			if probeErr != nil && strings.TrimSpace(string(out)) != "activating" {
				return errors.New("system package worker stopped; inspect the job and resume")
			}
			time.Sleep(time.Second)
		}
		if canceled() {
			return context.Canceled
		}
	}
	if j.Selected("apps") {
		for i := range j.Packages {
			p := &j.Packages[i]
			if !p.Selected || p.Status == "Installed" {
				continue
			}
			if canceled() {
				return context.Canceled
			}
			if p.Kind == "flatpak" && p.Scope == "user" {
				j.Phase = "Installing user Flatpak " + p.Name
				if err = a.saveRecovery(j); err != nil {
					return err
				}
				if err = checkFlatpakRemote(ctx, *p); err != nil {
					p.Status = "Blocked"
					p.Reason = err.Error()
					continue
				}
				if err = installFlatpak(context.Background(), *p); err != nil {
					p.Status = "Blocked"
					p.Reason = err.Error()
					continue
				}
			}
			var verifyErr error
			if p.Kind == "rpm" {
				_, verifyErr = commandOutput(ctx, "rpm", "-q", "--", p.Name)
			} else {
				args := append([]string{"info"}, flatpakScope(p.Scope)...)
				args = append(args, p.Name+"//"+p.Branch)
				_, verifyErr = commandOutput(ctx, "flatpak", args...)
			}
			if verifyErr != nil {
				p.Status = "Blocked"
				p.Reason = verifyErr.Error()
			} else {
				p.Status = "Installed"
				p.Reason = ""
			}
			if err = a.saveRecovery(j); err != nil {
				return err
			}
		}
	}
	if canceled() {
		return context.Canceled
	}
	j.Status = "Running"
	j.Phase = "Applying selected user files"
	if err = a.saveRecovery(j); err != nil {
		return err
	}
	if err = a.applyRecoveryFiles(ctx, j, canceled); err != nil {
		return err
	}
	if err = a.applyRecoveryKeys(ctx, j, canceled); err != nil {
		return err
	}
	if canceled() {
		return context.Canceled
	}
	return nil
}

func (a *App) ResumeRecovery(ctx context.Context, id string, allowInteraction bool) (*RecoveryJob, error) {
	j, err := a.RecoveryStatus(ctx, id)
	if err != nil {
		return nil, err
	}
	if j.Active() {
		return j, errors.New("recovery is still active")
	}
	j.Status = "Interrupted"
	for i := range j.Files {
		f := &j.Files[i]
		if f.Status == "Blocked" && f.Retryable {
			f.Status = "Pending"
			f.Reason = ""
		}
	}
	// Reconcile packages that a previous committed transaction already installed.
	for i := range j.Packages {
		p := &j.Packages[i]
		if !p.Selected {
			continue
		}
		if p.Status == "Blocked" && p.Kind == "flatpak" {
			p.Status = "Pending"
			p.Reason = ""
		}
		if p.Kind == "rpm" {
			if _, err := commandOutput(ctx, "rpm", "-q", "--", p.Name); err == nil {
				p.Status = "Installed"
				p.Reason = ""
			}
		} else {
			args := append([]string{"info"}, flatpakScope(p.Scope)...)
			args = append(args, p.Name+"//"+p.Branch)
			if _, err := commandOutput(ctx, "flatpak", args...); err == nil {
				p.Status = "Installed"
				p.Reason = ""
			}
		}
	}
	if err = a.saveRecovery(j); err != nil {
		return j, err
	}
	return a.ApplyRecovery(ctx, id, allowInteraction)
}
