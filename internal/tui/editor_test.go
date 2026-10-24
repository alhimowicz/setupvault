package tui

import (
	"bytes"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestEditorKeyboardModesRestoredOnSuccessAndFailure(t *testing.T) {
	for _, script := range []string{"printf editor-body", "printf editor-body; exit 7"} {
		var output bytes.Buffer
		c := &editorProcess{Cmd: exec.Command("/bin/sh", "-c", script)}
		c.SetStdin(strings.NewReader(""))
		c.SetStdout(&output)
		c.SetStderr(io.Discard)
		err := c.Run()
		if (err != nil) != strings.Contains(script, "exit 7") {
			t.Fatal("editor exit status lost", err)
		}
		expected := "\x1b[>4;0m" + ansi.PushKittyKeyboard(0) + "editor-body" + ansi.PopKittyKeyboard(1) + ansi.ResetModifyOtherKeys
		if output.String() != expected {
			t.Fatalf("keyboard handoff not balanced: %q", output.String())
		}
	}
}

func TestConfiguredEditor(t *testing.T) {
	t.Setenv("VISUAL", "nano --ignorercfiles")
	t.Setenv("EDITOR", "vi")
	args := editorArgs()
	if len(args) != 2 || args[0] != "nano" || args[1] != "--ignorercfiles" {
		t.Fatal("configured editor not respected", args)
	}
}
