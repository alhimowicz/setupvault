package vault

import (
	"bufio"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func desktopKey(path string) bool {
	for _, prefix := range []string{"/org/gnome/desktop/interface/", "/org/gnome/desktop/background/", "/org/gnome/desktop/screensaver/", "/org/gnome/desktop/wm/preferences/", "/org/gnome/desktop/wm/keybindings/", "/org/gnome/settings-daemon/plugins/media-keys/", "/org/gnome/terminal/legacy/", "/org/gnome/shell/extensions/"} {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return path == "/org/gnome/shell/favorite-apps" || path == "/org/gnome/shell/enabled-extensions" || path == "/org/gnome/shell/disable-user-extensions"
}

func recoverySchema(section, name string) string {
	if strings.HasPrefix(section, "/org/gnome/desktop/") || section == "/org/gnome/shell/" || section == "/org/gnome/settings-daemon/plugins/media-keys/" {
		return strings.ReplaceAll(strings.Trim(section, "/"), "/", ".")
	}
	if strings.HasPrefix(section, "/org/gnome/terminal/legacy/profiles:/:") {
		return "org.gnome.Terminal.Legacy.Profile:" + section
	}
	if section == "/org/gnome/terminal/legacy/profiles:/" {
		return "org.gnome.Terminal.ProfilesList"
	}
	if section == "/org/gnome/terminal/legacy/" {
		return "org.gnome.Terminal.Legacy.Settings"
	}
	return ""
}

func planRecoveryKeys(ctx context.Context, j *RecoveryJob, inventory string) ([]RecoveryKey, error) {
	if !j.Selected("gnome") {
		return nil, nil
	}
	data, err := os.ReadFile(filepath.Join(inventory, "gnome-settings.dconf"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) > 8<<20 {
		return nil, errors.New("GNOME preferences export is too large")
	}
	var keys []RecoveryKey
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	section := ""
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = "/" + strings.Trim(line, "[]/") + "/"
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok || !desktopKey(section+name) {
			continue
		}
		k := RecoveryKey{Path: section + name, Value: value, Status: "Pending", Schema: recoverySchema(section, name), Name: name}
		if k.Schema != "" {
			if known, e := commandOutput(ctx, "gsettings", "list-keys", k.Schema); e == nil && !strings.Contains("\n"+string(known), "\n"+name+"\n") {
				k.Status = "Skipped"
				k.Reason = "Preference is not supported by the target schema"
			}
		}
		if strings.Contains(k.Path, "picture-uri") {
			k.Value = strings.ReplaceAll(k.Value, "file://"+j.SourceHome+"/", "file://"+j.TargetHome+"/")
		}
		out, err := commandOutput(ctx, "dconf", "read", k.Path)
		if err != nil {
			k.Status = "Blocked"
			k.Reason = err.Error()
		} else {
			k.Before = strings.TrimSpace(string(out))
		}
		keys = append(keys, k)
	}
	return keys, scanner.Err()
}

var quotedUUID = regexp.MustCompile(`'([^']+)'`)

func compatibleExtensions(j *RecoveryJob) (map[string]bool, error) {
	out, err := commandOutput(context.Background(), "gnome-shell", "--version")
	if err != nil {
		return nil, err
	}
	parts := strings.Fields(string(out))
	if len(parts) == 0 {
		return nil, errors.New("cannot identify GNOME Shell version")
	}
	major := strings.Split(parts[len(parts)-1], ".")[0]
	base := filepath.Join(j.TargetHome, ".local/share/gnome-shell/extensions")
	entries, _ := os.ReadDir(base)
	supported := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		var meta struct {
			UUID     string   `json:"uuid"`
			Versions []string `json:"shell-version"`
		}
		if readLimitedJSON(filepath.Join(base, e.Name(), "metadata.json"), &meta, 1<<20) != nil || meta.UUID != e.Name() {
			continue
		}
		for _, v := range meta.Versions {
			if strings.Split(v, ".")[0] == major {
				supported[meta.UUID] = true
			}
		}
	}
	// System extensions belong to installed packages; their metadata is checked too.
	entries, _ = os.ReadDir("/usr/share/gnome-shell/extensions")
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		var meta struct {
			UUID     string   `json:"uuid"`
			Versions []string `json:"shell-version"`
		}
		if readLimitedJSON(filepath.Join("/usr/share/gnome-shell/extensions", e.Name(), "metadata.json"), &meta, 1<<20) != nil {
			continue
		}
		for _, v := range meta.Versions {
			if strings.Split(v, ".")[0] == major {
				supported[meta.UUID] = true
			}
		}
	}
	return supported, nil
}

func (a *App) applyRecoveryKeys(ctx context.Context, j *RecoveryJob, canceled func() bool) error {
	if !j.Selected("gnome") {
		return nil
	}
	for i := range j.Keys {
		k := &j.Keys[i]
		if k.Status == "Applied" || k.Status == "Skipped" || k.Status == "Undone" {
			continue
		}
		if canceled() {
			return context.Canceled
		}
		current, err := commandOutput(ctx, "dconf", "read", k.Path)
		if err != nil {
			k.Status = "Blocked"
			k.Reason = err.Error()
			continue
		}
		before := strings.TrimSpace(string(current))
		if k.Status == "Applying" && before == k.After {
			k.Status = "Applied"
			continue
		}
		if before != k.Before {
			k.Status = "Blocked"
			k.Reason = "GNOME setting changed since review"
			continue
		}
		value := k.Value
		if k.Schema != "" {
			if _, err = commandOutput(ctx, "gsettings", "range", k.Schema, k.Name); err != nil {
				k.Status = "Skipped"
				k.Reason = "Schema or preference is unavailable after app installation"
				continue
			}
		}
		if k.Path == "/org/gnome/shell/enabled-extensions" {
			compatible, err := compatibleExtensions(j)
			if err != nil {
				k.Status = "Blocked"
				k.Reason = err.Error()
				continue
			}
			var accepted []string
			for _, match := range quotedUUID.FindAllStringSubmatch(value, -1) {
				if compatible[match[1]] {
					accepted = append(accepted, "'"+match[1]+"'")
				} else {
					j.Notices = append(j.Notices, "Extension left disabled: "+match[1]+" (not present or incompatible)")
				}
			}
			value = "[" + strings.Join(accepted, ", ") + "]"
		}
		k.Status = "Applying"
		k.After = value
		j.Phase = "Applying GNOME preference " + k.Path
		if err = a.checkpointRecovery(j, nil, k); err != nil {
			return err
		}
		args := []string{"write", k.Path, value}
		tool := "dconf"
		if k.Schema != "" {
			tool = "gsettings"
			args = []string{"set", k.Schema, k.Name, value}
		}
		if _, err = commandOutput(ctx, tool, args...); err != nil {
			k.Status = "Blocked"
			k.Reason = err.Error()
			continue
		}
		result, err := commandOutput(ctx, "dconf", "read", k.Path)
		if err != nil {
			k.Status = "Blocked"
			k.Reason = err.Error()
			continue
		}
		k.After = strings.TrimSpace(string(result))
		k.Status = "Applied"
		k.Reason = ""
		j.NeedsLogout = true
		if err = a.checkpointRecovery(j, nil, k); err != nil {
			return err
		}
	}
	return nil
}

func (a *App) undoRecoveryKeys(ctx context.Context, j *RecoveryJob) error {
	for i := range j.Keys {
		k := &j.Keys[i]
		if k.Status != "Applied" {
			continue
		}
		current, err := commandOutput(ctx, "dconf", "read", k.Path)
		if err != nil {
			return err
		}
		if strings.TrimSpace(string(current)) == k.Before {
			k.Status = "Undone"
			k.Reason = ""
			continue
		}
		if strings.TrimSpace(string(current)) != k.After {
			k.Reason = "Changed after recovery; current setting was preserved"
			continue
		}
		args := []string{"write", k.Path, k.Before}
		if k.Before == "" {
			args = []string{"reset", k.Path}
		}
		if _, err = commandOutput(ctx, "dconf", args...); err != nil {
			return err
		}
		k.Status = "Undone"
		if err = a.checkpointRecovery(j, nil, k); err != nil {
			return err
		}
	}
	return nil
}
