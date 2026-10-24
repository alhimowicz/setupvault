package vault

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRecoveryCommandDoesNotWaitForOrphanPipes(t *testing.T) {
	for _, exit := range []string{"0", "7"} {
		start := time.Now()
		out, err := commandOutput(context.Background(), "/bin/sh", "-c", "sleep 30 & echo $!; exit "+exit)
		if pid, e := strconv.Atoi(strings.TrimSpace(string(out))); e == nil {
			if p, e := os.FindProcess(pid); e == nil {
				_ = p.Kill()
			}
		}
		if time.Since(start) > 8*time.Second {
			t.Fatal("inherited output pipe kept the command waiting")
		}
		if (err != nil) != (exit != "0") {
			t.Fatalf("command exit %s was misreported: %v", exit, err)
		}
	}
}

func TestRecoveryInventoryRejectsLinksAndOversizeFiles(t *testing.T) {
	dir := t.TempDir()
	secret := filepath.Join(t.TempDir(), "outside")
	put(t, secret, "outside content")
	if err := os.Symlink(secret, filepath.Join(dir, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := readRecoveryInventory(dir, "linked", 100); err == nil {
		t.Fatal("inventory followed an outside symlink")
	}
	put(t, filepath.Join(dir, "large"), "12345")
	if _, err := readRecoveryInventory(dir, "large", 4); err == nil {
		t.Fatal("inventory size limit was ignored")
	}
}

func TestSourceApprovalBindsReviewedIdentity(t *testing.T) {
	p := RecoveryPackage{Scope: "user", Origin: "fixture", URL: "https://fixture.example/repo/", Key: "reviewed", AddRemote: true}
	ids, err := approvedRecoverySources([]RecoveryPackage{p}, []string{"user:fixture"})
	if err != nil || len(ids) != 1 || ids[0] != p.SourceID() {
		t.Fatal("friendly source name was not bound to the reviewed identity", ids, err)
	}
	p.URL = "https://different.example/repo/"
	if _, err = approvedRecoverySources([]RecoveryPackage{p}, ids); err == nil {
		t.Fatal("source approval survived a URL change")
	}
	if validRecoverySource(RecoveryPackage{URL: "https://user:secret@example.com/?token=secret"}) == nil {
		t.Fatal("source accepted credentials")
	}
	listing := string(publicRemoteListing([]byte("fixture https://user:secret@example.com/repo/?token=secret system\n")))
	if strings.Contains(listing, "secret") || !strings.Contains(listing, "https://example.com/repo/") {
		t.Fatal("remote listing exposed credentials", listing)
	}
}

func TestResumedDNFTransactionStaysWithinReview(t *testing.T) {
	expected := []byte(`{"version":"1","rpms":[{"nevra":"hello-1-1.x86_64","action":"Install","repo_id":"fedora"},{"nevra":"tree-1-1.x86_64","action":"Install","repo_id":"fedora"}]}`)
	remaining := []byte(`{"version":"1","rpms":[{"nevra":"tree-1-1.x86_64","action":"Install","repo_id":"fedora"}]}`)
	if !reviewedDNF(remaining, expected) || !reviewedDNF(nil, expected) {
		t.Fatal("already completed packages prevented resume")
	}
	for _, changed := range []string{strings.ReplaceAll(string(remaining), "tree-1-1", "tree-2-1"), strings.ReplaceAll(string(remaining), "fedora", "different"), strings.ReplaceAll(string(remaining), "Install", "Remove")} {
		if reviewedDNF([]byte(changed), expected) {
			t.Fatal("unreviewed change was allowed", changed)
		}
	}
}

func TestDesktopTerminalRecoveryKeys(t *testing.T) {
	for _, path := range []string{"/org/gnome/Ptyxis/font-name", "/org/gnome/Ptyxis/Profiles/id/palette", "/org/gnome/Console/theme", "/org/gnome/terminal/legacy/default-show-menubar"} {
		if !desktopKey(path) || recoverySchema(path[:strings.LastIndex(path, "/")+1], "") == "" {
			t.Fatal("terminal preferences are not covered", path)
		}
	}
	if desktopKey("/org/gnome/desktop/peripherals/touchpad/speed") {
		t.Fatal("machine preference was included")
	}
}

func TestPackageStopControlIsExplicitAndBounded(t *testing.T) {
	dir := t.TempDir()
	put(t, filepath.Join(dir, "stop"), "0")
	if packageStopRequested(dir) {
		t.Fatal("new package job was already stopped")
	}
	if err := requestPackageStop(dir); err != nil {
		t.Fatal(err)
	}
	if !packageStopRequested(dir) {
		t.Fatal("system installer did not observe the stop request")
	}
}

func TestFailedPreparationCannotResumeAsSuccessfulRecovery(t *testing.T) {
	a := fixture(t)
	id := strings.Repeat("b", 32)
	if err := os.MkdirAll(filepath.Join(a.StateDir, "recovery", id), 0700); err != nil {
		t.Fatal(err)
	}
	j := &RecoveryJob{Version: 1, ID: id, ProfileID: a.Profile.ID, TargetHome: a.Profile.Home, Status: "Failed", Phase: "Restoring and verifying private staging data"}
	if err := a.saveRecovery(j); err != nil {
		t.Fatal(err)
	}
	if _, err := a.ResumeRecovery(context.Background(), id, false); err == nil || !strings.Contains(err.Error(), "preparation") {
		t.Fatal("incomplete staging was accepted for recovery", err)
	}
}
