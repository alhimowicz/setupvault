package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const password = "only-a-cli-test-password"

func invoke(t *testing.T, ctx context.Context, args []string, input string) (int, map[string]any, string) {
	t.Helper()
	var out, stderr bytes.Buffer
	code := Run(ctx, args, "test", strings.NewReader(input), &out, &stderr)
	var result map[string]any
	decoder := json.NewDecoder(&out)
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("non-JSON response (exit %d): %v, %s", code, err, out.String())
	}
	if decoder.More() || out.Len() > 0 && strings.TrimSpace(out.String()) != "" {
		t.Fatal("extra stdout after JSON response")
	}
	if strings.Contains(stderr.String(), password) {
		t.Fatal("password in stderr")
	}
	return code, result, stderr.String()
}

func fixture(t *testing.T) (string, []string) {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	if err := os.MkdirAll(home, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	return root, []string{"--json", "--config-dir", filepath.Join(root, "config"), "--state-dir", filepath.Join(root, "state")}
}

func command(base []string, args ...string) []string {
	return append(append([]string{}, base...), args...)
}

func TestConfigurationAndValidation(t *testing.T) {
	root, base := fixture(t)
	ctx := context.Background()
	code, result, _ := invoke(t, ctx, command(base, "--set-config", "--repository", filepath.Join(root, "repo"), "--select", "apps,gnome", "--name", "fixture", "--include", filepath.Join(root, "custom"), "--exclude", "**/temporary/**"), "")
	if code != 0 || result["ok"] != true {
		t.Fatal(result)
	}
	path := filepath.Join(root, "config/profile.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("profile not private")
	}
	code, result, _ = invoke(t, ctx, command(base, "--set-config", "--keep-last", "0"), "")
	if code != 2 || result["ok"] != false {
		t.Fatal(result)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("invalid update changed profile")
	}
	code, result, _ = invoke(t, ctx, command(base, "--set-config", "--select=", "--remove-include", filepath.Join(root, "custom")), "")
	if code != 0 {
		t.Fatal(result)
	}
	data := result["data"].(map[string]any)
	if len(data["selected"].([]any)) != 0 {
		t.Fatal("groups not cleared")
	}
	code, result, _ = invoke(t, ctx, command(base, "--contents"), "")
	if code != 0 {
		t.Fatal(result)
	}
	if _, ok := result["data"].(map[string]any)["groups"]; !ok {
		t.Fatal("missing content groups")
	}
}

func TestInvalidInputAndNoImplicitInteraction(t *testing.T) {
	_, base := fixture(t)
	ctx := context.Background()
	for _, args := range [][]string{{"--backup", "--status"}, {"--status", "--include", "/tmp/file"}, {"--restore", "--snapshot", "latest"}, {"--activity", "--lines", "0"}, {"--unknown", password}, {"--timeout", "nonsense"}} {
		code, r, _ := invoke(t, ctx, command(base, args...), "")
		if code != 2 || r["ok"] != false {
			t.Fatal(args, r)
		}
		encoded, _ := json.Marshal(r)
		if strings.Contains(string(encoded), password) {
			t.Fatal("bad argument value leaked")
		}
	}
	for _, args := range [][]string{{"--connect", "dropbox"}, {"--enable-system"}, {"--import", "--file", "missing.age"}} {
		code, r, _ := invoke(t, ctx, command(base, args...), "")
		if code != 3 || r["ok"] != false {
			t.Fatal(args, r)
		}
	}
	code, r, _ := invoke(t, ctx, command(base, "--help"), "")
	if code != 0 || r["cli_version"] != float64(1) {
		t.Fatal(r)
	}
}

func TestPasswordFilesAndStdin(t *testing.T) {
	root, base := fixture(t)
	path := filepath.Join(root, "password")
	if err := os.WriteFile(path, []byte(password+"\r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := readPassword(context.Background(), path, false, strings.NewReader(""))
	if err != nil || p != password {
		t.Fatal("private password file", err)
	}
	p, err = readPassword(context.Background(), "", true, strings.NewReader(password+"\n"))
	if err != nil || p != password {
		t.Fatal("stdin password", err)
	}
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	code, r, _ := invoke(t, context.Background(), command(base, "--backup", "--password-file", path), "")
	if code != 2 {
		t.Fatal(r)
	}
	if _, err = readPassword(context.Background(), "", true, strings.NewReader(strings.Repeat("x", 5000))); err == nil {
		t.Fatal("unbounded input accepted")
	}
}

func TestCLIBackupImportRestoreWithoutKeyring(t *testing.T) {
	if _, err := exec.LookPath("restic"); err != nil {
		t.Skip("restic required")
	}
	root, base := fixture(t)
	ctx := context.Background()
	source := filepath.Join(root, "settings.json")
	if err := os.WriteFile(source, []byte(`{"theme":"dark"}`), 0600); err != nil {
		t.Fatal(err)
	}
	// Any accidental call to the keyring fails this entire flow.
	bin := filepath.Join(root, "tools")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "secret-tool"), []byte("#!/bin/sh\nexit 99\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	code, r, _ := invoke(t, ctx, command(base, "--set-config", "--repository", filepath.Join(root, "repo"), "--select=", "--include", source), "")
	if code != 0 {
		t.Fatal(r)
	}
	code, r, progress := invoke(t, ctx, command(base, "--backup", "--password-stdin", "--progress"), password+"\n")
	if code != 0 {
		t.Fatal(r)
	}
	if r["data"].(map[string]any)["status"] != "Healthy" {
		t.Fatal(r)
	}
	for _, line := range strings.Split(strings.TrimSpace(progress), "\n") {
		var event map[string]any
		if err := json.Unmarshal([]byte(line), &event); err != nil || event["event"] != "progress" {
			t.Fatal("invalid progress", line, err)
		}
	}
	code, r, _ = invoke(t, ctx, command(base, "--snapshots", "--password-stdin"), password)
	if code != 0 || len(r["data"].([]any)) != 1 {
		t.Fatal(r)
	}
	kit := filepath.Join(root, "recovery.age")
	code, r, _ = invoke(t, ctx, command(base, "--export", "--file", kit, "--password-stdin"), password)
	if code != 0 {
		t.Fatal(r)
	}
	newbase := []string{"--json", "--config-dir", filepath.Join(root, "imported"), "--state-dir", filepath.Join(root, "imported-state")}
	code, r, _ = invoke(t, ctx, command(newbase, "--import", "--file", kit, "--password-stdin"), "wrong-password-for-test")
	if code == 0 {
		t.Fatal("wrong password accepted")
	}
	code, r, _ = invoke(t, ctx, command(newbase, "--import", "--file", kit, "--password-stdin"), password)
	if code != 0 || r["data"].(map[string]any)["password_stored"] != false {
		t.Fatal(r)
	}
	target := filepath.Join(root, "restored")
	code, r, _ = invoke(t, ctx, command(newbase, "--restore", "--snapshot", "latest", "--target", target, "--password-stdin"), password)
	if code != 0 {
		t.Fatal(r)
	}
	data, err := os.ReadFile(filepath.Join(target, source))
	if err != nil || string(data) != `{"theme":"dark"}` {
		t.Fatalf("restore mismatch: %s %v", data, err)
	}
	code, r, _ = invoke(t, ctx, command(newbase, "--restore", "--snapshot", "latest", "--target", target, "--password-stdin"), password)
	if code == 0 {
		t.Fatal("nonempty target overwritten")
	}
}

func TestCredentialSearchDoesNotUnlock(t *testing.T) {
	root, base := fixture(t)
	bin := filepath.Join(root, "tools")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nfor arg in \"$@\"; do [ \"$arg\" = '--unlock' ] && exit 88; done\nprintf 'secret = [not available]\\n'\n"
	if err := os.WriteFile(filepath.Join(bin, "secret-tool"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+":"+os.Getenv("PATH"))
	code, r, _ := invoke(t, context.Background(), command(base, "--backup"), "")
	if code != 3 {
		t.Fatal(r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	code, r, _ = invoke(t, ctx, command(base, "--backup"), "")
	if code != 130 {
		t.Fatal(r)
	}
}

func TestLegacyCommands(t *testing.T) {
	_, base := fixture(t)
	for _, args := range [][]string{{"status", "--json"}, {"config", "--json"}, {"version", "--json"}} {
		full := append(append([]string{}, base[1:]...), args...)
		code, r, _ := invoke(t, context.Background(), full, "")
		if code != 0 || r["ok"] != true {
			t.Fatal(r)
		}
	}
	if err := foregroundTerminal(strings.NewReader("")); err == nil {
		t.Fatal("nonterminal interface allowed")
	}
}

func TestPasswordInputDeadlineAndFIFO(t *testing.T) {
	root, base := fixture(t)
	fifo := filepath.Join(root, "fifo")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPassword(context.Background(), fifo, false, strings.NewReader("")); err == nil {
		t.Fatal("FIFO accepted as password file")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()
	var out, stderr bytes.Buffer
	code := Run(context.Background(), command(base, "--import", "--file", "missing.age", "--password-stdin", "--timeout", "10ms"), "test", r, &out, &stderr)
	if code != 124 {
		t.Fatalf("stdin deadline not honored: %d %s", code, out.String())
	}
}

func TestLegacyRawStatusAndLiteralFlagValue(t *testing.T) {
	_, base := fixture(t)
	var out, stderr bytes.Buffer
	args := append(append([]string{}, base[1:]...), "status")
	code := Run(context.Background(), args, "test", strings.NewReader(""), &out, &stderr)
	var data map[string]any
	if err := json.Unmarshal(out.Bytes(), &data); err != nil || code != 0 || data["status"] != "Not backed up" {
		t.Fatalf("legacy status changed: %s %v", out.String(), err)
	}
	out.Reset()
	args = append(append([]string{}, base[1:]...), "--set-config", "--name", "--json")
	code = Run(context.Background(), args, "test", strings.NewReader(""), &out, &stderr)
	if err := json.Unmarshal(out.Bytes(), &data); err != nil || code != 0 || data["name"] != "--json" || data["cli_version"] != nil {
		t.Fatalf("literal flag value treated as option: %s %v", out.String(), err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	code, _, _ = invoke(t, ctx, command(base, "--backup"), "")
	if code != 124 {
		t.Fatal("expired operation did not report a timeout", code)
	}
}
