package vault

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type Snapshot struct {
	ID       string    `json:"id"`
	ShortID  string    `json:"short_id"`
	Time     time.Time `json:"time"`
	Hostname string    `json:"hostname"`
	Paths    []string  `json:"paths"`
}

type Progress struct {
	Phase      string `json:"phase"`
	Files      uint64 `json:"files"`
	TotalFiles uint64 `json:"total_files"`
	Bytes      uint64 `json:"bytes"`
	TotalBytes uint64 `json:"total_bytes"`
}

type commandError struct {
	Code    int
	Message string
}

func (e *commandError) Error() string { return e.Message }

func (a *App) restic(ctx context.Context, args []string, progress func(Progress)) ([]byte, error) {
	out, err := a.runRestic(ctx, args, progress)
	var ce *commandError
	if !errors.As(err, &ce) || ce.Code != 11 || ctx.Err() != nil {
		return out, err
	}
	// Restic decides which locks are stale; never bypass or force-remove active locks.
	if _, err = a.runRestic(ctx, []string{"unlock"}, nil); err != nil {
		return nil, fmt.Errorf("repository is locked; stale-lock cleanup failed: %w", err)
	}
	out, err = a.runRestic(ctx, args, progress)
	if errors.As(err, &ce) && ce.Code == 11 {
		ce.Message = "Repository is busy. Wait for the other operation to finish, then retry.\n" + ce.Message
	} else if err == nil {
		a.Log("Repository operation succeeded after stale-lock cleanup")
	}
	return out, err
}

func (a *App) runRestic(ctx context.Context, args []string, progress func(Progress)) ([]byte, error) {
	if a.Profile.Repository == "" {
		return nil, errors.New("choose a destination in Connections first")
	}
	password, err := a.Password(ctx)
	if err != nil {
		return nil, err
	}
	if len(password) > 4096 || strings.ContainsAny(password, "\r\n\x00") {
		return nil, errors.New("invalid repository password format")
	}
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	defer r.Close()
	if _, err = w.WriteString(password + "\n"); err != nil {
		w.Close()
		return nil, err
	}
	w.Close()
	command := append([]string{"-r", a.Profile.Repository, "--password-file", "/proc/self/fd/3", "--json"}, args...)
	cmd := exec.CommandContext(ctx, "restic", command...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Let restic remove its repository lock before falling back to a forced stop.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 30 * time.Second
	cmd.ExtraFiles = []*os.File{r}
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "RESTIC_") && !strings.HasPrefix(e, "RCLONE_CONFIG=") {
			cmd.Env = append(cmd.Env, e)
		}
	}
	if a.Profile.RcloneConfig != "" {
		cmd.Env = append(cmd.Env, "RCLONE_CONFIG="+a.Profile.RcloneConfig)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, fmt.Errorf("start restic: %w (install the restic package)", err)
	}
	var output bytes.Buffer
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 65536), 4<<20)
	for scanner.Scan() {
		line := scanner.Bytes()
		if output.Len() < 8<<20 {
			output.Write(line)
			output.WriteByte('\n')
		}
		if progress != nil {
			var p struct {
				Type       string `json:"message_type"`
				Files      uint64 `json:"files_done"`
				TotalFiles uint64 `json:"total_files"`
				Bytes      uint64 `json:"bytes_done"`
				TotalBytes uint64 `json:"total_bytes"`
			}
			if json.Unmarshal(line, &p) == nil && p.Type == "status" {
				progress(Progress{"Backing up", p.Files, p.TotalFiles, p.Bytes, p.TotalBytes})
			}
		}
	}
	scanErr := scanner.Err()
	err = cmd.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if scanErr != nil {
		return nil, fmt.Errorf("read backup engine output: %w", scanErr)
	}
	if err != nil {
		code := -1
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			code = exit.ExitCode()
		}
		message := strings.TrimSpace(stderr.String())
		for _, line := range bytes.Split(stderr.Bytes(), []byte{'\n'}) {
			var entry struct {
				Type    string `json:"message_type"`
				Message string `json:"message"`
			}
			if json.Unmarshal(line, &entry) == nil && entry.Type == "exit_error" && entry.Message != "" {
				message = entry.Message
			}
		}
		message = sensitive.ReplaceAllString(strings.ReplaceAll(message, password, "[REDACTED]"), "$1$2[REDACTED]")
		if len(message) > 2000 {
			message = message[len(message)-2000:]
		}
		if message == "" {
			message = err.Error()
		}
		return output.Bytes(), &commandError{code, message}
	}
	return output.Bytes(), nil
}

func (a *App) initialize(ctx context.Context) error {
	_, err := a.restic(ctx, []string{"cat", "config"}, nil)
	if err == nil {
		return nil
	}
	var ce *commandError
	if !errors.As(err, &ce) || ce.Code != 10 {
		return err
	}
	_, err = a.restic(ctx, []string{"init"}, nil)
	return err
}

func (a *App) lockOperation() (func(), error) {
	if err := os.MkdirAll(a.StateDir, 0700); err != nil {
		return nil, err
	}
	lock, err := os.OpenFile(filepath.Join(a.StateDir, "backup.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return nil, errors.New("another repository operation is already running for this profile; wait for it to finish")
	}
	return func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}, nil
}

func (a *App) Backup(ctx context.Context, progress func(Progress)) (result error) {
	unlock, err := a.lockOperation()
	if err != nil {
		return err
	}
	defer unlock()
	s := a.State()
	s.LastAttempt = time.Now()
	s.Status = "Running"
	s.LastError = ""
	if err = a.saveState(s); err != nil {
		return err
	}
	a.Log("Backup started")
	defer func() {
		if result != nil {
			s.Status = "Failed"
			if errors.Is(result, context.Canceled) {
				s.Status = "Canceled"
			}
			var ce *commandError
			if errors.As(result, &ce) && ce.Code == 3 {
				s.Status = "Partial: some files could not be read"
			}
			s.LastError = result.Error()
			_ = a.saveState(s)
			a.Log(s.Status + ": " + s.LastError)
		}
	}()
	if a.Profile.SystemEnabled {
		if progress != nil {
			progress(Progress{Phase: "Collecting protected system configuration"})
		}
		if out, err := exec.CommandContext(ctx, "systemctl", "start", SystemUnit(os.Getuid())).CombinedOutput(); err != nil {
			return fmt.Errorf("system export failed: %s", strings.TrimSpace(string(out)))
		}
	}
	a.Profile.InventoryPath = a.InventoryDir()
	if err = a.Save(); err != nil {
		return err
	}
	if progress != nil {
		progress(Progress{Phase: "Recording apps and settings"})
	}
	if _, err = a.Collect(ctx); err != nil {
		return err
	}
	sources, err := a.Sources()
	if err != nil {
		return err
	}
	if err = a.initialize(ctx); err != nil {
		return err
	}
	args := []string{"backup", "--tag", "setupvault:" + a.Profile.ID, "--host", a.Profile.Name}
	for _, exclude := range a.Exclusions() {
		args = append(args, "--exclude", exclude)
	}
	args = append(args, "--")
	args = append(args, sources...)
	out, err := a.restic(ctx, args, progress)
	if err != nil {
		return err
	}
	var summary struct {
		Type  string `json:"message_type"`
		ID    string `json:"snapshot_id"`
		Files uint64 `json:"total_files_processed"`
		Bytes uint64 `json:"total_bytes_processed"`
	}
	for _, line := range bytes.Split(out, []byte{'\n'}) {
		var v struct {
			Type string `json:"message_type"`
		}
		if json.Unmarshal(line, &v) == nil && v.Type == "summary" {
			if err = json.Unmarshal(line, &summary); err != nil {
				return err
			}
		}
	}
	if summary.ID == "" {
		return errors.New("backup engine did not confirm a committed snapshot")
	}
	s.LastBackup = time.Now()
	s.Snapshot = summary.ID
	s.Files = summary.Files
	s.Bytes = summary.Bytes
	s.Status = "Verifying"
	marker, err := os.ReadFile(filepath.Join(a.InventoryDir(), "restore-check.txt"))
	if err != nil {
		return err
	}
	hash := sha256.Sum256(marker)
	s.MarkerSHA256 = hex.EncodeToString(hash[:])
	if err = a.saveState(s); err != nil {
		return err
	}
	if progress != nil {
		progress(Progress{Phase: "Checking repository and restoring a sample from storage"})
	}
	if err = a.verify(ctx, s); err != nil {
		return err
	}
	s.LastVerified = time.Now()
	s.Status = "Healthy"
	if _, err = a.restic(ctx, []string{"forget", "--tag", "setupvault:" + a.Profile.ID, "--group-by", "", "--keep-last", strconv.Itoa(a.Profile.KeepLast), "--prune"}, nil); err != nil {
		return fmt.Errorf("backup verified, but retention failed: %w", err)
	}
	if err = a.saveState(s); err != nil {
		return err
	}
	a.Log("Backup and sample restore verified: " + s.Snapshot)
	return nil
}

func (a *App) verify(ctx context.Context, s State) error {
	if _, err := a.restic(ctx, []string{"check"}, nil); err != nil {
		return fmt.Errorf("repository check failed: %w", err)
	}
	if s.Snapshot == "" || s.MarkerSHA256 == "" {
		return errors.New("no completed backup with a restore marker to verify")
	}
	out, err := a.restic(ctx, []string{"dump", s.Snapshot, filepath.Join(a.Profile.InventoryPath, "restore-check.txt")}, nil)
	if err != nil {
		return fmt.Errorf("sample restore failed: %w", err)
	}
	// restic's binary dump is collected by a line scanner; preserve its final newline.
	hash := sha256.Sum256(out)
	if hex.EncodeToString(hash[:]) != s.MarkerSHA256 {
		return errors.New("restored sample differs from the backed-up sample")
	}
	return nil
}

func (a *App) Verify(ctx context.Context) error {
	unlock, err := a.lockOperation()
	if err != nil {
		return err
	}
	defer unlock()
	s := a.State()
	if err := a.verify(ctx, s); err != nil {
		s.Status = "Verification failed"
		if errors.Is(err, context.Canceled) {
			s.Status = "Verification canceled"
		}
		s.LastError = err.Error()
		_ = a.saveState(s)
		a.Log(s.Status + ": " + s.LastError)
		return err
	}
	s.LastVerified = time.Now()
	if !s.LastAttempt.After(s.LastBackup) {
		s.Status = "Healthy"
		s.LastError = ""
	} else {
		if s.Status == "Verification failed" || s.Status == "Verification canceled" {
			s.LastError = "The saved snapshot passed verification, but a newer backup attempt did not complete. Run a new backup."
		}
		s.Status = "Verified; backup incomplete"
	}
	a.Log("Repository and sample restore verified: " + s.Snapshot)
	return a.saveState(s)
}

func (a *App) Snapshots(ctx context.Context) ([]Snapshot, error) {
	out, err := a.restic(ctx, []string{"snapshots"}, nil)
	if err != nil {
		return nil, err
	}
	var snapshots []Snapshot
	if err = json.Unmarshal(out, &snapshots); err != nil {
		return nil, err
	}
	if snapshots == nil {
		snapshots = []Snapshot{}
	}
	return snapshots, nil
}

func (a *App) Restore(ctx context.Context, snapshot, target string) error {
	if snapshot == "" || strings.HasPrefix(snapshot, "-") {
		return errors.New("choose a snapshot first")
	}
	if !filepath.IsAbs(target) || filepath.Clean(target) == "/" || filepath.Clean(target) == filepath.Clean(a.Profile.Home) {
		return errors.New("choose a new absolute restore folder, not / or your home folder")
	}
	unlock, err := a.lockOperation()
	if err != nil {
		return err
	}
	defer unlock()
	if info, err := os.Lstat(target); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return errors.New("restore folder must not be a symbolic link")
	}
	if entries, err := os.ReadDir(target); err == nil && len(entries) > 0 {
		return errors.New("restore folder must be empty; existing files will not be overwritten")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(target, 0700); err != nil {
		return err
	}
	_, err = a.restic(ctx, []string{"restore", snapshot, "--target", target}, nil)
	if err == nil {
		a.Log("Restored snapshot " + snapshot + " to " + target)
	}
	return err
}
