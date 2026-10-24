package vault

import (
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
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
)

type RecoveryRemote struct {
	Name         string `json:"name"`
	URL          string `json:"url"`
	Scope        string `json:"scope"`
	Key          string `json:"key,omitempty"`
	KeyData      []byte `json:"key_data,omitempty"`
	RawKey       string `json:"-"`
	RequiresAuth bool   `json:"requires_auth,omitempty"`
}
type RecoveryInventory struct {
	Version      int                 `json:"version"`
	Architecture string              `json:"architecture"`
	Remotes      []RecoveryRemote    `json:"flatpak_remotes"`
	RPMs         []RecoveryRPMRecord `json:"rpms"`
}
type RecoveryRPMRecord struct {
	Name    string `json:"name"`
	Arch    string `json:"arch"`
	Version string `json:"version"`
	Repo    string `json:"repo"`
	Reason  string `json:"reason"`
}

func (a *App) collectRecoveryInventory(ctx context.Context) error {
	inv := RecoveryInventory{Version: 1, Architecture: hostArch(), Remotes: recoveryRemotes(ctx)}
	if data, err := commandOutput(ctx, "dnf", "repoquery", "--userinstalled", "--queryformat", "%{name}\t%{arch}\t%{evr}\t%{from_repo}\t%{reason}\n"); err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			f := strings.Split(line, "\t")
			if len(f) == 5 {
				inv.RPMs = append(inv.RPMs, RecoveryRPMRecord{Name: f[0], Arch: f[1], Version: f[2], Repo: f[3], Reason: f[4]})
			}
		}
	}
	return writeJSON(filepath.Join(a.InventoryDir(), "recovery-inventory.json"), inv)
}

type dnfRPM struct {
	NEVRA  string `json:"nevra"`
	Action string `json:"action"`
	Repo   string `json:"repo_id"`
	Reason string `json:"reason"`
}
type dnfTransaction struct {
	Version string   `json:"version"`
	RPMs    []dnfRPM `json:"rpms"`
}

var packageToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+:-]{0,255}$`)

func validPackage(s string) bool { return packageToken.MatchString(s) && !strings.HasSuffix(s, ".rpm") }
func hostArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	}
	return runtime.GOARCH
}

func commandOutput(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	cmd.WaitDelay = 2 * time.Second
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	err := cmd.Run()
	if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState != nil && cmd.ProcessState.Success() {
		err = nil
	}
	if err != nil {
		message := sensitive.ReplaceAllString(strings.TrimSpace(stderr.String()), "$1$2[REDACTED]")
		if len(message) > 1500 {
			message = message[len(message)-1500:]
		}
		if message == "" {
			message = err.Error()
		}
		return out.Bytes(), fmt.Errorf("%s: %s", name, message)
	}
	return out.Bytes(), nil
}

func flatpakScope(scope string) []string {
	if scope == "user" {
		return []string{"--user"}
	}
	if scope == "system" || scope == "default" || scope == "" {
		return []string{"--system"}
	}
	return []string{"--installation=" + scope}
}

func recoveryRemotes(ctx context.Context, extra ...string) []RecoveryRemote {
	scopes := []string{"user", "system"}
	for _, scope := range extra {
		if scope != "user" && scope != "system" && scope != "default" && validPackage(scope) {
			scopes = append(scopes, scope)
		}
	}
	if data, err := commandOutput(ctx, "flatpak", "list", "--app", "--columns=installation"); err == nil {
		for _, s := range strings.Fields(string(data)) {
			if s != "user" && s != "system" && s != "default" && validPackage(s) {
				scopes = append(scopes, s)
			}
		}
	}
	var result []RecoveryRemote
	for _, scope := range scopes {
		args := append([]string{"remotes"}, flatpakScope(scope)...)
		args = append(args, "--columns=name,url")
		data, err := commandOutput(ctx, "flatpak", args...)
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			fields := strings.Fields(line)
			if len(fields) < 2 || !validPackage(fields[0]) {
				continue
			}
			r := RecoveryRemote{Name: fields[0], URL: fields[1], Scope: scope}
			r.URL, r.RequiresAuth = publicSourceURL(r.URL)
			base := "/var/lib/flatpak/repo"
			if r.Scope == "user" {
				home, _ := os.UserHomeDir()
				base = filepath.Join(home, ".local/share/flatpak/repo")
				if dataHome := os.Getenv("XDG_DATA_HOME"); dataHome != "" {
					base = filepath.Join(dataHome, "flatpak/repo")
				}
			}
			if data, err := os.ReadFile(filepath.Join(base, r.Name+".trustedkeys.gpg")); err == nil {
				sum := sha256.Sum256(data)
				r.RawKey = hex.EncodeToString(sum[:])
				r.Key = r.RawKey
				if len(data) <= 1<<20 {
					r.KeyData = data
					if identity, e := publicKeyIdentity(ctx, data); e == nil {
						r.Key = identity
					}
				}
			}
			result = append(result, r)
		}
	}
	return result
}

func canonicalDNF(raw []byte) ([]byte, error) {
	var t dnfTransaction
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, err
	}
	if len(t.RPMs) > 10000 {
		return nil, errors.New("too many packages in transaction")
	}
	for _, r := range t.RPMs {
		if !validPackage(r.NEVRA) {
			return nil, errors.New("invalid transaction package")
		}
		if r.Action != "Install" && r.Action != "Upgrade" && r.Action != "Reinstall" && r.Action != "Reason Change" {
			return nil, fmt.Errorf("transaction would %s %s; review it separately", r.Action, r.NEVRA)
		}
	}
	sort.Slice(t.RPMs, func(i, j int) bool { return t.RPMs[i].NEVRA+t.RPMs[i].Action < t.RPMs[j].NEVRA+t.RPMs[j].Action })
	return json.Marshal(t)
}

func reviewedDNF(actual, expected []byte) bool {
	var reviewed, next dnfTransaction
	if json.Unmarshal(expected, &reviewed) != nil {
		return false
	}
	if len(actual) == 0 {
		return true // All reviewed packages may already be installed on resume.
	}
	if json.Unmarshal(actual, &next) != nil || next.Version != reviewed.Version {
		return false
	}
	approved := map[dnfRPM]int{}
	for _, p := range reviewed.RPMs {
		approved[p]++
	}
	for _, p := range next.RPMs {
		if approved[p] == 0 {
			return false
		}
		approved[p]--
	}
	return true
}

func prepareDNF(ctx context.Context, names []string, dir string) (json.RawMessage, error) {
	if len(names) == 0 {
		return nil, nil
	}
	if len(names) > 10000 {
		return nil, errors.New("too many requested packages")
	}
	for _, n := range names {
		if !validPackage(n) {
			return nil, errors.New("invalid RPM identifier")
		}
	}
	args := []string{"--assumeyes", "--setopt=install_weak_deps=False", "--setopt=obsoletes=0", "install", "--no-allow-downgrade", "--store", dir, "--"}
	args = append(args, names...)
	if _, err := commandOutput(ctx, "dnf", args...); err != nil {
		return nil, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "transaction.json"))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	return canonicalDNF(data)
}

func (a *App) planRecoveryPackages(ctx context.Context, j *RecoveryJob, inventory, dir string) error {
	if !j.Selected("apps") {
		return nil
	}
	var enhanced RecoveryInventory
	if data, e := readRecoveryInventory(inventory, "recovery-inventory.json", 4<<20); e == nil {
		if json.Unmarshal(data, &enhanced) != nil {
			return errors.New("invalid recovery inventory")
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	remotes := recoveryRemotes(ctx)
	data, err := readRecoveryInventory(inventory, "rpm-packages.txt", 4<<20)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if len(data) > 4<<20 {
		return errors.New("package inventory too large")
	}
	for _, name := range strings.Fields(string(data)) {
		p := RecoveryPackage{ID: "rpm:" + name, Kind: "rpm", Name: name, Selected: true, Status: "Pending"}
		for _, old := range enhanced.RPMs {
			if old.Name+"."+old.Arch == name {
				p.Origin = old.Repo
				if !strings.Contains(old.Reason, "User") {
					p.Selected = false
					p.Status = "Skipped"
					p.Reason = "Not an explicitly installed app (" + old.Reason + "); review separately"
				}
				break
			}
		}
		if !validPackage(name) {
			p.Status = "Blocked"
			p.Reason = "Invalid package identifier"
		}
		if strings.HasPrefix(name, "kernel") || strings.HasPrefix(name, "grub") || strings.HasPrefix(name, "shim") || strings.Contains(name, "nvidia") || strings.Contains(name, "akmod") || strings.Contains(name, "kmod-") {
			p.Selected = false
			p.Status = "Skipped"
			p.Reason = "Boot or hardware package: review separately"
		}
		if p.Status == "Pending" {
			if _, err := commandOutput(ctx, "rpm", "-q", "--", name); err == nil {
				p.Status = "Installed"
				p.Selected = false
			} else {
				out, err := commandOutput(ctx, "dnf", "repoquery", "--available", "--queryformat", "%{name}.%{arch}\n", "--", name)
				if err != nil || strings.TrimSpace(string(out)) == "" {
					p.Status = "Blocked"
					p.Reason = "Unavailable in configured target repositories; configure its source or deselect it"
				}
			}
		}
		j.Packages = append(j.Packages, p)
	}
	data, err = readRecoveryInventory(inventory, "flatpak-apps.tsv", 4<<20)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		p := RecoveryPackage{Kind: "flatpak", Name: fields[0], Origin: fields[1], Branch: fields[2], Scope: fields[3], Selected: true, Status: "Pending"}
		if p.Scope == "default" {
			p.Scope = "system"
		}
		p.ID = "flatpak:" + p.Scope + ":" + p.Name + ":" + p.Branch
		if !validPackage(p.Name) || !validPackage(p.Origin) || !validPackage(p.Branch) || !validPackage(p.Scope) {
			p.Status = "Blocked"
			p.Reason = "Invalid Flatpak identifier"
		}
		var remote *RecoveryRemote
		if p.Scope != "user" && p.Scope != "system" {
			remotes = append(remotes, recoveryRemotes(ctx, p.Scope)...)
		}
		for i := range remotes {
			r := &remotes[i]
			if r.Name == p.Origin && (r.Scope == p.Scope || r.Scope == "default" && p.Scope == "system") {
				remote = r
				break
			}
		}
		if remote == nil {
			p.Status = "Blocked"
			p.Reason = "Remote identity is not recorded; configure the original source or deselect this app"
			private := false
			for _, old := range enhanced.Remotes {
				if old.Name == p.Origin && (old.Scope == p.Scope || old.Scope == "default" && p.Scope == "system") {
					p.URL = old.URL
					p.Key = old.Key
					p.KeyData = old.KeyData
					private = old.RequiresAuth
					break
				}
			}
			if p.URL == "" {
				p.URL = legacyRemoteURL(inventory, p.Origin, p.Scope)
			}
			if !private && validRecoverySource(p) == nil {
				p.AddRemote = true
				p.Status = "Needs source approval"
				p.Reason = "Approve adding " + p.Scope + " source " + p.Origin + " (" + p.URL + ") before installation"
				for _, id := range j.ApprovedSources {
					if id == p.SourceID() {
						p.Status = "Pending"
						p.Arch = hostArch()
						p.Reason = "Approved new source: " + p.URL
					}
				}
			}
		} else {
			p.URL = remote.URL
			p.Key = remote.Key
			for _, old := range enhanced.Remotes {
				if old.Name == p.Origin && (old.Scope == p.Scope || old.Scope == "default" && p.Scope == "system") && (old.URL != remote.URL || old.Key != "" && old.Key != remote.Key && old.Key != remote.RawKey) {
					p.Status = "Blocked"
					p.Reason = "Flatpak remote identity differs from the backup"
				}
			}
		}
		if p.Status == "Pending" && !p.AddRemote {
			args := append([]string{"info"}, flatpakScope(p.Scope)...)
			args = append(args, p.Name+"//"+p.Branch)
			if _, err := commandOutput(ctx, "flatpak", args...); err == nil {
				p.Status = "Installed"
				p.Selected = false
			} else {
				args = append([]string{"remote-info"}, flatpakScope(p.Scope)...)
				args = append(args, "--show-ref", p.Origin, p.Name+"//"+p.Branch)
				ref, err := commandOutput(ctx, "flatpak", args...)
				parts := strings.Split(strings.TrimSpace(string(ref)), "/")
				if err != nil || len(parts) != 4 {
					p.Status = "Blocked"
					p.Reason = "App metadata could not be resolved"
					if err != nil {
						p.Reason = err.Error()
					}
					if p.Scope != "user" {
						p.Status = "Needs authorization"
						p.Arch = hostArch()
						p.Reason = "System source metadata will be checked by the authorized installer: " + p.Reason
					}
				} else {
					p.Arch = parts[2]
				}
			}
		}
		j.Packages = append(j.Packages, p)
	}
	return a.resolveRecoveryDNF(ctx, j, dir)
}

func (a *App) resolveRecoveryDNF(ctx context.Context, j *RecoveryJob, dir string) error {
	for i := range j.Packages {
		p := &j.Packages[i]
		for _, s := range j.SkipApps {
			if s == p.ID || s == p.Name {
				p.Selected = false
				p.Reason = "Deselected in recovery review"
			}
		}
	}
	var names []string
	for _, p := range j.Packages {
		if p.Selected && p.Kind == "rpm" && p.Status == "Pending" {
			names = append(names, p.Name)
		}
	}
	store := filepath.Join(dir, "dnf-"+randomID())
	defer os.RemoveAll(store)
	transaction, err := prepareDNF(ctx, names, store)
	if err != nil {
		j.Notices = append(j.Notices, err.Error())
		for i := range j.Packages {
			p := &j.Packages[i]
			if p.Kind == "rpm" && p.Selected && p.Status == "Pending" {
				p.Status = "Blocked"
				p.Reason = err.Error()
			}
		}
		return nil
	}
	j.DNF = transaction
	return nil
}

func (a *App) SelectRecovery(ctx context.Context, id string, groups, skip, skipFiles []string, approved ...[]string) (*RecoveryJob, error) {
	unlock, err := a.lockOperation()
	if err != nil {
		return nil, err
	}
	defer unlock()
	j, err := a.Recovery(id)
	if err != nil {
		return nil, err
	}
	if j.Status != "Ready" {
		return nil, errors.New("only a ready plan can be edited")
	}
	if groups != nil {
		for _, g := range groups {
			if g != "apps" && g != "settings" && g != "gnome" && g != "manual" && g != "custom" && g != "system" {
				return nil, errors.New("unknown recovery group")
			}
		}
		j.Groups = groups
	}
	if skip != nil {
		j.SkipApps = skip
	}
	if skipFiles != nil {
		j.SkipFiles = skipFiles
	}
	if len(approved) > 0 && approved[0] != nil {
		j.ApprovedSources, err = approvedRecoverySources(j.Packages, approved[0])
		if err != nil {
			return j, err
		}
	}
	if !j.Selected("apps") {
		j.ApprovedSources = nil
	}
	dir, _ := a.recoveryDir(id)
	stage := filepath.Join(dir, "stage")
	r, err := os.OpenRoot(stage)
	if err != nil {
		return j, err
	}
	defer r.Close()
	data, err := r.ReadFile(filepath.Join(j.Inventory, "profile.json"))
	if err != nil {
		return j, err
	}
	var source Profile
	if json.Unmarshal(data, &source) != nil || source.Validate() != nil || source.ID != j.ProfileID {
		return j, errors.New("invalid staged source profile")
	}
	verified := map[string]RecoveryFile{}
	for _, f := range j.Files {
		verified[f.Path] = f
	}
	j.Files = nil
	j.Keys = nil
	j.Packages = nil
	j.DNF = nil
	if err = a.planRecoveryFiles(ctx, j, stage, &source); err != nil {
		return j, err
	}
	for i := range j.Files {
		f := &j.Files[i]
		old, ok := verified[f.Path]
		if !ok || old.SourceHash != f.SourceHash {
			f.Status = "Blocked"
			f.Reason = "Staged data changed after snapshot verification"
			if ok {
				f.SourceHash = old.SourceHash
				f.After = old.After
			}
		}
	}
	for i := range j.Files {
		f := &j.Files[i]
		for _, path := range j.SkipFiles {
			if path == f.Path {
				f.Status = "Skipped"
				f.Reason = "Deselected in recovery review"
			}
		}
	}
	if err = a.planRecoveryPackages(ctx, j, filepath.Join(stage, j.Inventory), dir); err != nil {
		return j, err
	}
	j.Keys, err = planRecoveryKeys(ctx, j, filepath.Join(stage, j.Inventory))
	if err != nil {
		return j, err
	}
	for i := range j.Packages {
		p := &j.Packages[i]
		for _, s := range skip {
			if s == p.ID || s == p.Name {
				p.Selected = false
				p.Reason = "Deselected in recovery review"
			}
		}
	}
	return j, a.saveRecovery(j)
}
