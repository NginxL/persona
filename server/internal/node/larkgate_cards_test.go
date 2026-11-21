package node

import (
	"os"
	"path/filepath"
	"testing"
)

// Commands the assistant ran that asked on the phone although they only read Feishu or did not call lark-cli at all
// (#8119), and the writes that must still ask. Only the classifier and the approval adapters see these strings;
// nothing runs them. The helper's lark-cli knows `docs +fetch` (read) and `docs +create` (write) and says it has no
// `im messages get` (#10845); anything else lark-cli gives no risk for, which counts as a write. The conversation is in bypass mode, where a classified
// ordinary write runs (larkBypassSafe) and sending a message still asks.
func TestLarkCardsOnlyForWrites(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, "reports"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, command string
		cards         int
	}{
		// Text that only names lark-cli: a note written to a file, a pattern, a process list.
		{"heredoc note in home", "cat >> ~/reports/x.md <<'EOF'\n- lark-cli im +messages-send: no app configured\nEOF", 0},
		{"heredoc after cd", "cd ~/reports && cat >> notes.md <<'EOF'\nlark-cli api POST /open-apis/im/v1/messages\nEOF", 0},
		{"heredoc after cd ;", "cd ~/reports; cat > notes.md <<EOF\nlark-cli im +messages-send\nEOF", 0},
		{"perl substitution", `perl -0pi -e 's/(- old\n)/$1- lark-cli has no app configured\n/' ~/reports/memory.md`, 0},
		{"grep pattern", `grep -n 'lark-cli' script.py | head -8`, 0},
		{"grep -v in a process list", `ps aux | grep -v lark-helper | grep -v lark-cli | head -3`, 0},
		{"app and process check", `ls -d /Applications/Lark.app 2>/dev/null; pgrep -fl -i "lark|feishu" | grep -v -i "lark-helper\|lark-cli" | head -3`, 0},
		{"log grep then a program", `git log --oneline -40 | grep -iE "empty lark-cli"; gh pr list --limit 5`, 0},

		// Reads with a prefix or in a compound command.
		{"cd then absolute read into python", "cd /tmp; /opt/homebrew/bin/lark-cli api GET /open-apis/im/v1/messages --params '{\"container_id\":\"oc_x\"}' 2>/dev/null | python3 -c \"import json,sys\nfor m in json.load(sys.stdin)['data']['items']:\n  if m['msg_type']=='system': continue\n  print(m['body']['content'][:120])\"", 0},
		{"env -i with system PATH", `env -i HOME=$HOME PATH=/usr/bin:/bin:/opt/homebrew/bin lark-cli api GET /open-apis/im/v1/messages --params '{}'`, 0},
		{"assignment then env -i", `CHAT=oc_x; env -i HOME=$HOME PATH=/usr/bin:/bin lark-cli api GET /open-apis/im/v1/messages --params "{\"container_id\":\"$CHAT\"}"`, 0},
		// A PATH that adds only a login shell directory (the helper's /opt/login/bin, where an npm-installed lark-cli
		// and its node live) changes nothing a command can run: reads stay reads and a send still asks (#12483).
		{"login shell PATH added again", `export PATH="/opt/login/bin:$PATH"; lark-cli docs +fetch --doc-id a 2>&1 | head -5`, 0},
		{"login shell PATH again for a send", `export PATH="/opt/login/bin:$PATH"; lark-cli im +messages-send --chat-id oc_x --text hi`, 1},
		{"literal loop of reads", `for t in omt_a omt_b; do lark-cli api GET /open-apis/im/v1/messages --params "{\"container_id\":\"$t\"}"; done`, 0},
		{"variable flag value", `T=abc; lark-cli docs +fetch --as user --doc-id $T 2>&1 | jq -c '.data'`, 0},
		{"quoted variable flag value", `lark-cli docs +fetch --doc-id "$DOCUMENT"`, 0},
		{"literal variable in an api route", `T=abc; lark-cli api GET "/open-apis/drive/v2/permissions/$T/public" --params '{"type":"file"}' | jq -c .data`, 0},
		{"method lark-cli does not have", `lark-cli im messages get --params '{"message_id":"om_x"}' --as user`, 0},
		{"help into sed", `lark-cli --help 2>&1 | grep -iE "profile|config" | head -8; lark-cli config --help 2>&1 | sed -n 1,25p`, 0},

		// Reads on another machine: lark-cli there acts as the user too.
		{"remote auth status", `ssh -o BatchMode=yes host.example 'export PATH=$HOME/.local/bin:$PATH; lark-cli auth status 2>&1 | grep -iE "status|user" | head -8'`, 0},
		{"remote lookup and read", `timeout 40 ssh -o ConnectTimeout=15 host.example 'for c in lark-cli gh; do printf "%s: " $c; command -v $c || echo missing; done; lark-cli api GET /open-apis/im/v1/chats'`, 0},

		// Writes still ask, alone or anywhere in a command.
		{"send", `lark-cli im +messages-send --chat-id oc_x --text hi`, 1},
		{"api POST", `lark-cli api POST /open-apis/im/v1/messages --data '{"msg_type":"text"}'`, 1},
		{"send after cd into jq", `cd /tmp; lark-cli im +messages-send --chat-id oc_x --text hi | jq .`, 1},
		{"env -i POST", `env -i PATH=/usr/bin:/bin lark-cli api POST /open-apis/im/v1/messages --data '{}'`, 1},
		{"send after a read", `lark-cli docs +fetch --doc-id a | jq .; lark-cli im +messages-send --chat-id oc_x --text hi`, 1},
		{"write in a loop", `for c in oc_a oc_b; do lark-cli im +messages-send --chat-id $c --text hi; done`, 1},
		{"write in a substitution", `R=$(lark-cli docs +create --title x); echo "$R"`, 1},
		{"write with a variable route", `T=abc; lark-cli api POST "/open-apis/im/v1/messages/$T/reply"`, 1},
		{"unresolved route", `lark-cli api GET "/open-apis/im/v1/messages/$ID"`, 1},
		{"variable method", `lark-cli api "$M" /open-apis/im/v1/messages`, 1},
		{"variable after --dry-run", `lark-cli im +messages-send --dry-run --chat-id oc_x --text $MSG`, 1},
		{"remote send", `ssh host.example 'lark-cli im +messages-send --chat-id oc_x --text hi'`, 1},
		{"remote POST unquoted", `ssh -p 2222 host.example lark-cli api POST /open-apis/im/v1/messages`, 1},
		{"remote script after a read", `ssh host.example 'lark-cli auth status; python3 verify.py'`, 1},
		{"text piped to a remote shell", `echo 'lark-cli im +messages-send --text hi' | ssh host.example sh`, 1},
		{"ssh option that runs a command", `ssh -o ProxyCommand='lark-cli im +messages-send --text hi' host.example true`, 1},
		{"python heredoc sends", "python3 - <<'EOF'\nimport subprocess\nsubprocess.run(['lark-cli', 'im', '+messages-send'])\nEOF", 1},
		{"inline code naming the CLI", `lark-cli docs +fetch | python3 -c 'import os; os.system("lark-cli im +messages-send")'`, 1},
		{"inline code starting programs", `lark-cli docs +fetch | python3 -c 'import subprocess; subprocess.run(["sh"])'`, 1},
		{"pattern piped to a shell", `grep lark-cli notes.txt | sh`, 1},
		{"text piped to a program", `echo 'lark-cli im +messages-send' | make -f -`, 1},
		{"file written then run", `echo 'send:; lark-cli im +messages-send' > /tmp/x.mk; make -f /tmp/x.mk`, 1},
		{"heredoc script then run", "cd ~/reports && cat > run.sh <<'EOF'\nlark-cli im +messages-send\nEOF\nsh run.sh", 1},
		{"unquoted heredoc substitution", "cd ~/reports && cat >> notes.md <<EOF\n$(lark-cli im +messages-send)\nEOF", 1},
		{"perl substitution that evaluates", `perl -pi -e 's/x/system("lark-cli im +messages-send")/e' notes.txt`, 1},
		{"perl code", `perl -e 'system("lark-cli im +messages-send")'`, 1},
		{"perl magic open of a command", `perl -pi -e 's/a/b/' 'lark-cli im +messages-send --text hi|'`, 1},
		{"perl magic open, no name", `lark-cli docs +fetch --doc-id a; perl -pe 's/a/b/' "$SEND|"`, 1},
		{"perl -e reading a command as a file", `lark-cli docs +fetch --doc-id a; perl -ne 'print' "$SEND|"`, 1},
		{"perl -e reading a command via <>", `lark-cli docs +fetch --doc-id a; perl -e 'print while <>' "$SEND|"`, 1},
		{"ssh option with a tab", "ssh -o 'ProxyCommand\tlark-cli im +messages-send --text hi' host.example true", 1},
		{"comment before a piped program", "echo 'lark-cli im +messages-send' | # make\nmake -f -", 1},
		{"code reaching a builtin by name", `lark-cli docs +fetch | python3 -c "import os,sys; getattr(os, 'sys'+'tem')(sys.stdin.read())"`, 1},
		{"untrusted PATH", `PATH=/nonexistent/tools:$PATH lark-cli docs +fetch --doc-id a`, 1},
		{"untrusted PATH beside a login shell one", `export PATH="/opt/login/bin:/nonexistent/tools:$PATH"; lark-cli docs +fetch --doc-id a`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) { assertAssistantBypassClassification(t, tc.command, tc.cards) })
	}
}
