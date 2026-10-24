package vault

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func recoveryFixture(t *testing.T) (*App, *RecoveryJob, string) {
	t.Helper()
	requireRestic(t)
	source := fixture(t)
	source.Profile.Selected = []string{"settings", "manual"}
	for _, path := range []string{".zprofile", ".inputrc", ".p10k.zsh"} {
		put(t, filepath.Join(source.Profile.Home, path), "captured "+path)
	}
	put(t, filepath.Join(source.Profile.Home, ".local/bin/tool"), "#!/bin/sh\ntouch /this-script-must-never-run\n")
	if err := os.Chmod(filepath.Join(source.Profile.Home, ".local/bin/tool"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("tool", filepath.Join(source.Profile.Home, ".local/bin/tool-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("missing", filepath.Join(source.Profile.Home, ".local/bin/missing-link")); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(source.Profile.Home, ".config/rclone/rclone.conf"), "old connection")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := source.Backup(ctx, nil); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "recovery.age")
	if err := source.Export(ctx, file); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "new-home")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", target)
	app, err := New(filepath.Join(target, ".config/setupvault"), filepath.Join(target, ".local/state/setupvault"))
	if err != nil {
		t.Fatal(err)
	}
	if err = app.ImportRecovery(ctx, file, testPassword, false); err != nil {
		t.Fatal(err)
	}
	put(t, filepath.Join(target, ".config/example/preferences.json"), "target setting")
	put(t, filepath.Join(target, ".config/rclone/rclone.conf"), "new connection")
	job, err := app.PlanRecovery(ctx, "latest", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return app, job, target
}

func TestGuidedRecoveryPlansAppliesAndUndoesSettings(t *testing.T) {
	app, job, target := recoveryFixture(t)
	if job.Status != "Ready" {
		t.Fatal(job.Status)
	}
	original, err := os.ReadFile(filepath.Join(target, ".config/example/preferences.json"))
	if err != nil || string(original) != "target setting" {
		t.Fatal("planning overwrote a live file", err)
	}
	blocked := false
	for _, f := range job.Files {
		if f.Path == ".local/bin/missing-link" && f.Status == "Blocked" {
			blocked = true
		}
	}
	if !blocked {
		t.Fatal("unresolved manual symlink was not identified")
	}
	job, err = app.SelectRecovery(context.Background(), job.ID, nil, nil, []string{".local/bin/missing-link"})
	if err != nil {
		t.Fatal(err)
	}
	if err = app.runRecoveryJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	job, err = app.Recovery(job.ID)
	if err != nil || job.Status != "Completed" {
		t.Fatalf("recovery failed: %+v %v", job, err)
	}
	for _, path := range []string{".zprofile", ".inputrc", ".p10k.zsh", ".config/example/preferences.json", ".local/bin/tool"} {
		if _, err := os.Stat(filepath.Join(target, path)); err != nil {
			t.Fatal("missing applied file", path, err)
		}
	}
	info, err := os.Stat(filepath.Join(target, ".local/bin/tool"))
	if err != nil || info.Mode().Perm() != 0755 {
		t.Fatal("executable mode lost", err)
	}
	if data, _ := os.ReadFile(filepath.Join(target, ".config/rclone/rclone.conf")); string(data) != "new connection" {
		t.Fatal("current cloud credentials were overwritten")
	}
	put(t, filepath.Join(target, ".zprofile"), "edited after recovery")
	undone, err := app.UndoRecoverySettings(context.Background(), job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if undone.Status != "Undo needs attention" {
		t.Fatal("post-recovery edit not reported", undone.Status)
	}
	if data, _ := os.ReadFile(filepath.Join(target, ".config/example/preferences.json")); string(data) != "target setting" {
		t.Fatal("original target setting not restored")
	}
	if data, _ := os.ReadFile(filepath.Join(target, ".zprofile")); string(data) != "edited after recovery" {
		t.Fatal("Undo overwrote newer user work")
	}
	if _, err := os.Lstat(filepath.Join(target, ".local/bin/tool")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("Undo retained a newly restored executable", err)
	}
}

func TestRecoveryRejectsDestinationDriftAndStageTampering(t *testing.T) {
	app, job, target := recoveryFixture(t)
	var chosen *RecoveryFile
	for i := range job.Files {
		f := &job.Files[i]
		if f.Path == ".inputrc" {
			chosen = f
		}
		if f.Status == "Blocked" {
			f.Status = "Skipped"
		}
	}
	if chosen == nil {
		t.Fatal("fixture .inputrc missing")
	}
	dir, _ := app.recoveryDir(job.ID)
	put(t, filepath.Join(dir, "stage", chosen.Source), "modified staged content")
	put(t, filepath.Join(target, ".config/example/preferences.json"), "new work since plan")
	if err := app.saveRecovery(job); err != nil {
		t.Fatal(err)
	}
	if err := app.runRecoveryJob(context.Background(), job.ID); err != nil {
		t.Fatal(err)
	}
	got, err := app.Recovery(job.ID)
	if err != nil || got.Status != "Partial" {
		t.Fatal("unsafe changes not reported", err)
	}
	if _, err := os.Stat(filepath.Join(target, ".inputrc")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("tampered staged file was applied", err)
	}
	if data, _ := os.ReadFile(filepath.Join(target, ".config/example/preferences.json")); string(data) != "new work since plan" {
		t.Fatal("new user work overwritten")
	}
}

func TestRecoveryPathAndPackageTrustBoundaries(t *testing.T) {
	app := fixture(t)
	for _, id := range []string{"../root", "a/../../root", "", strings.Repeat("x", 32)} {
		if _, err := app.Recovery(id); err == nil {
			t.Fatal("invalid job ID accepted", id)
		}
	}
	for _, name := range []string{"--allowerasing", "/tmp/evil.rpm", "@all", "evil.rpm", "foo;touch bad", "foo\nbar"} {
		if validPackage(name) {
			t.Fatal("unsafe package identifier accepted", name)
		}
	}
	for _, action := range []string{"Remove", "Downgrade", "Erase"} {
		raw, _ := json.Marshal(dnfTransaction{Version: "1.0", RPMs: []dnfRPM{{NEVRA: "hello-1-1.x86_64", Action: action}}})
		if _, err := canonicalDNF(raw); err == nil {
			t.Fatal("destructive package transaction accepted", action)
		}
	}
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if err = root.Symlink(t.TempDir(), "escape"); err != nil {
		t.Fatal(err)
	}
	if _, err = entryHash(root, "escape/file"); err == nil {
		t.Fatal("destination symlink escape accepted")
	}
}

func TestAsyncRecoveryWaitTimeoutLeavesJobQueued(t *testing.T) {
	app := fixture(t)
	if err := app.Save(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(app.StateDir, "recovery", strings.Repeat("a", 32))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	j := &RecoveryJob{Version: 1, ID: filepath.Base(dir), ProfileID: app.Profile.ID, Status: "Ready", TargetHome: app.Profile.Home, Groups: []string{"settings"}, Created: time.Now()}
	if err := app.saveRecovery(j); err != nil {
		t.Fatal(err)
	}
	fakeCommand(t, "systemd-run", "exit 0\n")
	got, err := app.ApplyRecovery(context.Background(), j.ID, false)
	if err != nil || got.Status != "Queued" {
		t.Fatal("job not queued asynchronously", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err = app.WaitRecovery(ctx, j.ID, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("waiting deadline not returned", err)
	}
	got, err = app.Recovery(j.ID)
	if err != nil || got.Status != "Queued" {
		t.Fatal("waiting timeout canceled the job", err)
	}
	if err = app.Backup(context.Background(), nil); err == nil {
		t.Fatal("backup allowed while recovery is queued")
	}
}
