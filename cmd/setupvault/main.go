package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	tea "charm.land/bubbletea/v2"
	"github.com/alhimowicz/setupvault/internal/tui"
	"github.com/alhimowicz/setupvault/internal/vault"
	"github.com/charmbracelet/x/term"
)

var version = "dev"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "SetupVault:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("setupvault", flag.ContinueOnError)
	config := flags.String("config-dir", "", "Profile directory (before subcommand)")
	state := flags.String("state-dir", "", "Local state directory (before subcommand)")
	showVersion := flags.Bool("version", false, "Print version")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: setupvault [options] [status|backup|verify|snapshots|restore ID NEW_FOLDER|export FILE|import FILE]")
		fmt.Fprintln(flags.Output(), "Run without a subcommand to open the terminal interface.")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if *showVersion {
		v := version
		if v == "dev" {
			if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
				v = info.Main.Version
			}
		}
		fmt.Println("SetupVault", v)
		return nil
	}
	args = flags.Args()
	if len(args) == 1 && args[0] == "enable-system" {
		return vault.EnableSystemAccess()
	}
	if len(args) == 2 && args[0] == "internal-system-export" {
		return vault.ExportSystem(args[1])
	}
	a, err := vault.New(*config, *state)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		_, err = tea.NewProgram(tui.New(a), tea.WithContext(ctx)).Run()
		return err
	}
	switch {
	case len(args) == 1 && args[0] == "status":
		return json.NewEncoder(os.Stdout).Encode(a.State())
	case len(args) == 1 && args[0] == "backup":
		return a.Backup(ctx, func(p vault.Progress) { fmt.Fprintln(os.Stderr, p.Phase) })
	case len(args) == 1 && args[0] == "verify":
		return a.Verify(ctx)
	case len(args) == 1 && args[0] == "snapshots":
		snapshots, err := a.Snapshots(ctx)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(snapshots)
	case len(args) == 3 && args[0] == "restore":
		return a.Restore(ctx, args[1], args[2])
	case len(args) == 2 && args[0] == "export":
		return a.Export(ctx, args[1])
	case len(args) == 2 && args[0] == "import":
		password, err := readPassword()
		if err != nil {
			return err
		}
		return a.Import(ctx, args[1], password)
	default:
		flags.Usage()
		return errors.New("unknown command or missing arguments")
	}
}

func readPassword() (string, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fmt.Fprint(f, "Recovery password: ")
	password, err := term.ReadPassword(f.Fd())
	fmt.Fprintln(f)
	return string(password), err
}
