# SetupVault

A Go terminal app for backing up a Fedora/GNOME setup to an encrypted, versioned repository. Built with Bubble Tea, restic and rclone. **Early development; keep your existing backup until you have checked a restore.**

## Run

```sh
sudo dnf install restic rclone libsecret dconf flatpak
make build
./bin/setupvault
```

Building requires Go 1.26 or newer. SetupVault runs as your desktop user. The installed RPM adds an optional, narrowly scoped administrator helper for system configuration.

1. **Connections:** choose an existing connection with Enter, connect Dropbox with `d`, or choose a local folder with `l`. Choose a dedicated backup folder. The connection test uploads and reads a small temporary probe.
2. **Contents:** Space selects groups; Enter expands the actual folders. Add other settings locations with `a`. Individual paths can be excluded. Review this before backing up.
3. **Settings:** press `p` to set the recovery password; save it in Bitwarden. Press `x` to export an encrypted `setupvault-recovery.age` profile and keep that file somewhere you can access after a reinstall.
4. Press `b` to back up. Overview shows the destination, previous backup, verification, next scheduled run and cloud quota when available. Activity shows results. An incomplete backup is reported as partial, never healthy.
5. After checking a restore, set a schedule with `s` in Settings, for example `daily` or `*-*-* 19:00:00`. Use `off` to disable it. Keep the executable at its installed path after scheduling.

Tab / Shift+Tab changes pages, arrows select, Esc cancels a task, and `q` quits. The app has no persistent web server.

## What is saved

| Group | Contents |
| --- | --- |
| GNOME | dconf export, extensions, themes, icons, fonts, custom backgrounds |
| Apps | User-installed RPM names, Flatpak IDs/origins/branches/installations, Flatpak remote details, VS Code extension IDs when found |
| App settings | `~/.config`, shell configuration, launchers, Flatpak overrides and installed Flatpak `config` directories; selected known preferences stored elsewhere |
| System (opt in) | Separate archive of `/etc` and `/usr/local`, including custom DNF repository definitions; account databases, machine ID and SSH host keys excluded |
| Manual apps (opt in) | `~/AppImages`, `~/.local/bin`, `~/.local/opt`, `~/opt`, user plugins |
| Custom paths | Additional files or folders you explicitly include |

The default sources omit Documents, Downloads, Pictures, browser profiles, mail storage and general personal files. Cache/log patterns and Chrome/Firefox profiles are excluded. Config files can contain account credentials or app data: the whole backup is encrypted. Explicit custom paths can include personal files; check their tree before saving.

Packaged applications are represented by installation lists, not copies of RPMs or Flatpak runtimes. Settings stored outside the listed locations require custom paths. Symlinks are backed up as symlinks; add a manually installed app's actual executable directory if it lives elsewhere. Running applications may change their settings during a backup; close them for a consistent recovery copy.

## Recover after a reinstall

1. Install SetupVault and its dependencies; open Settings and import the recovery profile **before saving a new profile**. Alternatively:

   ```sh
   setupvault import /path/to/setupvault-recovery.age
   ```

   The password prompt is hidden. Import refuses to overwrite an existing profile; use separate `--config-dir` and `--state-dir` options if needed. Home paths are adapted to the new user. Scheduling and administrator access stay off.
2. Reconnect the cloud account under Connections. Select the **same remote name and backup folder** saved in your profile, or update the destination to the reconnected name.
3. Open Restore, press `r`, select a dated snapshot and press Enter. Restore writes into a new empty folder, preserving the original absolute path structure inside it. It never writes directly over your live home directory or `/`.
4. Review the restored `inventory` folder, reinstall the listed RPM/Flatpak apps from their original repositories, then copy the selected settings into your new home. Load GNOME preferences with `dconf load / < gnome-settings.dconf` from that restored inventory. Log out and back in afterward. Apply system configuration selectively with administrator access; hardware and Fedora versions can differ.

**This first version stages restored files and installation lists; applying settings and reinstalling packages remain manual.** It is not an OS image, and cannot promise identical hardware-dependent settings or that an old package still exists.

If you lose the profile file but have the password, choose the existing repository, set its password, and load Restore. Snapshots remain standard [restic repositories](https://restic.readthedocs.io/en/stable/030_preparing_a_new_repo.html), recoverable without SetupVault.

## Encryption and scheduling

restic encrypts backup data and filenames before upload. The repository password is stored in the desktop Secret Service (`secret-tool`), passed to restic through an inherited pipe, and omitted from profile JSON, arguments and logs. The portable profile is age-encrypted with the same password. Cloud OAuth credentials stay in rclone's own configuration and are not included in exports; reconnect after reinstalling.

Setting a password for an existing repository first checks it can unlock that repository. Changing the encryption password requires restic's key-management commands; SetupVault does not silently replace a working key.

A backup is healthy only after restic commits a snapshot, checks repository structure, and restores a random marker from that snapshot. This is a sample restore, not a full read of every stored data block. Retention keeps the latest N snapshots with this profile's tag, then prunes unreferenced data after a verified backup. Other profiles' snapshots are not forgotten.

Scheduling uses your systemd user manager and an unlocked login keyring. This version requires an active desktop session and does not promise unattended backups while logged out. Failed scheduled jobs retry after an hour; persistent timers catch up when the user manager starts. Overview reports actual timer status.

State is in `$XDG_STATE_HOME/setupvault` (default `~/.local/state/setupvault`); the editable profile is `$XDG_CONFIG_HOME/setupvault/profile.json` (default `~/.config/setupvault`). Passwords and state are not published to GitHub. Separate profiles need separate config **and** state directories.

Dropbox browser connection and existing rclone remotes are supported. OneDrive connection is implemented but needs account testing. For Google Drive, configure an rclone remote with your own OAuth client first; SetupVault lists existing remotes. Provider authorization rules still apply. See [rclone Dropbox](https://rclone.org/dropbox/), [OneDrive](https://rclone.org/onedrive/) and [Drive](https://rclone.org/drive/) documentation.

## Build, test and package

```sh
make build
make test                 # race detector + real local restic backup/restore
make rpm                  # requires golang, rpm-build, systemd-rpm-macros
```

The live cloud test uploads only generated fixture settings into a new random `SetupVault-Tests` directory and deletes its own directory afterward:

```sh
SETUPVAULT_TEST_REMOTE=my-dropbox go test -run TestCloudBackupRestore -count=1 -v ./internal/vault
```

RPMs and source RPMs are written under `dist/rpmbuild`. Install a locally built RPM with `sudo dnf install ./dist/rpmbuild/RPMS/$(uname -m)/setupvault-*.rpm`. Then open **SetupVault** from the app menu or run `setupvault`. Contents → `s` authorizes the installed system export helper once. The helper only exports fixed system paths; it cannot apply user-supplied restore instructions as root.

The source archive includes vendored Go dependencies for builds without network access. The spec and GitHub Actions prepare packages for COPR; **no COPR repository or official Fedora package is published yet**. To publish, create a COPR project with a Fedora account and submit the source RPM:

```sh
copr-cli build YOUR_COPR_NAME dist/rpmbuild/SRPMS/setupvault-*.src.rpm
```

Official Fedora inclusion is a separate package review and maintenance process. This repository currently targets Fedora 44; the vendored spec is intended for initial COPR distribution.

License: MIT. Dependencies retain their own licenses, included in the vendor source archive.
