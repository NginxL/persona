//go:build !windows

package node

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/osproc"
)

const externalCancelNative = `#!/bin/sh
printf '%s\n' "$*" >> "$CLI_FAKE_DIR/external-argv"
echo '{"type":"system","subtype":"init","model":"example-model"}'
while IFS= read -r line; do
  printf '%s\n' "$line" >> "$CLI_FAKE_DIR/external-stdin"
  case "$line" in
    *'"type":"control_request"'*)
      case "$line" in
        *'"subtype":"initialize"'*)
          printf 'initializing\n' > "$CLI_FAKE_DIR/initializing"
          while [ -f "$CLI_FAKE_DIR/init-hold" ]; do /bin/sleep 0.02; done ;;
      esac
      id=$(printf '%s' "$line" | sed -n 's/.*"request_id":"\([^"]*\)".*/\1/p')
      printf '{"type":"control_response","response":{"subtype":"success","request_id":"%s"}}\n' "$id" ;;
    *'"type":"user"'*) echo '{"type":"result","subtype":"success","model":"example-model","result":"hello"}' ;;
  esac
done
`

func TestExternalCancelInterruptsInventoryAndInitializeBeforeInput(t *testing.T) {
	for _, phase := range []string{"inventory", "initialize"} {
		t.Run(phase, func(t *testing.T) {
			f, launcher, execution, _ := externalAssistantFixture(t)
			id := execution.External.SessionID
			cwd := t.TempDir()
			f.n.state.put(id, sessionState{Launcher: launcher, Cwd: cwd, IsMain: true})
			writeFile(t, filepath.Join(f.dir, "example-native"), externalCancelNative)
			hold, marker := "init-hold", "initializing"
			if phase == "inventory" {
				hold, marker = "inventory-hold", "inventory-waiting"
				body := strings.Replace(externalTestAdapter, "inventory) exec", `inventory)
                  printf 'inventory\n' > "$CLI_FAKE_DIR/inventory-waiting"
                  while [ -f "$CLI_FAKE_DIR/inventory-hold" ]; do /bin/sleep 0.02; done
                  exec`, 1)
				writeFile(t, filepath.Join(f.dir, "example-adapter"), body)
			}
			writeFile(t, filepath.Join(f.dir, hold), "1")
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			defer os.Remove(filepath.Join(f.dir, hold))
			params, _ := json.Marshal(externalMergeParams(launcher, execution, "canceled message must never be replayed"))
			sent := make(chan error, 1)
			go func() {
				_, err := f.n.handle(ctx, "assistant.send", params)
				sent <- err
			}()
			cliWait(t, "blocked "+phase, func() bool { return f.file(marker) != "" })
			stop := make(chan error, 1)
			stopParams, _ := json.Marshal(map[string]any{"session_id": id})
			go func() {
				_, err := f.n.handle(t.Context(), "session.interrupt", stopParams)
				stop <- err
			}()
			select {
			case err := <-stop:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Stop waited behind a forced inventory or initialization")
			}
			select {
			case err := <-sent:
				if err == nil {
					t.Fatal("a canceled initialization reported successful delivery")
				}
			case <-time.After(2 * time.Second):
				t.Fatal("the canceled send remained pending after Stop returned")
			}
			if p := f.proc(id); p != nil && p.alive() {
				t.Fatal("Stop left the uninitialized native process running")
			}
			if phase == "inventory" && f.file("external-argv") != "" {
				t.Fatal("Stop during inventory still started a native process")
			}
			if strings.Contains(f.file("external-stdin"), `"type":"user"`) {
				t.Fatal("Stop delivered user input before initialization was authorized")
			}
			if output := strings.TrimSpace(f.file("external-settings-path")); output != "" {
				cliWait(t, "private settings cleanup", func() bool {
					_, err := os.Stat(output)
					return os.IsNotExist(err)
				})
			}
			if f.n.externalStarts.has(id) {
				t.Fatal("an ended send retained its startup cancellation token")
			}

			// A later user operation is a new generation. It may start after the Stop, and its RPC ending must not
			// kill the established conversation or replay the canceled operation.
			if err := os.Remove(filepath.Join(f.dir, hold)); err != nil {
				t.Fatal(err)
			}
			next, endNext := context.WithCancel(t.Context())
			nextParams, _ := json.Marshal(externalMergeParams(launcher, execution, "new user operation after Stop"))
			if _, err := f.n.handle(next, "assistant.send", nextParams); err != nil {
				endNext()
				t.Fatal(err)
			}
			endNext()
			f.idle(t, id)
			p := f.proc(id)
			if p == nil || !p.alive() || p.model.StartupContext != nil {
				t.Fatal("an established conversation retained or was killed by its completed startup context")
			}
			input := f.file("external-stdin")
			if strings.Contains(input, "canceled message must never be replayed") || strings.Count(input, "new user operation after Stop") != 1 {
				t.Fatal("the canceled startup retried or replayed user input")
			}
		})
	}
}

func TestExternalCancelColdCompactionInitialization(t *testing.T) {
	f, launcher, execution, _ := externalAssistantFixture(t)
	id := execution.External.SessionID
	f.n.state.put(id, sessionState{Launcher: launcher, Cwd: t.TempDir(), IsMain: true, ConfigDir: t.TempDir()})
	controlTranscript(t, f, id)
	externalMergeModelReply(f, launcher, execution)
	writeFile(t, filepath.Join(f.dir, "example-native"), externalCancelNative)
	writeFile(t, filepath.Join(f.dir, "init-hold"), "1")
	defer os.Remove(filepath.Join(f.dir, "init-hold"))
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	compacted := make(chan error, 1)
	go func() { compacted <- f.n.assistantCompact(ctx, id, false) }()
	cliWait(t, "cold compaction initialization", func() bool { return f.file("initializing") != "" })
	stopped := make(chan error, 1)
	go func() { stopped <- f.n.assistantInterrupt(id) }()
	select {
	case err := <-stopped:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not cancel a cold compaction's initialization")
	}
	select {
	case err := <-compacted:
		if err == nil {
			t.Fatal("an uninitialized cold compaction was accepted")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the canceled cold compaction remained pending")
	}
	if strings.Contains(f.file("external-stdin"), "/compact") || f.n.externalStarts.has(id) {
		t.Fatal("the canceled cold compaction delivered input or retained its startup token")
	}
	if err := os.Remove(filepath.Join(f.dir, "init-hold")); err != nil {
		t.Fatal(err)
	}
	if err := f.n.assistantCompact(t.Context(), id, false); err != nil {
		t.Fatalf("a later compaction inherited the canceled startup: %v", err)
	}
	cliWait(t, "the later compaction", func() bool { return strings.Contains(f.file("external-stdin"), "/compact") })
	if strings.Count(f.file("external-stdin"), "/compact") != 1 || f.proc(id).model.StartupContext != nil {
		t.Fatal("a canceled compaction replayed or persisted its startup context")
	}
}

func TestExternalCancelPendingGenerationsAreIndependent(t *testing.T) {
	n := &Node{}
	const id = "11111111-2222-4333-8444-555555555555"
	first, finishFirst := n.beginExternalStartup(t.Context(), id)
	defer finishFirst()
	queued, finishQueued := n.beginExternalStartup(t.Context(), id)
	defer finishQueued()
	n.cancelExternalStartups(id)
	if first.Err() == nil || queued.Err() == nil {
		t.Fatal("Stop did not cancel every already-pending send for the session")
	}
	later, finishLater := n.beginExternalStartup(t.Context(), id)
	defer finishLater()
	finishFirst()
	finishQueued()
	if later.Err() != nil || !n.externalStarts.has(id) {
		t.Fatal("an old cancellation or deferred cleanup canceled/deleted the new operation")
	}
	if err := finishExternalStartup(later); err != nil {
		t.Fatal(err)
	}
	finishLater()
	if n.externalStarts.has(id) {
		t.Fatal("completed startup tokens were not released")
	}
}

func TestExternalCancelStartFailureNeverLeaksLocalCommand(t *testing.T) {
	for _, failure := range []string{"deleted-after-validation", "argument-list-too-long"} {
		t.Run(failure, func(t *testing.T) {
			f, launcher, execution, _ := externalAssistantFixture(t)
			cmd, cleanup, err := f.n.externalCLICommand(t.Context(), execution.External.SessionID, launcher, t.TempDir(), execution, nil, os.Environ())
			if err != nil {
				t.Fatal(err)
			}
			defer cleanup()
			const marker = "synthetic-private-adapter-path"
			private := filepath.Join(t.TempDir(), marker)
			if failure == "deleted-after-validation" {
				writeFile(t, private, "#!/bin/sh\nexit 0\n")
				if err := os.Chmod(private, 0o700); err != nil {
					t.Fatal(err)
				}
				cmd.Path = private
				cmd.Args[0] = private
				if err := os.Remove(private); err != nil {
					t.Fatal(err)
				}
			} else {
				cmd.Args = append(cmd.Args, strings.Repeat(marker, 1<<16))
			}
			err = startExternalCommand(t.Context(), cmd, nil)
			requireExternalCode(t, err, "adapter_unavailable")
			response, _ := json.Marshal(err)
			for _, secret := range []string{marker, private, f.n.external.config.Command[0]} {
				if strings.Contains(err.Error(), secret) || strings.Contains(string(response), secret) || strings.Contains(f.log.String(), secret) {
					t.Fatal("a native start failure exposed a local command, argument or path")
				}
			}
			if cmd.Process != nil {
				t.Fatal("a pre-execution failure started an adapter process")
			}
			externalInventoryNoNative(t, f)
		})
	}
}

// This fixture establishes a lifecycle mechanism, not behavior observed from a real Claude Code process: after
// acknowledging initialize, the leader leaves a TERM-ignoring child in its original process group and exits first.
func TestExternalCancelLeaderExitSweepsGroupBeforeSettingsCleanup(t *testing.T) {
	f, launcher, execution, _ := externalAssistantFixture(t)
	body := strings.Replace(externalTestNative, "#!/bin/sh\n", "#!/bin/sh\ntrap 'exit 0' TERM\n", 1)
	body = strings.Replace(body, `printf '{"type":"control_response","response":{"subtype":"success","request_id":"%s"}}\n' "$id" ;;`,
		`printf '{"type":"control_response","response":{"subtype":"success","request_id":"%s"}}\n' "$id"
          case "$line" in
            *'"subtype":"initialize"'*)
              /bin/sh -c 'trap "" TERM; printf "%s\n" "$$" > "$CLI_FAKE_DIR/term-child-ready"; exec /bin/sleep 20' </dev/null >/dev/null 2>&1 &
              while [ ! -f "$CLI_FAKE_DIR/term-child-ready" ]; do /bin/sleep 0.01; done ;;
          esac ;;`, 1)
	writeFile(t, filepath.Join(f.dir, "example-native"), body)
	p := externalMergeStart(t, f, launcher, t.TempDir(), execution)
	pid := p.cmd.Process.Pid
	defer osproc.SweepGroup(pid, time.Second)
	cliWait(t, "the TERM-ignoring child", func() bool { return f.file("term-child-ready") != "" })
	child, err := strconv.Atoi(strings.TrimSpace(f.file("term-child-ready")))
	if err != nil || child <= 0 {
		t.Fatal("the native fixture did not report its child")
	}
	if group, err := syscall.Getpgid(child); err != nil || group != pid || !osproc.Alive(child) {
		t.Fatalf("the ready child does not belong to the native process group: pgid=%d, err=%v", group, err)
	}
	output := strings.TrimSpace(f.file("external-settings-path"))
	if _, err := os.Stat(output); err != nil {
		t.Fatal("the native fixture has no private settings output")
	}
	if err := f.n.assistantInterrupt(p.id); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.exited:
	case <-time.After(2 * time.Second):
		t.Fatal("the native waiter did not finish within its group cleanup budget")
	}
	if osproc.Alive(child) {
		t.Fatal("the leader's Wait published exit/settings cleanup while its TERM-ignoring child still lived")
	}
	if err := syscall.Kill(-pid, 0); err != syscall.ESRCH {
		t.Fatalf("the native fixture's group is not empty after waiter cleanup: %v", err)
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatal("private settings were not removed after the group ended")
	}
}

func TestExternalCancelCompletedStartupDoesNotOwnProcessLifetime(t *testing.T) {
	f, launcher, execution, _ := externalAssistantFixture(t)
	ctx, finish := f.n.beginExternalStartup(t.Context(), execution.External.SessionID)
	defer finish()
	cmd := exec.Command("/bin/sh", "-c", "/bin/sleep 0.1")
	if err := startExternalCommand(ctx, cmd, nil); err != nil {
		t.Fatal(err)
	}
	if err := finishExternalStartup(ctx); err != nil {
		t.Fatal(err)
	}
	finish()
	if err := cmd.Wait(); err != nil {
		t.Fatal("a successfully detached process was killed when the startup context ended")
	}
	if f.n.executionFingerprint(launcher, execution) != f.n.executionFingerprint(launcher, modelExecution{
		Model: execution.Model, Fingerprint: execution.Fingerprint, IsMain: execution.IsMain,
		External: execution.External, StartupContext: ctx,
	}) {
		t.Fatal("a one-shot startup context changed the effective execution fingerprint")
	}
}
