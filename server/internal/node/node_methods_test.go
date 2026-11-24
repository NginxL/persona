package node

import (
	"context"
	"encoding/json"
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/node/clipshare"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// nodeRPC's adapter decodes a call's params into the method's own type once: a field the type does not have (a newer
// manager's) is ignored, empty params leave it zero, and params that do not decode are refused in the handlers' words,
// "bad <method> params", with the decoder's error as the cause.
func TestNodeRPCDecodesParamsIntoTheMethodsType(t *testing.T) {
	t.Parallel()
	type params struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	var got []params
	h := rpcHandler("planted.decode", func(_ *Node, _ context.Context, p params) (string, error) {
		got = append(got, p)
		return "ok:" + p.Name, nil
	})
	for _, raw := range []string{`{"name":"a","count":2,"added_by_a_newer_manager":{"x":[1]}}`, ``, `null`} {
		out, err := h(nil, t.Context(), json.RawMessage(raw))
		if err != nil || out == nil {
			t.Fatalf("%q: %v, %v", raw, out, err)
		}
	}
	if want := []params{{"a", 2}, {}, {}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded %+v, want %+v", got, want)
	}
	out, err := h(nil, t.Context(), json.RawMessage(`{"count":"two"}`))
	var cause *json.UnmarshalTypeError
	if out != nil || err == nil || err.Error() != "bad planted.decode params" || !errors.As(err, &cause) {
		t.Fatalf("undecodable params: %v, %v (want bad planted.decode params, caused by the decoder's error)", out, err)
	}
	if len(got) != 3 {
		t.Fatalf("the handler ran on params that did not decode: %+v", got)
	}
}

// A method that takes json.RawMessage is handed the params themselves, the same bytes: secret.run and model.run clear
// them after reading, and a copy would leave the original uncleared.
func TestNodeRPCHandsRawParamsOverAsTheyCame(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(` {"password":"aHVudGVyMg=="} `)
	h := rpcHandler("planted.raw", func(_ *Node, _ context.Context, p json.RawMessage) (any, error) {
		if &p[0] != &raw[0] || len(p) != len(raw) {
			t.Errorf("handed %q, a copy of the params", p)
		}
		clear(p)
		return nil, nil
	})
	if _, err := h(nil, t.Context(), raw); err != nil {
		t.Fatal(err)
	}
	if strings.Trim(string(raw), "\x00") != "" {
		t.Fatalf("the params were not cleared in place: %q", raw)
	}
}

// Every registration is checked where it is made: a name the table cannot answer, a missing handler and a second
// registration are refused, each for its own reason.
func TestMethodTableAdmitsOnlyNamesItCanAnswer(t *testing.T) {
	t.Parallel()
	handler := func(*Node, context.Context, json.RawMessage) (any, error) { return nil, nil }
	var table methodTable
	table.register("planted.kept", handler)
	for _, tc := range []struct {
		name methodName
		h    methodHandler
		want error // nil: admitted
	}{
		{"machine.planted", handler, nil},
		{"sessions.planted", handler, nil}, // sessions., not session.
		{"", handler, errUnnamedMethod},
		{"machine.planted", nil, errNoHandler},
		{"planted.kept", handler, errRegisteredTwice},
		{"assistant.segment.planted", handler, errReservedPrefix},
		{methodGitBackendPrefix + "status", handler, errReservedPrefix},
		{"node.update.planted", handler, errReservedPrefix},
		{"task.workspace.capabilities", handler, errReservedPrefix},
		{"plugin.node.planted", handler, errReservedPrefix},
		{nodeproto.SourceSend, handler, errDeliveryAlias},
		{nodeproto.OwnerInterrupt, handler, errDeliveryAlias},
		{nodeproto.OwnerCardAnswer, handler, errDeliveryAlias},
		{"session.planted", handler, errSessionBound},
		{"terminal.planted", handler, errSessionBound},
		{"assistant.send", handler, errSessionBound},
		{assistantwire.DeviceAssistantAnswerAgain, handler, errSessionBound},
		{"assistant.permissions.reset", handler, errSessionBound},
		{"codex.answer", handler, errSessionBound},
	} {
		err := table.admit(tc.name, tc.h)
		if tc.want == nil && err != nil || tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("admit(%q): %v, want %v", tc.name, err, tc.want)
		}
	}
	if _, _, ok := table.answer(nil, t.Context(), "machine.planted", nil); ok {
		t.Fatal("admit changed the table")
	}
}

// A domain that registers a session-bound method panics when the node starts, at the init() that does it, and the
// table never holds it: the shared decode and the session middleware a table entry would skip stay in front of it.
func TestNodeRPCPanicsOnARefusedName(t *testing.T) {
	const planted methodName = "session.planted"
	func() {
		defer func() {
			msg, _ := recover().(string)
			if !strings.Contains(msg, `"session.planted"`) || !strings.Contains(msg, errSessionBound.Error()) ||
				!strings.Contains(msg, `docs/server/architecture.md "The node's method table"`) {
				t.Fatalf("nodeRPC(%q) panicked with %q, want the refusal and the doc", planted, msg)
			}
		}()
		nodeRPC(planted, func(*Node, context.Context, json.RawMessage) (any, error) { return nil, nil })
	}()
	if _, _, ok := nodeMethods.answer(nil, t.Context(), planted, nil); ok {
		t.Fatalf("the table holds %s", planted)
	}
}

// The methods that moved from handleMethod's switch into the table answer through Node.handle as the switch had them
// answer: the same result and the same error, for params they take and params they refuse. before is the switch's
// case as it was; for machine.ports and machine.processes, which decoded their params themselves, it includes that
// decode. Calls with effects (a kill, a service action, a secret, a model request) use params refused before anything
// runs. clipboard.get is only looked up: answering it reads this computer's clipboard.
func TestMethodsMovedToTheTableAnswerAsBefore(t *testing.T) {
	moved := []methodName{"sessions.history", "workspace.naming.resolve", "node.doctor", "node.doctor.fix",
		proto.MethodMachineNetwork, proto.MethodRoutineCheck, proto.MethodSecretRun, proto.MethodSecretStage,
		proto.MethodModelHTTP, proto.MethodModelRun, methodClipboardGet, methodClipboardSet, proto.MethodMachinePorts,
		proto.MethodMachineProcesses, proto.MethodMachineProcessKill, proto.MethodMachineServices,
		proto.MethodMachineServiceLogs, proto.MethodMachineServiceAction}
	for _, m := range moved {
		if nodeMethods.handlers[m] == nil {
			t.Errorf("%s is not in the method table", m)
		}
	}

	type handler = func(*Node, context.Context, json.RawMessage) (any, error)
	decoded := func(method string, call func(n *Node, ctx context.Context, raw json.RawMessage) (any, error, bool)) handler {
		return func(n *Node, ctx context.Context, raw json.RawMessage) (any, error) {
			if out, err, ok := call(n, ctx, raw); ok {
				return out, err
			}
			return nil, errors.New("bad " + method + " params")
		}
	}
	ports := decoded(proto.MethodMachinePorts, func(n *Node, ctx context.Context, raw json.RawMessage) (any, error, bool) {
		var p proto.MachinePortsParams
		if len(raw) > 0 && json.Unmarshal(raw, &p) != nil {
			return nil, nil, false
		}
		out, err := n.machinePorts(ctx, p)
		return out, err, true
	})
	processes := decoded(proto.MethodMachineProcesses, func(n *Node, ctx context.Context, raw json.RawMessage) (any, error, bool) {
		var p proto.MachineProcessesParams
		if len(raw) > 0 && json.Unmarshal(raw, &p) != nil {
			return nil, nil, false
		}
		out, err := n.machineProcesses(ctx, p)
		return out, err, true
	})
	services := func() *Node {
		return &Node{serviceRunner: &machineServiceRunner{
			goos:     "plan9",
			now:      func() time.Time { return time.Unix(1700000000, 0) },
			lookPath: func(string) (string, error) { return "", exec.ErrNotFound },
			run: func(context.Context, int, string, ...string) ([]byte, bool, error) {
				return nil, false, errors.New("ran a command")
			},
		}}
	}
	plain := func() *Node { return &Node{} }

	for _, tc := range []struct {
		method methodName
		params string
		node   func() *Node
		before handler
		want   string // the error both answer ("": none)
	}{
		{"sessions.history", `{"since":"2026-01-01T00:00:00Z","source":"runtime","limit":1}`, plain,
			func(n *Node, ctx context.Context, p json.RawMessage) (any, error) { return n.sessionsHistory(ctx, p) }, ""},
		{"sessions.history", `{"since":`, plain,
			func(n *Node, ctx context.Context, p json.RawMessage) (any, error) { return n.sessionsHistory(ctx, p) },
			"sessions.history: malformed params"},
		{"workspace.naming.resolve", `{`, plain, (*Node).resolveWorkspaceNaming, "unexpected end of JSON input"},
		{"node.doctor", `{"fix":"yes"}`, plain,
			func(n *Node, ctx context.Context, p json.RawMessage) (any, error) { return n.doctor(ctx, p, false) },
			"bad node.doctor params"},
		{"node.doctor.fix", `{"fix":"yes"}`, plain,
			func(n *Node, ctx context.Context, p json.RawMessage) (any, error) { return n.doctor(ctx, p, true) },
			"bad node.doctor params"},
		{proto.MethodMachineNetwork, `{"unknown_field":true}`, plain, (*Node).machineNetwork, ""},
		{proto.MethodRoutineCheck, `{"command":"","timeout":1}`, plain,
			func(_ *Node, ctx context.Context, p json.RawMessage) (any, error) { return routineCheck(ctx, p) },
			"invalid routine check command or timeout"},
		{proto.MethodSecretRun, `{"password":`, plain, (*Node).secretRun, "secret.run: bad params"},
		{proto.MethodSecretStage, `{"env":`, plain,
			func(n *Node, _ context.Context, p json.RawMessage) (any, error) { return n.secretStageCall(p) },
			"unexpected end of JSON input"},
		{proto.MethodModelHTTP, `{"url":1}`, plain,
			func(_ *Node, ctx context.Context, p json.RawMessage) (any, error) { return modelHTTP(ctx, p) }, ""},
		{proto.MethodModelRun, `{"launcher":`, plain, (*Node).modelRun, "model.run: bad params"},
		{methodClipboardSet, `{"mime":`, plain,
			func(_ *Node, ctx context.Context, p json.RawMessage) (any, error) { return clipshare.Set(ctx, p) }, ""},
		{proto.MethodMachinePorts, `[1]`, plain, ports, "bad machine.ports params"},
		{proto.MethodMachinePorts, `{"protocol":"icmp"}`, plain, ports, `invalid protocol "icmp": expected tcp or udp`},
		{proto.MethodMachineProcesses, `{"sort":7}`, plain, processes, "bad machine.processes params"},
		{proto.MethodMachineProcesses, `{"sort":"size"}`, plain, processes,
			`invalid sort "size": expected cpu, memory, or uptime`},
		{proto.MethodMachineProcessKill, ``, plain, (*Node).machineProcessKill, "bad machine.process.kill params"},
		{proto.MethodMachineProcessKill, `{"pid":1,"signal":"SIGTERM"}`, plain, (*Node).machineProcessKill,
			"refusing to signal system or invalid PID"},
		{proto.MethodMachineServices, `{}`, services, (*Node).machineServices, ""},
		{proto.MethodMachineServiceLogs, `{`, plain, (*Node).machineServiceLogs, "invalid machine.service_logs params"},
		{proto.MethodMachineServiceAction, `{"kind":"systemd"}`, plain, (*Node).machineServiceAction, ""},
	} {
		t.Run(string(tc.method)+" "+tc.params, func(t *testing.T) {
			// Each call has its own params: secret.run and model.run clear the ones they are handed.
			got, gotErr := tc.node().handle(t.Context(), string(tc.method), json.RawMessage(tc.params))
			want, wantErr := tc.before(tc.node(), t.Context(), json.RawMessage(tc.params))
			if !reflect.DeepEqual(got, want) {
				t.Errorf("answered %#v, before %#v", got, want)
			}
			if wireError(gotErr) != wireError(wantErr) {
				t.Errorf("answered error %q, before %q", wireError(gotErr), wireError(wantErr))
			}
			if tc.want != "" && (gotErr == nil || gotErr.Error() != tc.want) {
				t.Errorf("error %v, want %q", gotErr, tc.want)
			}
		})
	}
}

// wireError is how an error reaches the manager: an *rpc.Error as it is, any other by its text (rpc's reply).
func wireError(err error) string {
	var coded *rpc.Error
	switch {
	case err == nil:
		return ""
	case errors.As(err, &coded):
		b, _ := json.Marshal(coded)
		return string(b)
	}
	return err.Error()
}
