package node

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// handoffTestValue is the one value staged in these tests: nothing the node sends or logs may carry it.
const handoffTestValue = "synthetic-handoff-value-5b1e9d"

// handoffTestNode is launchKeyTestNode with hand-offs of its own (not the test process's).
func handoffTestNode() *Node {
	n := launchKeyTestNode()
	n.secrets = newSecretStage()
	n.keeper = newAssistantKeeper() // resident: only such a node takes hand-offs
	return n
}

// stageForTest does what the manager does: a check for a key, then the values sealed to it.
func stageForTest(t *testing.T, n *Node, env ...proto.SecretEnvVar) (proto.SecretStageResult, error) {
	t.Helper()
	out, err := n.secretStageCall(json.RawMessage(`{"check":true}`))
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	k := out.(proto.SecretStageResult)
	if k.Key == "" || k.KeyID == "" || k.Ticket != "" {
		t.Fatalf("check answered %+v", k)
	}
	sealed, err := proto.SealSecretEnv(k.Key, k.KeyID, env)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(sealed, []byte(handoffTestValue)) {
		t.Fatal("sealed in the clear")
	}
	raw, _ := json.Marshal(proto.SecretStageParams{KeyID: k.KeyID, Sealed: sealed})
	res, err := n.secretStageCall(raw)
	if err != nil {
		return proto.SecretStageResult{}, err
	}
	return res.(proto.SecretStageResult), nil
}

func envVar(name, value string) proto.SecretEnvVar {
	return proto.SecretEnvVar{Name: name, Value: []byte(value)}
}

// A hand-off is sealed to a one-time key; its values go once, with the machine's launch key, to the first who asks
// with the ticket; nobody after.
func TestSecretStageAndTake(t *testing.T) {
	launchKeyHome(t)
	n := handoffTestNode()
	key, err := ensureLaunchKey(nodeclient.LaunchKeyPath())
	if err != nil {
		t.Fatal(err)
	}
	nine := []proto.SecretEnvVar{}
	for i := range 9 {
		nine = append(nine, envVar(fmt.Sprintf("A%d", i), handoffTestValue))
	}
	for what, env := range map[string][]proto.SecretEnvVar{
		"none":          nil,
		"PATH":          {envVar("PATH", handoffTestValue)},
		"a loader's":    {envVar("LD_PRELOAD", handoffTestValue)},
		"a mac loader":  {envVar("DYLD_INSERT_LIBRARIES", handoffTestValue)},
		"Persona's":     {envVar("PERSONA_HOME", handoffTestValue)},
		"the Agent's":   {envVar("ANTHROPIC_BASE_URL", handoffTestValue)},
		"a proxy":       {envVar("HTTPS_PROXY", handoffTestValue)},
		"git's command": {envVar("GIT_SSH_COMMAND", handoffTestValue)},
		"an editor":     {envVar("EDITOR", handoffTestValue)},
		"lower case":    {envVar("github_token", handoffTestValue)},
		"too short":     {envVar("GITHUB_TOKEN", "fifteen-bytes!!")},
		"a NUL":         {envVar("GITHUB_TOKEN", "abcdefghijklmnop\x00q")},
		"twice":         {envVar("A_TOKEN", handoffTestValue), envVar("A_TOKEN", handoffTestValue)},
		"nine":          nine,
		// a value where the name goes: never said back
		"swapped": {envVar(handoffTestValue, "GITHUB_TOKEN-is-the-name")},
	} {
		if _, err := stageForTest(t, n, env...); err == nil || strings.Contains(err.Error(), handoffTestValue) {
			t.Errorf("%s: %v", what, err)
		}
	}

	// A key works once, and only for what was sealed to it.
	out, _ := n.secretStageCall(json.RawMessage(`{"check":true}`))
	k := out.(proto.SecretStageResult)
	other, _ := n.secretStageCall(json.RawMessage(`{"check":true}`))
	sealed, _ := proto.SealSecretEnv(other.(proto.SecretStageResult).Key, other.(proto.SecretStageResult).KeyID, []proto.SecretEnvVar{envVar("GITHUB_TOKEN", handoffTestValue)})
	raw, _ := json.Marshal(proto.SecretStageParams{KeyID: k.KeyID, Sealed: sealed})
	if _, err := n.secretStageCall(raw); err == nil {
		t.Fatal("opened with another key")
	}
	raw, _ = json.Marshal(proto.SecretStageParams{KeyID: k.KeyID, Sealed: sealed}) // the node zeroed the first
	if _, err := n.secretStageCall(raw); err == nil || !strings.Contains(err.Error(), "used") {
		t.Fatalf("the key again: %v", err)
	}
	if _, err := n.secretStageCall(json.RawMessage(`{"env":[{"name":"GITHUB_TOKEN","value":"` + base64.StdEncoding.EncodeToString([]byte(handoffTestValue)) + `"}]}`)); err == nil {
		t.Fatal("values in the clear were taken")
	}

	res, err := stageForTest(t, n, envVar("GITHUB_TOKEN", handoffTestValue))
	if err != nil || len(res.Ticket) != 64 || time.Until(res.ExpiresAt) > proto.SecretStageKeep || time.Until(res.ExpiresAt) < time.Minute {
		t.Fatalf("staged: %+v %v", res, err)
	}
	ask := func(ticket, k string) map[string]any {
		return n.secretEnvFor(sockRequest{Kind: "secret.env", Ticket: ticket, LaunchKey: k})
	}
	for what, k := range map[string]string{"no key": "", "another key": strings.Repeat("0", launchKeyLen)} {
		if resp := ask(res.Ticket, k); resp["error"] == nil || resp["env"] != nil {
			t.Fatalf("%s: %v", what, resp)
		}
	}
	if !n.handoffs().has(res.Ticket) {
		t.Fatal("a refused caller used the ticket up")
	}
	// Fetched before a start named it: refused, and gone (whoever asked was not that start).
	if resp := ask(res.Ticket, key); resp["error"] == nil || resp["env"] != nil || n.handoffs().has(res.Ticket) {
		t.Fatalf("fetched before its start: %v", resp)
	}
	res, _ = stageForTest(t, n, envVar("GITHUB_TOKEN", handoffTestValue))
	if !n.handoffs().reserve(res.Ticket) || n.handoffs().reserve(res.Ticket) || n.handoffs().has(res.Ticket) {
		t.Fatal("reserved once, and only once")
	}
	if resp := ask(res.Ticket, key); fmtEnv(resp) != "GITHUB_TOKEN="+handoffTestValue {
		t.Fatalf("with the key and the ticket: %v", resp)
	}
	if resp := ask(res.Ticket, key); resp["error"] == nil || resp["env"] != nil {
		t.Fatalf("a second time: %v", resp)
	}
	if resp := ask("not-a-ticket", key); resp["error"] == nil {
		t.Fatalf("an unknown ticket: %v", resp)
	}

	// Not used in time: gone.
	res, _ = stageForTest(t, n, envVar("GITHUB_TOKEN", handoffTestValue))
	s := n.handoffs()
	s.mu.Lock()
	st := s.staged[res.Ticket]
	st.expires = time.Now().Add(-time.Second)
	s.staged[res.Ticket] = st
	s.mu.Unlock()
	if n.handoffs().has(res.Ticket) || n.handoffs().reserve(res.Ticket) {
		t.Fatal("expired, still good")
	}
	if resp := ask(res.Ticket, key); resp["error"] == nil {
		t.Fatalf("expired: %v", resp)
	}
}

func fmtEnv(resp map[string]any) string {
	env, _ := resp["env"].([]string)
	return strings.Join(env, " ")
}

// Through the real socket, with what personad launch --ticket sends.
func TestSecretEnvOverTheSocket(t *testing.T) {
	launchKeyHome(t)
	n := handoffTestNode()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ln, err := n.listenOnce(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	res, err := stageForTest(t, n, envVar("GITHUB_TOKEN", handoffTestValue), envVar("NPM_TOKEN", handoffTestValue+"-2"))
	if err != nil {
		t.Fatal(err)
	}
	n.handoffs().reserve(res.Ticket) // its start named it
	env, err := fetchSecretEnv(ctx, res.Ticket)
	if err != nil || strings.Join(env, " ") != "GITHUB_TOKEN="+handoffTestValue+" NPM_TOKEN="+handoffTestValue+"-2" {
		t.Fatalf("fetched: %v %v", env, err)
	}
	if _, err := fetchSecretEnv(ctx, res.Ticket); err == nil || !strings.Contains(err.Error(), "approve it again") {
		t.Fatalf("again: %v", err)
	}
}

func jsonLine(v any, html bool) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(html)
	_ = enc.Encode(v)
	return b.Bytes()
}

// What the node sends has every handed-over value hidden in its string values, never in keys, and stays valid JSON.
func TestSecretScrubberHidesValuesInLines(t *testing.T) {
	s := &secretScrubber{}
	plain := []byte(`{"method":"session.items","params":{"items":[{"text":"nothing here"}]}}` + "\n")
	if got := s.line(plain); !bytes.Equal(got, plain) {
		t.Fatalf("nothing to hide: %s", got)
	}
	value := "tok<en>&" + handoffTestValue + `"quoted"\back`
	s.add([]byte(value))
	for _, html := range []bool{false, true} {
		for what, v := range map[string]any{
			"a transcript item": map[string]any{"method": "session.items", "params": map[string]any{"items": []any{map[string]any{"text": "the token is " + value + " (from the env)"}}}},
			"a card's input":    map[string]any{"method": "hook.event", "params": map[string]any{"tool_input": map[string]any{"command": "curl -H 'Authorization: " + value + "'"}}},
			"a screen":          map[string]any{"result": map[string]any{"text": "$ echo $TOKEN\n" + value + "\n$ "}},
			"next to escapes":   map[string]any{"result": map[string]any{"text": "\n\t" + value + "\\\n"}},
			"twice":             map[string]any{"result": map[string]any{"a": value, "b": value + value}},
		} {
			got := s.line(jsonLine(v, html))
			if !json.Valid(bytes.TrimSpace(got)) {
				t.Errorf("%s (html %v): not JSON any more: %s", what, html, got)
			}
			if bytes.Contains(got, []byte(handoffTestValue)) || !bytes.Contains(got, []byte(proto.Redacted)) {
				t.Errorf("%s (html %v): %s", what, html, got)
			}
		}
	}

	// Its base64 becomes base64 of the same length: text shows no value, a payload around it still decodes.
	b64 := base64.StdEncoding.EncodeToString([]byte(value))
	got := s.line(jsonLine(map[string]any{"text": "printf %s $TOKEN | base64 → " + b64}, false))
	if bytes.Contains(got, []byte(b64)) || !json.Valid(bytes.TrimSpace(got)) {
		t.Fatalf("base64 in text: %s", got)
	}
	payload := base64.StdEncoding.EncodeToString(append([]byte("abc"), []byte(value)...)) // aligned: the value's own base64 inside
	got = s.line(jsonLine(map[string]any{"data": payload}, false))
	var back struct{ Data string }
	if err := json.Unmarshal(got, &back); err != nil {
		t.Fatal(err)
	}
	if dec, err := base64.StdEncoding.DecodeString(back.Data); err != nil || bytes.Contains(dec, []byte(handoffTestValue)) {
		t.Fatalf("a base64 payload: %v %q", err, dec)
	}

	// Never a key: a value that is also a key name stays as a key.
	s2 := &secretScrubber{}
	s2.add([]byte("a-sixteen-byte-key-name"))
	got = s2.line([]byte(`{"a-sixteen-byte-key-name": "a-sixteen-byte-key-name"}`))
	if string(got) != `{"a-sixteen-byte-key-name": "`+proto.Redacted+`"}` {
		t.Fatalf("a key: %s", got)
	}

	// A match starts a character: never inside an escape (\n, \uXXXX), so the line stays valid.
	s3 := &secretScrubber{}
	s3.add([]byte("nsynthetic-value-0123"))
	s3.add([]byte("00e9synthetic-value-4567"))
	line := jsonLine(map[string]any{"text": "a\nsynthetic-value-0123 and nsynthetic-value-0123, ésynthetic-value-4567 or 00e9synthetic-value-4567"}, false)
	line = bytes.ReplaceAll(line, []byte("é"), []byte(`é`))
	got = s3.line(line)
	if !json.Valid(bytes.TrimSpace(got)) || !strings.Contains(string(got), `a\nsynthetic-value-0123 and `+proto.Redacted) ||
		!strings.Contains(string(got), `ésynthetic-value-4567 or `+proto.Redacted) {
		t.Fatalf("escape boundaries: %s", got)
	}
	// A value ending in a backslash cannot eat half of an escape.
	s4 := &secretScrubber{}
	s4.add([]byte(`Tr0ub4dor3-xyz-pw\`))
	got = s4.line(jsonLine(map[string]any{"text": `pw is Tr0ub4dor3-xyz-pw\" ok`}, false))
	if !json.Valid(bytes.TrimSpace(got)) {
		t.Fatalf("a trailing backslash: %s", got)
	}
}

// The process keeps the last secretScrubMost values, the oldest going first, whole.
func TestSecretScrubberKeepsTheNewest(t *testing.T) {
	s := &secretScrubber{}
	for i := range secretScrubMost + 1 {
		s.add([]byte(fmt.Sprintf("synthetic-value-%04d-padding", i)))
	}
	if got := s.line(jsonLine(map[string]any{"a": "synthetic-value-0000-padding"}, false)); bytes.Contains(got, []byte(proto.Redacted)) {
		t.Fatalf("the oldest is still hidden: %s", got)
	}
	for _, i := range []int{1, secretScrubMost} {
		v := fmt.Sprintf("synthetic-value-%04d-padding", i)
		if got := s.line(jsonLine(map[string]any{"a": v, "b": base64.StdEncoding.EncodeToString([]byte(v))}, false)); bytes.Contains(got, []byte(v)) || bytes.Contains(got, []byte(base64.StdEncoding.EncodeToString([]byte(v)))) {
			t.Fatalf("%d: %s", i, got)
		}
	}
}

// The connection's line filter hides values only once there are some, framed or not.
func TestScrubFilter(t *testing.T) {
	s := &secretScrubber{}
	line := []byte(`{"result":{"text":"` + handoffTestValue + `"}}` + "\n")
	if got := s.filter(line); string(got) != string(line) {
		t.Fatalf("nothing staged: %s", got)
	}
	s.add([]byte(handoffTestValue))
	if got := s.filter(line); strings.Contains(string(got), handoffTestValue) || !strings.HasSuffix(string(got), "}}\n") {
		t.Fatalf("staged: %s", got)
	}
	for _, framed := range []bool{false, true} {
		var out syncBuffer
		p := rpc.New("t", &out)
		p.LineFilter = s.filter
		_, end := servePeer(t, p, framed)
		_ = p.Notify("session.items", map[string]any{"items": []any{map[string]any{"text": handoffTestValue}}})
		end()
		if !strings.Contains(out.String(), "session.items") || strings.Contains(out.String(), handoffTestValue) {
			t.Fatalf("framed=%t sent: %q", framed, out.String())
		}
	}
}

// Staging hides the values in what the node sends from then on, a terminal frame's base64 included, at any offset.
func TestStagedValuesAreHidden(t *testing.T) {
	launchKeyHome(t)
	n := handoffTestNode()
	if _, err := stageForTest(t, n, envVar("GITHUB_TOKEN", handoffTestValue)); err != nil {
		t.Fatal(err)
	}
	if got := n.scrubBytes([]byte("out: " + handoffTestValue)); string(got) != "out: "+proto.Redacted {
		t.Fatalf("terminal bytes: %q", got)
	}
	for _, lead := range []string{"", "a", "ab"} { // every base64 alignment
		frame := base64.StdEncoding.EncodeToString([]byte("\x1b[1m" + lead + handoffTestValue + "\r\n"))
		dec, err := base64.StdEncoding.DecodeString(n.scrubB64(frame))
		if err != nil || bytes.Contains(dec, []byte(handoffTestValue)) || !bytes.Contains(dec, []byte(lead+proto.Redacted)) {
			t.Fatalf("frame with %q in front: %v %q", lead, err, dec)
		}
	}
	var out bytes.Buffer
	p := rpc.New("t", &out)
	p.LineFilter = n.handoffs().scrub.filter
	_ = p.Notify("session.items", map[string]any{"items": []any{map[string]any{"text": handoffTestValue}}})
	if strings.Contains(out.String(), handoffTestValue) {
		t.Fatalf("sent: %s", out.String())
	}
}

// Every connection's Node of a process shares its hand-offs: a reconnect keeps the values hidden.
func TestHandoffsOutliveAConnection(t *testing.T) {
	a, b := &Node{}, &Node{}
	if a.handoffs() != b.handoffs() {
		t.Fatal("two connections, two stages")
	}
}

// Only a new Claude Code start whose command personad can exec takes a ticket, and only one staged and no other
// start's; its command line carries the ticket, never a value, and runs the agent through personad launch.
func TestSecretStartCommand(t *testing.T) {
	launchKeyHome(t)
	n := handoffTestNode()
	n.bin = "/opt/persona/personad"
	n.setManaged([]proto.ManagedLauncher{{Name: "gw", BaseURL: "http://gw.example.com", Model: "m1", APIKey: launchKeyTestSecret},
		{Name: "cx", Agent: "codex", BaseURL: "http://gw.example.com", Model: "m1", APIKey: launchKeyTestSecret}})
	res, err := stageForTest(t, n, envVar("GITHUB_TOKEN", handoffTestValue))
	if err != nil {
		t.Fatal(err)
	}
	code := func(err error) string {
		var re *rpc.Error
		if errors.As(err, &re) {
			return re.Code
		}
		return ""
	}
	for what, err := range map[string]error{
		"codex":                    n.secretStartProblem("codex", "codex", "", res.Ticket),
		"a Codex managed launcher": n.secretStartProblem("claude", "cx", "", res.Ticket),
		"a resume":                 n.secretStartProblem("claude", "gw", "11111111-2222-3333-4444-555555555555", res.Ticket),
		"an rc function":           n.secretStartProblem("claude", "myclaude", "", res.Ticket),
		"not staged":               n.secretStartProblem("claude", "gw", "", "nope"),
		"omp":                      n.secretStartProblem(ompAgent, ompAgent, "", res.Ticket),
	} {
		if code(err) == "" {
			t.Errorf("%s: %v", what, err)
		}
	}
	if c := code(n.secretStartProblem("claude", "gw", "", "nope")); c != secretEnvGoneCode {
		t.Errorf("not staged: %q", c)
	}
	if err := n.secretStartProblem("claude", "gw", "", res.Ticket); err != nil {
		t.Fatalf("gw: %v", err)
	}
	if code(n.secretStartProblem("claude", "claude", "", res.Ticket)) != secretEnvGoneCode {
		t.Fatal("a second start took the same ticket")
	}

	spec := claudeLaunch{Launcher: "gw"}
	line := n.withSecretTicket(n.claudeCommandLine(spec, "--session-id x", shellQuote), "gw", res.Ticket, shellQuote)
	if !strings.HasPrefix(line, "/opt/persona/personad launch --ticket "+res.Ticket+" gw --session-id x") || strings.Contains(line, handoffTestValue) {
		t.Fatalf("managed: %s", line)
	}
	spec.Launcher = "claude"
	line = n.withSecretTicket(n.claudeCommandLine(spec, "--session-id x", shellQuote), "claude", res.Ticket, shellQuote)
	if !strings.HasPrefix(line, "/opt/persona/personad launch --ticket "+res.Ticket+" -- claude --session-id x") {
		t.Fatalf("plain claude: %s", line)
	}
	dir := "~/.claude-work"
	n.claudeStartDir.Store(&dir)
	line = n.withSecretTicket(n.claudeCommandLine(spec, "--session-id x", shellQuote), "claude", res.Ticket, shellQuote)
	if !strings.HasPrefix(line, `CLAUDE_CONFIG_DIR="$HOME"/.claude-work /opt/persona/personad launch --ticket `+res.Ticket+" -- claude --session-id x") {
		t.Fatalf("plain claude with its directory: %s", line)
	}
	if err := LaunchWithSecrets(context.Background(), res.Ticket, []string{"sh", "-c", "env"}); err == nil || !strings.Contains(err.Error(), "only -- claude") {
		t.Fatalf("another command: %v", err)
	}
}

// A value handed over again is the newest: it stays hidden after secretScrubMost others.
func TestSecretScrubberMovesAValueHandedAgain(t *testing.T) {
	s := &secretScrubber{}
	first := "synthetic-value-first-padding"
	s.add([]byte(first))
	for i := range secretScrubMost - 1 {
		s.add([]byte(fmt.Sprintf("synthetic-value-%04d-padding", i)))
	}
	s.add([]byte(first)) // again
	s.add([]byte("synthetic-value-newer-padding"))
	if got := s.line(jsonLine(map[string]any{"a": first}, false)); bytes.Contains(got, []byte(first)) {
		t.Fatalf("handed again, then dropped: %s", got)
	}
	if got := s.line(jsonLine(map[string]any{"a": "synthetic-value-0000-padding"}, false)); !bytes.Contains(got, []byte("synthetic-value-0000-padding")) {
		t.Fatalf("the least recent should have gone: %s", got)
	}
}

// A value of several lines (a PEM key) is found as a terminal shows it, with CRLF, and in a transcript, with LF.
func TestSecretScrubberMultiLineValues(t *testing.T) {
	s := &secretScrubber{}
	pem := "-----BEGIN SYNTHETIC KEY-----\nAAAAsyntheticline1\nBBBBsyntheticline2\n-----END SYNTHETIC KEY-----"
	s.add([]byte(pem))
	crlf := strings.ReplaceAll(pem, "\n", "\r\n")
	if got := s.bytes([]byte("$ cat key\r\n" + crlf + "\r\n$ ")); bytes.Contains(got, []byte("AAAAsyntheticline1")) {
		t.Fatalf("terminal: %q", got)
	}
	if got := s.line(jsonLine(map[string]any{"text": "key:\n" + pem}, false)); bytes.Contains(got, []byte("AAAAsyntheticline1")) {
		t.Fatalf("transcript: %s", got)
	}
}

// Whatever the value and wherever it sits, the line stays valid JSON and no string value carries it.
func TestSecretScrubberKeepsLinesValid(t *testing.T) {
	r := rand.New(rand.NewPCG(1, 2))
	alphabet := []rune("ab01/+=_-\"\\<>&\u2028\n\t éu")
	word := func(n int) string {
		var b strings.Builder
		for range n {
			b.WriteRune(alphabet[r.IntN(len(alphabet))])
		}
		return b.String()
	}
	for range 2000 {
		value := "v" + word(16+r.IntN(12))
		s := &secretScrubber{}
		s.add([]byte(value))
		texts := []any{}
		for range 1 + r.IntN(4) {
			texts = append(texts, word(r.IntN(6))+value+word(r.IntN(6)), word(r.IntN(10)))
		}
		v := map[string]any{"method": "session.items", word(3): texts, "b64": base64.StdEncoding.EncodeToString([]byte(word(2) + value))}
		got := s.line(jsonLine(v, r.IntN(2) == 0))
		var back any
		if err := json.Unmarshal(got, &back); err != nil {
			t.Fatalf("value %q: not JSON: %s", value, got)
		}
		var walk func(any)
		walk = func(x any) {
			switch x := x.(type) {
			case string:
				if strings.Contains(x, value) {
					t.Fatalf("value %q left in %q", value, x)
				}
			case []any:
				for _, e := range x {
					walk(e)
				}
			case map[string]any:
				for _, e := range x {
					walk(e)
				}
			}
		}
		walk(back)
	}
}

// secret_env comes only from a resident node: a node that serves one connection forgets what it hides.
func TestSecretEnvOnlyFromAResidentNode(t *testing.T) {
	if caps := (&Node{}).capabilities(); slices.Contains(caps, nodeproto.SecretEnv) || !slices.Contains(caps, nodeproto.SecretRun) {
		t.Fatalf("one connection's node: %v", caps)
	}
	if caps := (&Node{keeper: newAssistantKeeper()}).capabilities(); !slices.Contains(caps, nodeproto.SecretEnv) {
		t.Fatalf("a resident node: %v", caps)
	}
}

// BenchmarkSecretScrubberLine is one 1 MiB line holding a value, with 64 values kept.
func BenchmarkSecretScrubberLine(b *testing.B) {
	s := &secretScrubber{}
	for i := range secretScrubMost {
		s.add([]byte(fmt.Sprintf("synthetic-value-%04d-padding", i)))
	}
	text := strings.Repeat("ordinary transcript text, nothing secret here. ", 1<<20/48) + "synthetic-value-0042-padding"
	line := jsonLine(map[string]any{"result": map[string]any{"text": text}}, false)
	b.SetBytes(int64(len(line)))
	for b.Loop() {
		_ = s.line(line)
	}
}

// A node that serves one connection takes no hand-off: it would forget what it hides with that connection.
func TestSecretStageNeedsAResidentNode(t *testing.T) {
	n := handoffTestNode()
	n.keeper = nil
	if _, err := n.secretStageCall(json.RawMessage(`{"check":true}`)); err == nil || !strings.Contains(err.Error(), "resident") {
		t.Fatalf("one connection's node: %v", err)
	}
}

// A value that had CRLF itself, printed to a terminal (each LF becomes CRLF): CR CR LF.
func TestSecretScrubberCRLFValueInATerminal(t *testing.T) {
	s := &secretScrubber{}
	v := "-----BEGIN SYNTHETIC-----\r\nAAAAsyntheticline1\r\n-----END SYNTHETIC-----"
	s.add([]byte(v))
	shown := strings.ReplaceAll(v, "\n", "\r\n")
	if got := s.bytes([]byte("$ cat key\r\n" + shown + "\r\n")); bytes.Contains(got, []byte("AAAAsyntheticline1")) {
		t.Fatalf("terminal: %q", got)
	}
}
