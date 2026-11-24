package nodecmd

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// The device entry point dispatches both direction commands to scriptcli. --help exits before it looks for a node.
func TestDirectionCommandsDispatch(t *testing.T) {
	if command := os.Getenv("PERSONAD_TEST_DIRECTION_COMMAND"); command != "" {
		handled, err := Run(context.Background(), func() {}, []string{command, "--help"}, Build{})
		if err != nil || !handled {
			os.Exit(99)
		}
		return
	}
	for _, tc := range []struct {
		command string
		usage   string
	}{
		{command: "create-direction", usage: "usage: personad create-direction <name> --project <slug>"},
		{command: "write-progress", usage: "usage: personad write-progress <direction-id>"},
	} {
		t.Run(tc.command, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestDirectionCommandsDispatch$")
			cmd.Env = append(os.Environ(), "PERSONAD_TEST_DIRECTION_COMMAND="+tc.command)
			var stdout, stderr strings.Builder
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("run: %v; stdout %q; stderr %q", err, stdout.String(), stderr.String())
			}
			if !strings.Contains(stdout.String(), tc.usage) || stderr.Len() != 0 {
				t.Fatalf("stdout %q, stderr %q; want %q on stdout only", stdout.String(), stderr.String(), tc.usage)
			}
		})
	}
}
