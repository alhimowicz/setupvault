package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/alhimowicz/setupvault/internal/cli"
	"github.com/alhimowicz/setupvault/internal/vault"
)

var version = "dev"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM, syscall.SIGHUP)
	code := run(ctx, os.Args[1:])
	cancel()
	os.Exit(code)
}
func run(ctx context.Context, args []string) int {
	var err error
	if len(args) == 1 && args[0] == "enable-system" {
		err = vault.EnableSystemAccess()
	} else if len(args) == 2 && args[0] == "internal-system-export" {
		err = vault.ExportSystem(args[1])
	} else if len(args) == 4 && args[0] == "internal-recovery-worker" {
		err = vault.RunRecoveryWorker(ctx, args[1], args[2], args[3])
	} else if len(args) == 1 && args[0] == "internal-authorize-recovery" {
		err = vault.AuthorizeRecoveryPackages(ctx, os.Stdin)
	} else if len(args) == 3 && args[0] == "internal-recovery-packages" {
		err = vault.RunRecoveryPackages(ctx, args[1], args[2])
	} else {
		v := version
		if v == "dev" {
			if info, ok := debug.ReadBuildInfo(); ok && info.Main.Version != "" && info.Main.Version != "(devel)" {
				v = info.Main.Version
			}
		}
		return cli.Run(ctx, args, v, os.Stdin, os.Stdout, os.Stderr)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "SetupVault:", err)
		return 1
	}
	return 0
}
