package vault

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResticLockRecoveryDoesNotRemoveActiveLocks(t *testing.T) {
	for _, mode := range []string{"stale", "active", "unlock-fails", "other-error"} {
		t.Run(mode, func(t *testing.T) {
			a := fixture(t)
			dir := t.TempDir()
			t.Setenv("SETUPVAULT_LOCKTEST", dir)
			t.Setenv("SETUPVAULT_LOCKMODE", mode)
			fakeCommand(t, "restic", `
printf '%s\n' "$*" >> "$SETUPVAULT_LOCKTEST/calls"
case " $* " in
  *" --remove-all "*|*" --no-lock "*) exit 99 ;;
  *" unlock "*)
    [ "$SETUPVAULT_LOCKMODE" = unlock-fails ] && { echo 'network unavailable' >&2; exit 1; }
    [ "$SETUPVAULT_LOCKMODE" = stale ] && touch "$SETUPVAULT_LOCKTEST/cleared"
    exit 0 ;;
  *" check "*)
    [ "$SETUPVAULT_LOCKMODE" = other-error ] && { echo 'check failed' >&2; exit 3; }
    [ -f "$SETUPVAULT_LOCKTEST/cleared" ] && exit 0
    printf '%s\n' '{"message_type":"exit_error","code":11,"message":"repository is locked by PID 1234\naccess_token=private-test-token"}' >&2
    exit 11 ;;
esac
exit 99
`)
			_, err := a.restic(context.Background(), []string{"check"}, nil)
			calls, readErr := os.ReadFile(filepath.Join(dir, "calls"))
			if readErr != nil {
				t.Fatal(readErr)
			}
			wantCalls := 3
			if mode == "other-error" {
				wantCalls = 1
			} else if mode == "unlock-fails" {
				wantCalls = 2
			}
			if len(strings.Split(strings.TrimSpace(string(calls)), "\n")) != wantCalls || strings.Contains(string(calls), "--remove-all") || strings.Contains(string(calls), "--no-lock") {
				t.Fatalf("unexpected lock recovery calls: %s", calls)
			}
			if (err == nil) != (mode == "stale") {
				t.Fatalf("unexpected result in %s: %v", mode, err)
			}
			if mode == "active" && (!strings.Contains(err.Error(), "Repository is busy") || !strings.Contains(err.Error(), "PID 1234") || strings.Contains(err.Error(), "message_type") || strings.Contains(err.Error(), "private-test-token")) {
				t.Fatalf("active lock reason was hidden or leaked credentials: %v", err)
			}
		})
	}
}

func TestResticCancellationReleasesRealRepositoryLock(t *testing.T) {
	requireRestic(t)
	a := fixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := a.initialize(ctx); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(a.Profile.Home, "large-fixture")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	err = f.Truncate(128 << 20)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	operation, stop := context.WithCancel(ctx)
	defer stop()
	finished := make(chan error, 1)
	go func() {
		_, err := a.restic(operation, []string{"backup", "--compression", "off", "--limit-upload", "64", "--", path}, nil)
		finished <- err
	}()
	for {
		locks, err := os.ReadDir(filepath.Join(a.Profile.Repository, "locks"))
		if err != nil {
			t.Fatal(err)
		}
		if len(locks) > 0 {
			break
		}
		select {
		case err := <-finished:
			t.Fatalf("operation ended before it could be canceled: %v", err)
		case <-ctx.Done():
			t.Fatal("operation never acquired its repository lock")
		case <-time.After(5 * time.Millisecond):
		}
	}
	stop()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatal("cancellation not reported", err)
		}
	case <-ctx.Done():
		t.Fatal("restic did not stop after cancellation")
	}
	locks, err := os.ReadDir(filepath.Join(a.Profile.Repository, "locks"))
	if err != nil || len(locks) != 0 {
		t.Fatalf("canceled operation left repository locks: %v, %v", locks, err)
	}
	if _, err := a.runRestic(ctx, []string{"check"}, nil); err != nil {
		t.Fatal("repository could not be checked after cancellation", err)
	}
}

func TestSuccessfulVerificationClearsFailureAndPreservesIncompleteBackup(t *testing.T) {
	for _, incomplete := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "incomplete"}[incomplete], func(t *testing.T) {
			a := fixture(t)
			hash := sha256.Sum256([]byte("marker\n"))
			s := State{Status: "Verification failed", LastError: "old lock error", Snapshot: "fixture", MarkerSHA256: hex.EncodeToString(hash[:]), LastBackup: time.Now().Add(-time.Hour)}
			if incomplete {
				s.LastAttempt = time.Now()
			}
			if err := a.saveState(s); err != nil {
				t.Fatal(err)
			}
			fakeCommand(t, "restic", `
case " $* " in
  *" check "*) exit 0 ;;
  *" dump "*) printf 'marker\n'; exit 0 ;;
esac
exit 99
`)
			if err := a.Verify(context.Background()); err != nil {
				t.Fatal(err)
			}
			got := a.State()
			if got.LastVerified.IsZero() || strings.Contains(got.LastError, "old lock error") || got.Status == "Verification failed" {
				t.Fatalf("successful verification kept stale failure: %+v", got)
			}
			if incomplete && (got.Status != "Verified; backup incomplete" || !strings.Contains(got.LastError, "newer backup attempt")) || !incomplete && (got.Status != "Healthy" || got.LastError != "") {
				t.Fatalf("backup completeness was misreported: %+v", got)
			}
		})
	}
}

func TestConcurrentVerificationDoesNotOverwriteRunningBackupState(t *testing.T) {
	a := fixture(t)
	unlock, err := a.lockOperation()
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err := a.saveState(State{Status: "Running"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Verify(context.Background()); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatal("concurrent verification was allowed", err)
	}
	if got := a.State().Status; got != "Running" {
		t.Fatal("verification changed a running backup's state", got)
	}
}
