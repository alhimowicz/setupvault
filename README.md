# SetupVault

A Go terminal app for backing up a Fedora/GNOME setup to an encrypted, versioned repository. Built with Bubble Tea, restic and rclone. **Early development; keep your existing backup until you have checked a restore.**

[Go package](https://pkg.go.dev/github.com/alhimowicz/setupvault/cmd/setupvault)

## Run

```sh
sudo dnf install restic rclone libsecret dconf flatpak polkit systemd dnf5 dbus-daemon procps-ng glib2 gnupg2
go install github.com/alhimowicz/setupvault/cmd/setupvault@latest
setupvault
```

Go installs the executable into `GOBIN`, or `$(go env GOPATH)/bin` by default; add that directory to your `PATH`. The Fedora RPM installs runtime dependencies and provides the protected helpers for system export and RPM/system Flatpak recovery. Go installation provides the TUI, CLI, file recovery and user Flatpak recovery; install the RPM for system app installation. When rclone is missing, Connections shows the reason and offers **i: Install rclone**, with administrator authentication. Local-folder backups do not require rclone.

To build a checkout instead:

```sh
make build
./bin/setupvault
```

Building requires Go 1.26 or newer. SetupVault runs as your desktop user. The installed RPM adds an optional, narrowly scoped administrator helper for system configuration.

1. **Connections:** choose an existing connection with Enter, connect Dropbox with `d`, or choose a local folder with `l`. Choose a dedicated backup folder. The connection test uploads and reads a small temporary probe.
2. **Contents:** Space selects groups; Enter expands the actual folders. Add other settings locations with `a`. Select a `[custom]` path and press `d` or Delete to remove it from the backup selection; the original files stay on disk. Individual paths can be excluded. Review this before backing up.
3. **Settings:** press `p` to set the recovery password; save it in Bitwarden. Press `x` to export an encrypted `setupvault-recovery.age` profile and keep that file somewhere you can access after a reinstall.
4. Press `b` to back up. Overview shows the destination, previous backup, verification, next scheduled run and cloud quota when available. Activity shows results. An incomplete backup is reported as partial, never healthy.
5. After checking a restore, set a schedule with `s` in Settings, for example `daily` or `*-*-* 19:00:00`. Use `off` to disable it. Keep the executable at its installed path after scheduling.

Tab / Shift+Tab changes pages, arrows select, Esc cancels a foreground task, and `q` quits. After recovery starts, `q` leaves its detached installer running; Restore → **c** requests a stop after the current transaction. The app has no persistent web server.

Animated dots indicate background refreshes and running tasks. Long status messages and errors wrap to the terminal width. Page Up / Page Down scroll content on small terminals while navigation help stays visible. The advanced profile editor uses `$VISUAL`, then `$EDITOR`, or Nano/Vi when neither is configured; Nano's Ctrl+X returns to SetupVault.

## Terminal appearance

SetupVault uses the terminal's default foreground/background and basic ANSI palette. It keeps the terminal background and transparency, uses themed accents for status, and marks selected tabs with brackets and reverse video. Colors are shared across screens, loading indicators and input fields.

In **Settings**, press **t** to switch between **Terminal** (default) and **Monochrome**. The choice is saved immediately and restored next time. Any nonempty `NO_COLOR` value, including `0` or `false`, forces monochrome; `TERM=dumb` does too. Settings shows when an environment override is active. Bold, reverse video and text labels remain useful without color.

The same preference is configurable through the CLI:

```sh
setupvault --set-config --theme monochrome --json
setupvault --set-config --theme terminal --json
NO_COLOR=1 setupvault
```

Terminal colors come from the terminal emulator's own palette, so existing terminal themes work without separate SetupVault theme files. Individual palette contrast remains under the terminal user's control. CLI JSON and progress output stay free of color escapes.

## CLI flags for scripts and agents

Use ordinary flags for automation. `--json` returns one response on stdout, including failures, and implies noninteractive operation. `--help --json` lists the supported parameters. Existing commands such as `setupvault backup` and `setupvault restore ID NEW_FOLDER` still work.

```sh
setupvault --status --json
setupvault --contents --json
setupvault --connections --json
setupvault --quota --json --timeout 30s

setupvault --set-config --name MyPC \
  --repository rclone:my-dropbox:PC-Setup-Backups/MyPC \
  --select gnome,apps,settings,manual --keep-last 7 --json
setupvault --set-config --include /absolute/custom/config \
  --exclude '**/temporary/**' --json

setupvault --backup --json --progress --timeout 2h
setupvault --snapshots --json
setupvault --restore --snapshot latest --target /absolute/empty/folder --json
setupvault --export --file /safe/place/recovery.age --json
setupvault --activity --lines 20 --json
```

Configuration flags are partial updates. `--select` replaces the group list; `--include` and `--exclude` add entries and can be repeated. `--remove-include` and `--remove-exclude` remove exact entries. An empty `--select=''` selects no presets. `--config-dir` and `--state-dir` choose an isolated profile; use both for separate profiles.

Passwords are supplied through a private file or stdin, never as a password argument:

```sh
setupvault --backup --password-file /private/recovery-password --json
setupvault --import --file /safe/place/recovery.age \
  --password-stdin --json < /private/recovery-password
```

Password files must have private permissions (`chmod 600`). Stdin accepts one password line through EOF. A supplied password is kept in memory for that invocation. Without an explicit password, JSON mode searches the existing keyring item without requesting an unlock; a locked or missing item returns `credentials_required`.

Noninteractive import saves the profile without storing the password in a desktop keyring. Continue supplying `--password-file` on later calls, or opt into desktop storage with `--store-password` during import. That explicit option may display an unlock prompt. `--set-password --password-file FILE --allow-interaction` stores a password separately for TUI use and scheduled backups. It verifies the password against an existing repository before replacing a keyring item.

`--connect dropbox --allow-interaction` and `--enable-system --allow-interaction` permit browser or administrator authentication. `--schedule daily` enables scheduling and `--schedule off` disables it. Scheduled jobs require the password in an unlocked desktop keyring; per-invocation password files are not saved into timer units.

Successful JSON responses have `{"cli_version":1,"ok":true,"data":...}`. Failures have `ok:false` and an `error` containing `code` and `message`. Exit codes are 0 for success, 1 for operation failure (including partial backups), 2 for invalid arguments, 3 for required credentials/interaction, 124 for timeout and 130 for cancellation. Backup errors also return the current backup state in `data`. With `--progress`, stderr contains separate JSON progress events; stdout remains a single final response.

The TUI requires the foreground terminal. If a shell reports `suspended (tty input)`, run `fg` in that shell. Background launches are rejected before the interface changes terminal settings. Use the action flags for background jobs.

## What is saved

| Group | Contents |
| --- | --- |
| GNOME | dconf export, extensions, themes, icons, fonts, custom backgrounds |
| Apps | User-installed RPM names, Flatpak IDs/origins/branches/installations, Flatpak remote details, VS Code extension IDs when found |
| App settings | `~/.config`, shell configuration (`.bashrc`, `.bash_profile`, `.profile`, `.bash_aliases`, `.bash_logout`, `.inputrc`, `.zshrc`, `.zprofile`, `.zshenv`, `.zlogin`, `.zlogout`, `.p10k.zsh`, `.oh-my-zsh`), launchers, Flatpak overrides and installed Flatpak `config` directories; selected known preferences stored elsewhere |
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
3. Open Restore, press `r`, select a dated snapshot and press Enter for **Restore my setup**. SetupVault downloads and verifies the snapshot into private staging, then presents a review. Preparing the plan does not install apps or replace live settings. **e: Extract files only** keeps the separate-folder restore option.
4. Review categories; **p** lists apps, **f** lists files and **d** shows details, including the full proposed RPM transaction. Space deselects categories or individual items. Resolve or deselect blocked entries. On the app screen, **s** reviews missing Flatpak sources; Space explicitly approves adding the displayed HTTPS source and signing identity. Private sources need manual configuration. Custom DNF repositories must already be configured on the target.
5. Press **a** to start. Authenticate once for selected RPM/system Flatpak installations. Native package managers install apps before selected settings are applied. The TUI remains responsive; close it and reopen Restore to reconnect to the job. **c** stops at a safe boundary. **a** resumes a stopped job after reconciling completed installs. CLI timeouts stop waiting without canceling recovery.
6. Inspect the completion report and per-item reasons. **u: Undo settings** restores previous files and desktop keys, preserving subsequent edits. Installed packages remain installed. Log out and back in when requested; SetupVault never logs out automatically. Re-enable backup scheduling explicitly afterward.

```sh
setupvault --plan-recovery --snapshot latest --password-file /private/recovery-password --json
setupvault --plan-recovery --job JOB_ID --skip-file .local/bin/broken-link --json
setupvault --plan-recovery --job JOB_ID --approve-source user:flathub --json
setupvault --apply-recovery --job JOB_ID --allow-interaction --json
setupvault --recovery-status --job JOB_ID --json
setupvault --cancel-recovery --job JOB_ID --json
setupvault --resume-recovery --job JOB_ID --allow-interaction --wait --progress --json
setupvault --undo-recovery-settings --job JOB_ID --json
```

Use the returned `id` in place of `JOB_ID`. `--recovery-status` without `--job` lists jobs. `--select`, repeated `--skip-app NAME_OR_ID` and repeated `--skip-file RELATIVE_HOME_PATH` edit a ready plan; supplied lists replace their previous skip lists. `--approve-source SCOPE:REMOTE` binds approval to the source URL and signing identity in that reviewed plan. Apply/resume normally return after queuing; add `--wait` to follow completion. These actions use saved verified staging and need no repository password after preparation.

Automatic recovery supports available RPMs and Flatpak apps, captured shell and application files, compatible GNOME preferences, GNOME Terminal/Ptyxis/Console preferences, and captured manual binaries. Existing app versions are kept. Hardware/boot packages, unsupported preferences and incompatible GNOME extensions are reported or skipped. Raw dconf databases, SetupVault's own settings, current rclone credentials and machine display settings are protected. Close applications before applying their preferences; running-app conflicts are reported for retry. Old absolute paths are adapted only in supported desktop launchers and wallpaper URIs; arbitrary application settings may need adjustment. Manual apps are copied, never executed during import.

System archives remain staged for selective administrator review. VS Code extension IDs are recorded, but extension installation is still manual. This is setup recovery, not a full OS image or package rollback. Recovery survives closing the TUI while the user systemd manager remains active; after logout, reboot or an interrupted worker, reopen and Resume. Staging and Undo copies consume local disk until removed after successful validation. Do not remove them during an active job.

The [recovery design](docs/recovery-design.txt) describes the intended flow and records the first implementation's remaining limits.

If you lose the profile file but have the password, choose the existing repository, set its password, and load Restore. Snapshots remain standard [restic repositories](https://restic.readthedocs.io/en/stable/030_preparing_a_new_repo.html), recoverable without SetupVault.

## Encryption and scheduling

restic encrypts backup data and filenames before upload. The repository password is stored in the desktop Secret Service (`secret-tool`), passed to restic through an inherited pipe, and omitted from profile JSON, arguments and logs. The portable profile is age-encrypted with the same password. Cloud OAuth credentials stay in rclone's own configuration and are not included in exports; reconnect after reinstalling.

Setting a password for an existing repository first checks it can unlock that repository. Changing the encryption password requires restic's key-management commands; SetupVault does not silently replace a working key.

A backup is healthy only after restic commits a snapshot, checks repository structure, and restores a random marker from that snapshot. This is a sample restore, not a full read of every stored data block. Retention keeps the latest N snapshots with this profile's tag, then prunes unreferenced data after a verified backup. Other profiles' snapshots are not forgotten.

Cancellation interrupts restic and allows up to 30 seconds for cleanup before a forced stop. Quitting the TUI waits for its running task to finish cleanup. If a repository operation encounters a lock, SetupVault asks restic to remove stale locks and retries once; active locks are never forcibly removed. Backup, verification and restore for the same local profile are serialized. A power failure or forced process kill can still leave a lock; recovery is possible once restic considers it stale.

Overview displays the saved failure reason even after restarting. A successful verification clears an old verification failure. If a newer backup attempt was incomplete, the status says `Verified; backup incomplete`: the stored snapshot passed, but a new backup is still needed.

Scheduling uses your systemd user manager and an unlocked login keyring. This version requires an active desktop session and does not promise unattended backups while logged out. Failed scheduled jobs retry after an hour; persistent timers catch up when the user manager starts. Overview reports actual timer status.

State is in `$XDG_STATE_HOME/setupvault` (default `~/.local/state/setupvault`); the editable profile is `$XDG_CONFIG_HOME/setupvault/profile.json` (default `~/.config/setupvault`). Passwords and state are not published to GitHub. Separate profiles need separate config **and** state directories.

Dropbox browser connection and existing rclone remotes are supported. OneDrive connection is implemented but needs account testing. For Google Drive, configure an rclone remote with your own OAuth client first; SetupVault lists existing remotes. Provider authorization rules still apply. See [rclone Dropbox](https://rclone.org/dropbox/), [OneDrive](https://rclone.org/onedrive/) and [Drive](https://rclone.org/drive/) documentation.

## Build, test and package

```sh
make build
make test                 # race detector + real local restic backup/restore
make rpm                  # requires golang, rpm-build, systemd-rpm-macros
```

The [Fedora VM test plan](docs/e2e-test-plan.txt) and [execution report](docs/e2e-test-report.txt) document the original end-to-end CLI/TUI checks. The [guided recovery report](docs/recovery-implementation-test-report.txt) covers actual reinstalls, desktop settings, Undo, detached jobs and the tested builds and limits.

The live cloud test uploads only generated fixture settings into a new random `SetupVault-Tests` directory and deletes its own directory afterward:

```sh
SETUPVAULT_TEST_REMOTE=my-dropbox go test -run TestCloudBackupRestore -count=1 -v ./internal/vault
```

RPMs and source RPMs are written under `dist/rpmbuild`. Install a locally built RPM with `sudo dnf install ./dist/rpmbuild/RPMS/$(uname -m)/setupvault-*.rpm`. Then open **SetupVault** from the app menu or run `setupvault`. Contents → `s` authorizes the installed system export helper once. System export uses fixed paths. App recovery uses a separate authorized helper that accepts bounded package identifiers and a reviewed transaction; it never applies the staged system archive as root.

The source archive includes vendored Go dependencies for builds without network access. The spec and GitHub Actions prepare packages for COPR; **no COPR repository or official Fedora package is published yet**. To publish, create a COPR project with a Fedora account and submit the source RPM:

```sh
copr-cli build YOUR_COPR_NAME dist/rpmbuild/SRPMS/setupvault-*.src.rpm
```

Official Fedora inclusion is a separate package review and maintenance process. This repository currently targets Fedora 44; the vendored spec is intended for initial COPR distribution.

License: MIT. Dependencies retain their own licenses, included in the vendor source archive.
