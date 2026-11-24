package node

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
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

func TestLarkLiteralPrefixesAcrossIndependentReads(t *testing.T) {
	for _, tc := range []struct {
		name, command string
	}{
		{"relative assignment after directory change", `cd /tmp; READER=./reader.sh; $READER list`},
		{"null redirect after assignment", `READER=/tmp/reader.sh; ls /tmp 2>/dev/null; $READER list`},
		{"descriptor duplication after assignment", `READER=/tmp/reader.sh; ls /tmp 2>&1; $READER list`},
		{"glob before assignment", `ls /tmp/*/; READER=/tmp/reader.sh; $READER list`},
		{"scalar parameter before assignment", `ls "$HOME"; READER=/tmp/reader.sh; $READER list`},
		{"brace expansion before assignment", `echo {one,two}; READER=/tmp/reader.sh; $READER list`},
	} {
		t.Run(tc.name, func(t *testing.T) { assertAssistantBypassClassification(t, tc.command, 0) })
	}
}

func TestLarkLiteralPrefixTrackedDirectories(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, command string
	}{
		{"explicit current directory child", `cd ` + root + `; cd ./sub; READER=./reader.sh; $READER list`},
		{"literal parent directory", `cd ` + filepath.Join(root, "sub") + `; cd ..; READER=./reader.sh; $READER list`},
		{"leave and return to binding directory", `cd ` + root + `; READER=./reader.sh; cd ` + filepath.Join(root, "sub") + `; cd ` + root + `; $READER list`},
		{"current directory no-op", `cd ` + root + `; cd .; READER=./reader.sh; $READER list`},
	} {
		t.Run(tc.name, func(t *testing.T) { assertAssistantBypassClassification(t, tc.command, 0) })
	}
}

func TestLarkLiteralPrefixIndependentReadSafety(t *testing.T) {
	gate := exampleGate("")
	missing := filepath.Join(t.TempDir(), "missing")
	directoryRoot := t.TempDir()
	if err := os.Mkdir(filepath.Join(directoryRoot, "sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	nestedDirectory := filepath.Join(directoryRoot, "a", "b", "c")
	if err := os.MkdirAll(nestedDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	// Reach the filesystem root without assuming how many components t.TempDir contains.
	rootAscent, err := filepath.Rel(nestedDirectory, string(filepath.Separator))
	if err != nil {
		t.Fatal(err)
	}
	directoryFile := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(directoryFile, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, command string
		cards         int
	}{
		{"assignment before directory change", `READER=./reader.sh; cd /tmp; $READER list`, 1},
		{"assignment before second directory change", `cd /tmp; READER=./reader.sh; cd /var; $READER list`, 1},
		{"relative directory change stays unknown", `cd /tmp; cd subdirectory; READER=./reader.sh; $READER list`, 1},
		{"unknown directory relative pipeline stays unknown", `cd /tmp; cd subdirectory; READER=./reader.sh; $READER list | cat`, 1},
		{"existing relative directory change stays unknown", `cd ` + directoryRoot + `; cd sub; READER=./reader.sh; $READER list`, 1},
		{"bare executable after directory change", `cd /tmp; READER=reader.sh; $READER list`, 1},
		{"relative CLI after directory change", `cd /tmp; READER=./lark-cli; $READER docs +fetch --doc-id example`, 1},
		{"relative gate command after directory change", `cd /tmp; REQUEST=./ex; $REQUEST GET /health`, 1},
		{"relative gate read after directory change", `cd /tmp; REQUEST=./ex.sh; $REQUEST GET /health`, 1},
		{"relative gate write after directory change", `cd /tmp; REQUEST=./ex.sh; $REQUEST POST /messages/example/recall`, 1},
		{"gate write after null redirect", `REQUEST=/tmp/example/scripts/ex.sh; ls /tmp 2>/dev/null; $REQUEST POST /messages/example/recall`, 1},
		{"gate deletion after read glob", `ls /tmp/*/; REQUEST=/tmp/example/scripts/ex.sh; $REQUEST DELETE /messages/example`, 1},
		{"command substitution before assignment", `ls $(choose_path); READER=/tmp/reader.sh; $READER list`, 1},
		{"process substitution before assignment", `cat <(printf example); READER=/tmp/reader.sh; $READER list`, 1},
		{"arithmetic expansion before assignment", `ls $((1 + 1)); READER=/tmp/reader.sh; $READER list`, 1},
		{"parameter assignment before assignment", `ls ${OTHER:=/tmp}; READER=/tmp/reader.sh; $READER list`, 1},
		{"file output clears assignment", `READER=/tmp/reader.sh; ls /tmp > /tmp/listing; $READER list`, 1},
		{"dynamic redirect clears assignment", `READER=/tmp/reader.sh; ls /tmp 2>$ERROR_FILE; $READER list`, 1},
		{"environment assignment clears binding", `READER=/tmp/reader.sh; EXAMPLE=value ls /tmp; $READER list`, 1},
		{"unsupported wrapper stops resolution", `xargs ls /tmp/*/; READER=/tmp/reader.sh; $READER list`, 1},
		{"dynamic reassignment stays unknown", `READER=/tmp/reader.sh; ls /tmp 2>/dev/null; READER=$(choose_reader); $READER list`, 1},
		{"named descriptor null redirect clears binding", `READER=/tmp/reader.sh; ls /tmp {READER}>/dev/null; $READER list`, 1},
		{"named descriptor duplication clears binding", `READER=/tmp/reader.sh; ls /tmp {fd}>&1; $READER list`, 1},
		{"ampersand output clears binding", `READER=/tmp/reader.sh; ls /tmp &>/dev/null; $READER list`, 1},
		{"clobber output clears binding", `READER=/tmp/reader.sh; ls /tmp >|/dev/null; $READER list`, 1},
		{"descriptor close clears binding", `READER=/tmp/reader.sh; ls /tmp >&-; $READER list`, 1},
		{"source descriptor three clears binding", `READER=/tmp/reader.sh; ls /tmp 3>&1; $READER list`, 1},
		{"target descriptor three clears binding", `READER=/tmp/reader.sh; ls /tmp 2>&3; $READER list`, 1},
		{"input descriptor duplication clears binding", `READER=/tmp/reader.sh; cat <&0; $READER list`, 1},
		{"here string clears binding", `READER=/tmp/reader.sh; cat <<< example; $READER list`, 1},
		{"read-write redirect clears binding", `READER=/tmp/reader.sh; ls /tmp <> /dev/null; $READER list`, 1},
		{"here document clears binding", "READER=/tmp/reader.sh; cat <<'EOF'\nexample\nEOF\n$READER list", 1},
		{"stdout device target clears binding", `READER=/tmp/reader.sh; ls /tmp >/dev/stdout; $READER list`, 1},
		{"zsh parameter subscript clears binding", `READER=/tmp/reader.sh; echo $HOME[READER=7]; $READER list`, 1},
		{"zsh parameter modifier clears binding", `READER=/tmp/reader.sh; echo $HOME:F:READER=7:h; $READER list`, 1},
		{"quoted zsh parameter modifier clears binding", `READER=/tmp/reader.sh; echo "$HOME:F:READER=7:h"; $READER list`, 1},
		{"special parameter modifier clears binding", `READER=/tmp/reader.sh; echo $#HOME:F:READER=7:h; $READER list`, 1},
		{"zsh tilde flag subscript clears binding", `READER=/tmp/reader.sh; echo $~HOME[READER=7]; $READER list`, 1},
		{"zsh equals flag subscript clears binding", `READER=/tmp/reader.sh; ls $=HOME[READER=7]; $READER list`, 1},
		{"zsh caret flag subscript clears binding", `READER=/tmp/reader.sh; cat $^HOME[READER=7]; $READER list`, 1},
		{"zsh plus flag subscript clears binding", `READER=/tmp/reader.sh; echo $+HOME[READER=7]; $READER list`, 1},
		{"zsh non ASCII subscript clears binding", `READER=/tmp/reader.sh; echo $é[READER=7]; $READER list`, 1},
		{"zsh tilde modifier glob clears binding", `READER=/tmp/reader.sh; echo $~HOME:F:READER=7:h/*; $READER list`, 1},
		{"zsh glob qualifier clears binding to CLI", `READER=/tmp/reader.sh; X='mp(e:READER=lark-c\li:)'; echo /[t]$~X; $READER im +messages-send --chat-id oc_example --text hello`, 1},
		{"dollar at is not scalar", `echo "$@"; READER=/tmp/reader.sh; $READER list`, 1},
		{"dollar star is not scalar", `echo $*; READER=/tmp/reader.sh; $READER list`, 1},
		{"array parameter is not scalar", `echo ${X[1]}; READER=/tmp/reader.sh; $READER list`, 1},
		{"length parameter is not scalar", `echo ${#X}; READER=/tmp/reader.sh; $READER list`, 1},
		{"indirect parameter is not scalar", `echo ${!X}; READER=/tmp/reader.sh; $READER list`, 1},
		{"extended glob stays unknown", `ls /tmp/@(one|two); READER=/tmp/reader.sh; $READER list`, 1},
		{"sort scalar is not independent", `sort "$HOME"; READER=/tmp/reader.sh; $READER list`, 1},
		{"find glob is not independent", `find /tmp/*; READER=/tmp/reader.sh; $READER list`, 1},
		{"timeout wrapper stops independent read", `timeout 5 ls "$HOME"; READER=/tmp/reader.sh; $READER list`, 1},
		{"env wrapper stops independent read", `env ls /tmp/*; READER=/tmp/reader.sh; $READER list`, 1},
		{"uniq output operand is not independent", `uniq "$INPUT" "$OUTPUT" | cat; READER=/tmp/reader.sh; $READER list`, 1},
		{"ps is not independent", `ps "$HOME" | cat; READER=/tmp/reader.sh; $READER list`, 1},
		{"stat is not independent", `stat "$HOME" | cat; READER=/tmp/reader.sh; $READER list`, 1},
		{"du is not independent", `du "$HOME" | cat; READER=/tmp/reader.sh; $READER list`, 1},
		{"df is not independent", `df "$HOME" | cat; READER=/tmp/reader.sh; $READER list`, 1},
		{"previous directory stays unknown", `cd /tmp; READER=./reader.sh; cd -; $READER list`, 1},
		{"other user home stays unknown", `cd /tmp; READER=./reader.sh; cd ~other; $READER list`, 1},
		{"directory option stays unknown", `cd /tmp; READER=./reader.sh; cd -P /tmp; $READER list`, 1},
		{"bare cd stays unknown", `cd; READER=./reader.sh; $READER list`, 1},
		{"missing directory stays unknown", `cd ` + missing + `; READER=./reader.sh; $READER list`, 1},
		{"file instead of directory stays unknown", `cd ` + directoryFile + `; READER=./reader.sh; $READER list`, 1},
		{"unanchored relative executable stays unknown", `cd /tmp; READER=sub/reader.sh; $READER list`, 1},
		{"parent relative executable stays unknown", `cd /tmp; READER=./../reader.sh; $READER list`, 1},
		{"system bin directory stays unknown", `cd /bin; READER=./cat; $READER list`, 1},
		{"system usr bin directory stays unknown", `cd /usr/bin; READER=./cat; $READER list`, 1},
		{"joined bin directory stays unknown", `cd /; READER=./bin/cat; lark-cli docs +fetch --doc-id example | $READER`, 1},
		{"joined usr bin directory stays unknown", `cd /usr; READER=./bin/cat; lark-cli docs +fetch --doc-id example | $READER`, 1},
		{"joined root usr bin directory stays unknown", `cd /; READER=./usr/bin/cat; lark-cli docs +fetch --doc-id example | $READER`, 1},
		{"nested cwd system directory stays unknown", `cd ` + nestedDirectory + `; bash -c 'cd ` + rootAscent + `; READER=./usr/bin/cat; lark-cli docs +fetch --doc-id example | $READER'`, 1},
		{"parent relative gate read stays unknown", `cd /tmp; REQUEST=../example/scripts/ex.sh; $REQUEST GET /health`, 1},
		{"nested relative CLI stays unknown", `cd /tmp; READER=./sub/lark-cli; $READER docs +fetch --doc-id example`, 1},
		{"pushd stays unknown", `pushd /tmp; READER=./reader.sh; $READER list`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) { assertAssistantBypassClassification(t, tc.command, tc.cards, gate) })
	}
}

func TestLarkLiteralPrefixCodexShellApprovalShapes(t *testing.T) {
	gate := exampleGate("")
	for _, tc := range []struct {
		name, command string
		cards         int
	}{
		{"wrapped independent read", `/bin/zsh -lc 'READER=/tmp/reader.sh; ls /tmp 2>&1; $READER list'`, 0},
		{"wrapped named descriptor", `/bin/zsh -lc 'READER=/tmp/reader.sh; ls /tmp {READER}>/dev/null; $READER list'`, 1},
		{"wrapped zsh subscript", `/bin/zsh -lc 'READER=/tmp/reader.sh; echo $HOME[READER=7]; $READER list'`, 1},
		{"wrapped zsh modifier", `/bin/zsh -lc 'READER=/tmp/reader.sh; ls $HOME:F:READER=7:h; $READER list'`, 1},
		{"wrapped zsh glob qualifier clears binding to CLI", `/bin/zsh -lc 'READER=/tmp/reader.sh; X="mp(e:READER=lark-c\li:)"; ls /[t]$~X; $READER im +messages-send --chat-id oc_example --text hello'`, 1},
		{"wrapped gate write", `/bin/zsh -lc 'REQUEST=/tmp/example/scripts/ex.sh; ls /tmp 2>&1; $REQUEST POST /messages/example/recall'`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) { assertAssistantBypassClassification(t, tc.command, tc.cards, gate) })
	}
}

func TestLarkLiteralPrefixPermissionModes(t *testing.T) {
	const read = `REQUEST=/tmp/example/scripts/ex.sh; ls "$HOME"; $REQUEST GET /health`
	const write = `REQUEST=/tmp/example/scripts/ex.sh; ls "$HOME"; $REQUEST POST /messages/example/recall`
	for _, tc := range []struct {
		name, origin, mode, command, want string
		health                            bool
		cards                             int
	}{
		{name: "ordinary default keeps normal read flow", mode: proto.ModeDefault, command: read, want: ""},
		{name: "inbox default keeps normal read flow", origin: originInbox, mode: proto.ModeDefault, command: read, want: ""},
		{name: "health keeps normal read flow", mode: proto.ModeBypass, command: read, health: true, want: ""},
		{name: "ordinary default write confirms", mode: proto.ModeDefault, command: write, want: "deny", cards: 1},
		{name: "inbox write is refused", origin: originInbox, mode: proto.ModeBypass, command: write, want: "deny"},
		{name: "health write confirms", mode: proto.ModeBypass, command: write, health: true, want: "deny", cards: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := codexTestNode(t)
			fh := withFakeHub(t, n)
			cwd := t.TempDir()
			n.larkCfg, n.larkCfgAt = olCfg(), time.Now().Add(time.Hour)
			n.larkCfg.Gates = []proto.CommandGate{exampleGate("")}
			n.procs[testThread] = &assistantProc{id: testThread, agent: "claude", origin: tc.origin, cwd: cwd, exited: make(chan struct{})}
			n.persistModes(testThread, tc.mode, "")
			if tc.health {
				n.gates().markSpace(testThread)
			}
			fh.answer = func(proto.Interaction) proto.Resolution { no := false; return proto.Resolution{Allow: &no} }
			decision, reason, err := n.larkCheck(context.Background(), testThread, cwd, tc.command,
				proto.Interaction{SessionID: testThread, ExpiresAt: time.Now().Add(time.Minute)})
			if err != nil || decision != tc.want || len(fh.interactions()) != tc.cards {
				t.Fatalf("decision=%q reason=%q cards=%d err=%v; want %q/%d", decision, reason, len(fh.interactions()), err, tc.want, tc.cards)
			}
		})
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
