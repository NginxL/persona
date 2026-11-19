package node

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/aiwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func runModel(t *testing.T, p proto.ModelRunParams) (any, error) {
	t.Helper()
	home := os.Getenv("HOME")
	n := &Node{home: home, dir: filepath.Join(home, ".persona"), logger: log.New(io.Discard, "", 0)}
	raw, _ := json.Marshal(p)
	return n.modelRun(context.Background(), raw)
}

func runCode(err error) string {
	var re *rpc.Error
	if errors.As(err, &re) {
		return re.Code
	}
	return ""
}

// model.run runs claude once, headless, with no tools, MCP servers, hooks or session, the system prompt from a file and
// the conversation on stdin, and answers with its result; a CLI that is not logged in and a run past its time say so
// with their codes, and a word that is no launcher never runs.
func TestModelRunAnswersWithAHeadlessClaude(t *testing.T) {
	out := t.TempDir()
	fakeClaudeHome(t, `printf '%s\n' "$@" > `+shellQuote(filepath.Join(out, "args"))+`
cat > `+shellQuote(filepath.Join(out, "stdin"))+`
pwd > `+shellQuote(filepath.Join(out, "cwd"))+`
while [ $# -gt 0 ]; do
  [ "$1" = --system-prompt-file ] && cat "$2" > `+shellQuote(filepath.Join(out, "system"))+`
  [ "$1" = --settings ] && cat "$2" > `+shellQuote(filepath.Join(out, "settings"))+`
  shift
done
printf '{"type":"result","subtype":"success","is_error":false,"result":"pong","modelUsage":{"m-1":{}},"usage":{"input_tokens":14,"output_tokens":7,"cache_read_input_tokens":95,"cache_creation_input_tokens":83}}\n'
`)
	got, err := runModel(t, proto.ModelRunParams{Launcher: "claude", Model: "m-1", Effort: "high", System: "be brief", Prompt: "ping"})
	usage := aiwire.Usage{InputTokens: 14, OutputTokens: 7, CacheReadInputTokens: 95, CacheCreationInputTokens: 83}
	if a, ok := got.(proto.ModelRunAnswer); err != nil || !ok || a.Text != "pong" || a.Model != "m-1" || a.Usage != usage {
		t.Fatalf("answer: %#v %v", got, err)
	}
	read := func(name string) string {
		b, _ := os.ReadFile(filepath.Join(out, name))
		return strings.TrimSpace(string(b))
	}
	args := "\n" + read("args") + "\n"
	for _, want := range []string{"\n-p\n", "\n--no-session-persistence\n", "\n--tools\n\n", "\n--strict-mcp-config\n", "\n--max-turns\n1\n", "\n--model\nm-1\n", "\n--effort\nhigh\n"} {
		if !strings.Contains(args, want) {
			t.Errorf("args lack %q: %q", want, args)
		}
	}
	if read("stdin") != "ping" || read("system") != "be brief" || !strings.Contains(read("settings"), `"disableAllHooks":true`) {
		t.Errorf("stdin %q, system %q, settings %q", read("stdin"), read("system"), read("settings"))
	}
	if cwd := read("cwd"); !strings.Contains(cwd, launcherCheckDir) {
		t.Errorf("ran in %q", cwd)
	} else if _, err := os.Stat(cwd); !os.IsNotExist(err) {
		t.Errorf("its directory was left behind: %v", err)
	}

	fakeClaudeHome(t, `printf '{"type":"result","subtype":"success","is_error":true,"result":"Not logged in · Please run /login"}\n'`)
	if _, err := runModel(t, proto.ModelRunParams{Launcher: "claude", Prompt: "ping"}); runCode(err) != proto.CodeModelRunLoggedOut {
		t.Fatalf("logged out: %v", err)
	}

	fakeClaudeHome(t, "sleep 5\n")
	if _, err := runModel(t, proto.ModelRunParams{Launcher: "claude", Prompt: "ping", TimeoutMS: 300}); runCode(err) != proto.CodeModelRunTimeout {
		t.Fatalf("timeout: %v", err)
	}

	if _, err := runModel(t, proto.ModelRunParams{Launcher: "reboot", Prompt: "ping"}); err == nil || runCode(err) != "" {
		t.Fatalf("not a launcher: %v", err)
	}
}

// A run waits for one of the computer's slots within its own time: with every slot taken it times out unrun.
func TestModelRunWaitsForASlot(t *testing.T) {
	ran := filepath.Join(t.TempDir(), "ran")
	fakeClaudeHome(t, "touch "+shellQuote(ran)+"\n")
	for range cap(modelRunSlots) {
		modelRunSlots <- struct{}{}
	}
	defer func() {
		for range cap(modelRunSlots) {
			<-modelRunSlots
		}
	}()
	if _, err := runModel(t, proto.ModelRunParams{Launcher: "claude", Prompt: "ping", TimeoutMS: 200}); runCode(err) != proto.CodeModelRunTimeout {
		t.Fatalf("busy: %v", err)
	}
	if _, err := os.Stat(ran); !os.IsNotExist(err) {
		t.Fatal("ran without a slot")
	}
}
