package vault

import (
	"fmt"
	"os/exec"
)

type DependencyError struct{ Tool, Package string }

func (e *DependencyError) Error() string {
	return fmt.Sprintf("%s is required for this action. Install the %s package (sudo dnf install %s).", e.Tool, e.Package, e.Package)
}
func CheckDependency(tool string) error {
	if _, err := exec.LookPath(tool); err == nil {
		return nil
	}
	packages := map[string]string{"rclone": "rclone", "restic": "restic", "secret-tool": "libsecret", "dconf": "dconf", "gsettings": "glib2", "flatpak": "flatpak", "dbus-run-session": "dbus-daemon"}
	if p, ok := packages[tool]; ok {
		return &DependencyError{Tool: tool, Package: p}
	}
	return fmt.Errorf("required program is unavailable: %s", tool)
}

func InstallRuntime(tool string) error {
	packages := map[string]string{"rclone": "rclone", "restic": "restic"}
	name, ok := packages[tool]
	if !ok {
		return fmt.Errorf("unsupported runtime dependency")
	}
	// This explicit TUI action installs a fixed package and lets its transaction finish.
	cmd := exec.Command("pkexec", "/usr/bin/dnf", "--assumeyes", "install", "--", name)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("install %s: %s", name, stringsTrim(output))
	}
	return CheckDependency(tool)
}
