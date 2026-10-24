package vault

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

func unitArg(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, `%`, `%%`, `$`, `$$`).Replace(s) + `"`
}

func (a *App) SetSchedule(ctx context.Context, calendar string) error {
	if strings.ContainsAny(calendar, "\r\n\x00") {
		return errors.New("schedule must be one systemd calendar expression")
	}
	unit := "setupvault-" + a.Profile.ID
	if calendar == "" {
		if out, err := exec.CommandContext(ctx, "systemctl", "--user", "disable", "--now", unit+".timer").CombinedOutput(); err != nil && !strings.Contains(string(out), "does not exist") {
			return fmt.Errorf("disable schedule: %s", strings.TrimSpace(string(out)))
		}
		a.Profile.Schedule = ""
		return a.Save()
	}
	if a.Profile.Repository == "" {
		return errors.New("choose and test a destination before scheduling")
	}
	if _, err := a.Password(ctx); err != nil {
		return err
	}
	if out, err := exec.CommandContext(ctx, "systemd-analyze", "calendar", calendar).CombinedOutput(); err != nil {
		return fmt.Errorf("invalid schedule: %s", strings.TrimSpace(string(out)))
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		return err
	}
	config, err := os.UserConfigDir()
	if err != nil {
		return err
	}
	dir := filepath.Join(config, "systemd/user")
	service := "[Unit]\nDescription=SetupVault encrypted backup\n\n[Service]\nType=oneshot\nExecStart=" + unitArg(binary) + " --config-dir " + unitArg(a.ConfigDir) + " --state-dir " + unitArg(a.StateDir) + " backup\nTimeoutStartSec=4h\nUMask=0077\nNice=10\nRestart=on-failure\nRestartSec=1h\n"
	timer := "[Unit]\nDescription=SetupVault scheduled backup\n\n[Timer]\nOnCalendar=" + calendar + "\nPersistent=true\nUnit=" + unit + ".service\n\n[Install]\nWantedBy=timers.target\n"
	if err = writePrivate(filepath.Join(dir, unit+".service"), []byte(service)); err != nil {
		return err
	}
	if err = writePrivate(filepath.Join(dir, unit+".timer"), []byte(timer)); err != nil {
		return err
	}
	for _, args := range [][]string{{"--user", "daemon-reload"}, {"--user", "enable", "--now", unit + ".timer"}, {"--user", "restart", unit + ".timer"}} {
		if out, err := exec.CommandContext(ctx, "systemctl", args...).CombinedOutput(); err != nil {
			return fmt.Errorf("configure schedule: %s", strings.TrimSpace(string(out)))
		}
	}
	a.Profile.Schedule = calendar
	return a.Save()
}

func (a *App) NextRun(ctx context.Context) string {
	if a.Profile.Schedule == "" {
		return "Not scheduled"
	}
	unit := "setupvault-" + a.Profile.ID + ".timer"
	active, err := exec.CommandContext(ctx, "systemctl", "--user", "is-active", unit).Output()
	if err != nil || strings.TrimSpace(string(active)) != "active" {
		return "Timer inactive — reapply schedule"
	}
	out, err := exec.CommandContext(ctx, "systemctl", "--user", "show", unit, "--property=NextElapseUSecRealtime", "--value").Output()
	if err != nil {
		return "Schedule status unavailable"
	}
	value := strings.TrimSpace(string(out))
	if value == "" {
		return "No next run reported"
	}
	return value
}

func SystemUnit(uid int) string { return "setupvault-system-export@" + strconv.Itoa(uid) + ".service" }

func (a *App) EnableSystem(ctx context.Context) error {
	if _, err := os.Stat("/usr/lib/systemd/system/setupvault-system-export@.service"); err != nil {
		return errors.New("install the SetupVault RPM first; the protected system export helper is not installed")
	}
	if out, err := exec.CommandContext(ctx, "pkexec", "/usr/bin/setupvault", "enable-system").CombinedOutput(); err != nil {
		return fmt.Errorf("administrator access was not enabled: %s", strings.TrimSpace(string(out)))
	}
	a.Profile.SystemEnabled = true
	found := false
	for _, id := range a.Profile.Selected {
		if id == "system" {
			found = true
		}
	}
	if !found {
		a.Profile.Selected = append(a.Profile.Selected, "system")
	}
	return a.Save()
}
