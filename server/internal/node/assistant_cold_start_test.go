package node

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A real interactive shell takes twelve seconds before it can invoke the CLI.
// The CLI then takes four seconds to ACK: the old deadline killed it at 15s.
func TestAssistantColdStartSlowShell(t *testing.T) {
	f := cliTest(t)
	writeFile(t, filepath.Join(os.Getenv("HOME"), ".zshrc"), `if [[ "$ZSH_EXECUTION_STRING" = *'__PERSONA_ASSISTANT_SHELL_READY__'* ]]; then echo 'shell plugin initialization' >&2; sleep 12; fi`+"\n")
	script := strings.Replace(fakeCLI, "while IFS= read -r line; do", "sleep 4\nwhile IFS= read -r line; do", 1)
	writeFile(t, filepath.Join(os.Getenv("HOME"), "bin", "claude"), "#!/bin/sh\n"+script)
	if err := os.Chmod(filepath.Join(os.Getenv("HOME"), "bin", "claude"), 0o700); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	start := time.Now()
	id, err := f.n.assistantSend(ctx, "", t.TempDir(), "claude", "hello", nil, "acceptEdits", "high", cliExec())
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 16*time.Second {
		t.Fatal("slow shell/ACK fixture did not delay startup")
	}
	f.idle(t, id)
	controlTranscript(t, f, id)
	checkControlState(t, f, id, "acceptEdits", "high")
	p := f.proc(id)
	p.mu.Lock()
	shellMS := p.shellReadyAt.Sub(p.born).Milliseconds()
	p.mu.Unlock()
	t.Logf("shell initialization elapsed_ms=%d; cold send elapsed_ms=%d; user inputs=1", shellMS, time.Since(start).Milliseconds())
	if shellMS < 12000 {
		t.Fatalf("shell fixture did not delay 12 seconds: %dms", shellMS)
	}
	if got := strings.Count(f.file("stdin.jsonl"), `"type":"user"`); got != 1 {
		t.Fatalf("user inputs = %d", got)
	}
	if !strings.Contains(f.log.String(), "shell initialization completed elapsed_ms=") {
		t.Fatal("shell startup duration missing")
	}
}

func shortControlWaits(t *testing.T) {
	t.Helper()
	control, startup := assistantControlTimeout, assistantStartupTimeout
	assistantControlTimeout, assistantStartupTimeout = time.Second, 2*time.Second
	t.Cleanup(func() { assistantControlTimeout, assistantStartupTimeout = control, startup })
	shortStartRetries(t, time.Millisecond)
}

func TestAssistantColdStartControlTimeoutRetries(t *testing.T) {
	for _, text := range []string{"hello", ""} {
		t.Run(text, func(t *testing.T) { testAssistantColdStartControlTimeoutRetries(t, text) })
	}
}

func testAssistantColdStartControlTimeoutRetries(t *testing.T, text string) {
	f := cliTest(t)
	shortControlWaits(t)
	prefix := `if [ "$PERSONA_ASSISTANT" = 1 ]; then
 echo start >> "$CLI_FAKE_DIR/starts"
if [ ! -e "$CLI_FAKE_DIR/retried" ]; then
 : > "$CLI_FAKE_DIR/retried"
 echo '{"type":"system","subtype":"init"}'
 read line
 printf '%s\n' "$line" >> "$CLI_FAKE_DIR/stdin.jsonl"
 sleep 30
 exit 0
fi
fi
`
	writeFile(t, filepath.Join(os.Getenv("HOME"), "bin", "claude"), "#!/bin/sh\n"+prefix+fakeCLI)
	id, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "claude", text, nil, "acceptEdits", "high", cliExec())
	if err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	controlTranscript(t, f, id)
	checkControlState(t, f, id, "acceptEdits", "high")
	if got := strings.Count(f.file("starts"), "start"); got != 2 {
		t.Fatalf("attempts = %d, want 2", got)
	}
	wantInputs := 0
	if text != "" {
		wantInputs = 1
	}
	if got := strings.Count(f.file("stdin.jsonl"), `"type":"user"`); got != wantInputs {
		t.Fatalf("user inputs = %d, want %d", got, wantInputs)
	}
	log := f.log.String()
	for _, want := range []string{"control_timeout stage=control_response_timeout wait_ms=", "stopping process from node", "starting it again"} {
		if !strings.Contains(log, want) {
			t.Fatalf("log missing %q: %s", want, log)
		}
	}
	if strings.Contains(log, "exited ") {
		t.Fatalf("node termination reported as a spontaneous exit: %s", log)
	}
}

func TestAssistantColdStartTimeoutStages(t *testing.T) {
	for _, stage := range []string{"shell_start_timeout", "assistant_ready_timeout", "control_response_timeout"} {
		t.Run(stage, func(t *testing.T) {
			f := cliTest(t)
			shortControlWaits(t)
			assistantStartRetries = 1
			script := "if [ \"$PERSONA_ASSISTANT\" = 1 ]; then echo start >> \"$CLI_FAKE_DIR/starts\"; fi\nread line\nsleep 30\n"
			if stage == "shell_start_timeout" {
				writeFile(t, filepath.Join(os.Getenv("HOME"), ".zshrc"), `if [[ "$ZSH_EXECUTION_STRING" = *'__PERSONA_ASSISTANT_SHELL_READY__'* ]]; then echo shell >> "$CLI_FAKE_DIR/shell-starts"; sleep 30; fi`+"\n")
			}
			if stage == "control_response_timeout" {
				script = "echo '{\"type\":\"system\",\"subtype\":\"init\"}'\n" + script
			}
			writeFile(t, filepath.Join(os.Getenv("HOME"), "bin", "claude"), "#!/bin/sh\n"+script)
			_, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "claude", "hello", nil, "acceptEdits", "high", cliExec())
			var re *rpc.Error
			if !errors.As(err, &re) || re.Code != "assistant_control_failed" {
				t.Fatalf("error = %v", err)
			}
			for _, want := range []string{"2 次", "共等", "超时", "消息没有送达"} {
				if !strings.Contains(re.Message, want) {
					t.Fatalf("error missing %q: %s", want, re.Message)
				}
			}
			if strings.Contains(re.Message, "后退出") {
				t.Fatalf("wrong cause: %s", re.Message)
			}
			if log := f.log.String(); !strings.Contains(log, "control_timeout stage="+stage+" wait_ms=") {
				t.Fatalf("wrong stage: %s", log)
			}
			counter := "starts"
			if stage == "shell_start_timeout" {
				counter = "shell-starts"
			}
			if got := len(strings.Fields(f.file(counter))); got != 2 {
				t.Fatalf("attempts = %d", got)
			}
			if strings.Contains(f.file("stdin.jsonl"), `"type":"user"`) {
				t.Fatal("input delivered despite failed preparation")
			}
		})
	}
}

// Virtual time covers a CLI that has no startup output until it handles a
// control, the readiness transition, and the ordinary warm-control deadline.
func TestAssistantColdStartControlBudgets(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &assistantProc{stdin: discardControlWriter{}, exited: make(chan struct{}), ready: make(chan struct{}), born: time.Now()}
		done := make(chan error, 1)
		go func() { done <- p.control(context.Background(), map[string]any{"subtype": "set_permission_mode"}) }()
		synctest.Wait()
		time.Sleep(20 * time.Second) // sleep-ok: advances only the synctest virtual clock
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("startup used normal deadline: %v", err)
		default:
		}
		p.started()
		synctest.Wait()
		time.Sleep(14 * time.Second) // sleep-ok: advances only the synctest virtual clock
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("readiness did not reset deadline: %v", err)
		default:
		}
		p.controlResponse(assistantControlResponse{RequestID: "persona-1", Subtype: "success"})
		synctest.Wait()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
		began := time.Now()
		err := p.control(context.Background(), map[string]any{"subtype": "set_permission_mode"})
		if !errors.Is(err, context.DeadlineExceeded) || time.Since(began) != assistantControlTimeout {
			t.Fatalf("warm deadline = %v after %v", err, time.Since(began))
		}
	})
}

type discardControlWriter struct{}

func (discardControlWriter) Write(b []byte) (int, error) { return len(b), nil }
func (discardControlWriter) Close() error                { return nil }

func TestAssistantColdStartCancellationDoesNotRetry(t *testing.T) {
	f := cliTest(t)
	controlFile(t, f, "control-hold", "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	cwd := t.TempDir()
	go func() {
		_, err := f.n.assistantSend(ctx, "", cwd, "claude", "hello", nil, "acceptEdits", "high", cliExec())
		done <- err
	}()
	cliWait(t, "startup control waiting", func() bool { return f.file("control-waiting") != "" })
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled start did not finish")
	}
	if strings.Contains(f.log.String(), "starting it again") || strings.Contains(f.file("stdin.jsonl"), `"type":"user"`) {
		t.Fatal("canceled start retried or delivered input")
	}
}

func TestAssistantColdStartDormantSpareBudget(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &assistantProc{stdin: discardControlWriter{}, exited: make(chan struct{}), ready: make(chan struct{}), born: time.Now().Add(-2 * time.Minute)}
		done := make(chan error, 1)
		go func() { done <- p.control(context.Background(), map[string]any{"subtype": "set_permission_mode"}) }()
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatalf("dormant spare given no response budget: %v", err)
		default:
		}
		p.controlResponse(assistantControlResponse{RequestID: "persona-1", Subtype: "success"})
		synctest.Wait()
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	})
}

func TestAssistantColdStartCallerDeadlineKeepsCause(t *testing.T) {
	f := cliTest(t)
	controlFile(t, f, "control-hold", "")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, err := f.n.assistantSend(ctx, "", t.TempDir(), "claude", "hello", nil, "acceptEdits", "high", cliExec())
	var re *rpc.Error
	if !errors.Is(err, context.DeadlineExceeded) || !errors.As(err, &re) {
		t.Fatalf("deadline error = %v", err)
	}
	for _, want := range []string{"停止了助手", "确认设置请求超时", "已等"} {
		if !strings.Contains(re.Message, want) {
			t.Fatalf("deadline error missing %q: %s", want, re.Message)
		}
	}
	if strings.Contains(f.log.String(), "starting it again") || strings.Contains(f.file("stdin.jsonl"), `"type":"user"`) {
		t.Fatal("caller deadline retried or delivered input")
	}
}

func TestAssistantColdStartRoundFallbackKeepsRequestedSettings(t *testing.T) {
	n, argv, _ := roundTestNode(t, roundStarted("2.1.322", 1), false)
	shortControlWaits(t)
	n.noteModes(roundConversation, "acceptEdits", "high")
	n.persistModes(roundConversation, "acceptEdits", "high")
	dir := t.TempDir()
	t.Setenv("CLI_FAKE_DIR", dir)
	script := "printf '%s\\n' \"$@\" >> " + shellQuote(argv) + "\necho ---- >> " + shellQuote(argv) + "\n" + `case " $* " in
 *' --resume-session-at '*)
  echo '{"type":"system","subtype":"init"}'
  read line
  sleep 30
  exit 0 ;;
esac
` + fakeCLI
	writeFile(t, filepath.Join(os.Getenv("HOME"), "bin", "claude"), "#!/bin/sh\n"+script)
	out := sendRound(t, n, "timeout-round")
	if out["context"] != roundKept || out["why"] != roundWhyStartFailed {
		t.Fatalf("round fallback = %v", out)
	}
	starts := roundStarts(t, argv)
	if len(starts) != 2 || !hasArg(starts[0], "--resume-session-at") || hasArg(starts[1], "--resume-session-at") {
		t.Fatalf("starts = %q", starts)
	}
	p := idleRound(n)
	p.mu.Lock()
	mode, effort := p.mode, p.effort
	p.mu.Unlock()
	if mode != "acceptEdits" || effort != "high" {
		t.Fatalf("fallback changed settings: %q %q", mode, effort)
	}
	waitFor(t, "the fallback's single user input", func() bool {
		raw, _ := os.ReadFile(filepath.Join(dir, "stdin.jsonl"))
		return strings.Count(string(raw), `"type":"user"`) == 1
	})
	st, _ := n.state.get(roundConversation)
	if st.AppMode != "acceptEdits" || st.AppEffort != "high" {
		t.Fatalf("fallback saved settings: %q %q", st.AppMode, st.AppEffort)
	}
}

// External adapters invoke their CLI directly, without interactive-shell init.
func TestAssistantColdStartDirectAdapterReportsAssistantStage(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		p := &assistantProc{stdin: discardControlWriter{}, exited: make(chan struct{}), ready: make(chan struct{}), born: time.Now(), launcher: proto.ExternalLauncherName("adapter", "target")}
		err := p.control(context.Background(), map[string]any{"subtype": "set_permission_mode"})
		var timeout *assistantControlTimeoutError
		if !errors.As(err, &timeout) || timeout.stage != "assistant_ready_timeout" {
			t.Fatalf("direct adapter timeout = %v", err)
		}
	})
}
