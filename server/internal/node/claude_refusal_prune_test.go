package node

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pruneID = "11111111-2222-3333-4444-555555555555"

func pruningLine(typ string, extra ...string) string {
	parts := []string{`"type":"` + typ + `"`}
	for _, e := range extra {
		if e = strings.TrimSuffix(e, ","); e != "" {
			parts = append(parts, e)
		}
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func prunePrompt(uuid, text string) string {
	return pruningLine("user", `"uuid":"`+uuid+`"`, `"message":{"role":"user","content":`+quote(text)+`}`)
}

// pruneVirtualPrompt is a user row Claude Code writes itself (a summary line, a tool note): no turn starts there.
func pruneVirtualPrompt(uuid, text string) string {
	return pruningLine("user", `"uuid":"`+uuid+`"`, `"isVirtual":true`, `"message":{"role":"user","content":`+quote(text)+`}`)
}

func pruneToolResult(uuid string) string {
	return pruningLine("user", `"uuid":"`+uuid+`"`,
		`"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]}`)
}

func pruneAssistant(uuid, stopReason, content string) string {
	return pruningLine("assistant", `"uuid":"`+uuid+`"`, `"message":{"role":"assistant","model":"primary-model",`+
		stopReason+`"content":`+content+`}`)
}

func pruneSynthetic(uuid, extra, content string) string {
	return pruningLine("assistant", `"uuid":"`+uuid+`"`, extra,
		`"message":{"role":"assistant","model":"<synthetic>","content":`+content+`}`)
}

func pruneText(s string) string { return `[{"type":"text","text":` + quote(s) + `}]` }

// quote is JSON string quoting for the fixtures.
func quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func cleanTurn(text string) []string {
	return []string{prunePrompt("u-"+text, text), pruneAssistant("a-"+text, `"stop_reason":"end_turn",`, pruneText("done "+text))}
}

func refusalTurnLines(text string) []string {
	return []string{
		prunePrompt("u-r-"+text, text),
		pruningLine("system", `"subtype":"model_refusal_no_fallback"`),
		pruneSynthetic("e-r-"+text, `"error":"invalid_request",`,
			pruneText("API Error: Opus 5.5 can't help with this. Start a new session to continue.")),
	}
}

// writePruneFile writes lines as a transcript file and returns its path.
func writePruneFile(t *testing.T, lines []string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), pruneID+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readPruneFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPruneCleanTranscriptUntouched(t *testing.T) {
	path := writePruneFile(t, append(cleanTurn("one"), cleanTurn("two")...))
	before := readPruneFile(t, path)
	pruned, hasClean, err := pruneClaudeRefusalFile(path)
	if err != nil || pruned || !hasClean {
		t.Fatalf("clean transcript: pruned=%v hasClean=%v err=%v", pruned, hasClean, err)
	}
	if got := readPruneFile(t, path); got != before {
		t.Fatalf("a clean transcript changed:\n%s", got)
	}
	if _, err := os.Stat(path + refusalBackupSuffix); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a clean transcript was backed up: %v", err)
	}
}

// A single-step refusal behind a clean turn is cut back to that turn, with a 0600 backup next to it whose name is no
// *.jsonl (the projects scan would take it for a session).
func TestPruneSingleRefusalTruncatesToTheLastCleanTurn(t *testing.T) {
	clean := cleanTurn("one")
	lines := append(append([]string{}, clean...), pruningLine("last-prompt", `"leafUuid":"u-r-x"`))
	lines = append(lines, refusalTurnLines("x")...)
	path := writePruneFile(t, lines)
	original := readPruneFile(t, path)

	pruned, hasClean, err := pruneClaudeRefusalFile(path)
	if err != nil || !pruned || !hasClean {
		t.Fatalf("refusal turn: pruned=%v hasClean=%v err=%v", pruned, hasClean, err)
	}
	want := strings.Join(clean, "\n") + "\n" + strings.Join(lines[len(clean):len(clean)+1], "\n") + "\n"
	if got := readPruneFile(t, path); got != want {
		t.Fatalf("not truncated to the clean turn:\n%s\nwant:\n%s", got, want)
	}
	backup := path + refusalBackupSuffix
	if filepath.Ext(backup) != ".bak" || strings.HasSuffix(backup, ".jsonl") {
		t.Fatalf("the backup name is a session: %s", backup)
	}
	st, err := os.Stat(backup)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("the backup is %o, want 0600", st.Mode().Perm())
	}
	if got := readPruneFile(t, backup); got != original {
		t.Fatal("the backup is not the original")
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the truncated file's mode changed: %v %v", st, err)
	}
}

// A failed turn can be several steps: its tool call and result, a virtual user line and the CLI's informational rows
// are the turn's like its prompt, and go with it.
func TestPruneMultiStepRefusalTurn(t *testing.T) {
	clean := cleanTurn("one")
	lines := append([]string{}, clean...)
	lines = append(lines,
		prunePrompt("u-r-two", "second request"),
		pruneAssistant("a-tool", `"stop_reason":"tool_use",`, `[{"type":"tool_use","id":"t1","name":"Read","input":{}}]`),
		pruneToolResult("u-result"),
		pruningLine("system", `"subtype":"informational"`),
		pruneVirtualPrompt("u-virtual", "virtual"),
		pruningLine("system", `"subtype":"model_refusal_no_fallback"`),
		pruneSynthetic("e-r-two", `"error":"invalid_request",`,
			pruneText("API Error: Opus 5.5 can't help with this. Start a new session to continue.")),
	)
	path := writePruneFile(t, lines)
	pruned, hasClean, err := pruneClaudeRefusalFile(path)
	if err != nil || !pruned || !hasClean {
		t.Fatalf("multi-step refusal: pruned=%v hasClean=%v err=%v", pruned, hasClean, err)
	}
	if got := readPruneFile(t, path); got != strings.Join(clean, "\n")+"\n" {
		t.Fatalf("the whole failed turn was not pruned:\n%s", got)
	}
}

// A session that already accumulated refused turns heals all the way back to its last clean one.
func TestPruneConsecutiveRefusedTurns(t *testing.T) {
	clean := cleanTurn("one")
	lines := append([]string{}, clean...)
	lines = append(lines, refusalTurnLines("two")...)
	lines = append(lines, refusalTurnLines("three")...)
	path := writePruneFile(t, lines)
	pruned, hasClean, err := pruneClaudeRefusalFile(path)
	if err != nil || !pruned || !hasClean {
		t.Fatalf("consecutive refusals: pruned=%v hasClean=%v err=%v", pruned, hasClean, err)
	}
	if got := readPruneFile(t, path); got != strings.Join(clean, "\n")+"\n" {
		t.Fatalf("not every trailing refused turn was pruned:\n%s", got)
	}
}

// A queue-operation line right before a prompt is that prompt's: it is cut with the refused turn it led into.
func TestPruneTakesTheQueueOperationPrefix(t *testing.T) {
	clean := cleanTurn("one")
	lines := append([]string{}, clean...)
	lines = append(lines, pruningLine("queue-operation", `"operation":"enqueue","content":"second"`))
	lines = append(lines, refusalTurnLines("two")...)
	path := writePruneFile(t, lines)
	if pruned, _, err := pruneClaudeRefusalFile(path); err != nil || !pruned {
		t.Fatalf("pruned=%v err=%v", pruned, err)
	}
	if got := readPruneFile(t, path); got != strings.Join(clean, "\n")+"\n" {
		t.Fatalf("the queue-operation line stayed:\n%s", got)
	}
}

// A refused first turn has no clean turn to resume to: the file goes, its copy stays.
func TestPruneFirstTurnRemoved(t *testing.T) {
	lines := append([]string{pruningLine("summary", `"summary":"t"`)}, refusalTurnLines("one")...)
	path := writePruneFile(t, lines)
	original := readPruneFile(t, path)
	pruned, hasClean, err := pruneClaudeRefusalFile(path)
	if err != nil || !pruned || hasClean {
		t.Fatalf("first-turn refusal: pruned=%v hasClean=%v err=%v", pruned, hasClean, err)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("the poisoned transcript stayed: %v", err)
	}
	if got := readPruneFile(t, path+refusalBackupSuffix); got != original {
		t.Fatal("the backup is not the original")
	}
}

// A fallback model's 404 and "Prompt is too long" are no refusals to resume past; a safeguards word is.
func TestPruneKeepsOtherSyntheticFailures(t *testing.T) {
	for _, tc := range []struct {
		name string
		turn []string
	}{
		{"a fallback model the gateway does not serve", []string{
			prunePrompt("u-r", "second"),
			pruningLine("system", `"subtype":"model_refusal_fallback"`),
			pruneSynthetic("e-r", `"error":"model_not_found","apiErrorStatus":404,`, pruneText("API Error: 404 fallback-model is not available")),
		}},
		{"prompt too long", []string{
			prunePrompt("u-r", "second"),
			pruneSynthetic("e-r", `"error":"invalid_request",`, pruneText("Prompt is too long")),
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writePruneFile(t, append(cleanTurn("one"), tc.turn...))
			before := readPruneFile(t, path)
			if pruned, hasClean, err := pruneClaudeRefusalFile(path); err != nil || pruned || !hasClean {
				t.Fatalf("pruned=%v hasClean=%v err=%v", pruned, hasClean, err)
			}
			if got := readPruneFile(t, path); got != before {
				t.Fatalf("the transcript changed:\n%s", got)
			}
		})
	}
}

// The safeguard refusal's older words are a refusal; anything else in a fallback turn is the fallback's own.
func TestPruneSyntheticTextRefusal(t *testing.T) {
	lines := append([]string{}, cleanTurn("one")...)
	lines = append(lines,
		prunePrompt("u-r", "second"),
		pruneSynthetic("e-r", ``, pruneText("API Error: opus's safeguards flagged this message")))
	path := writePruneFile(t, lines)
	if pruned, _, err := pruneClaudeRefusalFile(path); err != nil || !pruned {
		t.Fatalf("the safeguard refusal was kept: pruned=%v err=%v", pruned, err)
	}
}

// healClaudeRefusalTranscript clears what the file held for the caches, and a removed first-turn file leaves no path.
func TestHealClearsTheCaches(t *testing.T) {
	n := assistantNode(t)
	path := writePruneFile(t, append(refusalTurnLines("one"), pruningLine("summary", `"s":"x"`)))
	n.paths[pruneID] = path
	n.metas[path] = quickMeta{}
	pruned, hasClean := n.healClaudeRefusalTranscript(pruneID)
	if !pruned || hasClean {
		t.Fatalf("pruned=%v hasClean=%v", pruned, hasClean)
	}
	n.mu.Lock()
	_, hasPath := n.paths[pruneID]
	_, hasMeta := n.metas[path]
	n.mu.Unlock()
	if hasPath || hasMeta {
		t.Fatalf("the caches still hold the removed transcript: path=%v meta=%v", hasPath, hasMeta)
	}
	if got := n.transcriptPath(pruneID); got != "" {
		t.Fatalf("the removed transcript still resolves to %q", got)
	}
}

// A kept refusal leaves everything alone.
func TestHealLeavesACleanSession(t *testing.T) {
	n := assistantNode(t)
	path := writePruneFile(t, cleanTurn("one"))
	n.paths[pruneID] = path
	n.metas[path] = quickMeta{}
	if pruned, _ := n.healClaudeRefusalTranscript(pruneID); pruned {
		t.Fatal("a clean session was pruned")
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	if _, ok := n.metas[path]; !ok {
		t.Fatal("a clean session's meta was cleared")
	}
	if n.paths[pruneID] != path {
		t.Fatal("a clean session's path was cleared")
	}
}

// The send heals before it resumes: a warm but idle process still holds the refused turn (and writes its own
// sessions/<pid>.json lock file), so it is stopped first and the next message starts cold on clean history.
func TestAssistantSendHealsTheRefusalAndRestartsTheProcess(t *testing.T) {
	f := cliTest(t)
	id, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "claude", "first", nil, "", "", cliExec())
	if err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	old := f.proc(id)
	if old == nil || old.cmd == nil || old.cmd.Process == nil {
		t.Fatal("no process for the first message")
	}
	// Real claude -p writes sessions/<pid>.json while warm; stopping old before pruning prevents self-blocking.
	writeClaudeSession(t, f.n.home, old.cmd.Process.Pid, id, "sdk-cli")
	path := filepath.Join(f.dir, id+".jsonl")
	lines := append([]string{}, cleanTurn("kept")...)
	lines = append(lines, refusalTurnLines("refused")...)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.n.mu.Lock()
	f.n.paths[id] = path
	f.n.metas[path] = quickMeta{}
	f.n.mu.Unlock()
	if _, err := f.n.assistantSend(context.Background(), id, "", "claude", "second", nil, "", "", cliExec()); err != nil {
		t.Fatal(err)
	}
	if got := f.proc(id); got == nil || got == old {
		t.Fatal("the process that held the refused turn was not replaced")
	}
	if got := readPruneFile(t, path); got != strings.Join(cleanTurn("kept"), "\n")+"\n" {
		t.Fatalf("the refused turn was not pruned:\n%s", got)
	}
	if _, err := os.Stat(path + refusalBackupSuffix); err != nil {
		t.Fatalf("no backup: %v", err)
	}
	f.idle(t, id)
}

// Immediate model-switch recovery sends only prompts.RenderModelRetry(), which asks the fallback model to finish "the
// request above": that resend must keep the refused turn in the transcript so the fallback model still sees the prompt.
func TestAssistantRecoveryResendKeepsRefusedTurnForFallbackModel(t *testing.T) {
	f := cliTest(t)
	id, err := f.n.assistantSend(context.Background(), "", t.TempDir(), "claude", "first", nil, "", "", cliExec())
	if err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	path := filepath.Join(f.dir, id+".jsonl")
	lines := append([]string{}, cleanTurn("kept")...)
	lines = append(lines, refusalTurnLines("original-refused-request")...)
	wantBefore := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(wantBefore), 0o600); err != nil {
		t.Fatal(err)
	}
	rec := &assistantTurnRecovery{retried: map[string]bool{}, used: map[string]bool{}, running: true}
	f.n.mu.Lock()
	f.n.paths[id] = path
	if f.n.recoveries == nil {
		f.n.recoveries = map[string]*assistantTurnRecovery{}
	}
	f.n.recoveries[id] = rec
	f.n.mu.Unlock()

	rctx := withAssistantRecovery(context.Background(), rec, false)
	nextExec := cliExec()
	nextExec.Model = "fallback-model"
	if _, err := f.n.assistantSend(rctx, id, "", "claude", "continue the request above", nil, "", "", nextExec); err != nil {
		t.Fatal(err)
	}
	f.idle(t, id)
	if got := readPruneFile(t, path); !strings.Contains(got, "original-refused-request") {
		t.Fatalf("recovery resend pruned the prompt the fallback model needed:\n%s", got)
	}
}

// Transcripts larger than the 512 KiB tail peek fast-return when clean and still prune accurately when ending on a refusal.
func TestPruneLargeTranscriptTailFastPath(t *testing.T) {
	bigReply := strings.Repeat("x", 700<<10)
	clean := append([]string{}, cleanTurn("head")...)
	clean = append(clean, prunePrompt("u-big", "big"), pruneAssistant("a-big", `"stop_reason":"end_turn",`, pruneText(bigReply)))
	cleanPath := writePruneFile(t, clean)
	if pruned, hasClean, err := pruneClaudeRefusalFile(cleanPath); err != nil || pruned || !hasClean {
		t.Fatalf("large clean transcript: pruned=%v hasClean=%v err=%v", pruned, hasClean, err)
	}

	refused := append([]string{}, clean...)
	refused = append(refused, refusalTurnLines("tail-refusal")...)
	refusedPath := writePruneFile(t, refused)
	if pruned, hasClean, err := pruneClaudeRefusalFile(refusedPath); err != nil || !pruned || !hasClean {
		t.Fatalf("large refused transcript: pruned=%v hasClean=%v err=%v", pruned, hasClean, err)
	}
	if got := readPruneFile(t, refusedPath); got != strings.Join(clean, "\n")+"\n" {
		t.Fatalf("large refused transcript did not truncate cleanly (len=%d)", len(got))
	}
}
