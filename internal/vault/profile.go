package vault

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"filippo.io/age"
)

type Profile struct {
	Version       int      `json:"version"`
	ID            string   `json:"id"`
	Name          string   `json:"name"`
	Home          string   `json:"home"`
	Repository    string   `json:"repository"`
	RcloneConfig  string   `json:"rclone_config,omitempty"`
	Selected      []string `json:"selected"`
	CustomPaths   []string `json:"custom_paths,omitempty"`
	Excludes      []string `json:"excludes,omitempty"`
	Schedule      string   `json:"schedule,omitempty"`
	KeepLast      int      `json:"keep_last"`
	SystemEnabled bool     `json:"system_enabled"`
	InventoryPath string   `json:"inventory_path,omitempty"`
}

type State struct {
	Repository     string    `json:"repository"`
	Status         string    `json:"status"`
	LastAttempt    time.Time `json:"last_attempt,omitempty"`
	LastBackup     time.Time `json:"last_backup,omitempty"`
	LastVerified   time.Time `json:"last_verified,omitempty"`
	LastConnection time.Time `json:"last_connection,omitempty"`
	Snapshot       string    `json:"snapshot,omitempty"`
	LastError      string    `json:"last_error,omitempty"`
	MarkerSHA256   string    `json:"marker_sha256,omitempty"`
	Files          uint64    `json:"files"`
	Bytes          uint64    `json:"bytes"`
}

type App struct {
	ConfigDir string
	StateDir  string
	Profile   Profile
	// Tests provide an in-memory password; normal operation uses Secret Service.
	password string
}

func New(configDir, stateDir string) (*App, error) {
	if configDir == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return nil, err
		}
		configDir = filepath.Join(dir, "setupvault")
	}
	if stateDir == "" {
		dir := os.Getenv("XDG_STATE_HOME")
		if dir == "" {
			home, err := os.UserHomeDir()
			if err != nil {
				return nil, err
			}
			dir = filepath.Join(home, ".local", "state")
		}
		stateDir = filepath.Join(dir, "setupvault")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	configDir, err = filepath.Abs(configDir)
	if err != nil {
		return nil, err
	}
	stateDir, err = filepath.Abs(stateDir)
	if err != nil {
		return nil, err
	}
	name, _ := os.Hostname()
	a := &App{ConfigDir: configDir, StateDir: stateDir, Profile: Profile{
		Version: 1, ID: randomID(), Name: name, Home: home, KeepLast: 7,
		Selected: []string{"gnome", "apps", "settings"},
	}}
	a.Profile.Excludes = DefaultExclusions(home)
	data, err := os.ReadFile(a.ProfilePath())
	if err == nil {
		if err = json.Unmarshal(data, &a.Profile); err != nil {
			return nil, fmt.Errorf("read profile: %w", err)
		}
		if err = a.Profile.Validate(); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return a, nil
}

func randomID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func (a *App) ProfilePath() string  { return filepath.Join(a.ConfigDir, "profile.json") }
func (a *App) InventoryDir() string { return filepath.Join(a.StateDir, "inventory") }

func (p Profile) Validate() error {
	if p.Version != 1 {
		return fmt.Errorf("unsupported profile version %d", p.Version)
	}
	if len(p.ID) != 32 {
		return errors.New("invalid profile ID")
	}
	if _, err := hex.DecodeString(p.ID); err != nil {
		return errors.New("invalid profile ID")
	}
	if p.KeepLast < 1 || p.KeepLast > 10000 {
		return errors.New("keep_last must be between 1 and 10000")
	}
	if !filepath.IsAbs(p.Home) {
		return errors.New("home must be an absolute path")
	}
	for _, s := range append(append([]string{p.Name, p.Home, p.Repository, p.Schedule, p.RcloneConfig, p.InventoryPath}, p.CustomPaths...), p.Excludes...) {
		if strings.IndexFunc(s, func(r rune) bool { return r < 32 || r == 127 }) >= 0 {
			return errors.New("profile values must not contain control characters")
		}
	}
	for _, id := range p.Selected {
		if id != "apps" && id != "settings" && id != "gnome" && id != "manual" && id != "system" {
			return errors.New("unknown contents group")
		}
	}
	if strings.HasPrefix(p.Repository, "rclone:") {
		remote, folder, ok := strings.Cut(strings.TrimPrefix(p.Repository, "rclone:"), ":")
		if !ok || remote == "" || folder == "" || strings.ContainsAny(remote, " /\\:=") || strings.HasPrefix(remote, "-") {
			return errors.New("use rclone:remote:dedicated-folder")
		}
		for _, part := range strings.Split(folder, "/") {
			if part == ".." {
				return errors.New("cloud folder must not contain '..'")
			}
		}
	}
	for _, path := range p.CustomPaths {
		if !filepath.IsAbs(path) {
			return errors.New("custom paths must be absolute")
		}
	}
	if p.Repository != "" && !filepath.IsAbs(p.Repository) && !strings.HasPrefix(p.Repository, "rclone:") {
		return errors.New("choose an absolute local folder or rclone:remote:folder destination")
	}
	return nil
}

func writeJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return writePrivate(path, append(data, '\n'))
}

func writePrivate(path string, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".setupvault-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

func (a *App) Save() error {
	if err := a.Profile.Validate(); err != nil {
		return err
	}
	return writeJSON(a.ProfilePath(), a.Profile)
}
func (a *App) State() State {
	s := State{Repository: a.Profile.Repository, Status: "Not backed up"}
	data, err := os.ReadFile(filepath.Join(a.StateDir, "state.json"))
	if err == nil {
		var saved State
		if json.Unmarshal(data, &saved) == nil && saved.Repository == a.Profile.Repository {
			if saved.Status == "Running" || saved.Status == "Verifying" {
				if f, err := os.OpenFile(filepath.Join(a.StateDir, "backup.lock"), os.O_RDWR, 0600); err == nil {
					if syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB) == nil {
						saved.Status = "Interrupted"
						_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
					}
					f.Close()
				}
			}
			return saved
		}
	}
	return s
}
func (a *App) saveState(s State) error {
	s.Repository = a.Profile.Repository
	return writeJSON(filepath.Join(a.StateDir, "state.json"), s)
}

func (a *App) StorePassword(ctx context.Context, password string) error {
	return a.storePassword(ctx, password, true)
}

func (a *App) storePassword(ctx context.Context, password string, verifyRepository bool) error {
	if len(password) < 12 || len(password) > 4096 || strings.ContainsAny(password, "\r\n\x00") {
		return errors.New("use a recovery password of 12–4096 characters, on one line")
	}
	if verifyRepository && a.Profile.Repository != "" {
		probe := *a
		probe.password = password
		if _, err := probe.restic(ctx, []string{"cat", "config"}, nil); err != nil {
			var ce *commandError
			if !errors.As(err, &ce) || ce.Code != 10 {
				return fmt.Errorf("password was not saved because the destination could not be unlocked: %w", err)
			}
		}
	}
	cmd := exec.CommandContext(ctx, "secret-tool", "store", "--label=SetupVault "+a.Profile.Name, "application", "setupvault", "profile", a.Profile.ID)
	cmd.Stdin = strings.NewReader(password)
	if err := cmd.Run(); err != nil {
		return errors.New("could not save password: install libsecret and unlock your desktop keyring")
	}
	return nil
}

func (a *App) Password(ctx context.Context) (string, error) {
	if a.password != "" {
		return a.password, nil
	}
	out, err := exec.CommandContext(ctx, "secret-tool", "lookup", "application", "setupvault", "profile", a.Profile.ID).Output()
	if err != nil || len(out) == 0 {
		return "", errors.New("password unavailable: unlock your desktop keyring or import a recovery profile")
	}
	return strings.TrimSuffix(string(out), "\n"), nil
}

func (a *App) Export(ctx context.Context, path string) error {
	if _, err := os.Lstat(path); err == nil {
		return errors.New("export file already exists; choose a new name")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	password, err := a.Password(ctx)
	if err != nil {
		return err
	}
	recipient, err := age.NewScryptRecipient(password)
	if err != nil {
		return err
	}
	var buf bytes.Buffer
	w, err := age.Encrypt(&buf, recipient)
	if err != nil {
		return err
	}
	if err = json.NewEncoder(w).Encode(a.Profile); err != nil {
		return err
	}
	if err = w.Close(); err != nil {
		return err
	}
	// Export never replaces a user's existing recovery kit.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(buf.Bytes())
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func ReadRecovery(path, password string) (Profile, error) {
	var p Profile
	f, err := os.Open(path)
	if err != nil {
		return p, err
	}
	defer f.Close()
	id, err := age.NewScryptIdentity(password)
	if err != nil {
		return p, err
	}
	id.SetMaxWorkFactor(20)
	r, err := age.Decrypt(io.LimitReader(f, 2<<20), id)
	if err != nil {
		return p, errors.New("cannot unlock recovery profile: check the password and file")
	}
	data, err := io.ReadAll(io.LimitReader(r, (1<<20)+1))
	if err != nil {
		return p, errors.New("recovery profile authentication failed")
	}
	if len(data) > 1<<20 {
		return p, errors.New("recovery profile is too large")
	}
	if err = json.Unmarshal(data, &p); err != nil {
		return p, errors.New("invalid recovery profile")
	}
	if err = p.Validate(); err != nil {
		return p, err
	}
	// Importing a profile must never start a backup on a fresh machine.
	p.Schedule = ""
	p.SystemEnabled = false
	return p, nil
}

func (a *App) Import(ctx context.Context, path, password string) error {
	if _, err := os.Stat(a.ProfilePath()); err == nil {
		return errors.New("a profile already exists; use a separate --config-dir to import safely")
	}
	p, err := ReadRecovery(path, password)
	if err != nil {
		return err
	}
	newHome, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	oldHome := p.Home
	for i, path := range p.CustomPaths {
		if path == oldHome || strings.HasPrefix(path, oldHome+"/") {
			p.CustomPaths[i] = newHome + strings.TrimPrefix(path, oldHome)
		}
	}
	for i, path := range p.Excludes {
		if strings.HasPrefix(path, oldHome+"/") {
			p.Excludes[i] = newHome + strings.TrimPrefix(path, oldHome)
		}
	}
	p.Home = newHome
	p.RcloneConfig = ""
	old := a.Profile
	a.Profile = p
	// The encrypted profile authenticates this password; cloud sign-in may not exist yet.
	if err = a.storePassword(ctx, password, false); err != nil {
		a.Profile = old
		return err
	}
	return a.Save()
}

func (a *App) Log(message string) {
	_ = os.MkdirAll(a.StateDir, 0700)
	f, err := os.OpenFile(filepath.Join(a.StateDir, "activity.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%s  %s\n", time.Now().Format(time.RFC3339), message)
}
