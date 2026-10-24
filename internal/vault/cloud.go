package vault

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

type Remote struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

var sensitive = regexp.MustCompile(`(?i)(access_token|refresh_token|client_secret|authid|token|password)(["\s:=]+)[^\s&,"}]+`)

func (a *App) rclone(ctx context.Context, stdin []byte, args ...string) ([]byte, error) {
	if err := CheckDependency("rclone"); err != nil {
		return nil, err
	}
	if a.Profile.RcloneConfig != "" {
		args = append([]string{"--config", a.Profile.RcloneConfig}, args...)
	}
	cmd := exec.CommandContext(ctx, "rclone", args...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		message := sensitive.ReplaceAllString(strings.TrimSpace(stderr.String()), "$1$2[REDACTED]")
		if len(message) > 2000 {
			message = message[len(message)-2000:]
		}
		if message == "" {
			message = err.Error()
		}
		return nil, fmt.Errorf("cloud connection: %s", message)
	}
	return out.Bytes(), nil
}

type Quota struct {
	Total uint64 `json:"total"`
	Used  uint64 `json:"used"`
	Free  uint64 `json:"free"`
}

func (a *App) Quota(ctx context.Context) (Quota, error) {
	var q Quota
	if !strings.HasPrefix(a.Profile.Repository, "rclone:") {
		return q, errors.New("quota is available for cloud destinations")
	}
	remote := strings.TrimPrefix(a.Profile.Repository, "rclone:")
	name, _, ok := strings.Cut(remote, ":")
	if !ok {
		return q, errors.New("invalid remote")
	}
	out, err := a.rclone(ctx, nil, "about", name+":", "--json")
	if err != nil {
		return q, err
	}
	err = json.Unmarshal(out, &q)
	return q, err
}

func (a *App) Remotes(ctx context.Context) ([]Remote, error) {
	out, err := a.rclone(ctx, nil, "listremotes", "--long")
	if err != nil {
		return nil, err
	}
	var remotes []Remote
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 {
			remotes = append(remotes, Remote{strings.TrimSuffix(fields[0], ":"), fields[1]})
		}
	}
	return remotes, nil
}

func (a *App) Connect(ctx context.Context, provider string) (string, error) {
	if provider != "dropbox" && provider != "onedrive" {
		return "", errors.New("Google Drive requires an existing rclone connection with its own OAuth client; automatic setup currently supports Dropbox and OneDrive")
	}
	name := "setupvault-" + provider
	remotes, err := a.Remotes(ctx)
	if err != nil {
		return "", err
	}
	for _, r := range remotes {
		if r.Name == name {
			return name, nil
		}
	}
	_, err = a.rclone(ctx, nil, "config", "create", name, provider, "config_is_local=true", "--no-output")
	return name, err
}

func (a *App) TestConnection(ctx context.Context) error {
	repo := a.Profile.Repository
	if repo == "" {
		return errors.New("choose a destination first")
	}
	if !strings.HasPrefix(repo, "rclone:") {
		if _, err := os.Stat(repo); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(repo, 0700); err != nil {
			return err
		}
		f, err := os.CreateTemp(repo, ".setupvault-probe-")
		if err != nil {
			return err
		}
		name := f.Name()
		defer os.Remove(name)
		if _, err = f.WriteString("setupvault-connection-test"); err != nil {
			f.Close()
			return err
		}
		if err = f.Close(); err != nil {
			return err
		}
		if data, err := os.ReadFile(name); err != nil || string(data) != "setupvault-connection-test" {
			return errors.New("local write/read test failed")
		}
	} else {
		remote := strings.TrimPrefix(repo, "rclone:")
		if !strings.Contains(remote, ":") {
			return errors.New("destination must be rclone:remote:folder")
		}
		path := strings.TrimRight(remote, "/") + "/.setupvault-probe-" + randomID()
		var data [32]byte
		if _, err := rand.Read(data[:]); err != nil {
			return err
		}
		if _, err := a.rclone(ctx, data[:], "rcat", path); err != nil {
			return err
		}
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			_, _ = a.rclone(cleanup, nil, "deletefile", path)
		}()
		out, err := a.rclone(ctx, nil, "cat", path)
		if err != nil {
			return err
		}
		if !bytes.Equal(out, data[:]) {
			return errors.New("cloud read-back did not match the uploaded test")
		}
	}
	s := a.State()
	s.LastConnection = time.Now()
	if err := a.saveState(s); err != nil {
		return err
	}
	a.Log("Destination write/read test passed")
	return nil
}
