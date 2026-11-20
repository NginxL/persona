package node

import (
	"path/filepath"
	"testing"
)

// Literal command prefixes are expanded for classification only. A prefix may contain an interpreter and its flags,
// not only one executable path. Unknown expansions and every write shape keep their approval cards (#11307).
func TestLarkLiteralCommandPrefixes(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		cards         int
	}{
		{"interpreter prefix", `READER="python3 -I -B /opt/example/reader.py"; timeout 60 $READER list < /dev/null 2>&1 | head -30`, 0},
		{"CLI read prefix", `READER="lark-cli docs +fetch"; $READER --doc-id example | jq .`, 0},
		{"prefix in nested shell", `bash -c 'READER="python3 -I -B /opt/example/reader.py"; timeout 60 $READER list'`, 0},
		{"nested CLI read prefix", `sh -c 'READER="lark-cli docs +fetch"; $READER --doc-id example'`, 0},
		{"unknown command prefix", `READER="$(choose_reader)"; $READER list`, 1},
		{"CLI write prefix", `READER="lark-cli im +messages-send --chat-id oc_example --text hello"; $READER`, 1},
		{"gated write prefix", `REQUEST="ex POST /messages/example/recall"; $REQUEST`, 1},
		{"write in nested prefix", `bash -c 'REQUEST="ex POST /messages/example/recall"; $REQUEST'`, 1},
		{"write after expanded read", `REQUEST="ex GET /health"; $REQUEST; ex POST /messages/example/recall`, 1},
		{"prefix before conditional suffix", `READER="python3 -I -B /opt/example/reader.py"; timeout 60 $READER list | head; cd /tmp && python3 verify.py`, 0},
		{"write in conditional suffix", `REQUEST="ex GET /health"; $REQUEST; cd /tmp && ex POST /messages/example/recall`, 1},
		{"literal arguments in nested shell", `F=/tmp/example.log; timeout 590 bash -c "until grep -qE 'done|STOP' $F; do sleep 10; done"; cat $F`, 0},
		{"nested write after literal argument expansion", `F=/tmp/example.log; bash -c "cat $F; ex POST /messages/example/recall"`, 1},
		{"nested write through expanded executable", `F=ex; bash -c "$F POST /messages/example/recall"`, 1},
		{"literal remote script fragments", `SCRIPT='uname -r; uptime -s'; timeout 40 ssh host "ssh other '$SCRIPT; systemctl is-active example'" 2>&1; timeout 40 ssh host "ssh third '$SCRIPT; ss -ltnH | head -2'" 2>&1`, 0},
		{"remote script fragment with CLI write", `SCRIPT='lark-cli im +messages-send --chat-id oc_example --text hello'; ssh host "$SCRIPT"`, 1},
		{"nested script fragment with gated write", `SCRIPT='ex POST /messages/example/recall'; bash -c "$SCRIPT"`, 1},
		{"nested while read filter", `bash -c 'T=/tmp/example.log; timeout 60 grep -a -n "timestamp" $T | cut -d: -f1 | while read n; do sed -n "${n}p" $T | python3 -I -c "import json,sys; print(json.load(sys.stdin))"; done'`, 0},
		{"block before gated read", `cd /tmp && { f=$(git ls-files | grep -m1 review); echo "$f"; grep -n model "$f" | head; }; ex GET /accounts | python3 -c 'import json,sys; print(json.load(sys.stdin))' | tail`, 0},
		{"write inside block", `{ f=example; echo "$f"; ex POST /messages/$f/recall; }; ex GET /accounts`, 1},
		{"shell punctuation in a value stays unknown", `READER="python3 /opt/example/reader.py; lark-cli im +messages-send --text hello"; $READER`, 1},
		{"expanded opaque program before a read", `READER="python3 -I -B /opt/example/reader.py"; $READER list; lark-cli docs +fetch --doc-id example`, 0},
		{"expanded opaque program after a read", `READER="python3 -I -B /opt/example/reader.py"; lark-cli docs +fetch --doc-id example; $READER list`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) { assertAssistantBypassClassification(t, tc.command, tc.cards) })
	}
}

func TestLarkReadCommandSubstitutions(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		cards         int
	}{
		{"read saved and formatted", `for i in $(seq 1 2); do out=$(timeout 30 lark-cli im +chat-messages-list --chat-id oc_$i --as user); n=$(printf '%s' "$out" | python3 -I -c 'import sys,json; d=json.load(sys.stdin); print(len(d))'); if [ "${n:-0}" -gt 0 ]; then echo "$n"; fi; sleep 1; done`, 0},
		{"write in substitution", `out=$(lark-cli im +messages-send --chat-id oc_example --text hello); echo "$out"`, 1},
		{"read substituted into eval", `out=$(lark-cli docs +fetch --doc-id example); eval "$out"`, 1},
		{"read substituted into shell", `out=$(lark-cli docs +fetch --doc-id example); sh -c "$out"`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) { assertAssistantBypassClassification(t, tc.command, tc.cards) })
	}
}

func TestLarkLiteralLoopCommandAssignments(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		cards         int
	}{
		{"literal loop executable", `QUERY=/opt/example/metric; for psm in dev host; do timeout 90 $QUERY --json apm metric query "sum{job=$psm}" > /tmp/metric-$psm.json 2>/tmp/metric-$psm.err; done`, 0},
		{"conditional literal loop assignment stays unknown", `cd /tmp && QUERY=/opt/example/metric; for psm in dev host; do timeout 90 $QUERY --json apm metric query "sum{job=$psm}" > /tmp/metric-$psm.json; echo "$psm rc=$?"; done`, 1},
		{"conditional assignment cannot hide a deletion route", `action=delete; false && action=read; for i in one; do ex GET "/documents/$action"; done`, 1},
		{"conditional gated write assignment", `cd /tmp && QUERY=ex; for id in one; do $QUERY POST /messages/$id/recall; done`, 1},
		{"literal loop with environment", `QUERY=/opt/example/metric; for id in one two; do TOKEN="$(auth token)" timeout 60 $QUERY query --id $id > /tmp/query-$id.json; done`, 0},
		{"literal loop with heredoc reader", "QUERY=/opt/example/metric; for id in one two; do timeout 60 $QUERY query --id $id > /tmp/query-$id.json; python3 -I - /tmp/query-$id.json <<'EOF'\nimport sys\nprint(len(open(sys.argv[1]).read()))\nEOF\ndone", 0},
		{"literal loop with result reader", "QUERY=/opt/example/metric; for id in one two; do TOKEN=\"$(auth token)\" timeout 60 $QUERY query --id $id > /tmp/query-$id.json; python3 -I - /tmp/query-$id.json <<'EOF'\nimport sys\nprint(len(open(sys.argv[1]).read()))\nEOF\ndone", 0},
		{"literal loop checksum", `for f in /opt/example/personad; do $f --version 2>&1 | head -1; sha256sum $f; cat $f.version 2>/dev/null; done`, 0},
		{"gated write through loop assignment", `QUERY=ex; for id in one; do $QUERY POST /messages/$id/recall; done`, 1},
		{"write after expanded loop", `QUERY=/opt/example/metric; for id in one; do $QUERY query --id $id; done; lark-cli im +messages-send --chat-id oc_example --text hello`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) { assertAssistantBypassClassification(t, tc.command, tc.cards) })
	}
}

// A query separator in an unquoted absolute route is not an argument split when the shell glob has no match. Keeping
// it as the last argument also means nullglob/failglob cannot turn it into a different request. Sed address regular
// expressions are data: only actual e/w/W commands or substitution flags make the script opaque (#11307).
func TestLarkLiteralQueryAndSedAddresses(t *testing.T) {
	query := filepath.Join(t.TempDir(), "accounts") + "?fresh=1"
	for _, tc := range []struct {
		name, command string
		cards         int
	}{
		{"unmatched GET query glob", `ex GET ` + query, 0},
		{"quoted GET query", `ex GET '/accounts?fresh=1'`, 0},
		{"POST with the same query", `ex POST ` + query, 1},
		{"glob in query value", `ex GET /accounts?fresh=*`, 1},
		{"another argument after the query", `ex GET /accounts?fresh=1 /messages/example/delete`, 1},
		{"read through sed addresses", `lark-cli im +chat-messages-list --as user --chat-id oc_example 2>&1 | sed -n '/## Messages/,/## Summary/p' | head -14`, 0},
		{"sed e command", `lark-cli docs +fetch --doc-id example | sed -n '/Messages/e lark-cli im +messages-send --text hello'`, 1},
		{"sed substitution e flag", `lark-cli docs +fetch --doc-id example | sed -n 's/.*/lark-cli im +messages-send --text hello/e'`, 1},
		{"sed substitution w flag", `lark-cli docs +fetch --doc-id example | sed -n 's/Messages/Summary/w /tmp/example-output'`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) { assertAssistantBypassClassification(t, tc.command, tc.cards) })
	}
}

func TestLarkContactSearchIsARead(t *testing.T) {
	assertAssistantBypassClassification(t, `lark-cli contact +search-user --as user --query example`, 0)
	// Risk metadata remains authoritative when it says a same-named command writes.
	v := classifyLark(`lark-cli contact +search-user --as user --query example`, nil, "", func([]string) string { return "write" })
	if len(v.calls) != 1 || v.calls[0].Risk != "write" || !v.cliWrite {
		t.Fatalf("reported write risk was lost: %+v", v)
	}
}

func TestLarkUnparsedWrapperGateNames(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		cards         int
	}{
		{"env split string gated write", `env -S "ex POST /messages/example/recall"`, 1},
		{"env long split string gated write", `env --split-string="ex POST /messages/example/recall"`, 1},
		{"direct gated write control", `ex POST /messages/example/recall`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) { assertAssistantBypassClassification(t, tc.command, tc.cards) })
	}
}

func TestLarkResolvedPrefixScope(t *testing.T) {
	for _, tc := range []struct {
		name, command string
		cards         int
	}{
		{"direct read piped into script", `lark-cli docs +fetch --doc-id example | python3 /opt/example/reader.py`, 1},
		{"variable read piped into script", `READER="python3 /opt/example/reader.py"; lark-cli docs +fetch --doc-id example | $READER`, 1},
		{"direct script piped into read", `python3 /opt/example/reader.py | lark-cli docs +fetch --doc-id example`, 1},
		{"variable script piped into read", `READER="python3 /opt/example/reader.py"; $READER | lark-cli docs +fetch --doc-id example`, 1},
		{"same words in a later literal segment", `READER="python3 /opt/example/reader.py"; $READER; python3 /opt/example/reader.py; lark-cli docs +fetch --doc-id example`, 1},
		{"resolved script in a separate sequential pipeline", `READER="python3 /opt/example/reader.py"; timeout 60 $READER list < /dev/null 2>&1 | head -30; lark-cli contact +search-user --as user --query example 2>/dev/null | python3 -c 'import sys,json; print(len(json.load(sys.stdin)))' | head -5`, 0},
		{"resolved opaque program in a sequential prefix", `READER="python3 /opt/example/reader.py"; $READER list; lark-cli docs +fetch --doc-id example`, 0},
	} {
		t.Run(tc.name, func(t *testing.T) { assertAssistantBypassClassification(t, tc.command, tc.cards) })
	}
}
