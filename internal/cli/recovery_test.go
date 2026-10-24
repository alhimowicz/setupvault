package cli

import (
	"context"
	"testing"
)

func TestRecoveryFlagValidationAndMissingConnector(t *testing.T) {
	_, base := fixture(t)
	for _, args := range [][]string{{"--apply-recovery"}, {"--resume-recovery"}, {"--plan-recovery"}, {"--recovery-status", "--wait"}, {"--cancel-recovery"}} {
		code, result, _ := invoke(t, context.Background(), command(base, args...), "")
		if code != 2 || result["ok"] != false {
			t.Fatal("invalid recovery flags accepted", args, result)
		}
	}
	t.Setenv("PATH", t.TempDir())
	code, result, _ := invoke(t, context.Background(), command(base, "--connections"), "")
	if code != 3 || result["error"].(map[string]any)["code"] != "dependency_missing" {
		t.Fatal("missing rclone was not clearly reported", result)
	}
}
