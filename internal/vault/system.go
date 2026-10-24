package vault

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

func EnableSystemAccess() error {
	if os.Geteuid() != 0 {
		return errors.New("administrator authentication required")
	}
	uid, err := strconv.Atoi(os.Getenv("PKEXEC_UID"))
	if err != nil || uid <= 0 {
		return errors.New("enable system access from the SetupVault TUI using pkexec")
	}
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return err
	}
	// Grant only this user's fixed, root-owned, read-only export unit.
	rule := fmt.Sprintf("polkit.addRule(function(action, subject) {\n  if (action.id == 'org.freedesktop.systemd1.manage-units' && action.lookup('unit') == '%s' && action.lookup('verb') == 'start' && subject.user == %q) { return polkit.Result.YES; }\n});\n", SystemUnit(uid), u.Username)
	path := fmt.Sprintf("/etc/polkit-1/rules.d/90-setupvault-%d.rules", uid)
	if err = writePrivate(path, []byte(rule)); err != nil {
		return err
	}
	if err = os.Chmod(path, 0644); err != nil {
		return err
	}
	if out, err := exec.Command("restorecon", "-F", path).CombinedOutput(); err != nil {
		return fmt.Errorf("label policy: %s", out)
	}
	return nil
}

func ExportSystem(uidText string) error {
	if os.Geteuid() != 0 {
		return errors.New("system export must run through its installed root service")
	}
	uid, err := strconv.Atoi(uidText)
	if err != nil || uid <= 0 {
		return errors.New("invalid export user")
	}
	u, err := user.LookupId(strconv.Itoa(uid))
	if err != nil {
		return err
	}
	gid, err := strconv.Atoi(u.Gid)
	if err != nil {
		return err
	}
	base := "/var/lib/setupvault"
	if err = secureRootDirectory(base, 0, 0755); err != nil {
		return err
	}
	dir := filepath.Join(base, strconv.Itoa(uid))
	if err = secureRootDirectory(dir, gid, 0750); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".system-config-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chown(0, gid); err != nil {
		f.Close()
		return err
	}
	if err = f.Chmod(0640); err != nil {
		f.Close()
		return err
	}
	f.Close()
	args := []string{"--create", "--file", f.Name(), "--xattrs", "--acls", "--selinux", "--directory", "/"}
	for _, p := range []string{"etc/shadow*", "etc/gshadow*", "etc/passwd*", "etc/group", "etc/group-", "etc/machine-id", "etc/ssh/ssh_host_*"} {
		args = append(args, "--exclude="+p)
	}
	args = append(args, "etc", "usr/local")
	if out, err := exec.Command("tar", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("system export incomplete: %s", out)
	}
	return os.Rename(f.Name(), filepath.Join(dir, "system-config.tar"))
}

func secureRootDirectory(path string, gid int, mode os.FileMode) error {
	if err := os.Mkdir(path, mode); err != nil && !os.IsExist(err) {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	var st unix.Stat_t
	if err = unix.Lstat(path, &st); err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || st.Uid != 0 || info.Mode().Perm()&0022 != 0 {
		return errors.New("unsafe system export directory")
	}
	if err = os.Chown(path, 0, gid); err != nil {
		return err
	}
	return os.Chmod(path, mode)
}
