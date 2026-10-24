package cli

import (
	"context"
	"errors"
	"fmt"
	"github.com/charmbracelet/x/term"
	"golang.org/x/sys/unix"
	"io"
	"os"
	"strings"
)

func readPassword(ctx context.Context, path string, fromStdin bool, in io.Reader) (string, error) {
	reader := in
	if path != "" && path != "-" {
		f, err := os.OpenFile(path, os.O_RDONLY|unix.O_NONBLOCK, 0)
		if err != nil {
			return "", errors.New("cannot open password file")
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			return "", errors.New("cannot inspect password file")
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return "", errors.New("password file must be a private regular file (chmod 600)")
		}
		reader = f
	} else {
		if !fromStdin && path != "-" {
			return "", errors.New("password input is required")
		}
		if f, ok := in.(*os.File); ok && term.IsTerminal(f.Fd()) {
			return "", errors.New("pipe password input or redirect a private file; terminal input is disabled")
		}
	}
	type inputResult struct {
		data []byte
		err  error
	}
	done := make(chan inputResult, 1)
	go func() { data, err := io.ReadAll(io.LimitReader(reader, 4099)); done <- inputResult{data, err} }()
	var data []byte
	var err error
	select {
	case result := <-done:
		data, err = result.data, result.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if err != nil {
		return "", errors.New("cannot read password input")
	}
	if len(data) > 4098 {
		return "", errors.New("password input is too long")
	}
	password := strings.TrimSuffix(string(data), "\n")
	if strings.HasSuffix(string(data), "\r\n") {
		password = strings.TrimSuffix(password, "\r")
	}
	return password, nil
}

func terminalPassword() (string, error) {
	f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", err
	}
	defer f.Close()
	fmt.Fprint(f, "Recovery password: ")
	p, err := term.ReadPassword(f.Fd())
	fmt.Fprintln(f)
	return string(p), err
}
