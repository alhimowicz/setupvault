package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/alhimowicz/setupvault/internal/tui"
	"github.com/alhimowicz/setupvault/internal/vault"
	"github.com/charmbracelet/x/term"
	"golang.org/x/sys/unix"
)

type response struct {
	Version int      `json:"cli_version"`
	OK      bool     `json:"ok"`
	Data    any      `json:"data,omitempty"`
	Error   *failure `json:"error,omitempty"`
}
type failure struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Exit    int    `json:"-"`
}

func (e *failure) Error() string   { return e.Message }
func invalid(message string) error { return &failure{"invalid_arguments", message, 2} }

type list []string

func (v *list) String() string     { return strings.Join(*v, ",") }
func (v *list) Set(s string) error { *v = append(*v, s); return nil }

type options struct {
	job                                                                            string
	wait                                                                           bool
	skipApps, skipFiles                                                            list
	config, state, file, snapshot, target, passwordFile                            string
	json, nonInteractive, passwordStdin, storePassword, allowInteraction, progress bool
	name, repository, selected, rcloneConfig, calendar, provider                   string
	keep, lines                                                                    int
	include, exclude, removeInclude, removeExclude                                 list
	timeout                                                                        time.Duration
}

func Run(ctx context.Context, args []string, version string, in io.Reader, out, stderr io.Writer) int {
	var o options
	wantedJSON := false
	for _, arg := range args {
		if arg == "--json" || arg == "--json=true" {
			wantedJSON = true
		}
	}
	f := flag.NewFlagSet("setupvault", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	actions := map[string]*bool{}
	for name, description := range map[string]string{
		"status": "Show profile, backup state and next scheduled run", "backup": "Create and verify an encrypted backup",
		"verify": "Check the last backup and restore its sample", "snapshots": "List repository snapshots",
		"restore": "Restore --snapshot into empty --target", "export": "Export encrypted recovery profile to --file",
		"import": "Import encrypted recovery profile from --file", "config": "Read current profile",
		"set-config": "Update profile using configuration flags", "contents": "List content groups, paths and exclusions",
		"connections": "List rclone connections", "test-connection": "Test destination write and read", "quota": "Read cloud storage usage",
		"set-password": "Store supplied password in desktop keyring", "enable-system": "Authorize installed protected system export helper",
		"activity": "Read recent activity", "version": "Show application version",
		"plan-recovery": "Prepare and compare a guided setup recovery", "apply-recovery": "Start an asynchronous reviewed recovery job", "recovery-status": "Read a recovery job or list jobs", "resume-recovery": "Resume a stopped recovery", "cancel-recovery": "Stop recovery after the current transaction", "undo-recovery-settings": "Undo applied user files and desktop settings",
	} {
		actions[name] = f.Bool(name, false, description)
	}
	f.StringVar(&o.config, "config-dir", "", "Profile directory")
	f.StringVar(&o.state, "state-dir", "", "Local state directory")
	f.BoolVar(&o.json, "json", false, "Versioned JSON response, including errors; implies noninteractive operation")
	f.BoolVar(&o.nonInteractive, "non-interactive", false, "Never read terminal input or unlock a keyring")
	f.DurationVar(&o.timeout, "timeout", 0, "Operation deadline, e.g. 30s or 15m; 0 disables")
	f.StringVar(&o.file, "file", "", "Recovery profile path for --import or --export")
	f.StringVar(&o.snapshot, "snapshot", "", "Snapshot ID or latest for --restore")
	f.StringVar(&o.job, "job", "", "Recovery job ID")
	f.BoolVar(&o.wait, "wait", false, "Wait for asynchronous recovery; a waiting timeout leaves the job running")
	f.Var(&o.skipApps, "skip-app", "With --plan-recovery: deselect an app ID or name; repeatable")
	f.Var(&o.skipFiles, "skip-file", "With --plan-recovery: deselect a relative home path; repeatable")
	f.StringVar(&o.target, "target", "", "Empty absolute restore folder")
	f.StringVar(&o.passwordFile, "password-file", "", "Password from private regular file; '-' reads stdin")
	f.BoolVar(&o.passwordStdin, "password-stdin", false, "Read password from stdin through EOF")
	f.BoolVar(&o.storePassword, "store-password", false, "On import, also save in desktop keyring; may request unlock")
	f.BoolVar(&o.allowInteraction, "allow-interaction", false, "Allow requested browser sign-in or administrator/keyring authentication")
	f.BoolVar(&o.progress, "progress", false, "Backup progress on stderr; JSON lines with --json")
	f.StringVar(&o.name, "name", "", "With --set-config: profile name")
	f.StringVar(&o.repository, "repository", "", "With --set-config: absolute folder or rclone:remote:folder")
	f.StringVar(&o.selected, "select", "", "With --set-config: replace groups, comma-separated; empty selects none")
	f.IntVar(&o.keep, "keep-last", 7, "With --set-config: retained snapshots, 1–10000")
	f.StringVar(&o.rcloneConfig, "rclone-config", "", "With --set-config: rclone configuration file")
	f.Var(&o.include, "include", "With --set-config: add absolute path; repeatable")
	f.Var(&o.exclude, "exclude", "With --set-config: add exclusion pattern; repeatable")
	f.Var(&o.removeInclude, "remove-include", "With --set-config: remove custom path; repeatable")
	f.Var(&o.removeExclude, "remove-exclude", "With --set-config: remove exclusion pattern; repeatable")
	f.StringVar(&o.calendar, "schedule", "", "Set systemd calendar expression or off; uses keyring password")
	f.StringVar(&o.provider, "connect", "", "Connect dropbox or onedrive; requires --allow-interaction")
	f.IntVar(&o.lines, "lines", 50, "With --activity: recent lines, 1–1000")
	finish := func(data any, err error) int {
		if err != nil {
			e := &failure{"operation_failed", err.Error(), 1}
			var typed *failure
			var dependency *vault.DependencyError
			if errors.As(err, &typed) {
				e = typed
			} else if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				e = &failure{"canceled", "operation canceled", 130}
			} else if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
				e = &failure{"timeout", "operation deadline exceeded", 124}
			} else if errors.Is(err, vault.ErrRecoveryInteraction) {
				e = &failure{"interaction_required", err.Error(), 3}
			} else if errors.As(err, &dependency) {
				e = &failure{"dependency_missing", err.Error(), 3}
			} else if errors.Is(err, vault.ErrCredentialsRequired) {
				e = &failure{"credentials_required", err.Error(), 3}
			}
			if o.json {
				_ = json.NewEncoder(out).Encode(response{1, false, data, e})
			} else {
				fmt.Fprintln(stderr, "SetupVault:", e.Message)
			}
			return e.Exit
		}
		if o.json {
			if err := json.NewEncoder(out).Encode(response{Version: 1, OK: true, Data: data}); err != nil {
				return 1
			}
		} else if data != nil {
			if err := json.NewEncoder(out).Encode(data); err != nil {
				return 1
			}
		}
		return 0
	}
	help := func() int {
		if !o.json {
			fmt.Fprintln(out, "Usage: setupvault [--ACTION] [--FLAGS]\nRun without flags for the terminal interface. Original commands remain supported.")
			f.SetOutput(out)
			f.PrintDefaults()
			return 0
		}
		type parameter struct {
			Name        string `json:"name"`
			Default     string `json:"default"`
			Description string `json:"description"`
		}
		var parameters []parameter
		f.VisitAll(func(p *flag.Flag) { parameters = append(parameters, parameter{"--" + p.Name, p.DefValue, p.Usage}) })
		return finish(map[string]any{"version": version, "parameters": parameters, "exit_codes": map[int]string{0: "success", 1: "operation_failed", 2: "invalid_arguments", 3: "credentials_or_interaction_required", 124: "timeout", 130: "canceled"}}, nil)
	}
	if err := f.Parse(args); err != nil {
		o.json = o.json || wantedJSON
		if errors.Is(err, flag.ErrHelp) {
			return help()
		}
		return finish(nil, invalid("invalid flags; use --help"))
	}
	// Preserve legacy positional commands, including global flags before the command.
	legacy := false
	if rest := f.Args(); len(rest) > 0 {
		legacy = true
		name := rest[0]
		if actions[name] == nil {
			return finish(nil, invalid("unknown command; use --help"))
		}
		_ = f.Set(name, "true")
		rest = rest[1:]
		if (name == "import" || name == "export") && len(rest) > 0 && !strings.HasPrefix(rest[0], "-") {
			rest = append([]string{"--file", rest[0]}, rest[1:]...)
		}
		if name == "restore" && len(rest) >= 2 && !strings.HasPrefix(rest[0], "-") && !strings.HasPrefix(rest[1], "-") {
			rest = append([]string{"--snapshot", rest[0], "--target", rest[1]}, rest[2:]...)
		}
		if err := f.Parse(rest); err != nil {
			o.json = o.json || wantedJSON
			if errors.Is(err, flag.ErrHelp) {
				return help()
			}
			return finish(nil, invalid("invalid command flags; use --help"))
		}
		if len(f.Args()) > 0 {
			return finish(nil, invalid("unexpected positional arguments; use named flags"))
		}
	}
	o.nonInteractive = o.nonInteractive || o.json
	seen := map[string]bool{}
	f.Visit(func(p *flag.Flag) { seen[p.Name] = true })
	var chosen []string
	for name, p := range actions {
		if *p {
			chosen = append(chosen, name)
		}
	}
	if seen["schedule"] {
		chosen = append(chosen, "schedule")
	}
	if seen["connect"] {
		chosen = append(chosen, "connect")
	}
	if len(chosen) > 1 {
		return finish(nil, invalid("choose exactly one action"))
	}
	if o.timeout < 0 {
		return finish(nil, invalid("--timeout must not be negative"))
	}
	if o.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, o.timeout)
		defer cancel()
	}
	if len(chosen) == 0 {
		if o.nonInteractive {
			return finish(nil, invalid("choose an action, for example --status"))
		}
		for name := range seen {
			if name != "config-dir" && name != "state-dir" {
				return finish(nil, invalid("choose an action; use --help"))
			}
		}
		if err := foregroundTerminal(in); err != nil {
			return finish(nil, err)
		}
		a, err := vault.New(o.config, o.state)
		if err != nil {
			return finish(nil, err)
		}
		model, err := tea.NewProgram(tui.New(a), tea.WithInput(in), tea.WithOutput(out), tea.WithContext(ctx)).Run()
		if m, ok := model.(tui.Model); ok {
			m.Shutdown()
		}
		return finish(nil, err)
	}
	action := chosen[0]
	if legacy && action == "backup" && !o.json {
		o.progress = true
	}
	if err := validateFlags(action, seen, &o); err != nil {
		return finish(nil, err)
	}
	if action == "version" {
		if !o.json {
			fmt.Fprintln(out, "SetupVault", version)
			return 0
		}
		return finish(map[string]string{"version": version}, nil)
	}
	a, err := vault.New(o.config, o.state)
	if err != nil {
		return finish(nil, err)
	}
	if action == "import" || action == "set-password" || action == "backup" || action == "verify" || action == "snapshots" || action == "restore" || action == "export" || action == "plan-recovery" && o.job == "" {
		if o.passwordFile != "" || o.passwordStdin {
			password, e := readPassword(ctx, o.passwordFile, o.passwordStdin, in)
			if e != nil {
				if errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded) {
					return finish(nil, e)
				}
				return finish(nil, invalid(e.Error()))
			}
			if e = a.UsePassword(password); e != nil {
				return finish(nil, invalid(e.Error()))
			}
		} else if action == "import" || action == "set-password" {
			if o.nonInteractive {
				return finish(nil, vault.ErrCredentialsRequired)
			}
			password, e := terminalPassword()
			if e != nil {
				return finish(nil, e)
			}
			if e = a.UsePassword(password); e != nil {
				return finish(nil, e)
			}
		} else if o.nonInteractive {
			if err = a.UseUnlockedKeyring(ctx); err != nil {
				return finish(nil, err)
			}
		}
	}
	data, err := execute(ctx, a, action, &o, seen, stderr)
	if legacy && !o.json {
		if action == "status" {
			data = a.State()
		}
		if action == "backup" || action == "verify" || action == "restore" || action == "export" || action == "import" {
			data = nil
		}
	}
	return finish(data, err)
}

func foregroundTerminal(in io.Reader) error {
	f, ok := in.(*os.File)
	if !ok || !term.IsTerminal(f.Fd()) {
		return invalid("the interface requires a foreground terminal; use --status --json for scripts")
	}
	pgid, err := unix.IoctlGetInt(int(f.Fd()), unix.TIOCGPGRP)
	if err != nil {
		return errors.New("cannot determine the terminal foreground process group")
	}
	if pgid != unix.Getpgrp() {
		return errors.New("the interface needs the foreground terminal; run fg in the launching shell, or start setupvault without &")
	}
	return nil
}

func validateFlags(action string, seen map[string]bool, o *options) error {
	allowed := map[string]bool{"json": true, "non-interactive": true, "config-dir": true, "state-dir": true, "timeout": true, action: true}
	add := func(names ...string) {
		for _, s := range names {
			allowed[s] = true
		}
	}
	switch action {
	case "plan-recovery":
		add("snapshot", "job", "select", "skip-app", "skip-file", "password-file", "password-stdin", "progress")
		if o.job == "" && o.snapshot == "" {
			return invalid("--plan-recovery requires --snapshot or --job")
		}
	case "apply-recovery", "resume-recovery":
		add("job", "wait", "progress", "allow-interaction")
		if o.job == "" {
			return invalid("recovery action requires --job")
		}
	case "cancel-recovery", "undo-recovery-settings":
		add("job")
		if o.job == "" {
			return invalid("recovery action requires --job")
		}
	case "recovery-status":
		add("job")
	case "set-config":
		add("name", "repository", "select", "keep-last", "rclone-config", "include", "exclude", "remove-include", "remove-exclude")
	case "backup":
		add("password-file", "password-stdin", "progress")
	case "verify", "snapshots":
		add("password-file", "password-stdin")
	case "restore":
		add("password-file", "password-stdin", "snapshot", "target")
		if o.snapshot == "" || o.target == "" {
			return invalid("--restore requires --snapshot and --target")
		}
	case "export", "import":
		add("password-file", "password-stdin", "file")
		if o.file == "" {
			return invalid("--import/--export requires --file")
		}
		if action == "import" {
			add("store-password")
		}
	case "set-password":
		add("password-file", "password-stdin", "allow-interaction")
	case "connect", "enable-system":
		add("allow-interaction")
	case "activity":
		add("lines")
		if o.lines < 1 || o.lines > 1000 {
			return invalid("--lines must be 1–1000")
		}
	}
	for name := range seen {
		if !allowed[name] {
			return invalid("--" + name + " does not apply to --" + action)
		}
	}
	if o.passwordStdin && o.passwordFile != "" {
		return invalid("choose --password-file or --password-stdin")
	}
	return nil
}

func execute(ctx context.Context, a *vault.App, action string, o *options, seen map[string]bool, stderr io.Writer) (any, error) {
	switch action {
	case "plan-recovery", "apply-recovery", "recovery-status", "resume-recovery", "cancel-recovery", "undo-recovery-settings":
		return executeRecovery(ctx, a, action, o, seen, stderr)
	case "status":
		_, err := os.Stat(a.ProfilePath())
		return map[string]any{"configured": err == nil, "name": a.Profile.Name, "state": a.State(), "next_run": a.NextRun(ctx)}, nil
	case "config":
		return a.Profile, nil
	case "set-config":
		p := a.Profile
		if seen["name"] {
			p.Name = o.name
		}
		if seen["repository"] {
			p.Repository = o.repository
		}
		if seen["select"] {
			p.Selected = []string{}
			if o.selected != "" {
				for _, s := range strings.Split(o.selected, ",") {
					p.Selected = append(p.Selected, strings.TrimSpace(s))
				}
			}
		}
		if seen["keep-last"] {
			p.KeepLast = o.keep
		}
		if seen["rclone-config"] {
			p.RcloneConfig = o.rcloneConfig
		}
		p.CustomPaths = updateList(p.CustomPaths, o.include, o.removeInclude)
		p.Excludes = updateList(p.Excludes, o.exclude, o.removeExclude)
		if err := p.Validate(); err != nil {
			return nil, invalid(err.Error())
		}
		a.Profile = p
		if err := a.Save(); err != nil {
			return nil, err
		}
		return p, nil
	case "contents":
		return map[string]any{"groups": a.Groups(), "custom_paths": a.Profile.CustomPaths, "excludes": a.Exclusions(), "inventory": map[string]any{"path": a.InventoryDir(), "generated_on_backup": true}, "system_access_enabled": a.Profile.SystemEnabled}, nil
	case "connections":
		r, err := a.Remotes(ctx)
		if r == nil {
			r = []vault.Remote{}
		}
		return r, err
	case "connect":
		if !o.allowInteraction {
			return nil, &failure{"interaction_required", "browser sign-in requires --allow-interaction", 3}
		}
		r, err := a.Connect(ctx, o.provider)
		return map[string]string{"remote": r}, err
	case "test-connection":
		return map[string]string{"repository": a.Profile.Repository}, a.TestConnection(ctx)
	case "quota":
		return a.Quota(ctx)
	case "backup":
		var progress func(vault.Progress)
		if o.progress {
			progress = func(p vault.Progress) {
				if o.json {
					_ = json.NewEncoder(stderr).Encode(map[string]any{"cli_version": 1, "event": "progress", "data": p})
				} else {
					fmt.Fprintln(stderr, p.Phase)
				}
			}
		}
		err := a.Backup(ctx, progress)
		return a.State(), err
	case "verify":
		err := a.Verify(ctx)
		return a.State(), err
	case "snapshots":
		return a.Snapshots(ctx)
	case "restore":
		err := a.Restore(ctx, o.snapshot, o.target)
		return map[string]string{"snapshot": o.snapshot, "target": o.target}, err
	case "export":
		return map[string]string{"file": o.file}, a.Export(ctx, o.file)
	case "import":
		password, err := a.Password(ctx)
		if err != nil {
			return nil, err
		}
		if err = a.ImportRecovery(ctx, o.file, password, o.storePassword || !o.nonInteractive); err != nil {
			return nil, err
		}
		return map[string]any{"profile": a.Profile, "password_stored": o.storePassword || !o.nonInteractive}, nil
	case "set-password":
		password, err := a.Password(ctx)
		if err != nil {
			return nil, err
		}
		if o.nonInteractive && !o.allowInteraction {
			probe := *a
			if err = probe.UseUnlockedKeyring(ctx); err != nil {
				return nil, &failure{"interaction_required", "keyring storage needs an unlocked existing item or --allow-interaction; password files support unattended operation", 3}
			}
		}
		if err = a.StorePassword(ctx, password); err != nil {
			return nil, err
		}
		if err = a.Save(); err != nil {
			return nil, err
		}
		return map[string]bool{"password_stored": true}, nil
	case "schedule":
		calendar := o.calendar
		if strings.EqualFold(calendar, "off") {
			calendar = ""
		}
		if calendar != "" {
			if err := a.UseUnlockedKeyring(ctx); err != nil {
				return nil, err
			}
		}
		if err := a.SetSchedule(ctx, calendar); err != nil {
			return nil, err
		}
		return map[string]string{"schedule": a.Profile.Schedule, "next_run": a.NextRun(ctx)}, nil
	case "enable-system":
		if o.nonInteractive && !o.allowInteraction {
			return nil, &failure{"interaction_required", "administrator authentication requires --allow-interaction", 3}
		}
		if err := a.EnableSystem(ctx); err != nil {
			return nil, err
		}
		return a.Profile, nil
	case "activity":
		f, err := os.Open(filepath.Join(a.StateDir, "activity.log"))
		if errors.Is(err, os.ErrNotExist) {
			return []string{}, nil
		}
		if err != nil {
			return nil, err
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return nil, err
		}
		offset := max(int64(0), info.Size()-(1<<20))
		if _, err = f.Seek(offset, io.SeekStart); err != nil {
			return nil, err
		}
		data, err := io.ReadAll(io.LimitReader(f, 1<<20))
		if err != nil {
			return nil, err
		}
		if len(data) == 0 {
			return []string{}, nil
		}
		lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
		if offset > 0 {
			lines = lines[1:]
		}
		return lines[max(0, len(lines)-o.lines):], nil
	}
	return nil, invalid("unknown action")
}

func updateList(current, add, remove []string) []string {
	result := []string{}
	for _, s := range append(append([]string{}, current...), add...) {
		skip := false
		for _, v := range remove {
			if v == s {
				skip = true
			}
		}
		for _, v := range result {
			if v == s {
				skip = true
			}
		}
		if !skip {
			result = append(result, s)
		}
	}
	return result
}
