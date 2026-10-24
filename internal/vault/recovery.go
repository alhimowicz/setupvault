package vault

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type RecoveryPackage struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Scope     string `json:"scope,omitempty"`
	Origin    string `json:"origin,omitempty"`
	URL       string `json:"url,omitempty"`
	Key       string `json:"key,omitempty"`
	KeyData   []byte `json:"key_data,omitempty"`
	AddRemote bool   `json:"add_remote,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Arch      string `json:"arch,omitempty"`
	Selected  bool   `json:"selected"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
}

type RecoveryFile struct {
	Path       string `json:"path"`
	Source     string `json:"source"`
	Group      string `json:"group"`
	Bytes      int64  `json:"bytes"`
	Mode       uint32 `json:"mode"`
	Link       string `json:"link,omitempty"`
	Before     string `json:"before"`
	After      string `json:"after"`
	SourceHash string `json:"source_hash"`
	Rewrite    bool   `json:"rewrite,omitempty"`
	Status     string `json:"status"`
	Reason     string `json:"reason,omitempty"`
	Saved      bool   `json:"saved,omitempty"`
	Retryable  bool   `json:"retryable,omitempty"`
}

type RecoveryKey struct {
	Schema string `json:"schema,omitempty"`
	Name   string `json:"name,omitempty"`
	Path   string `json:"path"`
	Value  string `json:"value"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

type RecoveryJob struct {
	Version         int               `json:"version"`
	ID              string            `json:"id"`
	ProfileID       string            `json:"profile_id"`
	Status          string            `json:"status"`
	Phase           string            `json:"phase"`
	Error           string            `json:"error,omitempty"`
	Created         time.Time         `json:"created"`
	Updated         time.Time         `json:"updated"`
	Snapshot        string            `json:"snapshot"`
	SourceHome      string            `json:"source_home"`
	Inventory       string            `json:"inventory"`
	TargetHome      string            `json:"target_home"`
	Groups          []string          `json:"groups"`
	SkipApps        []string          `json:"skip_apps,omitempty"`
	SkipFiles       []string          `json:"skip_files,omitempty"`
	ApprovedSources []string          `json:"approved_sources,omitempty"`
	Packages        []RecoveryPackage `json:"packages"`
	Files           []RecoveryFile    `json:"files"`
	Keys            []RecoveryKey     `json:"keys,omitempty"`
	Notices         []string          `json:"notices,omitempty"`
	DNF             json.RawMessage   `json:"dnf_transaction,omitempty"`
	Unit            string            `json:"unit,omitempty"`
	NeedsLogout     bool              `json:"needs_logout,omitempty"`
	Privileged      bool              `json:"privileged,omitempty"`
	StagedBytes     uint64            `json:"staged_bytes"`
	checkpointTime  time.Time
	checkpointCount int
}

var recoveryID = regexp.MustCompile(`^[a-f0-9]{32}$`)

func (a *App) recoveryDir(id string) (string, error) {
	if !recoveryID.MatchString(id) {
		return "", errors.New("invalid recovery job ID")
	}
	return filepath.Join(a.StateDir, "recovery", id), nil
}

func (a *App) saveRecovery(j *RecoveryJob) error {
	dir, err := a.recoveryDir(j.ID)
	if err != nil {
		return err
	}
	j.Updated = time.Now()
	if err = writeJSON(filepath.Join(dir, "job.json"), j); err != nil {
		return err
	}
	if err = writePrivate(filepath.Join(dir, "checkpoints.jsonl"), nil); err != nil {
		return err
	}
	j.checkpointTime = time.Now()
	j.checkpointCount = 0
	return nil
}

func readLimitedJSON(path string, dst any, limit int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return err
	}
	if int64(len(data)) > limit {
		return errors.New("recovery metadata is too large")
	}
	return json.Unmarshal(data, dst)
}

func (a *App) Recovery(id string) (*RecoveryJob, error) {
	dir, err := a.recoveryDir(id)
	if err != nil {
		return nil, err
	}
	var j RecoveryJob
	if err = readLimitedJSON(filepath.Join(dir, "job.json"), &j, 64<<20); err != nil {
		return nil, err
	}
	if j.ID != id || j.Version != 1 || j.ProfileID != a.Profile.ID || !filepath.IsAbs(j.TargetHome) || j.TargetHome == "/" {
		return nil, errors.New("recovery job does not match this profile")
	}
	if j.TargetHome != a.Profile.Home {
		return nil, errors.New("recovery target no longer matches the current profile home")
	}
	if err = a.readRecoveryCheckpoints(&j, dir); err != nil {
		return nil, err
	}
	return &j, nil
}

func (a *App) Recoveries() ([]RecoveryJob, error) {
	entries, err := os.ReadDir(filepath.Join(a.StateDir, "recovery"))
	if errors.Is(err, os.ErrNotExist) {
		return []RecoveryJob{}, nil
	}
	if err != nil {
		return nil, err
	}
	var jobs []RecoveryJob
	for _, e := range entries {
		if !e.IsDir() || !recoveryID.MatchString(e.Name()) {
			continue
		}
		j, err := a.Recovery(e.Name())
		if err == nil {
			jobs = append(jobs, *j)
		}
	}
	return jobs, nil
}

func recoveryActive(s string) bool {
	return s == "Queued" || s == "Running" || s == "Finishing transaction"
}
func (j *RecoveryJob) Active() bool { return recoveryActive(j.Status) }
func (j *RecoveryJob) Selected(group string) bool {
	for _, g := range j.Groups {
		if group == g {
			return true
		}
	}
	return false
}

func (a *App) PlanRecovery(ctx context.Context, snapshot string, groups []string, progress func(Progress)) (job *RecoveryJob, result error) {
	unlock, err := a.lockOperation()
	if err != nil {
		return nil, err
	}
	defer unlock()
	if err = a.noActiveRecovery(""); err != nil {
		return nil, err
	}
	snapshots, err := a.Snapshots(ctx)
	if err != nil {
		return nil, err
	}
	var chosen *Snapshot
	for i := range snapshots {
		s := &snapshots[i]
		if snapshot == "latest" && len(s.Tags) > 0 {
			belongs := false
			for _, tag := range s.Tags {
				if tag == "setupvault:"+a.Profile.ID {
					belongs = true
				}
			}
			if !belongs {
				continue
			}
		}
		if snapshot == "latest" {
			if chosen == nil || s.Time.After(chosen.Time) {
				chosen = s
			}
		} else if s.ID == snapshot || strings.HasPrefix(s.ID, snapshot) {
			if chosen != nil {
				return nil, errors.New("ambiguous snapshot ID")
			}
			chosen = s
		}
	}
	if chosen == nil {
		return nil, errors.New("snapshot not found")
	}
	j := &RecoveryJob{Version: 1, ID: randomID(), ProfileID: a.Profile.ID, Status: "Preparing", Phase: "Restoring and verifying private staging data", Created: time.Now(), Snapshot: chosen.ID, TargetHome: a.Profile.Home}
	dir, _ := a.recoveryDir(j.ID)
	defer func() {
		if result != nil && j.Status == "Preparing" {
			j.Status = "Failed"
			j.Error = result.Error()
			_ = a.saveRecovery(j)
		}
	}()
	if err = os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	if progress != nil {
		progress(Progress{Phase: j.Phase})
	}
	stage := filepath.Join(dir, "stage")
	stats, err := a.restic(ctx, []string{"stats", "--mode", "restore-size", chosen.ID}, nil)
	if err != nil {
		return j, err
	}
	var estimate struct {
		Bytes uint64 `json:"total_size"`
	}
	if err = json.Unmarshal(stats, &estimate); err != nil {
		return j, err
	}
	j.StagedBytes = estimate.Bytes
	if err = checkRecoverySpace(dir, estimate.Bytes); err != nil {
		return j, err
	}
	if _, err = a.restic(ctx, []string{"restore", chosen.ID, "--target", stage, "--verify"}, progress); err != nil {
		j.Status = "Failed"
		j.Error = err.Error()
		_ = a.saveRecovery(j)
		return j, err
	}
	var source Profile
	var inventory string
	err = filepath.WalkDir(stage, func(path string, e os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if e.IsDir() || e.Name() != "profile.json" {
			return nil
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(path), "restore-check.txt")); err != nil {
			return nil
		}
		r, err := os.OpenRoot(stage)
		if err != nil {
			return err
		}
		defer r.Close()
		rel, _ := filepath.Rel(stage, path)
		data, err := r.ReadFile(rel)
		if err != nil {
			return err
		}
		var p Profile
		if len(data) > 1<<20 || json.Unmarshal(data, &p) != nil || p.Validate() != nil || p.ID != a.Profile.ID {
			return nil
		}
		if inventory != "" {
			return errors.New("multiple recovery inventories found")
		}
		source = p
		inventory = filepath.Dir(path)
		return nil
	})
	if err != nil || inventory == "" {
		if err == nil {
			err = errors.New("snapshot has no matching SetupVault recovery inventory")
		}
		return nil, err
	}
	j.SourceHome = source.Home
	if source.Home == "/" || j.TargetHome == "/" {
		return nil, errors.New("recovery requires a user home, not the filesystem root")
	}
	j.Inventory, _ = filepath.Rel(stage, inventory)
	if groups == nil {
		for _, g := range source.Selected {
			if g != "system" {
				groups = append(groups, g)
			}
		}
		groups = append(groups, "custom")
	}
	for _, g := range groups {
		if g != "gnome" && g != "apps" && g != "settings" && g != "manual" && g != "custom" && g != "system" {
			return nil, fmt.Errorf("unknown recovery group %q", g)
		}
	}
	j.Groups = groups
	j.Notices = append(j.Notices, "System configuration is staged for selective review; live system files and login shell are unchanged.")
	if progress != nil {
		progress(Progress{Phase: "Comparing settings and resolving applications"})
	}
	if err = a.planRecoveryFiles(ctx, j, stage, &source); err != nil {
		return nil, err
	}
	if err = a.planRecoveryPackages(ctx, j, inventory, dir); err != nil {
		return nil, err
	}
	j.Keys, err = planRecoveryKeys(ctx, j, inventory)
	if err != nil {
		return nil, err
	}
	j.Status = "Ready"
	j.Phase = "Review changes before starting recovery"
	if err = a.saveRecovery(j); err != nil {
		return nil, err
	}
	return j, nil
}

func (a *App) noActiveRecovery(except string) error {
	jobs, err := a.Recoveries()
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if j.ID != except && j.Active() {
			if current, e := a.RecoveryStatus(context.Background(), j.ID); e == nil {
				j = *current
			}
		}
		if j.ID != except && j.Active() {
			return fmt.Errorf("recovery %s is already active", j.ID)
		}
	}
	return nil
}
