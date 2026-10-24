package vault

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Group struct {
	ID, Name, Note string
	Paths          []string
	Selected       bool
}

var excludedApps = map[string]bool{"com.google.Chrome": true, "org.mozilla.firefox": true, "com.brave.Browser": true, "com.microsoft.Edge": true, "org.chromium.Chromium": true}

func (a *App) Groups() []Group {
	h := a.Profile.Home
	groups := []Group{
		{ID: "gnome", Name: "GNOME preferences and extensions", Paths: []string{filepath.Join(h, ".config/dconf"), filepath.Join(h, ".local/share/gnome-shell"), filepath.Join(h, ".local/share/gnome-background-properties"), filepath.Join(h, ".local/share/backgrounds"), filepath.Join(h, ".local/share/icons"), filepath.Join(h, ".local/share/fonts"), filepath.Join(h, ".local/share/themes"), filepath.Join(h, ".themes"), filepath.Join(h, ".icons"), filepath.Join(h, ".fonts")}},
		{ID: "apps", Name: "Installed app and repository inventory", Note: "RPM/Flatpak lists are recorded before every backup; packages are reinstalled during recovery.", Paths: []string{a.InventoryDir()}},
		{ID: "settings", Name: "Native and Flatpak app settings", Note: "Common config locations; app-specific data outside these locations needs an explicit custom path.", Paths: []string{filepath.Join(h, ".config"), filepath.Join(h, ".bashrc"), filepath.Join(h, ".bash_profile"), filepath.Join(h, ".zshrc"), filepath.Join(h, ".gitconfig"), filepath.Join(h, ".oh-my-zsh"), filepath.Join(h, ".local/share/applications"), filepath.Join(h, ".local/share/flatpak/overrides"), filepath.Join(h, ".local/share/flatpak/db")}},
		{ID: "system", Name: "Custom system configuration", Note: "Enable administrator access once from Contents (requires RPM installation).", Paths: []string{filepath.Join("/var/lib/setupvault", fmt.Sprint(os.Getuid()), "system-config.tar")}},
		{ID: "manual", Name: "Manual apps, AppImages and user plugins", Paths: []string{filepath.Join(h, "AppImages"), filepath.Join(h, ".local/bin"), filepath.Join(h, ".local/opt"), filepath.Join(h, "opt"), filepath.Join(h, ".vst3"), filepath.Join(h, ".local/share/yabridge")}},
	}
	entries, _ := os.ReadDir(filepath.Join(h, ".var/app"))
	for _, e := range entries {
		if !e.IsDir() || excludedApps[e.Name()] {
			continue
		}
		app := filepath.Join(h, ".var/app", e.Name())
		groups[2].Paths = append(groups[2].Paths, filepath.Join(app, "config"))
		// Preferences stored outside XDG_CONFIG_HOME are explicitly listed.
		if e.Name() == "org.localsend.localsend_app" {
			groups[2].Paths = append(groups[2].Paths, filepath.Join(app, "data/org.localsend.localsend_app"))
		}
		if e.Name() == "org.telegram.desktop" {
			for _, n := range []string{"settingss", "key_datas", "shortcuts-custom.json"} {
				groups[2].Paths = append(groups[2].Paths, filepath.Join(app, "data/TelegramDesktop/tdata", n))
			}
		}
		if e.Name() == "net.thunderbird.Thunderbird" {
			base := filepath.Join(app, ".thunderbird")
			groups[2].Paths = append(groups[2].Paths, filepath.Join(base, "profiles.ini"), filepath.Join(base, "installs.ini"))
			profiles, _ := os.ReadDir(base)
			for _, p := range profiles {
				if p.IsDir() {
					for _, n := range []string{"prefs.js", "user.js", "extensions.json", "addons.json", "key4.db", "cert9.db", "logins.json", "xulstore.json"} {
						groups[2].Paths = append(groups[2].Paths, filepath.Join(base, p.Name(), n))
					}
				}
			}
		}
	}
	for i := range groups {
		for _, id := range a.Profile.Selected {
			if id == groups[i].ID {
				groups[i].Selected = true
			}
		}
		var existing []string
		for _, p := range groups[i].Paths {
			if _, err := os.Lstat(p); err == nil || groups[i].ID == "apps" || groups[i].ID == "system" {
				existing = append(existing, p)
			}
		}
		groups[i].Paths = existing
	}
	return groups
}

func (a *App) Toggle(id string) {
	for i, s := range a.Profile.Selected {
		if s == id {
			a.Profile.Selected = append(a.Profile.Selected[:i], a.Profile.Selected[i+1:]...)
			return
		}
	}
	a.Profile.Selected = append(a.Profile.Selected, id)
}

func (a *App) Sources() ([]string, error) {
	var paths []string
	for _, g := range a.Groups() {
		if g.Selected {
			if g.ID == "system" && !a.Profile.SystemEnabled {
				return nil, fmt.Errorf("system configuration selected, but administrator access is not enabled")
			}
			paths = append(paths, g.Paths...)
		}
	}
	paths = append(paths, a.Profile.CustomPaths...)
	// A recovery manifest and restore marker belong to every snapshot.
	paths = append(paths, a.InventoryDir())
	sort.Strings(paths)
	var result []string
	for _, p := range paths {
		p = filepath.Clean(p)
		covered := false
		for _, old := range result {
			if p == old || strings.HasPrefix(p, old+string(os.PathSeparator)) {
				covered = true
				break
			}
		}
		if !covered {
			if _, err := os.Lstat(p); err != nil {
				return nil, fmt.Errorf("source unavailable %s: %w", p, err)
			}
			result = append(result, p)
		}
	}
	return result, nil
}

func DefaultExclusions(home string) []string {
	result := []string{"**/cache/**", "**/Cache/**", "**/Caches/**", "**/Code Cache/**", "**/GPUCache/**", "**/GPUPersistentCache/**", "**/DawnGraphiteCache/**", "**/DawnWebGPUCache/**", "**/Crashpad/**", "**/Service Worker/**", "**/blob_storage/**", "**/Session Storage/**", "**/IndexedDB/**", "**/logs/**", "**/Logs/**", "**/node_modules/**", "**/.git/**", "**/workspaceStorage/**", "**/History/**", "**/MetadataCaches/**", "**/Singleton*", "**/*.log", "**/*.sock", "**/*.pid", "**/*-journal", "**/heroic/tools/**", "**/Mail/**", "**/ImapMail/**"}
	for _, n := range []string{"google-chrome", "chromium", "BraveSoftware", "microsoft-edge", ".mozilla", "Duplicati"} {
		result = append(result, filepath.Join(home, ".config", n)+"/**")
	}
	for name := range excludedApps {
		result = append(result, filepath.Join(home, ".var/app", name)+"/**")
	}
	sort.Strings(result)
	return result
}

func (a *App) Exclusions() []string {
	result := append([]string{}, a.Profile.Excludes...)
	result = append(result, a.ConfigDir+"/**")
	for _, n := range []string{"state.json", "activity.log", "backup.lock", "*.service", "*.timer"} {
		result = append(result, filepath.Join(a.StateDir, n))
	}
	if filepath.IsAbs(a.Profile.Repository) {
		result = append(result, a.Profile.Repository+"/**")
	}
	gnome := false
	for _, id := range a.Profile.Selected {
		if id == "gnome" {
			gnome = true
		}
	}
	if !gnome {
		result = append(result, filepath.Join(a.Profile.Home, ".config/dconf")+"/**")
	}
	return result
}

func (a *App) Collect(ctx context.Context) (string, error) {
	if err := os.MkdirAll(a.InventoryDir(), 0700); err != nil {
		return "", err
	}
	var marker [32]byte
	if _, err := rand.Read(marker[:]); err != nil {
		return "", err
	}
	if err := writePrivate(filepath.Join(a.InventoryDir(), "restore-check.txt"), []byte(hex.EncodeToString(marker[:])+"\n")); err != nil {
		return "", err
	}
	manifest := map[string]any{"created": time.Now().Format(time.RFC3339), "profile": a.Profile.Name, "home": a.Profile.Home, "notes": "App lists support reinstallation. Restore system settings selectively on different hardware."}
	commands := map[string][]string{"rpm-packages.txt": {"dnf", "repoquery", "--userinstalled", "--queryformat", "%{name}.%{arch}\n"}, "flatpak-apps.tsv": {"flatpak", "list", "--app", "--columns=application,origin,branch,installation"}, "flatpak-remotes.txt": {"flatpak", "remotes", "--show-details"}, "gnome-settings.dconf": {"dconf", "dump", "/"}}
	apps, gnome := false, false
	for _, id := range a.Profile.Selected {
		if id == "apps" {
			apps = true
		}
		if id == "gnome" {
			gnome = true
		}
	}
	for name, args := range commands {
		if err := os.Remove(filepath.Join(a.InventoryDir(), name)); err != nil && !os.IsNotExist(err) {
			return "", err
		}
		if name == "gnome-settings.dconf" && !gnome {
			continue
		}
		if name != "gnome-settings.dconf" && !apps {
			continue
		}
		if _, err := exec.LookPath(args[0]); err != nil {
			manifest[name] = "not installed"
			continue
		}
		out, err := exec.CommandContext(ctx, args[0], args[1:]...).Output()
		if err != nil {
			return "", fmt.Errorf("cannot record %s: %w", name, err)
		}
		if err = writePrivate(filepath.Join(a.InventoryDir(), name), out); err != nil {
			return "", err
		}
	}
	if err := os.Remove(filepath.Join(a.InventoryDir(), "vscode-extensions.txt")); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	extensions := filepath.Join(a.Profile.Home, ".var/app/com.visualstudio.code/data/vscode/extensions/extensions.json")
	if data, err := os.ReadFile(extensions); err == nil && apps {
		var entries []struct {
			Identifier struct {
				ID string `json:"id"`
			} `json:"identifier"`
		}
		if json.Unmarshal(data, &entries) == nil {
			var ids []string
			for _, e := range entries {
				ids = append(ids, e.Identifier.ID)
			}
			sort.Strings(ids)
			if err = writePrivate(filepath.Join(a.InventoryDir(), "vscode-extensions.txt"), []byte(strings.Join(ids, "\n")+"\n")); err != nil {
				return "", err
			}
		}
	}
	if err := writeJSON(filepath.Join(a.InventoryDir(), "manifest.json"), manifest); err != nil {
		return "", err
	}
	if err := writeJSON(filepath.Join(a.InventoryDir(), "profile.json"), a.Profile); err != nil {
		return "", err
	}
	return hex.EncodeToString(marker[:]), nil
}
