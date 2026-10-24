package tui

import (
	"io"
	"os"
	"os/exec"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func (m *Model) editProfile() tea.Cmd {
	if err := m.app.Save(); err != nil {
		m.message = err.Error()
		m.failed = true
		return nil
	}
	args := editorArgs()
	args = append(args, m.app.ProfilePath())
	return tea.Exec(&editorProcess{Cmd: exec.Command(args[0], args[1:]...)}, func(err error) tea.Msg { return editorMsg{err} })
}

func editorArgs() []string {
	for _, key := range []string{"VISUAL", "EDITOR"} {
		if args := strings.Fields(os.Getenv(key)); len(args) > 0 {
			return args
		}
	}
	if _, err := exec.LookPath("nano"); err == nil {
		return []string{"nano"}
	}
	return []string{"vi"}
}

type editorProcess struct{ *exec.Cmd }

func (c *editorProcess) SetStdin(r io.Reader)  { c.Stdin = r }
func (c *editorProcess) SetStdout(w io.Writer) { c.Stdout = w }
func (c *editorProcess) SetStderr(w io.Writer) { c.Stderr = w }

func (c *editorProcess) Run() (result error) {
	// A terminal reset can restore enhanced-key defaults; the editor needs legacy Ctrl keys.
	if _, err := io.WriteString(c.Stdout, "\x1b[>4;0m"+ansi.PushKittyKeyboard(0)); err != nil {
		return err
	}
	defer func() {
		_, err := io.WriteString(c.Stdout, ansi.PopKittyKeyboard(1)+ansi.ResetModifyOtherKeys)
		if result == nil {
			result = err
		}
	}()
	return c.Cmd.Run()
}
