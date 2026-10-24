package vault

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func (p RecoveryPackage) SourceID() string {
	sum := sha256.Sum256([]byte(p.URL + "\n" + p.Key))
	return fmt.Sprintf("%s:%s#%x", p.Scope, p.Origin, sum[:])
}

func approvedRecoverySources(packages []RecoveryPackage, requested []string) ([]string, error) {
	result := []string{}
	for _, id := range requested {
		found := false
		for _, p := range packages {
			if p.AddRemote && (id == p.SourceID() || id == p.Scope+":"+p.Origin) {
				result = append(result, p.SourceID())
				found = true
				break
			}
		}
		if !found {
			return nil, errors.New("source approval does not match a reviewed source")
		}
	}
	return result, nil
}

func publicSourceURL(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", true
	}
	private := u.User != nil || u.RawQuery != "" || u.Fragment != ""
	u.User, u.RawQuery, u.Fragment = nil, "", ""
	return u.String(), private
}

func publicRemoteListing(data []byte) []byte {
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		fields := strings.Fields(line)
		for k, field := range fields {
			if strings.HasPrefix(field, "https://") || strings.HasPrefix(field, "oci+https://") {
				fields[k], _ = publicSourceURL(field)
			}
		}
		lines[i] = strings.Join(fields, "\t")
	}
	return []byte(strings.Join(lines, "\n"))
}

func publicKeyIdentity(ctx context.Context, data []byte) (string, error) {
	dir, err := os.MkdirTemp("", "setupvault-public-key-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	key := filepath.Join(dir, "key.gpg")
	if err = os.WriteFile(key, data, 0600); err != nil {
		return "", err
	}
	out, err := commandOutput(ctx, "gpg", "--no-options", "--homedir", dir, "--batch", "--with-colons", "--show-keys", key)
	if err != nil {
		return "", err
	}
	var ids []string
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Split(line, ":")
		if len(fields) > 9 && fields[0] == "fpr" {
			ids = append(ids, fields[9])
		}
	}
	if len(ids) == 0 {
		return "", errors.New("no signing identity in public key")
	}
	sort.Strings(ids)
	return "openpgp:" + strings.Join(ids, ","), nil
}

func legacyRemoteURL(inventory, name, scope string) string {
	data, err := readRecoveryInventory(inventory, "flatpak-remotes.txt", 4<<20)
	if err != nil || len(data) > 4<<20 {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 || fields[0] != name {
			continue
		}
		correctScope := false
		uri := ""
		for _, f := range fields {
			for _, part := range strings.Split(f, ",") {
				if part == scope || part == "default" && scope == "system" {
					correctScope = true
				}
			}
			if strings.HasPrefix(f, "https://") || strings.HasPrefix(f, "oci+https://") {
				if safe, private := publicSourceURL(f); !private {
					uri = safe
				}
			}
		}
		if correctScope {
			return uri
		}
	}
	return ""
}

func validRecoverySource(p RecoveryPackage) error {
	u, err := url.Parse(p.URL)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Host == "" || u.Scheme != "https" && u.Scheme != "oci+https" {
		return errors.New("application source must use HTTPS without embedded credentials")
	}
	if len(p.KeyData) > 1<<20 {
		return errors.New("public signing key is too large")
	}
	if len(p.KeyData) > 0 {
		return nil
	}
	if strings.HasPrefix(p.URL, "oci+https://") {
		return nil
	}
	if p.URL == "https://dl.flathub.org/repo/" || p.URL == "https://flathub.org/repo/" {
		return nil
	}
	return errors.New("original public signing key was not captured")
}

func addRecoveryRemote(ctx context.Context, p RecoveryPackage) error {
	if err := validRecoverySource(p); err != nil {
		return err
	}
	args := append([]string{"remote-add", "--if-not-exists"}, flatpakScope(p.Scope)...)
	location := p.URL
	if len(p.KeyData) > 0 {
		if strings.HasPrefix(p.Key, "openpgp:") {
			id, err := publicKeyIdentity(ctx, p.KeyData)
			if err != nil || id != p.Key {
				return errors.New("public signing identity does not match the reviewed source")
			}
		} else {
			sum := sha256.Sum256(p.KeyData)
			if hex.EncodeToString(sum[:]) != p.Key {
				return errors.New("public signing key does not match the reviewed source")
			}
		}
		dir, err := os.MkdirTemp("", "setupvault-source-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(dir)
		path := filepath.Join(dir, "public.gpg")
		if err = os.WriteFile(path, p.KeyData, 0600); err != nil {
			return err
		}
		args = append(args, "--gpg-import="+path)
	} else if !strings.HasPrefix(p.URL, "oci+") {
		args = append(args, "--from")
		location = "https://dl.flathub.org/repo/flathub.flatpakrepo"
	} else {
		args = append(args, "--authenticator-name=org.flatpak.Authenticator.Oci")
	}
	args = append(args, "--", p.Origin, location)
	if os.Geteuid() == 0 && p.Scope != "user" {
		args = append([]string{"--", "flatpak"}, args...)
		_, err := commandOutput(ctx, "dbus-run-session", args...)
		return err
	}
	_, err := commandOutput(ctx, "flatpak", args...)
	return err
}
