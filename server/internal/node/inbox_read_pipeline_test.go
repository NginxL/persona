package node

import (
	"context"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func inboxReadPipelineTestGate() proto.CommandGate {
	return proto.CommandGate{Command: "example-api", Script: "example/scripts/api.sh", Title: "Example", Rules: []proto.LarkRule{
		{Method: "GET", Path: "/private", Action: "confirm"},
		{Method: "GET", Path: "**", Action: "allow"},
		{Method: "HEAD", Path: "**", Action: "allow"},
		{Method: "*", Path: "**", Action: "confirm"},
	}}
}

// These commands are only parsed; no gate, interpreter, or network service is executed.
func TestInboxReadFormattingPipelineFilters(t *testing.T) {
	gates := []proto.CommandGate{inboxReadPipelineTestGate()}
	for _, tc := range []struct {
		name, command string
		want          bool
	}{
		{name: "jq", command: `example-api GET /accounts | jq -r '.accounts[] | .status'`, want: true},
		{name: "head", command: `example-api GET /accounts | head -5`, want: true},
		{name: "tail", command: `example-api GET /accounts | tail -5`, want: true},
		{name: "grep", command: `example-api GET /accounts | grep .`, want: true},
		{name: "cut", command: `example-api GET /accounts | cut -f 1`, want: true},
		{name: "tr", command: `example-api GET /accounts | tr a b`, want: true},
		{name: "wc", command: `example-api GET /accounts | wc -l`, want: true},
		{name: "column", command: `example-api GET /accounts | column -t`, want: true},
		{name: "stdout stderr duplication", command: `timeout 30 example-api GET /accounts 2>&1 | jq . 2>&1 | tail -30`, want: true},
		{name: "egrep", command: `example-api GET /accounts | egrep .`},
		{name: "sort", command: `example-api GET /accounts | sort`},
		{name: "sort output file", command: `example-api GET /accounts | sort -o /tmp/accounts.txt | tail -5`},
		{name: "sort compress program", command: `example-api GET /accounts | sort --compress-program=sh | tail -5`},
		{name: "uniq", command: `example-api GET /accounts | uniq`},
		{name: "uniq output file", command: `example-api GET /accounts | uniq /dev/stdin /tmp/accounts.txt | tail -5`},
		{name: "echo", command: `example-api GET /accounts | echo hi`},
		{name: "date", command: `example-api GET /accounts | date`},
		{name: "printf", command: `example-api GET /accounts | printf hi`},
		{name: "true", command: `example-api GET /accounts | true`},
		{name: "seq", command: `example-api GET /accounts | seq 1`},
		{name: "which", command: `example-api GET /accounts | which jq`},
		{name: "type", command: `example-api GET /accounts | type jq`},
		{name: "cd", command: `example-api GET /accounts | cd /tmp`},
		{name: "Python formatting", command: `example-api GET /accounts | python3 -c 'import sys; print(sys.stdin.read())'`},
		{name: "isolated Python formatting", command: `example-api GET /accounts | python3 -I -c 'import sys; print(sys.stdin.read())'`},
		{name: "untrusted interpreter path", command: `example-api GET /accounts | /tmp/python3 -c 'import sys; print(sys.stdin.read())'`},
		{name: "process execution", command: `example-api GET /accounts | python3 -c 'import subprocess; subprocess.run(["id"])'`},
		{name: "file write", command: `example-api GET /accounts | python3 -c 'import sys; open("/tmp/result.txt", "w").write(sys.stdin.read())'`},
		{name: "pathlib write text", command: `example-api GET /accounts | python3 -c 'import pathlib, sys; pathlib.Path("/tmp/result.txt").write_text(sys.stdin.read())'`},
		{name: "pathlib write bytes", command: `example-api GET /accounts | python3 -c 'from pathlib import Path; Path("/tmp/result.txt").write_bytes(b"y")'`},
		{name: "io open write", command: `example-api GET /accounts | python3 -c 'import io; io.open("/tmp/result.txt", "w").write("y")'`},
		{name: "os open and write", command: `example-api GET /accounts | python3 -c 'import os; os.write(os.open("/tmp/result.txt", 65), b"y")'`},
		{name: "json codecs open", command: `example-api GET /accounts | python3 -c 'import json; json.codecs.open("/tmp/result.txt", "w").write("y")'`},
		{name: "shutil copy", command: `example-api GET /accounts | python3 -c 'import shutil; shutil.copy("/etc/hosts", "/tmp/result.txt")'`},
		{name: "glob os remove", command: `example-api GET /accounts | python3 -c 'import glob; glob.os.remove("/tmp/result.txt")'`},
		{name: "exec string", command: `example-api GET /accounts | python3 -c 'exec("print(1)")'`},
		{name: "dynamic import", command: `example-api GET /accounts | python3 -c '__import__("os").system("id")'`},
		{name: "Python module", command: `example-api GET /accounts | python3 -m json.tool`},
		{name: "Python heredoc", command: "example-api GET /accounts | python3 - <<'EOF'\nimport sys\nprint(sys.stdin.read())\nEOF"},
		{name: "Python network import", command: `example-api GET /accounts | python3 -c 'import urllib.request; print(urllib.request.urlopen("https://example.com").read())'`},
		{name: "typing get_type_hints evaluates a string", command: `example-api GET /accounts | python3 -c 'import typing
def f(x: "__import__(\x27os\x27).system(\x27touch /tmp/review-result\x27)"): pass
typing.get_type_hints(f)
print(1)'`},
		{name: "typing from-import alias evaluates a string", command: `example-api GET /accounts | python3 -c 'from typing import get_type_hints as g
class C:
    a: "__import__(\x27os\x27).system(\x27id\x27)"
g(C)'`},
		{name: "singledispatch evaluates a string annotation", command: `example-api GET /accounts | python3 -c 'import functools
@functools.singledispatch
def f(x): pass
def h(x: "__import__(\x27os\x27).system(\x27id\x27)"): pass
f.register(h)'`},
		{name: "appended shell command", command: `example-api GET /accounts | jq .; touch /tmp/changed`},
		{name: "second gate call", command: `example-api GET /accounts | example-api GET /accounts | tail -5`},
		{name: "gate output file", command: `example-api GET /accounts -o /tmp/accounts.json | tail -5`},
		{name: "network filter", command: `example-api GET /accounts | curl https://example.com`},
		{name: "file redirect", command: `example-api GET /accounts > /tmp/accounts.json | jq .`},
		{name: "inherited output descriptor", command: `example-api GET /accounts >&3 | tail -5`},
		{name: "inherited input descriptor", command: `example-api GET /accounts <&3 | tail -5`},
		{name: "command substitution", command: `example-api GET /accounts | jq "$(cat filter.jq)"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := larkReadFormattingPipeline(tc.command, gates); got != tc.want {
				t.Fatalf("larkReadFormattingPipeline()=%v, want %v", got, tc.want)
			}
		})
	}
}

// The end-to-end cases distinguish this Inbox exception from the pre-existing ordinary read-pipeline path. In
// particular, plan mode must return to the usual permission flow even though an ordinary default read is allowed.
func TestInboxDefaultReadFormattingPipelineBoundary(t *testing.T) {
	const readPipeline = `timeout 30 example-api GET /accounts?fresh=1 2>&1 | jq -r '(.accounts // .data // .)[] | [(.status // .state), .used_pct, .reset_at] | @tsv' 2>&1 | tail -30`
	const pythonPipeline = `example-api GET /accounts | python3 -c 'import sys; print(sys.stdin.read())'`

	for _, tc := range []struct {
		name, origin, command, mode, want string
		changeCwd                         bool
	}{
		{name: "default mode allows fixed filters", origin: originInbox, command: readPipeline, want: "allow"},
		{name: "plan mode retains permission flow", origin: originInbox, command: readPipeline, mode: proto.ModePlan, want: ""},
		{name: "bypass mode is outside this exception", origin: originInbox, command: readPipeline, mode: proto.ModeBypass, want: ""},
		{name: "POST mutation", origin: originInbox, command: `timeout 30 example-api POST /accounts '{}' 2>&1 | tail -30`, want: "deny"},
		{name: "confirmation required GET", origin: originInbox, command: `example-api GET /private | python3 -c 'import sys; print(sys.stdin.read())'`, want: ""},
		{name: "ordinary origin", command: pythonPipeline, want: ""},
		{name: "changed working directory", origin: originInbox, command: pythonPipeline, changeCwd: true, want: ""},
		{name: "Python formatting is not a filter", origin: originInbox, command: pythonPipeline, want: ""},
		{name: "typing get_type_hints is not auto-allowed", origin: originInbox, command: `example-api GET /accounts | python3 -c 'import typing
def f(x: "__import__(\x27os\x27).system(\x27touch /tmp/review-result\x27)"): pass
typing.get_type_hints(f)
print(1)'`, want: ""},
		{name: "typing from-import alias is not auto-allowed", origin: originInbox, command: `example-api GET /accounts | python3 -c 'from typing import get_type_hints as g
class C:
    a: "__import__(\x27os\x27).system(\x27id\x27)"
g(C)'`, want: ""},
		{name: "singledispatch is not auto-allowed", origin: originInbox, command: `example-api GET /accounts | python3 -c 'import functools
@functools.singledispatch
def f(x): pass
def h(x: "__import__(\x27os\x27).system(\x27id\x27)"): pass
f.register(h)'`, want: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := codexTestNode(t)
			fh := withFakeHub(t, n)
			gate := inboxReadPipelineTestGate()
			n.larkCfg, n.larkCfgAt = proto.AssistantLark{Gates: []proto.CommandGate{gate}}, time.Now().Add(time.Hour)
			cwd := t.TempDir()
			n.procs[testThread] = &assistantProc{id: testThread, agent: "claude", origin: tc.origin, cwd: cwd, exited: make(chan struct{})}
			mode := tc.mode
			if mode == "" {
				mode = proto.ModeDefault
			}
			n.persistModes(testThread, mode, "")
			fh.answer = func(proto.Interaction) proto.Resolution { no := false; return proto.Resolution{Allow: &no} }
			checkCwd := cwd
			if tc.changeCwd {
				checkCwd = t.TempDir()
			}
			decision, reason, err := n.larkCheck(context.Background(), testThread, checkCwd, tc.command,
				proto.Interaction{SessionID: testThread, ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil || decision != tc.want {
				t.Fatalf("decision=%q reason=%q err=%v, want %q", decision, reason, err, tc.want)
			}
			if len(fh.interactions()) != 0 {
				t.Fatalf("classification unexpectedly opened a card: %v", fh.interactions())
			}
		})
	}
}
