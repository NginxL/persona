package node

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestSourcePermissionUsesLiveProcessAndCurrentMode(t *testing.T) {
	n := assistantNode(t)
	p, _ := fakeAssistant(n, sidA, proto.ModeBypass)
	p.cmd = &exec.Cmd{Process: &os.Process{Pid: os.Getpid()}}
	p.born = time.Now()
	args := json.RawMessage(`{"text":"synthetic report"}`)
	proof := n.sourcePermissionIn([]int{os.Getpid()}, sidA, "agent_send", args)
	if !proof.Matches(sidA, "agent_send", args) || proof.Class != peerModeBypass {
		t.Fatalf("capture: %+v", proof)
	}
	p.mu.Lock()
	p.mode = "default"
	p.mu.Unlock()
	next := n.sourcePermissionIn([]int{os.Getpid()}, sidA, "agent_send", args)
	if next.Class != peerModePrompting || proof.Class != peerModeBypass {
		t.Fatalf("changed capture: old %+v new %+v", proof, next)
	}
	for _, mode := range []string{"plan", "", "unknown"} {
		p.mu.Lock()
		p.mode = mode
		p.mu.Unlock()
		if got := n.sourcePermissionIn([]int{os.Getpid()}, sidA, "agent_send", args); got.Valid() {
			t.Fatalf("unknown %s: %+v", mode, got)
		}
	}
	p.mu.Lock()
	p.mode = proto.ModeBypass
	p.mu.Unlock()
	if n.sourcePermissionIn([]int{-1}, sidA, "agent_send", args).Valid() || n.sourcePermissionIn([]int{os.Getpid()}, sidB, "agent_send", args).Valid() {
		t.Fatal("caller claimed another process/session")
	}
	child, _ := fakeAssistant(n, sidB, "default")
	child.cmd = &exec.Cmd{Process: &os.Process{Pid: os.Getpid() + 1}}
	child.born = time.Now()
	if n.sourcePermissionIn([]int{os.Getpid() + 1, os.Getpid()}, sidA, "agent_send", args).Valid() {
		t.Fatal("nested Agent impersonated its bypass parent")
	}
	p.settingsMu.Lock()
	if n.sourcePermissionIn([]int{os.Getpid()}, sidA, "agent_send", args).Valid() {
		t.Error("mode switch in flight was attested")
	}
	p.settingsMu.Unlock()
	close(p.exited)
	if n.sourcePermissionIn([]int{os.Getpid()}, sidA, "agent_send", args).Valid() {
		t.Error("exited process was attested")
	}
}

func TestSourcePermissionExternalTranscriptAndGeneration(t *testing.T) {
	n, _, home := herdrNode(t)
	noPaneEnv(t)
	path := modeTranscript(t, home, home, proto.ModeBypass)
	mark := sourceGeneration(os.Getpid())
	if mark == "" {
		t.Skip("kernel process generation unavailable")
	}
	start := strings.TrimPrefix(strings.TrimPrefix(mark, "linux:"), "windows:")
	writeOutsideClaude(t, home, sidA, "cli", "busy", nil, map[string]any{"procStart": start})
	args := json.RawMessage(`{"kind":"blocked","text":"synthetic question"}`)
	get := func() proto.SourcePermission {
		return n.sourcePermissionIn([]int{os.Getpid()}, sidA, "report_to_supervisor", args)
	}
	old := get()
	if !old.Matches(sidA, "report_to_supervisor", args) || old.Class != peerModeBypass {
		t.Fatalf("capture %+v", old)
	}
	// The cached list can still say bypass; only the source transcript counts.
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b = []byte(strings.ReplaceAll(string(b), "bypassPermissions", "default"))
	if err := os.WriteFile(path, b, 0600); err != nil {
		t.Fatal(err)
	}
	if p := get(); p.Class != peerModePrompting {
		t.Fatalf("current transcript: %+v", p)
	}
	// An unrelated config's newer bypass copy does not override the source profile.
	modeTranscript(t, filepath.Join(home, "other"), home, proto.ModeBypass)
	if p := get(); p.Class != peerModePrompting {
		t.Fatalf("wrong profile: %+v", p)
	}
	c := n.liveClaudes()[0]
	if sourceRegistryMatches(c, "linux:impossible") || sourceRegistryMatches(c, "") {
		t.Error("reused/unknown generation accepted")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if get().Valid() {
		t.Error("missing source transcript attested")
	}
}

func TestSourcePermissionDeliveryIsSeparateFromOwnerAndRawParameters(t *testing.T) {
	n, f, home := herdrNode(t)
	noPaneEnv(t)
	shortPeerWait(t, 100*time.Millisecond)
	f.reply("session.snapshot", snapshot(nil, nil, nil, nil))
	inbox := newFakeInbox(t, modeTranscript(t, home, home, proto.ModeBypass))
	inbox.setMode(peerModeBypass)
	writeOutsideClaude(t, home, sidA, "claude-desktop", "idle", inbox, nil)
	proof := proto.SourcePermission{At: time.Now(), Session: sidB, Generation: "process-1", Class: peerModeBypass, Digest: proto.SourceCallDigest("agent_send", json.RawMessage(`{}`))}
	for i, method := range []string{"session.send", nodeproto.SourceSend} {
		_, err := request(t, n, method, map[string]any{"session_id": sidA, "text": "synthetic report", "source_permission": proof, "from_mode": "bypass"})
		head := envelopeHead(t, inbox, i)
		if method == "session.send" {
			if refusalCode(err) != peerHeld || strings.Contains(head, "from-mode") {
				t.Fatalf("raw params: %s %v", head, err)
			}
		} else if err != nil || !strings.Contains(head, `from-mode="bypass"`) {
			t.Fatalf("attested send: %s %v", head, err)
		}

	}
	content, _ := inbox.users()[1]["message"].(map[string]any)["content"].(string)
	if strings.Contains(content, "用户本人") || !strings.Contains(content, "synthetic report") {
		t.Fatalf("source text impersonates owner: %s", content)
	}
	proof.Class = peerModePrompting
	_, err := request(t, n, nodeproto.SourceSend, map[string]any{"session_id": sidA, "text": "different source class", "source_permission": proof})
	if refusalCode(err) != peerHeld || !strings.Contains(envelopeHead(t, inbox, 2), `from-mode="prompting"`) {
		t.Fatalf("class chosen to suit recipient: %v", err)
	}
}

func TestSourcePermissionSocketRebuildsCallerClaims(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("no verified socket peer PID")
	}
	if sourceGeneration(os.Getpid()) == "" {
		t.Skip("peer process generation unavailable")
	}
	n, hub := runtimeNode(t)
	p, _ := fakeAssistant(n, sidA, "default")
	p.cmd = &exec.Cmd{Process: &os.Process{Pid: os.Getpid()}}
	p.born = time.Now()
	ask := mcpRelay(t, n, hub)
	args := json.RawMessage(`{"text":"a < b & c","from_mode":"bypass"}`)
	req := map[string]any{"kind": "mcp.call", "name": "agent_send", "session_id": sidA, "args": args, "SOURCE_PERMISSION": map[string]any{"class": "bypass"}, "from_mode": "bypass"}
	got := ask(req)
	raw, _ := json.Marshal(got["source_permission"])
	var proof proto.SourcePermission
	if err := json.Unmarshal(raw, &proof); err != nil || proof.Class != "prompting" || !proof.Matches(sidA, "agent_send", args) {
		t.Fatalf("node did not rebuild source: %v %v", got, err)
	}
	p.mu.Lock()
	p.mode = "plan"
	p.mu.Unlock()
	if got = ask(req); got["source_permission"] != nil {
		t.Fatalf("claimed bypass in unknown mode: %v", got)
	}
	p.mu.Lock()
	p.mode = proto.ModeBypass
	p.mu.Unlock()
	req["session_id"] = sidB
	if got = ask(req); got["source_permission"] != nil {
		t.Fatalf("claimed another session: %v", got)
	}
}

func TestSourceGenerationIsStableAndRejectsMissingProcesses(t *testing.T) {
	mark := sourceGeneration(os.Getpid())
	if mark == "" {
		t.Skip("no process generation on this platform")
	}
	if mark != sourceGeneration(os.Getpid()) || sourceGeneration(999999) != "" {
		t.Fatalf("process generation %q", mark)
	}
}
