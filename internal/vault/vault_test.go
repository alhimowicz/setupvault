package vault

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const testPassword = "fixture-only-recovery-password"

func fixture(t *testing.T) *App {
	t.Helper()
	dir := t.TempDir()
	a, err := New(filepath.Join(dir, "config"), filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	a.Profile.Home = filepath.Join(dir, "home")
	a.Profile.Repository = filepath.Join(dir, "repository")
	a.Profile.Selected = []string{"settings"}
	a.Profile.Excludes = DefaultExclusions(a.Profile.Home)
	a.password = testPassword
	put(t, filepath.Join(a.Profile.Home, ".config/example/preferences.json"), `{"theme":"dark"}`)
	return a
}

func put(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
}

func requireRestic(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic is required for backup/restore integration tests")
	}
}

func roundTrip(t *testing.T, a *App) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	h := a.Profile.Home
	for _, p := range []string{".config/example/cache/cached.bin", ".config/example/CachedData/compiled.bin", ".config/example/CachedExtensionVSIXs/extension.vsix", ".config/google-chrome/Default/Preferences", "Documents/personal.txt", ".config/dconf/user"} {
		put(t, filepath.Join(h, p), "must not be backed up")
	}
	put(t, filepath.Join(h, ".var/app/org.example.App/config/settings.ini"), "flatpak preference")
	if err := a.TestConnection(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.Backup(ctx, nil); err != nil {
		t.Fatal(err)
	}
	s := a.State()
	if s.Status != "Healthy" || s.Snapshot == "" || s.LastVerified.IsZero() {
		t.Fatalf("backup not verified: %+v", s)
	}
	if err := a.Verify(ctx); err != nil {
		t.Fatal(err)
	}
	// A fresh profile can discover an existing repository without its original local state.
	originalID := a.Profile.ID
	a.Profile.ID = randomID()
	snapshots, err := a.Snapshots(ctx)
	a.Profile.ID = originalID
	if err != nil || len(snapshots) != 1 {
		t.Fatalf("snapshot discovery: %v, %d snapshots", err, len(snapshots))
	}
	target := filepath.Join(t.TempDir(), "restore")
	if err := a.Restore(ctx, snapshots[0].ID, target); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{".config/example/preferences.json", ".var/app/org.example.App/config/settings.ini"} {
		want, err := os.ReadFile(filepath.Join(h, p))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(target, h, p))
		if err != nil || !bytes.Equal(want, got) {
			t.Fatalf("restored %s differs: %s, %v", p, got, err)
		}
	}
	for _, p := range []string{".config/example/cache/cached.bin", ".config/example/CachedData/compiled.bin", ".config/example/CachedExtensionVSIXs/extension.vsix", ".config/google-chrome/Default/Preferences", "Documents/personal.txt", ".config/dconf/user"} {
		if _, err := os.Stat(filepath.Join(target, h, p)); !os.IsNotExist(err) {
			t.Fatalf("excluded file restored: %s (%v)", p, err)
		}
	}
	if err := a.Restore(ctx, snapshots[0].ID, target); err == nil {
		t.Fatal("restore overwrote a nonempty directory")
	}
}

func TestLocalBackupRestore(t *testing.T) { requireRestic(t); roundTrip(t, fixture(t)) }

func TestCloudBackupRestore(t *testing.T) {
	remote := os.Getenv("SETUPVAULT_TEST_REMOTE")
	if remote == "" {
		t.Skip("set SETUPVAULT_TEST_REMOTE to an authenticated rclone remote name for a tiny live test")
	}
	if strings.ContainsAny(remote, ":/\\ \r\n") {
		t.Fatal("test remote must be a name, without colon or path")
	}
	requireRestic(t)
	a := fixture(t)
	// Every test owns a new directory; cleanup cannot target a pre-existing backup.
	destination := remote + ":SetupVault-Tests/" + randomID()
	a.Profile.Repository = "rclone:" + destination
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if _, err := a.rclone(ctx, nil, "purge", destination); err != nil {
			t.Errorf("remove test-only repository %s: %v", destination, err)
		}
	})
	roundTrip(t, a)
}

func fakeCommand(t *testing.T, name, script string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+":"+os.Getenv("PATH"))
}

func TestPartialBackupIsNeverHealthy(t *testing.T) {
	a := fixture(t)
	a.Profile.Selected = nil
	old := State{Status: "Healthy", LastBackup: time.Now().Add(-time.Hour), LastVerified: time.Now().Add(-time.Hour), Snapshot: "previous"}
	if err := a.saveState(old); err != nil {
		t.Fatal(err)
	}
	fakeCommand(t, "restic", `
case " $* " in
  *" cat config "*) exit 0 ;;
  *" backup "*) printf '%s\n' 'some files unreadable; access_token=secret-provider-value' >&2; exit 3 ;;
esac
exit 1
`)
	err := a.Backup(context.Background(), nil)
	if err == nil {
		t.Fatal("partial backup returned success")
	}
	s := a.State()
	if !strings.HasPrefix(s.Status, "Partial") || !s.LastBackup.Equal(old.LastBackup) || s.Snapshot != "previous" {
		t.Fatalf("partial snapshot replaced successful state: %+v", s)
	}
	log, _ := os.ReadFile(filepath.Join(a.StateDir, "activity.log"))
	if strings.Contains(string(log), "secret-provider-value") {
		t.Fatal("credential leaked into activity log")
	}
}

func TestLockAndInterruptedState(t *testing.T) {
	a := fixture(t)
	if err := os.MkdirAll(a.StateDir, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(filepath.Join(a.StateDir, "backup.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatal(err)
	}
	if err = a.Backup(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("concurrent backup allowed: %v", err)
	}
	if err = a.saveState(State{Status: "Running"}); err != nil {
		t.Fatal(err)
	}
	if got := a.State().Status; got != "Running" {
		t.Fatal(got)
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}
	if got := a.State().Status; got != "Interrupted" {
		t.Fatal(got)
	}
}

func TestRecoveryProfile(t *testing.T) {
	a := fixture(t)
	a.Profile.Schedule = "daily"
	a.Profile.SystemEnabled = true
	path := filepath.Join(t.TempDir(), "recovery.age")
	if err := a.Export(context.Background(), path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("recovery file is not private", err)
	}
	data, _ := os.ReadFile(path)
	if bytes.Contains(data, []byte(a.Profile.Home)) || bytes.Contains(data, []byte(testPassword)) {
		t.Fatal("recovery profile contains plaintext")
	}
	p, err := ReadRecovery(path, testPassword)
	if err != nil || p.ID != a.Profile.ID || p.Schedule != "" || p.SystemEnabled {
		t.Fatalf("recovery must not activate backups or admin access: %+v, %v", p, err)
	}
	if _, err := ReadRecovery(path, "incorrect-password"); err == nil {
		t.Fatal("wrong password accepted")
	}
	data[len(data)-1] ^= 1
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadRecovery(path, testPassword); err == nil {
		t.Fatal("tampered recovery accepted")
	}
	if err := a.Export(context.Background(), path); err == nil {
		t.Fatal("recovery file overwritten")
	}
}

func TestPasswordPassedPrivately(t *testing.T) {
	a := fixture(t)
	a.Profile.Repository = ""
	capture := t.TempDir()
	t.Setenv("SETUPVAULT_CAPTURE", capture)
	fakeCommand(t, "secret-tool", "printf '%s\\n' \"$@\" > \"$SETUPVAULT_CAPTURE/args\"\ncat > \"$SETUPVAULT_CAPTURE/stdin\"\n")
	if err := a.StorePassword(context.Background(), testPassword); err != nil {
		t.Fatal(err)
	}
	args, _ := os.ReadFile(filepath.Join(capture, "args"))
	input, _ := os.ReadFile(filepath.Join(capture, "stdin"))
	if bytes.Contains(args, []byte(testPassword)) || string(input) != testPassword {
		t.Fatal("password was not passed only through stdin")
	}
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(a.ProfilePath())
	if bytes.Contains(data, []byte(testPassword)) {
		t.Fatal("password saved in profile")
	}
}

func TestImportBeforeCloudSignIn(t *testing.T) {
	a := fixture(t)
	a.Profile.Repository = "rclone:old-cloud:backups/desktop"
	a.Profile.Schedule = "daily"
	file := filepath.Join(t.TempDir(), "recovery.age")
	if err := a.Export(context.Background(), file); err != nil {
		t.Fatal(err)
	}
	b := fixture(t)
	fakeCommand(t, "secret-tool", "cat >/dev/null\n")
	fakeCommand(t, "restic", "echo 'cloud sign-in unavailable' >&2\nexit 1\n")
	if err := b.Import(context.Background(), file, testPassword); err != nil {
		t.Fatal(err)
	}
	if b.Profile.ID != a.Profile.ID || b.Profile.Schedule != "" || b.Profile.Home == a.Profile.Home {
		t.Fatal("profile did not import safely")
	}
	if err := b.Import(context.Background(), file, testPassword); err == nil {
		t.Fatal("import overwrote existing profile")
	}
}

func TestWrongPasswordDoesNotReplaceKeyring(t *testing.T) {
	a := fixture(t)
	fakeCommand(t, "restic", "echo 'wrong password' >&2\nexit 12\n")
	fakeCommand(t, "secret-tool", "exit 99\n")
	if err := a.StorePassword(context.Background(), "new-wrong-password"); err == nil || !strings.Contains(err.Error(), "was not saved") {
		t.Fatalf("keyring password replacement allowed: %v", err)
	}
}

func TestProfileValidationAndDestinationState(t *testing.T) {
	a := fixture(t)
	if err := a.saveState(State{Status: "Healthy"}); err != nil {
		t.Fatal(err)
	}
	a.Profile.Repository += "-different"
	if a.State().Status == "Healthy" {
		t.Fatal("old destination status shown as healthy")
	}
	for _, repo := range []string{"relative", "rclone:", "rclone:x:", "rclone:x:folder/../old", "rclone:x:abc\nother"} {
		a.Profile.Repository = repo
		if err := a.Profile.Validate(); err == nil {
			t.Errorf("invalid destination accepted: %q", repo)
		}
	}
}

func TestDeselectionRemovesOldInventory(t *testing.T) {
	a := fixture(t)
	a.Profile.Selected = nil
	for _, name := range []string{"rpm-packages.txt", "gnome-settings.dconf", "vscode-extensions.txt"} {
		put(t, filepath.Join(a.InventoryDir(), name), "old settings")
	}
	if _, err := a.Collect(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"rpm-packages.txt", "gnome-settings.dconf", "vscode-extensions.txt"} {
		if _, err := os.Stat(filepath.Join(a.InventoryDir(), name)); !os.IsNotExist(err) {
			t.Fatal("deselected data retained", name)
		}
	}
}

func TestRestoreRejectsUnsafeTargets(t *testing.T) {
	a := fixture(t)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(t.TempDir(), link); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/", a.Profile.Home, "relative", link} {
		if err := a.Restore(context.Background(), "abc123", target); err == nil {
			t.Errorf("accepted %q", target)
		}
	}
}

func TestProfileFileAndScheduleSafety(t *testing.T) {
	a := fixture(t)
	if err := a.Save(); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(a.ProfilePath())
	if info.Mode().Perm() != 0600 {
		t.Fatal("profile permissions")
	}
	if err := a.SetSchedule(context.Background(), "daily\nExecStart=/bin/false"); err == nil {
		t.Fatal("unit injection allowed")
	}
	if got := unitArg(`/some path/100%/$file`); got != `"/some path/100%%/$$file"` {
		t.Fatalf("unit path quoting: %s", got)
	}
	var p Profile
	data, _ := os.ReadFile(a.ProfilePath())
	if err := json.Unmarshal(data, &p); err != nil {
		t.Fatal(err)
	}
}

func TestRootHelperBoundaries(t *testing.T) {
	if os.Geteuid() != 0 {
		if err := EnableSystemAccess(); err == nil {
			t.Fatal("unprivileged policy installation allowed")
		}
		if err := ExportSystem("1000"); err == nil {
			t.Fatal("unprivileged system export allowed")
		}
		return
	}
	for _, uid := range []string{"0", "-1", "../../etc", "x"} {
		if err := ExportSystem(uid); err == nil {
			t.Errorf("unsafe export user: %q", uid)
		}
	}
	dir := filepath.Join(t.TempDir(), "unsafe")
	if err := os.Mkdir(dir, 0777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0777); err != nil {
		t.Fatal(err)
	}
	if err := secureRootDirectory(dir, 0, 0750); err == nil {
		t.Fatal("writable root export directory accepted")
	}
}

func TestDesktopTimer(t *testing.T) {
	if os.Getenv("SETUPVAULT_TEST_DESKTOP") != "1" {
		t.Skip("requires a running user systemd manager")
	}
	a := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	config, err := os.UserConfigDir()
	if err != nil {
		t.Fatal(err)
	}
	unit := "setupvault-" + a.Profile.ID
	t.Cleanup(func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.SetSchedule(cleanup, ""); err != nil {
			t.Errorf("disable test timer: %v", err)
		}
		for _, suffix := range []string{".service", ".timer"} {
			if err := os.Remove(filepath.Join(config, "systemd/user", unit+suffix)); err != nil && !os.IsNotExist(err) {
				t.Error(err)
			}
		}
		if out, err := exec.CommandContext(cleanup, "systemctl", "--user", "daemon-reload").CombinedOutput(); err != nil {
			t.Errorf("reload user manager: %s", out)
		}
	})
	if err := a.SetSchedule(ctx, "2099-01-01 00:00:00"); err != nil {
		t.Fatal(err)
	}
	if got := a.NextRun(ctx); !strings.Contains(got, "2099") {
		t.Fatalf("next run not read from systemd: %s", got)
	}
	if err := a.SetSchedule(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if got := a.NextRun(ctx); got != "Not scheduled" {
		t.Fatal(got)
	}
}
