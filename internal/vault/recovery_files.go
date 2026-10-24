package vault

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func below(path, parent string) bool {
	return path == parent || strings.HasPrefix(path, parent+string(os.PathSeparator))
}

func entryHash(root *os.Root, path string) (string, error) {
	info, err := root.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "missing", nil
	}
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		link, err := root.Readlink(path)
		return "link:" + link, err
	}
	if !info.Mode().IsRegular() {
		return "", errors.New("destination is not a regular file or symlink")
	}
	f, err := root.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	hash := sha256.New()
	if _, err = io.Copy(hash, f); err != nil {
		return "", err
	}
	return fmt.Sprintf("%o:%s", info.Mode().Perm(), hex.EncodeToString(hash.Sum(nil))), nil
}

func recoveryGroup(path string, p *Profile) string {
	for _, custom := range p.CustomPaths {
		if below(path, custom) {
			return "custom"
		}
	}
	rel, _ := filepath.Rel(p.Home, path)
	for _, prefix := range []string{"AppImages", ".local/bin", ".local/opt", "opt", ".vst3", ".local/share/yabridge"} {
		if below(rel, prefix) {
			return "manual"
		}
	}
	for _, prefix := range []string{".local/share/gnome-shell", ".local/share/gnome-background-properties", ".local/share/backgrounds", ".local/share/fonts", ".local/share/themes", ".local/share/icons", ".themes", ".icons", ".fonts"} {
		if below(rel, prefix) {
			return "gnome"
		}
	}
	return "settings"
}

func (a *App) protectedRecoveryPath(rel string) bool {
	for _, prefix := range []string{".config/dconf", ".config/rclone", ".config/setupvault", ".local/state/setupvault", ".local/share/flatpak/db", ".local/share/flatpak/repo", ".config/monitors.xml"} {
		if below(rel, prefix) {
			return true
		}
	}
	for _, path := range []string{a.ConfigDir, a.StateDir} {
		if below(path, a.Profile.Home) {
			p, _ := filepath.Rel(a.Profile.Home, path)
			if below(rel, p) {
				return true
			}
		}
	}
	if strings.HasPrefix(rel, ".config/systemd/user/") && strings.Contains(filepath.Base(rel), "setupvault") {
		return true
	}
	return false
}

func (a *App) planRecoveryFiles(ctx context.Context, j *RecoveryJob, stage string, source *Profile) error {
	src, err := os.OpenRoot(stage)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenRoot(j.TargetHome)
	if err != nil {
		return err
	}
	defer dst.Close()
	// Restic prefixes absolute paths beneath the staging directory.
	home := filepath.Join(stage, strings.TrimPrefix(source.Home, "/"))
	if _, err = os.Stat(home); errors.Is(err, os.ErrNotExist) {
		j.Notices = append(j.Notices, "No home files were captured in this snapshot.")
		return nil
	}
	err = filepath.WalkDir(home, func(path string, e os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rel, err := filepath.Rel(home, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		old := filepath.Join(source.Home, rel)
		if a.protectedRecoveryPath(rel) || below(old, source.InventoryPath) {
			if e.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if e.IsDir() {
			return nil
		}
		info, err := e.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 {
			j.Notices = append(j.Notices, "Unsupported special file staged: "+rel)
			return nil
		}
		group := recoveryGroup(old, source)
		sourceRel, _ := filepath.Rel(stage, path)
		f := RecoveryFile{Path: rel, Source: sourceRel, Group: group, Bytes: info.Size(), Mode: uint32(info.Mode().Perm()), Status: "Pending"}
		f.SourceHash, err = entryHash(src, sourceRel)
		if err != nil {
			return err
		}
		f.After = f.SourceHash
		if group == "manual" && info.Mode().IsRegular() {
			input, e := src.Open(sourceRel)
			if e == nil {
				var header [20]byte
				_, _ = io.ReadFull(input, header[:])
				input.Close()
				if string(header[:4]) == "\x7fELF" {
					machine := binary.LittleEndian.Uint16(header[18:])
					if hostArch() == "x86_64" && machine != 62 && machine != 3 || hostArch() == "aarch64" && machine != 183 {
						f.Status = "Blocked"
						f.Reason = "Manual binary architecture does not match this PC"
					}
				}
			}
		}
		f.Before, err = entryHash(dst, rel)
		if err != nil {
			f.Status = "Blocked"
			f.Reason = err.Error()
		}
		if info.Mode()&os.ModeSymlink != 0 {
			link, err := src.Readlink(sourceRel)
			if err != nil {
				return err
			}
			absolute := link
			if !filepath.IsAbs(link) {
				absolute = filepath.Clean(filepath.Join(filepath.Dir(old), link))
			}
			if below(absolute, source.Home) {
				mapped := j.TargetHome
				if absolute != source.Home {
					mapped = filepath.Join(j.TargetHome, strings.TrimPrefix(absolute, source.Home+"/"))
				}
				link, err = filepath.Rel(filepath.Dir(filepath.Join(j.TargetHome, rel)), mapped)
				if err != nil {
					return err
				}
			} else {
				f.Status = "Blocked"
				f.Reason = "Symlink target is outside the restored home; review staged files"
			}
			f.Link = link
			f.After = "link:" + link
		}
		if f.Before == f.After {
			f.Status = "Unchanged"
		}
		j.Files = append(j.Files, f)
		return nil
	})
	if err != nil {
		return err
	}
	for _, p := range source.CustomPaths {
		if !below(p, source.Home) {
			j.Notices = append(j.Notices, "Custom path outside home staged for manual mapping: "+p)
		}
	}
	planned := map[string]bool{}
	for _, f := range j.Files {
		if f.Status != "Blocked" && j.Selected(f.Group) {
			planned[f.Path] = true
		}
	}
	for i := range j.Files {
		f := &j.Files[i]
		if f.Link == "" || f.Status == "Blocked" {
			continue
		}
		target := filepath.Clean(filepath.Join(filepath.Dir(f.Path), f.Link))
		present := planned[target]
		for p := range planned {
			if below(p, target) {
				present = true
				break
			}
		}
		if !present {
			if _, err := dst.Stat(target); err != nil {
				f.Status = "Blocked"
				f.Reason = "Symlink target is not selected or was not captured; review staged files"
			}
		}
	}
	return nil
}

func copyRecoveryEntry(src *os.Root, source string, dst *os.Root, target string, link string) error {
	info, err := src.Lstat(source)
	if err != nil {
		return err
	}
	if err = dst.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return err
	}
	temp := filepath.Join(filepath.Dir(target), ".setupvault-"+randomID())
	defer dst.Remove(temp)
	if info.Mode()&os.ModeSymlink != 0 {
		if link == "" {
			link, err = src.Readlink(source)
			if err != nil {
				return err
			}
		}
		if err = dst.Symlink(link, temp); err != nil {
			return err
		}
	} else {
		if !info.Mode().IsRegular() {
			return errors.New("unsupported recovery file type")
		}
		input, err := src.Open(source)
		if err != nil {
			return err
		}
		defer input.Close()
		output, err := dst.OpenFile(temp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(output, input)
		if copyErr == nil {
			copyErr = output.Chmod(info.Mode().Perm())
		}
		if copyErr == nil {
			copyErr = output.Sync()
		}
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return dst.Rename(temp, target)
}

func (a *App) applyRecoveryFiles(ctx context.Context, j *RecoveryJob, canceled func() bool) error {
	dir, _ := a.recoveryDir(j.ID)
	src, err := os.OpenRoot(filepath.Join(dir, "stage"))
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := os.OpenRoot(j.TargetHome)
	if err != nil {
		return err
	}
	defer dst.Close()
	rollback := filepath.Join(dir, "rollback", "files")
	if err = os.MkdirAll(rollback, 0700); err != nil {
		return err
	}
	old, err := os.OpenRoot(rollback)
	if err != nil {
		return err
	}
	defer old.Close()
	running := map[string]bool{}
	for i := range j.Files {
		f := &j.Files[i]
		if !j.Selected(f.Group) || f.Status == "Unchanged" || f.Status == "Restored" || f.Status == "Blocked" || f.Status == "Undone" || f.Status == "Skipped" {
			continue
		}
		if canceled() {
			return context.Canceled
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		app := recoveryAppKey(f.Path)
		busy, known := running[app]
		if app != "" && !known {
			busy = recoveryAppRunning(ctx, f.Path)
			running[app] = busy
		}
		if busy {
			f.Status = "Blocked"
			f.Retryable = true
			f.Reason = "Close the running app and resume recovery before replacing its settings"
			continue
		}
		current, err := entryHash(dst, f.Path)
		if err != nil {
			f.Status = "Blocked"
			f.Reason = err.Error()
			continue
		}
		if f.Saved && current == f.After {
			f.Status = "Restored"
			continue
		}
		if current != f.Before {
			f.Status = "Blocked"
			f.Reason = "Destination changed since the recovery plan; prepare a new plan"
			continue
		}
		hash, err := entryHash(src, f.Source)
		if err != nil || hash != f.SourceHash {
			f.Status = "Blocked"
			f.Reason = "Staged file changed after verification"
			continue
		}
		if !f.Saved && current != "missing" {
			if err = copyRecoveryEntry(dst, f.Path, old, f.Path, ""); err != nil {
				return err
			}
		}
		f.Saved = true
		f.Status = "Applying"
		j.Phase = "Restoring " + f.Path
		if err = a.checkpointRecovery(j, f, nil); err != nil {
			return err
		}
		if err = copyRecoveryEntry(src, f.Source, dst, f.Path, f.Link); err != nil {
			f.Status = "Blocked"
			f.Retryable = true
			f.Reason = err.Error()
			continue
		}
		hash, err = entryHash(dst, f.Path)
		if err != nil || hash != f.After {
			f.Status = "Blocked"
			f.Reason = "Restored file did not match the plan"
			continue
		}
		f.Status = "Restored"
		f.Retryable = false
		f.Reason = ""
		if err = a.checkpointRecovery(j, f, nil); err != nil {
			return err
		}
	}
	return nil
}

func recoveryAppRunning(ctx context.Context, rel string) bool {
	app := ""
	if strings.HasPrefix(rel, ".config/") {
		app = strings.Split(strings.TrimPrefix(rel, ".config/"), "/")[0]
	}
	if strings.HasPrefix(rel, ".var/app/") {
		id := strings.Split(strings.TrimPrefix(rel, ".var/app/"), "/")[0]
		out, err := commandOutput(ctx, "flatpak", "ps", "--columns=application")
		return err == nil && strings.Contains("\n"+string(out), "\n"+id+"\n")
	}
	if app == "" || app == "dconf" || app == "systemd" {
		return false
	}
	out, err := commandOutput(ctx, "ps", "-u", fmt.Sprint(os.Getuid()), "-o", "comm=")
	if err != nil {
		return false
	}
	for _, name := range strings.Fields(string(out)) {
		if strings.EqualFold(app, name) {
			return true
		}
	}
	return false
}

func recoveryAppKey(rel string) string {
	if strings.HasPrefix(rel, ".config/") {
		return "native:" + strings.Split(strings.TrimPrefix(rel, ".config/"), "/")[0]
	}
	if strings.HasPrefix(rel, ".var/app/") {
		return "flatpak:" + strings.Split(strings.TrimPrefix(rel, ".var/app/"), "/")[0]
	}
	return ""
}

func (a *App) UndoRecoverySettings(ctx context.Context, id string) (*RecoveryJob, error) {
	unlock, err := a.lockOperation()
	if err != nil {
		return nil, err
	}
	defer unlock()
	j, err := a.Recovery(id)
	if err != nil {
		return nil, err
	}
	if j.Active() {
		return nil, errors.New("wait for recovery to stop before undoing settings")
	}
	dir, _ := a.recoveryDir(id)
	dst, err := os.OpenRoot(j.TargetHome)
	if err != nil {
		return nil, err
	}
	defer dst.Close()
	old, err := os.OpenRoot(filepath.Join(dir, "rollback", "files"))
	if err != nil {
		return nil, err
	}
	defer old.Close()
	blocked := false
	for i := len(j.Files) - 1; i >= 0; i-- {
		if ctx.Err() != nil {
			return j, ctx.Err()
		}
		f := &j.Files[i]
		if !f.Saved || f.Status == "Undone" {
			continue
		}
		current, err := entryHash(dst, f.Path)
		if err != nil {
			return j, err
		}
		if current == f.Before {
			f.Status = "Undone"
			continue
		}
		if current != f.After {
			f.Reason = "Changed after recovery; current file was preserved"
			blocked = true
			continue
		}
		if f.Before == "missing" {
			err = dst.Remove(f.Path)
		} else {
			original, checkErr := entryHash(old, f.Path)
			if checkErr != nil || original != f.Before {
				return j, errors.New("settings rollback copy changed; current file was preserved")
			}
			err = copyRecoveryEntry(old, f.Path, dst, f.Path, "")
		}
		if err != nil {
			return j, err
		}
		f.Status = "Undone"
		f.Reason = ""
		if err = a.checkpointRecovery(j, f, nil); err != nil {
			return j, err
		}
	}
	if err = a.undoRecoveryKeys(ctx, j); err != nil {
		return j, err
	}
	for _, k := range j.Keys {
		if k.Status == "Applied" && k.Reason != "" {
			blocked = true
		}
	}
	j.Status = "Undone"
	if blocked {
		j.Status = "Undo needs attention"
	}
	j.Phase = "Settings undo finished; installed packages are unchanged"
	if blocked {
		j.Phase += "; newer file changes were preserved"
	}
	return j, a.saveRecovery(j)
}
