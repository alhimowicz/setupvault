package vault

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
)

type packageRequest struct {
	Version  int               `json:"version"`
	ID       string            `json:"id"`
	RPMs     []string          `json:"rpms"`
	Expected json.RawMessage   `json:"expected_dnf,omitempty"`
	Flatpaks []RecoveryPackage `json:"flatpaks,omitempty"`
}
type packageResult struct {
	Status string `json:"status"`
	Phase  string `json:"phase"`
	Error  string `json:"error,omitempty"`
}

func validatePackageRequest(r *packageRequest) error {
	if r.Version != 1 || !recoveryID.MatchString(r.ID) || len(r.RPMs) > 10000 || len(r.Flatpaks) > 10000 {
		return errors.New("invalid package recovery request")
	}
	for _, name := range r.RPMs {
		if !validPackage(name) {
			return errors.New("invalid requested RPM identifier")
		}
	}
	if len(r.RPMs) > 0 {
		if _, err := canonicalDNF(r.Expected); err != nil {
			return err
		}
	}
	for _, p := range r.Flatpaks {
		if p.AddRemote {
			if err := validRecoverySource(p); err != nil {
				return err
			}
		}
		if p.Kind != "flatpak" || p.Scope == "user" || !validPackage(p.Scope) || !validPackage(p.Name) || !validPackage(p.Origin) || !validPackage(p.Branch) || p.Arch != "" && !validPackage(p.Arch) {
			return errors.New("invalid system Flatpak request")
		}
	}
	return nil
}

func packageJobDir(uid, id string) (string, error) {
	n, err := strconv.Atoi(uid)
	if err != nil || n <= 0 || !recoveryID.MatchString(id) {
		return "", errors.New("invalid package job owner or ID")
	}
	return filepath.Join("/var/lib/setupvault/recovery", uid, id), nil
}

func packageUnit(uid, id string) string { return "setupvault-recovery-packages-" + uid + "-" + id }

func rootJSON(path string, value any, gid int) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".update-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chown(0, gid); err == nil {
		err = f.Chmod(0640)
	}
	if err == nil {
		err = json.NewEncoder(f).Encode(value)
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

// AuthorizeRecoveryPackages accepts bounded data over stdin, never paths or commands.
func AuthorizeRecoveryPackages(ctx context.Context, input io.Reader) error {
	if os.Geteuid() != 0 {
		return errors.New("administrator authentication required")
	}
	uid := os.Getenv("PKEXEC_UID")
	u, err := user.LookupId(uid)
	if err != nil || uid == "0" {
		return errors.New("start package recovery through pkexec as the desktop user")
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return err
	}
	data, err := io.ReadAll(io.LimitReader(input, (8<<20)+1))
	if err != nil {
		return err
	}
	if len(data) > 8<<20 {
		return errors.New("package request too large")
	}
	var request packageRequest
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&request); err != nil {
		return err
	}
	if err = validatePackageRequest(&request); err != nil {
		return err
	}
	dir, err := packageJobDir(uid, request.ID)
	if err != nil {
		return err
	}
	for _, base := range []string{"/var/lib/setupvault", "/var/lib/setupvault/recovery"} {
		if err = secureRootDirectory(base, 0, 0755); err != nil {
			return err
		}
	}
	for _, path := range []string{filepath.Dir(dir), dir} {
		if err = secureRootDirectory(path, gid, 0750); err != nil {
			return err
		}
	}
	// An authenticated retry can replace a terminal request, never a running one.
	if out, err := exec.Command("/usr/bin/systemctl", "is-active", packageUnit(uid, request.ID)).Output(); err == nil && string(bytes.TrimSpace(out)) == "active" {
		return errors.New("package transaction is already running")
	}
	if err = rootJSON(filepath.Join(dir, "request.json"), request, gid); err != nil {
		return err
	}
	if err = rootJSON(filepath.Join(dir, "result.json"), packageResult{Status: "Queued", Phase: "Package installation queued"}, gid); err != nil {
		return err
	}
	args := []string{"--quiet", "--collect", "--service-type=exec", "--unit=" + packageUnit(uid, request.ID), "--property=UMask=0022", "--property=KillMode=mixed", "--property=TimeoutStopSec=10min", "/usr/bin/setupvault", "internal-recovery-packages", uid, request.ID}
	if _, err = commandOutput(ctx, "/usr/bin/systemd-run", args...); err != nil {
		return err
	}
	return nil
}

func RunRecoveryPackages(ctx context.Context, uid, id string) (result error) {
	if os.Geteuid() != 0 {
		return errors.New("package worker must be run by its authorized system service")
	}
	dir, err := packageJobDir(uid, id)
	if err != nil {
		return err
	}
	u, err := user.LookupId(uid)
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return err
	}
	if err = secureRootDirectory(dir, gid, 0750); err != nil {
		return err
	}
	var r packageRequest
	if err = readLimitedJSON(filepath.Join(dir, "request.json"), &r, 8<<20); err != nil {
		return err
	}
	if err = validatePackageRequest(&r); err != nil || r.ID != id {
		if err == nil {
			err = errors.New("package job mismatch")
		}
		return err
	}
	status := packageResult{Status: "Running", Phase: "Resolving approved package transaction"}
	save := func() error { return rootJSON(filepath.Join(dir, "result.json"), status, gid) }
	defer func() {
		if result != nil {
			status.Status = "Failed"
			status.Error = result.Error()
		} else {
			status.Status = "Completed"
			status.Phase = "Selected system packages installed"
		}
		_ = save()
	}()
	if err = save(); err != nil {
		return err
	}
	for _, p := range r.Flatpaks {
		if err = checkFlatpakRemote(ctx, p); err != nil {
			return err
		}
		args := append([]string{"--", "flatpak", "remote-info", "--show-ref"}, flatpakScope(p.Scope)...)
		args = append(args, "--", p.Origin, p.Name+"//"+p.Branch)
		if _, err = commandOutput(ctx, "dbus-run-session", args...); err != nil {
			return err
		}
	}
	if len(r.RPMs) > 0 {
		txdir := filepath.Join(dir, "transaction-"+randomID())
		actual, err := prepareDNF(ctx, r.RPMs, txdir)
		if err != nil {
			return err
		}
		expected, err := canonicalDNF(r.Expected)
		if err != nil {
			return err
		}
		if !reviewedDNF(actual, expected) {
			return errors.New("package transaction changed since review; prepare a new plan")
		}
		if len(actual) > 0 {
			status.Phase = "Installing approved RPM transaction"
			if err = save(); err != nil {
				return err
			}
			// A package commit finishes before normal cancellation is acted on.
			if _, err = commandOutput(context.Background(), "/usr/bin/dnf", "--assumeyes", "--setopt=install_weak_deps=False", "--setopt=obsoletes=0", "replay", txdir); err != nil {
				return err
			}
		}
	}
	for _, p := range r.Flatpaks {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err = checkFlatpakRemote(ctx, p); err != nil {
			return err
		}
		status.Phase = "Installing system Flatpak " + p.Name
		if err = save(); err != nil {
			return err
		}
		if err = installFlatpak(context.Background(), p); err != nil {
			return err
		}
	}
	return nil
}

func checkFlatpakRemote(ctx context.Context, p RecoveryPackage) error {
	for _, r := range recoveryRemotes(ctx, p.Scope) {
		if r.Name == p.Origin && r.Scope == p.Scope {
			if r.URL != p.URL || p.Key != "" && r.Key != p.Key && r.RawKey != p.Key {
				return errors.New("Flatpak remote identity changed since review")
			}
			return nil
		}
	}
	if p.AddRemote {
		if err := addRecoveryRemote(ctx, p); err != nil {
			return err
		}
		p.AddRemote = false
		return checkFlatpakRemote(ctx, p)
	}
	return errors.New("Flatpak remote is no longer configured")
}

func installFlatpak(ctx context.Context, p RecoveryPackage) error {
	args := append([]string{"install", "--noninteractive", "--assumeyes", "--app"}, flatpakScope(p.Scope)...)
	if p.Arch != "" {
		args = append(args, "--arch="+p.Arch)
	}
	args = append(args, "--", p.Origin, p.Name+"//"+p.Branch)
	if os.Geteuid() == 0 && p.Scope != "user" {
		wrapped := append([]string{"--", "flatpak"}, args...)
		_, err := commandOutput(ctx, "dbus-run-session", wrapped...)
		return err
	}
	_, err := commandOutput(ctx, "flatpak", args...)
	return err
}

func (a *App) authorizeRecovery(ctx context.Context, j *RecoveryJob) error {
	if _, err := os.Stat("/usr/bin/setupvault"); err != nil {
		return errors.New("install the SetupVault Fedora RPM to enable authorized system app recovery")
	}
	r := packageRequest{Version: 1, ID: j.ID, Expected: j.DNF}
	if j.Selected("apps") {
		for _, p := range j.Packages {
			if !p.Selected || p.Status == "Installed" {
				continue
			}
			if p.Kind == "rpm" {
				r.RPMs = append(r.RPMs, p.Name)
			} else if p.Scope != "user" {
				r.Flatpaks = append(r.Flatpaks, p)
			}
		}
	}
	if len(r.RPMs) == 0 && len(r.Flatpaks) == 0 {
		return nil
	}
	if err := validatePackageRequest(&r); err != nil {
		return err
	}
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "pkexec", "/usr/bin/setupvault", "internal-authorize-recovery")
	cmd.Stdin = bytes.NewReader(data)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("package authorization: %s", stringsTrim(out))
	}
	j.Privileged = true
	return nil
}

func stringsTrim(b []byte) string {
	if len(b) > 1500 {
		b = b[len(b)-1500:]
	}
	return string(bytes.TrimSpace(b))
}
