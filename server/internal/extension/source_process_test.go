package extension

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/extension/sandbox"
	"github.com/nwverse/persona/server/internal/extension/sourcevalidate"
	"github.com/nwverse/persona/server/internal/testhome"
)

func TestMain(m *testing.M) {
	// The market tests make and clone repositories of their own: none of that may reach the repository this run was
	// started from (`git bisect run` exports GIT_DIR; #13610).
	testhome.ClearGit()
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case sandbox.ValidationRunCommand:
			os.Exit(sandbox.RunValidation(os.Args[2:]))
		case sandbox.RunCommand:
			os.Exit(sandbox.Run(os.Args[2:], os.Stderr))
		case "snapshot-test-read":
			b, err := os.ReadFile("/extension/bundle.mjs")
			if err != nil || string(b) != "export default 1" {
				os.Exit(1)
			}
			for fd := 3; fd < 64; fd++ {
				target, _ := os.Readlink(fmt.Sprintf("/proc/self/fd/%d", fd))
				if strings.Contains(target, "persona-source-artifact") {
					os.Exit(1)
				}
			}
			os.Exit(0)
		case sourcevalidate.Command:
			os.Exit(sourcevalidate.Run(os.Args[2:], os.Stdin, os.Stdout))
		}
	}
	os.Exit(m.Run())
}

func TestSourceValidationCancellationAndPrivateErrors(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := CheckSelfContainedContext(ctx, []byte("export default 1")); !errors.Is(err, sourcevalidate.ErrStopped) {
		t.Fatal(err)
	}
	privateRoot := t.TempDir()
	_, err := CheckSourceBundle(privateRoot, &SourceRuntime{Bundle: "missing.mjs", SHA256: strings.Repeat("0", 64)})
	if err == nil || strings.Contains(err.Error(), privateRoot) || strings.Contains(err.Error(), "missing.mjs") {
		t.Fatal("source error disclosed filesystem information", err)
	}
}
