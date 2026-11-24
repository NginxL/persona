package extmgr

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extension"
	pserve "github.com/nwverse/persona/server/internal/extension/serve"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Taking back what an extension's program sent (docs/extensions/connectors.md "Sent as the user"), end to end: a real extension
// (this test binary run as its program, as PERSONA_TEST_PLUGIN=recall) installed through the hub's routes, rows written
// the three ways the ledger gets them, and RecallSent asking the program.

// serveRecallExtensionForTest is the program of an extension whose connector sends (actions.do answers the message ref the
// arguments name) and can take a message back: it writes the params of every recall as they came to PERSONA_TEST_RECALL_LOG,
// and answers by the message ref.
func serveRecallExtensionForTest() {
	logFile := os.Getenv("PERSONA_TEST_RECALL_LOG")
	_ = pserve.Serve(context.Background(), os.Stdin, os.Stdout, pserve.Program{ID: os.Getenv("PERSONA_TEST_RECALL_ID"), Version: cmp.Or(os.Getenv("PERSONA_TEST_RECALL_VERSION"), "1.0.0"),
		Namespaces: map[string]pserve.Handler{"connector": func(ctx context.Context, method string, params json.RawMessage) (any, error) {
			id := pserve.Contribution(params)
			switch method {
			case "status":
				return map[string]string{"state": "ok"}, nil
			case "tools.list":
				return map[string]any{"tools": []any{}}, nil
			case "actions.list":
				return map[string]any{"tools": []any{map[string]any{"name": id + "_send", "description": "Send a message", "input_schema": map[string]any{"type": "object"}}}}, nil
			case "actions.describe":
				return map[string]any{"action": map[string]any{"name": id + "_send", "visible": "Example"}, "summary": "send"}, nil
			case "actions.do":
				var p struct {
					Args struct {
						Ref string `json:"ref"`
					} `json:"args"`
				}
				_ = json.Unmarshal(params, &p)
				return map[string]string{"text": "sent", "message_ref": p.Args.Ref}, nil
			case "recall":
				if f, err := os.OpenFile(logFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
					_, _ = f.Write(append(append([]byte{}, params...), '\n'))
					f.Close()
				}
				var p struct {
					MessageRef string `json:"message_ref"`
				}
				_ = json.Unmarshal(params, &p)
				switch p.MessageRef {
				case "ok":
					return map[string]bool{"recalled": true}, nil
				case "cannot":
					return nil, &rpc.Error{Code: "not_recallable", Message: "too old for the service"}
				case "unknown":
					return nil, &rpc.Error{Code: "recall_unknown", Message: "the service did not answer"}
				case "refused":
					return nil, errors.New("the service said no")
				}
				return nil, errors.New("unexpected message ref " + p.MessageRef)
			}
			return nil, pserve.UnknownMethod(method)
		}}})
}

// recallPkg is an extension package for the recall tests.
type recallPkg struct {
	id, conn  string   // the extension's id and its connector's
	gate      string   // the command of its assistant gate, "" for none: not the assistant contribution's id, which is the extension's own
	moreGates []string // the commands of further assistant gates of the same extension, one contribution each
	version   string
	recall    bool // its connector declares recall
	gateOnly  bool // no program and no connector: only the assistant's skill and gate
	log       string
	// gateConnector is the connector the gate file says its gate is for ("connector"): the extension's own when empty.
	gateConnector string
}

func (o recallPkg) write(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	self, _ := os.Executable()
	_ = os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	script := fmt.Sprintf("#!/bin/sh\nPERSONA_TEST_PLUGIN=recall PERSONA_TEST_RECALL_ID=%s PERSONA_TEST_RECALL_VERSION=%s PERSONA_TEST_RECALL_LOG=%s exec %s \"$@\"\n",
		o.id, cmp.Or(o.version, "1.0.0"), o.log, self)
	_ = os.WriteFile(filepath.Join(dir, "bin", "program"), []byte(script), 0o755)
	caps := `"status", "actions"`
	if o.recall {
		caps += `, "recall"`
	}
	assistant := ""
	if o.gate != "" {
		// One assistant contribution per gate: the first as before, one more for each of moreGates.
		var contributions []string
		for i, command := range append([]string{o.gate}, o.moreGates...) {
			skill, gateFile, id := "skill-"+o.id, "gate.json", "assistant-"+o.id // another extension's skills and contributions are not this one's, and a command can be shared
			if i > 0 {
				skill, gateFile, id = fmt.Sprintf("skill-%s-%d", o.id, i+1), fmt.Sprintf("gate%d.json", i+1), fmt.Sprintf("assistant%d-%s", i+1, o.id)
			}
			_ = os.MkdirAll(filepath.Join(dir, "skills", skill, "scripts"), 0o755)
			_ = os.WriteFile(filepath.Join(dir, "skills", skill, "SKILL.md"), []byte("---\nname: "+skill+"\ndescription: Call the example API.\n---\nUse it.\n"), 0o644)
			_ = os.WriteFile(filepath.Join(dir, "skills", skill, "scripts", "x.sh"), []byte("#!/bin/bash\n"), 0o755)
			_ = os.WriteFile(filepath.Join(dir, gateFile), []byte(fmt.Sprintf(`{"command": %q, "script": "%s/scripts/x.sh", "title": "示例",
			"connector": %q, "env": ["X_BASE"], "base_env": "X_BASE", "rules": [{"method": "GET", "path": "**", "action": "allow"}]}`, command, skill, cmp.Or(o.gateConnector, o.conn))), 0o644)
			contributions = append(contributions, fmt.Sprintf(`{"id": %q, "skills": ["skills/%s"], "gate": %q}`, id, skill, gateFile))
		}
		assistant = `"assistant": [` + strings.Join(contributions, ", ") + `]`
	}
	contributes := fmt.Sprintf(`"connectors": [{"id": %q, "name": "Example %s", "capabilities": [%s], "sends_as_user": true}]`, o.conn, o.id, caps)
	runtimeAndActivation := fmt.Sprintf(`"runtime": {"command": {%q: "bin/program"}}, "activation_events": ["on_startup"],`, runtime.GOOS+"-"+runtime.GOARCH)
	if o.gateOnly {
		contributes, runtimeAndActivation = assistant, ""
	} else if assistant != "" {
		contributes += ", " + assistant
	}
	manifest := fmt.Sprintf(`{"manifest_version": 1, "id": %q, "name": "Example %s", "version": %q,
		"engines": {"plugin_protocol": 1}, %s "contributes": {%s}}`, o.id, o.id, cmp.Or(o.version, "1.0.0"), runtimeAndActivation, contributes)
	_ = os.WriteFile(filepath.Join(dir, extension.ManifestFile), []byte(manifest), 0o644)
	return dir
}

// recallExtStore is a hub store with the extensions' rows and the ledger, both in memory.
type recallExtStore struct {
	*extensionStore
	led *sentLedgerStore
}

func (s recallExtStore) RecordSentAsUser(ctx context.Context, r db.SentAsUser) (int64, error) {
	return s.led.RecordSentAsUser(ctx, r)
}
func (s recallExtStore) SentAsUsers(ctx context.Context, before int64, limit int, c string) ([]db.SentAsUser, int64, error) {
	return s.led.SentAsUsers(ctx, before, limit, c)
}
func (s recallExtStore) PruneSentAsUser(ctx context.Context, before time.Time) (int64, error) {
	return s.led.PruneSentAsUser(ctx, before)
}
func (s recallExtStore) SentAsUserByID(ctx context.Context, id int64) (db.SentAsUser, bool, error) {
	return s.led.SentAsUserByID(ctx, id)
}
func (s recallExtStore) MarkSentAsUserRecalled(ctx context.Context, id int64, at time.Time) (bool, error) {
	return s.led.MarkSentAsUserRecalled(ctx, id, at)
}

// recallHub is a hub that runs extensions, lets every write of a connector through the gate and keeps the ledger, with a
// computer "box" connected for the node's gate proxy to report from.
func recallHub(t *testing.T) (*Manager, *sentLedgerStore) {
	t.Helper()
	h := newTestManager(t)
	led := newSentLedgerStore()
	h.store = recallExtStore{newExtensionStore(), led}
	h.connectors = h.newConnectors([]connector.Rule{{Connector: "*", Action: "*", Decision: connector.Allow}})
	box := newMachine(h, MachineConfig{ID: "box", Name: "box"})
	h.machines, h.byID["box"] = append(h.machines, box), box
	h.extensions = newExtensions(h.cfg.dir, nil, h.logf)
	h.extensions.sandboxes.noDefault = true // test programs live outside their packages (extension_sandbox_test.go)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		for _, p := range h.extensions.reg.List() {
			if hp := h.extensions.get(p.Record.ID); hp != nil && hp.proc != nil {
				hp.proc.Stop()
			}
		}
	})
	h.startConnectors(ctx)
	h.startExtensions(ctx)
	return h, led
}

func hasPermission(lines any, key string) bool {
	list, _ := lines.([]any)
	for _, l := range list {
		if m, _ := l.(map[string]any); m["key"] == key {
			return true
		}
	}
	return false
}

func waitConnectorOK(t *testing.T, h *Manager, id string) {
	t.Helper()
	waitLong(t, "the connector "+id, func() bool {
		c := h.connectors.Get(id)
		return c != nil && connector.StatusOf(context.Background(), c).State == connector.StateOK
	})
}

// installRecall installs the package through the routes (what the user agrees to is what the preview showed) and waits
// for its connector. It returns the preview.
func installRecall(t *testing.T, h *Manager, pkg, conn string) map[string]any {
	t.Helper()
	mux := h.routes()
	code, pv := call(t, mux, "POST", "/api/v1/plugins/-/preview", h.cfg.Token, `{"source": "dir:`+pkg+`"}`)
	if code != 200 {
		t.Fatalf("preview: %d %v", code, pv)
	}
	if code, out := call(t, mux, "POST", "/api/v1/plugins/-/install", h.cfg.Token, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"])); code != 200 {
		t.Fatalf("install: %d %v", code, out)
	}
	if conn != "" { // an extension with no connector has nothing to wait for
		waitConnectorOK(t, h, conn)
	}
	return pv
}

// asksOf is the params of each connector.recall the program wrote down.
func asksOf(t *testing.T, logFile string) []map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(logFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var out []map[string]json.RawMessage
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("a log line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

func rawString(m map[string]json.RawMessage, k string) string {
	var s string
	_ = json.Unmarshal(m[k], &s)
	return s
}

// sendThrough writes a row the way a gated write does: the extension's connector asked to send, the message ref its
// program gives being ref. It returns the row's id.
func sendThrough(t *testing.T, h *Manager, led *sentLedgerStore, conn, ref string) int64 {
	t.Helper()
	args := fmt.Sprintf(`{"chat":"c1","text":"hello","ref":%q}`, ref)
	if r := h.callConnector(context.Background(), asker{}, conn+"_send", json.RawMessage(args)); r.IsError {
		t.Fatalf("send: %+v", r)
	}
	rows := led.all()
	return rows[len(rows)-1].ID
}

// reportedByGate writes a row the way the node's gate proxy reports a write (lark.sent): connector lark, the gate's command.
func reportedByGate(t *testing.T, h *Manager, led *sentLedgerStore, command, ref string) int64 {
	t.Helper()
	s := proto.LarkSent{Command: command, Method: "POST", Path: "/conversations/c3/messages", MessageRef: ref,
		LetThrough: proto.LarkLetThrough{Kind: proto.LarkThroughAlways}}
	if _, err := h.RecordNodeSent(context.Background(), "box", larkSentParams(t, s, nil)); err != nil {
		t.Fatal(err)
	}
	rows := led.all()
	return rows[len(rows)-1].ID
}

// recordedBy writes a row the way an extension reports its own send (sent.record).
func recordedBy(t *testing.T, h *Manager, led *sentLedgerStore, ext, conn, ref string) int64 {
	t.Helper()
	params := fmt.Sprintf(`{"contribution":%q,"target":{"chat":"c2"},"text":"hi","message_ref":%q}`, conn, ref)
	if _, err := h.extensions.extensionSentRecord(context.Background(), ext, json.RawMessage(params)); err != nil {
		t.Fatal(err)
	}
	rows := led.all()
	return rows[len(rows)-1].ID
}

func recalledAtOf(led *sentLedgerStore, id int64) *time.Time {
	row, _, _ := led.SentAsUserByID(context.Background(), id)
	return row.RecalledAt
}

// A row of each of the ledger's three sources is taken back by asking the extension that provides its connector: the
// user is asked first, the program is given the row's action, message ref, target and time (and the connector it is for),
// and only its yes writes recalled_at; a second recall finds it done and does not ask the program again.
func TestRecallSentAsksTheExtensionOfEachSource(t *testing.T) {
	needsLarkModule(t)
	h, led := recallHub(t)
	logFile := filepath.Join(t.TempDir(), "recalls.log")
	pkg := recallPkg{id: "ext-a", conn: "lark", gate: "examplecli", recall: true, log: logFile}.write(t)
	pv := installRecall(t, h, pkg, "lark")
	if !hasPermission(pv["permissions"], "recalls_sent") {
		t.Fatalf("what it can take back is shown before installing: %v", pv["permissions"])
	}

	rows := []struct {
		name   string
		id     int64
		action string
	}{
		{"a write through the gate", sendThrough(t, h, led, "lark", "ok"), "lark_send"},
		{"a send the extension recorded", recordedBy(t, h, led, "ext-a", "lark", "ok"), "sent.record"},
		{"a write the node's gate reports", reportedByGate(t, h, led, "examplecli", "ok"), "POST /conversations/c3/messages"},
	}
	for asked, row := range rows {
		confirmed := 0
		got, err := h.RecallSent(context.Background(), row.id, func(context.Context, db.SentAsUser) error {
			confirmed++
			if n := len(asksOf(t, logFile)); n != asked {
				t.Errorf("%s: the program was asked before the user agreed", row.name)
			}
			return nil
		})
		if err != nil || got.RecalledAt == nil || recalledAtOf(led, row.id) == nil || confirmed != 1 {
			t.Fatalf("%s: %v, marked %v, confirmed %d", row.name, err, recalledAtOf(led, row.id), confirmed)
		}
		asks := asksOf(t, logFile)
		if len(asks) != asked+1 {
			t.Fatalf("%s: the program was asked %d times, want %d", row.name, len(asks), asked+1)
		}
		last := asks[asked]
		if rawString(last, "contribution") != "lark" || rawString(last, "action") != row.action || rawString(last, "message_ref") != "ok" ||
			rawString(last, "at") == "" || len(last) != 5 {
			t.Errorf("%s: asked %s", row.name, last)
		}
		if strings.Contains(row.action, "/") { // what the node's gate reported carries the gate it went through, and where
			var target map[string]string
			_ = json.Unmarshal(last["target"], &target)
			if target["command"] != "examplecli" || target["conversation"] != "c3" || target["method"] != "POST" {
				t.Errorf("%s: the target %v", row.name, target)
			}
		}
	}
	id := rows[2].id
	_, err := h.RecallSent(context.Background(), id, func(context.Context, db.SentAsUser) error { t.Error("asked again"); return nil })
	if codeOf(err) != RecallAlreadyRecalled || len(asksOf(t, logFile)) != 3 {
		t.Fatalf("a second recall: %v, asked %d times", err, len(asksOf(t, logFile)))
	}
}

// What the program answers is told as the core does: it cannot (nothing was sent and it says so), the outcome is not
// known, the service refuses; only a yes writes recalled_at, and the user can ask again.
func TestRecallSentTellsWhatTheExtensionAnswered(t *testing.T) {
	needsLarkModule(t)
	h, led := recallHub(t)
	logFile := filepath.Join(t.TempDir(), "recalls.log")
	installRecall(t, h, recallPkg{id: "ext-a", conn: "lark", gate: "examplecli", recall: true, log: logFile}.write(t), "lark")
	for _, tc := range []struct{ ref, code, text string }{
		{"cannot", RecallNotRecallable, "too old for the service"},
		{"unknown", RecallUnknown, "the service did not answer"},
		{"refused", RecallRefused, "the service said no"},
	} {
		id := sendThrough(t, h, led, "lark", tc.ref)
		_, err := h.RecallSent(context.Background(), id, func(context.Context, db.SentAsUser) error { return nil })
		if codeOf(err) != tc.code || !strings.Contains(err.Error(), tc.text) || recalledAtOf(led, id) != nil {
			t.Errorf("%s: %v (code %q), marked %v", tc.ref, err, codeOf(err), recalledAtOf(led, id))
		}
	}
	if n := len(asksOf(t, logFile)); n != 3 {
		t.Errorf("asked %d times", n)
	}
}

// refusedRecall says that a recall of the row is refused as not recallable before anyone is asked: not the user, and not
// an extension (the logs are the files the programs write each ask to).
func refusedRecall(t *testing.T, h *Manager, led *sentLedgerStore, what string, id int64, logs ...string) {
	t.Helper()
	asked := func() (n int) {
		for _, l := range logs {
			n += len(asksOf(t, l))
		}
		return n
	}
	before := asked()
	_, err := h.RecallSent(context.Background(), id, func(context.Context, db.SentAsUser) error { t.Errorf("%s: the user was asked", what); return nil })
	if codeOf(err) != RecallNotRecallable || recalledAtOf(led, id) != nil || asked() != before {
		t.Errorf("%s: %v (code %q), marked %v, extensions asked %d times", what, err, codeOf(err), recalledAtOf(led, id), asked()-before)
	}
}

// recallable says that the row is taken back by asking the extension whose log is logFile, once more than it was.
func recallable(t *testing.T, h *Manager, led *sentLedgerStore, what string, id int64, logFile string) {
	t.Helper()
	before := len(asksOf(t, logFile))
	if _, err := h.RecallSent(context.Background(), id, func(context.Context, db.SentAsUser) error { return nil }); err != nil || recalledAtOf(led, id) == nil || len(asksOf(t, logFile)) != before+1 {
		t.Fatalf("%s: %v, marked %v, asked %d (was %d)", what, err, recalledAtOf(led, id), len(asksOf(t, logFile)), before)
	}
}

// The gate a node's proxy reported a write through must be one of the extension that provides the row's connector (a
// row's connector is lark whatever gate it went through): another extension's gate, one nobody has, and a row that names
// none are not taken back and nothing is sent to any extension; the user is not asked either. Only a gate row is held to
// this: a write through an extension's own connector is taken back by it.
func TestRecallSentOfAGateRowNeedsTheGateOfTheConnectorsExtension(t *testing.T) {
	needsLarkModule(t)
	h, led := recallHub(t)
	logA, logB := filepath.Join(t.TempDir(), "a.log"), filepath.Join(t.TempDir(), "b.log")
	installRecall(t, h, recallPkg{id: "ext-a", conn: "lark", gate: "examplecli", recall: true, log: logA}.write(t), "lark")
	installRecall(t, h, recallPkg{id: "ext-b", conn: "otherchat", gate: "othercli", recall: true, log: logB}.write(t), "otherchat")

	refusedRecall(t, h, led, "the gate of the other extension, which can recall too", reportedByGate(t, h, led, "othercli", "ok"), logA, logB)
	refusedRecall(t, h, led, "a gate nobody has", reportedByGate(t, h, led, "nobodycli", "ok"), logA, logB)
	refusedRecall(t, h, led, "a row that names no gate", reportedByGate(t, h, led, "", "ok"), logA, logB)

	recallable(t, h, led, "its own gate", reportedByGate(t, h, led, "examplecli", "ok"), logA)
	recallable(t, h, led, "a write through the other extension's connector", sendThrough(t, h, led, "otherchat", "ok"), logB)
	if len(asksOf(t, logA)) != 1 || len(asksOf(t, logB)) != 1 {
		t.Fatalf("asked %d and %d times, want once each", len(asksOf(t, logA)), len(asksOf(t, logB)))
	}
}

// A gate row whose target says nothing the hub can read about the gate is a gate row all the same: not taken back, and
// nobody is asked. The target here is what a damaged row, or one written by another version, could hold.
func TestRecallSentOfAGateRowWhoseTargetNamesNoReadableGate(t *testing.T) {
	needsLarkModule(t)
	h, led := recallHub(t)
	logFile := filepath.Join(t.TempDir(), "a.log")
	installRecall(t, h, recallPkg{id: "ext-a", conn: "lark", gate: "examplecli", recall: true, log: logFile}.write(t), "lark")
	for _, target := range []string{`null`, `{}`, `[]`, `7`, `"examplecli"`, `{"command": ""}`, `{"command": null}`, `{"command": 5}`, `{"command": ["examplecli"]}`} {
		id, _ := led.RecordSentAsUser(context.Background(), db.SentAsUser{At: time.Now(), Connector: "lark", Action: "POST /conversations/c3/messages",
			Target: json.RawMessage(target), MessageRef: "ok"})
		refusedRecall(t, h, led, "the target "+target, id, logFile)
	}
	// What is not a gate row is not held to this, whatever its target: the extension's own record of a send names none.
	id, _ := led.RecordSentAsUser(context.Background(), db.SentAsUser{At: time.Now(), Connector: "lark", Action: "sent.record", Target: json.RawMessage(`[]`), MessageRef: "ok"})
	recallable(t, h, led, "a row that is not a gate's", id, logFile)
}

// Two extensions can have an assistant gate with one command (it is checked for its format only), and the node then uses
// one of them: whose a write through it was cannot be told, so it is taken back by neither, whichever extension provides
// the row's connector, and an extension with no program, which the assistant is given the gate of all the same, counts.
// Once only one has the command again, it is the one that is asked.
func TestRecallSentOfAGateRowWhoseCommandTwoExtensionsHave(t *testing.T) {
	needsLarkModule(t)
	h, led := recallHub(t)
	mux := h.routes()
	logA, logB := filepath.Join(t.TempDir(), "a.log"), filepath.Join(t.TempDir(), "b.log")
	installRecall(t, h, recallPkg{id: "ext-a", conn: "lark", gate: "samecli", recall: true, log: logA}.write(t), "lark")
	row := reportedByGate(t, h, led, "samecli", "ok")
	// Before the second one is there a row is taken back: it is the count that refuses afterwards.
	recallable(t, h, led, "the only extension with the command", reportedByGate(t, h, led, "samecli", "ok"), logA)

	installRecall(t, h, recallPkg{id: "ext-b", conn: "otherchat", gate: "samecli", recall: true, log: logB}.write(t), "otherchat")
	refusedRecall(t, h, led, "a command two extensions have", row, logA, logB)
	refusedRecall(t, h, led, "the same, for a row written later", reportedByGate(t, h, led, "samecli", "ok"), logA, logB)
	recallable(t, h, led, "a write through its own connector is no gate row", sendThrough(t, h, led, "otherchat", "ok"), logB)

	if code, out := call(t, mux, "DELETE", "/api/v1/plugins/ext-b/-/", h.cfg.Token, ""); code != 200 {
		t.Fatalf("remove: %d %v", code, out)
	}
	waitLong(t, "its connector to go", func() bool { return h.connectors.Get("otherchat") == nil })
	recallable(t, h, led, "the other extension removed", row, logA)

	installRecall(t, h, recallPkg{id: "ext-c", conn: "unused", gate: "samecli", gateOnly: true}.write(t), "")
	refusedRecall(t, h, led, "an extension with no program has the command", reportedByGate(t, h, led, "samecli", "ok"), logA)
}

// A gate row's command is counted over the extensions the assistant is given the gates of (assistantGates): not a disabled
// one, and not one with a problem (it cannot run on this hub); both leave the other the only one with the command.
func TestRecallSentCountsOnlyTheExtensionsTheAssistantIsGiven(t *testing.T) {
	needsLarkModule(t)
	h, led := recallHub(t)
	mux := h.routes()
	logA, logB := filepath.Join(t.TempDir(), "a.log"), filepath.Join(t.TempDir(), "b.log")
	installRecall(t, h, recallPkg{id: "ext-a", conn: "lark", gate: "samecli", recall: true, log: logA}.write(t), "lark")
	installRecall(t, h, recallPkg{id: "ext-b", conn: "otherchat", gate: "samecli", recall: true, log: logB}.write(t), "otherchat")
	refusedRecall(t, h, led, "two enabled extensions have it", reportedByGate(t, h, led, "samecli", "ok"), logA, logB)

	if code, out := call(t, mux, "PUT", "/api/v1/plugins/ext-b/-/enabled", h.cfg.Token, `{"enabled": false}`); code != 200 {
		t.Fatalf("disable: %d %v", code, out)
	}
	waitLong(t, "its connector to go", func() bool { return h.connectors.Get("otherchat") == nil })
	recallable(t, h, led, "the other one is disabled", reportedByGate(t, h, led, "samecli", "ok"), logA)

	if code, out := call(t, mux, "PUT", "/api/v1/plugins/ext-b/-/enabled", h.cfg.Token, `{"enabled": true}`); code != 200 {
		t.Fatalf("enable: %d %v", code, out)
	}
	waitConnectorOK(t, h, "otherchat")
	refusedRecall(t, h, led, "the other one is enabled again", reportedByGate(t, h, led, "samecli", "ok"), logA, logB)
	h.extensions.reg.Get("ext-b").Problem = "does not run on this hub" // as when the hub no longer meets its engines
	h.extensions.apply(context.Background(), "ext-b")
	recallable(t, h, led, "the other one has a problem", reportedByGate(t, h, led, "samecli", "ok"), logA)
}

// An extension that still needs setting up (a required secret is missing) is not run, but the assistant is given its gate
// all the same, so it counts.
func TestRecallSentCountsAnExtensionThatNeedsSetup(t *testing.T) {
	needsLarkModule(t)
	h, led := recallHub(t)
	logA := filepath.Join(t.TempDir(), "a.log")
	installRecall(t, h, recallPkg{id: "ext-a", conn: "lark", gate: "samecli", recall: true, log: logA}.write(t), "lark")
	dir := recallPkg{id: "ext-c", conn: "unused", gate: "samecli", gateOnly: true}.write(t)
	raw, err := os.ReadFile(filepath.Join(dir, extension.ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	withSecret := strings.Replace(string(raw), `"engines":`, `"secrets": [{"name": "token", "title": "Token", "required": true}], "engines":`, 1)
	if err := os.WriteFile(filepath.Join(dir, extension.ManifestFile), []byte(withSecret), 0o644); err != nil {
		t.Fatal(err)
	}
	installRecall(t, h, dir, "")
	if missing := h.extensions.reg.Missing(context.Background(), h.extensions.reg.Get("ext-c")); len(missing) == 0 {
		t.Fatal("the fixture is not an extension that needs setup")
	}
	refusedRecall(t, h, led, "an extension that needs setup has the command", reportedByGate(t, h, led, "samecli", "ok"), logA)
}

// An extension can have more than one assistant gate, and every command of it counts: one of its later gates is its own
// as much as the first (a row through it is taken back while it is alone with the command), and is shared when another
// extension has the command, whichever of the first's gates it is.
func TestRecallSentCountsEveryGateOfAnExtension(t *testing.T) {
	needsLarkModule(t)
	h, led := recallHub(t)
	logA, logB := filepath.Join(t.TempDir(), "a.log"), filepath.Join(t.TempDir(), "b.log")
	installRecall(t, h, recallPkg{id: "ext-a", conn: "lark", gate: "firstcli", moreGates: []string{"samecli"}, recall: true, log: logA}.write(t), "lark")
	recallable(t, h, led, "its second gate, while it is alone with the command", reportedByGate(t, h, led, "samecli", "ok"), logA)
	installRecall(t, h, recallPkg{id: "ext-b", conn: "otherchat", gate: "samecli", recall: true, log: logB}.write(t), "otherchat")
	refusedRecall(t, h, led, "a command that the second gate of one extension and the first of another have", reportedByGate(t, h, led, "samecli", "ok"), logA, logB)
	recallable(t, h, led, "its first gate, which nobody else has", reportedByGate(t, h, led, "firstcli", "ok"), logA)
}

// An extension that is switched off, or removed, has nothing to ask (its connector is not here); switched on again it is
// asked as before. One removed and installed again is a new install: what the ledger wrote before is not taken back, and
// what it writes after is.
func TestRecallSentWhenTheExtensionIsOffRemovedOrInstalledAgain(t *testing.T) {
	needsLarkModule(t)
	h, led := recallHub(t)
	mux := h.routes()
	logFile := filepath.Join(t.TempDir(), "recalls.log")
	pkg := recallPkg{id: "ext-a", conn: "lark", gate: "examplecli", recall: true, log: logFile}
	installRecall(t, h, pkg.write(t), "lark")
	yes := func(context.Context, db.SentAsUser) error { return nil }

	id := sendThrough(t, h, led, "lark", "ok")
	if code, out := call(t, mux, "PUT", "/api/v1/plugins/ext-a/-/enabled", h.cfg.Token, `{"enabled": false}`); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	waitLong(t, "its connector to go", func() bool { return h.connectors.Get("lark") == nil })
	_, err := h.RecallSent(context.Background(), id, yes)
	if codeOf(err) != RecallNotRecallable || !strings.Contains(err.Error(), "not here any more") || len(asksOf(t, logFile)) != 0 {
		t.Fatalf("switched off: %v, asked %d", err, len(asksOf(t, logFile)))
	}
	call(t, mux, "PUT", "/api/v1/plugins/ext-a/-/enabled", h.cfg.Token, `{"enabled": true}`)
	waitConnectorOK(t, h, "lark")
	if _, err := h.RecallSent(context.Background(), id, yes); err != nil || recalledAtOf(led, id) == nil {
		t.Fatalf("switched on again: %v", err)
	}

	old := sendThrough(t, h, led, "lark", "ok")
	if code, out := call(t, mux, "DELETE", "/api/v1/plugins/ext-a/-/", h.cfg.Token, ""); code != 200 {
		t.Fatalf("remove: %d %v", code, out)
	}
	waitLong(t, "its connector to go", func() bool { return h.connectors.Get("lark") == nil })
	if _, err := h.RecallSent(context.Background(), old, yes); codeOf(err) != RecallNotRecallable {
		t.Fatalf("removed: %v", err)
	}
	asked := len(asksOf(t, logFile))
	installRecall(t, h, pkg.write(t), "lark")
	if _, err := h.RecallSent(context.Background(), old, yes); codeOf(err) != RecallNotRecallable || recalledAtOf(led, old) != nil || len(asksOf(t, logFile)) != asked {
		t.Fatalf("a send from before this install: %v, asked %d (was %d)", err, len(asksOf(t, logFile)), asked)
	}
	// What a page of the ledger asks (recallCheck) says the same before anyone is asked: the old row offers no recall, the
	// new one does. And the connector, asked directly, refuses it too (a second check, in proc).
	oldRow, _, _ := led.SentAsUserByID(context.Background(), old)
	if _, rerr := h.recallCheck(context.Background(), oldRow, time.Now()); rerr == nil || rerr.Code != RecallNotRecallable {
		t.Errorf("a send from before this install offers a recall: %v", rerr)
	}
	rc, _ := connector.RecallerOf(h.connectors.Get("lark"))
	if err := rc.Recall(context.Background(), connector.SentMessage{Action: oldRow.Action, Ref: "ok", Target: oldRow.Target, At: oldRow.At}); !errors.Is(err, connector.ErrCannotRecall) || len(asksOf(t, logFile)) != asked {
		t.Errorf("the connector asked directly: %v, asked %d (was %d)", err, len(asksOf(t, logFile)), asked)
	}
	fresh := sendThrough(t, h, led, "lark", "ok")
	freshRow, _, _ := led.SentAsUserByID(context.Background(), fresh)
	if _, rerr := h.recallCheck(context.Background(), freshRow, time.Now()); rerr != nil {
		t.Errorf("a send from after this install offers no recall: %v", rerr)
	}
	if _, err := h.RecallSent(context.Background(), fresh, yes); err != nil || recalledAtOf(led, fresh) == nil || len(asksOf(t, logFile)) != asked+1 {
		t.Fatalf("a send from after it: %v, asked %d", err, len(asksOf(t, logFile)))
	}
}

// An extension that did not say it can take things back is not asked (a recall of its row is not recallable), and one
// upgraded to say so is, also for what it sent before: the upgrade neither asks the user again nor starts the install over.
func TestRecallSentAfterAnUpgradeThatDeclaresRecall(t *testing.T) {
	needsLarkModule(t)
	h, led := recallHub(t)
	mux := h.routes()
	logFile := filepath.Join(t.TempDir(), "recalls.log")
	pv := installRecall(t, h, recallPkg{id: "ext-a", conn: "lark", recall: false, log: logFile}.write(t), "lark")
	if hasPermission(pv["permissions"], "recalls_sent") {
		t.Fatalf("shown for an extension that cannot: %v", pv["permissions"])
	}
	yes := func(context.Context, db.SentAsUser) error { return nil }
	id := sendThrough(t, h, led, "lark", "ok")
	if _, err := h.RecallSent(context.Background(), id, yes); codeOf(err) != RecallNotRecallable || len(asksOf(t, logFile)) != 0 {
		t.Fatalf("an extension that does not declare it: %v, asked %d", err, len(asksOf(t, logFile)))
	}

	_, up := call(t, mux, "POST", "/api/v1/plugins/-/preview", h.cfg.Token, `{"source": "dir:`+recallPkg{id: "ext-a", conn: "lark", recall: true, version: "1.1.0", log: logFile}.write(t)+`", "upgrade": "ext-a"}`)
	added, _ := up["added_permissions"].([]any)
	if up["more_permissions"] == true || len(added) != 0 || !hasPermission(up["permissions"], "recalls_sent") {
		t.Fatalf("the upgrade preview: more %v, added %v, all %v", up["more_permissions"], up["added_permissions"], up["permissions"])
	}
	if code, out := call(t, mux, "POST", "/api/v1/plugins/ext-a/-/upgrade", h.cfg.Token, fmt.Sprintf(`{"preview_id": %q}`, up["preview_id"])); code != 200 || out["version"] != "1.1.0" {
		t.Fatalf("an upgrade that adds it needs no consent: %d %v", code, out)
	}
	waitLong(t, "the new version's connector to declare it", func() bool {
		c := h.connectors.Get("lark")
		return c != nil && c.Manifest().Has(connector.CapRecall) && connector.StatusOf(context.Background(), c).State == connector.StateOK
	})
	if _, detail := call(t, mux, "GET", "/api/v1/plugins/ext-a/-/", h.cfg.Token, ""); !hasPermission(detail["permissions"], "recalls_sent") {
		t.Errorf("an installed extension's details say what it can take back: %v", detail["permissions"])
	}
	if _, err := h.RecallSent(context.Background(), id, yes); err != nil || recalledAtOf(led, id) == nil || len(asksOf(t, logFile)) != 1 {
		t.Fatalf("what it sent before the upgrade: %v, asked %d", err, len(asksOf(t, logFile)))
	}
}

// An upgrade can change the command of the extension's gate. The extension has the command it has now: a gate row of the
// new command is taken back, and one of the old command is not (the extension no longer has that gate, so whose it was
// cannot be told).
func TestRecallSentAfterAnUpgradeThatChangesTheGate(t *testing.T) {
	needsLarkModule(t)
	h, led := recallHub(t)
	mux := h.routes()
	logFile := filepath.Join(t.TempDir(), "recalls.log")
	installRecall(t, h, recallPkg{id: "ext-a", conn: "lark", gate: "oldcli", recall: true, log: logFile}.write(t), "lark")
	recallable(t, h, led, "the gate it was installed with", reportedByGate(t, h, led, "oldcli", "ok"), logFile)
	old := reportedByGate(t, h, led, "oldcli", "ok")

	_, up := call(t, mux, "POST", "/api/v1/plugins/-/preview", h.cfg.Token, `{"source": "dir:`+recallPkg{id: "ext-a", conn: "lark", gate: "newcli", recall: true, version: "1.1.0", log: logFile}.write(t)+`", "upgrade": "ext-a"}`)
	if code, out := call(t, mux, "POST", "/api/v1/plugins/ext-a/-/upgrade", h.cfg.Token, fmt.Sprintf(`{"preview_id": %q}`, up["preview_id"])); code != 200 || out["version"] != "1.1.0" {
		t.Fatalf("upgrade: %d %v (preview %v)", code, out, up)
	}
	waitLong(t, "the new version to run with its gate", func() bool {
		hp := h.extensions.get("ext-a")
		c := h.connectors.Get("lark")
		return hp != nil && slices.Contains(hp.gateCommands, "newcli") && c != nil && connector.StatusOf(context.Background(), c).State == connector.StateOK
	})
	recallable(t, h, led, "the gate it has now", reportedByGate(t, h, led, "newcli", "ok"), logFile)
	refusedRecall(t, h, led, "the gate it had", old, logFile)
}

// What the ledger wrote before an install is not asked about, for an installed extension only: a dev extension and a
// built-in one are not replaced by another under their id, and are asked about what came before they were registered.
func TestRecallSentBeforeTheInstallIsRefusedForAnInstalledExtensionOnly(t *testing.T) {
	needsLarkModule(t)
	h, led := recallHub(t)
	ctx := context.Background()
	before := time.Now().Add(-time.Hour)
	sentBefore := func(conn string) int64 {
		id, _ := led.RecordSentAsUser(ctx, db.SentAsUser{At: before, Connector: conn, Action: conn + "_send", Target: json.RawMessage(`{}`), MessageRef: "ok"})
		return id
	}
	rows := map[string]int64{"chatinstalled": sentBefore("chatinstalled"), "chatdev": sentBefore("chatdev"), "chatbuiltin": sentBefore("chatbuiltin")}
	logs := map[string]string{}
	for conn := range rows {
		logs[conn] = filepath.Join(t.TempDir(), conn+".log")
	}

	installRecall(t, h, recallPkg{id: "ext-installed", conn: "chatinstalled", recall: true, log: logs["chatinstalled"]}.write(t), "chatinstalled")
	refusedRecall(t, h, led, "an installed extension, a row from before its install", rows["chatinstalled"], logs["chatinstalled"])

	dev := recallPkg{id: "ext-dev", conn: "chatdev", recall: true, log: logs["chatdev"]}.write(t)
	if _, err := h.extensions.reg.AddDevContext(ctx, dev, nil, nil); err != nil {
		t.Fatal(err)
	}
	waitConnectorOK(t, h, "chatdev")
	recallable(t, h, led, "a dev extension, a row from before it was registered", rows["chatdev"], logs["chatdev"])

	builtin := recallPkg{id: "ext-builtin", conn: "chatbuiltin", recall: true, log: logs["chatbuiltin"]}.write(t)
	raw, err := os.ReadFile(extension.ManifestPath(builtin))
	if err != nil {
		t.Fatal(err)
	}
	m, err := extension.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.extensions.reg.AddBuiltin(m, builtin, true); err != nil {
		t.Fatal(err)
	}
	h.extensions.apply(ctx, "ext-builtin")
	waitLong(t, "its connector to be added", func() bool { return h.connectors.Get("chatbuiltin") != nil })
	if err := h.connectors.SetEnabled("chatbuiltin", true); err != nil { // a built-in extension's connectors start off until the user turns them on
		t.Fatal(err)
	}
	waitConnectorOK(t, h, "chatbuiltin")
	recallable(t, h, led, "a built-in extension, a row from before it was registered", rows["chatbuiltin"], logs["chatbuiltin"])
}

// A connector that no extension provides (a built-in one), even one that can take back anything, is not asked about a
// row of the node's gate proxy, whether the gate it names is nobody's or is another extension's: it is no gate of an
// extension that provides it. Nobody is asked.
func TestRecallSentOfAGateRowOfAConnectorNoExtensionProvides(t *testing.T) {
	needsLarkModule(t)
	h, led := recallHub(t)
	logFile := filepath.Join(t.TempDir(), "recalls.log")
	installRecall(t, h, recallPkg{id: "ext-a", conn: "lark", gate: "examplecli", recall: true, log: logFile}.write(t), "lark")
	c := &recallsAnything{&recallingThreads{refThreads: &refThreads{threadsConnector: newThreadsConnector(), ref: "41"}, events: &eventLog{}}}
	if err := h.addConnector(c); err != nil {
		t.Fatal(err)
	}
	put := func(action, target string) int64 {
		id, _ := led.RecordSentAsUser(context.Background(), db.SentAsUser{At: time.Now(), Connector: "example", Action: action, Target: json.RawMessage(target), MessageRef: "41"})
		return id
	}
	for what, command := range map[string]string{"a gate nobody has": "nobodycli", "a gate only an extension has": "examplecli"} {
		_, err := h.RecallSent(context.Background(), put("POST /conversations/c3/messages", `{"method":"POST","command":"`+command+`"}`),
			func(context.Context, db.SentAsUser) error { t.Errorf("%s: the user was asked", what); return nil })
		if codeOf(err) != RecallNotRecallable || c.askedCount() != 0 || len(asksOf(t, logFile)) != 0 {
			t.Fatalf("%s, a row of a built-in connector: %v, asked %d and %d", what, err, c.askedCount(), len(asksOf(t, logFile)))
		}
	}
	// Its own write is taken back, as the gate does not come into it.
	own := put("example_send", `{}`)
	if _, err := h.RecallSent(context.Background(), own, func(context.Context, db.SentAsUser) error { return nil }); err != nil || c.askedCount() != 1 {
		t.Fatalf("its own write: %v, asked %d", err, c.askedCount())
	}
}

// recallsAnything is recallingThreads that says it can take back any action.
type recallsAnything struct{ *recallingThreads }

func (recallsAnything) CanRecall(string, time.Duration) bool { return true }

// ledgerPage is the ledger as GET /api/v1/connectors/sent gives it to the user's own device, by row id.
func ledgerPage(t *testing.T, h *Manager) map[int64]map[string]any {
	t.Helper()
	code, out := call(t, h.routes(), "GET", "/api/v1/connectors/sent?limit=200", deviceToken("dev_phone", roleFull), "")
	if code != 200 {
		t.Fatalf("the ledger: %d %v", code, out)
	}
	rows := map[int64]map[string]any{}
	items, _ := out["items"].([]any)
	for _, it := range items {
		m := it.(map[string]any)
		rows[int64(m["id"].(float64))] = m
	}
	return rows
}

// The ledger's page says of each row of an extension's connector what a recall of it would say before anyone is asked
// (can_recall is recallCheck's answer): a row from before the extension's install is not offered, nor a gate row that
// names no gate, a gate nobody has, or a gate two extensions have; the extension's own rows are, and what the page does not
// offer a recall refuses too.
func TestSentLedgerPageOffersRecallOfAnExtensionsRowsAsARecallWouldAllow(t *testing.T) {
	needsLarkModule(t)
	h, led := recallHub(t)
	ctx := context.Background()
	logA, logB := filepath.Join(t.TempDir(), "a.log"), filepath.Join(t.TempDir(), "b.log")
	put := func(at time.Time, action, target string) int64 {
		id, err := led.RecordSentAsUser(ctx, db.SentAsUser{At: at, Connector: "lark", Action: action, Target: json.RawMessage(target), MessageRef: "ok"})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	beforeInstall := put(time.Now().Add(-time.Hour), "lark_send", `{}`)
	beforeInstallGate := put(time.Now().Add(-time.Hour), "POST /conversations/c3/messages", `{"method":"POST","command":"examplecli"}`)

	installRecall(t, h, recallPkg{id: "ext-a", conn: "lark", gate: "examplecli", recall: true, log: logA}.write(t), "lark")
	own := sendThrough(t, h, led, "lark", "ok")
	ownGate := reportedByGate(t, h, led, "examplecli", "ok")
	noGate := reportedByGate(t, h, led, "", "ok")
	nobodysGate := reportedByGate(t, h, led, "nobodycli", "ok")

	check := func(when string, want map[int64]bool) {
		t.Helper()
		page := ledgerPage(t, h)
		for id, can := range want {
			if got, ok := page[id]["can_recall"].(bool); !ok || got != can {
				t.Errorf("%s: row %d (%v): can_recall %v, want %v", when, id, page[id]["action"], page[id]["can_recall"], can)
			}
			if !can {
				refusedRecall(t, h, led, fmt.Sprintf("%s: row %d, which the page does not offer", when, id), id, logA, logB)
			}
		}
	}
	check("one extension", map[int64]bool{beforeInstall: false, beforeInstallGate: false, own: true, ownGate: true, noGate: false, nobodysGate: false})

	installRecall(t, h, recallPkg{id: "ext-b", conn: "otherchat", gate: "examplecli", recall: true, log: logB}.write(t), "otherchat")
	check("a second extension with the same gate command", map[int64]bool{own: true, ownGate: false})
}

// Not being offered a recall is not the same as being offered no per-chat rule: a rule is about the chat and does not need
// to know whose the write was (chat_rules.go), so a row through a gate command that two Lark gates share is not offered a
// recall and still offers the rule. Someone making the two agree would change one of them without meaning to. Both gate
// files say they are for the Lark connector, so the per-chat rules see the command twice.
func TestSentLedgerPageOffersAPerChatRuleForARowWhoseCommandTwoExtensionsHave(t *testing.T) {
	needsLarkModule(t)
	h, led := recallHub(t)
	logA, logB := filepath.Join(t.TempDir(), "a.log"), filepath.Join(t.TempDir(), "b.log")
	installRecall(t, h, recallPkg{id: "ext-a", conn: "lark", gate: "samecli", recall: true, log: logA}.write(t), "lark")
	installRecall(t, h, recallPkg{id: "ext-b", conn: "otherchat", gate: "samecli", gateConnector: "lark", recall: true, log: logB}.write(t), "otherchat")
	if got := h.extensions.gateCommands(connector.Lark); !slices.Equal(got, []string{"samecli", "samecli"}) {
		t.Fatalf("the Lark gates' commands are %v, want the command twice", got)
	}
	id := reportedByGate(t, h, led, "samecli", "ok")
	row := ledgerPage(t, h)[id]
	rule, _ := row["chat_rule"].(map[string]any)
	if row["can_recall"] != false || rule["can"] != true {
		t.Errorf("a row of a command two Lark gates have: can_recall %v, per-chat rule %v", row["can_recall"], rule)
	}
	refusedRecall(t, h, led, "a command two Lark gates have", id, logA, logB)
}
